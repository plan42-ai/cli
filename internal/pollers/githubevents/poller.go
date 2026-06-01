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
	"github.com/plan42-ai/github-event-handlers/handlers"
)

const (
	defaultPollInterval = 60 * time.Second
	maxPhasingJitter    = 10 * time.Second
	maxEventsPerPoll    = 300
	eventsPageSize      = 100
)

// Config holds the dependencies for the github events poller.
type Config struct {
	Registry    *handlers.HandlerRegistry
	Checkpoints *CheckpointStore
	WorkerCount int // default 100
}

// pairState tracks a running per-pair polling goroutine.
type pairState struct {
	cancel context.CancelFunc
	info   ConnectionInfo
}

// Poller owns one polling goroutine per (GithubConnectionID, OrgName) pair.
// It collects events from the GitHub Events API and sends them to an event
// channel. A separate Dispatcher reads from the channel and invokes handlers.
type Poller struct {
	checkpoints *CheckpointStore
	workerCount int

	// cg owns the context shared by the dispatcher workers. Cancelling it
	// aborts in-flight handler calls (used by Close for forced shutdown).
	cg *concurrency.ContextGroup

	// stopCh is closed to signal polling goroutines to stop producing.
	// Graceful shutdown closes stopCh without cancelling cg so workers
	// drain with a live context.
	stopCh   chan struct{}
	stopOnce sync.Once

	// eventCh carries events from polling goroutines to the dispatcher.
	eventCh    chan handlers.Event
	dispatcher *Dispatcher

	// pollerWg tracks polling goroutines so we can wait for them before
	// closing the event channel.
	pollerWg     sync.WaitGroup
	shutdownOnce sync.Once
	shutdownErr  error

	// mu guards the pairs map.
	mu    sync.Mutex
	pairs map[CheckpointKey]*pairState
}

// New creates a fully initialized Poller with its dispatcher worker pool
// running. There is no separate Start method.
func New(cfg Config) *Poller {
	wc := cfg.WorkerCount
	if wc <= 0 {
		wc = defaultWorkerCount
	}
	cg := concurrency.NewContextGroup()
	eventCh := make(chan handlers.Event, 2*wc)
	return &Poller{
		checkpoints: cfg.Checkpoints,
		workerCount: wc,
		cg:          cg,
		stopCh:      make(chan struct{}),
		eventCh:     eventCh,
		dispatcher:  NewDispatcher(cfg.Registry, cg, eventCh, wc),
		pairs:       make(map[CheckpointKey]*pairState),
	}
}

// EventCh returns the channel that polling goroutines send events to and the
// dispatcher reads from. Exposed for testing.
func (p *Poller) EventCh() <-chan handlers.Event { return p.eventCh }

// ShutdownContext performs a graceful shutdown: it stops all polling goroutines,
// then lets workers drain the event channel with a live context so in-flight
// handler calls can complete their API/database work. Finally it flushes
// checkpoints. Safe to call multiple times.
func (p *Poller) ShutdownContext(ctx context.Context) error {
	p.shutdownOnce.Do(func() {
		// Step 1: Signal polling goroutines to stop (without cancelling
		// the worker context so handlers keep a live context).
		p.stopPollers()

		// Step 2: Wait for all polling goroutines to return.
		p.pollerWg.Wait()

		// Step 3: Close the event channel (safe: all producers have exited).
		close(p.eventCh)

		// Step 4: Wait for dispatcher workers to drain and exit.
		if err := p.dispatcher.Wait(ctx); err != nil {
			p.shutdownErr = err
			return
		}

		// Step 5: Cancel the parent context (no-op if Close already did it).
		p.cg.Cancel()

		// Step 6: Flush checkpoints.
		p.shutdownErr = p.checkpoints.Shutdown(ctx)
	})
	return p.shutdownErr
}

// Close performs a forced shutdown: it cancels the context immediately so
// in-flight handler calls are aborted, then runs the same drain sequence as
// ShutdownContext. Use Close when you need to bound shutdown time.
func (p *Poller) Close() error {
	p.cg.Cancel()
	return p.ShutdownContext(context.Background())
}

// stopPollers signals all polling goroutines to stop without cancelling the
// worker context.
func (p *Poller) stopPollers() {
	p.stopOnce.Do(func() { close(p.stopCh) })
}

// UpdateTargets starts goroutines for new pairs and stops goroutines for removed
// pairs. It satisfies the environments.EventPoller interface.
func (p *Poller) UpdateTargets(desired map[CheckpointKey]ConnectionInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Stop goroutines for pairs no longer in the desired set.
	for key, state := range p.pairs {
		if _, ok := desired[key]; !ok {
			slog.Info("github events poller: stopping pair",
				"connection", key.GithubConnectionID, "org", key.OrgName)
			state.cancel()
			delete(p.pairs, key)
			p.checkpoints.Delete(key)
		}
	}

	// Start goroutines for new pairs.
	for key, info := range desired {
		if _, ok := p.pairs[key]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(p.cg.Context())
		p.pairs[key] = &pairState{cancel: cancel, info: info}
		p.pollerWg.Add(1)
		go p.pollPair(ctx, key, info)
		slog.Info("github events poller: starting pair",
			"connection", key.GithubConnectionID, "org", key.OrgName)
	}
}

// pollPair is the per-pair polling goroutine.
func (p *Poller) pollPair(ctx context.Context, key CheckpointKey, info ConnectionInfo) {
	defer p.pollerWg.Done()

	// Phase jitter: sleep a random duration in [0, 10s] before the first poll.
	//nolint:gosec // Cryptographic randomness not needed for jitter.
	jitter := time.Duration(rand.Int64N(int64(maxPhasingJitter)))
	if err := p.sleepOrStop(ctx, jitter); err != nil {
		return
	}

	// Build a go-github client for making Events API requests.
	ghClient, err := newEventsClient(info)
	if err != nil {
		slog.Error("github events poller: failed to create github client",
			"connection", key.GithubConnectionID, "org", key.OrgName, "error", err)
		return
	}

	for {
		p.doPoll(ctx, key, info, ghClient)
		if p.isStopped() || ctx.Err() != nil {
			return
		}

		interval := p.getPollInterval(key)
		if err := p.sleepOrStop(ctx, interval); err != nil {
			return
		}
	}
}

// isStopped returns true if stopCh has been closed.
func (p *Poller) isStopped() bool {
	select {
	case <-p.stopCh:
		return true
	default:
		return false
	}
}

// sleepOrStop sleeps for the given duration, returning early if the context
// is cancelled, the per-pair context is cancelled, or stopCh is closed.
func (p *Poller) sleepOrStop(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		if p.isStopped() {
			return context.Canceled
		}
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.stopCh:
		return context.Canceled
	}
}

// doPoll performs one iteration of the polling loop for a pair.
func (p *Poller) doPoll(ctx context.Context, key CheckpointKey, info ConnectionInfo, ghClient *github.Client) {
	cp, _ := p.checkpoints.Get(key)

	// Build the request manually to set If-None-Match.
	reqURL := fmt.Sprintf("users/%s/events/orgs/%s", info.User, key.OrgName)
	u := addListOptions(reqURL, &github.ListOptions{PerPage: eventsPageSize, Page: 1})

	req, err := ghClient.NewRequest("GET", u, nil)
	if err != nil {
		slog.Error("github events poller: failed to create request", "error", err)
		return
	}
	if cp.ETag != "" {
		req.Header.Set("If-None-Match", cp.ETag)
	}

	var events []*github.Event
	resp, err := ghClient.Do(ctx, req, &events)

	// go-github returns a non-nil error for 304 (CheckResponse treats non-2xx
	// as errors), but the response is still populated. Handle 304 before the
	// generic error path so we correctly update pollInterval.
	if resp != nil && resp.StatusCode == http.StatusNotModified {
		pollInterval := parsePollInterval(resp)
		slog.Info("No new events found.",
			"connection", key.GithubConnectionID, "org", key.OrgName,
			"pollInterval", pollInterval)
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
	newETag := resp.Header.Get("ETag")

	// Collect all new events across pages before dispatching. The Events API
	// returns newest-first; we reverse before dispatch so handlers see events
	// in chronological order.
	var firstEventID string
	if len(events) > 0 {
		firstEventID = events[0].GetID()
	}
	collected, hitCheckpoint := collectNewEvents(events, cp.LastEventID)
	totalFetched := len(collected)

	for page := 2; !hitCheckpoint && len(events) > 0 && totalFetched < maxEventsPerPoll; page++ {
		pageEvents, _, pageErr := fetchPage(ctx, ghClient, info.User, key.OrgName, page)
		if pageErr != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("github events poller: pagination failed",
				"connection", key.GithubConnectionID, "org", key.OrgName,
				"page", page, "error", pageErr)
			// Don't advance checkpoint; re-fetch on the next poll.
			return
		}
		if len(pageEvents) == 0 {
			break
		}
		pageNew, hit := collectNewEvents(pageEvents, cp.LastEventID)
		collected = append(collected, pageNew...)
		totalFetched += len(pageNew)
		hitCheckpoint = hit
	}

	// Dispatch events oldest-first so handlers see chronological order.
	for i := len(collected) - 1; i >= 0; i-- {
		sharedEvt, parseErr := handlers.ParseEventsAPI(collected[i])
		if parseErr != nil {
			slog.Error("github events poller: failed to parse payload",
				"eventID", collected[i].GetID(), "type", collected[i].GetType(), "error", parseErr)
			continue
		}
		if sharedEvt == nil {
			continue
		}
		if err := p.enqueue(ctx, sharedEvt); err != nil {
			return
		}
	}

	if firstEventID == "" && cp.LastEventID != "" {
		firstEventID = cp.LastEventID
	}
	newCp := Checkpoint{
		LastEventID:      firstEventID,
		ETag:             newETag,
		PollIntervalSecs: int(pollInterval.Seconds()),
	}
	p.checkpoints.Set(key, newCp)

	slog.Info("github events poller: poll complete",
		"connection", key.GithubConnectionID, "org", key.OrgName,
		"newEvents", len(collected),
		"pollInterval", pollInterval)
}

// collectNewEvents walks a page of events and returns those that are newer than
// lastEventID. The returned slice is in the same (newest-first) order as the
// input. hitCheckpoint is true if lastEventID was found.
func collectNewEvents(events []*github.Event, lastEventID string) (newEvents []*github.Event, hitCheckpoint bool) {
	for _, evt := range events {
		if evt.GetID() == lastEventID && lastEventID != "" {
			hitCheckpoint = true
			return
		}
		newEvents = append(newEvents, evt)
	}
	return
}

// enqueue sends an event to the event channel, respecting cancellation
// and the stop signal.
func (p *Poller) enqueue(ctx context.Context, evt handlers.Event) error {
	select {
	case p.eventCh <- evt:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.stopCh:
		return context.Canceled
	}
}

// fetchPage fetches a single page of events from the GitHub Events API.
func fetchPage(ctx context.Context, ghClient *github.Client, user, org string, page int) ([]*github.Event, *github.Response, error) {
	reqURL := fmt.Sprintf("users/%s/events/orgs/%s", user, org)
	u := addListOptions(reqURL, &github.ListOptions{PerPage: eventsPageSize, Page: page})
	req, err := ghClient.NewRequest("GET", u, nil)
	if err != nil {
		return nil, nil, err
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

// newEventsClient builds a go-github client for polling the Events API.
func newEventsClient(info ConnectionInfo) (*github.Client, error) {
	httpClient := &http.Client{
		Transport: &tokenTransport{
			token:   info.Token,
			wrapped: http.DefaultTransport,
		},
	}
	gh := github.NewClient(httpClient)
	if info.BaseURL != "" && info.BaseURL != "https://api.github.com" {
		var err error
		gh, err = gh.WithEnterpriseURLs(info.BaseURL, info.BaseURL)
		if err != nil {
			return nil, err
		}
	}
	return gh, nil
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

// PairKeys returns a snapshot of the current running pair keys (for testing).
func (p *Poller) PairKeys() []CheckpointKey {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := make([]CheckpointKey, 0, len(p.pairs))
	for k := range p.pairs {
		keys = append(keys, k)
	}
	return keys
}
