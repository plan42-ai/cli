package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	ghapi "github.com/google/go-github/v81/github"
	"golang.org/x/oauth2"

	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42/messages"
)

const (
	DefaultGithubURL        = "https://github.com"
	defaultGithubGraphqlURL = "https://api.github.com/graphql"
)

type Client struct {
	restClient *ghapi.Client
	httpClient *http.Client
	graphqlURL string
}

func NewClient(token string, baseURL string) (*Client, error) {
	if token == "" {
		return nil, fmt.Errorf("missing github token")
	}

	httpClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	rest := ghapi.NewClient(httpClient)

	if baseURL != "" && baseURL != DefaultGithubURL {
		configured, err := rest.WithEnterpriseURLs(baseURL, baseURL)
		if err != nil {
			return nil, fmt.Errorf("unable to configure github client: %w", err)
		}
		rest = configured
	}

	return &Client{
		restClient: rest,
		httpClient: httpClient,
		graphqlURL: graphqlURL(baseURL),
	}, nil
}

func graphqlURL(baseURL string) string {
	if baseURL == "" || baseURL == DefaultGithubURL {
		return defaultGithubGraphqlURL
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return defaultGithubGraphqlURL
	}

	root := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	return root.JoinPath("api", "graphql").String()
}

func (c *Client) GetCurrentUser(ctx context.Context) (*ghapi.User, *ghapi.Response, error) {
	return c.restClient.Users.Get(ctx, "")
}

func (c *Client) ListOrganizations(ctx context.Context, page int, perPage int) ([]*ghapi.Organization, *ghapi.Response, error) {
	return c.restClient.Organizations.List(ctx, "", &ghapi.ListOptions{Page: page, PerPage: perPage})
}

func (c *Client) SearchRepositories(ctx context.Context, query string, opts *ghapi.SearchOptions) (*ghapi.RepositoriesSearchResult, *ghapi.Response, error) {
	return c.restClient.Search.Repositories(ctx, query, opts)
}

func (c *Client) ListBranches(ctx context.Context, owner string, repo string, opts *ghapi.BranchListOptions) ([]*ghapi.Branch, *ghapi.Response, error) {
	return c.restClient.Repositories.ListBranches(ctx, owner, repo, opts)
}

func (c *Client) GetPRFeedBack(ctx context.Context, org string, repo string, prNum int) ([]messages.PRFeedback, error) {
	var err error
	var ret []messages.PRFeedback

	ret, err = c.getReviewThreadFeedback(ctx, org, repo, prNum, ret)
	if err != nil {
		return nil, err
	}

	ret, err = c.getIssueCommentFeedback(ctx, org, repo, prNum, ret)
	if err != nil {
		return nil, err
	}

	ret, err = c.getReviewCommentFeedback(ctx, org, repo, prNum, ret)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (c *Client) getReviewThreadFeedback(ctx context.Context, org string, repo string, prNum int, ret []messages.PRFeedback) ([]messages.PRFeedback, error) {
	req := request(
		reviewThreadQuery,
		reviewThreadVariables{
			Owner: org,
			Name:  repo,
			PRNum: prNum,
		},
	)

	for {
		var resp reviewThreadResponse

		err := c.queryGraphQL(ctx, &req, &resp)
		if err != nil {
			return nil, err
		}

		for _, thread := range resp.Data.Repository.PullRequest.ReviewThreads.Nodes {
			comments, err := c.GetThreadComments(ctx, thread.ID)
			if err != nil {
				return nil, err
			}
			if len(comments) == 0 {
				continue
			}
			ret = append(ret, messages.PRFeedback{
				ID:         thread.ID,
				IsResolved: thread.IsResolved,
				Comments:   comments,
			})
		}

		if !resp.Data.Repository.PullRequest.ReviewThreads.PageInfo.HasNextPage {
			break
		}
		req.Variables.Cursor = resp.Data.Repository.PullRequest.ReviewThreads.PageInfo.EndCursor
	}
	return ret, nil
}

func (c *Client) getIssueCommentFeedback(ctx context.Context, org string, repo string, prNum int, ret []messages.PRFeedback) ([]messages.PRFeedback, error) {
	req := request(
		issueCommentsQuery,
		issueCommentVariables{Owner: org, Name: repo, PRNum: prNum},
	)

	for {
		var resp issueCommentsResponse
		if err := c.queryGraphQL(ctx, &req, &resp); err != nil {
			return nil, err
		}

		comments := resp.Data.Repository.PullRequest.Comments
		for _, comment := range comments.Nodes {
			user := ""
			if comment.Author != nil {
				user = comment.Author.Login
			}
			if isPlan42Comment(user, comment.Body) {
				continue
			}
			ret = append(ret, messages.PRFeedback{
				ID: comment.ID,
				Comments: []messages.Comment{{
					User:            user,
					Body:            comment.Body,
					Date:            comment.CreatedAt,
					IsMinimized:     comment.IsMinimized,
					MinimizedReason: comment.MinimizedReason,
				}},
			})
		}

		if !comments.PageInfo.HasNextPage {
			break
		}
		req.Variables.Cursor = comments.PageInfo.EndCursor
	}

	return ret, nil
}

func (c *Client) getReviewCommentFeedback(ctx context.Context, org string, repo string, prNum int, ret []messages.PRFeedback) ([]messages.PRFeedback, error) {
	req := request(
		reviewCommentsQuery,
		reviewCommentVariables{Owner: org, Name: repo, PRNum: prNum},
	)

	for {
		var resp reviewCommentsResponse
		if err := c.queryGraphQL(ctx, &req, &resp); err != nil {
			return nil, err
		}

		reviews := resp.Data.Repository.PullRequest.Reviews
		for _, review := range reviews.Nodes {
			if review.Body == "" {
				continue
			}
			user := ""
			if review.Author != nil {
				user = review.Author.Login
			}
			if isPlan42Comment(user, review.Body) {
				continue
			}
			commitHash := ""
			if review.Commit != nil {
				commitHash = review.Commit.Oid
			}
			ret = append(ret, messages.PRFeedback{
				ID: review.ID,
				Comments: []messages.Comment{{
					User:       user,
					Body:       review.Body,
					Date:       review.CreatedAt,
					CommitHash: commitHash,
				}},
			})
		}

		if !reviews.PageInfo.HasNextPage {
			break
		}
		req.Variables.Cursor = reviews.PageInfo.EndCursor
	}

	return ret, nil
}

func isPlan42Comment(user string, body string) bool {
	if !strings.HasPrefix(strings.ToLower(user), "plan42") {
		return false
	}
	unescaped := html.UnescapeString(body)
	return strings.HasPrefix(unescaped, "<!-- event-horizon")
}

func request[T any](query string, variables T) graphQLRequest[T] {
	return graphQLRequest[T]{
		Query:     query,
		Variables: variables,
	}
}

func (c *Client) GetThreadComments(ctx context.Context, threadID string) ([]messages.Comment, error) {
	req := request(
		commentQuery,
		commentVariables{
			ThreadID: threadID,
		},
	)

	var ret []messages.Comment
	for {
		var resp commentQueryResult

		err := c.queryGraphQL(ctx, &req, &resp)
		if err != nil {
			return nil, err
		}
		for _, c := range resp.Data.Node.Comments.Nodes {
			user := c.Author.Login
			if isPlan42Comment(user, c.Body) {
				continue
			}
			ret = append(
				ret,
				messages.Comment{
					User:            user,
					Body:            c.Body,
					Date:            c.CreatedAt,
					DiffHunk:        c.DiffHunk,
					Path:            c.Path,
					StartLine:       c.StartLine,
					OrigStartLine:   c.OriginalStartLine,
					CommitHash:      c.Commit.Oid,
					IsMinimized:     c.IsMinimized,
					MinimizedReason: c.MinimizedReason,
				},
			)
		}
		if !resp.Data.Node.Comments.PageInfo.HasNextPage {
			break
		}
		req.Variables.Cursor = resp.Data.Node.Comments.PageInfo.EndCursor
	}
	return ret, nil
}

type reviewThreadVariables struct {
	Owner  string `json:"owner"`
	Name   string `json:"name"`
	PRNum  int    `json:"prNum"`
	Cursor string `json:"cursor"`
}

type graphQLRequest[T any] struct {
	Query     string `json:"query"`
	Variables T      `json:"variables"`
}

type reviewThreadResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						ID         string `json:"id"`
						IsResolved bool   `json:"isResolved"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

const reviewThreadQuery = `
query($owner:String!, $name:String!, $prNum:Int!, $cursor:String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $prNum) {
      reviewThreads(first: 100 , after: $cursor) {
        pageInfo { hasNextPage endCursor } 
        nodes {
          id
          isResolved
        }
      }
    }
  }
}
`

type commentQueryResult struct {
	Data struct {
		Node struct {
			Comments struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					Author struct {
						Login string `json:"login"`
					} `json:"author"`
					Body            string    `json:"body"`
					CreatedAt       time.Time `json:"createdAt"`
					IsMinimized     bool      `json:"isMinimized"`
					MinimizedReason string    `json:"minimizedReason"`
					DiffHunk        string    `json:"diffHunk"`
					Path            string    `json:"path"`
					Commit          struct {
						Oid string `json:"oid"`
					} `json:"commit"`
					StartLine         int `json:"startLine"`
					OriginalStartLine int `json:"originalStartLine"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"node"`
	} `json:"data"`
}

type commentVariables struct {
	ThreadID string `json:"threadID"`
	Cursor   string `json:"cursor"`
}

const commentQuery = `
query($threadID:ID!, $cursor:String) {
  node(id: $threadID) {
    ... on PullRequestReviewThread {
      comments(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          author { login }
          body
          createdAt
          isMinimized
          minimizedReason
          diffHunk
          path
          commit { oid }
          startLine
          originalStartLine
        }
      }
    }
  }
}
`

type issueCommentVariables struct {
	Owner  string `json:"owner"`
	Name   string `json:"name"`
	PRNum  int    `json:"prNum"`
	Cursor string `json:"cursor"`
}

type issueCommentsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				Comments struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						ID     string `json:"id"`
						Author *struct {
							Login string `json:"login"`
						} `json:"author"`
						Body            string    `json:"body"`
						CreatedAt       time.Time `json:"createdAt"`
						IsMinimized     bool      `json:"isMinimized"`
						MinimizedReason string    `json:"minimizedReason"`
					} `json:"nodes"`
				} `json:"comments"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

const issueCommentsQuery = `
query($owner:String!, $name:String!, $prNum:Int!, $cursor:String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $prNum) {
      comments(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          author { login }
          body
          createdAt
          isMinimized
          minimizedReason
        }
      }
    }
  }
}
`

type reviewCommentVariables struct {
	Owner  string `json:"owner"`
	Name   string `json:"name"`
	PRNum  int    `json:"prNum"`
	Cursor string `json:"cursor"`
}

type reviewCommentsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				Reviews struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						ID     string `json:"id"`
						Author *struct {
							Login string `json:"login"`
						} `json:"author"`
						Body      string    `json:"body"`
						CreatedAt time.Time `json:"createdAt"`
						Commit    *struct {
							Oid string `json:"oid"`
						} `json:"commit"`
					} `json:"nodes"`
				} `json:"reviews"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

const reviewCommentsQuery = `
query($owner:String!, $name:String!, $prNum:Int!, $cursor:String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $prNum) {
      reviews(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          author { login }
          body
          createdAt
          commit { oid }
        }
      }
    }
  }
}
`

func (c *Client) queryGraphQL(ctx context.Context, req any, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphqlURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token())
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")
	httpReq.Header.Set("Accept", "application/vnd.github+json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer util.Close(httpResp.Body)
	if httpResp.StatusCode != http.StatusOK {
		return fmt.Errorf("github graphql query returned status %d", httpResp.StatusCode)
	}

	decoder := json.NewDecoder(httpResp.Body)
	return decoder.Decode(resp)
}

func (c *Client) token() string {
	transport := c.httpClient.Transport
	if transport == nil {
		return ""
	}
	if t, ok := transport.(*oauth2.Transport); ok {
		token, _ := t.Source.Token()
		if token != nil {
			return token.AccessToken
		}
	}
	return ""
}
