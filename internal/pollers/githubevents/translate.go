package githubevents

import (
	"strings"

	"github.com/google/go-github/v81/github"
	"github.com/google/uuid"
	githubeventslib "github.com/plan42-ai/github-event-handlers"
)

// translateIssueComment converts a go-github Events API envelope and parsed
// IssueCommentEvent payload into the shared library's IssueCommentEvent.
// Field mapping follows design.md Section 11.2 (runner source column).
func translateIssueComment(env *github.Event, payload *github.IssueCommentEvent) *githubeventslib.IssueCommentEvent {
	return &githubeventslib.IssueCommentEvent{
		EventBase: githubeventslib.EventBase{DeliveryID: uuid.New().String()},
		Action:    payload.GetAction(),
		Comment: githubeventslib.Comment{
			Body:  payload.GetComment().GetBody(),
			Login: payload.GetComment().GetUser().GetLogin(),
		},
		Issue: githubeventslib.Issue{
			Number:        payload.GetIssue().GetNumber(),
			State:         payload.GetIssue().GetState(),
			IsPullRequest: payload.GetIssue().IsPullRequest(),
		},
		Repository: repoFromEnvelope(env),
	}
}

// translatePullRequestReviewComment converts Events API envelopes plus
// PullRequestReviewComment payloads into shared library events per design
// Section 11.3 (runner source column).
func translatePullRequestReviewComment(env *github.Event, payload *github.PullRequestReviewCommentEvent) *githubeventslib.PullRequestReviewCommentEvent {
	return &githubeventslib.PullRequestReviewCommentEvent{
		EventBase: githubeventslib.EventBase{DeliveryID: uuid.New().String()},
		Action:    payload.GetAction(),
		Comment: githubeventslib.Comment{
			Body:  payload.GetComment().GetBody(),
			Login: payload.GetComment().GetUser().GetLogin(),
		},
		PullRequest: githubeventslib.PullRequest{
			ID:     payload.GetPullRequest().GetID(),
			Number: payload.GetPullRequest().GetNumber(),
			State:  payload.GetPullRequest().GetState(),
			Login:  payload.GetPullRequest().GetUser().GetLogin(),
		},
		Repository: repoFromEnvelope(env),
	}
}

// repoFromEnvelope splits the Events API envelope's repo name ("owner/name")
// into the shared library's Repository struct. The Events API envelope's
// Repo.Name carries the "owner/name" form; FullName and Owner are nil.
func repoFromEnvelope(env *github.Event) githubeventslib.Repository {
	full := env.GetRepo().GetName()
	org, name, _ := strings.Cut(full, "/")
	return githubeventslib.Repository{
		FullName: full,
		Org:      org,
		Name:     name,
	}
}
