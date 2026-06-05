package githubevents

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v81/github"
	"github.com/plan42-ai/concurrency"
	ghapi "github.com/plan42-ai/github-event-handlers/github"
	"github.com/plan42-ai/github-event-handlers/handlers"
)

const (
	defaultEventCapacity = 200
	defaultPollInterval  = 60 * time.Second
	maxPhasingJitter     = 10 * time.Second
	maxEventsPerPoll     = 300
	eventsPageSize       = 100
)

// Config holds the dependencies for the github events poller.
type Config struct {
	Checkpoints *CheckpointStore
	// EventCapacity is the buffer size of the event channel. Default 200.
	EventCapacity int
}

// target tracks a running polling goroutine for a target github org.
type target struct {
	cancel context.CancelFunc
	// done is closed by the polling goroutine once it has fully exited, so a
	// removed target's checkpoint can be deleted only when no further writes to
	// it are possible.
	done chan struct{}
}

// Poller owns one polling goroutine per (GithubConnectionID, OrgName) pair.
// It collects events from the GitHub Events API and sends them to an event
// channel exposed via EventCh. A separate Dispatcher consumes that channel and
// invokes handlers.
type Poller struct {
	checkpoints  *CheckpointStore
	cg           *concurrency.ContextGroup
	eventCh      chan DispatchItem
	shutdownOnce sync.Once
	shutdownErr  error
	mu           sync.Mutex
	targets      map[CheckpointKey]*target
}

func NewPoller(cfg Config) *Poller {
	capacity := cfg.EventCapacity
	if capacity <= 0 {
		capacity = defaultEventCapacity
	}
	return &Poller{
		checkpoints: cfg.Checkpoints,
		cg:          concurrency.NewContextGroup(),
		eventCh:     make(chan DispatchItem, capacity),
		targets:     make(map[CheckpointKey]*target),
	}
}

// EventCh returns the channel that polling goroutines send events to. A
// Dispatcher consumes it. The channel is closed when the Poller shuts down.
func (p *Poller) EventCh() <-chan DispatchItem { return p.eventCh }

// Close stops all polling goroutines, closes the event channel so the consumer
// can finish draining it, and shuts down the checkpoint store (a final flush).
// It blocks until the polling goroutines have exited. Safe to call multiple
// times.
func (p *Poller) Close() error {
	p.shutdownOnce.Do(func() {
		// Stop polling goroutines by cancelling their contexts, then wait for
		// them to return.
		p.cg.Cancel()
		p.cg.Wait()

		// Close the event channel (safe: the Poller is the sole producer and
		// all polling goroutines have exited).
		close(p.eventCh)

		// Flush checkpoints.
		p.shutdownErr = p.checkpoints.Shutdown(context.Background())
	})
	return p.shutdownErr
}

// UpdateTargets starts goroutines for new targets and stops goroutines for
// removed targets. It satisfies the environments.EventPoller interface.
//
// New targets start polling in the same pass that cancels the removed ones, so
// they don't have to wait for the old goroutines to exit. The second pass then
// waits for each removed target to exit and deletes its checkpoint. Both passes
// run under p.mu, which is what makes the checkpoint delete safe: a cancelled
// target can no longer write its checkpoint once done is closed, and no
// concurrent UpdateTargets can re-add it between the wait and the delete. The
// lock order is always Poller -> Checkpoints (a polling goroutine never takes
// p.mu), so waiting under the lock can't deadlock.
func (p *Poller) UpdateTargets(desired map[CheckpointKey]ConnectionInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	slog.InfoContext(p.cg.Context(), "github events poller: UpdateTargets", "n", len(desired))

	p.cancelRemovedTargetsAndStartNewOnes(desired)
	p.waitForRemovedTargets(desired)
}

// cancelRemovedTargetsAndStartNewOnes cancels every running target not in
// desired (leaving it registered for waitForRemovedTargets to reap) and starts
// a goroutine for every desired target not already running. Must be called with
// p.mu held.
func (p *Poller) cancelRemovedTargetsAndStartNewOnes(desired map[CheckpointKey]ConnectionInfo) {
	for key, tgt := range p.targets {
		if _, ok := desired[key]; !ok {
			slog.Info("github events poller: stopping target",
				"connection", key.GithubConnectionID, "org", key.OrgName)
			tgt.cancel()
		}
	}

	for key, info := range desired {
		if _, ok := p.targets[key]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(p.cg.Context())
		done := make(chan struct{})
		p.targets[key] = &target{cancel: cancel, done: done}
		p.cg.Add(1)
		go p.pollTarget(ctx, key, info, done)
		slog.Info("github events poller: starting target",
			"connection", key.GithubConnectionID, "org", key.OrgName)
	}
}

// waitForRemovedTargets waits for every cancelled target (those no longer in
// desired) to exit, then deletes its checkpoint and unregisters it. Must be
// called with p.mu held.
func (p *Poller) waitForRemovedTargets(desired map[CheckpointKey]ConnectionInfo) {
	for key, tgt := range p.targets {
		if _, ok := desired[key]; ok {
			continue
		}
		<-tgt.done
		p.checkpoints.Delete(key)
		delete(p.targets, key)
	}
}

func (p *Poller) pollTarget(ctx context.Context, key CheckpointKey, info ConnectionInfo, done chan struct{}) {
	defer p.cg.Done()
	defer close(done)

	slog.InfoContext(ctx, "starting to poll github org", "org", key.OrgName, "connection", key.GithubConnectionID, "baseURL", info.BaseURL)
	defer slog.InfoContext(ctx, "done polling github org", "org", key.OrgName, "connection", key.GithubConnectionID, "baseURL", info.BaseURL)

	// Phase jitter: sleep a random duration in [0, 10s] before the first poll.
	//nolint:gosec // Cryptographic randomness not needed for jitter.
	jitter := time.Duration(rand.Int64N(int64(maxPhasingJitter)))
	if err := sleepCtx(ctx, jitter); err != nil {
		return
	}

	// Build the github-event-handlers client that dispatched events carry for
	// handlers to act on. The go-github client for polling is provided in info.GHClient.
	handlerClient, err := newHandlerClient(info)
	if err != nil {
		slog.Error("github events poller: failed to create handler client",
			"connection", key.GithubConnectionID, "org", key.OrgName, "error", err)
		return
	}

	for {
		p.doPoll(ctx, key, info, info.GHClient, handlerClient)
		if ctx.Err() != nil {
			return
		}

		interval := p.getPollInterval(key)
		if err := sleepCtx(ctx, interval); err != nil {
			return
		}
	}
}

// doPoll performs one iteration of the polling loop for a pair. handlerClient is
// the github client dispatched events carry for handlers to act on.
func (p *Poller) doPoll(ctx context.Context, key CheckpointKey, info ConnectionInfo, ghClient *github.Client, handlerClient *ghapi.Client) {
	cp, hasCheckpoint := p.checkpoints.Get(key)

	// Fetch the first page, sending If-None-Match so an unchanged feed returns 304.
	events, resp, err := fetchEventsPage(ctx, ghClient, info.User, key.OrgName, 1, cp.ETag)

	// go-github returns a non-nil error for 304 (CheckResponse treats non-2xx
	// as errors), but the response is still populated. Handle 304 before the
	// generic error path so we correctly update pollInterval.
	if resp != nil && resp.StatusCode == http.StatusNotModified {
		pollInterval := parsePollInterval(resp)
		slog.Info("No new events found.",
			"connection", key.GithubConnectionID,
			"org", key.OrgName,
			"poll_interval", pollInterval,
		)
		cp.PollIntervalSecs = int(pollInterval.Seconds())
		p.checkpoints.Set(key, cp)
		return
	}

	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Error("github events poller: poll failed",
			"connection", key.GithubConnectionID, "org", key.OrgName, "error", err)
		return
	}

	pollInterval := parsePollInterval(resp)
	// The newest event (first on page 1) becomes the new checkpoint.
	newCp := Checkpoint{
		LastEventID:      newestEventID(events, cp.LastEventID),
		ETag:             resp.Header.Get("ETag"),
		PollIntervalSecs: int(pollInterval.Seconds()),
	}

	// First poll for this target: record a baseline at the newest event and
	// dispatch nothing, so only events that arrive afterward are processed.
	if !hasCheckpoint {
		p.checkpoints.Set(key, newCp)
		return
	}

	// Collect new events (newest-first) across as many pages as needed, stopping
	// at the previous checkpoint, the last page (a short page means no more), or
	// the per-poll cap. The loop condition tests the most recently fetched page.
	selected, hitCheckpoint := selectNewEvents(events, cp.LastEventID)
	for page := 2; !hitCheckpoint && len(events) == eventsPageSize && len(selected) < maxEventsPerPoll; page++ {
		events, _, err = fetchEventsPage(ctx, ghClient, info.User, key.OrgName, page, "")
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("github events poller: pagination failed",
				"connection", key.GithubConnectionID, "org", key.OrgName,
				"page", page, "error", err)
			// Don't advance the checkpoint; re-fetch on the next poll.
			return
		}
		var pageNew []*github.Event
		pageNew, hitCheckpoint = selectNewEvents(events, cp.LastEventID)
		selected = append(selected, pageNew...)
	}

	// If the context is cancelled mid-dispatch, leave the checkpoint so the
	// undelivered events are refetched on the next poll.
	if !p.dispatch(ctx, selected, handlerClient) {
		return
	}
	p.checkpoints.Set(key, newCp)

	slog.Info("github events poller: poll complete",
		"connection", key.GithubConnectionID, "org", key.OrgName,
		"newEvents", len(selected),
		"pollInterval", pollInterval)
}

// newestEventID returns the ID of the newest event on a page (the first one),
// or fallback when the page is empty (so the existing checkpoint is preserved).
func newestEventID(events []*github.Event, fallback string) string {
	if len(events) > 0 {
		return events[0].GetID()
	}
	return fallback
}

// dispatch parses selected events and enqueues them oldest-first, so handlers
// see them in chronological order. Each event is paired with handlerClient, the
// github client for its connection. It returns false if the context was
// cancelled before every event was enqueued, signalling the caller not to
// advance the checkpoint.
func (p *Poller) dispatch(ctx context.Context, selected []*github.Event, handlerClient *ghapi.Client) bool {
	for i := len(selected) - 1; i >= 0; i-- {
		evt, err := handlers.ParseEventsAPI(selected[i])
		if err != nil {
			slog.Error("github events poller: failed to parse payload",
				"eventID", selected[i].GetID(), "type", selected[i].GetType(), "error", err)
			continue
		}
		if evt == nil {
			continue
		}
		if err := p.enqueue(ctx, DispatchItem{event: evt, client: handlerClient}); err != nil {
			return false
		}
	}
	return true
}

// selectNewEvents returns the prefix of events (newest-first) that is newer
// than lastEventID — everything up to but not including the event matching it.
// The result is a sub-slice of events, not a copy. hitCheckpoint is true if
// lastEventID was found on the page.
func selectNewEvents(events []*github.Event, lastEventID string) (newEvents []*github.Event, hitCheckpoint bool) {
	for i, evt := range events {
		if lastEventID != "" && evt.GetID() == lastEventID {
			return events[:i], true
		}
	}
	return events, false
}

// enqueue sends an item to the event channel, respecting cancellation.
func (p *Poller) enqueue(ctx context.Context, item DispatchItem) error {
	select {
	case p.eventCh <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fetchEventsPage fetches one page of the org events feed. etag, when non-empty,
// is sent as If-None-Match so an unchanged feed returns 304; pass it only for
// the first page.
func fetchEventsPage(ctx context.Context, ghClient *github.Client, user, org string, page int, etag string) ([]*github.Event, *github.Response, error) {
	reqURL := fmt.Sprintf("users/%s/events/orgs/%s", user, org)
	u := addListOptions(reqURL, &github.ListOptions{PerPage: eventsPageSize, Page: page})
	req, err := ghClient.NewRequest("GET", u, nil)
	if err != nil {
		return nil, nil, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	var events []*github.Event
	resp, err := ghClient.Do(ctx, req, &events)
	return events, resp, err
}

// getPollInterval returns the poll interval for the given key.
func (p *Poller) getPollInterval(key CheckpointKey) time.Duration {
	cp, ok := p.checkpoints.Get(key)
	if !ok || cp.PollIntervalSecs <= 0 {
		return defaultPollInterval
	}
	return time.Duration(cp.PollIntervalSecs) * time.Second
}

// parsePollInterval extracts x-poll-interval from the response headers.
func parsePollInterval(resp *github.Response) time.Duration {
	if resp == nil || resp.Response == nil {
		return defaultPollInterval
	}
	val := resp.Header.Get("X-Poll-Interval")
	if val == "" {
		return defaultPollInterval
	}
	secs, err := strconv.Atoi(val)
	if err != nil || secs <= 0 {
		return defaultPollInterval
	}
	return time.Duration(secs) * time.Second
}

// sleepCtx sleeps for the given duration, returning early if the context is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isPublicGitHub returns true if baseURL refers to public GitHub (or is empty).
// Runner configs may use "https://github.com" as the URL for public GitHub
// connections, but the go-github client needs "https://api.github.com" (or
// empty) to target the correct API endpoint.
func isPublicGitHub(baseURL string) bool {
	return baseURL == "" || baseURL == "https://api.github.com" || baseURL == "https://github.com"
}

// NewGitHubClient builds a go-github client authenticated with the given token
// and targeting the given base URL. For public GitHub (or empty URL) it targets
// https://api.github.com; otherwise it configures enterprise URLs.
func NewGitHubClient(token, baseURL string) (*github.Client, error) {
	httpClient := &http.Client{
		Transport: &tokenTransport{
			token:   token,
			wrapped: http.DefaultTransport,
		},
	}
	gh := github.NewClient(httpClient)
	if !isPublicGitHub(baseURL) {
		var err error
		gh, err = gh.WithEnterpriseURLs(baseURL, baseURL)
		if err != nil {
			return nil, err
		}
	}
	return gh, nil
}

// newHandlerClient builds the github client that dispatched events carry for
// handlers to act on, authenticated with the connection's token. The token is
// applied by tokenTransport, so no per-call auth context is needed.
func newHandlerClient(info ConnectionInfo) (*ghapi.Client, error) {
	httpClient := &http.Client{
		Transport: &tokenTransport{
			token:   info.Token,
			wrapped: http.DefaultTransport,
		},
	}
	// Normalize "https://github.com" to "" so the shared library's NewClient
	// targets https://api.github.com instead of treating it as Enterprise.
	baseURL := info.BaseURL
	if isPublicGitHub(baseURL) {
		baseURL = ""
	}
	return ghapi.NewClient(httpClient, baseURL)
}

type tokenTransport struct {
	token   string
	wrapped http.RoundTripper
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "token "+t.token)
	return t.wrapped.RoundTrip(req)
}

// addListOptions appends ListOptions query params to the URL.
func addListOptions(s string, opts *github.ListOptions) string {
	if opts == nil {
		return s
	}
	sep := "?"
	if strings.Contains(s, "?") {
		sep = "&"
	}
	var params string
	if opts.Page > 0 {
		params += fmt.Sprintf("page=%d", opts.Page)
	}
	if opts.PerPage > 0 {
		if params != "" {
			params += "&"
		}
		params += fmt.Sprintf("per_page=%d", opts.PerPage)
	}
	if params == "" {
		return s
	}
	return s + sep + params
}

// TargetKeys returns a snapshot of the current running target keys (for testing).
func (p *Poller) TargetKeys() []CheckpointKey {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := make([]CheckpointKey, 0, len(p.targets))
	for k := range p.targets {
		keys = append(keys, k)
	}
	return keys
}
