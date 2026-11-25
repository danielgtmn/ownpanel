package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/store"
)

const fakeReleases = `[
 {"tag_name":"nightly","name":"Nightly nightly-20261124-878136d","prerelease":true,"published_at":"2026-11-24T22:47:01Z",
  "html_url":"https://github.com/o/r/releases/tag/nightly",
  "assets":[{"name":"checksums.txt"},{"name":"ownpanel_nightly-20261124-878136d_linux_amd64.tar.gz"}]},
 {"tag_name":"v1.2.0","name":"v1.2.0","published_at":"2026-11-20T10:00:00Z","html_url":"https://github.com/o/r/releases/tag/v1.2.0",
  "assets":[{"name":"checksums.txt"},{"name":"ownpanel_v1.2.0_linux_amd64.tar.gz"}]},
 {"tag_name":"v1.1.0","name":"v1.1.0","published_at":"2026-11-01T10:00:00Z","html_url":"https://github.com/o/r/releases/tag/v1.1.0",
  "assets":[{"name":"checksums.txt"},{"name":"ownpanel_v1.1.0_linux_amd64.tar.gz"}]}
]`

type updatesEnv struct {
	*env
	dir     string // the updater's directory
	release string // install.sh's release file
}

// newUpdatesEnv is an installation of version on channel, with a fake GitHub.
// installed=false means no release file (not installed by install.sh).
func newUpdatesEnv(t *testing.T, version, channel string, installed bool) *updatesEnv {
	t.Helper()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/releases" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(fakeReleases))
	}))
	t.Cleanup(gh.Close)
	dir := t.TempDir()
	upd := filepath.Join(dir, "update")
	os.Mkdir(upd, 0o770)
	rel := filepath.Join(dir, "release")
	if installed {
		os.WriteFile(rel, []byte("# Written by install.sh\nchannel="+channel+"\nversion="+version+"\n"), 0o644)
	}
	st, err := store.Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := panel.New(panel.Config{
		Store:         st,
		Deploys:       &deploy.Manager{Base: filepath.Join(dir, "sites")},
		Web:           nopWeb{},
		Caddy:         caddy.Options{AdminListen: "127.0.0.1:2019", TLSMode: caddy.TLSOff, HTTPPort: 8080, HTTPSPort: 8443},
		SetupCodeFile: filepath.Join(dir, "setup-code"),
		Updates: panel.UpdatesConfig{
			Version: version, Repo: "o/r", APIURL: gh.URL, ReleaseFile: rel, Dir: upd, Arch: "amd64",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureSetupCode(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, h: (&Server{Panel: svc}).Handler(), svc: svc, code: filepath.Join(dir, "setup-code")}
	e.cookie = e.setup("root", "root-password-1")
	return &updatesEnv{env: e, dir: upd, release: rel}
}

func (u *updatesEnv) file(name string) string {
	b, err := os.ReadFile(filepath.Join(u.dir, name))
	if err != nil {
		return ""
	}
	return string(b)
}

func TestUpdatesOverview(t *testing.T) {
	u := newUpdatesEnv(t, "v1.1.0", "stable", true)
	rec := u.as("GET", "/api/updates", "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	info := decode[panel.UpdateInfo](t, rec)
	if !info.Supported || info.Installed != (panel.InstalledRelease{Version: "v1.1.0", Channel: "stable"}) || info.CheckFailed {
		t.Fatalf("%+v", info)
	}
	if info.Available.Stable == nil || info.Available.Stable.Version != "v1.2.0" || info.Available.Stable.Older ||
		info.Available.Stable.NotesURL == "" || info.Available.Nightly == nil {
		t.Fatalf("available: %s", rec.Body)
	}
	if !info.UpdateAvailable || info.Status.State != "idle" || info.Auto.Mode != "off" {
		t.Fatalf("%s", rec.Body)
	}
	for _, key := range []string{`"checked_at":`, `"check_error":`, `"installed":`, `"available":`, `"update_available":`, `"auto":`, `"status":`, `"supported":`,
		`"published_at":`, `"notes_url":`, `"started_at":`, `"finished_at":`, `"from":`, `"to":`, `"step":`, `"message":`, `"window":`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Errorf("answer lacks %s: %s", key, rec.Body)
		}
	}
}

func TestCheckForUpdatesEndpoint(t *testing.T) {
	u := newUpdatesEnv(t, "v1.1.0", "stable", true)
	rec := u.as("POST", "/api/updates/check", "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	info := decode[panel.UpdateInfo](t, rec)
	if !info.UpdateAvailable || info.Available.Stable == nil || info.Available.Stable.Version != "v1.2.0" || info.CheckFailed {
		t.Fatalf("%s", rec.Body)
	}
	checked, err := time.Parse(time.RFC3339, info.CheckedAt)
	if err != nil || time.Since(checked) > time.Minute {
		t.Fatalf("checked_at %q: %v", info.CheckedAt, err)
	}
	// GET tells the same time.
	if got := decode[panel.UpdateInfo](t, u.as("GET", "/api/updates", "")); got.CheckedAt != info.CheckedAt {
		t.Fatalf("GET checked_at %q, check %q", got.CheckedAt, info.CheckedAt)
	}
}

func TestUpdatesNotInstalledByInstaller(t *testing.T) {
	u := newUpdatesEnv(t, "dev", "", false)
	info := decode[panel.UpdateInfo](t, u.as("GET", "/api/updates", ""))
	if info.Supported || info.UpdateAvailable {
		t.Fatalf("%+v", info)
	}
	if rec := u.as("POST", "/api/updates", `{"channel":"stable"}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "installer") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := u.as("PUT", "/api/updates/auto", `{"mode":"off","window":"03:00-05:00"}`); rec.Code != 409 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if u.file("request.json") != "" {
		t.Fatal("request written")
	}
}

func TestUpdatesPermissions(t *testing.T) {
	u := newUpdatesEnv(t, "v1.1.0", "stable", true)
	u.as("PUT", "/api/sites/demo", `{"domains":["demo.example.com"]}`)
	_, readTok := u.createToken("read", "null")
	_, siteTok := u.createToken("write", `["demo"]`)
	_, writeTok := u.createToken("write", "null")
	u.as("POST", "/api/users", `{"name":"bob","password":"bob-password-1","role":"member"}`)
	bob := sessionCookie(t, u.do(req{method: "POST", path: "/api/login", body: `{"name":"bob","password":"bob-password-1"}`}))

	if rec := u.do(req{method: "GET", path: "/api/updates"}); rec.Code != 401 {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	if rec := u.do(req{method: "POST", path: "/api/updates/check"}); rec.Code != 401 {
		t.Fatalf("anonymous check: %d", rec.Code)
	}
	for name, r := range map[string]req{
		"member":           {method: "POST", path: "/api/updates", body: `{"channel":"stable"}`, cookie: bob, csrf: true},
		"read token":       {method: "POST", path: "/api/updates", body: `{"channel":"stable"}`, token: readTok},
		"site token":       {method: "POST", path: "/api/updates", body: `{"channel":"stable"}`, token: siteTok},
		"member get":       {method: "GET", path: "/api/updates", cookie: bob},
		"read token auto":  {method: "PUT", path: "/api/updates/auto", body: `{"mode":"off","window":"03:00-05:00"}`, token: readTok},
		"member check":     {method: "POST", path: "/api/updates/check", cookie: bob, csrf: true},
		"read token check": {method: "POST", path: "/api/updates/check", token: readTok},
		"site token check": {method: "POST", path: "/api/updates/check", token: siteTok},
	} {
		if rec := u.do(r); rec.Code != 403 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if u.file("request.json") != "" {
		t.Fatal("a refused caller wrote a request")
	}
	// Without the CSRF header a session cannot ask for an update.
	if rec := u.do(req{method: "POST", path: "/api/updates", body: `{"channel":"stable"}`, cookie: u.cookie}); rec.Code != 403 {
		t.Fatalf("no csrf: %d", rec.Code)
	}
	if rec := u.do(req{method: "POST", path: "/api/updates/check", cookie: u.cookie}); rec.Code != 403 {
		t.Fatalf("check without csrf: %d", rec.Code)
	}
	if rec := u.do(req{method: "POST", path: "/api/updates/check", token: writeTok}); rec.Code != 200 {
		t.Fatalf("check with write token: %d %s", rec.Code, rec.Body)
	}
	// An unrestricted write token of an admin can.
	if rec := u.do(req{method: "POST", path: "/api/updates", body: `{"channel":"stable"}`, token: writeTok}); rec.Code != 202 {
		t.Fatalf("write token: %d %s", rec.Code, rec.Body)
	}
}

// The root side accepts exactly these shapes (deploy/ownpanel-update).
var (
	rootChannelRe = regexp.MustCompile(`^\{"id":"([0-9a-f]{32})","channel":"(stable|nightly)"\}$`)
	rootVersionRe = regexp.MustCompile(`^\{"id":"([0-9a-f]{32})","version":"(v[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4})"\}$`)
)

func TestRequestUpdate(t *testing.T) {
	u := newUpdatesEnv(t, "v1.1.0", "stable", true)
	for _, bad := range []string{`{}`, `{"channel":"beta"}`, `{"version":"1.2"}`, `{"version":"v1.2.0; rm -rf /"}`,
		`{"channel":"stable","version":"v1.2.0"}`, `{"version":"nightly-20261124-878136d"}`} {
		if rec := u.as("POST", "/api/updates", bad); rec.Code != 400 {
			t.Errorf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}

	rec := u.as("POST", "/api/updates", `{"channel":"stable"}`)
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if info := decode[panel.UpdateInfo](t, rec); info.Status.State != "requested" || info.Status.To != "v1.2.0" {
		t.Fatalf("status: %+v", info.Status)
	}
	body := u.file("request.json")
	if m := rootChannelRe.FindStringSubmatch(body); m == nil || m[2] != "stable" {
		t.Fatalf("request.json %q does not match what the root side accepts", body)
	}
	st, _ := os.Stat(filepath.Join(u.dir, "request.json"))
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode())
	}

	// One at a time: while requested, and while running.
	if rec := u.as("POST", "/api/updates", `{"channel":"stable"}`); rec.Code != 409 {
		t.Fatalf("second request: %d %s", rec.Code, rec.Body)
	}
	os.Remove(filepath.Join(u.dir, "request.json"))
	os.WriteFile(filepath.Join(u.dir, "status.json"), []byte(`{"state":"running","step":"downloading","started_at":"2999-01-01T00:00:00Z","from":"v1.1.0","to":"v1.2.0"}`), 0o640)
	if rec := u.as("POST", "/api/updates", `{"version":"v1.2.0"}`); rec.Code != 409 {
		t.Fatalf("while running: %d %s", rec.Code, rec.Body)
	}
	os.Remove(filepath.Join(u.dir, "status.json"))

	rec = u.as("POST", "/api/updates", `{"version":"v1.2.0"}`)
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if body := u.file("request.json"); !rootVersionRe.MatchString(body) {
		t.Fatalf("request.json %q does not match what the root side accepts", body)
	}
	os.Remove(filepath.Join(u.dir, "request.json"))

	act := u.as("GET", "/api/activity", "").Body.String()
	if !strings.Contains(act, `"action":"update.request"`) || !strings.Contains(act, "v1.1.0 to v1.2.0 (stable)") {
		t.Fatalf("activity: %s", act)
	}
}

func TestRequestUpdateRefusesDowngrade(t *testing.T) {
	u := newUpdatesEnv(t, "v1.2.0", "stable", true)
	if rec := u.as("POST", "/api/updates", `{"channel":"stable"}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "already installed") {
		t.Fatalf("same version: %d %s", rec.Code, rec.Body)
	}
	rec := u.as("POST", "/api/updates", `{"version":"v1.1.0"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "older") {
		t.Fatalf("downgrade: %d %s", rec.Code, rec.Body)
	}
	if u.file("request.json") != "" {
		t.Fatal("downgrade requested without confirmation")
	}
	if rec := u.as("POST", "/api/updates", `{"version":"v1.1.0","allow_downgrade":true}`); rec.Code != 202 {
		t.Fatalf("confirmed downgrade: %d %s", rec.Code, rec.Body)
	}
	os.Remove(filepath.Join(u.dir, "request.json"))

	// From the nightly, the stable release is older (made before it).
	n := newUpdatesEnv(t, "nightly-20261124-878136d", "nightly", true)
	info := decode[panel.UpdateInfo](t, n.as("GET", "/api/updates", ""))
	if info.Available.Stable == nil || !info.Available.Stable.Older || info.UpdateAvailable {
		t.Fatalf("%+v", info)
	}
	if rec := n.as("POST", "/api/updates", `{"channel":"stable"}`); rec.Code != 409 {
		t.Fatalf("switch to older stable: %d %s", rec.Code, rec.Body)
	}
	if rec := n.as("POST", "/api/updates", `{"channel":"stable","allow_downgrade":true}`); rec.Code != 202 {
		t.Fatalf("confirmed switch: %d %s", rec.Code, rec.Body)
	}
}

func TestAutoUpdateSetting(t *testing.T) {
	u := newUpdatesEnv(t, "v1.1.0", "stable", true)
	for _, bad := range []string{`{"mode":"always"}`, `{"mode":"stable","window":"3-5"}`, `{"mode":"stable","window":"05:00-05:00"}`} {
		if rec := u.as("PUT", "/api/updates/auto", bad); rec.Code != 400 {
			t.Errorf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	// Only the installed channel.
	if rec := u.as("PUT", "/api/updates/auto", `{"mode":"nightly","window":"03:00-05:00"}`); rec.Code != 409 {
		t.Fatalf("other channel: %d %s", rec.Code, rec.Body)
	}
	rec := u.as("PUT", "/api/updates/auto", `{"mode":"stable","window":"22:30-01:00"}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if info := decode[panel.UpdateInfo](t, rec); info.Auto != (panel.AutoUpdate{Mode: "stable", Window: "22:30-01:00"}) {
		t.Fatalf("%+v", info.Auto)
	}
	if got := u.file("auto.json"); got != `{"mode":"stable","window":"22:30-01:00"}` {
		t.Fatalf("auto.json: %s", got)
	}
	if !strings.Contains(u.as("GET", "/api/activity", "").Body.String(), `"action":"update.auto"`) {
		t.Fatal("no activity entry")
	}

	// Switching channels moves automatic updates along.
	if rec := u.as("POST", "/api/updates", `{"channel":"nightly"}`); rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := u.file("auto.json"); got != `{"mode":"nightly","window":"22:30-01:00"}` {
		t.Fatalf("auto.json after switch: %s", got)
	}
}

func TestUpdateResultIsWatchedAfterRestart(t *testing.T) {
	u := newUpdatesEnv(t, "v1.2.0", "stable", true)
	status := filepath.Join(u.dir, "status.json")
	os.WriteFile(status, []byte(`{"id":"auto","state":"running","step":"verifying","started_at":"2999-01-01T00:00:00Z","from":"v1.1.0","to":"v1.2.0"}`), 0o640)
	done := make(chan struct{})
	go func() {
		u.svc.WatchUpdateResult(context.Background(), 10*time.Millisecond, 5*time.Second)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	os.WriteFile(status, []byte(`{"id":"auto","state":"succeeded","started_at":"2999-01-01T00:00:00Z","finished_at":"2999-01-01T00:01:00Z","from":"v1.1.0","to":"v1.2.0"}`), 0o640)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still watching")
	}
	act := u.as("GET", "/api/activity", "").Body.String()
	if !strings.Contains(act, "updated from v1.1.0 to v1.2.0 (automatic)") {
		t.Fatalf("activity: %s", act)
	}
}

func TestUpdateResultIsRecordedOnce(t *testing.T) {
	u := newUpdatesEnv(t, "v1.2.0", "stable", true)
	os.WriteFile(filepath.Join(u.dir, "status.json"), []byte(`{"id":"0123456789abcdef0123456789abcdef","state":"rolled_back","step":"","message":"The update to v1.3.0 failed.","started_at":"2026-11-25T03:00:00Z","finished_at":"2026-11-25T03:02:00Z","from":"v1.2.0","to":"v1.3.0"}`), 0o640)
	u.svc.RecordUpdateResult(context.Background())
	info := decode[panel.UpdateInfo](t, u.as("GET", "/api/updates", ""))
	if info.Status.State != "rolled_back" || info.Status.To != "v1.3.0" || info.Status.FinishedAt == "" {
		t.Fatalf("%+v", info.Status)
	}
	act := u.as("GET", "/api/activity", "").Body.String()
	if n := strings.Count(act, `"action":"update.result"`); n != 1 {
		t.Fatalf("recorded %d times: %s", n, act)
	}
	if !strings.Contains(act, "v1.2.0 was put back") {
		t.Fatalf("detail: %s", act)
	}
}
