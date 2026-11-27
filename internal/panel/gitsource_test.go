package panel

import (
	"archive/tar"
	"bytes"
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
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/danielgtmn/ownpanel/internal/build/buildtest"
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
	if err != nil || res.Result != DeliveryAccepted {
		t.Fatalf("new: %+v %v", res, err)
	}
	g.svc.gitTips.wg.Wait()
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
	gl := func(c string) []byte {
		return fmt.Appendf(nil, `{"object_kind":"push","ref":"refs/heads/main","after":%q,"checkout_sha":%q,"user_name":"Ada L",`+
			`"commits":[{"id":%q,"message":"From GitLab","author":{"name":"Ada","email":"a@example.com"}}]}`, c, c, c)
	}
	bbc := func(c string) []byte {
		return fmt.Appendf(nil, `{"actor":{"display_name":"Ada"},"push":{"changes":[{"new":{"type":"branch","name":"main",`+
			`"target":{"hash":%q,"message":"From Bitbucket\n","author":{"raw":"Ada <a@example.com>"}}}}]}}`, c)
	}
	bbs := func(c string) []byte {
		return fmt.Appendf(nil, `{"eventKey":"repo:refs_changed","actor":{"name":"ada"},"changes":[{"ref":{"id":"refs/heads/main",`+
			`"displayId":"main","type":"BRANCH"},"fromHash":"0000000000000000000000000000000000000001","toHash":%q,"type":"UPDATE"}]}`, c)
	}
	gh := func(c string) []byte { return pushBody("refs/heads/main", c) }

	cases := []struct {
		name    string
		body    func(string) []byte
		headers func(body []byte, d string) map[string]string
		message string
	}{
		{"GitHub", gh, func(b []byte, d string) map[string]string {
			return map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": d, "X-Hub-Signature-256": "sha256=" + hmacHex(secret, b),
				"X-Hub-Signature": "sha1=0123456789012345678901234567890123456789"}
		}, "Fix the header"},
		{"Gitea", gh, func(b []byte, d string) map[string]string {
			return map[string]string{"X-Gitea-Event": "push", "X-Gitea-Delivery": d, "X-Gitea-Signature": hmacHex(secret, b)}
		}, "Fix the header"},
		// Exactly the headers Gitea 1.24 sends (taken from a real delivery),
		// with a SHA-1 signature next to the SHA-256 ones.
		{"Gitea, all headers", gh, func(b []byte, d string) map[string]string {
			return map[string]string{"X-GitHub-Delivery": d, "X-GitHub-Event": "push", "X-GitHub-Event-Type": "push",
				"X-Gitea-Delivery": d, "X-Gitea-Event": "push", "X-Gitea-Signature": hmacHex(secret, b), "X-Gogs-Delivery": d,
				"X-Gogs-Event": "push", "X-Gogs-Signature": hmacHex(secret, b), "X-Hub-Signature": "sha1=2e9f536656c0009e9be5e1d156198d063edca087",
				"X-Hub-Signature-256": "sha256=" + hmacHex(secret, b)}
		}, "Fix the header"},
		{"Forgejo", gh, func(b []byte, d string) map[string]string {
			return map[string]string{"X-Forgejo-Event": "push", "X-Forgejo-Delivery": d, "X-Forgejo-Signature": hmacHex(secret, b),
				"X-Hub-Signature-256": "sha256=" + hmacHex(secret, b)}
		}, "Fix the header"},
		{"Gogs", gh, func(b []byte, d string) map[string]string {
			return map[string]string{"X-Gogs-Event": "push", "X-Gogs-Delivery": d, "X-Gogs-Signature": hmacHex(secret, b)}
		}, "Fix the header"},
		{"GitLab", gl, func(b []byte, d string) map[string]string {
			return map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Event-UUID": d, "X-Gitlab-Token": secret}
		}, "From GitLab"},
		{"Bitbucket Cloud", bbc, func(b []byte, d string) map[string]string {
			return map[string]string{"X-Event-Key": "repo:push", "X-Request-UUID": "{" + d + "}", "X-Hub-Signature": "sha256=" + hmacHex(secret, b)}
		}, "From Bitbucket"},
		{"Bitbucket Server", bbs, func(b []byte, d string) map[string]string {
			return map[string]string{"X-Event-Key": "repo:refs_changed", "X-Request-Id": d, "X-Hub-Signature": "sha256=" + hmacHex(secret, b)}
		}, ""},
	}
	send := func(h map[string]string, body []byte) (GitHubDeliveryResult, error) {
		hh := http.Header{}
		for k, v := range h {
			hh.Set(k, v)
		}
		res, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: hh, Body: body})
		g.svc.gitTips.wg.Wait()
		return res, err
	}
	for i, c := range cases {
		// A new push for every host: the panel records the commit it finds.
		commit := g.repo.Commit(c.name, map[string]string{"n.txt": c.name})
		body := c.body(commit)
		h := c.headers(body, fmt.Sprintf("d-%d", i))
		res, err := send(h, body)
		if err != nil || res.Result != DeliveryAccepted {
			t.Fatalf("%s: %+v %v", c.name, res, err)
		}
		runs, _ := g.svc.ListSiteRuns(ctx, bob, "bobsite")
		if len(runs.Runs) != i+1 {
			t.Fatalf("%s: %d runs", c.name, len(runs.Runs))
		}
		r := runs.Runs[0]
		if r.Commit != commit || r.Branch != "main" || r.Message != c.message || r.Status != "queued" || r.Repository != st.Source.Repository ||
			r.CommitURL != "" {
			t.Fatalf("%s: run %+v", c.name, r)
		}
		// The same delivery again.
		res, err = send(h, body)
		if err != nil || res.Result != DeliveryDuplicate {
			t.Fatalf("%s replayed: %+v %v", c.name, res, err)
		}
	}
	commit := g.repo.Head("main")
	body := gh(commit)

	reject := func(name string, id string, h http.Header, body []byte) {
		t.Helper()
		if _, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: h, Body: body}); err != ErrDeliveryRejected {
			t.Errorf("%s: %v", name, err)
		}
	}
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		h.Set("X-GitHub-Delivery", "r-"+fmt.Sprint(len(kv)))
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	reject("no proof", id, hdr("X-GitHub-Event", "push"), body)
	reject("wrong signature", id, hdr("X-Hub-Signature-256", "sha256="+hmacHex("wrong", body)), body)
	reject("signature of another body", id, hdr("X-Hub-Signature-256", "sha256="+hmacHex(secret, gl(commit))), body)
	reject("only a sha1 signature", id, hdr("X-Hub-Signature", "sha1="+hmacHex(secret, body)[:40]), body)
	reject("sha1 next to a wrong sha256", id, hdr("X-Hub-Signature", "sha1=00", "X-Hub-Signature-256", "sha256="+hmacHex("x", body)), body)
	reject("bad hex", id, hdr("X-Gitea-Signature", "zz"), body)
	reject("wrong token", id, hdr("X-Gitlab-Token", secret+"x"), gl(commit))
	reject("one right, one wrong", id, hdr("X-Gitlab-Token", secret, "X-Hub-Signature-256", "sha256="+hmacHex("x", gl(commit))), gl(commit))
	reject("unknown address", strings.Repeat("0", 32), githubHeaders(secret, body, "x1"), body)
	reject("address of another form", "../../x", githubHeaders(secret, body, "x2"), body)
	bad := []byte(`{"ref": "refs/heads/main", "after": 5}`)
	reject("malformed", id, githubHeaders(secret, bad, "x3"), bad)
	two := append(append([]byte{}, body...), body...)
	reject("two values", id, githubHeaders(secret, two, "x4"), two)
	reject("bad delivery id", id, githubHeaders(secret, body, "a b"), body)
	// Without a delivery id it could be replayed unnoticed: refused.
	noID := http.Header{}
	noID.Set("X-Hub-Signature-256", "sha256="+hmacHex(secret, body))
	reject("without delivery id", id, noID, body)

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
	// Publish on push off.
	if _, err := g.svc.SetGitSiteSource(ctx, bob, "bobsite", GitSourceInput{Repository: g.addr, Branch: "main", HostKey: g.fingerprint()}); err != nil {
		t.Fatal(err)
	}
	ignored("publish on push off", body, sign(body, "off1", "push"))

	runs, _ := g.svc.ListSiteRuns(ctx, bob, "bobsite")
	g.assertNoGitSecrets("runs", asJSON(t, runs))
	g.assertNoGitSecrets("log", g.logs.String())
}

// M2 (security review): a notification only says that the branch changed.
// An old notification sent again with a new delivery id, or one naming
// any commit at all, records a run of the commit the branch points at
// now, and none when that commit already waits or was published.
func TestGitNotificationRecordsTheBranchTip(t *testing.T) {
	g := newGitWorld(t)
	ctx := context.Background()
	st := g.link(bob, "bobsite")
	id := strings.TrimPrefix(st.Source.NotifyURL, "https://panel.example.com:8443"+GitNotifyPath)
	send := func(commit, delivery string) {
		t.Helper()
		body := pushBody("refs/heads/main", commit)
		res, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: githubHeaders(st.NotifySecret, body, delivery), Body: body})
		if err != nil || res.Result != DeliveryAccepted {
			t.Fatalf("%+v %v", res, err)
		}
		g.svc.gitTips.wg.Wait()
	}
	old := g.repo.Head("main")
	tip := g.repo.Commit("newer", map[string]string{"index.html": "newer"})
	send(tip, "a")
	// The old push replayed (new id): no run of the old commit, and the
	// queued run of the tip is not replaced.
	send(old, "b")
	// Made up: a commit that does not exist.
	send(strings.Repeat("ab", 20), "c")
	runs, _ := g.svc.ListSiteRuns(ctx, bob, "bobsite")
	if len(runs.Runs) != 1 || runs.Runs[0].Commit != tip || runs.Runs[0].Status != "queued" || runs.Runs[0].Message != "Fix the header" {
		t.Fatalf("runs: %+v", runs.Runs)
	}
	// Notifications that name another commit than the tip leave the
	// message empty (it is not the tip's).
	if _, err := g.svc.store.FinishRun(ctx, runs.Runs[0].ID, "failed", "x"); err != nil {
		t.Fatal(err)
	}
	send(old, "d")
	runs, _ = g.svc.ListSiteRuns(ctx, bob, "bobsite")
	if len(runs.Runs) != 2 || runs.Runs[0].Commit != tip || runs.Runs[0].Message != "" {
		t.Fatalf("after a failed run: %+v", runs.Runs)
	}
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

// A build of a site linked by address gets the commit's files through the
// panel's own fetcher.
func TestGitSourceBuildCommit(t *testing.T) {
	g := newGitWorld(t)
	ctx := context.Background()
	fake := buildtest.Start(t)
	g.svc.builds = fake.Client()
	g.svc.build.poll = 5 * time.Millisecond
	rec := &teeSources{inner: g.svc.build.sources}
	g.svc.build.sources = rec
	g.link(bob, "bobsite")
	commit := g.repo.Head("main")
	res, err := g.svc.BuildCommit(ctx, bob, SourceRef{Site: "bobsite", Commit: commit}, settings, true, "")
	if err != nil || res.Status != RunSucceeded {
		t.Fatalf("%+v %v", res, err)
	}
	files := untar(t, io.NopCloser(bytes.NewReader(rec.buf.Bytes())))
	if files["index.html"] != "<h1>bob</h1>" || files["a/b.txt"] != "b" {
		t.Fatalf("source handed to the build: %v", files)
	}
	// A commit the branch no longer points at: the reason is told.
	g.repo.Commit("newer", map[string]string{"index.html": "newer"})
	_, err = g.svc.BuildCommit(ctx, bob, SourceRef{Site: "bobsite", Commit: commit}, settings, true, "")
	wantKind(t, err, Unavailable)
	if !strings.Contains(err.Error(), "now points at commit") {
		t.Fatalf("message: %v", err)
	}
	// GitHub sources are not fetched by the panel yet.
	_, err = g.svc.BuildCommit(ctx, admin, SourceRef{Site: "rootsite", Commit: commit}, settings, true, "")
	wantKind(t, err, Unavailable)
}

// teeSources keeps a copy of what the fetcher hands to the build.
type teeSources struct {
	inner SourceFetcher
	buf   bytes.Buffer
}

func (t *teeSources) FetchSource(ctx context.Context, ref SourceRef) (io.ReadCloser, int, error) {
	r, strip, err := t.inner.FetchSource(ctx, ref)
	if err != nil {
		return nil, 0, err
	}
	t.buf.Reset()
	return struct {
		io.Reader
		io.Closer
	}{io.TeeReader(r, &t.buf), r}, strip, nil
}

// Runs of a Git source never link to GitHub, whatever their address looks like.
func TestGitRunsHaveNoGitHubLink(t *testing.T) {
	g := newGitWorld(t)
	sha := strings.Repeat("a", 40)
	for _, repo := range []string{"git@codeberg.org:a/b.git", "ssh://git@codeberg.org:22/a/b.git", "https://codeberg.org/a/b.git"} {
		if v := g.svc.runViewFor(store.Run{SourceKind: store.SourceGit, Repository: repo, CommitSHA: sha}); v.CommitURL != "" {
			t.Errorf("%s: %s", repo, v.CommitURL)
		}
	}
	if v := g.svc.runViewFor(store.Run{SourceKind: store.SourceGitHub, Repository: "acme/site", CommitSHA: sha}); v.CommitURL == "" {
		t.Error("GitHub run without a link")
	}
}

// A push to a Git source is built and published by the runner, and
// "Build now" and proposing build settings work for Git sources too.
func TestGitSourceRunsAreBuilt(t *testing.T) {
	g := newGitWorld(t)
	ctx := context.Background()
	fake := buildtest.Start(t)
	g.svc.builds = fake.Client()
	g.svc.build.poll = 5 * time.Millisecond
	g.svc.gitTempDir = t.TempDir()
	st := g.link(bob, "bobsite")
	g.repo.Commit("site", map[string]string{"package.json": `{"devDependencies":{"vite":"5"},"scripts":{"build":"vite build"}}`})

	prop, err := g.svc.DetectBuildSettings(ctx, bob, "bobsite")
	if err != nil || prop.Commit != g.repo.Head("main") || prop.Result.Preset != "vite" {
		t.Fatalf("proposal: %+v %v", prop, err)
	}
	if _, err := g.svc.SetSiteBuildSettings(ctx, bob, "bobsite", SiteBuildSettingsInput{Command: "npm run build", Output: "dist", Image: "node-24"}); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); g.svc.RunBuilds(runCtx) }()
	defer func() { stop(); <-done }()

	wait := func(commit string) Run {
		t.Helper()
		for range 400 {
			runs, _ := g.svc.ListSiteRuns(ctx, bob, "bobsite")
			for _, r := range runs.Runs {
				if r.Commit == commit && store.RunFinished(r.Status) {
					return r
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no finished run of %s", commit)
		return Run{}
	}
	// A push.
	commit := g.repo.Commit("push", map[string]string{"index.html": "pushed"})
	id := strings.TrimPrefix(st.Source.NotifyURL, "https://panel.example.com:8443"+GitNotifyPath)
	body := pushBody("refs/heads/main", commit)
	if _, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: githubHeaders(st.NotifySecret, body, "e1"), Body: body}); err != nil {
		t.Fatal(err)
	}
	if r := wait(commit); r.Status != RunSucceeded || r.Release == "" {
		t.Fatalf("push run: %+v", r)
	}
	// Build now.
	r, err := g.svc.StartSiteBuild(ctx, bob, "bobsite")
	if err != nil || r.Commit != commit || r.Trigger != RunTriggerManual {
		t.Fatalf("build now: %+v %v", r, err)
	}
	if r := wait(commit); r.Status != RunSucceeded {
		t.Fatalf("manual run: %+v", r)
	}
	if _, err := g.svc.StartSiteBuild(ctx, bobRead, "bobsite"); KindOf(err) != Forbidden {
		t.Fatalf("read token: %v", err)
	}
	// Temporary files stay in the panel's directory and are gone after.
	if left, _ := os.ReadDir(g.svc.gitTempDir); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

// L3/M1 (security review): checks and fetches are limited in all and per
// person or site.
func TestGitSlots(t *testing.T) {
	l := slotLimiter{size: 2, perKey: 1}
	ctx := context.Background()
	a, err := l.acquire(ctx, "u1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.acquire(ctx, "u1", time.Second); KindOf(err) != Throttled {
		t.Fatalf("second for the same key: %v", err)
	}
	b, err := l.acquire(ctx, "u2", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := l.acquire(ctx, "u3", 50*time.Millisecond); KindOf(err) != Throttled || time.Since(start) > time.Second {
		t.Fatalf("all slots taken: %v", err)
	}
	a()
	a() // releasing twice is harmless
	c, err := l.acquire(ctx, "u3", time.Second)
	if err != nil {
		t.Fatalf("after a release: %v", err)
	}
	b()
	c()
	if len(l.keys) != 0 || len(l.slots) != 0 {
		t.Fatalf("left: %v %d", l.keys, len(l.slots))
	}
	// A fetch waits as long as its context allows.
	x, _ := l.acquire(ctx, "s1", 0)
	y, _ := l.acquire(ctx, "s2", 0)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := l.acquire(cctx, "s3", 0); err == nil {
		t.Fatal("got a slot although none was free")
	}
	x()
	y()
}

// Where #137 and #116 meet: a Git site's push is recorded and built first
// even when a run of a pull request (left from a GitHub link the site had
// before) waits with the same commit, and that run never replaces the push
// nor builds once the site is linked by address.
func TestGitPushAndPullRequestRuns(t *testing.T) {
	g := newGitWorld(t)
	ctx := context.Background()
	st := g.link(bob, "bobsite")
	tip := g.repo.Commit("newer", map[string]string{"index.html": "newer"})
	stale, err := g.svc.store.AddRun(ctx, store.Run{Site: "bobsite", Trigger: RunTriggerPullRequest, SourceKind: store.SourceGitHub,
		RepositoryID: 7, Repository: "acme/site", Ref: "refs/pull/3/head", CommitSHA: tip, PullRequest: 3,
		Preview: previewTag(7, 3)}, runSupersededDetail, RunsKept)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(st.Source.NotifyURL, "https://panel.example.com:8443"+GitNotifyPath)
	body := pushBody("refs/heads/main", tip)
	if res, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: githubHeaders(st.NotifySecret, body, "pr-1"), Body: body}); err != nil || res.Result != DeliveryAccepted {
		t.Fatalf("%+v %v", res, err)
	}
	g.svc.gitTips.wg.Wait()
	runs, _ := g.svc.ListSiteRuns(ctx, bob, "bobsite")
	if len(runs.Runs) != 2 || runs.Runs[0].Trigger != RunTriggerPush || runs.Runs[0].Commit != tip || runs.Runs[0].Status != "queued" ||
		runs.Runs[1].ID != stale.ID || runs.Runs[1].Status != "queued" {
		t.Fatalf("runs: %+v", runs.Runs)
	}
	// The same notification again (new id): still one push run.
	if _, err := g.svc.ReceiveGitNotification(ctx, GitNotification{ID: id, Header: githubHeaders(st.NotifySecret, body, "pr-2"), Body: body}); err != nil {
		t.Fatal(err)
	}
	g.svc.gitTips.wg.Wait()
	if runs, _ := g.svc.ListSiteRuns(ctx, bob, "bobsite"); len(runs.Runs) != 2 {
		t.Fatalf("replayed: %+v", runs.Runs)
	}
	// The worker takes the push first, then the pull request, which may
	// not build: the site is linked by address now.
	first, err := g.svc.store.ClaimNextRun(ctx, "bobsite", runSupersededDetail)
	if err != nil || first.Trigger != RunTriggerPush || first.SourceKind != store.SourceGit {
		t.Fatalf("first: %+v %v", first, err)
	}
	if _, err := g.svc.store.FinishRun(ctx, first.ID, store.RunSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	second, err := g.svc.store.ClaimNextRun(ctx, "bobsite", runSupersededDetail)
	if err != nil || second.ID != stale.ID {
		t.Fatalf("second: %+v %v", second, err)
	}
	if err := g.svc.previewRunAllowed(ctx, second); KindOf(err) != Conflict {
		t.Fatalf("a preview of a pull request for a site linked by address: %v", err)
	}
	// Previews of pull requests are GitHub's only.
	if _, err := g.svc.SetSitePreviews(ctx, bob, "bobsite", SitePreviewsInput{PullRequests: true}); KindOf(err) != Conflict {
		t.Fatalf("previews for a Git source: %v", err)
	}
}
