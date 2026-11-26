package github_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/github/githubtest"
)

// installed is a fake with acme/site installed and a token for it.
func installed(t *testing.T, perms map[string]string) (*githubtest.Server, *github.Client, int64, github.Token) {
	t.Helper()
	srv, c := newFake(t)
	ctx := context.Background()
	repo := srv.AddRepo("acme", "site", true)
	other := srv.AddRepo("acme", "other", true)
	code, _ := srv.CreateCode(manifest(), "acme")
	creds, err := c.ConvertManifest(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	app, _ := c.AsApp(creds.ClientID, creds.PrivateKey)
	inst := srv.AddInstallation(creds.ID, "acme", "Organization", nil)
	tok, err := app.InstallationToken(ctx, inst, github.TokenRequest{RepositoryIDs: []int64{repo}, Permissions: perms})
	if err != nil {
		t.Fatal(err)
	}
	_ = other
	return srv, c, repo, tok
}

func names(t *testing.T, r io.Reader) []string {
	t.Helper()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h.Name)
	}
}

func TestTarball(t *testing.T) {
	srv, c, repo, tok := installed(t, map[string]string{"contents": "read", "metadata": "read"})
	ctx := context.Background()
	sha := srv.SetFiles(repo, "main", map[string]string{"index.html": "<h1>hi</h1>", "src/a.js": "x"})
	if srv.Head(repo, "main") != sha {
		t.Fatal("head")
	}
	b, err := c.GetBranch(ctx, tok, "acme", "site", "main")
	if err != nil || b.SHA != sha {
		t.Fatalf("branch: %+v %v", b, err)
	}
	rc, err := c.Tarball(ctx, tok, "acme", "site", sha, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got := names(t, rc)
	rc.Close()
	prefix := "acme-site-" + sha[:7] + "/"
	if len(got) < 3 || got[0] != prefix || !strings.HasPrefix(got[len(got)-1], prefix) {
		t.Fatalf("names: %v", got)
	}

	// Too large: the stream fails.
	rc, err = c.Tarball(ctx, tok, "acme", "site", sha, 10)
	if err == nil {
		_, err = io.ReadAll(rc)
		rc.Close()
	}
	if !errors.Is(err, github.ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}

	// Unknown commit, other repository, bad ids.
	if _, err := c.Tarball(ctx, tok, "acme", "site", strings.Repeat("a", 40), 1<<20); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("unknown commit: %v", err)
	}
	if _, err := c.Tarball(ctx, tok, "acme", "other", sha, 1<<20); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("other repository: %v", err)
	}
	if _, err := c.Tarball(ctx, tok, "acme", "site", "main", 1<<20); !errors.Is(err, github.ErrInvalid) {
		t.Fatalf("not a commit id: %v", err)
	}
	if _, err := c.Tarball(ctx, tok, "acme", "../x", sha, 1<<20); !errors.Is(err, github.ErrInvalid) {
		t.Fatalf("bad name: %v", err)
	}

	// The time limit is the caller's context.
	srv.TarballDelay(2 * time.Second)
	tctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	rc, err = c.Tarball(tctx, tok, "acme", "site", sha, 1<<20)
	if err == nil {
		_, err = io.ReadAll(rc)
		rc.Close()
	}
	if err == nil {
		t.Fatal("no timeout")
	}
	if strings.Contains(err.Error(), tok.Value()) || strings.Contains(err.Error(), "token=") {
		t.Fatalf("error leaks: %v", err)
	}
}

func TestTarballNeedsContents(t *testing.T) {
	srv, c, repo, tok := installed(t, map[string]string{"metadata": "read"})
	sha := srv.SetFiles(repo, "main", map[string]string{"index.html": "x"})
	if _, err := c.Tarball(context.Background(), tok, "acme", "site", sha, 1<<20); !errors.Is(err, github.ErrForbidden) {
		t.Fatalf("%v", err)
	}
}

// A redirect to anything but https (or loopback http for a loopback
// GitHub) is refused, and the token is never sent to the download address.
func TestTarballRedirects(t *testing.T) {
	var sawAuth bool
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = sawAuth || r.Header.Get("Authorization") != ""
		w.Write([]byte("data"))
	}))
	defer dl.Close()
	target := dl.URL + "/x.tar.gz"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer api.Close()
	c, err := github.New(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	tok := github.NewToken("ghs_secret", time.Now().Add(time.Hour))
	sha := strings.Repeat("b", 40)
	rc, err := c.Tarball(context.Background(), tok, "acme", "site", sha, 100)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if sawAuth {
		t.Fatal("the token went to the download address")
	}
	for _, bad := range []string{"ftp://example.com/x", "http://example.com/x", "https://user:pw@example.com/x"} {
		target = bad
		if _, err := c.Tarball(context.Background(), tok, "acme", "site", sha, 100); !errors.Is(err, github.ErrBadAnswer) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestSetCommitStatus(t *testing.T) {
	srv, c, repo, tok := installed(t, map[string]string{"statuses": "write"})
	sha := srv.SetFiles(repo, "main", map[string]string{"index.html": "x"})
	long := strings.Repeat("é", 200)
	err := c.SetCommitStatus(context.Background(), tok, "acme", "site", sha, github.CommitStatus{
		State: github.StatusPending, TargetURL: "https://panel.example.com/#/sites/blog", Description: long, Context: "ownpanel",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := srv.Statuses()
	if len(got) != 1 || got[0].State != "pending" || got[0].SHA != sha || len([]rune(got[0].Description)) != 140 || got[0].Context != "ownpanel" {
		t.Fatalf("%+v", got)
	}
	if err := c.SetCommitStatus(context.Background(), tok, "acme", "site", sha, github.CommitStatus{State: "done"}); !errors.Is(err, github.ErrInvalid) {
		t.Fatalf("bad state: %v", err)
	}
	_, c2, _, ro := installed(t, map[string]string{"contents": "read"})
	_ = c2
	if err := c.SetCommitStatus(context.Background(), ro, "acme", "site", sha, github.CommitStatus{State: "success"}); err == nil {
		t.Fatal("a token of another fake was accepted")
	}
}
