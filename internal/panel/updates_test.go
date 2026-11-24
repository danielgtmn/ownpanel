package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// releaseList is shaped like GitHub's answer: the nightly is the rolling
// prerelease, titled with the build it holds.
const releaseList = `[
 {"tag_name":"nightly","name":"Nightly nightly-20261124-878136d","draft":false,"prerelease":true,
  "published_at":"2026-11-24T22:47:01Z","html_url":"https://github.com/o/r/releases/tag/nightly",
  "assets":[{"name":"checksums.txt"},{"name":"ownpanel_nightly-20261124-878136d_linux_amd64.tar.gz"},{"name":"ownpanel_nightly-20261124-878136d_linux_arm64.tar.gz"}]},
 {"tag_name":"v1.3.0","name":"v1.3.0","draft":true,"prerelease":false,"published_at":"2026-11-23T10:00:00Z","assets":[]},
 {"tag_name":"v1.2.0","name":"v1.2.0","draft":false,"prerelease":false,"published_at":"2026-11-20T10:00:00Z",
  "html_url":"https://github.com/o/r/releases/tag/v1.2.0",
  "assets":[{"name":"checksums.txt"},{"name":"ownpanel_v1.2.0_linux_amd64.tar.gz"}]},
 {"tag_name":"v1.10.0-rc.1","name":"v1.10.0-rc.1","draft":false,"prerelease":true,"published_at":"2026-11-22T10:00:00Z","assets":[]},
 {"tag_name":"v1.1.9","name":"v1.1.9","draft":false,"prerelease":false,"published_at":"2026-11-01T10:00:00Z",
  "assets":[{"name":"checksums.txt"},{"name":"ownpanel_v1.1.9_linux_amd64.tar.gz"}]}
]`

func TestPickReleases(t *testing.T) {
	u := newUpdater(UpdatesConfig{Repo: "o/r", Arch: "amd64"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/releases" || r.Header.Get("Accept") != "application/vnd.github+json" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(releaseList))
	}))
	defer srv.Close()
	u.cfg.APIURL = srv.URL
	got, dates, err := u.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Stable == nil || got.Stable.Version != "v1.2.0" || got.Stable.NotesURL != "https://github.com/o/r/releases/tag/v1.2.0" {
		t.Fatalf("stable: %+v", got.Stable)
	}
	if got.Nightly == nil || got.Nightly.Version != "nightly-20261124-878136d" {
		t.Fatalf("nightly: %+v", got.Nightly)
	}
	if _, ok := dates["v1.1.9"]; !ok {
		t.Fatalf("dates: %v", dates)
	}

	// A nightly whose archive for this machine is not uploaded yet (the
	// release is being published) is not offered.
	got, _, _ = pickReleases([]githubRelease{{TagName: "nightly", Name: "Nightly nightly-20261125-abcdef0"}}, "amd64")
	if got.Nightly != nil {
		t.Fatalf("incomplete nightly offered: %+v", got.Nightly)
	}
}

func TestIsOlder(t *testing.T) {
	dates := map[string]time.Time{
		"v1.2.0": time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}
	cases := []struct {
		target, installed string
		want              bool
	}{
		{"v1.2.0", "v1.10.0", true},
		{"v1.10.0", "v1.2.0", false},
		{"v1.2.0", "v1.2.0", false},
		{"nightly-20261119-aaaaaaa", "nightly-20261124-bbbbbbb", true},
		{"nightly-20261124-aaaaaaa", "nightly-20261124-bbbbbbb", false},
		{"v1.2.0", "nightly-20261124-bbbbbbb", true},  // stable cut before the nightly
		{"nightly-20261124-bbbbbbb", "v1.2.0", false}, // nightly newer than stable
		{"v9.9.9", "nightly-20261124-bbbbbbb", false}, // date unknown: not older
		{"v1.2.0", "dev", false},
	}
	for _, c := range cases {
		if got := isOlder(c.target, c.installed, dates); got != c.want {
			t.Errorf("isOlder(%s, %s) = %v, want %v", c.target, c.installed, got, c.want)
		}
	}
}

func TestReadStatus(t *testing.T) {
	dir := t.TempDir()
	u := newUpdater(UpdatesConfig{Dir: dir})
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	if st := u.readStatus(now); st.State != UpdateIdle {
		t.Fatalf("no file: %+v", st)
	}
	write("status.json", `{"id":"auto","state":"running","step":"installing","message":"Installing v1.2.0","started_at":"2026-11-25T11:59:00Z","finished_at":"","from":"v1.1.9","to":"v1.2.0"}`)
	if st := u.readStatus(now); st.State != UpdateRunning || st.Step != "installing" || st.From != "v1.1.9" || st.To != "v1.2.0" || st.id != "auto" {
		t.Fatalf("running: %+v", st)
	}
	// Running for too long: the update died.
	if st := u.readStatus(now.Add(time.Hour)); st.State != UpdateFailed || !strings.Contains(st.Message, "stopped") {
		t.Fatalf("stale: %+v", st)
	}
	// A waiting request wins.
	write("request.json", `{}`)
	if st := u.readStatus(now); st.State != UpdateRequested {
		t.Fatalf("requested: %+v", st)
	}
	os.Remove(filepath.Join(dir, "request.json"))
	write("status.json", `{"state":"exploded"}`)
	if st := u.readStatus(now); st.State != UpdateIdle {
		t.Fatalf("unknown state: %+v", st)
	}
	write("status.json", `not json`)
	if st := u.readStatus(now); st.State != UpdateIdle {
		t.Fatalf("broken file: %+v", st)
	}
}

func TestAutoSetting(t *testing.T) {
	dir := t.TempDir()
	u := newUpdater(UpdatesConfig{Dir: dir})
	if a := u.readAuto(); a.Mode != "off" || a.Window != "03:00-05:00" {
		t.Fatalf("default: %+v", a)
	}
	for _, bad := range []AutoUpdate{
		{"weekly", "03:00-05:00"}, {"stable", "3:00-5:00"}, {"stable", "24:00-01:00"},
		{"stable", "03:00-03:00"}, {"stable", "03:00-05:00 ; rm"}, {"", ""},
	} {
		if err := validAuto(bad); KindOf(err) != Invalid {
			t.Errorf("%+v accepted", bad)
		}
	}
	if err := validAuto(AutoUpdate{"nightly", "23:30-01:00"}); err != nil {
		t.Fatal(err)
	}
	if err := u.writeAuto(AutoUpdate{"stable", "01:00-02:00"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "auto.json"))
	// The root side matches exactly this shape.
	if string(b) != `{"mode":"stable","window":"01:00-02:00"}` {
		t.Fatalf("auto.json: %s", b)
	}
	os.WriteFile(filepath.Join(dir, "auto.json"), []byte(`{"mode":"sometimes","window":"01:00-02:00"}`), 0o640)
	if a := u.readAuto(); a.Mode != "off" {
		t.Fatalf("invalid file: %+v", a)
	}
}

func TestReleaseLookupIsCached(t *testing.T) {
	var calls atomic.Int32
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "rate limited", http.StatusForbidden)
			return
		}
		w.Write([]byte(releaseList))
	}))
	defer srv.Close()
	u := newUpdater(UpdatesConfig{Repo: "o/r", Arch: "amd64", APIURL: srv.URL})
	now := time.Now()
	for i := 0; i < 3; i++ {
		if _, _, err := u.releases(context.Background(), now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("looked up %d times within 30 minutes", calls.Load())
	}
	fail.Store(true)
	if _, _, err := u.releases(context.Background(), now.Add(31*time.Minute)); err == nil {
		t.Fatal("a failed lookup reported no error")
	}
	// Failures are retried sooner than successes.
	fail.Store(false)
	if _, _, err := u.releases(context.Background(), now.Add(34*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls: %d", calls.Load())
	}
}

func TestReleaseLookupTimesOut(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	u := newUpdater(UpdatesConfig{Repo: "o/r", APIURL: srv.URL, Client: &http.Client{Timeout: 100 * time.Millisecond}})
	if _, _, err := u.fetch(context.Background()); err == nil {
		t.Fatal("no timeout")
	}
}
