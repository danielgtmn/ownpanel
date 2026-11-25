package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func nightlyList(version, published, created string) string {
	return `[{"tag_name":"nightly","name":"Nightly ` + version + `","prerelease":true,
	  "published_at":"` + published + `","created_at":"` + created + `","html_url":"https://github.com/o/r/releases/tag/nightly",
	  "assets":[{"name":"checksums.txt"},{"name":"ownpanel_` + version + `_linux_amd64.tar.gz"}]}]`
}

// The nightly is one release that every build replaces: published_at stays
// at its first publication, created_at is the build.
func TestNightlyDateIsTheBuild(t *testing.T) {
	var list []githubRelease
	if err := json.Unmarshal([]byte(nightlyList("nightly-20261125-bd26838", "2026-11-24T22:47:01Z", "2026-11-25T05:50:00Z")), &list); err != nil {
		t.Fatal(err)
	}
	got, dates, _ := pickReleases(list, "amd64")
	want := time.Date(2026, 11, 25, 5, 50, 0, 0, time.UTC)
	if got.Nightly == nil || !got.Nightly.PublishedAt.Equal(want) || !dates["nightly-20261125-bd26838"].Equal(want) {
		t.Fatalf("nightly: %+v %v", got.Nightly, dates)
	}

	// Without created_at, the day in the build's name.
	list[0].CreatedAt = time.Time{}
	got, _, _ = pickReleases(list, "amd64")
	if got.Nightly == nil || !got.Nightly.PublishedAt.Equal(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("from the name: %+v", got.Nightly)
	}

	// Stable releases keep published_at.
	got, _, _ = pickReleases([]githubRelease{{TagName: "v1.2.0", PublishedAt: want.Add(time.Hour), CreatedAt: want,
		Assets: []struct {
			Name string `json:"name"`
		}{{Name: "checksums.txt"}, {Name: "ownpanel_v1.2.0_linux_amd64.tar.gz"}}}}, "amd64")
	if got.Stable == nil || !got.Stable.PublishedAt.Equal(want.Add(time.Hour)) {
		t.Fatalf("stable: %+v", got.Stable)
	}
}

// Asking for the nightly looks again first, so what is shown and recorded is
// the build the updater will install, not the one cached up to 30 minutes
// ago; the request names the channel, so the updater takes the current build
// even if it changes once more.
func TestRequestNightlyLooksAgain(t *testing.T) {
	var body atomic.Value
	body.Store(nightlyList("nightly-20261125-6a36d15", "2026-11-24T22:47:01Z", "2026-11-25T01:10:00Z"))
	var calls atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(body.Load().(string)))
	}))
	defer gh.Close()

	s, _, _ := newService(t)
	dir := t.TempDir()
	rel := filepath.Join(dir, "release")
	os.WriteFile(rel, []byte("channel=stable\nversion=v1.1.9\n"), 0o644)
	upd := filepath.Join(dir, "update")
	os.Mkdir(upd, 0o770)
	t0 := time.Date(2026, 11, 25, 6, 0, 0, 0, time.UTC)
	now := t0
	s.now = func() time.Time { return now }
	s.updates = newUpdater(UpdatesConfig{Version: "v1.1.9", Repo: "o/r", Arch: "amd64", APIURL: gh.URL, ReleaseFile: rel, Dir: upd})
	ctx := context.Background()

	info := s.updateInfo(ctx)
	if info.Available.Nightly == nil || info.Available.Nightly.Version != "nightly-20261125-6a36d15" {
		t.Fatalf("first look: %+v", info.Available.Nightly)
	}
	// A new build replaces the nightly; the page still shows the old one.
	body.Store(nightlyList("nightly-20261125-bd26838", "2026-11-24T22:47:01Z", "2026-11-25T05:50:00Z"))
	now = t0.Add(5 * time.Minute)
	if info := s.updateInfo(ctx); info.Available.Nightly.Version != "nightly-20261125-6a36d15" {
		t.Fatalf("cache: %+v", info.Available.Nightly)
	}

	info, err := s.RequestUpdate(ctx, admin, UpdateRequest{Channel: "nightly"})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || info.Status.State != UpdateRequested || info.Status.To != "nightly-20261125-bd26838" ||
		info.Available.Nightly.Version != "nightly-20261125-bd26838" {
		t.Fatalf("request: %d %+v %+v", calls.Load(), info.Status, info.Available.Nightly)
	}
	b, _ := os.ReadFile(filepath.Join(upd, updateRequestFile))
	var req map[string]any
	if err := json.Unmarshal(b, &req); err != nil || req["channel"] != "nightly" || req["version"] != nil {
		t.Fatalf("request.json %s", b)
	}

	// Once the updater runs, the version it reports is the one shown.
	os.Remove(filepath.Join(upd, updateRequestFile))
	os.WriteFile(filepath.Join(upd, updateStatusFile), []byte(`{"id":"x","state":"running","step":"downloading",
	  "started_at":"2026-11-25T06:05:00Z","from":"v1.1.9","to":"nightly-20261125-c0ffee0"}`), 0o640)
	if st := s.updateInfo(ctx).Status; st.State != UpdateRunning || st.To != "nightly-20261125-c0ffee0" {
		t.Fatalf("running: %+v", st)
	}

	act, err := s.store.Activities(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range act {
		found = found || (a.Action == "update.request" && strings.Contains(a.Detail, "to nightly-20261125-bd26838 (nightly)"))
	}
	if !found {
		t.Fatalf("activity: %+v", act)
	}
}
