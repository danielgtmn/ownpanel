package mcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/gitsource"
	"github.com/danielgtmn/ownpanel/internal/gitsource/gitsourcetest"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/panzerschrank"
)

func TestGitSourceTools(t *testing.T) {
	gitsource.AllowPrivateHosts()
	key := make([]byte, panzerschrank.KeySize)
	rand.Read(key)
	box, _ := panzerschrank.New(key)
	e := newEnvWith(t, "panel.example.com", func(c *panel.Config) {
		c.Caddy.TLSMode = caddy.TLSInternal
		c.Secrets = box
	})
	ctx := context.Background()
	if _, err := e.svc.SaveSite(ctx, e.admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	gitsourcetest.NewRepo(t, root, "ada/site.git", map[string]string{"index.html": "hi"})
	srv := gitsourcetest.NewSSH(t, root)
	addr := srv.Address("ada/site.git")
	fp := ssh.FingerprintSHA256(srv.HostKey.PublicKey())

	r := e.connect(t, e.read)
	refused(t, call_(t, r, "inspect_git_repository", map[string]any{"site": "blog", "repository": addr}), "only read")
	cs := e.connect(t, e.write)
	var insp panel.GitRepositoryCheck
	ok(t, call_(t, cs, "inspect_git_repository", map[string]any{"site": "blog", "repository": addr}), &insp)
	if insp.HostKey != fp {
		t.Fatalf("inspect: %+v", insp)
	}
	refused(t, call_(t, cs, "inspect_git_repository", map[string]any{"site": "blog", "repository": "file:///etc/passwd"}), "only SSH and HTTPS")

	res := call_(t, cs, "link_git_site_source", map[string]any{"site": "blog", "repository": addr, "branch": "main",
		"publish_on_push": true, "host_key": fp})
	var out siteSourceOutput
	ok(t, res, &out)
	if out.Source == nil || out.Source.Kind != "git" || out.Source.DeployKey == "" || out.Source.NotifyURL == "" || out.Source.Status != "not_checked" {
		t.Fatalf("link: %+v", out.Source)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "notify_secret") || strings.Contains(string(raw), "PRIVATE") {
		t.Fatalf("link answer shows a secret: %s", raw)
	}
	k, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(out.Source.DeployKey))
	srv.Authorize(k)
	var chk gitCheckOutput
	ok(t, call_(t, cs, "check_git_site_source", map[string]any{"site": "blog"}), &chk)
	if !chk.OK || !chk.BranchFound || chk.Source.Status != "linked" || chk.Source.CheckedAt == "" {
		t.Fatalf("check: %+v", chk)
	}
	var got siteSourceOutput
	ok(t, call_(t, r, "get_site_source", map[string]any{"site": "blog"}), &got)
	if got.Source.HostKey != fp || got.Source.Transport != "ssh" {
		t.Fatalf("get: %+v", got.Source)
	}
}
