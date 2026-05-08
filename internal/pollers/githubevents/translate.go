package githubevents

import (
	"strings"
	"time"

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

// translatePullRequestReview converts Events API envelopes plus
// PullRequestReview payloads into shared library events per design
// Section 11.4 (runner source column).
func translatePullRequestReview(env *github.Event, payload *github.PullRequestReviewEvent) *githubeventslib.PullRequestReviewEvent {
	review := payload.GetReview()
	var reviewBody *string
	if review != nil && review.Body != nil {
		bodyCopy := review.GetBody()
		reviewBody = &bodyCopy
	}

	return &githubeventslib.PullRequestReviewEvent{
		EventBase: githubeventslib.EventBase{DeliveryID: uuid.New().String()},
		Action:    payload.GetAction(),
		Review: githubeventslib.Review{
			Body:  reviewBody,
			Login: review.GetUser().GetLogin(),
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

// translatePullRequest converts a go-github Events API envelope and parsed
// PullRequestEvent payload into the shared library's PullRequestEvent.
// Field mapping follows design.md Section 11.5 (runner source column).
func translatePullRequest(env *github.Event, payload *github.PullRequestEvent) *githubeventslib.PullRequestEvent {
	return &githubeventslib.PullRequestEvent{
		EventBase: githubeventslib.EventBase{DeliveryID: uuid.New().String()},
		Action:    payload.GetAction(),
		Number:    payload.GetNumber(),
		PullRequest: githubeventslib.PullRequest{
			ID:        payload.GetPullRequest().GetID(),
			Number:    payload.GetPullRequest().GetNumber(),
			State:     payload.GetPullRequest().GetState(),
			Merged:    payload.GetPullRequest().GetMerged(),
			Draft:     payload.GetPullRequest().GetDraft(),
			HTMLURL:   payload.GetPullRequest().GetHTMLURL(),
			UpdatedAt: timestampToTimePtr(payload.GetPullRequest().UpdatedAt),
			Login:     payload.GetPullRequest().GetUser().GetLogin(),
		},
		Repository: repoFromEnvelope(env),
	}
}

// timestampToTimePtr converts a *github.Timestamp to a *time.Time. Returns nil
// if the timestamp is nil or its embedded Time is the Go zero value.
func timestampToTimePtr(ts *github.Timestamp) *time.Time {
	if ts == nil || ts.IsZero() {
		return nil
	}
	t := ts.Time
	return &t
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
