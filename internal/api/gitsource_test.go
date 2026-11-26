package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/danielgtmn/ownpanel/internal/gitsource"
	"github.com/danielgtmn/ownpanel/internal/gitsource/gitsourcetest"
	"github.com/danielgtmn/ownpanel/internal/panel"
)

func TestGitSourceOverHTTP(t *testing.T) {
	gitsource.AllowPrivateHosts()
	e, _ := newGitHubEnv(t)
	root := t.TempDir()
	repo := gitsourcetest.NewRepo(t, root, "ada/site.git", map[string]string{"index.html": "hi"})
	srv := gitsourcetest.NewSSH(t, root)
	if rec := e.as("PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`); rec.Code != 200 {
		t.Fatalf("site: %d %s", rec.Code, rec.Body)
	}
	addr := srv.Address("ada/site.git")

	rec := e.as("POST", "/api/sites/blog/source/git/inspect", fmt.Sprintf(`{"repository":%q}`, addr))
	insp := decode[panel.GitRepositoryCheck](t, rec)
	fp := ssh.FingerprintSHA256(srv.HostKey.PublicKey())
	if rec.Code != 200 || insp.HostKey != fp {
		t.Fatalf("inspect: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("POST", "/api/sites/blog/source/git/inspect", `{"repository":"ext::sh -c id"}`); rec.Code != 400 {
		t.Fatalf("hostile address: %d %s", rec.Code, rec.Body)
	}

	rec = e.as("PUT", "/api/sites/blog/source/git", fmt.Sprintf(`{"repository":%q,"branch":"main","publish_on_push":true,"host_key":%q}`, addr, fp))
	st := decode[panel.SiteSourceState](t, rec)
	if rec.Code != 200 || st.Source == nil || st.Source.DeployKey == "" || len(st.NotifySecret) != 64 || st.Source.NotifyURL == "" {
		t.Fatalf("link: %d %s", rec.Code, rec.Body)
	}
	secret := st.NotifySecret
	k, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(st.Source.DeployKey))
	srv.Authorize(k)

	rec = e.as("POST", "/api/sites/blog/source/git/check", "")
	chk := decode[panel.GitSourceCheck](t, rec)
	if rec.Code != 200 || !chk.OK || !chk.BranchFound {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	// The secret is never shown again.
	for _, path := range []string{"/api/sites/blog/source", "/api/sites/blog/runs"} {
		if rec := e.as("GET", path, ""); rec.Code != 200 || strings.Contains(rec.Body.String(), secret) ||
			strings.Contains(rec.Body.String(), "notify_secret") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := e.as("POST", "/api/sites/blog/source/git/check", ""); strings.Contains(rec.Body.String(), secret) {
		t.Fatal("check shows the secret")
	}

	// A notification: no session, no token, no CSRF header.
	hs := httptest.NewServer(e.h)
	t.Cleanup(hs.Close)
	hook := hs.URL + panel.GitNotifyPath + st.Source.NotifyURL[strings.LastIndexByte(st.Source.NotifyURL, '/')+1:]
	commit := repo.Head("main")
	body := fmt.Appendf(nil, `{"ref":"refs/heads/main","after":%q,"commits":[{"id":%q,"message":"Hello","author":{"name":"Ada"}}],"user_name":"ada"}`, commit, commit)
	send := func(h map[string]string, ctype string, body []byte) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", hook, bytes.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	gitlab := map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": secret, "X-Gitlab-Event-UUID": "u-1"}
	if code, answer := send(gitlab, "application/json", body); code != 200 || answer != `{"result":"handled","runs":1}`+"\n" {
		t.Fatalf("notify: %d %s", code, answer)
	}
	if code, answer := send(gitlab, "application/json", body); code != 200 || !strings.Contains(answer, "duplicate") {
		t.Fatalf("again: %d %s", code, answer)
	}
	mac := hmac.New(sha256.New, []byte("wrong"))
	mac.Write(body)
	bad := map[string]string{"X-Gitea-Signature": hex.EncodeToString(mac.Sum(nil))}
	code, wrong := send(bad, "application/json", body)
	if code != 400 || !strings.Contains(wrong, "not accepted") {
		t.Fatalf("wrong signature: %d %s", code, wrong)
	}
	hook = hs.URL + panel.GitNotifyPath + strings.Repeat("a", 32)
	code, unknown := send(gitlab, "application/json", body)
	if code != 400 || unknown != wrong {
		t.Fatalf("unknown address answers differently: %d %s", code, unknown)
	}
	if code, _ := send(gitlab, "application/x-www-form-urlencoded", []byte("payload=x")); code != 400 {
		t.Fatalf("form: %d", code)
	}

	runs := decode[panel.SiteRuns](t, e.as("GET", "/api/sites/blog/runs", ""))
	if len(runs.Runs) != 1 || runs.Runs[0].Commit != commit || runs.Runs[0].Message != "Hello" || runs.Runs[0].Pusher != "ada" {
		t.Fatalf("runs: %+v", runs.Runs)
	}

	// New secret and key.
	rec = e.as("POST", "/api/sites/blog/source/git/notify-secret", "")
	if st2 := decode[panel.SiteSourceState](t, rec); rec.Code != 200 || len(st2.NotifySecret) != 64 || st2.NotifySecret == secret {
		t.Fatalf("new secret: %d %s", rec.Code, rec.Body)
	}
	rec = e.as("POST", "/api/sites/blog/source/git/deploy-key", "")
	if st2 := decode[panel.SiteSourceState](t, rec); rec.Code != 200 || st2.Source.DeployKey == st.Source.DeployKey || st2.NotifySecret != "" {
		t.Fatalf("new key: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("POST", "/api/sites/blog/source/git/host-key", fmt.Sprintf(`{"host_key":%q}`, fp)); rec.Code != 200 {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	var raw map[string]any
	json.Unmarshal(e.as("GET", "/api/sites/blog/source", "").Body.Bytes(), &raw)
	if raw["source"].(map[string]any)["transport"] != "ssh" {
		t.Fatalf("source: %v", raw)
	}
}
