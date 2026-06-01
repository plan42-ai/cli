package githubevents

import (
	"strings"

	"github.com/google/go-github/v81/github"
	"github.com/google/uuid"
	"github.com/plan42-ai/github-event-handlers/handlers"
)

// translateIssueComment converts a go-github Events API envelope and parsed
// IssueCommentEvent payload into the shared library's IssueCommentEvent.
// Field mapping follows design.md Section 11.2 (runner source column).
func translateIssueComment(env *github.Event, payload *github.IssueCommentEvent) *handlers.IssueCommentEvent {
	return &handlers.IssueCommentEvent{
		EventBase: handlers.EventBase{DeliveryID: uuid.New().String()},
		Action:    payload.GetAction(),
		Comment: handlers.Comment{
			Body:  payload.GetComment().GetBody(),
			Login: payload.GetComment().GetUser().GetLogin(),
		},
		Issue: handlers.Issue{
			Number:        payload.GetIssue().GetNumber(),
			State:         payload.GetIssue().GetState(),
			IsPullRequest: payload.GetIssue().IsPullRequest(),
		},
		Repository: repoFromEnvelope(env),
	}
}

// repoFromEnvelope splits the Events API envelope's repo name ("owner/name")
// into the shared library's Repository struct. The Events API envelope's
// Repo.Name carries the "owner/name" form; FullName and Owner are nil.
func repoFromEnvelope(env *github.Event) handlers.Repository {
	full := env.GetRepo().GetName()
	org, name, _ := strings.Cut(full, "/")
	return handlers.Repository{
		FullName: full,
		Org:      org,
		Name:     name,
	}
}
