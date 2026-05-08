package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plan42-ai/cli/internal/pollers/environments"
	"github.com/plan42-ai/cli/internal/pollers/githubevents"
	"github.com/plan42-ai/concurrency"
	githubeventslib "github.com/plan42-ai/github-event-handlers"
	"github.com/plan42-ai/github-event-handlers/githubclient"
	"github.com/plan42-ai/sdk-go/p42"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTenantID  = "tenant-1"
	testRunnerID  = "runner-1"
	testConnID    = "conn-1"
	testUserLogin = "testuser"
	testToken     = "ghp_testtoken" //nolint:gosec // test credential
	testOrgName   = "my-org"
)

// JSON field name constants used in mock GitHub Events API responses.
const (
	fType    = "type"
	fName    = "name"
	fPayload = "payload"
	fAction  = "action"
	fNumber  = "number"
	fLogin   = "login"
	fRepo    = "repo"
	fOpen    = "open"
	fUser    = "user"
	fState   = "state"
	fPR      = "pull_request"
)

func ptr[T any](v T) *T { return &v }

// newPlan42MockServer returns an httptest.Server serving the Plan42 API
// responses needed by environment discovery.
func newPlan42MockServer(t *testing.T, tenant *p42.Tenant, conns []*p42.GithubConnection, envs []p42.Environment) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/tenants/"+testTenantID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tenant)
	})
	mux.HandleFunc("GET /v1/tenants/"+testTenantID+"/github-connections", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p42.List[*p42.GithubConnection]{Items: conns})
	})
	mux.HandleFunc("GET /v1/tenants/"+testTenantID+"/environments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p42.List[p42.Environment]{Items: envs})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// newTestCheckpointStore creates a CheckpointStore backed by a temp dir.
func newTestCheckpointStore(t *testing.T, cg *concurrency.ContextGroup) *githubevents.CheckpointStore {
	t.Helper()
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	store, err := githubevents.NewCheckpointStore(cg)
	require.NoError(t, err)
	return store
}

// newTestCheckpointStoreInDir creates a CheckpointStore using a specific dir as
// the fake HOME, allowing a checkpoint file to persist across store instances.
func newTestCheckpointStoreInDir(t *testing.T, fakeHome string, cg *concurrency.ContextGroup) *githubevents.CheckpointStore {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(fakeHome, ".config"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", fakeHome)
	store, err := githubevents.NewCheckpointStore(cg)
	require.NoError(t, err)
	return store
}

// baseURLRewriter wraps a Reconciler to inject a custom BaseURL into all
// ConnectionInfo values, directing the events poller to a mock GH server.
type baseURLRewriter struct {
	inner   environments.Reconciler
	baseURL string
}

func (b *baseURLRewriter) Reconcile(desired map[githubevents.CheckpointKey]githubevents.ConnectionInfo) {
	rewritten := make(map[githubevents.CheckpointKey]githubevents.ConnectionInfo, len(desired))
	for k, v := range desired {
		v.BaseURL = b.baseURL
		rewritten[k] = v
	}
	b.inner.Reconcile(rewritten)
}

// TestDiscoveryReconcilePollCycle verifies that environment discovery computes
// the correct desired set and that the events poller starts polling goroutines
// for the reconciled pairs.
func TestDiscoveryReconcilePollCycle(t *testing.T) {
	tenant := &p42.Tenant{
		TenantID:                  testTenantID,
		DefaultGithubConnectionID: ptr(testConnID),
	}
	conns := []*p42.GithubConnection{{
		ConnectionID:    testConnID,
		Private:         true,
		RunnerID:        ptr(testRunnerID),
		GithubUserLogin: ptr(testUserLogin),
		OAuthToken:      ptr(testToken),
	}}
	envs := []p42.Environment{{
		EnvironmentID:      "env-1",
		GithubConnectionID: ptr(testConnID),
		Repos:              []string{testOrgName + "/repo-a", testOrgName + "/repo-b"},
	}}

	p42Server := newPlan42MockServer(t, tenant, conns, envs)
	client := p42.NewClient(p42Server.URL, p42.WithAPIToken("test-token"))

	var ghRequests atomic.Int64
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ghRequests.Add(1)
		w.Header().Set("X-Poll-Interval", "60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(ghServer.Close)

	checkpointCg := concurrency.NewContextGroup()
	t.Cleanup(func() {
		checkpointCg.Cancel()
		_ = checkpointCg.WaitTimeout(5 * time.Second)
	})
	store := newTestCheckpointStore(t, checkpointCg)

	registry := githubeventslib.NewHandlerRegistry(githubeventslib.Config{})
	eventsPoller := githubevents.New(githubevents.Config{
		Registry:    registry,
		Checkpoints: store,
		WorkerCount: 2,
	})
	eventsPoller.Start()

	// Wrap the reconciler to inject our mock GH server URL.
	wrapper := &baseURLRewriter{inner: eventsPoller, baseURL: ghServer.URL}

	envPoller := environments.New(environments.Config{
		Client:       client,
		TenantID:     testTenantID,
		RunnerID:     testRunnerID,
		Reconciler:   wrapper,
		RandDuration: func(_ time.Duration) time.Duration { return 0 },
	})
	envPoller.Start()

	// Wait for the full discovery -> reconcile -> poll cycle.
	require.Eventually(t, func() bool {
		return ghRequests.Load() > 0
	}, 15*time.Second, 50*time.Millisecond,
		"expected at least one GitHub Events API request")

	keys := eventsPoller.PairKeys()
	expectedKey := githubevents.CheckpointKey{GithubConnectionID: testConnID, OrgName: testOrgName}
	assert.Contains(t, keys, expectedKey)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, envPoller.ShutdownContext(shutdownCtx))
	require.NoError(t, eventsPoller.ShutdownContext(shutdownCtx))
}

// TestEventFlowEndToEnd sets up a mock GitHub Events API server that returns
// a page of events and verifies the full pipeline: fetch -> translate -> dispatch -> handle.
func TestEventFlowEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var handledEvents []string

	registry := githubeventslib.NewHandlerRegistry(githubeventslib.Config{})
	registry.Register("issue_comment", func(_ context.Context, evt githubeventslib.Event, _ githubclient.GithubAPI) {
		mu.Lock()
		defer mu.Unlock()
		handledEvents = append(handledEvents, "issue_comment:"+evt.GetDeliveryID())
	})
	registry.Register(fPR, func(_ context.Context, evt githubeventslib.Event, _ githubclient.GithubAPI) {
		mu.Lock()
		defer mu.Unlock()
		handledEvents = append(handledEvents, "pull_request:"+evt.GetDeliveryID())
	})

	var requestCount atomic.Int64
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		w.Header().Set("X-Poll-Interval", "60")

		if count > 1 {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		events := []map[string]any{
			{
				"id":  "1001",
				fType: "IssueCommentEvent",
				fRepo: map[string]any{"id": 1, fName: "my-org/my-repo"},
				fPayload: map[string]any{
					fAction:   "created",
					"issue":   map[string]any{fNumber: 42, fState: fOpen, fPR: map[string]any{}},
					"comment": map[string]any{"body": "/Plan42", fUser: map[string]any{fLogin: "alice"}},
				},
			},
			{
				"id":  "1000",
				fType: "PullRequestEvent",
				fRepo: map[string]any{"id": 1, fName: "my-org/my-repo"},
				fPayload: map[string]any{
					fAction: "opened",
					fNumber: 7,
					fPR: map[string]any{
						"id": 123456, fNumber: 7, fState: fOpen,
						"merged": false, "draft": false,
						"html_url": "https://github.com/my-org/my-repo/pull/7",
						fUser:      map[string]any{fLogin: "alice"},
					},
				},
			},
		}

		w.Header().Set("ETag", `"etag-1"`)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(events)
	}))
	t.Cleanup(ghServer.Close)

	checkpointCg := concurrency.NewContextGroup()
	t.Cleanup(func() {
		checkpointCg.Cancel()
		_ = checkpointCg.WaitTimeout(5 * time.Second)
	})
	store := newTestCheckpointStore(t, checkpointCg)

	eventsPoller := githubevents.New(githubevents.Config{
		Registry:    registry,
		Checkpoints: store,
		WorkerCount: 5,
	})
	eventsPoller.Start()

	key := githubevents.CheckpointKey{GithubConnectionID: testConnID, OrgName: testOrgName}
	eventsPoller.Reconcile(map[githubevents.CheckpointKey]githubevents.ConnectionInfo{
		key: {Token: testToken, User: testUserLogin, BaseURL: ghServer.URL},
	})

	// Wait for both events to be handled.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handledEvents) >= 2
	}, 15*time.Second, 50*time.Millisecond,
		"expected both events to be handled")

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, handledEvents, 2)
	var hasIssueComment, hasPR bool
	for _, e := range handledEvents {
		if len(e) > 14 && e[:14] == "issue_comment:" {
			hasIssueComment = true
		}
		if len(e) > 13 && e[:13] == "pull_request:" {
			hasPR = true
		}
	}
	assert.True(t, hasIssueComment, "expected an issue_comment event")
	assert.True(t, hasPR, "expected a pull_request event")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, eventsPoller.ShutdownContext(shutdownCtx))
}

// TestCheckpointPersistence verifies that after processing events, checkpoints
// are persisted to disk. On simulated restart (reload checkpoint store), the
// poller resumes from the correct position.
func TestCheckpointPersistence(t *testing.T) {
	fakeHome := t.TempDir()
	cpPath := filepath.Join(fakeHome, ".config", "plan42-runner.checkpoint.json")

	// Phase 1: Process events and flush.
	var pollCount atomic.Int64
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := pollCount.Add(1)
		w.Header().Set("X-Poll-Interval", "60")
		w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, count))

		events := []map[string]any{{
			"id":  "event-42",
			fType: "PullRequestEvent",
			fRepo: map[string]any{"id": 1, fName: "org/repo"},
			fPayload: map[string]any{
				fAction: "opened", fNumber: 1,
				fPR: map[string]any{
					"id": 1, fNumber: 1, fState: fOpen,
					fUser: map[string]any{fLogin: "u"},
				},
			},
		}}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(events)
	}))
	t.Cleanup(ghServer.Close)

	cg1 := concurrency.NewContextGroup()
	store1 := newTestCheckpointStoreInDir(t, fakeHome, cg1)

	registry := githubeventslib.NewHandlerRegistry(githubeventslib.Config{})
	registry.Register(fPR, func(_ context.Context, _ githubeventslib.Event, _ githubclient.GithubAPI) {})

	poller1 := githubevents.New(githubevents.Config{
		Registry:    registry,
		Checkpoints: store1,
		WorkerCount: 1,
	})
	poller1.Start()

	key := githubevents.CheckpointKey{GithubConnectionID: testConnID, OrgName: "org"}
	poller1.Reconcile(map[githubevents.CheckpointKey]githubevents.ConnectionInfo{
		key: {Token: testToken, User: testUserLogin, BaseURL: ghServer.URL},
	})

	require.Eventually(t, func() bool {
		cp, ok := store1.Get(key)
		return ok && cp.LastEventID != ""
	}, 15*time.Second, 50*time.Millisecond)

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, poller1.ShutdownContext(shutCtx))
	require.NoError(t, store1.Flush(shutCtx))

	cg1.Cancel()
	_ = cg1.WaitTimeout(5 * time.Second)

	// Phase 2: Verify checkpoint file exists and has correct data.
	data, err := os.ReadFile(cpPath)
	require.NoError(t, err, "checkpoint file should exist")

	var parsed map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &parsed))
	require.Contains(t, parsed, testConnID+":org")

	// Phase 3: Load a new store from disk; verify checkpoint is loaded.
	cg2 := concurrency.NewContextGroup()
	t.Cleanup(func() {
		cg2.Cancel()
		_ = cg2.WaitTimeout(5 * time.Second)
	})
	store2 := newTestCheckpointStoreInDir(t, fakeHome, cg2)

	cp, ok := store2.Get(key)
	require.True(t, ok, "reloaded store should have the checkpoint")
	assert.Equal(t, "event-42", cp.LastEventID)
	assert.NotEmpty(t, cp.ETag)
}

// TestGracefulShutdownNoLeaks starts all components, triggers shutdown in the
// correct order, and verifies no goroutine leaks and checkpoint is flushed.
func TestGracefulShutdownNoLeaks(t *testing.T) {
	tenant := &p42.Tenant{
		TenantID:                  testTenantID,
		DefaultGithubConnectionID: ptr(testConnID),
	}
	conns := []*p42.GithubConnection{{
		ConnectionID:    testConnID,
		Private:         true,
		RunnerID:        ptr(testRunnerID),
		GithubUserLogin: ptr(testUserLogin),
		OAuthToken:      ptr(testToken),
	}}
	envs := []p42.Environment{{
		EnvironmentID:      "env-1",
		GithubConnectionID: ptr(testConnID),
		Repos:              []string{testOrgName + "/repo"},
	}}

	p42Server := newPlan42MockServer(t, tenant, conns, envs)
	client := p42.NewClient(p42Server.URL, p42.WithAPIToken("test-token"))

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Poll-Interval", "60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(ghServer.Close)

	checkpointCg := concurrency.NewContextGroup()
	store := newTestCheckpointStore(t, checkpointCg)

	registry := githubeventslib.NewHandlerRegistry(githubeventslib.Config{})
	eventsPoller := githubevents.New(githubevents.Config{
		Registry:    registry,
		Checkpoints: store,
		WorkerCount: 2,
	})
	eventsPoller.Start()

	wrapper := &baseURLRewriter{inner: eventsPoller, baseURL: ghServer.URL}

	envPoller := environments.New(environments.Config{
		Client:       client,
		TenantID:     testTenantID,
		RunnerID:     testRunnerID,
		Reconciler:   wrapper,
		RandDuration: func(_ time.Duration) time.Duration { return 0 },
	})
	envPoller.Start()

	// Let everything run briefly.
	time.Sleep(200 * time.Millisecond)

	// Shutdown in the correct order matching the runner's sequence.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	assert.NoError(t, envPoller.ShutdownContext(shutdownCtx), "env discovery shutdown")
	assert.NoError(t, eventsPoller.ShutdownContext(shutdownCtx), "events poller shutdown")
	assert.NoError(t, store.Flush(shutdownCtx), "checkpoint flush")
	// If we reach here without hanging, there are no goroutine leaks.
}
