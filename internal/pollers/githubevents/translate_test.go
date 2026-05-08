package githubevents

import (
	"testing"

	"github.com/google/go-github/v81/github"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslateIssueComment_AllFields(t *testing.T) {
	t.Parallel()

	env := &github.Event{
		Repo: &github.Repository{
			Name: github.Ptr("myorg/myrepo"),
		},
	}

	payload := &github.IssueCommentEvent{
		Action: github.Ptr("created"),
		Issue: &github.Issue{
			Number:           github.Ptr(42),
			State:            github.Ptr("open"),
			PullRequestLinks: &github.PullRequestLinks{},
		},
		Comment: &github.IssueComment{
			Body: github.Ptr("/Plan42 run tests"),
			User: &github.User{Login: github.Ptr("alice")},
		},
	}

	evt := translateIssueComment(env, payload)

	// Delivery ID must be a valid UUID.
	_, err := uuid.Parse(evt.GetDeliveryID())
	require.NoError(t, err, "delivery ID should be a valid UUID")
	assert.NotEmpty(t, evt.GetDeliveryID())

	// EventType
	assert.Equal(t, "issue_comment", evt.EventType())

	// Action
	assert.Equal(t, "created", evt.Action)

	// Comment
	assert.Equal(t, "/Plan42 run tests", evt.Comment.Body)
	assert.Equal(t, "alice", evt.Comment.Login)

	// Issue
	assert.Equal(t, 42, evt.Issue.Number)
	assert.Equal(t, "open", evt.Issue.State)
	assert.True(t, evt.Issue.IsPullRequest)

	// Repository (split from envelope)
	assert.Equal(t, "myorg/myrepo", evt.Repository.FullName)
	assert.Equal(t, "myorg", evt.Repository.Org)
	assert.Equal(t, "myrepo", evt.Repository.Name)
}

func TestTranslateIssueComment_NotAPullRequest(t *testing.T) {
	t.Parallel()

	env := &github.Event{
		Repo: &github.Repository{
			Name: github.Ptr("owner/repo"),
		},
	}

	payload := &github.IssueCommentEvent{
		Action: github.Ptr("created"),
		Issue: &github.Issue{
			Number: github.Ptr(10),
			State:  github.Ptr("closed"),
			// PullRequestLinks is nil -> IsPullRequest() returns false
		},
		Comment: &github.IssueComment{
			Body: github.Ptr("just a comment"),
			User: &github.User{Login: github.Ptr("bob")},
		},
	}

	evt := translateIssueComment(env, payload)

	assert.False(t, evt.Issue.IsPullRequest)
	assert.Equal(t, "closed", evt.Issue.State)
	assert.Equal(t, 10, evt.Issue.Number)
	assert.Equal(t, "bob", evt.Comment.Login)
}

func TestTranslateIssueComment_DeliveryIDIsUnique(t *testing.T) {
	t.Parallel()

	env := &github.Event{
		Repo: &github.Repository{Name: github.Ptr("o/r")},
	}
	payload := &github.IssueCommentEvent{
		Action:  github.Ptr("created"),
		Issue:   &github.Issue{},
		Comment: &github.IssueComment{User: &github.User{}},
	}

	evt1 := translateIssueComment(env, payload)
	evt2 := translateIssueComment(env, payload)

	assert.NotEqual(t, evt1.GetDeliveryID(), evt2.GetDeliveryID(),
		"each call should generate a fresh UUID")
}

func TestRepoFromEnvelope(t *testing.T) {
	t.Parallel()

	const noSlashRepo = "single"

	tests := []struct {
		name     string
		repoName string
		wantFull string
		wantOrg  string
		wantName string
	}{
		{
			name:     "standard owner/repo",
			repoName: "myorg/myrepo",
			wantFull: "myorg/myrepo",
			wantOrg:  "myorg",
			wantName: "myrepo",
		},
		{
			name:     "different org",
			repoName: "acme-corp/backend-api",
			wantFull: "acme-corp/backend-api",
			wantOrg:  "acme-corp",
			wantName: "backend-api",
		},
		{
			name:     "no slash",
			repoName: noSlashRepo,
			wantFull: noSlashRepo,
			wantOrg:  noSlashRepo,
			wantName: "",
		},
		{
			name:     "multiple slashes",
			repoName: "a/b/c",
			wantFull: "a/b/c",
			wantOrg:  "a",
			wantName: "b/c",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := &github.Event{
				Repo: &github.Repository{Name: github.Ptr(tt.repoName)},
			}
			repo := repoFromEnvelope(env)
			assert.Equal(t, tt.wantFull, repo.FullName)
			assert.Equal(t, tt.wantOrg, repo.Org)
			assert.Equal(t, tt.wantName, repo.Name)
		})
	}
}

func TestTranslateIssueComment_NilFields(t *testing.T) {
	t.Parallel()

	// Verify the translator handles minimal go-github fields gracefully
	// via Get*() accessors returning zero values. Issue must be non-nil
	// because IsPullRequest() is a value-receiver method on github.Issue.
	env := &github.Event{
		Repo: &github.Repository{},
	}
	payload := &github.IssueCommentEvent{
		Issue:   &github.Issue{},
		Comment: &github.IssueComment{},
	}

	evt := translateIssueComment(env, payload)

	_, err := uuid.Parse(evt.GetDeliveryID())
	require.NoError(t, err)

	assert.Equal(t, "", evt.Action)
	assert.Equal(t, "", evt.Comment.Body)
	assert.Equal(t, "", evt.Comment.Login)
	assert.Equal(t, 0, evt.Issue.Number)
	assert.Equal(t, "", evt.Issue.State)
	assert.False(t, evt.Issue.IsPullRequest)
	assert.Equal(t, "", evt.Repository.FullName)
}
