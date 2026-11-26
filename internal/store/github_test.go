package store

import (
	"errors"
	"testing"
)

func TestGitHubAppsAndInstallations(t *testing.T) {
	s, _ := open(t)
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
	if a.ID == 0 || a.CreatedBy != "alice" || string(a.PrivateKey) != "\x01" || a.LastCheckedAt == nil {
		t.Fatalf("created: %+v", a)
	}
	if _, err := s.CreateGitHubApp(ctx, app, alice.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("same App twice: %v", err)
	}
	app.AppID = 78
	b, err := s.CreateGitHubApp(ctx, app, 0)
	if err != nil {
		t.Fatal(err)
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
	// A refresh keeps the count of known installations and drops the rest.
	inst.RepositorySelection = "selected"
	if err := s.ReplaceGitHubInstallations(ctx, a.ID, []GitHubInstallation{inst,
		{ID: 501, AccountLogin: "acme", AccountType: "Organization", RepositorySelection: "all"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GitHubInstallation(ctx, 500)
	if err != nil || got.RepositorySelection != "selected" || got.RepositoryCount == nil || *got.RepositoryCount != 4 {
		t.Fatalf("after refresh: %+v %v", got, err)
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
	if list, err := s.GitHubInstallations(ctx, 0); err != nil || len(list) != 0 {
		t.Fatalf("installations left: %v %v", list, err)
	}
	if err := s.DeleteGitHubApp(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	apps, err := s.GitHubApps(ctx)
	if err != nil || len(apps) != 1 || apps[0].AppID != 78 || apps[0].CreatedBy != "" {
		t.Fatalf("apps: %+v %v", apps, err)
	}
}
