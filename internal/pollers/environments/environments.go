// Package environments implements the environment discovery polling loop.
// It periodically queries the Plan42 API to determine which
// (GithubConnectionID, OrgName) pairs the runner should be polling GitHub for.
package environments

import (
	"context"
	"log/slog"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/google/go-github/v81/github"
	"github.com/plan42-ai/cache"
	"github.com/plan42-ai/cli/internal/config"
	"github.com/plan42-ai/cli/internal/pollers/githubevents"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/concurrency"
	"github.com/plan42-ai/sdk-go/p42"
)

const (
	maxDiscoveryInterval = 60 * time.Second
	connCacheTTL         = 8 * time.Hour
)

// EventPoller is the interface the github events poller must implement.
// Environment discovery calls UpdateTargets on each iteration with the desired
// set of polling pairs and their connection details.
type EventPoller interface {
	UpdateTargets(desired map[githubevents.CheckpointKey]githubevents.ConnectionInfo)
}

// Config holds the dependencies for the environment discovery poller.
type Config struct {
	Client        *p42.Client
	TenantID      string
	RunnerID      string
	EventPoller   EventPoller
	ConnectionIdx map[string]*config.GithubInfo
}

// Poller implements the environment discovery polling loop.
type Poller struct {
	client        *p42.Client
	tenantID      string
	runnerID      string
	eventPoller   EventPoller
	connectionIdx map[string]*config.GithubInfo
	cg            *concurrency.ContextGroup
	connections   *cache.Cache[string, *resolvedConn]
}

// New creates and starts a new environment discovery Poller. The poller
// is fully running when returned.
func New(cfg Config) *Poller {
	p := &Poller{
		client:        cfg.Client,
		tenantID:      cfg.TenantID,
		runnerID:      cfg.RunnerID,
		eventPoller:   cfg.EventPoller,
		connectionIdx: cfg.ConnectionIdx,
		cg:            concurrency.NewContextGroup(),
		connections:   cache.NewCacheWithTTL[string, *resolvedConn](connCacheTTL),
	}
	p.cg.Add(1)
	go p.run()
	return p
}

// ShutdownContext cancels the discovery loop and waits for it to finish.
func (p *Poller) ShutdownContext(ctx context.Context) error {
	p.cg.Cancel()
	err := p.cg.WaitContext(ctx)
	_ = p.connections.Close()
	return err
}

// run is the main discovery loop.
func (p *Poller) run() {
	defer p.cg.Done()
	ctx := p.cg.Context()

	timer := time.NewTimer(time.Duration(math.MaxInt64))
	timer.Stop()
	defer timer.Stop()

	for {
		p.discover(ctx)

		//nolint:gosec // Cryptographic randomness not needed for jitter.
		timer.Reset(rand.N(maxDiscoveryInterval))
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

// discover performs one iteration of the discovery cycle.
func (p *Poller) discover(ctx context.Context) {
	tenant, err := p.client.GetTenant(ctx, &p42.GetTenantRequest{
		TenantID: p.tenantID,
	})
	if err != nil {
		slog.ErrorContext(ctx, "environment discovery: GetTenant failed", "error", err)
		return
	}
	defaultConnID := util.Deref(tenant.DefaultGithubConnectionID)

	privateConns, err := getPrivateConnections(ctx, p.client, p.tenantID, p.runnerID)
	if err != nil {
		slog.ErrorContext(ctx, "environment discovery: ListGithubConnections failed", "error", err)
		return
	}

	desired, err := p.getTargetOrgs(ctx, defaultConnID, privateConns)
	if err != nil {
		slog.ErrorContext(ctx, "environment discovery: ListEnvironments failed", "error", err)
		return
	}
	p.eventPoller.UpdateTargets(desired)
}

// resolveConnectionID maps the literal "default" value to the tenant's default
// connection ID. A nil environment connection ID is treated as "default".
func resolveConnectionID(envConnID *string, defaultConnID string) string {
	if envConnID == nil || *envConnID == "" || *envConnID == "default" {
		return defaultConnID
	}
	return *envConnID
}

// extractOrg extracts the org name from a repo string in "org/repo" format.
func extractOrg(repo string) string {
	if i := strings.IndexByte(repo, '/'); i > 0 {
		return repo[:i]
	}
	return ""
}

// getPrivateConnections pages through all GitHub connections for a tenant and
// returns only private connections targeting the given runner, indexed by
// connection ID.
func getPrivateConnections(ctx context.Context, client *p42.Client, tenantID, runnerID string) (map[string]*p42.GithubConnection, error) {
	conns := make(map[string]*p42.GithubConnection)
	req := &p42.ListGithubConnectionsRequest{TenantID: tenantID, Private: util.Pointer(true)}
	for {
		resp, err := client.ListGithubConnections(ctx, req)
		if err != nil {
			return nil, err
		}
		for _, conn := range resp.Items {
			if conn.Private && conn.RunnerID != nil && *conn.RunnerID == runnerID {
				conns[conn.ConnectionID] = conn
			}
		}
		if resp.NextToken == nil {
			return conns, nil
		}
		req.Token = resp.NextToken
	}
}

// resolvedConn holds the GitHub client and authenticated user login for a
// private connection.
type resolvedConn struct {
	ghClient *github.Client
	user     string
}

// getConnection returns the resolved connection for connID. It returns the
// cached entry if one exists; otherwise it builds a new GitHub client,
// resolves the authenticated user, and caches the result. Returns nil if
// the connection cannot be resolved.
func (p *Poller) getConnection(ctx context.Context, connID string, info *config.GithubInfo) *resolvedConn {
	rc, ok := p.connections.GetCachedValue(connID)
	if ok {
		return rc
	}
	ghClient, err := githubevents.NewGitHubClient(info.Token, info.URL)
	if err != nil {
		slog.ErrorContext(ctx, "environment discovery: failed to create GitHub client for connection; skipping",
			"connectionID", connID, "error", err)
		return nil
	}
	user, _, err := ghClient.Users.Get(ctx, "")
	if err != nil {
		slog.ErrorContext(ctx, "environment discovery: failed to resolve GitHub user for connection; skipping",
			"connectionID", connID, "error", err)
		return nil
	}
	rc = &resolvedConn{ghClient: ghClient, user: user.GetLogin()}
	p.connections.AddIfNotPresent(connID, rc)
	return rc
}

// getTargetOrgs pages through all environments for the tenant and builds the
// desired set of unique (GithubConnectionID, OrgName) pairs for environments
// whose effective connection is a private connection targeting this runner.
func (p *Poller) getTargetOrgs(
	ctx context.Context,
	defaultConnID string,
	privateConns map[string]*p42.GithubConnection,
) (map[githubevents.CheckpointKey]githubevents.ConnectionInfo, error) {
	desired := make(map[githubevents.CheckpointKey]githubevents.ConnectionInfo)
	req := &p42.ListEnvironmentsRequest{TenantID: p.tenantID}
	for {
		resp, err := p.client.ListEnvironments(ctx, req)
		if err != nil {
			return nil, err
		}
		for _, env := range resp.Items {
			effectiveConnID := resolveConnectionID(env.GithubConnectionID, defaultConnID)
			if effectiveConnID == "" {
				continue
			}
			if privateConns[effectiveConnID] == nil {
				continue
			}
			localInfo := p.connectionIdx[effectiveConnID]
			if localInfo == nil || localInfo.Token == "" {
				slog.ErrorContext(ctx, "environment discovery: no local credentials for connection; skipping",
					"connectionID", effectiveConnID)
				continue
			}
			rc := p.getConnection(ctx, effectiveConnID, localInfo)
			if rc == nil {
				continue
			}
			for _, repo := range env.Repos {
				org := extractOrg(repo)
				if org == "" {
					continue
				}
				key := githubevents.CheckpointKey{
					GithubConnectionID: effectiveConnID,
					OrgName:            org,
				}
				if _, exists := desired[key]; !exists {
					desired[key] = githubevents.ConnectionInfo{
						Token:    localInfo.Token,
						BaseURL:  localInfo.URL,
						User:     rc.user,
						GHClient: rc.ghClient,
					}
				}
			}
		}
		if resp.NextToken == nil {
			return desired, nil
		}
		req.Token = resp.NextToken
	}
}
