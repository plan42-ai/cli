package environments

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/plan42-ai/cache"
	"github.com/plan42-ai/cli/internal/config"
	"github.com/plan42-ai/cli/internal/pollers/githubevents"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42"
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

// newMockGitHubServer creates a mock GitHub API server that resolves token to
// username via the /api/v3/user endpoint. tokenToUser maps "token <value>" to
// the login to return. The returned server URL can be used as the GithubInfo.URL
// for enterprise-style connections.
func newMockGitHubServer(t *testing.T, tokenToUser map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "token ")
		login, ok := tokenToUser[token]
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"login": login})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// mockEventPoller records calls to UpdateTargets.
type mockEventPoller struct {
	mu    sync.Mutex
	calls []map[githubevents.CheckpointKey]githubevents.ConnectionInfo
	ch    chan struct{}
}

func newMockEventPoller() *mockEventPoller {
	return &mockEventPoller{ch: make(chan struct{}, 100)}
}

func (m *mockEventPoller) UpdateTargets(desired map[githubevents.CheckpointKey]githubevents.ConnectionInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, desired)
	m.ch <- struct{}{}
}

func (m *mockEventPoller) lastCall() map[githubevents.CheckpointKey]githubevents.ConnectionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return nil
	}
	return m.calls[len(m.calls)-1]
}

func (m *mockEventPoller) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockEventPoller) waitForCall(ctx context.Context) bool {
	select {
	case <-m.ch:
		return true
	case <-ctx.Done():
		return false
	}
}

type apiFixture struct {
	tenant        *p42.Tenant
	connections   []*p42.GithubConnection
	envs          []p42.Environment
	connectionIdx map[string]*config.GithubInfo
}

func defaultFixture(ghURL string) apiFixture {
	return apiFixture{
		tenant: &p42.Tenant{
			TenantID:                  testTenantID,
			DefaultGithubConnectionID: util.Pointer(testConnID1),
		},
		connections: []*p42.GithubConnection{
			{
				ConnectionID: testConnID1,
				Private:      true,
				RunnerID:     util.Pointer(testRunnerID),
			},
		},
		envs: []p42.Environment{
			{
				EnvironmentID:      testEnvID1,
				GithubConnectionID: util.Pointer(testConnID1),
				Repos:              []string{testOrgName + "/repo-a", testOrgName + "/repo-b"},
			},
		},
		connectionIdx: map[string]*config.GithubInfo{
			testConnID1: {
				ConnectionID: testConnID1,
				Token:        testToken,
				URL:          ghURL,
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

func newTestPoller(t *testing.T, ts *httptest.Server, ep *mockEventPoller, connIdx map[string]*config.GithubInfo) *Poller {
	t.Helper()
	client := p42.NewClient(ts.URL, p42.WithAPIToken("test-token"))
	return New(Config{
		Client:        client,
		TenantID:      testTenantID,
		RunnerID:      testRunnerID,
		EventPoller:   ep,
		ConnectionIdx: connIdx,
	})
}

func shutdownPoller(t *testing.T, poller *Poller) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, poller.ShutdownContext(ctx))
}

func TestPrivateConnectionTargetingThisRunner(t *testing.T) {
	ghServer := newMockGitHubServer(t, map[string]string{testToken: testUserLogin})
	fix := defaultFixture(ghServer.URL)
	ts := newMockServer(t, fix)
	ep := newMockEventPoller()
	poller := newTestPoller(t, ts, ep, fix.connectionIdx)
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, ep.waitForCall(ctx), "expected UpdateTargets to be called")

	desired := ep.lastCall()
	key := githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: testOrgName}
	require.Contains(t, desired, key)
	require.Equal(t, testToken, desired[key].Token)
	require.Equal(t, testUserLogin, desired[key].User)
	require.Equal(t, ghServer.URL, desired[key].BaseURL)
}

func TestConnectionTargetingDifferentRunner(t *testing.T) {
	ghServer := newMockGitHubServer(t, map[string]string{testToken: testUserLogin})
	fix := defaultFixture(ghServer.URL)
	fix.connections[0].RunnerID = util.Pointer("other-runner")

	ts := newMockServer(t, fix)
	ep := newMockEventPoller()
	poller := newTestPoller(t, ts, ep, fix.connectionIdx)
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, ep.waitForCall(ctx))

	desired := ep.lastCall()
	require.Empty(t, desired, "no pairs should be produced for a different runner")
}

func TestNonPrivateConnection(t *testing.T) {
	ghServer := newMockGitHubServer(t, map[string]string{testToken: testUserLogin})
	fix := defaultFixture(ghServer.URL)
	fix.connections[0].Private = false

	ts := newMockServer(t, fix)
	ep := newMockEventPoller()
	poller := newTestPoller(t, ts, ep, fix.connectionIdx)
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, ep.waitForCall(ctx))

	desired := ep.lastCall()
	require.Empty(t, desired, "non-private connections should be excluded")
}

func TestDefaultConnectionIDResolution(t *testing.T) {
	ghServer := newMockGitHubServer(t, map[string]string{testToken: testUserLogin})
	fix := defaultFixture(ghServer.URL)
	fix.envs[0].GithubConnectionID = util.Pointer("default")

	ts := newMockServer(t, fix)
	ep := newMockEventPoller()
	poller := newTestPoller(t, ts, ep, fix.connectionIdx)
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, ep.waitForCall(ctx))

	desired := ep.lastCall()
	key := githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: testOrgName}
	require.Contains(t, desired, key, "\"default\" should resolve to the tenant's default connection")
}

func TestNilConnectionIDResolvesToDefault(t *testing.T) {
	ghServer := newMockGitHubServer(t, map[string]string{testToken: testUserLogin})
	fix := defaultFixture(ghServer.URL)
	fix.envs[0].GithubConnectionID = nil

	ts := newMockServer(t, fix)
	ep := newMockEventPoller()
	poller := newTestPoller(t, ts, ep, fix.connectionIdx)
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, ep.waitForCall(ctx))

	desired := ep.lastCall()
	key := githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: testOrgName}
	require.Contains(t, desired, key, "nil connection ID should resolve to the tenant's default connection")
}

func TestMultipleOrgsAcrossMultipleEnvironments(t *testing.T) {
	ghServer := newMockGitHubServer(t, map[string]string{"token1": "user1", "token2": "user2"})
	fix := apiFixture{
		tenant: &p42.Tenant{
			TenantID:                  testTenantID,
			DefaultGithubConnectionID: util.Pointer(testConnID1),
		},
		connections: []*p42.GithubConnection{
			{
				ConnectionID: testConnID1,
				Private:      true,
				RunnerID:     util.Pointer(testRunnerID),
			},
			{
				ConnectionID: testConnID2,
				Private:      true,
				RunnerID:     util.Pointer(testRunnerID),
			},
		},
		envs: []p42.Environment{
			{
				EnvironmentID:      testEnvID1,
				GithubConnectionID: util.Pointer(testConnID1),
				Repos:              []string{"org-a/repo-1", "org-b/repo-2"},
			},
			{
				EnvironmentID:      testEnvID2,
				GithubConnectionID: util.Pointer(testConnID2),
				Repos:              []string{"org-c/repo-3", "org-a/repo-4"},
			},
		},
		connectionIdx: map[string]*config.GithubInfo{
			testConnID1: {ConnectionID: testConnID1, Token: "token1", URL: ghServer.URL},
			testConnID2: {ConnectionID: testConnID2, Token: "token2", URL: ghServer.URL},
		},
	}

	ts := newMockServer(t, fix)
	ep := newMockEventPoller()
	poller := newTestPoller(t, ts, ep, fix.connectionIdx)
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, ep.waitForCall(ctx))

	desired := ep.lastCall()
	require.Len(t, desired, 4)

	require.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "org-a"})
	require.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "org-b"})
	require.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID2, OrgName: "org-c"})
	require.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID2, OrgName: "org-a"})
}

func TestEnvironmentWithNoRepos(t *testing.T) {
	ghServer := newMockGitHubServer(t, map[string]string{testToken: testUserLogin})
	fix := defaultFixture(ghServer.URL)
	fix.envs[0].Repos = nil

	ts := newMockServer(t, fix)
	ep := newMockEventPoller()
	poller := newTestPoller(t, ts, ep, fix.connectionIdx)
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, ep.waitForCall(ctx))

	desired := ep.lastCall()
	require.Empty(t, desired, "environment with no repos should produce no pairs")
}

func TestDeduplicationAcrossEnvironments(t *testing.T) {
	ghServer := newMockGitHubServer(t, map[string]string{testToken: testUserLogin})
	fix := apiFixture{
		tenant: &p42.Tenant{
			TenantID:                  testTenantID,
			DefaultGithubConnectionID: util.Pointer(testConnID1),
		},
		connections: []*p42.GithubConnection{
			{
				ConnectionID: testConnID1,
				Private:      true,
				RunnerID:     util.Pointer(testRunnerID),
			},
		},
		envs: []p42.Environment{
			{
				EnvironmentID:      testEnvID1,
				GithubConnectionID: util.Pointer(testConnID1),
				Repos:              []string{"same-org/repo-a"},
			},
			{
				EnvironmentID:      testEnvID2,
				GithubConnectionID: util.Pointer(testConnID1),
				Repos:              []string{"same-org/repo-b"},
			},
		},
		connectionIdx: map[string]*config.GithubInfo{
			testConnID1: {ConnectionID: testConnID1, Token: testToken, URL: ghServer.URL},
		},
	}

	ts := newMockServer(t, fix)
	ep := newMockEventPoller()
	poller := newTestPoller(t, ts, ep, fix.connectionIdx)
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, ep.waitForCall(ctx))

	desired := ep.lastCall()
	require.Len(t, desired, 1, "same (connectionID, org) pair from different environments should be deduplicated")
	key := githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "same-org"}
	require.Contains(t, desired, key)
}

func TestGracefulShutdownStopsLoop(t *testing.T) {
	ghServer := newMockGitHubServer(t, map[string]string{testToken: testUserLogin})
	fix := defaultFixture(ghServer.URL)
	ts := newMockServer(t, fix)
	ep := newMockEventPoller()
	poller := newTestPoller(t, ts, ep, fix.connectionIdx)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, ep.waitForCall(ctx))

	shutdownPoller(t, poller)

	countAfterShutdown := ep.callCount()

	time.Sleep(100 * time.Millisecond)
	require.Equal(t, countAfterShutdown, ep.callCount(),
		"no more UpdateTargets calls should happen after shutdown")
}

func TestMixedConnections(t *testing.T) {
	ghServer := newMockGitHubServer(t, map[string]string{"token1": "user1"})
	fix := apiFixture{
		tenant: &p42.Tenant{
			TenantID:                  testTenantID,
			DefaultGithubConnectionID: util.Pointer(testConnID1),
		},
		connections: []*p42.GithubConnection{
			{
				ConnectionID: testConnID1,
				Private:      true,
				RunnerID:     util.Pointer(testRunnerID),
			},
			{
				ConnectionID: testConnID2,
				Private:      true,
				RunnerID:     util.Pointer("other-runner"),
			},
			{
				ConnectionID: testConnID3,
				Private:      false,
				RunnerID:     nil,
			},
		},
		envs: []p42.Environment{
			{
				EnvironmentID:      testEnvID1,
				GithubConnectionID: util.Pointer(testConnID1),
				Repos:              []string{"good-org/repo"},
			},
			{
				EnvironmentID:      testEnvID2,
				GithubConnectionID: util.Pointer(testConnID2),
				Repos:              []string{"other-org/repo"},
			},
			{
				EnvironmentID:      testEnvID3,
				GithubConnectionID: util.Pointer(testConnID3),
				Repos:              []string{"public-org/repo"},
			},
		},
		connectionIdx: map[string]*config.GithubInfo{
			testConnID1: {ConnectionID: testConnID1, Token: "token1", URL: ghServer.URL},
		},
	}

	ts := newMockServer(t, fix)
	ep := newMockEventPoller()
	poller := newTestPoller(t, ts, ep, fix.connectionIdx)
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, ep.waitForCall(ctx))

	desired := ep.lastCall()
	require.Len(t, desired, 1, "only the private connection targeting this runner should produce pairs")

	key := githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "good-org"}
	require.Contains(t, desired, key)
	require.Equal(t, "token1", desired[key].Token)
	require.Equal(t, "user1", desired[key].User)
}

func TestResolveConnectionID(t *testing.T) {
	tests := []struct {
		name        string
		envConnID   *string
		defaultConn string
		want        string
	}{
		{"nil resolves to default", nil, testDefaultCID, testDefaultCID},
		{"empty resolves to default", util.Pointer(""), testDefaultCID, testDefaultCID},
		{"literal default resolves to default", util.Pointer("default"), testDefaultCID, testDefaultCID},
		{"explicit ID used as-is", util.Pointer("explicit-conn"), testDefaultCID, "explicit-conn"},
		{"nil with empty default", nil, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveConnectionID(tt.envConnID, tt.defaultConn)
			require.Equal(t, tt.want, got)
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
			require.Equal(t, tt.want, extractOrg(tt.input))
		})
	}
}

func TestUserResolutionFailureSkipsNewConnection(t *testing.T) {
	// When a brand-new connection's /user lookup fails and there is no
	// cached value, that connection is skipped but the discovery cycle
	// still completes (with an empty desired map for that connection).
	ghServer := newMockGitHubServer(t, map[string]string{}) // no valid tokens
	fix := defaultFixture(ghServer.URL)
	ts := newMockServer(t, fix)
	ep := newMockEventPoller()
	poller := newTestPoller(t, ts, ep, fix.connectionIdx)
	defer shutdownPoller(t, poller)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, ep.waitForCall(ctx), "expected UpdateTargets to be called")

	desired := ep.lastCall()
	require.Empty(t, desired, "failing connection with no cache should produce no targets")
}

func TestUserResolutionFailureUsesCachedValue(t *testing.T) {
	// Calls getTargetOrgs directly to test the caching fallback behavior
	// without going through the full Poller lifecycle (avoids jitter delays).
	//
	// Phase 1: both connections resolve successfully.
	// Phase 2: conn-1's token is removed so /user returns 401; the cached
	//          value from phase 1 should be used. conn-2 still succeeds.

	tokenToUser := map[string]string{"token1": "user1", "token2": "user2"}
	ghMux := http.NewServeMux()
	var mu sync.Mutex
	ghMux.HandleFunc("GET /api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "token ")
		login, ok := func() (string, bool) {
			mu.Lock()
			defer mu.Unlock()
			v, found := tokenToUser[token]
			return v, found
		}()
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"login": login})
	})
	ghServer := httptest.NewServer(ghMux)
	t.Cleanup(ghServer.Close)

	connIdx := map[string]*config.GithubInfo{
		testConnID1: {ConnectionID: testConnID1, Token: "token1", URL: ghServer.URL},
		testConnID2: {ConnectionID: testConnID2, Token: "token2", URL: ghServer.URL},
	}

	privateConns := map[string]*p42.GithubConnection{
		testConnID1: {ConnectionID: testConnID1, Private: true, RunnerID: util.Pointer(testRunnerID)},
		testConnID2: {ConnectionID: testConnID2, Private: true, RunnerID: util.Pointer(testRunnerID)},
	}

	// Mock Plan42 API for ListEnvironments.
	p42Mux := http.NewServeMux()
	envs := []p42.Environment{
		{EnvironmentID: testEnvID1, GithubConnectionID: util.Pointer(testConnID1), Repos: []string{"org-a/repo"}},
		{EnvironmentID: testEnvID2, GithubConnectionID: util.Pointer(testConnID2), Repos: []string{"org-b/repo"}},
	}
	p42Mux.HandleFunc("GET /v1/tenants/"+testTenantID+"/environments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p42.List[p42.Environment]{Items: envs})
	})
	p42Server := httptest.NewServer(p42Mux)
	t.Cleanup(p42Server.Close)
	p42Client := p42.NewClient(p42Server.URL, p42.WithAPIToken("test-token"))

	poller := &Poller{
		client:        p42Client,
		tenantID:      testTenantID,
		connectionIdx: connIdx,
		connections:   cache.NewCacheWithTTL[string, *resolvedConn](connCacheTTL),
	}
	t.Cleanup(func() { _ = poller.connections.Close() })
	ctx := context.Background()

	// Phase 1: both connections succeed.
	desired, err := poller.getTargetOrgs(ctx, testConnID1, privateConns)
	require.NoError(t, err)
	require.Len(t, desired, 2)
	require.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "org-a"})
	require.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID2, OrgName: "org-b"})
	require.Equal(t, "user1", desired[githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "org-a"}].User)

	// Phase 2: break conn-1.
	func() {
		mu.Lock()
		defer mu.Unlock()
		delete(tokenToUser, "token1")
	}()

	desired, err = poller.getTargetOrgs(ctx, testConnID1, privateConns)
	require.NoError(t, err)
	require.Len(t, desired, 2, "conn-1 should fall back to cached value")
	require.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "org-a"})
	require.Contains(t, desired, githubevents.CheckpointKey{GithubConnectionID: testConnID2, OrgName: "org-b"})
	require.Equal(t, "user1", desired[githubevents.CheckpointKey{GithubConnectionID: testConnID1, OrgName: "org-a"}].User,
		"cached user login should be preserved")
}
