package panel

import (
	"github.com/danielgtmn/sesam/v2"

	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// fakeWeb records every config it is given and can be told to fail.
type fakeWeb struct {
	mu    sync.Mutex
	cfgs  []string // JSON of each loaded config
	fail  error
	calls chan struct{}
}

func (f *fakeWeb) Load(_ context.Context, cfg any) error {
	b, _ := json.Marshal(cfg)
	f.mu.Lock()
	f.cfgs = append(f.cfgs, string(b))
	fail := f.fail
	f.mu.Unlock()
	if f.calls != nil {
		select {
		case f.calls <- struct{}{}:
		default:
		}
	}
	return fail
}

func (f *fakeWeb) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cfgs)
}

func (f *fakeWeb) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cfgs) == 0 {
		return ""
	}
	return f.cfgs[len(f.cfgs)-1]
}

func (f *fakeWeb) setFail(err error) {
	f.mu.Lock()
	f.fail = err
	f.mu.Unlock()
}

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// admin is the signed-in first user of every test service.
var admin = Actor{UserID: 1, Name: "root", Admin: true, Via: ViaSession}

func newService(t *testing.T) (*Service, *fakeWeb, string) {
	t.Helper()
	svc, web, sites := newEmptyService(t)
	// The first user gets id 1, which the admin actor refers to.
	addUser(t, svc, admin.Name, store.RoleAdmin)
	return svc, web, sites
}

// addUser adds a user with a sign-in (password "<name>-password") straight
// to the store, without an actor.
func addUser(t *testing.T, svc *Service, name, role string) store.User {
	t.Helper()
	ctx := context.Background()
	au, err := svc.auth.CreateUser(ctx, sesam.NewUser{Username: name})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.pw.SetPasswordHash(ctx, au.ID, userHash(t, name+"-password")); err != nil {
		t.Fatal(err)
	}
	u, err := svc.store.CreateUser(ctx, name, au.ID, role, false)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// userHashes keeps one hash per password for the whole test run: hashing
// is slow on purpose, and the permission matrices build hundreds of
// services. The hash has the format the panel used before sesam, which
// sesam accepts.
var userHashes sync.Map

func userHash(t *testing.T, pw string) string {
	t.Helper()
	if h, ok := userHashes.Load(pw); ok {
		return h.(string)
	}
	h, err := mailboxHashSlots.Hash(pw)
	if err != nil {
		t.Fatal(err)
	}
	userHashes.Store(pw, h)
	return h
}

// newEmptyService has no users yet, like a fresh install.
func newEmptyService(t *testing.T) (*Service, *fakeWeb, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sites := filepath.Join(dir, "sites")
	web := &fakeWeb{}
	svc, err := New(Config{
		Store:   st,
		Deploys: &deploy.Manager{Base: sites},
		Web:     web,
		Caddy: caddy.Options{
			AdminListen: "127.0.0.1:2019",
			StorageDir:  filepath.Join(dir, "caddy"),
			TLSMode:     caddy.TLSOff,
			HTTPPort:    8080,
			HTTPSPort:   8443,
		},
		SetupCodeFile: filepath.Join(dir, "setup-code"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, web, sites
}

func tarGz(t *testing.T, files map[string]string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

func wantKind(t *testing.T, err error, want Kind) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %v error, got nil", want)
	}
	if got := KindOf(err); got != want {
		t.Fatalf("kind = %v, want %v (err: %v)", got, want, err)
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	_, err := New(Config{Web: &fakeWeb{}, Caddy: caddy.Options{TLSMode: "bogus"}})
	if err == nil {
		t.Fatal("want error for unknown TLS mode")
	}
}

func TestSaveSiteSyncsAndNormalizes(t *testing.T) {
	svc, web, _ := newService(t)
	ctx := context.Background()

	site, err := svc.SaveSite(ctx, admin, "blog", []string{"Blog.Example.com", "blog.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if site.Name != "blog" || len(site.Domains) != 1 || site.Domains[0] != "blog.example.com" {
		t.Fatalf("site = %+v", site)
	}
	if site.Current != "" || len(site.Releases) != 0 {
		t.Fatalf("new site has releases: %+v", site)
	}
	if web.count() != 1 || !strings.Contains(web.last(), "blog.example.com") {
		t.Fatalf("web server not synced with the domain: %d %s", web.count(), web.last())
	}
	if got, err := svc.Site(ctx, admin, "blog"); err != nil || got.Name != "blog" {
		t.Fatalf("Site = %+v, %v", got, err)
	}
	if got := mustSites(t, svc); len(got) != 1 {
		t.Fatalf("Sites = %+v", got)
	}
}

func TestSitesEmptyIsNotNil(t *testing.T) {
	svc, _, _ := newService(t)
	if got := mustSites(t, svc); got == nil || len(got) != 0 {
		t.Fatalf("Sites = %#v", got)
	}
}

func TestPublishSyncsOnlyTheFirstTime(t *testing.T) {
	svc, web, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	before := web.count()

	site, err := svc.Publish(ctx, admin, "blog", tarGz(t, map[string]string{"index.html": "v1"}))
	if err != nil {
		t.Fatal(err)
	}
	if site.Current == "" || len(site.Releases) != 1 {
		t.Fatalf("site after publish = %+v", site)
	}
	if web.count() != before+1 || !strings.Contains(web.last(), "file_server") {
		t.Fatalf("first publish must sync a file serving config (calls %d -> %d)", before, web.count())
	}

	second, err := svc.Publish(ctx, admin, "blog", tarGz(t, map[string]string{"index.html": "v2"}))
	if err != nil {
		t.Fatal(err)
	}
	if web.count() != before+1 {
		t.Fatalf("second publish must not sync (calls %d)", web.count())
	}
	if second.Current == site.Current || len(second.Releases) != 2 {
		t.Fatalf("second = %+v first = %+v", second, site)
	}
}

func TestMakeLive(t *testing.T) {
	svc, web, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Publish(ctx, admin, "blog", tarGz(t, map[string]string{"index.html": "v1"}))
	if err != nil {
		t.Fatal(err)
	}
	// Nothing earlier than the only release.
	_, err = svc.MakeLive(ctx, admin, "blog", "")
	wantKind(t, err, Conflict)
	if !strings.Contains(err.Error(), "no earlier release") {
		t.Fatalf("message lost: %v", err)
	}

	// Release IDs sort by millisecond timestamp, so keep them apart.
	time.Sleep(2 * time.Millisecond)
	v2, err := svc.Publish(ctx, admin, "blog", tarGz(t, map[string]string{"index.html": "v2"}))
	if err != nil {
		t.Fatal(err)
	}
	calls := web.count()
	back, err := svc.MakeLive(ctx, admin, "blog", "")
	if err != nil {
		t.Fatal(err)
	}
	if back.Current != v1.Current {
		t.Fatalf("current = %q, want %q", back.Current, v1.Current)
	}
	fwd, err := svc.MakeLive(ctx, admin, "blog", v2.Current)
	if err != nil || fwd.Current != v2.Current {
		t.Fatalf("explicit release: %+v, %v", fwd, err)
	}
	if web.count() != calls {
		t.Fatal("MakeLive must not sync")
	}

	_, err = svc.MakeLive(ctx, admin, "blog", "nope")
	wantKind(t, err, Conflict)
}

func TestDeleteSiteRemovesFiles(t *testing.T) {
	svc, web, sites := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Publish(ctx, admin, "blog", tarGz(t, map[string]string{"index.html": "v1"})); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSite(ctx, admin, "blog"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sites, "blog")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("site files still there: %v", err)
	}
	if strings.Contains(web.last(), "blog.example.com") {
		t.Fatal("web server still configured for the deleted site")
	}
	_, err := svc.Site(ctx, admin, "blog")
	wantKind(t, err, NotFound)
}

func TestDeleteSiteKeepsFilesWhenNotSynced(t *testing.T) {
	svc, web, sites := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Publish(ctx, admin, "blog", tarGz(t, map[string]string{"index.html": "v1"})); err != nil {
		t.Fatal(err)
	}
	web.setFail(errors.New("down"))
	wantKind(t, svc.DeleteSite(ctx, admin, "blog"), NotSynced)
	if _, err := os.Stat(filepath.Join(sites, "blog")); err != nil {
		t.Fatalf("files must stay when the web server was not updated: %v", err)
	}
}

func TestErrorKinds(t *testing.T) {
	svc, web, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}

	t.Run("unknown site", func(t *testing.T) {
		_, err := svc.Site(ctx, admin, "ghost")
		wantKind(t, err, NotFound)
		_, err = svc.Publish(ctx, admin, "ghost", tarGz(t, map[string]string{"a": "b"}))
		wantKind(t, err, NotFound)
		_, err = svc.MakeLive(ctx, admin, "ghost", "")
		wantKind(t, err, NotFound)
		wantKind(t, svc.DeleteSite(ctx, admin, "ghost"), NotFound)
	})
	t.Run("bad name", func(t *testing.T) {
		_, err := svc.SaveSite(ctx, admin, "Bad Name!", []string{"a.example.com"})
		wantKind(t, err, Invalid)
		if !strings.Contains(err.Error(), "invalid site name") {
			t.Fatalf("message lost: %v", err)
		}
	})
	t.Run("bad domain", func(t *testing.T) {
		_, err := svc.SaveSite(ctx, admin, "other", []string{"nodots"})
		wantKind(t, err, Invalid)
	})
	t.Run("duplicate domain across sites", func(t *testing.T) {
		_, err := svc.SaveSite(ctx, admin, "other", []string{"blog.example.com"})
		wantKind(t, err, Invalid)
		if !strings.Contains(err.Error(), "already used by site") {
			t.Fatalf("message lost: %v", err)
		}
	})
	t.Run("not an archive", func(t *testing.T) {
		_, err := svc.Publish(ctx, admin, "blog", strings.NewReader("just text"))
		wantKind(t, err, Invalid)
		if !errors.Is(err, deploy.ErrBadArchive) {
			t.Fatalf("cause not reachable: %v", err)
		}
	})
	t.Run("empty archive", func(t *testing.T) {
		_, err := svc.Publish(ctx, admin, "blog", tarGz(t, nil))
		wantKind(t, err, Invalid)
	})
	t.Run("unsafe entry", func(t *testing.T) {
		_, err := svc.Publish(ctx, admin, "blog", tarGz(t, map[string]string{"../evil": "x"}))
		wantKind(t, err, Invalid)
	})
	t.Run("storage failure is internal", func(t *testing.T) {
		// A file where the site directory should be makes Deploy fail on disk.
		svc2, _, sites := newService(t)
		if _, err := svc2.SaveSite(ctx, admin, "blog", []string{"blog.example.com"}); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(sites, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sites, "blog"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := svc2.Publish(ctx, admin, "blog", tarGz(t, map[string]string{"index.html": "x"}))
		wantKind(t, err, Internal)
	})
	t.Run("web server down", func(t *testing.T) {
		web.setFail(errors.New("connection refused"))
		defer web.setFail(nil)
		site, err := svc.SaveSite(ctx, admin, "news", []string{"news.example.com"})
		wantKind(t, err, NotSynced)
		if !strings.Contains(err.Error(), "saved, but the web server could not be updated yet: connection refused") {
			t.Fatalf("message = %q", err)
		}
		if site.Name != "news" {
			t.Fatalf("saved site must be returned: %+v", site)
		}
		found := false
		for _, s := range mustSites(t, svc) {
			found = found || s.Name == "news"
		}
		if !found {
			t.Fatal("site not saved although only the sync failed")
		}
	})
	t.Run("plain errors are internal", func(t *testing.T) {
		if got := KindOf(errors.New("boom")); got != Internal {
			t.Fatalf("kind = %v", got)
		}
	})
}

func TestPublishNotSyncedReturnsSite(t *testing.T) {
	svc, web, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	web.setFail(errors.New("down"))
	site, err := svc.Publish(ctx, admin, "blog", tarGz(t, map[string]string{"index.html": "v1"}))
	wantKind(t, err, NotSynced)
	if site.Current == "" {
		t.Fatalf("release is live on disk, site should say so: %+v", site)
	}
}

func TestRunSyncsAtStartAndStopsOnCancel(t *testing.T) {
	svc, web, _ := newService(t)
	web.calls = make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.Run(ctx, time.Hour)
		close(done)
	}()
	select {
	case <-web.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("no initial sync")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestRunRetriesQuicklyWhileUnhealthy(t *testing.T) {
	svc, web, _ := newService(t)
	svc.unhealthyRetry = time.Millisecond
	web.calls = make(chan struct{}, 64)
	web.setFail(errors.New("down"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.Run(ctx, time.Hour)
		close(done)
	}()
	// Two syncs within the test despite the hour-long interval.
	for i := 0; i < 2; i++ {
		select {
		case <-web.calls:
		case <-time.After(5 * time.Second):
			t.Fatal("no retry while unhealthy")
		}
	}
	web.setFail(nil)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

// syncLog captures the log lines of Run for one test.
type syncLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func captureLog(t *testing.T) *syncLog {
	t.Helper()
	l := &syncLog{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(l, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return l
}

func TestRunWaitsQuietlyForWebServerAtStartup(t *testing.T) {
	logs := captureLog(t)
	svc, web, _ := newService(t)
	svc.startupGrace = time.Minute
	svc.startupRetry = time.Millisecond
	web.calls = make(chan struct{}, 64)
	web.setFail(errors.New("dial unix caddy-admin.sock: no such file or directory"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.Run(ctx, time.Hour)
		close(done)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-web.calls:
		case <-time.After(5 * time.Second):
			t.Fatal("no quick retry at startup")
		}
	}
	web.setFail(nil)
	// Once a sync succeeds, the next one is an hour away: the calls stop.
	for quiet := false; !quiet; {
		select {
		case <-web.calls:
		case <-time.After(200 * time.Millisecond):
			quiet = true
		}
	}
	cancel()
	<-done
	if out := logs.String(); strings.Contains(out, "not in sync") || strings.Contains(out, "back in sync") {
		t.Fatalf("startup wait was not quiet:\n%s", out)
	}
}

func TestRunWarnsWhenWebServerStaysDown(t *testing.T) {
	logs := captureLog(t)
	svc, web, _ := newService(t)
	svc.startupGrace = 0
	svc.unhealthyRetry = time.Millisecond
	web.calls = make(chan struct{}, 64)
	web.setFail(errors.New("down"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.Run(ctx, time.Hour)
		close(done)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-web.calls:
		case <-time.After(5 * time.Second):
			t.Fatal("no retry while unhealthy")
		}
	}
	cancel()
	<-done
	if out := logs.String(); !strings.Contains(out, "web server not in sync") {
		t.Fatalf("no warning once the startup wait is over:\n%s", out)
	}
}

func mustSites(t *testing.T, svc *Service) []Site {
	t.Helper()
	list, err := svc.Sites(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	return list
}
