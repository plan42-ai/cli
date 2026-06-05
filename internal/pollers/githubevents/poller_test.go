package githubevents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v81/github"
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

func TestDefaultEventCapacity(t *testing.T) {
	store := testCheckpointStore(t)
	p := NewPoller(Config{
		Checkpoints: store,
	})

	require.Equal(t, defaultEventCapacity, cap(p.eventCh), "default event capacity should be 200")
}

func TestCustomEventCapacity(t *testing.T) {
	store := testCheckpointStore(t)
	p := NewPoller(Config{
		Checkpoints:   store,
		EventCapacity: 50,
	})

	require.Equal(t, 50, cap(p.eventCh))
}

func TestUpdateTargetsStartsGoroutinesForNewPairs(t *testing.T) {
	store := testCheckpointStore(t)
	p := newTestPoller(t, store)

	key1 := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}
	key2 := CheckpointKey{GithubConnectionID: "c2", OrgName: testOrg2}

	info := stubConnectionInfo(t)
	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key1: info,
		key2: info,
	})

	keys := p.TargetKeys()
	require.Len(t, keys, 2, "should have 2 running pairs")
	require.Contains(t, keys, key1)
	require.Contains(t, keys, key2)
}

func TestUpdateTargetsStopsGoroutinesForRemovedPairs(t *testing.T) {
	store := testCheckpointStore(t)
	p := newTestPoller(t, store)

	key1 := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}
	key2 := CheckpointKey{GithubConnectionID: "c2", OrgName: testOrg2}

	info := stubConnectionInfo(t)
	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key1: info,
		key2: info,
	})

	require.Len(t, p.TargetKeys(), 2)

	// Remove key2.
	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key1: info,
	})

	keys := p.TargetKeys()
	require.Len(t, keys, 1, "should have 1 running pair after removing key2")
	require.Contains(t, keys, key1)
}

func TestUpdateTargetsDeletesCheckpointForRemovedPair(t *testing.T) {
	store := testCheckpointStore(t)
	p := newTestPoller(t, store)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}

	// Pre-seed a checkpoint.
	store.Set(key, Checkpoint{LastEventID: "42"})

	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key: stubConnectionInfo(t),
	})

	// Remove the pair. UpdateTargets waits for the goroutine to exit and deletes
	// the checkpoint synchronously, so it is gone once UpdateTargets returns.
	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{})

	_, ok := store.Get(key)
	require.False(t, ok, "checkpoint should be deleted when the pair is removed")
}

func TestNoGoroutineLeaksAfterUpdateTargetsRemovesPair(t *testing.T) {
	store := testCheckpointStore(t)
	p := newTestPoller(t, store)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}

	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key: stubConnectionInfo(t),
	})

	// Remove the pair.
	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{})

	// Close should complete promptly (no leaked goroutines).
	err := p.Close()
	require.NoError(t, err)
}

func TestEnqueueBlocksWhenFullButRespectsCancel(t *testing.T) {
	store := testCheckpointStore(t)
	p := newTestPoller(t, store)

	// Fill the buffer (capacity 1) so the next enqueue must block.
	p.eventCh <- DispatchItem{event: &testEvent{evtType: testEventType, delivery: "d1"}}

	// Now the channel is full. enqueue should block but respect cancellation.
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- p.enqueue(ctx, DispatchItem{event: &testEvent{evtType: testEventType, delivery: "d2"}})
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
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue did not return after context cancel")
	}
}

func TestSleepCtxRespectsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := sleepCtx(ctx, 10*time.Second)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, elapsed, time.Second)
}

func TestSleepCtxSleepsForDuration(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	err := sleepCtx(ctx, 50*time.Millisecond)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
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
			require.Equal(t, tt.expected, parsePollInterval(resp))
		})
	}
}

func TestParsePollIntervalNilResponse(t *testing.T) {
	require.Equal(t, defaultPollInterval, parsePollInterval(nil))
}

func TestPhasingJitterAppliedBeforeFirstPoll(t *testing.T) {
	var mu sync.Mutex
	var requestTimes []time.Time
	record := func() {
		mu.Lock()
		defer mu.Unlock()
		requestTimes = append(requestTimes, time.Now())
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		record()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer ts.Close()

	store := testCheckpointStore(t)

	p := NewPoller(Config{
		Checkpoints: store,
	})

	start := time.Now()

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}
	ghClient, err := NewGitHubClient("test-token", ts.URL)
	require.NoError(t, err)
	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key: {Token: "test-token", User: "testuser", BaseURL: ts.URL, GHClient: ghClient},
	})

	// Wait a bit for the phasing jitter (up to 10s) and first request.
	time.Sleep(200 * time.Millisecond)

	_ = p.Close()

	mu.Lock()
	defer mu.Unlock()

	for _, rt := range requestTimes {
		require.True(t, rt.After(start) || rt.Equal(start))
	}
}

func TestUpdateTargetsDoesNotBlockOnCancel(t *testing.T) {
	store := testCheckpointStore(t)
	p := newTestPoller(t, store)

	key := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}

	p.UpdateTargets(map[CheckpointKey]ConnectionInfo{
		key: stubConnectionInfo(t),
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
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIsPublicGitHub(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"", true},
		{"https://api.github.com", true},
		{"https://github.com", true},
		{"https://ghes.example.com", false},
		{"https://github.example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			require.Equal(t, tt.want, isPublicGitHub(tt.url))
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
	p := NewPoller(Config{
		Checkpoints:   store,
		EventCapacity: 1,
	})
	t.Cleanup(func() {
		_ = p.Close()
	})
	return p
}

// stubConnectionInfo returns a ConnectionInfo backed by a test HTTP server
// that returns an empty events list. This ensures polling goroutines started
// by UpdateTargets won't crash if they reach doPoll before the context is
// cancelled.
func stubConnectionInfo(t *testing.T) ConnectionInfo {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Poll-Interval", "60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(ts.Close)
	ghClient, err := NewGitHubClient("stub-token", ts.URL)
	require.NoError(t, err)
	return ConnectionInfo{Token: "stub-token", User: "stub-user", BaseURL: ts.URL, GHClient: ghClient}
}

func TestSelectNewEventsStopsAtCheckpoint(t *testing.T) {
	events := []*github.Event{
		{ID: github.Ptr("5")},
		{ID: github.Ptr("4")},
		{ID: github.Ptr("3")},
		{ID: github.Ptr("2")},
		{ID: github.Ptr("1")},
	}

	selected, hit := selectNewEvents(events, "3")
	require.True(t, hit, "should hit checkpoint")
	require.Len(t, selected, 2)
	require.Equal(t, "5", selected[0].GetID())
	require.Equal(t, "4", selected[1].GetID())
}

func TestSelectNewEventsNoCheckpoint(t *testing.T) {
	events := []*github.Event{
		{ID: github.Ptr("3")},
		{ID: github.Ptr("2")},
		{ID: github.Ptr("1")},
	}

	selected, hit := selectNewEvents(events, "")
	require.False(t, hit)
	require.Len(t, selected, 3)
}

func TestSelectNewEventsCheckpointNotFound(t *testing.T) {
	events := []*github.Event{
		{ID: github.Ptr("5")},
		{ID: github.Ptr("4")},
	}

	selected, hit := selectNewEvents(events, "99")
	require.False(t, hit, "checkpoint not in page")
	require.Len(t, selected, 2)
}

// eventsServer returns an httptest server that serves the given JSON body for
// every Events API request, with an ETag and poll interval.
func eventsServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"etag1"`)
		w.Header().Set("X-Poll-Interval", "60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestFirstPollEstablishesBaselineWithoutProcessing(t *testing.T) {
	// Three issue_comment events are already present when polling starts. They
	// would be dispatched if processed, but the first poll must skip them.
	ts := eventsServer(t, `[
		{"id":"3","type":"IssueCommentEvent","payload":{"action":"created","issue":{"number":3}}},
		{"id":"2","type":"IssueCommentEvent","payload":{"action":"created","issue":{"number":2}}},
		{"id":"1","type":"IssueCommentEvent","payload":{"action":"created","issue":{"number":1}}}
	]`)

	store := testCheckpointStore(t)
	p := NewPoller(Config{Checkpoints: store, EventCapacity: 10})

	info := ConnectionInfo{Token: "t", User: "u", BaseURL: ts.URL}
	ghClient, err := NewGitHubClient(info.Token, info.BaseURL)
	require.NoError(t, err)
	info.GHClient = ghClient
	handlerClient, err := newHandlerClient(info)
	require.NoError(t, err)
	key := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}

	p.doPoll(context.Background(), key, info, ghClient, handlerClient)

	require.Empty(t, p.eventCh, "first poll must not dispatch pre-existing events")

	cp, ok := store.Get(key)
	require.True(t, ok, "first poll must establish a baseline checkpoint")
	require.Equal(t, "3", cp.LastEventID, "baseline should be the latest event ID")
	require.Equal(t, `"etag1"`, cp.ETag)
}

func TestSubsequentPollProcessesOnlyNewEvents(t *testing.T) {
	ts := eventsServer(t, `[
		{"id":"3","type":"IssueCommentEvent","payload":{"action":"created","issue":{"number":3}}},
		{"id":"2","type":"IssueCommentEvent","payload":{"action":"created","issue":{"number":2}}},
		{"id":"1","type":"IssueCommentEvent","payload":{"action":"created","issue":{"number":1}}}
	]`)

	store := testCheckpointStore(t)
	p := NewPoller(Config{Checkpoints: store, EventCapacity: 10})

	info := ConnectionInfo{Token: "t", User: "u", BaseURL: ts.URL}
	ghClient, err := NewGitHubClient(info.Token, info.BaseURL)
	require.NoError(t, err)
	info.GHClient = ghClient
	handlerClient, err := newHandlerClient(info)
	require.NoError(t, err)
	key := CheckpointKey{GithubConnectionID: "c1", OrgName: testOrg1}

	// A baseline already exists at event "2"; only event "3" is new.
	store.Set(key, Checkpoint{LastEventID: "2"})

	p.doPoll(context.Background(), key, info, ghClient, handlerClient)

	require.Len(t, p.eventCh, 1, "only the event newer than the checkpoint should be dispatched")

	cp, ok := store.Get(key)
	require.True(t, ok)
	require.Equal(t, "3", cp.LastEventID, "checkpoint should advance to the newest event")
}
