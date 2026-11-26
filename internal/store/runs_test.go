package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testRun(site, sha string) Run {
	return Run{Site: site, Trigger: "push", SourceKind: SourceGitHub, RepositoryID: 7, Repository: "acme/site",
		Ref: "refs/heads/main", CommitSHA: sha, Message: "Fix", AuthorName: "Ann", AuthorLogin: "ann", Pusher: "ann"}
}

func sha(c byte) string { return strings.Repeat(string(c), 40) }

func TestRunsSupersedeClaimFinish(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		for _, n := range []string{"a", "b"} {
			if _, err := s.CreateSite(ctx, n, []string{n + ".example.com"}, ""); err != nil {
				t.Fatal(err)
			}
		}
		r1, err := s.AddRun(ctx, testRun("a", sha('1')), "replaced", 30)
		if err != nil || r1.Status != RunQueued || r1.CommitSHA != sha('1') || r1.StartedAt != nil {
			t.Fatalf("first: %+v %v", r1, err)
		}
		other, err := s.AddRun(ctx, testRun("b", sha('9')), "replaced", 30)
		if err != nil {
			t.Fatal(err)
		}
		r2, err := s.AddRun(ctx, testRun("a", sha('2')), "replaced", 30)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Run(ctx, r1.ID); got.Status != RunSkipped || got.Detail != "replaced" || got.FinishedAt == nil {
			t.Fatalf("older queued run: %+v", got)
		}
		if got, _ := s.Run(ctx, other.ID); got.Status != RunQueued {
			t.Fatalf("another site's run was touched: %+v", got)
		}
		if sites, _ := s.SitesWithQueuedRuns(ctx); strings.Join(sites, ",") != "a,b" {
			t.Fatalf("queued sites: %v", sites)
		}

		c, err := s.ClaimNextRun(ctx, "a", "replaced")
		if err != nil || c.ID != r2.ID || c.Status != RunRunning || c.StartedAt == nil {
			t.Fatalf("claim: %+v %v", c, err)
		}
		// A push while it runs queues the next one, which waits.
		r3, _ := s.AddRun(ctx, testRun("a", sha('3')), "replaced", 30)
		if got, _ := s.Run(ctx, r2.ID); got.Status != RunRunning {
			t.Fatalf("a running run was superseded: %+v", got)
		}
		if _, err := s.ClaimNextRun(ctx, "a", "replaced"); !errors.Is(err, ErrConflict) {
			t.Fatalf("second run at once: %v", err)
		}
		if _, err := s.FinishRun(ctx, r2.ID, RunRunning, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("not final: %v", err)
		}
		f, err := s.FinishRun(ctx, r2.ID, RunSucceeded, "")
		if err != nil || f.Status != RunSucceeded || f.FinishedAt == nil {
			t.Fatalf("finish: %+v %v", f, err)
		}
		if _, err := s.FinishRun(ctx, r2.ID, RunFailed, "x"); !errors.Is(err, ErrConflict) {
			t.Fatalf("finished twice: %v", err)
		}
		if _, err := s.FinishRun(ctx, "nope", RunFailed, "x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown run: %v", err)
		}
		if c, err := s.ClaimNextRun(ctx, "a", "replaced"); err != nil || c.ID != r3.ID {
			t.Fatalf("next: %+v %v", c, err)
		}
		if _, err := s.FinishRun(ctx, r3.ID, RunCancelled, "stopped"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimNextRun(ctx, "a", "replaced"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("nothing queued: %v", err)
		}

		list, err := s.SiteRuns(ctx, "a", 10)
		if err != nil || len(list) != 3 || list[0].ID != r3.ID || list[2].ID != r1.ID {
			t.Fatalf("list: %+v %v", list, err)
		}

		// Runs go with their site.
		if err := s.DeleteSite(ctx, "b"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Run(ctx, other.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("run of a removed site: %v", err)
		}
	})
}

func TestRunsArePruned(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		if _, err := s.CreateSite(ctx, "a", []string{"a.example.com"}, ""); err != nil {
			t.Fatal(err)
		}
		first, _ := s.AddRun(ctx, testRun("a", sha('0')), "replaced", 3)
		if _, err := s.ClaimNextRun(ctx, "a", "replaced"); err != nil {
			t.Fatal(err)
		}
		var last Run
		for i := 0; i < 6; i++ {
			last, _ = s.AddRun(ctx, testRun("a", sha(byte('1'+i))), "replaced", 3)
		}
		list, _ := s.SiteRuns(ctx, "a", 100)
		// The newest three, plus the running one that is never removed.
		if len(list) != 4 || list[0].ID != last.ID || list[3].ID != first.ID || list[3].Status != RunRunning {
			t.Fatalf("kept: %+v", list)
		}
	})
}

func TestDeliveries(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		now := time.Now()
		s.Now = func() time.Time { return now }
		app, err := s.CreateGitHubApp(ctx, GitHubApp{AppID: 77, Slug: "x", Name: "x", ClientID: "c", OwnerLogin: "acme",
			OwnerType: "Organization", HTMLURL: "u", PrivateKey: []byte{1}, WebhookSecret: []byte{2}, ClientSecret: []byte{3}}, "")
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := s.ClaimDelivery(ctx, "d1", app.ID); !ok || err != nil {
			t.Fatalf("first: %v %v", ok, err)
		}
		if ok, _ := s.ClaimDelivery(ctx, "d1", app.ID); ok {
			t.Fatal("a repeated delivery was new")
		}
		if err := s.ReleaseDelivery(ctx, "d1"); err != nil {
			t.Fatal(err)
		}
		if ok, _ := s.ClaimDelivery(ctx, "d1", app.ID); !ok {
			t.Fatal("a released delivery was not new")
		}
		now = now.Add(time.Hour)
		s.ClaimDelivery(ctx, "d2", app.ID)
		s.ClaimDelivery(ctx, "d3", app.ID)
		if n, err := s.ForgetDeliveries(ctx, now.Add(-time.Minute), 100); n != 1 || err != nil {
			t.Fatalf("by age: %d %v", n, err)
		}
		if n, _ := s.ForgetDeliveries(ctx, now.Add(-time.Minute), 1); n != 1 {
			t.Fatalf("by count: %d", n)
		}
		if ok, _ := s.ClaimDelivery(ctx, "d3", app.ID); ok {
			t.Fatal("the newest delivery was forgotten")
		}
	})
}

func TestRunChecks(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		if _, err := s.CreateSite(ctx, "a", []string{"a.example.com"}, ""); err != nil {
			t.Fatal(err)
		}
		for _, c := range []string{"", "abc", strings.Repeat("A", 40), strings.Repeat("g", 40), strings.Repeat("a", 64)} {
			if _, err := s.AddRun(ctx, testRun("a", c), "replaced", 30); !errors.Is(err, ErrInvalid) {
				t.Errorf("commit %q: %v", c, err)
			}
		}
		if _, err := s.AddRun(ctx, testRun("nope", sha('1')), "replaced", 30); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown site: %v", err)
		}
	})
}

func TestRepositoriesGoneAndInstallations(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		app, err := s.CreateGitHubApp(ctx, GitHubApp{AppID: 77, Slug: "x", Name: "x", ClientID: "c", OwnerLogin: "acme",
			OwnerType: "Organization", HTMLURL: "u", PrivateKey: []byte{1}, WebhookSecret: []byte{2}, ClientSecret: []byte{3}}, "")
		if err != nil {
			t.Fatal(err)
		}
		if got, err := s.GitHubAppByAppID(ctx, 77); err != nil || got.ID != app.ID {
			t.Fatalf("by GitHub's id: %+v %v", got, err)
		}
		if _, err := s.GitHubAppByAppID(ctx, 78); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown App: %v", err)
		}
		inst := GitHubInstallation{ID: 500, AppID: app.ID, AccountLogin: "acme", AccountType: "Organization", RepositorySelection: "all"}
		if err := s.PutGitHubInstallation(ctx, inst); err != nil {
			t.Fatal(err)
		}
		if err := s.SetGitHubInstallationSuspended(ctx, app.ID, 500, true); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GitHubInstallation(ctx, 500); !got.Suspended {
			t.Fatalf("suspended: %+v", got)
		}
		if err := s.SetGitHubInstallationSuspended(ctx, "other", 500, false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("another App's installation: %v", err)
		}

		for i := 0; i < 2; i++ { // twice is fine
			if err := s.SetRepositoryGone(ctx, 500, 7, true); err != nil {
				t.Fatal(err)
			}
		}
		if gone, _ := s.RepositoryGone(ctx, 500, 7); !gone {
			t.Fatal("not gone")
		}
		if gone, _ := s.RepositoryGone(ctx, 501, 7); gone {
			t.Fatal("gone for another installation")
		}
		if err := s.SetRepositoryGone(ctx, 500, 7, false); err != nil {
			t.Fatal(err)
		}
		if gone, _ := s.RepositoryGone(ctx, 500, 7); gone {
			t.Fatal("still gone")
		}
		s.SetRepositoryGone(ctx, 500, 8, true)
		if err := s.DeleteGitHubInstallation(ctx, "other", 500); !errors.Is(err, ErrNotFound) {
			t.Fatalf("another App deletes: %v", err)
		}
		if err := s.DeleteGitHubInstallation(ctx, app.ID, 500); err != nil {
			t.Fatal(err)
		}
		if gone, _ := s.RepositoryGone(ctx, 500, 8); gone {
			t.Fatal("gone outlived its installation")
		}
		if _, err := s.GitHubInstallation(ctx, 500); !errors.Is(err, ErrNotFound) {
			t.Fatalf("installation: %v", err)
		}
	})
}

// Delivery ids go with their App, repositories reported gone with their
// installation, also when a refresh drops it.
func TestGitHubRowsGoWithTheirParents(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		app, err := s.CreateGitHubApp(ctx, GitHubApp{AppID: 77, OwnerLogin: "acme", OwnerType: "Organization",
			PrivateKey: []byte{1}, WebhookSecret: []byte{2}, ClientSecret: []byte{3}}, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutGitHubInstallation(ctx, GitHubInstallation{ID: 500, AppID: app.ID, AccountLogin: "acme",
			AccountType: "Organization", RepositorySelection: "all"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetRepositoryGone(ctx, 500, 7, true); err != nil {
			t.Fatal(err)
		}
		// An installation the panel does not know has nothing to record.
		if err := s.SetRepositoryGone(ctx, 404, 7, true); err != nil {
			t.Fatal(err)
		}
		if gone, _ := s.RepositoryGone(ctx, 404, 7); gone {
			t.Fatal("recorded for an unknown installation")
		}
		if err := s.ReplaceGitHubInstallations(ctx, app.ID, nil); err != nil {
			t.Fatal(err)
		}
		if gone, _ := s.RepositoryGone(ctx, 500, 7); gone {
			t.Fatal("gone outlived the refresh that dropped its installation")
		}
		if ok, err := s.ClaimDelivery(ctx, "d1", app.ID); !ok || err != nil {
			t.Fatalf("claim: %v %v", ok, err)
		}
		if _, err := s.ClaimDelivery(ctx, "d2", "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("delivery of an unknown App: %v", err)
		}
		if err := s.DeleteGitHubApp(ctx, app.ID); err != nil {
			t.Fatal(err)
		}
		app, err = s.CreateGitHubApp(ctx, GitHubApp{AppID: 77, OwnerLogin: "acme", OwnerType: "Organization",
			PrivateKey: []byte{1}, WebhookSecret: []byte{2}, ClientSecret: []byte{3}}, "")
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := s.ClaimDelivery(ctx, "d1", app.ID); !ok || err != nil {
			t.Fatalf("the id outlived its App: %v %v", ok, err)
		}
	})
}
