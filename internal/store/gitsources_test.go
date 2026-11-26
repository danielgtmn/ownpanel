package store

import (
	"errors"
	"testing"
	"time"

	"github.com/danielgtmn/ablage"
)

func TestGitSources(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		clock := time.Unix(1_800_000_000, 0)
		s.Now = func() time.Time { return clock }
		alice, err := s.CreateUser(ctx, "alice", "", RoleMember, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"blog", "docs"} {
			if _, err := s.CreateSite(ctx, name, nil, alice.ID); err != nil {
				t.Fatal(err)
			}
		}
		ssh := SiteSource{Site: "blog", Kind: SourceGit, Repository: "git@codeberg.org:a/b.git", Branch: "main",
			DeployKey: []byte{1, 2}, DeployPublicKey: "ssh-ed25519 AAAA x", HostKey: "ssh-ed25519 BBBB",
			NotifyID: "00112233445566778899aabbccddeeff", NotifySecret: []byte{3}}
		// The rules of a Git source.
		for name, bad := range map[string]func(*SiteSource){
			"no notification id": func(s *SiteSource) { s.NotifyID = "" },
			"no secret":          func(s *SiteSource) { s.NotifySecret = nil },
			"key without host":   func(s *SiteSource) { s.HostKey = "" },
			"host without key":   func(s *SiteSource) { s.DeployKey, s.DeployPublicKey = nil, "" },
			"installation":       func(s *SiteSource) { s.InstallationID = 5 },
			"no address":         func(s *SiteSource) { s.Repository = "" },
		} {
			src := ssh
			bad(&src)
			if _, err := s.PutSiteSource(ctx, src, alice.ID); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: %v", name, err)
			}
		}
		gh := SiteSource{Site: "blog", Kind: SourceGitHub, RepositoryID: 1, Repository: "a/b", Branch: "main", HostKey: "x"}
		if _, err := s.PutSiteSource(ctx, gh, alice.ID); !errors.Is(err, ErrInvalid) {
			t.Errorf("GitHub with a host key: %v", err)
		}

		got, err := s.PutSiteSource(ctx, ssh, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID == "" || got.NotifyID != ssh.NotifyID || got.HostKey != ssh.HostKey || got.DeployPublicKey != ssh.DeployPublicKey ||
			string(got.DeployKey) != "\x01\x02" || got.CheckedAt != nil {
			t.Fatalf("linked: %+v", got)
		}
		// HTTPS: neither key.
		https := SiteSource{Site: "docs", Kind: SourceGit, Repository: "https://codeberg.org/a/c.git", Branch: "main",
			NotifyID: "ffeeddccbbaa99887766554433221100", NotifySecret: []byte{4}}
		if _, err := s.PutSiteSource(ctx, https, alice.ID); err != nil {
			t.Fatal(err)
		}
		// Notification ids are unique.
		dup := https
		dup.NotifyID = ssh.NotifyID
		if _, err := s.PutSiteSource(ctx, dup, alice.ID); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate id: %v", err)
		}
		if by, err := s.GitSourceByNotifyID(ctx, ssh.NotifyID); err != nil || by.Site != "blog" {
			t.Fatalf("by id: %+v %v", by, err)
		}
		if _, err := s.GitSourceByNotifyID(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown id: %v", err)
		}

		clock = clock.Add(time.Minute)
		if err := s.SetGitSourceCheck(ctx, "blog", got.ID, "no access", "ssh-ed25519 CCCC"); err != nil {
			t.Fatal(err)
		}
		cur, _ := s.SiteSource(ctx, "blog")
		if cur.CheckError != "no access" || cur.HostKeyNew != "ssh-ed25519 CCCC" || cur.CheckedAt == nil || !cur.CheckedAt.Equal(clock) {
			t.Fatalf("checked: %+v", cur)
		}
		if err := s.SetGitHostKey(ctx, "blog", got.ID, "ssh-ed25519 CCCC"); err != nil {
			t.Fatal(err)
		}
		cur, _ = s.SiteSource(ctx, "blog")
		if cur.HostKey != "ssh-ed25519 CCCC" || cur.HostKeyNew != "" || cur.CheckError != "" {
			t.Fatalf("accepted: %+v", cur)
		}
		if err := s.SetGitDeployKey(ctx, "blog", got.ID, []byte{9}, "ssh-ed25519 DDDD"); err != nil {
			t.Fatal(err)
		}
		cur, _ = s.SiteSource(ctx, "blog")
		if string(cur.DeployKey) != "\x09" || cur.DeployPublicKey != "ssh-ed25519 DDDD" || cur.CheckedAt != nil {
			t.Fatalf("new key: %+v", cur)
		}
		if err := s.SetGitNotifySecret(ctx, "blog", got.ID, "ffeeddccbbaa99887766554433221100", []byte{7}); !errors.Is(err, ErrConflict) {
			t.Fatalf("taken id: %v", err)
		}
		if err := s.SetGitNotifySecret(ctx, "blog", got.ID, "0123456789abcdef0123456789abcdef", []byte{7}); err != nil {
			t.Fatal(err)
		}
		// A change aimed at an older link of the site changes nothing.
		if err := s.SetGitSourceCheck(ctx, "blog", "other-id", "x", ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("other link: %v", err)
		}

		// Delivery ids, per source.
		for i, want := range []bool{true, false} {
			fresh, err := s.ClaimGitDelivery(ctx, got.ID, "d1")
			if err != nil || fresh != want {
				t.Fatalf("claim %d: %v %v", i, fresh, err)
			}
		}
		docs, _ := s.SiteSource(ctx, "docs")
		if fresh, err := s.ClaimGitDelivery(ctx, docs.ID, "d1"); err != nil || !fresh {
			t.Fatalf("other source: %v %v", fresh, err)
		}
		if err := s.ReleaseGitDelivery(ctx, got.ID, "d1"); err != nil {
			t.Fatal(err)
		}
		if fresh, _ := s.ClaimGitDelivery(ctx, got.ID, "d1"); !fresh {
			t.Fatal("released id not claimable")
		}
		if _, err := s.ClaimGitDelivery(ctx, "gone", "d1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown source: %v", err)
		}
		clock = clock.Add(time.Hour)
		if fresh, _ := s.ClaimGitDelivery(ctx, got.ID, "d2"); !fresh {
			t.Fatal("d2")
		}
		if n, err := s.ForgetGitDeliveries(ctx, clock.Add(-time.Minute), 100); err != nil || n != 2 {
			t.Fatalf("forget: %d %v", n, err)
		}
		if fresh, _ := s.ClaimGitDelivery(ctx, docs.ID, "d3"); !fresh {
			t.Fatal("d3")
		}
		// Unlinking removes the source's ids, and only those.
		if err := s.DeleteSiteSource(ctx, "blog"); err != nil {
			t.Fatal(err)
		}
		if fresh, err := s.ClaimGitDelivery(ctx, docs.ID, "d3"); err != nil || fresh {
			t.Fatalf("docs id kept: %v %v", fresh, err)
		}
		if n, _ := s.r.gitDelivs.Count(ctx, ablage.Ne("id", "")); n != 1 {
			t.Fatalf("ids left: %d", n)
		}
	})
}
