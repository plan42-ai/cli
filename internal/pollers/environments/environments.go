// Package environments implements the environment discovery polling loop.
// It periodically queries the Plan42 API to determine which
// (GithubConnectionID, OrgName) pairs the runner should be polling GitHub for.
package environments

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/plan42-ai/cli/internal/pollers/githubevents"
	"github.com/plan42-ai/concurrency"
	"github.com/plan42-ai/sdk-go/p42"
)

const maxDiscoveryInterval = 60 * time.Second

// Reconciler is the interface that the github events poller must implement.
// Environment discovery calls Reconcile on each iteration with the desired
// set of polling pairs and their connection details.
type Reconciler interface {
	Reconcile(desired map[githubevents.CheckpointKey]githubevents.ConnectionInfo)
}

// Config holds the dependencies for the environment discovery poller.
type Config struct {
	Client     *p42.Client
	TenantID   string
	RunnerID   string
	Reconciler Reconciler
	// RandDuration returns a random duration in [0, max). Overridable for testing.
	RandDuration func(ceiling time.Duration) time.Duration
}

// Poller implements the environment discovery polling loop.
type Poller struct {
	client       *p42.Client
	tenantID     string
	runnerID     string
	reconciler   Reconciler
	cg           *concurrency.ContextGroup
	randDuration func(ceiling time.Duration) time.Duration
}

// New creates a new environment discovery Poller. Call Start to begin the
// discovery loop.
func New(cfg Config) *Poller {
	rd := cfg.RandDuration
	if rd == nil {
		rd = defaultRandDuration
	}
	return &Poller{
		client:       cfg.Client,
		tenantID:     cfg.TenantID,
		runnerID:     cfg.RunnerID,
		reconciler:   cfg.Reconciler,
		cg:           concurrency.NewContextGroup(),
		randDuration: rd,
	}
}

// Start begins the discovery loop in a background goroutine.
func (p *Poller) Start() {
	p.cg.Add(1)
	go p.run()
}

// ShutdownContext cancels the discovery loop and waits for it to finish.
func (p *Poller) ShutdownContext(ctx context.Context) error {
	p.cg.Cancel()
	return p.cg.WaitContext(ctx)
}

// run is the main discovery loop.
func (p *Poller) run() {
	defer p.cg.Done()
	ctx := p.cg.Context()

	for {
		p.discover(ctx)

		sleepDur := p.randDuration(maxDiscoveryInterval)
		timer := time.NewTimer(sleepDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// discover performs one iteration of the discovery cycle.
func (p *Poller) discover(ctx context.Context) {
	// Step 1: Load tenant to get default GitHub connection ID.
	tenant, err := p.client.GetTenant(ctx, &p42.GetTenantRequest{
		TenantID: p.tenantID,
	})
	if err != nil {
		slog.ErrorContext(ctx, "environment discovery: GetTenant failed", "error", err)
		return
	}

	defaultConnID := ""
	if tenant.DefaultGithubConnectionID != nil {
		defaultConnID = *tenant.DefaultGithubConnectionID
	}

	// Step 2: Load all GitHub connections.
	connections, err := listAllGithubConnections(ctx, p.client, p.tenantID)
	if err != nil {
		slog.ErrorContext(ctx, "environment discovery: ListGithubConnections failed", "error", err)
		return
	}

	// Step 3: Filter to private connections targeting this runner and index by ID.
	privateConns := make(map[string]*p42.GithubConnection)
	for _, conn := range connections {
		if conn.Private && conn.RunnerID != nil && *conn.RunnerID == p.runnerID {
			privateConns[conn.ConnectionID] = conn
		}
	}

	// Step 4: Load all environments.
	environments, err := listAllEnvironments(ctx, p.client, p.tenantID)
	if err != nil {
		slog.ErrorContext(ctx, "environment discovery: ListEnvironments failed", "error", err)
		return
	}

	// Steps 5-7: Build the desired set of (GithubConnectionID, OrgName) pairs.
	desired := make(map[githubevents.CheckpointKey]githubevents.ConnectionInfo)
	for _, env := range environments {
		effectiveConnID := resolveConnectionID(env.GithubConnectionID, defaultConnID)
		if effectiveConnID == "" {
			continue
		}

		conn, ok := privateConns[effectiveConnID]
		if !ok {
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
					Token:   derefStr(conn.OAuthToken),
					BaseURL: "",
					User:    derefStr(conn.GithubUserLogin),
				}
			}
		}
	}

	p.reconciler.Reconcile(desired)
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

// derefStr safely dereferences a string pointer, returning "" for nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// defaultRandDuration returns a random duration in [0, max).
func defaultRandDuration(ceiling time.Duration) time.Duration {
	if ceiling <= 0 {
		return 0
	}
	//nolint:gosec // Cryptographic randomness not needed for jitter.
	return time.Duration(rand.Int64N(int64(ceiling)))
}

// listAllGithubConnections pages through all GitHub connections for a tenant.
func listAllGithubConnections(ctx context.Context, client *p42.Client, tenantID string) ([]*p42.GithubConnection, error) {
	var all []*p42.GithubConnection
	req := &p42.ListGithubConnectionsRequest{TenantID: tenantID}
	for {
		resp, err := client.ListGithubConnections(ctx, req)
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		if resp.NextToken == nil {
			return all, nil
		}
		req.Token = resp.NextToken
	}
}

// listAllEnvironments pages through all environments for a tenant.
func listAllEnvironments(ctx context.Context, client *p42.Client, tenantID string) ([]p42.Environment, error) {
	var all []p42.Environment
	req := &p42.ListEnvironmentsRequest{TenantID: tenantID}
	for {
		resp, err := client.ListEnvironments(ctx, req)
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		if resp.NextToken == nil {
			return all, nil
		}
		req.Token = resp.NextToken
	}
}
