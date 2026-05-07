package environments

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/plan42-ai/cli/internal/pollers/githubevents"
	"github.com/plan42-ai/sdk-go/p42"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTenantID   = "tenant-1"
	testRunnerID   = "runner-1"
	testConnID1    = "conn-1"
	testConnID2    = "conn-2"
	testConnID3    = "conn-3"
	testUserLogin  = "testuser"
	testToken      = "ghp_testtoken" //nolint:gosec // test credential
	testEnvID1     = "env-1"
	testEnvID2     = "env-2"
	testEnvID3     = "env-3"
	testOrgName    = "my-org"
	testDefaultCID = "default-conn"
)

// mockReconciler records calls to Reconcile.
type mockReconciler struct {
	mu    sync.Mutex
	calls []map[githubevents.CheckpointKey]ConnectionInfo
	ch    chan struct{}
}

func newMockReconciler() *mockReconciler {
	return &mockReconciler{ch: make(chan struct{}, 100)}
}

func (m *mockReconciler) Reconcile(desired map[githubevents.CheckpointKey]ConnectionInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, desired)
	m.ch <- struct{}{}
}

func (m *mockReconciler) lastCall() map[githubevents.CheckpointKey]ConnectionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return nil
	}
	return m.calls[len(m.calls)-1]
}

func (m *mockReconciler) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockReconciler) waitForCall(ctx context.Context) bool {
	select {
	case <-m.ch:
		return true
	case <-ctx.Done():
		return false
	}
}

func ptr[T any](v T) *T {
	return &v
}

type apiFixture struct {
	tenant      *p42.Tenant
	connections []*p42.GithubConnection
	envs        []p42.Environment
}

func defaultFixture() apiFixture {
	return apiFixture{
		tenant: &p42.Tenant{
			TenantID:                  testTenantID,
			DefaultGithubConnectionID: ptr(testConnID1),
		},
		connections: []*p42.GithubConnection{
			{
				ConnectionID:    testConnID1,
				Private:         true,
				RunnerID:        ptr(testRunnerID),
				GithubUserLogin: ptr(testUserLogin),
				OAuthToken:      ptr(testToken),
			},
		},
		envs: []p42.Environment{
			{
				EnvironmentID:      testEnvID1,
				GithubConnectionID: ptr(testConnID1),
				Repos:              []string{testOrgName + "/repo-a", testOrgName + "/repo-b"},
			},
		},
	}
}

func newMockServer(t *testing.T, fix apiFixture) *httptest.Server {
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

func newTestPoller(t *testing.T, ts *httptest.Server, rec *mockReconciler) *Poller {
	t.Helper()
	client := p42.NewClient(ts.URL, p42.WithAPIToken("test-token"))
	return New(Config{
		Client:       client,
		TenantID:     testTenantID,
		RunnerID:     testRunnerID,
		Reconciler:   rec,
		RandDuration: func(_ time.Duration) time.Duration { return 0 },
	})
}

func shutdownPoller(t *testing.T, poller *Poller) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, poller.ShutdownContext(ctx))
}

func TestPrivateConnectionTargetingThisRunner(t *testing.T) {
	fix := defaultFixture()
	ts := newMockServer(t, fix)
	rec := newMockReconciler()
	poller := newTestPoller(t, ts, rec)

	poller.Start()
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, rec.waitForCall(ctx), "expected Reconcile to be called")

	desired := rec.lastCall()
	key := githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: testOrgName}
	require.Contains(t, desired, key)
	assert.Equal(t, testToken, desired[key].Token)
	assert.Equal(t, testUserLogin, desired[key].User)
	assert.Equal(t, "", desired[key].BaseURL)
}

func TestConnectionTargetingDifferentRunner(t *testing.T) {
	fix := defaultFixture()
	fix.connections[0].RunnerID = ptr("other-runner")

	ts := newMockServer(t, fix)
	rec := newMockReconciler()
	poller := newTestPoller(t, ts, rec)

	poller.Start()
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, rec.waitForCall(ctx))

	desired := rec.lastCall()
	assert.Empty(t, desired, "no pairs should be produced for a different runner")
}

func TestNonPrivateConnection(t *testing.T) {
	fix := defaultFixture()
	fix.connections[0].Private = false

	ts := newMockServer(t, fix)
	rec := newMockReconciler()
	poller := newTestPoller(t, ts, rec)

	poller.Start()
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, rec.waitForCall(ctx))

	desired := rec.lastCall()
	assert.Empty(t, desired, "non-private connections should be excluded")
}

func TestDefaultConnectionIDResolution(t *testing.T) {
	fix := defaultFixture()
	fix.envs[0].GithubConnectionID = ptr("default")

	ts := newMockServer(t, fix)
	rec := newMockReconciler()
	poller := newTestPoller(t, ts, rec)

	poller.Start()
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, rec.waitForCall(ctx))

	desired := rec.lastCall()
	key := githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: testOrgName}
	require.Contains(t, desired, key, "\"default\" should resolve to the tenant's default connection")
}

func TestNilConnectionIDResolvesToDefault(t *testing.T) {
	fix := defaultFixture()
	fix.envs[0].GithubConnectionID = nil

	ts := newMockServer(t, fix)
	rec := newMockReconciler()
	poller := newTestPoller(t, ts, rec)

	poller.Start()
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, rec.waitForCall(ctx))

	desired := rec.lastCall()
	key := githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: testOrgName}
	require.Contains(t, desired, key, "nil connection ID should resolve to the tenant's default connection")
}

func TestMultipleOrgsAcrossMultipleEnvironments(t *testing.T) {
	fix := apiFixture{
		tenant: &p42.Tenant{
			TenantID:                  testTenantID,
			DefaultGithubConnectionID: ptr(testConnID1),
		},
		connections: []*p42.GithubConnection{
			{
				ConnectionID:    testConnID1,
				Private:         true,
				RunnerID:        ptr(testRunnerID),
				GithubUserLogin: ptr("user1"),
				OAuthToken:      ptr("token1"),
			},
			{
				ConnectionID:    testConnID2,
				Private:         true,
				RunnerID:        ptr(testRunnerID),
				GithubUserLogin: ptr("user2"),
				OAuthToken:      ptr("token2"),
			},
		},
		envs: []p42.Environment{
			{
				EnvironmentID:      testEnvID1,
				GithubConnectionID: ptr(testConnID1),
				Repos:              []string{"org-a/repo-1", "org-b/repo-2"},
			},
			{
				EnvironmentID:      testEnvID2,
				GithubConnectionID: ptr(testConnID2),
				Repos:              []string{"org-c/repo-3", "org-a/repo-4"},
			},
		},
	}

	ts := newMockServer(t, fix)
	rec := newMockReconciler()
	poller := newTestPoller(t, ts, rec)

	poller.Start()
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, rec.waitForCall(ctx))

	desired := rec.lastCall()
	assert.Len(t, desired, 4)

	assert.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "org-a"})
	assert.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "org-b"})
	assert.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID2, OrgName: "org-c"})
	assert.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID2, OrgName: "org-a"})
}

func TestEnvironmentWithNoRepos(t *testing.T) {
	fix := defaultFixture()
	fix.envs[0].Repos = nil

	ts := newMockServer(t, fix)
	rec := newMockReconciler()
	poller := newTestPoller(t, ts, rec)

	poller.Start()
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, rec.waitForCall(ctx))

	desired := rec.lastCall()
	assert.Empty(t, desired, "environment with no repos should produce no pairs")
}

func TestDeduplicationAcrossEnvironments(t *testing.T) {
	fix := apiFixture{
		tenant: &p42.Tenant{
			TenantID:                  testTenantID,
			DefaultGithubConnectionID: ptr(testConnID1),
		},
		connections: []*p42.GithubConnection{
			{
				ConnectionID:    testConnID1,
				Private:         true,
				RunnerID:        ptr(testRunnerID),
				GithubUserLogin: ptr(testUserLogin),
				OAuthToken:      ptr(testToken),
			},
		},
		envs: []p42.Environment{
			{
				EnvironmentID:      testEnvID1,
				GithubConnectionID: ptr(testConnID1),
				Repos:              []string{"same-org/repo-a"},
			},
			{
				EnvironmentID:      testEnvID2,
				GithubConnectionID: ptr(testConnID1),
				Repos:              []string{"same-org/repo-b"},
			},
		},
	}

	ts := newMockServer(t, fix)
	rec := newMockReconciler()
	poller := newTestPoller(t, ts, rec)

	poller.Start()
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, rec.waitForCall(ctx))

	desired := rec.lastCall()
	assert.Len(t, desired, 1, "same (connectionID, org) pair from different environments should be deduplicated")
	key := githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "same-org"}
	assert.Contains(t, desired, key)
}

func TestRandomSleepBetweenIterations(t *testing.T) {
	fix := defaultFixture()
	ts := newMockServer(t, fix)
	rec := newMockReconciler()

	var mu sync.Mutex
	var sleepDurations []time.Duration
	callCount := 0

	client := p42.NewClient(ts.URL, p42.WithAPIToken("test-token"))
	poller := New(Config{
		Client:     client,
		TenantID:   testTenantID,
		RunnerID:   testRunnerID,
		Reconciler: rec,
		RandDuration: func(_ time.Duration) time.Duration {
			mu.Lock()
			defer mu.Unlock()
			callCount++
			dur := time.Duration(callCount) * time.Millisecond
			sleepDurations = append(sleepDurations, dur)
			return dur
		},
	})

	poller.Start()
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		require.True(t, rec.waitForCall(ctx), "expected Reconcile call %d", i)
	}

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, len(sleepDurations), 3, "RandDuration should be called once per cycle")
}

func TestGracefulShutdownStopsLoop(t *testing.T) {
	fix := defaultFixture()
	ts := newMockServer(t, fix)
	rec := newMockReconciler()
	poller := newTestPoller(t, ts, rec)

	poller.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, rec.waitForCall(ctx))

	shutdownPoller(t, poller)

	countAfterShutdown := rec.callCount()

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, countAfterShutdown, rec.callCount(),
		"no more Reconcile calls should happen after shutdown")
}

func TestMixedConnections(t *testing.T) {
	fix := apiFixture{
		tenant: &p42.Tenant{
			TenantID:                  testTenantID,
			DefaultGithubConnectionID: ptr(testConnID1),
		},
		connections: []*p42.GithubConnection{
			{
				ConnectionID:    testConnID1,
				Private:         true,
				RunnerID:        ptr(testRunnerID),
				GithubUserLogin: ptr("user1"),
				OAuthToken:      ptr("token1"),
			},
			{
				ConnectionID:    testConnID2,
				Private:         true,
				RunnerID:        ptr("other-runner"),
				GithubUserLogin: ptr("user2"),
				OAuthToken:      ptr("token2"),
			},
			{
				ConnectionID:    testConnID3,
				Private:         false,
				RunnerID:        nil,
				GithubUserLogin: ptr("user3"),
				OAuthToken:      ptr("token3"),
			},
		},
		envs: []p42.Environment{
			{
				EnvironmentID:      testEnvID1,
				GithubConnectionID: ptr(testConnID1),
				Repos:              []string{"good-org/repo"},
			},
			{
				EnvironmentID:      testEnvID2,
				GithubConnectionID: ptr(testConnID2),
				Repos:              []string{"other-org/repo"},
			},
			{
				EnvironmentID:      testEnvID3,
				GithubConnectionID: ptr(testConnID3),
				Repos:              []string{"public-org/repo"},
			},
		},
	}

	ts := newMockServer(t, fix)
	rec := newMockReconciler()
	poller := newTestPoller(t, ts, rec)

	poller.Start()
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, rec.waitForCall(ctx))

	desired := rec.lastCall()
	assert.Len(t, desired, 1, "only the private connection targeting this runner should produce pairs")

	key := githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "good-org"}
	require.Contains(t, desired, key)
	assert.Equal(t, "token1", desired[key].Token)
	assert.Equal(t, "user1", desired[key].User)
}

func TestResolveConnectionID(t *testing.T) {
	tests := []struct {
		name        string
		envConnID   *string
		defaultConn string
		want        string
	}{
		{"nil resolves to default", nil, testDefaultCID, testDefaultCID},
		{"empty resolves to default", ptr(""), testDefaultCID, testDefaultCID},
		{"literal default resolves to default", ptr("default"), testDefaultCID, testDefaultCID},
		{"explicit ID used as-is", ptr("explicit-conn"), testDefaultCID, "explicit-conn"},
		{"nil with empty default", nil, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveConnectionID(tt.envConnID, tt.defaultConn)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractOrg(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"org/repo", "org"},
		{"my-org/my-repo", "my-org"},
		{"noslash", ""},
		{"", ""},
		{"/leading-slash", ""},
		{"org/repo/extra", "org"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, extractOrg(tt.input))
		})
	}
}
