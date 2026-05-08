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
	githubeventslib "github.com/plan42-ai/github-event-handlers"
	"github.com/plan42-ai/github-event-handlers/githubclient"
)

const (
	defaultWorkerCount  = 100
	defaultPollInterval = 60 * time.Second
	maxPhasingJitter    = 10 * time.Second
	maxEventsPerPoll    = 300
	eventsPageSize      = 100
)

// Config holds the dependencies for the github events poller.
type Config struct {
	Registry      *githubeventslib.HandlerRegistry
	Checkpoints   *CheckpointStore
	WorkerCount   int // default 100
	ChannelBuffer int // default 2 * WorkerCount
}

// pairState tracks a running per-pair polling goroutine.
type pairState struct {
	cancel context.CancelFunc
	info   ConnectionInfo
}

// Poller implements the github events poller. It owns one polling goroutine per
// (GithubConnectionID, OrgName) pair, a dispatch channel, and a worker pool.
type Poller struct {
	registry    *githubeventslib.HandlerRegistry
	checkpoints *CheckpointStore
	workerCount int
	chanBuf     int

	// Parent context group for the component's lifecycle.
	cg *concurrency.ContextGroup

	// Dispatch channel for events flowing from pollers to workers.
	dispatchCh chan githubeventslib.Event

	// workerCg tracks worker goroutines separately so shutdown can close
	// the channel between producers exiting and workers draining.
	workerCg *concurrency.ContextGroup

	// pollerWg tracks polling goroutines so we can wait for them before
	// closing the dispatch channel.
	pollerWg     sync.WaitGroup
	shutdownOnce sync.Once
	shutdownErr  error

	// mu guards the pairs map.
	mu    sync.Mutex
	pairs map[CheckpointKey]*pairState
}

// New creates a new github events poller. Call Start to launch the worker pool.
func New(cfg Config) *Poller {
	wc := cfg.WorkerCount
	if wc <= 0 {
		wc = defaultWorkerCount
	}
	cb := cfg.ChannelBuffer
	if cb <= 0 {
		cb = 2 * wc
	}
	return &Poller{
		registry:    cfg.Registry,
		checkpoints: cfg.Checkpoints,
		workerCount: wc,
		chanBuf:     cb,
		cg:          concurrency.NewContextGroup(),
		workerCg:    concurrency.NewContextGroup(),
		pairs:       make(map[CheckpointKey]*pairState),
	}
}

// Start launches the worker pool. Must be called before Reconcile.
func (p *Poller) Start() {
	p.dispatchCh = make(chan githubeventslib.Event, p.chanBuf)
	for range p.workerCount {
		p.workerCg.Add(1)
		go p.worker()
	}
}

// ShutdownContext stops all polling goroutines, drains the dispatch channel
// through the worker pool, and flushes checkpoints. Safe to call multiple times.
func (p *Poller) ShutdownContext(ctx context.Context) error {
	p.shutdownOnce.Do(func() {
		// Step 1: Cancel the parent context so all polling goroutines see ctx.Done().
		p.cg.Cancel()

		// Step 2: Wait for all polling goroutines to return.
		p.pollerWg.Wait()

		// Step 3: Close the dispatch channel (safe: all producers have exited).
		close(p.dispatchCh)

		// Step 4: Wait for workers to drain and exit.
		if err := p.workerCg.WaitContext(ctx); err != nil {
			p.shutdownErr = err
			return
		}

		// Step 5: Flush checkpoints.
		p.shutdownErr = p.checkpoints.Flush(ctx)
	})
	return p.shutdownErr
}

// Reconcile starts goroutines for new pairs and stops goroutines for removed
// pairs. It satisfies the Reconciler interface.
func (p *Poller) Reconcile(desired map[CheckpointKey]ConnectionInfo) {
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

// worker is the dispatch worker loop. Each worker reads events from dispatchCh
// and calls HandlerRegistry.Handle. The context passed to handlers derives from
// the component's parent context (not per-pair contexts).
func (p *Poller) worker() {
	defer p.workerCg.Done()
	ctx := p.cg.Context()
	for evt := range p.dispatchCh {
		if err := p.registry.Handle(ctx, evt, (*githubclient.GithubClient)(nil)); err != nil {
			slog.ErrorContext(ctx, "github events poller: handler error",
				"deliveryID", evt.GetDeliveryID(),
				"eventType", evt.EventType(),
				"error", err,
			)
		}
	}
}

// pollPair is the per-pair polling goroutine.
func (p *Poller) pollPair(ctx context.Context, key CheckpointKey, info ConnectionInfo) {
	defer p.pollerWg.Done()

	// Phase jitter: sleep a random duration in [0, 10s] before the first poll.
	//nolint:gosec // Cryptographic randomness not needed for jitter.
	jitter := time.Duration(rand.Int64N(int64(maxPhasingJitter)))
	if err := sleepCtx(ctx, jitter); err != nil {
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
		if ctx.Err() != nil {
			return
		}

		interval := p.getPollInterval(key)
		if err := sleepCtx(ctx, interval); err != nil {
			return
		}
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
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Error("github events poller: poll failed",
			"connection", key.GithubConnectionID, "org", key.OrgName, "error", err)
		return
	}

	pollInterval := parsePollInterval(resp)

	if resp.StatusCode == http.StatusNotModified {
		slog.Debug("github events poller: 304 Not Modified",
			"connection", key.GithubConnectionID, "org", key.OrgName,
			"pollInterval", pollInterval)
		cp.PollIntervalSecs = int(pollInterval.Seconds())
		p.checkpoints.Set(key, cp)
		return
	}

	newETag := resp.Header.Get("ETag")
	var firstEventID string
	totalProcessed := 0
	hitCheckpoint := false

	firstEventID, totalProcessed, hitCheckpoint = p.processPage(ctx, key, events, cp.LastEventID)
	if ctx.Err() != nil {
		return
	}

	// Paginate if the first page didn't reach the checkpoint.
	for page := 2; !hitCheckpoint && len(events) > 0 && totalProcessed < maxEventsPerPoll; page++ {
		pageEvents, _, pageErr := p.fetchPage(ctx, ghClient, info.User, key.OrgName, page)
		if pageErr != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("github events poller: pagination failed",
				"connection", key.GithubConnectionID, "org", key.OrgName,
				"page", page, "error", pageErr)
			break
		}
		if len(pageEvents) == 0 {
			break
		}
		_, n, hit := p.processPage(ctx, key, pageEvents, cp.LastEventID)
		if ctx.Err() != nil {
			return
		}
		totalProcessed += n
		hitCheckpoint = hit
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
		"newEvents", totalProcessed,
		"pollInterval", pollInterval)
}

// processPage processes a single page of events. It returns the first event's
// ID (from the first call for page 1), the count of events processed, and
// whether the checkpoint event ID was hit.
func (p *Poller) processPage(ctx context.Context, _ CheckpointKey, events []*github.Event, lastEventID string) (firstID string, count int, hitCheckpoint bool) {
	for i, evt := range events {
		if evt.GetID() == lastEventID && lastEventID != "" {
			hitCheckpoint = true
			return
		}
		if i == 0 {
			firstID = evt.GetID()
		}

		payload, parseErr := evt.ParsePayload()
		if parseErr != nil {
			slog.Error("github events poller: failed to parse payload",
				"eventID", evt.GetID(), "type", evt.GetType(), "error", parseErr)
			continue
		}

		var sharedEvt githubeventslib.Event
		switch p := payload.(type) {
		case *github.IssueCommentEvent:
			sharedEvt = translateIssueComment(evt, p)
		case *github.PullRequestReviewCommentEvent:
			sharedEvt = translatePullRequestReviewComment(evt, p)
		default:
			// Event types the runner does not handle yet.
			continue
		}

		slog.Debug("github events poller: dispatching event",
			"deliveryID", sharedEvt.GetDeliveryID(),
			"eventType", sharedEvt.EventType(),
			"eventID", evt.GetID(),
		)
		if err := p.enqueue(ctx, sharedEvt); err != nil {
			return
		}

		count++
	}
	return
}

// enqueue sends an event to the dispatch channel, respecting cancellation.
func (p *Poller) enqueue(ctx context.Context, evt githubeventslib.Event) error {
	select {
	case p.dispatchCh <- evt:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fetchPage fetches a single page of events from the GitHub Events API.
func (p *Poller) fetchPage(ctx context.Context, ghClient *github.Client, user, org string, page int) ([]*github.Event, *github.Response, error) {
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

// WorkerCount returns the configured worker count (for testing).
func (p *Poller) WorkerCount() int { return p.workerCount }

// ChannelBuffer returns the configured channel buffer size (for testing).
func (p *Poller) ChannelBuffer() int { return p.chanBuf }

// DispatchCh returns the dispatch channel (for testing).
func (p *Poller) DispatchCh() chan githubeventslib.Event { return p.dispatchCh }

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
