package poller

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/go-github/v81/github"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42/messages"
)

type pollerListOrgsForGithubConnectionRequest struct {
	messages.ListOrgsForGithubConnectionRequest
	GithubURL   string
	GithubToken string
}

func (req *pollerListOrgsForGithubConnectionRequest) Init(p *Poller) {
	cnn := p.connectionIdx[req.ConnectionID]
	if cnn != nil {
		req.GithubToken = cnn.Token
		req.GithubURL = cnn.URL
	}
}

func (req *pollerListOrgsForGithubConnectionRequest) Process(ctx context.Context) messages.Message {
	slog.InfoContext(ctx, "received ListOrgsForGithubConnectionRequest message", "connection_id", req.ConnectionID, "pagination_token", req.Token)
	client := github.NewClient(nil).WithAuthToken(req.GithubToken)

	var err error
	if req.GithubURL != "https://github.com" {
		slog.InfoContext(ctx, "setting custom github url")
		client, err = client.WithEnterpriseURLs(req.GithubURL, req.GithubURL)

		if err != nil {
			slog.ErrorContext(ctx, "unable to configure github client", "error", err)
			return &messages.ListOrgsForGithubConnectionResponse{
				ErrorMessage: util.Pointer(fmt.Sprintf("unable to configure github client: %v", err.Error())),
			}
		}
	}

	maxResults := 10
	if req.MaxResults != nil {
		maxResults = *req.MaxResults
	}

	page := 1
	if req.Token != nil {
		page, err = strconv.Atoi(*req.Token)
		if err != nil {
			slog.ErrorContext(ctx, "invalid pagination token in request")
			return &messages.ListOrgsForGithubConnectionResponse{
				ErrorMessage: util.Pointer("invalid pagination token"),
			}
		}
	}

	orgs, _, err := client.Organizations.List(
		ctx,
		"",
		&github.ListOptions{
			Page:    page,
			PerPage: maxResults,
		},
	)

	if err != nil {
		slog.ErrorContext(ctx, "call to organizations.List failed", "error", err)
		return &messages.ListOrgsForGithubConnectionResponse{
			ErrorMessage: util.Pointer(err.Error()),
		}
	}

	var orgNames []string
	for _, org := range orgs {
		orgNames = append(orgNames, *org.Login)
	}

	slog.InfoContext(ctx, "call to organizations.ListOrgs succeeded", "n_orgs", len(orgNames))

	var nextToken *string
	if len(orgNames) == maxResults {
		nextToken = util.Pointer(strconv.Itoa(page + 1))
	}

	return &messages.ListOrgsForGithubConnectionResponse{
		Items:     orgNames,
		NextToken: nextToken,
	}
}
