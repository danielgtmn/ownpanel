package store

import (
	"errors"
	"testing"
	"time"
)

func TestSiteSources(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		clock := time.Unix(1_800_000_000, 0)
		s.Now = func() time.Time { return clock }
		alice, err := s.CreateUser(ctx, "alice", "", RoleAdmin, false)
		if err != nil {
			t.Fatal(err)
		}
		bob, err := s.CreateUser(ctx, "bob", "", RoleAdmin, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"blog", "docs", "shop"} {
			if _, err := s.CreateSite(ctx, name, nil, alice.ID); err != nil {
				t.Fatal(err)
			}
		}
		app, err := s.CreateGitHubApp(ctx, GitHubApp{AppID: 7, OwnerLogin: "acme", OwnerType: "Organization",
			PrivateKey: []byte{1}, WebhookSecret: []byte{2}, ClientSecret: []byte{3}}, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutGitHubInstallation(ctx, GitHubInstallation{ID: 500, AppID: app.ID, AccountLogin: "acme",
			AccountType: "Organization", RepositorySelection: "all"}); err != nil {
			t.Fatal(err)
		}

		if _, err := s.SiteSource(ctx, "blog"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("no source yet: %v", err)
		}
		gh := SiteSource{Site: "blog", Kind: SourceGitHub, InstallationID: 500, RepositoryID: 9001, Repository: "acme/blog",
			Branch: "main", PublishOnPush: true}
		got, err := s.PutSiteSource(ctx, gh, bob.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Site != "blog" || got.InstallationID != 500 || got.RepositoryID != 9001 || got.Repository != "acme/blog" ||
			got.Branch != "main" || !got.PublishOnPush || got.CreatedBy != "bob" || !got.CreatedAt.Equal(clock) {
			t.Fatalf("linked: %+v", got)
		}
		// Linking again replaces the link and keeps when it was first made.
		clock = clock.Add(time.Hour)
		gh.Branch, gh.PublishOnPush = "release", false
		if got, err = s.PutSiteSource(ctx, gh, alice.ID); err != nil {
			t.Fatal(err)
		}
		if got.Branch != "release" || got.PublishOnPush || got.CreatedBy != "alice" || !got.CreatedAt.Equal(clock.Add(-time.Hour)) ||
			!got.UpdatedAt.Equal(clock) {
			t.Fatalf("relinked: %+v", got)
		}
		docs := gh
		docs.Site = "docs"
		if _, err := s.PutSiteSource(ctx, docs, ""); err != nil {
			t.Fatal(err)
		}
		other := gh
		other.Site, other.RepositoryID = "shop", 9002
		if _, err := s.PutSiteSource(ctx, other, ""); err != nil {
			t.Fatal(err)
		}

		// The former table constraints.
		for _, bad := range []SiteSource{
			{Site: "shop", Kind: "svn", Repository: "x", Branch: "main"},
			{Site: "shop", Kind: SourceGitHub, Repository: "acme/shop", Branch: "main"},
			{Site: "shop", Kind: SourceGitHub, RepositoryID: 1, Repository: "acme/shop", Branch: "main", DeployKey: []byte{1}},
			{Site: "shop", Kind: SourceGit, RepositoryID: 1, Repository: "git@example.org:x.git", Branch: "main"},
		} {
			if _, err := s.PutSiteSource(ctx, bad, ""); !errors.Is(err, ErrInvalid) {
				t.Fatalf("%+v: %v", bad, err)
			}
		}
		if _, err := s.PutSiteSource(ctx, SiteSource{Site: "nope", Kind: SourceGitHub, RepositoryID: 1}, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown site: %v", err)
		}
		missingInst := other
		missingInst.InstallationID = 404
		if _, err := s.PutSiteSource(ctx, missingInst, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown installation: %v", err)
		}
		// A Git source keeps its sealed values.
		git := SiteSource{Site: "shop", Kind: SourceGit, Repository: "git@example.org:shop.git", Branch: "main",
			DeployKey: []byte{4}, NotifySecret: []byte{5}, DeployPublicKey: "ssh-ed25519 A", HostKey: "ssh-ed25519 B",
			NotifyID: "0123456789abcdef0123456789abcdef"}
		if got, err := s.PutSiteSource(ctx, git, ""); err != nil || string(got.DeployKey) != "\x04" ||
			string(got.NotifySecret) != "\x05" || got.InstallationID != 0 || got.RepositoryID != 0 {
			t.Fatalf("git source: %+v %v", got, err)
		}

		list, err := s.SiteSourcesOfRepository(ctx, 9001)
		if err != nil || len(list) != 2 || list[0].Site != "blog" || list[1].Site != "docs" {
			t.Fatalf("sources of the repository: %+v %v", list, err)
		}
		// Only GitHub sources, by site name (shop has a Git source now).
		all, err := s.GitHubSiteSources(ctx)
		if err != nil || len(all) != 2 || all[0].Site != "blog" || all[1].Site != "docs" {
			t.Fatalf("GitHub sources: %+v %v", all, err)
		}
		if err := s.RenameSourceRepository(ctx, 9001, "acme/website"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.SiteSource(ctx, "docs"); got.Repository != "acme/website" {
			t.Fatalf("renamed: %+v", got)
		}

		// The user who linked it may go; so may the installation.
		if err := s.DeleteUser(ctx, bob.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.SiteSource(ctx, "docs"); got.CreatedBy != "" {
			t.Fatalf("after removing bob: %+v", got)
		}
		if err := s.DeleteGitHubApp(ctx, app.ID); err != nil {
			t.Fatal(err)
		}
		if got, err := s.SiteSource(ctx, "blog"); err != nil || got.InstallationID != 0 || got.RepositoryID != 9001 {
			t.Fatalf("connection removed: %+v %v", got, err)
		}

		// Unlinking, and removing the site.
		if err := s.DeleteSiteSource(ctx, "docs"); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSiteSource(ctx, "docs"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second unlink: %v", err)
		}
		if err := s.DeleteSite(ctx, "blog"); err != nil {
			t.Fatal(err)
		}
		if list, err := s.SiteSourcesOfRepository(ctx, 9001); err != nil || len(list) != 0 {
			t.Fatalf("after removing the site: %+v %v", list, err)
		}
		// A site created again under the name has no source.
		if _, err := s.CreateSite(ctx, "blog", nil, alice.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SiteSource(ctx, "blog"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("new site: %v", err)
		}
	})
}
