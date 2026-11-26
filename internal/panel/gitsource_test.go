package panel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/danielgtmn/ownpanel/internal/gitsource"
	"github.com/danielgtmn/ownpanel/internal/gitsource/gitsourcetest"
	"github.com/danielgtmn/ownpanel/internal/store"
)

type gitWorld struct {
	*githubWorld
	srv  *gitsourcetest.SSH
	repo *gitsourcetest.Repo
	addr string
	// secrets seen in answers that may show them once, to look for later.
	shown []string
}

func newGitWorld(t *testing.T) *gitWorld {
	t.Helper()
	gitsource.AllowPrivateHosts()
	g := &gitWorld{githubWorld: newGitHubWorld(t)}
	root := t.TempDir()
	g.repo = gitsourcetest.NewRepo(t, root, "bob/site.git", map[string]string{"index.html": "<h1>bob</h1>", "a/b.txt": "b"})
	g.srv = gitsourcetest.NewSSH(t, root)
	g.addr = g.srv.Address("bob/site.git")
	return g
}

func (g *gitWorld) fingerprint() string { return ssh.FingerprintSHA256(g.srv.HostKey.PublicKey()) }

// link links site as a to the repository and authorizes the deploy key at
// the server.
func (g *gitWorld) link(a Actor, site string) SiteSourceState {
	g.t.Helper()
	st, err := g.svc.SetGitSiteSource(context.Background(), a, site, GitSourceInput{Repository: g.addr, Branch: "main", PublishOnPush: true, HostKey: g.fingerprint()})
	if err != nil {
		g.t.Fatal(err)
	}
	g.authorize(st.Source.DeployKey)
	if st.NotifySecret != "" {
		g.shown = append(g.shown, st.NotifySecret)
	}
	return st
}

func (g *gitWorld) authorize(line string) {
	g.t.Helper()
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		g.t.Fatal(err)
	}
	g.srv.Authorize(k)
}

// assertNoGitSecrets looks for every deploy key's private half and every
// notification secret in data.
func (g *gitWorld) assertNoGitSecrets(what, data string) {
	g.t.Helper()
	for _, site := range []string{"rootsite", "bobsite", "bobother"} {
		src, err := g.svc.store.SiteSource(context.Background(), site)
		if err != nil || src.Kind != store.SourceGit {
			continue
		}
		if len(src.DeployKey) > 0 {
			raw, err := g.svc.secrets.Open(src.DeployKey, gitDeployKeyPurpose)
			if err != nil {
				g.t.Fatal(err)
			}
			for _, enc := range []string{hex.EncodeToString(raw), hex.EncodeToString(raw[:32]), string(raw[:32])} {
				if strings.Contains(data, enc) {
					g.t.Fatalf("%s contains the private deploy key of %s", what, site)
				}
			}
		}
		secret, err := g.svc.secrets.Open(src.NotifySecret, gitNotifyPurpose)
		if err != nil {
			g.t.Fatal(err)
		}
		if strings.Contains(data, string(secret)) {
			g.t.Fatalf("%s contains the notification secret of %s", what, site)
		}
	}
	for _, s := range g.shown {
		if strings.Contains(data, s) {
			g.t.Fatalf("%s contains a notification secret shown before", what)
		}
	}
	if strings.Contains(data, "PRIVATE KEY") {
		g.t.Fatalf("%s contains a private key", what)
	}
}

func TestGitSourceLinkCheckFetch(t *testing.T) {
	g := newGitWorld(t)
	ctx := context.Background()

	insp, err := g.svc.InspectGitRepository(ctx, bob, "bobsite", " "+g.addr+" ")
	if err != nil {
		t.Fatal(err)
	}
	if insp.HostKey != g.fingerprint() || insp.HostKeyType != ssh.KeyAlgoED25519 || insp.Transport != GitSSH || insp.Pinned ||
		!strings.HasPrefix(insp.Server, "127.0.0.1:") {
		t.Fatalf("inspect: %+v", insp)
	}

	// A fingerprint the server does not show, or none.
	_, err = g.svc.SetGitSiteSource(ctx, bob, "bobsite", GitSourceInput{Repository: g.addr, Branch: "main", HostKey: "SHA256:not-it"})
	wantKind(t, err, Conflict)
	_, err = g.svc.SetGitSiteSource(ctx, bob, "bobsite", GitSourceInput{Repository: g.addr, Branch: "main"})
	wantKind(t, err, Invalid)
	_, err = g.svc.SetGitSiteSource(ctx, bob, "bobsite", GitSourceInput{Repository: "file:///etc", Branch: "main"})
	wantKind(t, err, Invalid)
	_, err = g.svc.SetGitSiteSource(ctx, bob, "bobsite", GitSourceInput{Repository: g.addr, Branch: "-x", HostKey: g.fingerprint()})
	wantKind(t, err, Invalid)
	if src := g.source("bobsite"); src != nil {
		t.Fatalf("a refused link was stored: %+v", src)
	}

	st, err := g.svc.SetGitSiteSource(ctx, bob, "bobsite", GitSourceInput{Repository: g.addr, Branch: "main", PublishOnPush: true, HostKey: g.fingerprint()})
	if err != nil {
		t.Fatal(err)
	}
	src := st.Source
	if src.Kind != "git" || src.Repository != "ssh://git@"+g.srv.Addr+"/bob/site.git" || src.Transport != GitSSH ||
		src.HostKey != g.fingerprint() || src.Status != SourceNotChecked || src.LinkedBy != "bob" ||
		!strings.HasPrefix(src.DeployKey, "ssh-ed25519 ") || !strings.HasSuffix(src.DeployKey, " ownpanel-bobsite@panel.example.com") ||
		!strings.HasPrefix(src.NotifyURL, "https://panel.example.com:8443/api/git/notify/") {
		t.Fatalf("linked: %+v", src)
	}
	if len(st.NotifySecret) != 64 {
		t.Fatalf("secret: %q", st.NotifySecret)
	}
	secret := st.NotifySecret
	g.shown = append(g.shown, secret)

	// Before the key is added at the host.
	chk, err := g.svc.CheckGitSiteSource(ctx, bob, "bobsite")
	if err != nil {
		t.Fatal(err)
	}
	if chk.OK || chk.Source.Status != SourceAccessFailed || !strings.Contains(chk.Source.Problem, "did not accept the deploy key") {
		t.Fatalf("check without the key: %+v %+v", chk, chk.Source)
	}
	if got := g.source("bobsite"); got.Status != SourceAccessFailed {
		t.Fatalf("stored status: %+v", got)
	}

	g.authorize(src.DeployKey)
	g.repo.Commit("dev", nil)
	g.Git("branch", "dev")
	chk, err = g.svc.CheckGitSiteSource(ctx, bob, "bobsite")
	if err != nil {
		t.Fatal(err)
	}
	if !chk.OK || !chk.BranchFound || chk.Source.Status != SourceLinked || strings.Join(chk.Branches, ",") != "dev,main" ||
		chk.DefaultBranch != "main" || chk.Source.CheckedAt == nil {
		t.Fatalf("check: %+v %+v", chk, chk.Source)
	}
	for _, u := range g.srv.Users {
		if u != "git" {
			t.Fatalf("user %q", u)
		}
	}

	// Changing the branch keeps the deploy key and the notification
	// address, and shows no secret.
	st, err = g.svc.SetGitSiteSource(ctx, bob, "bobsite", GitSourceInput{Repository: g.addr, Branch: "dev", HostKey: g.fingerprint()})
	if err != nil {
		t.Fatal(err)
	}
	if st.Source.DeployKey != src.DeployKey || st.Source.NotifyURL != src.NotifyURL || st.NotifySecret != "" ||
		st.Source.Branch != "dev" || st.Source.Status != SourceLinked {
		t.Fatalf("changed: %+v", st.Source)
	}
	insp, err = g.svc.InspectGitRepository(ctx, bob, "bobsite", g.addr)
	if err != nil || !insp.Pinned {
		t.Fatalf("pinned: %+v %v", insp, err)
	}

	// Fetching the branch's commit.
	head := g.repo.Head("dev")
	tree, strip, err := g.svc.FetchGitSource(ctx, "bobsite", g.repo.Head("dev"))
	if err != nil || strip != 0 {
		t.Fatalf("fetch: %v", err)
	}
	files := untar(t, tree)
	if files["index.html"] != "<h1>bob</h1>" || files["a/b.txt"] != "b" {
		t.Fatalf("files: %v", files)
	}
	// A commit the branch no longer points at.
	g.Git("checkout", "-q", "dev")
	g.repo.Commit("newer on dev", map[string]string{"index.html": "new"})
	_, _, err = g.svc.FetchGitSource(ctx, "bobsite", head)
	wantKind(t, err, Conflict)
	if !strings.Contains(err.Error(), "now points at commit") {
		t.Fatalf("moved: %v", err)
	}
	_, _, err = g.svc.FetchGitSource(ctx, "bobsite", "not-a-commit")
	wantKind(t, err, Invalid)

	// Everything people and programs see is free of secrets.
	full, err := g.svc.GetSiteSource(ctx, bob, "bobsite")
	if err != nil {
		t.Fatal(err)
	}
	g.assertNoGitSecrets("source", asJSON(t, full))
	g.assertNoGitSecrets("check", asJSON(t, chk))
	acts, _ := g.svc.store.Activities(ctx, 50)
	for _, a := range acts {
		g.assertNoGitSecrets("activity", a.Detail)
	}
	g.assertNoGitSecrets("log", g.logs.String())

	// Unlinking.
	if err := g.svc.RemoveSiteSource(ctx, bob, "bobsite"); err != nil {
		t.Fatal(err)
	}
	if src := g.source("bobsite"); src != nil {
		t.Fatalf("still linked: %+v", src)
	}
}

func (g *gitWorld) Git(args ...string) string {
	g.t.Helper()
	return gitsourcetest.Git(g.t, g.repo.Dir, args...)
}

func untar(t *testing.T, r io.ReadCloser) map[string]string {
	t.Helper()
	defer r.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

func TestGitSourceHostKeyChange(t *testing.T) {
	g := newGitWorld(t)
	ctx := context.Background()
	g.link(bob, "bobsite")
	if chk, err := g.svc.CheckGitSiteSource(ctx, bob, "bobsite"); err != nil || !chk.OK {
		t.Fatalf("check: %+v %v", chk, err)
	}

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _ := ssh.NewSignerFromKey(priv)
	old := g.fingerprint()
	g.srv.SetHostKey(other)
	g.srv.Users = nil
	chk, err := g.svc.CheckGitSiteSource(ctx, bob, "bobsite")
	if err != nil {
		t.Fatal(err)
	}
	newFP := ssh.FingerprintSHA256(other.PublicKey())
	if chk.OK || chk.Source.Status != SourceHostKeyChanged || chk.Source.HostKey != old || chk.Source.NewHostKey != newFP {
		t.Fatalf("changed: %+v", chk.Source)
	}
	if len(g.srv.Users) != 0 {
		t.Fatal("the deploy key was used with a server showing another key")
	}
	// Nothing is fetched meanwhile, not even tried.
	_, _, err = g.svc.FetchGitSource(ctx, "bobsite", "")
	wantKind(t, err, Conflict)
	// Read-only actors see it but cannot accept.
	if got := g.sourceAs(bobRead, "bobsite"); got.Status != SourceHostKeyChanged || got.NewHostKey != newFP {
		t.Fatalf("read: %+v", got)
	}
	_, err = g.svc.AcceptGitHostKey(ctx, bobRead, "bobsite", newFP)
	wantKind(t, err, Forbidden)
	// Accepting a fingerprint the server does not show.
	_, err = g.svc.AcceptGitHostKey(ctx, bob, "bobsite", old)
	wantKind(t, err, Conflict)
	st, err := g.svc.AcceptGitHostKey(ctx, bob, "bobsite", newFP)
	if err != nil {
		t.Fatal(err)
	}
	if st.Source.HostKey != newFP || st.Source.NewHostKey != "" || st.Source.Status == SourceHostKeyChanged {
		t.Fatalf("accepted: %+v", st.Source)
	}
	if chk, err := g.svc.CheckGitSiteSource(ctx, bob, "bobsite"); err != nil || !chk.OK {
		t.Fatalf("check after accepting: %+v %v", chk, err)
	}
	acts, _ := g.svc.store.Activities(ctx, 10)
	if acts[0].Action != "site.source-host-key" || !strings.Contains(acts[0].Detail, newFP) {
		t.Fatalf("activity: %+v", acts[0])
	}
}

func (g *gitWorld) sourceAs(a Actor, site string) *SiteSource {
	g.t.Helper()
	st, err := g.svc.GetSiteSource(context.Background(), a, site)
	if err != nil {
		g.t.Fatal(err)
	}
	return st.Source
}

func TestGitSourceReplaceKeys(t *testing.T) {
	g := newGitWorld(t)
	ctx := context.Background()
	first := g.link(bob, "bobsite")
	if chk, _ := g.svc.CheckGitSiteSource(ctx, bob, "bobsite"); !chk.OK {
		t.Fatal("check")
	}
	st, err := g.svc.ReplaceGitDeployKey(ctx, bob, "bobsite")
	if err != nil {
		t.Fatal(err)
	}
	if st.Source.DeployKey == first.Source.DeployKey || st.Source.Status != SourceNotChecked || st.NotifySecret != "" {
		t.Fatalf("replaced: %+v", st.Source)
	}
	// The server only knows the old key.
	if chk, _ := g.svc.CheckGitSiteSource(ctx, bob, "bobsite"); chk.OK {
		t.Fatal("the old key still works in the panel")
	}

	oldID := strings.TrimPrefix(first.Source.NotifyURL, "https://panel.example.com:8443"+GitNotifyPath)
	st, err = g.svc.ReplaceGitNotifySecret(ctx, bob, "bobsite")
	if err != nil {
		t.Fatal(err)
	}
	if st.Source.NotifyURL == first.Source.NotifyURL || len(st.NotifySecret) != 64 || st.NotifySecret == first.NotifySecret {
		t.Fatalf("new secret: %+v", st)
	}
	// The old address and secret are refused.
	body := pushBody("refs/heads/main", g.repo.Head("main"))
	_, err = g.svc.ReceiveGitNotification(ctx, GitNotification{ID: oldID, Header: githubHeaders(first.NotifySecret, body, "d1"), Body: body})
	if err != ErrDeliveryRejected {
		t.Fatalf("old address: %v", err)
	}
	newID := strings.TrimPrefix(st.Source.NotifyURL, "https://panel.example.com:8443"+GitNotifyPath)
	_, err = g.svc.ReceiveGitNotification(ctx, GitNotification{ID: newID, Header: githubHeaders(first.NotifySecret, body, "d2"), Body: body})
	if err != ErrDeliveryRejected {
		t.Fatalf("old secret: %v", err)
	}
	res, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: newID, Header: githubHeaders(st.NotifySecret, body, "d3"), Body: body})
	if err != nil || res.Runs != 1 {
		t.Fatalf("new: %+v %v", res, err)
	}
	g.shown = append(g.shown, st.NotifySecret)
	g.assertNoGitSecrets("log", g.logs.String())
}

func hmacHex(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func githubHeaders(secret string, body []byte, delivery string) http.Header {
	h := http.Header{}
	h.Set("X-GitHub-Event", "push")
	h.Set("X-GitHub-Delivery", delivery)
	h.Set("X-Hub-Signature-256", "sha256="+hmacHex(secret, body))
	return h
}

func pushBody(ref, after string) []byte {
	return fmt.Appendf(nil, `{"ref":%q,"before":"0000000000000000000000000000000000000000","after":%q,`+
		`"head_commit":{"id":%q,"message":"Fix the header\n\nlonger text","author":{"name":"Ada","username":"ada"}},`+
		`"pusher":{"name":"ada"}}`, ref, after, after)
}

func TestGitNotificationFormats(t *testing.T) {
	g := newGitWorld(t)
	ctx := context.Background()
	st := g.link(bob, "bobsite")
	secret := st.NotifySecret
	id := strings.TrimPrefix(st.Source.NotifyURL, "https://panel.example.com:8443"+GitNotifyPath)
	commit := g.repo.Head("main")
	gl := fmt.Appendf(nil, `{"object_kind":"push","ref":"refs/heads/main","after":%q,"checkout_sha":%q,"user_name":"Ada L",`+
		`"commits":[{"id":%q,"message":"From GitLab","author":{"name":"Ada","email":"a@example.com"}}]}`, commit, commit, commit)
	bbc := fmt.Appendf(nil, `{"actor":{"display_name":"Ada"},"push":{"changes":[{"new":{"type":"branch","name":"main",`+
		`"target":{"hash":%q,"message":"From Bitbucket\n","author":{"raw":"Ada <a@example.com>"}}}}]}}`, commit)
	bbs := fmt.Appendf(nil, `{"eventKey":"repo:refs_changed","actor":{"name":"ada"},"changes":[{"ref":{"id":"refs/heads/main",`+
		`"displayId":"main","type":"BRANCH"},"fromHash":"0000000000000000000000000000000000000001","toHash":%q,"type":"UPDATE"}]}`, commit)
	gh := pushBody("refs/heads/main", commit)

	cases := []struct {
		name    string
		body    []byte
		headers map[string]string
		message string
	}{
		{"GitHub", gh, map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": "gh-1", "X-Hub-Signature-256": "sha256=" + hmacHex(secret, gh)}, "Fix the header"},
		{"Gitea", gh, map[string]string{"X-Gitea-Event": "push", "X-Gitea-Delivery": "gt-1", "X-Gitea-Signature": hmacHex(secret, gh)}, "Fix the header"},
		{"Forgejo", gh, map[string]string{"X-Forgejo-Event": "push", "X-Forgejo-Delivery": "fj-1", "X-Forgejo-Signature": hmacHex(secret, gh),
			"X-Hub-Signature-256": "sha256=" + hmacHex(secret, gh)}, "Fix the header"},
		{"Gogs", gh, map[string]string{"X-Gogs-Event": "push", "X-Gogs-Delivery": "gg-1", "X-Gogs-Signature": hmacHex(secret, gh)}, "Fix the header"},
		{"GitLab", gl, map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Event-UUID": "gl-1", "X-Gitlab-Token": secret}, "From GitLab"},
		{"Bitbucket Cloud", bbc, map[string]string{"X-Event-Key": "repo:push", "X-Request-UUID": "{bb-1}", "X-Hub-Signature": "sha256=" + hmacHex(secret, bbc)}, "From Bitbucket"},
		{"Bitbucket Server", bbs, map[string]string{"X-Event-Key": "repo:refs_changed", "X-Request-Id": "bbs-1", "X-Hub-Signature": "sha256=" + hmacHex(secret, bbs)}, ""},
	}
	for _, c := range cases {
		h := http.Header{}
		for k, v := range c.headers {
			h.Set(k, v)
		}
		res, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: h, Body: c.body})
		if err != nil || res.Result != DeliveryHandled || res.Runs != 1 {
			t.Fatalf("%s: %+v %v", c.name, res, err)
		}
		runs, _ := g.svc.ListSiteRuns(ctx, bob, "bobsite")
		r := runs.Runs[0]
		if r.Commit != commit || r.Branch != "main" || r.Message != c.message || r.Status != "queued" || r.Repository != st.Source.Repository {
			t.Fatalf("%s: run %+v", c.name, r)
		}
		// The same delivery again.
		res, err = g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: h, Body: c.body})
		if err != nil || res.Result != DeliveryDuplicate {
			t.Fatalf("%s replayed: %+v %v", c.name, res, err)
		}
	}

	reject := func(name string, id string, h http.Header, body []byte) {
		t.Helper()
		if _, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: h, Body: body}); err != ErrDeliveryRejected {
			t.Errorf("%s: %v", name, err)
		}
	}
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	reject("no proof", id, hdr("X-GitHub-Event", "push"), gh)
	reject("wrong signature", id, hdr("X-Hub-Signature-256", "sha256="+hmacHex("wrong", gh)), gh)
	reject("signature of another body", id, hdr("X-Hub-Signature-256", "sha256="+hmacHex(secret, bbc)), gh)
	reject("sha1 signature", id, hdr("X-Hub-Signature", "sha1="+hmacHex(secret, gh)[:40]), gh)
	reject("bad hex", id, hdr("X-Gitea-Signature", "zz"), gh)
	reject("wrong token", id, hdr("X-Gitlab-Token", secret+"x"), gl)
	reject("one right, one wrong", id, hdr("X-Gitlab-Token", secret, "X-Hub-Signature-256", "sha256="+hmacHex("x", gl)), gl)
	reject("unknown address", strings.Repeat("0", 32), githubHeaders(secret, gh, "x1"), gh)
	reject("address of another form", "../../x", githubHeaders(secret, gh, "x2"), gh)
	bad := []byte(`{"ref": "refs/heads/main", "after": 5}`)
	reject("malformed", id, githubHeaders(secret, bad, "x3"), bad)
	two := append(append([]byte{}, gh...), gh...)
	reject("two values", id, githubHeaders(secret, two, "x4"), two)
	reject("bad delivery id", id, githubHeaders(secret, gh, "a b"), gh)
	// A malformed body with a delivery id leaves the id unused.
	good := hdr("X-GitHub-Event", "push", "X-GitHub-Delivery", "x3", "X-Hub-Signature-256", "sha256="+hmacHex(secret, gh))
	if res, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: good, Body: gh}); err != nil || res.Runs != 1 {
		t.Fatalf("after a malformed one: %+v %v", res, err)
	}

	ignored := func(name string, body []byte, h http.Header) {
		t.Helper()
		res, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: h, Body: body})
		if err != nil || res.Result != DeliveryIgnored || res.Runs != 0 {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	sign := func(body []byte, delivery, event string) http.Header {
		h := githubHeaders(secret, body, delivery)
		h.Set("X-GitHub-Event", event)
		return h
	}
	ping := []byte(`{"zen":"hi"}`)
	ignored("ping", ping, sign(ping, "p1", "ping"))
	other := pushBody("refs/heads/other", commit)
	ignored("other branch", other, sign(other, "o1", "push"))
	tag := pushBody("refs/tags/v1", commit)
	ignored("tag", tag, sign(tag, "t1", "push"))
	del := pushBody("refs/heads/main", strings.Repeat("0", 40))
	ignored("deleted branch", del, sign(del, "del1", "push"))
	// Without a delivery id it is handled (no replay protection then; a
	// build only ever fetches the branch's current commit).
	h := http.Header{}
	h.Set("X-Hub-Signature-256", "sha256="+hmacHex(secret, gh))
	if res, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: h, Body: gh}); err != nil || res.Runs != 1 {
		t.Fatalf("without delivery id: %+v %v", res, err)
	}
	// Publish on push off.
	if _, err := g.svc.SetGitSiteSource(ctx, bob, "bobsite", GitSourceInput{Repository: g.addr, Branch: "main", HostKey: g.fingerprint()}); err != nil {
		t.Fatal(err)
	}
	ignored("publish on push off", gh, sign(gh, "off1", "push"))

	runs, _ := g.svc.ListSiteRuns(ctx, bob, "bobsite")
	g.assertNoGitSecrets("runs", asJSON(t, runs))
	g.assertNoGitSecrets("log", g.logs.String())
}

func TestGitSourceHTTPS(t *testing.T) {
	g := newGitWorld(t)
	ctx := context.Background()
	root := t.TempDir()
	repo := gitsourcetest.NewRepo(t, root, "pub/site.git", map[string]string{"index.html": "public"})
	srv := gitsourcetest.NewHTTPS(t, root)
	pool := srvPool(srv)
	gitsource.UseRoots(pool)
	defer gitsource.UseRoots(nil)

	insp, err := g.svc.InspectGitRepository(ctx, bob, "bobother", srv.Address("pub/site.git"))
	if err != nil || insp.Transport != GitHTTPS || insp.HostKey != "" || strings.Join(insp.Branches, ",") != "main" || insp.DefaultBranch != "main" {
		t.Fatalf("inspect: %+v %v", insp, err)
	}
	_, err = g.svc.SetGitSiteSource(ctx, bob, "bobother", GitSourceInput{Repository: srv.Address("pub/site.git"), Branch: "nope"})
	wantKind(t, err, Invalid)
	st, err := g.svc.SetGitSiteSource(ctx, bob, "bobother", GitSourceInput{Repository: srv.Address("pub/site.git"), Branch: "main", PublishOnPush: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Source.DeployKey != "" || st.Source.HostKey != "" || st.Source.Status != SourceLinked || st.NotifySecret == "" {
		t.Fatalf("https: %+v", st.Source)
	}
	tree, _, err := g.svc.FetchGitSource(ctx, "bobother", repo.Head("main"))
	if err != nil {
		t.Fatal(err)
	}
	if files := untar(t, tree); files["index.html"] != "public" {
		t.Fatalf("files: %v", files)
	}
	_, err = g.svc.AcceptGitHostKey(ctx, bob, "bobother", "SHA256:x")
	wantKind(t, err, Conflict)
	_, err = g.svc.ReplaceGitDeployKey(ctx, bob, "bobother")
	wantKind(t, err, Conflict)
}

func srvPool(srv *gitsourcetest.HTTPS) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}

func TestGitSourcePermissions(t *testing.T) {
	g := newGitWorld(t)
	ctx := context.Background()
	g.link(bob, "bobsite")
	fp := g.fingerprint()
	in := GitSourceInput{Repository: g.addr, Branch: "main", HostKey: fp}
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", admin}, {"admin write token", rootWriteTok}, {"member", bob}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody}, {"system", System},
	}
	ops := []struct {
		name string
		run  func(a Actor) error
		want []Kind
	}{
		{"see", func(a Actor) error { _, err := g.svc.GetSiteSource(ctx, a, "bobsite"); return err },
			[]Kind{ok, ok, ok, ok, ok, ok, NotFound, Unauthenticated, ok}},
		{"inspect", func(a Actor) error { _, err := g.svc.InspectGitRepository(ctx, a, "bobsite", g.addr); return err },
			[]Kind{ok, ok, ok, Forbidden, ok, Forbidden, NotFound, Unauthenticated, ok}},
		{"link", func(a Actor) error { _, err := g.svc.SetGitSiteSource(ctx, a, "bobsite", in); return err },
			[]Kind{ok, ok, ok, Forbidden, ok, Forbidden, NotFound, Unauthenticated, Forbidden}},
		{"check", func(a Actor) error { _, err := g.svc.CheckGitSiteSource(ctx, a, "bobsite"); return err },
			[]Kind{ok, ok, ok, Forbidden, ok, Forbidden, NotFound, Unauthenticated, ok}},
		{"accept key", func(a Actor) error { _, err := g.svc.AcceptGitHostKey(ctx, a, "bobsite", fp); return err },
			[]Kind{ok, ok, ok, Forbidden, ok, Forbidden, NotFound, Unauthenticated, ok}},
		{"new notification secret", func(a Actor) error { _, err := g.svc.ReplaceGitNotifySecret(ctx, a, "bobsite"); return err },
			[]Kind{ok, ok, ok, Forbidden, ok, Forbidden, NotFound, Unauthenticated, Forbidden}},
		{"link another member's site", func(a Actor) error { _, err := g.svc.SetGitSiteSource(ctx, a, "bobother", in); return err },
			[]Kind{ok, ok, ok, Forbidden, NotFound, Forbidden, NotFound, Unauthenticated, Forbidden}},
		{"link admin's site", func(a Actor) error { _, err := g.svc.SetGitSiteSource(ctx, a, "rootsite", in); return err },
			[]Kind{ok, ok, NotFound, NotFound, NotFound, Forbidden, ok, Unauthenticated, Forbidden}},
	}
	for _, op := range ops {
		for i, ac := range actors {
			// Plenty of room in the rate limit for this test.
			g.svc.gitChecks = gitChecks{}
			err := op.run(ac.a)
			got := ok
			if err != nil {
				got = KindOf(err)
			}
			if got != op.want[i] {
				t.Errorf("%s as %s: got %v (%v), want %v", op.name, ac.name, got, err, op.want[i])
			}
		}
	}
	g.assertNoGitSecrets("log", g.logs.String())
}

func TestGitChecksAreRateLimited(t *testing.T) {
	g := newGitWorld(t)
	ctx := context.Background()
	var err error
	for range int(gitCheckBurst) + 1 {
		if _, err = g.svc.InspectGitRepository(ctx, bob, "bobsite", g.addr); err != nil {
			break
		}
	}
	wantKind(t, err, Throttled)
	// Others are not affected.
	if _, err := g.svc.InspectGitRepository(ctx, admin, "bobsite", g.addr); err != nil {
		t.Fatal(err)
	}
}
