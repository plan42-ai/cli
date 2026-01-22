package poller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	ghapi "github.com/google/go-github/v81/github"
	"github.com/plan42-ai/cli/internal/github"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42/messages"
)

const (
	defaultPageSize = 10
	maxPageSize     = 100
)

var (
	errMaxResultInvalid       = errors.New("maxResults must be between 1 and 100")
	errInvalidPaginationToken = errors.New("invalid pagination token")
)

type ListOrgsPaginationKey struct {
	Page *int `json:"Page,omitempty"`
}

type pollerListOrgsForGithubConnectionRequest struct {
	messages.ListOrgsForGithubConnectionRequest
	client *github.Client
	err    error
}

func (req *pollerListOrgsForGithubConnectionRequest) Init(p *Poller) {
	req.client, req.err = p.GetClientForConnectionID(req.ConnectionID)
}

// ParsePagination parses MaxResults and Token into a key structure.
// key should be a pointer to the pagination key struct.
func ParsePagination[T any](maxResults *int, token *string, key *T) (int, error) {
	limit := defaultPageSize
	if maxResults != nil {
		limit = *maxResults
	}
	if limit <= 0 || limit > maxPageSize {
		return 0, errMaxResultInvalid
	}
	if token != nil {
		b, err := base64.RawURLEncoding.DecodeString(*token)
		if err != nil {
			return 0, errInvalidPaginationToken
		}
		if err := json.Unmarshal(b, key); err != nil {
			return 0, errInvalidPaginationToken
		}
	}
	return limit, nil
}

func NextToken[T any](paginationKey *T) (*string, error) {
	if paginationKey == nil {
		return nil, nil
	}
	jsonBytes, err := json.Marshal(paginationKey)
	if err != nil {
		return nil, err
	}
	return util.Pointer(base64.RawURLEncoding.EncodeToString(jsonBytes)), nil
}

func (req *pollerListOrgsForGithubConnectionRequest) Process(ctx context.Context) messages.Message {
	slog.InfoContext(ctx, "received ListOrgsForGithubConnectionRequest message", "connection_id", req.ConnectionID, "pagination_token", req.Token)
	if req.err != nil {
		slog.ErrorContext(ctx, "unable to initialize github client", "error", req.err, "connection_id", req.ConnectionID)
		return &messages.ListOrgsForGithubConnectionResponse{ErrorMessage: util.Pointer(req.err.Error())}
	}

	var paginationKey ListOrgsPaginationKey
	maxResults, err := ParsePagination(req.MaxResults, req.Token, &paginationKey)
	if err != nil {
		slog.ErrorContext(ctx, "unable to parse pagination key", "error", err, "connection_id", req.ConnectionID)
		return &messages.ListOrgsForGithubConnectionResponse{ErrorMessage: util.Pointer(err.Error())}
	}

	if req.Token == nil {
		paginationKey.Page = util.Pointer(1)
	}

	if paginationKey.Page == nil {
		user, _, err := req.client.GetCurrentUser(ctx)
		if err != nil {
			return &messages.ListOrgsForGithubConnectionResponse{ErrorMessage: util.Pointer("unable to fetch data for github user")}
		}
		var items []string
		if req.Search == nil || strings.Contains(*user.Login, *req.Search) {
			items = append(items, *user.Login)
		}
		return &messages.ListOrgsForGithubConnectionResponse{
			Items: items,
		}
	}

	orgs, resp, err := req.client.ListOrganizations(ctx, *paginationKey.Page, maxResults)
	if err != nil {
		slog.ErrorContext(ctx, "call to organizations.List failed", "error", err)
		return &messages.ListOrgsForGithubConnectionResponse{ErrorMessage: util.Pointer(err.Error())}
	}
	var orgNames []string
	for _, org := range orgs {
		if req.Search != nil && !strings.Contains(*org.Login, *req.Search) {
			continue
		}
		orgNames = append(orgNames, *org.Login)
	}
	slog.InfoContext(ctx, "call to organizations.List succeeded", "n_orgs", len(orgNames))
	var nextPaginationKey *ListOrgsPaginationKey

	switch {
	case resp != nil && resp.NextPage != 0:
		nextPaginationKey = &ListOrgsPaginationKey{
			Page: util.Pointer(resp.NextPage),
		}
	case len(orgNames) < maxResults:
		user, _, err := req.client.GetCurrentUser(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "call to users.Get failed", "error", err)
			return &messages.ListOrgsForGithubConnectionResponse{ErrorMessage: util.Pointer("unable to fetch data for github user")}
		}
		if req.Search == nil || strings.Contains(*user.Login, *req.Search) {
			orgNames = append(orgNames, *user.Login)
		}
	default:
		nextPaginationKey = &ListOrgsPaginationKey{}
	}
	nextToken, err := NextToken(nextPaginationKey)
	if err != nil {
		slog.ErrorContext(ctx, "unable to generate next pagination token", "error", err)
		return &messages.ListOrgsForGithubConnectionResponse{ErrorMessage: util.Pointer("unable to generate pagination token")}
	}
	return &messages.ListOrgsForGithubConnectionResponse{
		Items:     orgNames,
		NextToken: nextToken,
	}
}

type pollerSearchRepoRequest struct {
	messages.SearchRepoRequest
	client *github.Client
	err    error
}

func (req *pollerSearchRepoRequest) Init(p *Poller) {
	req.client, req.err = p.GetClientForConnectionID(req.ConnectionID)
}

type SearchRepoPaginationKey struct {
	Page int
}

func (req *pollerSearchRepoRequest) Process(ctx context.Context) messages.Message {
	slog.InfoContext(
		ctx,
		"received SearchRepoRequest message",
		"connection_id",
		req.ConnectionID,
		"org_name",
		req.OrgName,
		"pagination_token",
		req.Token,
	)
	if req.err != nil {
		slog.ErrorContext(ctx, "unable to initialize github client", "error", req.err, "connection_id", req.ConnectionID)
		return &messages.SearchRepoResponse{ErrorMessage: util.Pointer(req.err.Error())}
	}
	if req.OrgName == "" {
		slog.ErrorContext(ctx, "missing org name for search", "connection_id", req.ConnectionID)
		return &messages.SearchRepoResponse{ErrorMessage: util.Pointer("org name is required")}
	}
	if req.Search == "" {
		slog.ErrorContext(ctx, "missing search query", "connection_id", req.ConnectionID)
		return &messages.SearchRepoResponse{ErrorMessage: util.Pointer("search query is required")}
	}
	var paginationKey SearchRepoPaginationKey
	limit, err := ParsePagination(req.MaxResults, req.Token, &paginationKey)

	if err != nil {
		slog.ErrorContext(ctx, "unable to parse pagination key", "error", err, "connection_id", req.ConnectionID)
		return &messages.SearchRepoResponse{ErrorMessage: util.Pointer(err.Error())}
	}

	if req.Token == nil {
		paginationKey.Page = 1
	}

	query := fmt.Sprintf("%s org:%s fork:true", req.Search, req.OrgName)
	result, resp, searchErr := req.client.SearchRepositories(
		ctx,
		query,
		&ghapi.SearchOptions{ListOptions: ghapi.ListOptions{Page: paginationKey.Page, PerPage: limit}},
	)
	if searchErr != nil {
		slog.ErrorContext(ctx, "github repository search failed", "error", searchErr)
		return &messages.SearchRepoResponse{ErrorMessage: util.Pointer(searchErr.Error())}
	}
	var repos []string
	for _, repo := range result.Repositories {
		repos = append(repos, *repo.FullName)
	}
	var nextPaginationKey *SearchRepoPaginationKey
	if resp != nil && resp.NextPage != 0 {
		nextPaginationKey = &SearchRepoPaginationKey{
			Page: resp.NextPage,
		}
	}

	nextToken, err := NextToken(nextPaginationKey)
	if err != nil {
		slog.ErrorContext(ctx, "unable to generate next pagination token", "error", err)
		return &messages.SearchRepoResponse{ErrorMessage: util.Pointer("unable to generate pagination token")}
	}
	return &messages.SearchRepoResponse{Items: repos, NextToken: nextToken}
}

type pollerListRepoBranchesRequest struct {
	messages.ListRepoBranchesRequest
	client *github.Client
	err    error
}

func (req *pollerListRepoBranchesRequest) Init(p *Poller) {
	req.client, req.err = p.GetClientForConnectionID(req.ConnectionID)
}

type ListRepoBranchesPaginationKey struct {
	Page int
}

func (req *pollerListRepoBranchesRequest) Process(ctx context.Context) messages.Message {
	slog.InfoContext(
		ctx,
		"received ListRepoBranchesRequest message",
		"connection_id",
		req.ConnectionID,
		"org_name",
		req.OrgName,
		"repo_name",
		req.RepoName,
		"pagination_token",
		req.Token,
	)
	if req.err != nil {
		slog.ErrorContext(ctx, "unable to initialize github client", "error", req.err, "connection_id", req.ConnectionID)
		return &messages.ListRepoBranchesResponse{ErrorMessage: util.Pointer(req.err.Error())}
	}
	if req.OrgName == "" {
		slog.ErrorContext(ctx, "missing org name for branch listing", "connection_id", req.ConnectionID)
		return &messages.ListRepoBranchesResponse{ErrorMessage: util.Pointer("org name is required")}
	}
	if req.RepoName == "" {
		slog.ErrorContext(ctx, "missing repo name for branch listing", "connection_id", req.ConnectionID)
		return &messages.ListRepoBranchesResponse{ErrorMessage: util.Pointer("repo name is required")}
	}
	var paginationKey ListRepoBranchesPaginationKey
	limit, err := ParsePagination(req.MaxResults, req.Token, &paginationKey)
	if err != nil {
		slog.ErrorContext(ctx, "unable to parse pagination key", "error", err, "connection_id", req.ConnectionID)
		return &messages.ListRepoBranchesResponse{ErrorMessage: util.Pointer(err.Error())}
	}
	if req.Token == nil {
		paginationKey.Page = 1
	}
	branches, resp, err := req.client.ListBranches(
		ctx,
		req.OrgName,
		req.RepoName,
		&ghapi.BranchListOptions{ListOptions: ghapi.ListOptions{Page: paginationKey.Page, PerPage: limit}},
	)
	if err != nil {
		slog.ErrorContext(ctx, "github branch listing failed", "error", err)
		return &messages.ListRepoBranchesResponse{ErrorMessage: util.Pointer(err.Error())}
	}
	var branchNames []string
	for _, branch := range branches {
		name := branch.GetName()
		if name == "" {
			continue
		}
		if req.Search != nil && !strings.Contains(name, *req.Search) {
			continue
		}
		branchNames = append(branchNames, name)
	}
	var nextPaginationKey *ListRepoBranchesPaginationKey
	if resp != nil && resp.NextPage != 0 {
		nextPaginationKey = &ListRepoBranchesPaginationKey{Page: resp.NextPage}
	}
	nextToken, err := NextToken(nextPaginationKey)
	if err != nil {
		slog.ErrorContext(ctx, "unable to generate next pagination token", "error", err)
		return &messages.ListRepoBranchesResponse{ErrorMessage: util.Pointer("unable to generate pagination token")}
	}
	return &messages.ListRepoBranchesResponse{Items: branchNames, NextToken: nextToken}
}
