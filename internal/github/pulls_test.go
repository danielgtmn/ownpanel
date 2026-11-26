package github_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/github"
)

func TestPullRequestsAndComments(t *testing.T) {
	srv, c, repo, tok := installed(t, map[string]string{"pull_requests": "write"})
	ctx := context.Background()
	sha := srv.SetFiles(repo, "feature", map[string]string{"index.html": "x"})
	n := srv.OpenPull(repo, "main", "feature", 0, sha, "Change", "ann")

	pr, err := c.GetPullRequest(ctx, tok, "acme", "site", n)
	if err != nil || pr.Number != n || pr.State != github.PullOpen || pr.HeadSHA != sha || !pr.Private || pr.Merged {
		t.Fatalf("pull request: %+v %v", pr, err)
	}
	if _, err := c.GetPullRequest(ctx, tok, "acme", "site", 99); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("unknown pull request: %v", err)
	}
	if _, err := c.GetPullRequest(ctx, tok, "acme", "site", 0); !errors.Is(err, github.ErrInvalid) {
		t.Fatalf("number 0: %v", err)
	}

	srv.AddComment(repo, n, "mallory", "first")
	id, err := c.CreateIssueComment(ctx, tok, "acme", "site", n, "<!-- m -->\nhello")
	if err != nil || id == 0 {
		t.Fatalf("create: %d %v", id, err)
	}
	list, err := c.IssueComments(ctx, tok, "acme", "site", n)
	if err != nil || len(list) != 2 || list[1].ID != id || list[1].UserType != "Bot" || !strings.HasSuffix(list[1].UserLogin, "[bot]") ||
		list[1].AppID == 0 || list[0].UserLogin != "mallory" || list[0].AppID != 0 {
		t.Fatalf("comments: %+v %v", list, err)
	}
	if err := c.UpdateIssueComment(ctx, tok, "acme", "site", id, "changed"); err != nil {
		t.Fatal(err)
	}
	if got := srv.Comments(repo, n); got[1].Body != "changed" || got[1].Edits != 1 {
		t.Fatalf("after update: %+v", got)
	}
	// An App may only change its own comments.
	if err := c.UpdateIssueComment(ctx, tok, "acme", "site", list[0].ID, "x"); !errors.Is(err, github.ErrForbidden) {
		t.Fatalf("someone else's comment: %v", err)
	}
	for _, body := range []string{"", strings.Repeat("x", github.MaxCommentBytes+1), "a\x00b"} {
		if _, err := c.CreateIssueComment(ctx, tok, "acme", "site", n, body); !errors.Is(err, github.ErrInvalid) {
			t.Errorf("body of %d bytes: %v", len(body), err)
		}
	}

	// Without the permission, GitHub refuses.
	_, c2, repo2, ro := installed(t, map[string]string{"contents": "read"})
	_ = repo2
	if _, err := c2.IssueComments(ctx, ro, "acme", "site", 1); err == nil {
		t.Fatal("read the comments without the permission")
	}
}
