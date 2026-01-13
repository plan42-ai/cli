package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/go-github/v81/github"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42/messages"
)

const (
	defaultGithubURL = "https://github.com"
	maxResultsErrMsg = "maxResults must be between 1 and 100"
)

type pollerListOrgsForGithubConnectionRequest struct {
	messages.ListOrgsForGithubConnectionRequest
	client *github.Client
	err    error
}

func (req *pollerListOrgsForGithubConnectionRequest) Init(p *Poller) {
	req.client, req.err = p.GetClientForConnectionID(req.ConnectionID)
}

func (req *pollerListOrgsForGithubConnectionRequest) Process(ctx context.Context) messages.Message {
	slog.InfoContext(ctx, "received ListOrgsForGithubConnectionRequest message", "connection_id", req.ConnectionID, "pagination_token", req.Token)
	if req.err != nil {
		slog.ErrorContext(ctx, "unable to initialize github client", "error", req.err, "connection_id", req.ConnectionID)
		return &messages.ListOrgsForGithubConnectionResponse{ErrorMessage: util.Pointer(req.err.Error())}
	}
	maxResults, err := parseMaxResults(req.MaxResults)
	if err != nil {
		slog.ErrorContext(ctx, "invalid maxResults", "error", err)
		return &messages.ListOrgsForGithubConnectionResponse{ErrorMessage: util.Pointer(err.Error())}
	}
	page := 1
	if req.Token != nil {
		page, err = strconv.Atoi(*req.Token)
		if err != nil || page < 1 {
			slog.ErrorContext(ctx, "invalid pagination token in request")
			return &messages.ListOrgsForGithubConnectionResponse{ErrorMessage: util.Pointer("invalid pagination token")}
		}
	}
	orgs, resp, err := req.client.Organizations.List(
		ctx,
		"",
		&github.ListOptions{Page: page, PerPage: maxResults},
	)
	if err != nil {
		slog.ErrorContext(ctx, "call to organizations.List failed", "error", err)
		return &messages.ListOrgsForGithubConnectionResponse{ErrorMessage: util.Pointer(err.Error())}
	}
	var orgNames []string
	for _, org := range orgs {
		orgNames = append(orgNames, *org.Login)
	}
	slog.InfoContext(ctx, "call to organizations.List succeeded", "n_orgs", len(orgNames))
	var nextToken *string
	if resp != nil && resp.NextPage != 0 {
		next := strconv.Itoa(resp.NextPage)
		nextToken = &next
	}
	return &messages.ListOrgsForGithubConnectionResponse{Items: orgNames, NextToken: nextToken}
}

type pollerSearchRepoRequest struct {
	messages.SearchRepoRequest
	client *github.Client
	err    error
}

func (req *pollerSearchRepoRequest) Init(p *Poller) {
	req.client, req.err = p.GetClientForConnectionID(req.ConnectionID)
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
	limit, err := parseMaxResults(req.MaxResults)
	if err != nil {
		slog.ErrorContext(ctx, "invalid maxResults", "error", err)
		return &messages.SearchRepoResponse{ErrorMessage: util.Pointer(err.Error())}
	}
	page := 1
	if req.Token != nil {
		page, err = strconv.Atoi(*req.Token)
		if err != nil || page < 1 {
			slog.ErrorContext(ctx, "invalid pagination token in request")
			return &messages.SearchRepoResponse{ErrorMessage: util.Pointer("invalid pagination token")}
		}
	}
	query := fmt.Sprintf("%s org:%s fork:true", req.Search, req.OrgName)
	result, resp, searchErr := req.client.Search.Repositories(
		ctx,
		query,
		&github.SearchOptions{ListOptions: github.ListOptions{Page: page, PerPage: limit}},
	)
	if searchErr != nil {
		slog.ErrorContext(ctx, "github repository search failed", "error", searchErr)
		return &messages.SearchRepoResponse{ErrorMessage: util.Pointer(searchErr.Error())}
	}
	var repos []string
	for _, repo := range result.Repositories {
		repos = append(repos, *repo.FullName)
	}
	var nextToken *string
	if resp != nil && resp.NextPage != 0 {
		next := strconv.Itoa(resp.NextPage)
		nextToken = &next
	}
	return &messages.SearchRepoResponse{Items: repos, NextToken: nextToken}
}

func parseMaxResults(requested *int) (int, error) {
	if requested == nil {
		return 10, nil
	}
	if *requested < 1 || *requested > 100 {
		return 0, errors.New(maxResultsErrMsg)
	}
	return *requested, nil
}
