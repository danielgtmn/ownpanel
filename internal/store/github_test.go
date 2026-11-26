package store

import (
	"errors"
	"testing"
	"time"
)

func TestGitHubAppsAndInstallations(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		clock := time.Unix(1_800_000_000, 0)
		s.Now = func() time.Time { return clock }
		alice, err := s.CreateUser(ctx, "alice", "", RoleAdmin, false)
		if err != nil {
			t.Fatal(err)
		}
		app := GitHubApp{AppID: 77, Slug: "ownpanel-x", Name: "ownpanel x", ClientID: "Iv1.x", OwnerLogin: "acme",
			OwnerType: "Organization", HTMLURL: "https://github.com/apps/ownpanel-x",
			PrivateKey: []byte{1}, WebhookSecret: []byte{2}, ClientSecret: []byte{3}}
		a, err := s.CreateGitHubApp(ctx, app, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		if a.ID == "" || a.AppID != 77 || a.CreatedBy != "alice" || string(a.PrivateKey) != "\x01" ||
			string(a.WebhookSecret) != "\x02" || string(a.ClientSecret) != "\x03" || a.HTMLURL != app.HTMLURL ||
			a.LastCheckedAt == nil || !a.CreatedAt.Equal(clock) {
			t.Fatalf("created: %+v", a)
		}
		if _, err := s.CreateGitHubApp(ctx, app, alice.ID); !errors.Is(err, ErrConflict) {
			t.Fatalf("same App twice: %v", err)
		}
		bad := app
		bad.AppID, bad.OwnerType = 79, "Team"
		if _, err := s.CreateGitHubApp(ctx, bad, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unknown owner type: %v", err)
		}
		app.AppID = 78
		clock = clock.Add(time.Minute)
		b, err := s.CreateGitHubApp(ctx, app, "")
		if err != nil {
			t.Fatal(err)
		}
		if b.CreatedBy != "" {
			t.Fatalf("created by nobody: %+v", b)
		}
		if err := s.RecordGitHubCheck(ctx, b.ID, clock.Add(time.Hour), "gone"); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GitHubApp(ctx, b.ID); err != nil || got.LastError != "gone" || !got.LastCheckedAt.Equal(clock.Add(time.Hour)) {
			t.Fatalf("checked: %+v %v", got, err)
		}
		if _, err := s.GitHubApp(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown App: %v", err)
		}

		inst := GitHubInstallation{ID: 500, AppID: a.ID, AccountLogin: "acme", AccountType: "Organization", RepositorySelection: "all"}
		if err := s.PutGitHubInstallation(ctx, inst); err != nil {
			t.Fatal(err)
		}
		if err := s.SetGitHubRepositoryCount(ctx, 500, 4); err != nil {
			t.Fatal(err)
		}
		// Another App cannot take over an installation.
		stolen := inst
		stolen.AppID = b.ID
		if err := s.PutGitHubInstallation(ctx, stolen); !errors.Is(err, ErrConflict) {
			t.Fatalf("taken over: %v", err)
		}
		orphan := inst
		orphan.ID, orphan.AppID = 600, "nope"
		if err := s.PutGitHubInstallation(ctx, orphan); !errors.Is(err, ErrNotFound) {
			t.Fatalf("installation of an unknown App: %v", err)
		}
		invalid := inst
		invalid.RepositorySelection = "some"
		if err := s.PutGitHubInstallation(ctx, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unknown selection: %v", err)
		}
		// A refresh keeps the count of known installations and drops the rest.
		inst.RepositorySelection = "selected"
		if err := s.ReplaceGitHubInstallations(ctx, a.ID, []GitHubInstallation{inst,
			{ID: 501, AccountLogin: "Acme", AccountType: "User", RepositorySelection: "all"}}); err != nil {
			t.Fatal(err)
		}
		got, err := s.GitHubInstallation(ctx, 500)
		if err != nil || got.AppID != a.ID || got.RepositorySelection != "selected" || got.RepositoryCount == nil || *got.RepositoryCount != 4 {
			t.Fatalf("after refresh: %+v %v", got, err)
		}
		// By account ignoring case, then by GitHub's id.
		list, err := s.GitHubInstallations(ctx, a.ID)
		if err != nil || len(list) != 2 || list[0].ID != 500 || list[1].ID != 501 || list[1].RepositoryCount != nil {
			t.Fatalf("installations: %+v %v", list, err)
		}
		if list, err := s.GitHubInstallations(ctx, b.ID); err != nil || len(list) != 0 {
			t.Fatalf("installations of b: %+v %v", list, err)
		}
		if err := s.ReplaceGitHubInstallations(ctx, a.ID, []GitHubInstallation{{ID: 501, AccountLogin: "acme", AccountType: "Organization", RepositorySelection: "all"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GitHubInstallation(ctx, 500); !errors.Is(err, ErrNotFound) {
			t.Fatalf("removed installation: %v", err)
		}
		// Removing the App removes its installations.
		if err := s.DeleteGitHubApp(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
		if list, err := s.GitHubInstallations(ctx, ""); err != nil || len(list) != 0 {
			t.Fatalf("installations left: %v %v", list, err)
		}
		if err := s.DeleteGitHubApp(ctx, a.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete: %v", err)
		}
		apps, err := s.GitHubApps(ctx)
		if err != nil || len(apps) != 1 || apps[0].AppID != 78 || apps[0].CreatedBy != "" {
			t.Fatalf("apps: %+v %v", apps, err)
		}
	})
}

// Removing the user who connected an App keeps the App.
func TestGitHubAppOutlivesItsCreator(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		if _, err := s.CreateUser(ctx, "root", "", RoleAdmin, false); err != nil {
			t.Fatal(err)
		}
		bob, err := s.CreateUser(ctx, "bob", "", RoleAdmin, false)
		if err != nil {
			t.Fatal(err)
		}
		a, err := s.CreateGitHubApp(ctx, GitHubApp{AppID: 1, OwnerLogin: "bob", OwnerType: "User",
			PrivateKey: []byte{1}, WebhookSecret: []byte{2}, ClientSecret: []byte{3}}, bob.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteUser(ctx, bob.ID); err != nil {
			t.Fatal(err)
		}
		got, err := s.GitHubApp(ctx, a.ID)
		if err != nil || got.CreatedBy != "" || string(got.PrivateKey) != "\x01" {
			t.Fatalf("after removing bob: %+v %v", got, err)
		}
	})
}
