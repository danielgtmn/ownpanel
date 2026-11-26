package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// fakeRuntime runs a build by calling run (default: copy the source to the
// output folder named in the command "out <dir>").
type fakeRuntime struct {
	mu      sync.Mutex
	run     func(ctx context.Context, r Run) (Result, error)
	runs    []Run
	running atomic.Int32
	maxSeen atomic.Int32
	cleaned int
}

func (f *fakeRuntime) Check(context.Context) (RuntimeInfo, error) {
	return RuntimeInfo{Name: "fake 1.0", CPUs: 8, MemoryBytes: 16 << 30}, nil
}
func (f *fakeRuntime) Cleanup(context.Context) error {
	f.mu.Lock()
	f.cleaned++
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) Images(_ context.Context, list []build.Image) ([]build.ImageStatus, error) {
	var out []build.ImageStatus
	for _, img := range list {
		out = append(out, build.ImageStatus{Image: img, Present: true})
	}
	return out, nil
}
func (f *fakeRuntime) Run(ctx context.Context, r Run) (Result, error) {
	n := f.running.Add(1)
	defer f.running.Add(-1)
	for {
		m := f.maxSeen.Load()
		if n <= m || f.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	f.mu.Lock()
	f.runs = append(f.runs, r)
	run := f.run
	f.mu.Unlock()
	if run != nil {
		return run(ctx, r)
	}
	return Result{}, nil
}
func (f *fakeRuntime) RemoveTree(_ context.Context, dir string) error { return removeTree(dir) }

type fakeDisk struct {
	mu       sync.Mutex
	projects map[string]uint32
	ws       int64
	free     int64
}

func (d *fakeDisk) Check(string) error { return nil }
func (d *fakeDisk) SetProject(dir string, id uint32) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.projects[dir] = id
	return nil
}
func (d *fakeDisk) Project(dir string) (uint32, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.projects[dir], nil
}
func (d *fakeDisk) Usage(dir string) (Usage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if strings.Contains(dir, "/slots/") {
		return Usage{TotalBytes: d.ws, FreeBytes: d.ws, Files: 1000, FreeFiles: 1000}, nil
	}
	return Usage{TotalBytes: 100 << 30, FreeBytes: d.free, Files: 1 << 20, FreeFiles: 1 << 20}, nil
}

type fakeGuard struct {
	mu  sync.Mutex
	err error
}

func (g *fakeGuard) set(err error) {
	g.mu.Lock()
	g.err = err
	g.mu.Unlock()
}

func (g *fakeGuard) Protected() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

type env struct {
	w     *Worker
	rt    *fakeRuntime
	disk  *fakeDisk
	guard *fakeGuard
	cfg   Config
}

func newEnv(t *testing.T, slots int) *env {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	os.Mkdir(work, 0o700)
	res := filepath.Join(dir, "resolvers")
	os.WriteFile(res, []byte("192.0.2.53\n127.0.0.53\n"), 0o644)
	e := &env{rt: &fakeRuntime{}, disk: &fakeDisk{projects: map[string]uint32{}, ws: 1 << 30, free: 50 << 30}, guard: &fakeGuard{}}
	e.cfg = Config{
		Work: work, Resolvers: res, Slots: slots, WorkspaceBytes: 1 << 30, CacheBytes: 256 << 20, Caches: 3,
		Runtime: e.rt, Disk: e.disk, Guard: e.guard, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		WatchEvery: 20 * time.Millisecond,
	}
	w, err := New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.w = w
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, func() bool { return w.Status().Available })
	return e
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func srcTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(content))})
		tw.Write([]byte(content))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

var specID atomic.Uint64

func spec(site string) build.Spec {
	return build.Spec{ID: specID.Add(1), Site: site, Image: "node-22", Command: "npm run build", Output: "."}
}

func (e *env) start(t *testing.T, s build.Spec) build.Build {
	t.Helper()
	if _, err := e.w.Create(s); err != nil {
		t.Fatal(err)
	}
	if _, err := e.w.Source(context.Background(), s.ID, bytes.NewReader(srcTar(t, map[string]string{"index.html": "hi"})), 0); err != nil {
		t.Fatal(err)
	}
	b, err := e.w.Start(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *env) wait(t *testing.T, id uint64) build.Build {
	t.Helper()
	var b build.Build
	waitFor(t, func() bool {
		b, _ = e.w.Get(id)
		return build.Finished(b.State)
	})
	return b
}

func TestBuildSucceedsAndPacksOutput(t *testing.T) {
	e := newEnv(t, 1)
	e.rt.run = func(_ context.Context, r Run) (Result, error) {
		// What a build sees: its files, the variables and the command in
		// the control folder, the resolvers without loopback.
		envSh, _ := os.ReadFile(filepath.Join(r.Control, controlEnv))
		if !strings.Contains(string(envSh), "export TOKEN='abc'\\''def'") {
			t.Errorf("env.sh: %s", envSh)
		}
		if len(r.Resolvers) != 1 || r.Resolvers[0].String() != "192.0.2.53" {
			t.Errorf("resolvers %v", r.Resolvers)
		}
		os.MkdirAll(filepath.Join(r.Workspace, "dist"), 0o755)
		os.WriteFile(filepath.Join(r.Workspace, "dist", "index.html"), []byte("built"), 0o644)
		io.WriteString(r.Output, "token abc'def done\n")
		return Result{}, nil
	}
	s := spec("site-a")
	s.Output = "dist"
	s.Secrets = map[string]string{"TOKEN": "abc'def"}
	e.start(t, s)
	b := e.wait(t, s.ID)
	if b.State != build.StateSucceeded || b.OutputFiles != 1 {
		t.Fatalf("%+v", b)
	}
	var log bytes.Buffer
	if err := e.w.Log(context.Background(), s.ID, 0, false, &log, func() {}); err != nil {
		t.Fatal(err)
	}
	if log.String() != "token *** done\n" {
		t.Errorf("log %q", log.String())
	}
	f, err := e.w.Output(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, _ := gzip.NewReader(f)
	h, err := tar.NewReader(gz).Next()
	if err != nil || h.Name != "index.html" {
		t.Errorf("output: %v %v", h, err)
	}
	// The slot is empty again, the source gone, the secrets forgotten.
	entries, _ := os.ReadDir(e.w.slotDir(0))
	if len(entries) != 0 {
		t.Errorf("slot not emptied: %v", entries)
	}
	if _, err := os.Stat(e.w.sourcePath(s.ID)); err == nil {
		t.Errorf("source kept")
	}
	if e.w.builds[s.ID].spec.Secrets != nil {
		t.Errorf("secrets kept in memory")
	}
}

func TestParallelLimitAndOnePerSite(t *testing.T) {
	e := newEnv(t, 2)
	release := make(chan struct{})
	var mu sync.Mutex
	sites := map[string]int{}
	maxPerSite := 0
	e.rt.run = func(ctx context.Context, r Run) (Result, error) {
		site := filepath.Base(r.Cache)
		mu.Lock()
		sites[site]++
		maxPerSite = max(maxPerSite, sites[site])
		mu.Unlock()
		<-release
		mu.Lock()
		sites[site]--
		mu.Unlock()
		return Result{}, nil
	}
	var ids []uint64
	for _, site := range []string{"a", "a", "b", "c", "a"} {
		s := spec(site)
		e.start(t, s)
		ids = append(ids, s.ID)
	}
	waitFor(t, func() bool { return e.rt.running.Load() == 2 })
	st := e.w.Status()
	if st.Running != 2 || st.Waiting != 3 {
		t.Errorf("running %d waiting %d", st.Running, st.Waiting)
	}
	close(release)
	for _, id := range ids {
		if b := e.wait(t, id); b.State != build.StateSucceeded {
			t.Errorf("%d: %s %s", id, b.State, b.Message)
		}
	}
	if e.rt.maxSeen.Load() > 2 {
		t.Errorf("%d builds ran at once", e.rt.maxSeen.Load())
	}
	if maxPerSite > 1 {
		t.Errorf("two builds of one site ran at once")
	}
}

func TestTimeoutCancelAndUnprotected(t *testing.T) {
	e := newEnv(t, 2)
	e.rt.run = func(ctx context.Context, r Run) (Result, error) {
		<-ctx.Done()
		return Result{Killed: true, ExitCode: 137}, nil
	}
	s := spec("slow")
	s.Limits.Seconds = 1
	e.start(t, s)
	if b := e.wait(t, s.ID); b.Reason != build.ReasonTimeout {
		t.Errorf("timeout: %+v", b)
	}

	s = spec("cancelled")
	e.start(t, s)
	waitFor(t, func() bool { b, _ := e.w.Get(s.ID); return b.State == build.StateRunning })
	e.w.Cancel(s.ID)
	if b := e.wait(t, s.ID); b.State != build.StateCancelled {
		t.Errorf("cancel: %+v", b)
	}

	s = spec("unprotected")
	e.start(t, s)
	waitFor(t, func() bool { b, _ := e.w.Get(s.ID); return b.State == build.StateRunning })
	e.guard.set(errors.New("filter gone"))
	if b := e.wait(t, s.ID); b.Reason != build.ReasonUnprotected {
		t.Errorf("unprotected: %+v", b)
	}
	if st := e.w.Status(); st.Available || st.Reason != build.UnavailableNetwork {
		t.Errorf("status %+v", st)
	}
	if _, err := e.w.Create(spec("next")); err == nil {
		t.Errorf("a build was accepted without the filter")
	}
	// Back: available again, and the finished builds are still there.
	e.guard.set(nil)
	if !e.w.recoverForTest() {
		t.Errorf("builds did not become available again")
	}
	if b, err := e.w.Get(s.ID); err != nil || b.Reason != build.ReasonUnprotected {
		t.Errorf("a known build is gone after recovering: %v", err)
	}
}

func TestFailureReasons(t *testing.T) {
	e := newEnv(t, 1)
	cases := []struct {
		res    Result
		reason string
	}{
		{Result{ExitCode: 2}, build.ReasonExit},
		{Result{ExitCode: 137, OutOfMemory: true}, build.ReasonMemory},
	}
	for _, c := range cases {
		e.rt.run = func(context.Context, Run) (Result, error) { return c.res, nil }
		s := spec("fail")
		e.start(t, s)
		if b := e.wait(t, s.ID); b.Reason != c.reason {
			t.Errorf("want %s, got %+v", c.reason, b)
		}
	}
	// A link in the output is refused.
	e.rt.run = func(_ context.Context, r Run) (Result, error) {
		os.Symlink("/etc/passwd", filepath.Join(r.Workspace, "passwd"))
		return Result{}, nil
	}
	s := spec("link")
	e.start(t, s)
	if b := e.wait(t, s.ID); b.Reason != build.ReasonOutput {
		t.Errorf("link: %+v", b)
	}
	if _, err := e.w.Output(s.ID); err == nil {
		t.Errorf("output of a failed build")
	}
}

func TestCachesPerSiteAndEviction(t *testing.T) {
	e := newEnv(t, 1) // 3 cache projects
	var caches []string
	e.rt.run = func(_ context.Context, r Run) (Result, error) {
		caches = append(caches, r.Cache)
		os.WriteFile(filepath.Join(r.Cache, "mark"), []byte(filepath.Base(r.Cache)), 0o644)
		return Result{}, nil
	}
	for _, site := range []string{"a", "b", "c", "a", "d"} {
		s := spec(site)
		e.start(t, s)
		e.wait(t, s.ID)
		time.Sleep(10 * time.Millisecond) // distinct use times
	}
	if caches[0] != caches[3] {
		t.Errorf("site a got another cache the second time")
	}
	list, _ := e.w.Caches()
	var names []string
	projects := map[uint32]bool{}
	for _, c := range list {
		names = append(names, c.Site)
		p, _ := e.disk.Project(e.w.path(dirCaches, c.Site))
		if p < projectCacheBase || projects[p] {
			t.Errorf("cache %s has project %d", c.Site, p)
		}
		projects[p] = true
	}
	// b was used longest ago and went for d.
	if strings.Join(names, ",") != "a,c,d" {
		t.Errorf("caches %v", names)
	}
	if err := e.w.ClearCache(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.w.path(dirCaches, "a")); err == nil {
		t.Errorf("cache a not cleared")
	}
	if err := e.w.ClearCache(context.Background(), "../x"); err == nil {
		t.Errorf("invalid site accepted")
	}
}

func TestRejectsBadRequests(t *testing.T) {
	e := newEnv(t, 1)
	bad := []build.Spec{
		{ID: 0, Site: "a", Image: "node-22", Command: "x", Output: "."},
		{ID: 1, Site: "A_b", Image: "node-22", Command: "x", Output: "."},
		{ID: 1, Site: "a", Image: "docker.io/library/alpine", Command: "x", Output: "."},
		{ID: 1, Site: "a", Image: "nope", Command: "x", Output: "."},
		{ID: 1, Site: "a", Image: "node-22", Command: " ", Output: "."},
		{ID: 1, Site: "a", Image: "node-22", Command: "x", Output: "../x"},
		{ID: 1, Site: "a", Image: "node-22", Command: "x", Output: "/etc"},
		{ID: 1, Site: "a", Image: "node-22", Command: "x", Output: ".", Env: map[string]string{"LD_PRELOAD": "x"}},
		{ID: 1, Site: "a", Image: "node-22", Command: "x", Output: ".", Env: map[string]string{"HOME": "/"}},
		{ID: 1, Site: "a", Image: "node-22", Command: "x", Output: ".", Env: map[string]string{"A B": "x"}},
		{ID: 1, Site: "a", Image: "node-22", Command: "x", Output: ".", Limits: build.Limits{MemoryBytes: -1}},
	}
	for i, s := range bad {
		if _, err := e.w.Create(s); err == nil {
			t.Errorf("%d: accepted", i)
		}
	}
	// Limits above the maxima are lowered.
	s := spec("a")
	s.Limits = build.Limits{MemoryBytes: 1 << 40, Seconds: 1 << 20}
	b, err := e.w.Create(s)
	if err != nil {
		t.Fatal(err)
	}
	if b.Limits.MemoryBytes != build.MaxLimits.MemoryBytes || b.Limits.Seconds != build.MaxLimits.Seconds {
		t.Errorf("limits %+v", b.Limits)
	}
	// Starting needs a source; a bad source is refused when it arrives.
	if _, err := e.w.Start(s.ID); err == nil {
		t.Errorf("started without source")
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "../evil", Size: 1, Mode: 0o644})
	tw.Write([]byte("x"))
	tw.Close()
	if _, err := e.w.Source(context.Background(), s.ID, &buf, 0); err == nil {
		t.Errorf("source with ../ accepted")
	}
	if _, err := os.Stat(e.w.sourcePath(s.ID)); err == nil {
		t.Errorf("refused source kept")
	}
}

func TestMasker(t *testing.T) {
	var out bytes.Buffer
	m := newMasker(&out, map[string]string{"A": "secret-one", "B": "secret-one-longer", "C": "abc"})
	// Split at every position, including inside a value.
	in := "x secret-one y secret-one-longer z secret-on abc"
	for _, b := range []byte(in) {
		m.Write([]byte{b})
	}
	m.Flush()
	if want := "x *** y *** z secret-on abc"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestEnvScriptQuoting(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	values := map[string]string{
		"Q": `it's "quoted" $HOME ` + "`id`" + ` \n`,
		"N": "line1\nline2",
		"E": "",
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "env.sh"), envScript(build.Spec{Env: values}), 0o600)
	for name, want := range values {
		out, err := exec.Command("sh", "-c", `. "$1/env.sh" && printf %s "$`+name+`"`, "sh", dir).Output()
		if err != nil || string(out) != want {
			t.Errorf("%s: got %q (%v), want %q", name, out, err, want)
		}
	}
}

func TestLogLimitAndFollow(t *testing.T) {
	e := newEnv(t, 1)
	step := make(chan struct{})
	e.rt.run = func(_ context.Context, r Run) (Result, error) {
		io.WriteString(r.Output, "first\n")
		<-step
		r.Output.Write(bytes.Repeat([]byte("x"), 4096))
		return Result{}, nil
	}
	s := spec("log")
	s.Limits.LogBytes = 1024
	e.start(t, s)
	var mu sync.Mutex
	var got bytes.Buffer
	done := make(chan error)
	go func() {
		done <- e.w.Log(context.Background(), s.ID, 0, true, writerFunc(func(p []byte) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			return got.Write(p)
		}), func() {})
	}()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return got.Len() > 0 })
	close(step)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	b := e.wait(t, s.ID)
	if !b.LogTruncated || !strings.HasPrefix(got.String(), "first\n") || !strings.Contains(got.String(), "cut off") {
		t.Errorf("log %q (%+v)", got.String(), b)
	}
	if int64(got.Len()) > 1024+int64(len(truncatedNote)) {
		t.Errorf("log is %d bytes", got.Len())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// The socket speaks the protocol the client expects.
func TestSocketWithClient(t *testing.T) {
	e := newEnv(t, 1)
	e.rt.run = func(_ context.Context, r Run) (Result, error) {
		io.WriteString(r.Output, "built\n")
		return Result{}, nil
	}
	dir, _ := os.MkdirTemp("", "bw")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "w.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &httpServer{ln: ln, h: e.w.Handler()}
	go srv.serve()
	defer ln.Close()
	if fi, _ := os.Stat(sock); fi.Mode().Perm() != 0o660 {
		t.Errorf("socket mode %v", fi.Mode().Perm())
	}
	c := build.NewClient(sock)
	ctx := context.Background()
	st, err := c.Status(ctx)
	if err != nil || !st.Available || st.Slots != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	s := spec("client")
	if _, err := c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(ctx, s); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := c.Source(ctx, s.ID, bytes.NewReader(srcTar(t, map[string]string{"top/index.html": "x"})), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	rc, err := c.Log(ctx, s.ID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	logText, _ := io.ReadAll(rc)
	rc.Close()
	if string(logText) != "built\n" {
		t.Errorf("log %q", logText)
	}
	out, err := c.Output(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := gzip.NewReader(out)
	h, _ := tar.NewReader(gz).Next()
	out.Close()
	if h == nil || h.Name != "index.html" {
		t.Errorf("strip did not apply: %+v", h)
	}
	if err := c.Remove(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, s.ID); !build.IsNotFound(err) {
		t.Errorf("after remove: %v", err)
	}
	// Unknown fields are refused, and nothing of the request is echoed.
	_, err = c.Create(ctx, build.Spec{ID: 9, Site: "x", Image: "nope", Command: "SECRET-IN-COMMAND", Output: "."})
	if err == nil || strings.Contains(err.Error(), "SECRET-IN-COMMAND") {
		t.Errorf("error %v", err)
	}
}

func TestRemovesStaleSocketOnly(t *testing.T) {
	dir, _ := os.MkdirTemp("", "bw")
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "f")
	os.WriteFile(p, nil, 0o600)
	if _, err := Listen(p); err == nil {
		t.Errorf("a file was replaced by the socket")
	}
	ln, _ := net.Listen("unix", filepath.Join(dir, "s"))
	defer ln.Close()
	if _, err := Listen(filepath.Join(dir, "s")); err == nil {
		t.Errorf("a socket in use was replaced")
	}
}

type httpServer struct {
	ln net.Listener
	h  http.Handler
}

func (s *httpServer) serve() { _ = http.Serve(s.ln, s.h) }
