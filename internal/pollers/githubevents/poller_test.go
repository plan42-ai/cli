package githubevents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v81/github"
	ghapi "github.com/plan42-ai/github-event-handlers/github"
	"github.com/plan42-ai/github-event-handlers/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testOrg2       = "org2"
	testEventType  = "test_event"
	testEventsPath = "users/u/events"
)

// testCheckpointStore creates a CheckpointStore backed by a temp directory.
func testCheckpointStore(t *testing.T) *CheckpointStore {
	t.Helper()
	store, _ := newTestStore(t)
	return store
}

func TestDefaultWorkerCount(t *testing.T) {
	store := testCheckpointStore(t)
	p := New(Config{
		Registry:    handlers.NewHandlerRegistry(handlers.Config{}),
		Checkpoints: store,
	})

	assert.Equal(t, 100, p.workerCount, "default worker count should be 100")
}

func TestCustomWorkerCount(t *testing.T) {
	store := testCheckpointStore(t)
	p := New(Config{
		Registry:    handlers.NewHandlerRegistry(handlers.Config{}),
		Checkpoints: store,
		WorkerCount: 50,
	})

	assert.Equal(t, 50, p.workerCount)
}

func TestUpdateTargetsStartsGoroutinesForNewPairs(t *testing.T) {
	store := testCheckpointStore(t)
	p := newTestPoller(t, store)

	key1 := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}
	key2 := CheckpointKey{GithubConnectionID: "c2", OrgName: testOrg2}

	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key1: {Token: "t1", User: "u1"},
		key2: {Token: "t2", User: "u2"},
	})

	keys := p.PairKeys()
	assert.Len(t, keys, 2, "should have 2 running pairs")
	assert.Contains(t, keys, key1)
	assert.Contains(t, keys, key2)
}

func TestUpdateTargetsStopsGoroutinesForRemovedPairs(t *testing.T) {
	store := testCheckpointStore(t)
	p := newTestPoller(t, store)

	key1 := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}
	key2 := CheckpointKey{GithubConnectionID: "c2", OrgName: testOrg2}

	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key1: {Token: "t1", User: "u1"},
		key2: {Token: "t2", User: "u2"},
	})

	assert.Len(t, p.PairKeys(), 2)

	// Remove key2.
	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key1: {Token: "t1", User: "u1"},
	})

	keys := p.PairKeys()
	assert.Len(t, keys, 1, "should have 1 running pair after removing key2")
	assert.Contains(t, keys, key1)
}

func TestUpdateTargetsDeletesCheckpointForRemovedPair(t *testing.T) {
	store := testCheckpointStore(t)
	p := newTestPoller(t, store)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}

	// Pre-seed a checkpoint.
	store.Set(key, Checkpoint{LastEventID: "42"})

	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key: {Token: "t1", User: "u1"},
	})

	// Remove the pair.
	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{})

	_, ok := store.Get(key)
	assert.False(t, ok, "checkpoint should be deleted when pair is removed")
}

func TestNoGoroutineLeaksAfterUpdateTargetsRemovesPair(t *testing.T) {
	store := testCheckpointStore(t)
	p := newTestPoller(t, store)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}

	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key: {Token: "t1", User: "u1"},
	})

	// Remove the pair.
	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{})

	// Shutdown should complete without timeout (no leaked goroutines).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p.ShutdownContext(ctx)
	assert.NoError(t, err)
}

func TestDispatcherWorkersStartAndStop(t *testing.T) {
	store := testCheckpointStore(t)
	registry := handlers.NewHandlerRegistry(handlers.Config{})

	var handleCount atomic.Int64
	registry.Register(testEventType, func(_ context.Context, _ handlers.Event, _ ghapi.API) {
		handleCount.Add(1)
	})

	p := New(Config{
		Registry:    registry,
		Checkpoints: store,
		WorkerCount: 3,
	})
	// Send events through the dispatch channel directly.
	for range 5 {
		p.eventCh <- &testEvent{evtType: testEventType, delivery: "d1"}
	}

	// Shut down and wait for workers to drain.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p.ShutdownContext(ctx)
	require.NoError(t, err)

	assert.Equal(t, int64(5), handleCount.Load(), "all 5 events should have been handled")
}

func TestBackpressureBlocksPollerButRespectsCancel(t *testing.T) {
	store := testCheckpointStore(t)

	// Create a slow handler that blocks until cancelled.
	registry := handlers.NewHandlerRegistry(handlers.Config{})
	registry.Register(testEventType, func(ctx context.Context, _ handlers.Event, _ ghapi.API) {
		<-ctx.Done()
	})

	p := New(Config{
		Registry:    registry,
		Checkpoints: store,
		WorkerCount: 1,
	})

	// Fill the worker (1 event being processed) and the buffer (2 events buffered).
	p.eventCh <- &testEvent{evtType: testEventType, delivery: "d1"}
	p.eventCh <- &testEvent{evtType: testEventType, delivery: "d2"}
	p.eventCh <- &testEvent{evtType: testEventType, delivery: "d2b"}

	// Now the channel is full. enqueue should block but respect cancellation.
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- p.enqueue(ctx, &testEvent{evtType: testEventType, delivery: "d3"})
	}()

	// The enqueue should be blocked.
	select {
	case <-done:
		t.Fatal("enqueue returned immediately; expected it to block")
	case <-time.After(50 * time.Millisecond):
		// Good, it's blocked.
	}

	// Cancel the context; enqueue should return.
	cancel()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue did not return after context cancel")
	}

	// Use Close (forced shutdown) since the handler blocks on ctx.Done().
	_ = p.Close()
}

func TestShutdownDrainsChannelBeforeReturning(t *testing.T) {
	store := testCheckpointStore(t)

	var mu sync.Mutex
	var events []string

	registry := handlers.NewHandlerRegistry(handlers.Config{})
	registry.Register(testEventType, func(_ context.Context, evt handlers.Event, _ ghapi.API) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, evt.GetDeliveryID())
	})

	p := New(Config{
		Registry:    registry,
		Checkpoints: store,
		WorkerCount: 2,
	})

	// Enqueue some events.
	for i := range 5 {
		p.eventCh <- &testEvent{
			evtType:  testEventType,
			delivery: string(rune('A' + i)),
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p.ShutdownContext(ctx)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, events, 5, "all events should be drained by workers before shutdown completes")
}

func TestSleepCtxRespectsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := sleepCtx(ctx, 10*time.Second)
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, time.Second)
}

func TestSleepCtxSleepsForDuration(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	err := sleepCtx(ctx, 50*time.Millisecond)
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
}

func TestParsePollInterval(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected time.Duration
	}{
		{"valid", "90", 90 * time.Second},
		{"empty", "", defaultPollInterval},
		{"invalid", "abc", defaultPollInterval},
		{"zero", "0", defaultPollInterval},
		{"negative", "-1", defaultPollInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &github.Response{
				Response: &http.Response{
					Header: http.Header{},
				},
			}
			if tt.header != "" {
				resp.Header.Set("X-Poll-Interval", tt.header)
			}
			assert.Equal(t, tt.expected, parsePollInterval(resp))
		})
	}
}

func TestParsePollIntervalNilResponse(t *testing.T) {
	assert.Equal(t, defaultPollInterval, parsePollInterval(nil))
}

func TestPhasingJitterAppliedBeforeFirstPoll(t *testing.T) {
	var mu sync.Mutex
	var requestTimes []time.Time

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requestTimes = append(requestTimes, time.Now())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer ts.Close()

	store := testCheckpointStore(t)
	registry := handlers.NewHandlerRegistry(handlers.Config{})

	p := New(Config{
		Registry:    registry,
		Checkpoints: store,
		WorkerCount: 1,
	})

	start := time.Now()

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}
	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key: {Token: "test-token", User: "testuser", BaseURL: ts.URL},
	})

	// Wait a bit for the phasing jitter (up to 10s) and first request.
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.ShutdownContext(ctx)

	mu.Lock()
	defer mu.Unlock()

	for _, rt := range requestTimes {
		assert.True(t, rt.After(start) || rt.Equal(start))
	}
}

func TestUpdateTargetsDoesNotBlockOnCancel(t *testing.T) {
	store := testCheckpointStore(t)
	p := newTestPoller(t, store)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}

	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key: {Token: "t1", User: "u1"},
	})

	done := make(chan struct{})
	go func() {
		p.UpdateTargets(map[CheckpointKey]ConnectionInfo{})
		close(done)
	}()

	select {
	case <-done:
		// Good.
	case <-time.After(2 * time.Second):
		t.Fatal("UpdateTargets blocked when removing a pair")
	}
}

func TestAddListOptions(t *testing.T) {
	tests := []struct {
		name string
		base string
		opts *github.ListOptions
		want string
	}{
		{"nil opts", testEventsPath, nil, testEventsPath},
		{"page and perPage", testEventsPath, &github.ListOptions{Page: 2, PerPage: 100}, "users/u/events?page=2&per_page=100"},
		{"page only", testEventsPath, &github.ListOptions{Page: 3}, "users/u/events?page=3"},
		{"perPage only", testEventsPath, &github.ListOptions{PerPage: 50}, "users/u/events?per_page=50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addListOptions(tt.base, tt.opts)
			assert.Equal(t, tt.want, got)
		})
	}
}

// testEvent is a minimal Event implementation for tests.
type testEvent struct {
	evtType  string
	delivery string
}

func (e *testEvent) EventType() string     { return e.evtType }
func (e *testEvent) GetDeliveryID() string { return e.delivery }

// newTestPoller creates a started Poller with cleanup.
func newTestPoller(t *testing.T, store *CheckpointStore) *Poller {
	t.Helper()
	p := New(Config{
		Registry:    handlers.NewHandlerRegistry(handlers.Config{}),
		Checkpoints: store,
		WorkerCount: 1,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.ShutdownContext(ctx)
	})
	return p
}

func TestCollectNewEventsStopsAtCheckpoint(t *testing.T) {
	events := []*github.Event{
		{ID: github.Ptr("5")},
		{ID: github.Ptr("4")},
		{ID: github.Ptr("3")},
		{ID: github.Ptr("2")},
		{ID: github.Ptr("1")},
	}

	collected, hit := collectNewEvents(events, "3")
	require.True(t, hit, "should hit checkpoint")
	require.Len(t, collected, 2)
	assert.Equal(t, "5", collected[0].GetID())
	assert.Equal(t, "4", collected[1].GetID())
}

func TestCollectNewEventsNoCheckpoint(t *testing.T) {
	events := []*github.Event{
		{ID: github.Ptr("3")},
		{ID: github.Ptr("2")},
		{ID: github.Ptr("1")},
	}

	collected, hit := collectNewEvents(events, "")
	require.False(t, hit)
	require.Len(t, collected, 3)
}

func TestCollectNewEventsCheckpointNotFound(t *testing.T) {
	events := []*github.Event{
		{ID: github.Ptr("5")},
		{ID: github.Ptr("4")},
	}

	collected, hit := collectNewEvents(events, "99")
	require.False(t, hit, "checkpoint not in page")
	require.Len(t, collected, 2)
}
