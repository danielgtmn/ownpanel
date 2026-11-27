// Package worker is the build worker: `ownpaneld build-worker`, run by
// ownpanel-build.service as the system user ownpanel-build. It takes
// requests from the panel on a Unix socket (server.go), keeps the builds
// and their queue (this file), runs each build in a container of its own
// (run.go, podman_linux.go) and keeps every file of a build on the build
// disk, where the kernel enforces its size (disk_linux.go).
//
// The build disk is a file system of its own, set up by root
// (deploy/ownpanel-build-setup) and given to this user, with a project
// quota per area:
//
//	images/    project 2      the container images
//	results/   project 3      logs and outputs, until the panel fetched them
//	sources/   project 4      source trees of builds that wait for a slot
//	slots/N/   project 100+N  workspace and control files of the build in slot N
//	caches/S/  project 1000+n the cache of site S
//
// A directory gets its project when the worker creates it; everything below
// inherits it, and nothing inside a container can change it. The numbers
// must match the quotas the setup script sets.
package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// Project numbers on the build disk (see the package comment).
const (
	projectImages    = 2
	projectResults   = 3
	projectSources   = 4
	projectSlotBase  = 100
	projectCacheBase = 1000
	// MaxSlots and MaxCaches bound the configuration; the setup script
	// sets quotas for at most as many.
	MaxSlots  = 16
	MaxCaches = 1000
)

// Areas of the build disk.
const (
	dirImages  = "images"
	dirResults = "results"
	dirSources = "sources"
	dirSlots   = "slots"
	dirCaches  = "caches"
)

// Disk is the build disk. The real one checks the mount and the quotas and
// sets projects (disk_linux.go); tests use a fake.
type Disk interface {
	// Check returns nil when dir is the build disk: its own file system,
	// mounted without set-user-ID programs and devices, with project
	// quotas.
	Check(dir string) error
	// SetProject gives the empty folder dir the project id; everything
	// created below it inherits the project.
	SetProject(dir string, id uint32) error
	// Project returns the project of dir.
	Project(dir string) (uint32, error)
	// Usage returns the space and the files of the project of dir (or of
	// the whole disk for a folder without a quota of its own).
	Usage(dir string) (Usage, error)
}

// Usage is what statfs reports for a folder.
type Usage struct {
	TotalBytes, FreeBytes int64
	Files, FreeFiles      int64
}

// UsedBytes is TotalBytes minus FreeBytes.
func (u Usage) UsedBytes() int64 { return u.TotalBytes - u.FreeBytes }

// Guard tells whether the network limits for builds are in place.
type Guard interface {
	Protected() error
}

// Config is how the worker is set up.
type Config struct {
	// Work is the build disk's mount point.
	Work string
	// Resolvers is the file the setup script writes the name servers of
	// builds to (one address per line).
	Resolvers string
	// Slots is how many builds run at once.
	Slots int
	// WorkspaceBytes and CacheBytes are the quotas the setup script sets
	// for a slot and a cache; Caches is how many cache projects it made.
	WorkspaceBytes int64
	CacheBytes     int64
	Caches         int

	Runtime Runtime
	Disk    Disk
	Guard   Guard
	Images  []build.Image
	Logger  *slog.Logger
	// Now is time.Now unless a test sets it.
	Now func() time.Time
	// WatchEvery is how often a running build checks that the limits are
	// still in place (default 5 s).
	WatchEvery time.Duration
}

// Validate checks the configuration.
func (c Config) Validate() error {
	switch {
	case !filepath.IsAbs(c.Work) || filepath.Clean(c.Work) != c.Work || strings.ContainsAny(c.Work, ":,\n"):
		return errors.New("the work directory must be a plain absolute path")
	case c.Slots < 1 || c.Slots > MaxSlots:
		return fmt.Errorf("the number of slots must be between 1 and %d", MaxSlots)
	case c.WorkspaceBytes < 64<<20:
		return errors.New("the workspace limit must be at least 64 MB")
	case c.CacheBytes < 16<<20:
		return errors.New("the cache limit must be at least 16 MB")
	case c.Caches < 1 || c.Caches > MaxCaches:
		return fmt.Errorf("the number of caches must be between 1 and %d", MaxCaches)
	case c.Runtime == nil || c.Disk == nil || c.Guard == nil:
		return errors.New("runtime, disk and guard are needed")
	}
	for _, img := range c.Images {
		if !build.ValidImage(img) {
			return fmt.Errorf("the image %q is not valid", img.Name)
		}
	}
	return nil
}

// Worker keeps the builds.
type Worker struct {
	cfg Config
	log *slog.Logger

	mu        sync.Mutex
	builds    map[uint64]*job
	queue     []*job // started, waiting for a slot, oldest first
	slots     []*job // nil: free
	ready     bool
	down      *Unavailable // why builds cannot run now; nil when ready
	info      RuntimeInfo
	images    []build.ImageStatus
	defaults  build.Limits
	maxima    build.Limits
	clearing  map[string]bool // sites whose cache is being deleted
	broken    string          // why builds stay off until a restart (a slot could not be emptied)
	preparing atomic.Bool
	prepared  atomic.Bool
	wake      chan struct{}
	closing   bool
	running   sync.WaitGroup
}

// job is one build.
type job struct {
	spec   build.Spec // Secrets are dropped once the build has run
	limits build.Limits
	state  string
	strip  int

	hasSource bool
	receiving bool // the source tree is arriving
	reason    string
	message   string
	exitCode  int

	created, started, finished time.Time

	slot      int
	cancel    context.CancelFunc
	cancelled bool
	why       string // why it was killed: a build.Reason*
	log       *logFile
	logSize   int64
	logCut    bool
	outBytes  int64
	outFiles  int
	done      chan struct{}
}

// New returns a worker; Prepare makes it ready.
func New(cfg Config) (*Worker, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.WatchEvery == 0 {
		cfg.WatchEvery = 5 * time.Second
	}
	if cfg.Images == nil {
		cfg.Images = build.Images
	}
	return &Worker{
		cfg:      cfg,
		log:      cfg.Logger,
		builds:   map[uint64]*job{},
		slots:    make([]*job, cfg.Slots),
		clearing: map[string]bool{},
		down:     unavailable(build.UnavailablePreparing, "the build service is starting"),
		defaults: build.DefaultLimits,
		maxima:   build.MaxLimits,
		wake:     make(chan struct{}, 1),
	}, nil
}

func (w *Worker) path(parts ...string) string {
	return filepath.Join(append([]string{w.cfg.Work}, parts...)...)
}

func (w *Worker) slotDir(n int) string { return w.path(dirSlots, fmt.Sprint(n)) }

// Run prepares the worker, then schedules builds and removes expired ones
// until ctx ends; then it stops every build and waits for them.
func (w *Worker) Run(ctx context.Context) {
	go w.prepareLoop(ctx)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			w.shutdown()
			return
		case <-w.wake:
		case <-tick.C:
			w.expire()
			w.recheck(ctx)
		}
		w.schedule()
	}
}

// prepareLoop runs prepare (the first time) or recover (after the limits
// went away while the worker ran) until builds are available again. It
// waits between tries; an administrator's fix of the system or a missing
// runtime is noticed within minutes. Only one runs at a time.
func (w *Worker) prepareLoop(ctx context.Context) {
	if !w.preparing.CompareAndSwap(false, true) {
		return
	}
	defer w.preparing.Store(false)
	for {
		var err error
		if w.prepared.Load() {
			err = w.recover()
		} else if err = w.prepare(ctx); err == nil {
			w.prepared.Store(true)
		}
		if err == nil {
			w.poke()
			return
		}
		var u *Unavailable
		if !errors.As(err, &u) {
			u = unavailable(build.UnavailableRuntime, "%v", err)
		}
		w.setDown(u)
		w.log.Warn("builds are unavailable", "reason", u.Reason, "detail", u.Detail)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryAfter(u.Reason)):
		}
	}
}

func retryAfter(reason string) time.Duration {
	switch reason {
	case build.UnavailableNetwork:
		// Cheap to check, and builds wait for it.
		return 15 * time.Second
	case build.UnavailableImages, build.UnavailableOffline:
		return 2 * time.Minute
	default:
		return 10 * time.Minute
	}
}

// recover makes builds available again once the limits are back. It
// removes nothing: the builds the worker knows stay as they are.
func (w *Worker) recover() error {
	if p := w.protected(); p != nil {
		return p
	}
	if _, err := w.resolvers(); err != nil {
		return unavailable(build.UnavailableNetwork, "%v", err)
	}
	w.mu.Lock()
	broken := w.broken
	w.mu.Unlock()
	if broken != "" {
		return unavailable(build.UnavailableDisk, "%s; restart the build service", broken)
	}
	w.setDown(nil)
	w.log.Info("builds are available again")
	return nil
}

func (w *Worker) setDown(u *Unavailable) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.down = u
	w.ready = u == nil
}

// prepare checks the runtime, the disk and the network limits, removes
// whatever an earlier run left behind and fetches the images.
func (w *Worker) prepare(ctx context.Context) error {
	info, err := w.cfg.Runtime.Check(ctx)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.info = info
	w.defaults, w.maxima = machineLimits(info)
	w.mu.Unlock()

	if err := w.prepareDisk(); err != nil {
		return err
	}
	if err := w.cfg.Guard.Protected(); err != nil {
		return unavailable(build.UnavailableNetwork, "%v", err)
	}
	if _, err := w.resolvers(); err != nil {
		return unavailable(build.UnavailableNetwork, "%v", err)
	}
	// Nothing of an earlier run survives: its builds are gone with it (the
	// panel hears "unknown build" and marks them failed).
	if err := w.cfg.Runtime.Cleanup(ctx); err != nil {
		return unavailable(build.UnavailableRuntime, "removing the containers of an earlier run: %v", err)
	}
	for _, area := range []string{dirSources, dirResults} {
		if err := w.emptyArea(ctx, area); err != nil {
			return unavailable(build.UnavailableDisk, "cleaning %s: %v", area, err)
		}
	}
	for n := range w.cfg.Slots {
		if err := w.resetSlot(ctx, n); err != nil {
			return unavailable(build.UnavailableDisk, "cleaning slot %d: %v", n, err)
		}
	}
	if err := w.checkCaches(ctx); err != nil {
		return unavailable(build.UnavailableDisk, "checking the caches: %v", err)
	}
	images, err := w.cfg.Runtime.Images(ctx, w.cfg.Images)
	w.mu.Lock()
	w.images = images
	w.mu.Unlock()
	if err != nil {
		// The runtime says when the registry could not be reached at all.
		var u *Unavailable
		if errors.As(err, &u) {
			return u
		}
		return unavailable(build.UnavailableImages, "%v", err)
	}
	w.setDown(nil)
	w.log.Info("builds are available", "runtime", info.Name, "slots", w.cfg.Slots)
	return nil
}

// machineLimits lowers the default and maximum limits to the machine: a
// build never gets more than half of the memory or more processors than
// there are.
func machineLimits(info RuntimeInfo) (def, max build.Limits) {
	def, max = build.DefaultLimits, build.MaxLimits
	if info.MemoryBytes > 0 {
		max.MemoryBytes = min(max.MemoryBytes, max64(info.MemoryBytes/2, 256<<20))
		def.MemoryBytes = min(def.MemoryBytes, max.MemoryBytes)
	}
	if info.CPUs > 0 {
		max.CPUMillis = min(max.CPUMillis, info.CPUs*1000)
		def.CPUMillis = min(def.CPUMillis, max.CPUMillis)
	}
	return def, max
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// prepareDisk checks the build disk and creates its areas with their
// projects. A folder with the wrong project (made by hand, or by an older
// layout) is removed and made again.
func (w *Worker) prepareDisk() error {
	if err := w.cfg.Disk.Check(w.cfg.Work); err != nil {
		return unavailable(build.UnavailableDisk, "%v", err)
	}
	areas := []struct {
		dir     string
		project uint32
	}{
		{w.path(dirImages), projectImages},
		{w.path(dirResults), projectResults},
		{w.path(dirSources), projectSources},
	}
	for n := range w.cfg.Slots {
		areas = append(areas, struct {
			dir     string
			project uint32
		}{w.slotDir(n), uint32(projectSlotBase + n)})
	}
	if err := os.MkdirAll(w.path(dirSlots), 0o700); err != nil {
		return unavailable(build.UnavailableDisk, "%v", err)
	}
	if err := os.MkdirAll(w.path(dirCaches), 0o700); err != nil {
		return unavailable(build.UnavailableDisk, "%v", err)
	}
	for _, a := range areas {
		if err := w.ensureProjectDir(context.Background(), a.dir, a.project); err != nil {
			return unavailable(build.UnavailableDisk, "%v", err)
		}
	}
	// Slots of an earlier configuration with more of them.
	entries, err := os.ReadDir(w.path(dirSlots))
	if err != nil {
		return unavailable(build.UnavailableDisk, "%v", err)
	}
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err != nil || n < 0 || n >= w.cfg.Slots || strconv.Itoa(n) != e.Name() {
			if err := w.cfg.Runtime.RemoveTree(context.Background(), w.path(dirSlots, e.Name())); err != nil {
				return unavailable(build.UnavailableDisk, "%v", err)
			}
		}
	}
	// The quota of a slot must be the one the configuration names: else
	// the setup script did not set it, and a build could fill the disk.
	u, err := w.cfg.Disk.Usage(w.slotDir(0))
	if err != nil {
		return unavailable(build.UnavailableDisk, "%v", err)
	}
	if !near(u.TotalBytes, w.cfg.WorkspaceBytes) {
		return unavailable(build.UnavailableDisk, "the disk space of a build is %d bytes instead of %d: the quotas of the build disk are not set (restart ownpanel-build-setup)", u.TotalBytes, w.cfg.WorkspaceBytes)
	}
	return nil
}

// near: within 2 % (the file system rounds to blocks and keeps a little).
func near(got, want int64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d <= want/50
}

// ensureProjectDir makes dir with the project, or checks the one there.
func (w *Worker) ensureProjectDir(ctx context.Context, dir string, project uint32) error {
	info, err := os.Lstat(dir)
	if err == nil && info.IsDir() {
		if p, err := w.cfg.Disk.Project(dir); err == nil && p == project {
			return nil
		}
	}
	if err == nil {
		if err := w.cfg.Runtime.RemoveTree(ctx, dir); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	return w.cfg.Disk.SetProject(dir, project)
}

// emptyArea removes everything inside an area.
func (w *Worker) emptyArea(ctx context.Context, area string) error {
	entries, err := os.ReadDir(w.path(area))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := w.cfg.Runtime.RemoveTree(ctx, w.path(area, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// resetSlot empties a slot: the workspace and control folders of the last
// build go.
func (w *Worker) resetSlot(ctx context.Context, n int) error {
	dir := w.slotDir(n)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := w.cfg.Runtime.RemoveTree(ctx, filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// recheck: once a minute, whether the network limits and the disk are still
// there (an administrator may have flushed the packet filter). While they
// are not, no build starts.
func (w *Worker) recheck(ctx context.Context) {
	w.mu.Lock()
	ready := w.ready
	w.mu.Unlock()
	if !ready {
		go w.prepareLoop(ctx) // a no-op while one runs
		return
	}
	if err := w.protected(); err != nil {
		w.setDown(err)
		w.log.Error("builds are unavailable: the limits for builds are gone", "reason", err.Reason, "detail", err.Detail)
		go w.prepareLoop(ctx)
	}
}

// protected checks the network limits and the build disk.
func (w *Worker) protected() *Unavailable {
	if err := w.cfg.Guard.Protected(); err != nil {
		return unavailable(build.UnavailableNetwork, "%v", err)
	}
	if err := w.cfg.Disk.Check(w.cfg.Work); err != nil {
		return unavailable(build.UnavailableDisk, "%v", err)
	}
	return nil
}

// resolvers reads the name servers of builds. Loopback, unspecified and
// link-local addresses are left out: those are this server's or its
// network's own.
func (w *Worker) resolvers() ([]netip.Addr, error) {
	data, err := readSmall(w.cfg.Resolvers, 4<<10)
	if err != nil {
		return nil, fmt.Errorf("the name servers for builds are not known (%v): is ownpanel-build-setup running?", err)
	}
	var out []netip.Addr
	for _, line := range strings.Split(string(data), "\n") {
		a, err := netip.ParseAddr(strings.TrimSpace(line))
		if err != nil || a.Zone() != "" || a.IsLoopback() || a.IsUnspecified() || a.IsLinkLocalUnicast() || a.IsMulticast() {
			continue
		}
		out = append(out, a.Unmap())
		if len(out) == 3 {
			break
		}
	}
	if len(out) == 0 {
		// The server only names a resolver on itself (e.g. a local cache):
		// builds may not reach it, so they use public ones, which the
		// packet filter lets through like any public address.
		out = []netip.Addr{netip.MustParseAddr("9.9.9.9"), netip.MustParseAddr("1.1.1.1")}
	}
	return out, nil
}

func readSmall(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, err := f.Read(buf)
	// An empty file is no error: the setup writes no resolver when the
	// server only names one on itself.
	if err != nil && n == 0 && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

func (w *Worker) poke() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Status describes the worker.
func (w *Worker) Status() build.Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := build.Status{
		Available:      w.ready,
		Runtime:        w.info.Name,
		Images:         append([]build.ImageStatus(nil), w.images...),
		Slots:          w.cfg.Slots,
		Waiting:        len(w.queue),
		DefaultLimits:  w.defaults,
		MaxLimits:      w.maxima,
		WorkspaceBytes: w.cfg.WorkspaceBytes,
		CacheBytes:     w.cfg.CacheBytes,
	}
	if st.Images == nil {
		for _, img := range w.cfg.Images {
			st.Images = append(st.Images, build.ImageStatus{Image: img})
		}
	}
	for _, j := range w.slots {
		if j != nil {
			st.Running++
		}
	}
	if w.down != nil {
		st.Reason, st.Detail = w.down.Reason, w.down.Detail
	}
	if u, err := w.cfg.Disk.Usage(w.cfg.Work); err == nil {
		st.DiskBytes, st.FreeBytes = u.TotalBytes, u.FreeBytes
	}
	return st
}

// apiError is an answer to the panel that is not a success.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func errorf(status int, format string, args ...any) error {
	return &apiError{status, fmt.Sprintf(format, args...)}
}

const (
	statusBadRequest  = 400
	statusNotFound    = 404
	statusConflict    = 409
	statusTooLarge    = 413
	statusTooMany     = 429
	statusUnavailable = 503
	statusNoSpace     = 507
)

// Create registers a build.
func (w *Worker) Create(spec build.Spec) (build.Build, error) {
	if err := spec.Validate(); err != nil {
		return build.Build{}, errorf(statusBadRequest, "%v", err)
	}
	if _, ok := build.FindImage(w.cfg.Images, spec.Image); !ok {
		return build.Build{}, errorf(statusBadRequest, "there is no build image called %q", spec.Image)
	}
	spec.Output, _ = build.CleanOutput(spec.Output)
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.ready {
		return build.Build{}, errorf(statusUnavailable, "builds are unavailable on this server: %s", w.down.Detail)
	}
	limits, err := spec.Limits.Resolve(w.defaults, w.maxima)
	if err != nil {
		return build.Build{}, errorf(statusBadRequest, "%v", err)
	}
	if _, ok := w.builds[spec.ID]; ok {
		return build.Build{}, errorf(statusConflict, "build %d exists already", spec.ID)
	}
	if len(w.builds) >= build.MaxBuilds {
		return build.Build{}, errorf(statusTooMany, "the build service knows %d builds already; remove finished ones first", build.MaxBuilds)
	}
	spec.Limits = limits
	j := &job{spec: spec, limits: limits, state: build.StateCreated, created: w.cfg.Now(), slot: -1, done: make(chan struct{})}
	w.builds[spec.ID] = j
	w.log.Info("build created", "build", spec.ID, "site", spec.Site, "image", spec.Image)
	return j.view(), nil
}

func (j *job) view() build.Build {
	b := build.Build{
		ID: j.spec.ID, Site: j.spec.Site, Image: j.spec.Image, State: j.state,
		HasSource: j.hasSource, Reason: j.reason, Message: j.message, ExitCode: j.exitCode,
		Limits: j.limits, CreatedAt: stamp(j.created), StartedAt: stamp(j.started), FinishedAt: stamp(j.finished),
		LogBytes: j.logSize, LogTruncated: j.logCut, OutputBytes: j.outBytes, OutputFiles: j.outFiles,
	}
	if j.log != nil {
		b.LogBytes, b.LogTruncated, _, _ = j.log.state()
	}
	return b
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (w *Worker) get(id uint64) (*job, error) {
	j, ok := w.builds[id]
	if !ok {
		return nil, errorf(statusNotFound, "there is no build %d", id)
	}
	return j, nil
}

// Get returns a build.
func (w *Worker) Get(id uint64) (build.Build, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	j, err := w.get(id)
	if err != nil {
		return build.Build{}, err
	}
	return j.view(), nil
}

// List returns every build, oldest first.
func (w *Worker) List() []build.Build {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]build.Build, 0, len(w.builds))
	for _, j := range w.builds {
		out = append(out, j.view())
	}
	slices.SortFunc(out, func(a, b build.Build) int {
		if c := strings.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return int(a.ID) - int(b.ID)
	})
	return out
}

func (w *Worker) sourcePath(id uint64) string { return w.path(dirSources, fmt.Sprintf("%d.src", id)) }
func (w *Worker) logPath(id uint64) string    { return w.path(dirResults, fmt.Sprintf("%d.log", id)) }
func (w *Worker) outputPath(id uint64) string {
	return w.path(dirResults, fmt.Sprintf("%d.tar.gz", id))
}

// Start puts a build in line.
func (w *Worker) Start(id uint64) (build.Build, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	j, err := w.get(id)
	if err != nil {
		return build.Build{}, err
	}
	switch {
	case j.state != build.StateCreated:
		return j.view(), nil // started already: starting twice is fine
	case !j.hasSource:
		return build.Build{}, errorf(statusConflict, "build %d has no source tree yet", id)
	case !w.ready:
		return build.Build{}, errorf(statusUnavailable, "builds are unavailable on this server: %s", w.down.Detail)
	case len(w.queue) >= build.MaxWaiting:
		return build.Build{}, errorf(statusTooMany, "%d builds are waiting already", build.MaxWaiting)
	}
	j.state = build.StateQueued
	w.queue = append(w.queue, j)
	w.poke()
	return j.view(), nil
}

// Cancel stops a build that has not ended.
func (w *Worker) Cancel(id uint64) (build.Build, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	j, err := w.get(id)
	if err != nil {
		return build.Build{}, err
	}
	w.cancelLocked(j, "cancelled")
	return j.view(), nil
}

// cancelLocked ends a build that has not ended: one that waits is
// finished at once, a running one is killed (and finished by its run).
func (w *Worker) cancelLocked(j *job, message string) {
	switch j.state {
	case build.StateCreated, build.StateQueued:
		w.queue = slices.DeleteFunc(w.queue, func(q *job) bool { return q == j })
		j.state = build.StateCancelled
		j.message = message
		j.finished = w.cfg.Now()
		j.spec.Secrets = nil
		os.Remove(w.sourcePath(j.spec.ID))
		close(j.done)
	case build.StateRunning:
		if !j.cancelled {
			j.cancelled = true
			j.message = message
			j.cancel()
		}
	}
}

// Remove stops a build and deletes everything of it.
func (w *Worker) Remove(id uint64) error {
	w.mu.Lock()
	j, err := w.get(id)
	if err != nil {
		w.mu.Unlock()
		return err
	}
	w.cancelLocked(j, "removed")
	w.mu.Unlock()
	<-j.done
	w.mu.Lock()
	delete(w.builds, id)
	w.mu.Unlock()
	os.Remove(w.logPath(id))
	os.Remove(w.outputPath(id))
	os.Remove(w.sourcePath(id))
	return nil
}

// expire forgets finished builds after ResultTTL and builds never started
// after UnstartedTTL.
func (w *Worker) expire() {
	now := w.cfg.Now()
	var gone []uint64
	w.mu.Lock()
	for id, j := range w.builds {
		switch {
		case build.Finished(j.state) && now.Sub(j.finished) > build.ResultTTL:
			gone = append(gone, id)
		case j.state == build.StateCreated && now.Sub(j.created) > build.UnstartedTTL:
			gone = append(gone, id)
		}
	}
	w.mu.Unlock()
	for _, id := range gone {
		_ = w.Remove(id)
	}
}

// schedule starts waiting builds while slots are free, oldest first, at
// most one per site.
func (w *Worker) schedule() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.ready || w.closing {
		return
	}
	busy := map[string]bool{}
	for site := range w.clearing {
		busy[site] = true
	}
	for _, j := range w.slots {
		if j != nil {
			busy[j.spec.Site] = true
		}
	}
	for i := 0; i < len(w.queue); {
		slot := slices.Index(w.slots, nil)
		if slot < 0 {
			return
		}
		j := w.queue[i]
		if busy[j.spec.Site] {
			i++
			continue
		}
		w.queue = slices.Delete(w.queue, i, i+1)
		busy[j.spec.Site] = true
		w.slots[slot] = j
		j.slot = slot
		j.state = build.StateRunning
		j.started = w.cfg.Now()
		ctx, cancel := context.WithCancel(context.Background())
		j.cancel = cancel
		w.running.Add(1)
		go func() {
			defer w.running.Done()
			w.run(ctx, j)
		}()
	}
}

// finish records how a build ended and frees its slot.
func (w *Worker) finish(j *job, state, reason, message string, exitCode int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	j.state = state
	j.reason = reason
	if j.message == "" || state != build.StateCancelled {
		j.message = message
	}
	j.exitCode = exitCode
	j.finished = w.cfg.Now()
	j.spec.Secrets = nil
	j.spec.Env = nil
	if j.log != nil {
		j.logSize, j.logCut, _, _ = j.log.state()
		j.log = nil
	}
	if j.slot >= 0 {
		w.slots[j.slot] = nil
	}
	j.cancel()
	close(j.done)
	w.log.Info("build finished", "build", j.spec.ID, "site", j.spec.Site, "state", state, "reason", reason, "exit_code", exitCode)
	w.poke()
}

// shutdown kills every running build and waits until they are gone.
func (w *Worker) shutdown() {
	w.mu.Lock()
	w.closing = true
	for _, j := range w.builds {
		w.cancelLocked(j, "the build service stopped")
	}
	w.mu.Unlock()
	w.running.Wait()
}
