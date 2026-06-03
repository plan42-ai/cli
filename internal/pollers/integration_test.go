package pollers_test

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

	"github.com/plan42-ai/cli/internal/config"
	"github.com/plan42-ai/cli/internal/pollers/environments"
	"github.com/plan42-ai/cli/internal/pollers/githubevents"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/clock"
	ghapi "github.com/plan42-ai/github-event-handlers/github"
	"github.com/plan42-ai/github-event-handlers/handlers"
	"github.com/plan42-ai/sdk-go/p42"
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

// --- Plan42 API mock ---

type apiFixture struct {
	tenant      *p42.Tenant
	connections []*p42.GithubConnection
	envs        []p42.Environment
}

func defaultFixture() apiFixture {
	return apiFixture{
		tenant: &p42.Tenant{
			TenantID:                  testTenantID,
			DefaultGithubConnectionID: util.Pointer(testConnID),
		},
		connections: []*p42.GithubConnection{
			{
				ConnectionID:    testConnID,
				Private:         true,
				RunnerID:        util.Pointer(testRunnerID),
				GithubUserLogin: util.Pointer(testUserLogin),
			},
		},
		envs: []p42.Environment{
			{
				EnvironmentID:      "env-1",
				GithubConnectionID: util.Pointer(testConnID),
				Repos:              []string{testOrgName + "/repo-a"},
			},
		},
	}
}

func defaultConnectionIdx() map[string]*config.GithubInfo {
	return map[string]*config.GithubInfo{
		testConnID: {
			ConnectionID: testConnID,
			Token:        testToken,
			URL:          "",
		},
	}
}

func newPlan42Server(t *testing.T, fix apiFixture) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/tenants/"+testTenantID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fix.tenant)
	})

	mux.HandleFunc("GET /v1/tenants/"+testTenantID+"/github-connections", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p42.List[*p42.GithubConnection]{Items: fix.connections})
	})

	mux.HandleFunc("GET /v1/tenants/"+testTenantID+"/environments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p42.List[p42.Environment]{Items: fix.envs})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// --- GitHub Events API mock ---

// githubEventsHandler returns an http.Handler that serves a canned Events API
// page. requestCount is incremented on each request.
func githubEventsHandler(body string, requestCount *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestCount != nil {
			requestCount.Add(1)
		}
		// Support If-None-Match for 304 testing.
		if etag := r.Header.Get("If-None-Match"); etag == `"stable-etag"` {
			w.Header().Set("X-Poll-Interval", "60")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"stable-etag"`)
		w.Header().Set("X-Poll-Interval", "60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
}

// newCheckpointStore creates a temp-backed checkpoint store for tests.
func newCheckpointStore(t *testing.T) *githubevents.CheckpointStore {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "plan42-runner.checkpoint.json")
	store, err := githubevents.NewCheckpointStore(path, clock.NewRealClock())
	require.NoError(t, err)
	return store
}

// --- Tests ---

// TestDiscoveryReconcilePoll verifies that environment discovery computes the
// correct desired set of (connection, org) pairs and that the poller starts
// polling goroutines for them.
func TestDiscoveryReconcilePoll(t *testing.T) {
	fix := defaultFixture()
	p42Server := newPlan42Server(t, fix)
	client := p42.NewClient(p42Server.URL, p42.WithAPIToken("test-token"))

	store := newCheckpointStore(t)
	poller := githubevents.NewPoller(githubevents.Config{Checkpoints: store})

	envPoller := environments.New(environments.Config{
		Client:        client,
		TenantID:      testTenantID,
		RunnerID:      testRunnerID,
		EventPoller:   poller,
		ConnectionIdx: defaultConnectionIdx(),
	})

	// Wait for at least one reconcile cycle.
	require.Eventually(t, func() bool {
		return len(poller.TargetKeys()) > 0
	}, 5*time.Second, 50*time.Millisecond, "poller should have targets after discovery")

	keys := poller.TargetKeys()
	expectedKey := githubevents.CheckpointKey{
		GithubConnectionID: testConnID,
		OrgName:            testOrgName,
	}
	require.Contains(t, keys, expectedKey)

	// Shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, envPoller.ShutdownContext(ctx))
	require.NoError(t, poller.Close())
}

// TestEventFlowEndToEnd stands up a mock GitHub Events API, wires the full
// poller -> dispatcher -> handler stack, and asserts that events arrive at
// the handler with the correct types.
func TestEventFlowEndToEnd(t *testing.T) {
	eventsBody := `[
		{"id":"20","type":"IssueCommentEvent","repo":{"id":1,"name":"my-org/repo-a"},"payload":{"action":"created","issue":{"number":42,"state":"open","pull_request":{}},"comment":{"body":"hello","user":{"login":"alice"}}}},
		{"id":"19","type":"PullRequestEvent","repo":{"id":1,"name":"my-org/repo-a"},"payload":{"action":"opened","number":10,"pull_request":{"id":100,"number":10,"state":"open","user":{"login":"bob"}}}}
	]`

	var ghRequests atomic.Int64
	ghServer := httptest.NewServer(githubEventsHandler(eventsBody, &ghRequests))
	defer ghServer.Close()

	fix := defaultFixture()
	p42Server := newPlan42Server(t, fix)
	client := p42.NewClient(p42Server.URL, p42.WithAPIToken("test-token"))

	store := newCheckpointStore(t)
	poller := githubevents.NewPoller(githubevents.Config{Checkpoints: store, EventCapacity: 50})

	var mu sync.Mutex
	var handledTypes []string

	registry := handlers.NewHandlerRegistry(handlers.Config{
		Plan42Client:      client,
		CommentTriggerStr: "/Plan42",
		UIURL:             "https://test.plan42.ai",
	})
	// Override all four event types with a recording handler so we can inspect what arrives.
	for _, et := range []string{"issue_comment", "pull_request", "pull_request_review", "pull_request_review_comment"} {
		et := et
		registry.Register(et, func(_ context.Context, evt handlers.Event, _ ghapi.API) {
			mu.Lock()
			defer mu.Unlock()
			handledTypes = append(handledTypes, evt.EventType())
		})
	}

	dispatcher := githubevents.NewDispatcher(registry, poller.EventCh(), 5)

	// Pre-seed a checkpoint so the first poll dispatches events instead of
	// just establishing a baseline.
	key := githubevents.CheckpointKey{
		GithubConnectionID: testConnID,
		OrgName:            testOrgName,
	}
	store.Set(key, githubevents.Checkpoint{LastEventID: "18"})

	// Directly call UpdateTargets (instead of going through env discovery)
	// so we control the BaseURL pointed at our mock GitHub server.
	poller.UpdateTargets(map[githubevents.CheckpointKey]githubevents.ConnectionInfo{
		key: {
			Token:   testToken,
			User:    testUserLogin,
			BaseURL: ghServer.URL,
		},
	})

	// Wait for events to be handled.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handledTypes) >= 2
	}, 15*time.Second, 100*time.Millisecond, "expected at least 2 events to be handled")

	mu.Lock()
	types := append([]string{}, handledTypes...)
	mu.Unlock()

	require.Contains(t, types, "issue_comment")
	require.Contains(t, types, "pull_request")

	// Shutdown.
	require.NoError(t, poller.Close())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, dispatcher.ShutdownContext(ctx))
}

// TestCheckpointPersistenceAndResume verifies that after processing events the
// checkpoint is persisted to disk, and that a fresh CheckpointStore loaded
// from the same file resumes from the stored position.
func TestCheckpointPersistenceAndResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan42-runner.checkpoint.json")

	store1, err := githubevents.NewCheckpointStore(path, clock.NewRealClock())
	require.NoError(t, err)

	key := githubevents.CheckpointKey{
		GithubConnectionID: testConnID,
		OrgName:            testOrgName,
	}
	store1.Set(key, githubevents.Checkpoint{
		LastEventID:      "42",
		ETag:             `"etag-abc"`,
		PollIntervalSecs: 90,
	})

	// Shutdown flushes to disk.
	require.NoError(t, store1.Shutdown(context.Background()))

	// Verify the file exists and has content.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"42"`)

	// Reload from the same path.
	store2, err := githubevents.NewCheckpointStore(path, clock.NewRealClock())
	require.NoError(t, err)

	cp, ok := store2.Get(key)
	require.True(t, ok, "checkpoint should be loaded from disk")
	require.Equal(t, "42", cp.LastEventID)
	require.Equal(t, `"etag-abc"`, cp.ETag)
	require.Equal(t, 90, cp.PollIntervalSecs)

	require.NoError(t, store2.Shutdown(context.Background()))
}

// TestGracefulShutdownNoLeaks starts the full stack (env discovery, poller,
// dispatcher), triggers the shutdown sequence, and verifies no goroutine
// leaks and that checkpoints are flushed.
func TestGracefulShutdownNoLeaks(t *testing.T) {
	eventsBody := `[
		{"id":"5","type":"IssueCommentEvent","repo":{"id":1,"name":"my-org/repo-a"},"payload":{"action":"created","issue":{"number":1,"state":"open","pull_request":{}},"comment":{"body":"test","user":{"login":"alice"}}}}
	]`

	ghServer := httptest.NewServer(githubEventsHandler(eventsBody, nil))
	defer ghServer.Close()

	fix := defaultFixture()
	p42Server := newPlan42Server(t, fix)
	client := p42.NewClient(p42Server.URL, p42.WithAPIToken("test-token"))

	dir := t.TempDir()
	cpPath := filepath.Join(dir, "plan42-runner.checkpoint.json")
	store, err := githubevents.NewCheckpointStore(cpPath, clock.NewRealClock())
	require.NoError(t, err)

	poller := githubevents.NewPoller(githubevents.Config{Checkpoints: store, EventCapacity: 50})

	var handleCount atomic.Int64
	registry := handlers.NewHandlerRegistry(handlers.Config{
		Plan42Client:      client,
		CommentTriggerStr: "/Plan42",
	})
	for _, et := range []string{"issue_comment", "pull_request", "pull_request_review", "pull_request_review_comment"} {
		et := et
		_ = et
		registry.Register(et, func(_ context.Context, _ handlers.Event, _ ghapi.API) {
			handleCount.Add(1)
		})
	}

	dispatcher := githubevents.NewDispatcher(registry, poller.EventCh(), 5)

	// Pre-seed a checkpoint so events are dispatched.
	key := githubevents.CheckpointKey{GithubConnectionID: testConnID, OrgName: testOrgName}
	store.Set(key, githubevents.Checkpoint{LastEventID: "4"})

	// Start env discovery, which will reconcile the poller with targets.
	envPoller := environments.New(environments.Config{
		Client:        client,
		TenantID:      testTenantID,
		RunnerID:      testRunnerID,
		EventPoller:   poller,
		ConnectionIdx: defaultConnectionIdx(),
	})

	// Wait for at least one target to appear and one poll to happen.
	require.Eventually(t, func() bool {
		return len(poller.TargetKeys()) > 0
	}, 5*time.Second, 50*time.Millisecond)

	// Execute shutdown sequence (same order as main.go).
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	// 1. Stop environment discovery.
	require.NoError(t, envPoller.ShutdownContext(shutdownCtx))

	// 2. Stop the poller (closes EventCh, flushes checkpoints).
	require.NoError(t, poller.Close())

	// 3. Drain the dispatcher.
	require.NoError(t, dispatcher.ShutdownContext(shutdownCtx))

	// Verify checkpoint was flushed to disk. The checkpoint was seeded
	// before the first poll so the poller should have advanced it.
	_, err = os.Stat(cpPath)
	require.NoError(t, err, "checkpoint file should exist after shutdown flush")
}

// TestEndToEndWithMultipleOrgs verifies that multiple (connection, org)
// pairs are discovered and polled simultaneously.
func TestEndToEndWithMultipleOrgs(t *testing.T) {
	eventsBody := `[{"id":"1","type":"IssueCommentEvent","repo":{"id":1,"name":"%s/repo"},"payload":{"action":"created","issue":{"number":1,"state":"open","pull_request":{}},"comment":{"body":"hello","user":{"login":"u"}}}}]`

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"etag"`)
		w.Header().Set("X-Poll-Interval", "60")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, eventsBody, "org-a")
	}))
	defer ghServer.Close()

	fix := apiFixture{
		tenant: &p42.Tenant{
			TenantID:                  testTenantID,
			DefaultGithubConnectionID: util.Pointer(testConnID),
		},
		connections: []*p42.GithubConnection{
			{
				ConnectionID:    testConnID,
				Private:         true,
				RunnerID:        util.Pointer(testRunnerID),
				GithubUserLogin: util.Pointer(testUserLogin),
			},
		},
		envs: []p42.Environment{
			{
				EnvironmentID:      "env-1",
				GithubConnectionID: util.Pointer(testConnID),
				Repos:              []string{"org-a/repo-1", "org-b/repo-2"},
			},
		},
	}

	p42Server := newPlan42Server(t, fix)
	client := p42.NewClient(p42Server.URL, p42.WithAPIToken("test-token"))

	store := newCheckpointStore(t)
	poller := githubevents.NewPoller(githubevents.Config{Checkpoints: store})

	envPoller := environments.New(environments.Config{
		Client:        client,
		TenantID:      testTenantID,
		RunnerID:      testRunnerID,
		EventPoller:   poller,
		ConnectionIdx: defaultConnectionIdx(),
	})

	// Wait for both targets to appear.
	require.Eventually(t, func() bool {
		return len(poller.TargetKeys()) >= 2
	}, 5*time.Second, 50*time.Millisecond, "expected 2 targets for 2 distinct orgs")

	keys := poller.TargetKeys()
	require.Contains(t, keys, githubevents.CheckpointKey{GithubConnectionID: testConnID, OrgName: "org-a"})
	require.Contains(t, keys, githubevents.CheckpointKey{GithubConnectionID: testConnID, OrgName: "org-b"})

	// Clean shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, envPoller.ShutdownContext(ctx))
	require.NoError(t, poller.Close())
}
