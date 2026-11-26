//go:build linux

package worker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// Podman runs builds with rootless Podman through its command line. Every
// argument is fixed text, a number, or a path the worker made itself; the
// panel's input (the command, the variables) never becomes an argument:
// it is in files the container reads. Podman's own configuration files are
// the worker's (written at Check), so settings an administrator made for
// other uses of Podman on the server do not reach builds.
type Podman struct {
	// Bin is the podman command (default /usr/bin/podman).
	Bin string
	// Home is the build user's home, RunDir its XDG_RUNTIME_DIR (0700).
	Home, RunDir string
	// Work is the build disk; images live in its images area.
	Work string
	Log  *slog.Logger

	cgroupBase string // the unit's control group, delegated to the worker
	parent     string // the control group under which builds run
}

const cgroupRoot = "/sys/fs/cgroup"

func (p *Podman) bin() string {
	if p.Bin != "" {
		return p.Bin
	}
	return "/usr/bin/podman"
}

func (p *Podman) confDir() string { return filepath.Join(p.RunDir, "conf") }
func (p *Podman) storage() string { return filepath.Join(p.Work, dirImages, "storage") }
func (p *Podman) tmpDir() string  { return filepath.Join(p.Work, dirImages, "tmp") }

// env is the whole environment of every podman process: nothing of the
// worker's own is passed on.
func (p *Podman) env() []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
		"HOME=" + p.Home,
		"XDG_RUNTIME_DIR=" + p.RunDir,
		"XDG_CONFIG_HOME=" + filepath.Join(p.RunDir, "config"),
		"XDG_DATA_HOME=" + filepath.Join(p.Home, ".local", "share"),
		"CONTAINERS_CONF=" + filepath.Join(p.confDir(), "containers.conf"),
		"CONTAINERS_STORAGE_CONF=" + filepath.Join(p.confDir(), "storage.conf"),
		"CONTAINERS_REGISTRIES_CONF=" + filepath.Join(p.confDir(), "registries.conf"),
		"TMPDIR=" + p.tmpDir(),
	}
}

// global are the options before every podman command.
func (p *Podman) global() []string {
	return []string{
		"--root", p.storage(),
		"--runroot", filepath.Join(p.RunDir, "storage"),
		"--storage-driver", "overlay",
		"--cgroup-manager", "cgroupfs",
		"--events-backend", "none",
		"--log-level", "error",
	}
}

// podman runs a podman command and returns its standard output. Errors
// carry the first line of what it printed on standard error.
func (p *Podman) podman(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.bin(), append(p.global(), args...)...)
	cmd.Env = p.env()
	cmd.Dir = p.RunDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedBuffer{b: &stdout, n: 4 << 20}
	cmd.Stderr = &limitedBuffer{b: &stderr, n: 64 << 10}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), &podmanError{args: args[0], msg: msg, err: err}
	}
	return stdout.Bytes(), nil
}

type podmanError struct {
	args string
	msg  string
	err  error
}

func (e *podmanError) Error() string { return "podman " + e.args + ": " + e.msg }
func (e *podmanError) Unwrap() error { return e.err }

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

type limitedBuffer struct {
	b *bytes.Buffer
	n int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.n - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// writeConfig writes Podman's configuration files. They are complete
// files of the worker's own: /etc/containers and the user's
// ~/.config/containers are not read.
func (p *Podman) writeConfig() error {
	if err := os.MkdirAll(p.confDir(), 0o700); err != nil {
		return err
	}
	files := map[string]string{
		"containers.conf": `# Written by the ownpanel build service; it replaces every other containers.conf.
[containers]
http_proxy = false
log_driver = "none"
default_sysctls = []
[engine]
cgroup_manager = "cgroupfs"
events_logger = "none"
image_copy_tmp_dir = "` + p.tmpDir() + `"
`,
		"storage.conf": `# Written by the ownpanel build service.
[storage]
driver = "overlay"
graphroot = "` + p.storage() + `"
runroot = "` + filepath.Join(p.RunDir, "storage") + `"
rootless_storage_path = "` + p.storage() + `"
`,
		// Images are always named in full, with their digest.
		"registries.conf": `# Written by the ownpanel build service.
unqualified-search-registries = []
short-name-mode = "enforcing"
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(p.confDir(), name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Check implements Runtime.
func (p *Podman) Check(ctx context.Context) (RuntimeInfo, error) {
	info := RuntimeInfo{CPUs: runtime.NumCPU(), MemoryBytes: memTotal()}
	if _, err := os.Stat(p.bin()); err != nil {
		return info, unavailable(build.UnavailableRuntime, "podman is not installed (%s is missing)", p.bin())
	}
	// First, before podman starts a process of its own (its namespace
	// keeper) that would land in the unit's control group, which must hold
	// no processes for its controllers to be handed down.
	if err := p.setupCgroups(); err != nil {
		return info, unavailable(build.UnavailableLimits, "%v", err)
	}
	if err := p.writeConfig(); err != nil {
		return info, unavailable(build.UnavailableRuntime, "writing podman's configuration: %v", err)
	}
	out, err := p.podman(ctx, 30*time.Second, "version", "--format", "{{.Client.Version}}")
	if err != nil {
		return info, unavailable(build.UnavailableRuntime, "%v", err)
	}
	version := strings.TrimSpace(string(out))
	info.Name = "podman " + version
	if !atLeast(version, build.MinRuntime) {
		return info, unavailable(build.UnavailableRuntime, "podman %s is too old; builds need %d.%d or newer", version, build.MinRuntime[0], build.MinRuntime[1])
	}
	// Containers need a range of user ids of their own (/etc/subuid):
	// root inside is this user outside, everyone else an id from that range.
	out, err = p.podman(ctx, 30*time.Second, "unshare", "cat", "/proc/self/uid_map")
	if err != nil {
		return info, unavailable(build.UnavailableRuntime, "%v", err)
	}
	if n := len(strings.Fields(strings.TrimSpace(string(out)))); n < 6 {
		return info, unavailable(build.UnavailableRuntime, "the build user has no subordinate user ids (/etc/subuid)")
	}
	return info, nil
}

// atLeast tells whether version (e.g. "4.3.1") is at least want.
func atLeast(version string, want [2]int) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(strings.TrimFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	if err1 != nil || err2 != nil {
		return false
	}
	return major > want[0] || major == want[0] && minor >= want[1]
}

func memTotal() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err == nil {
				return kb << 10
			}
		}
	}
	return 0
}

// setupCgroups takes over the control group systemd delegated to the
// service (Delegate=yes): the worker moves itself into a leaf "worker" and
// builds run below "builds", with the processor, memory and process
// controllers enabled, so their limits are enforced by the kernel.
func (p *Podman) setupCgroups() error {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
	path := ""
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if v, ok := strings.CutPrefix(line, "0::"); ok {
			path = v
		}
	}
	if path == "" {
		return errors.New("control groups version 2 are not in use")
	}
	path = strings.TrimSuffix(path, "/worker")
	base := filepath.Join(cgroupRoot, path)
	var st syscall.Stat_t
	if err := syscall.Stat(base, &st); err != nil {
		return err
	}
	if st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("the control group %s is not delegated to the build service (Delegate=yes)", path)
	}
	controllers, err := os.ReadFile(filepath.Join(base, "cgroup.controllers"))
	if err != nil {
		return err
	}
	for _, c := range []string{"cpu", "memory", "pids"} {
		if !contains(strings.Fields(string(controllers)), c) {
			return fmt.Errorf("the %s controller is not available to the build service", c)
		}
	}
	leaf := filepath.Join(base, "worker")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(base, "cgroup.subtree_control"), []byte("+cpu +memory +pids"), 0); err != nil {
		return err
	}
	builds := filepath.Join(base, "builds")
	if err := os.MkdirAll(builds, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(builds, "cgroup.subtree_control"), []byte("+cpu +memory +pids"), 0); err != nil {
		return err
	}
	p.cgroupBase = base
	p.parent = path + "/builds"
	return nil
}

// killCgroups kills every process in the control groups below builds whose
// name contains match ("" for all) and removes them. The kernel does this
// at once for a whole group (cgroup.kill), so nothing escapes by forking.
func (p *Podman) killCgroups(match string) {
	if p.cgroupBase == "" {
		return
	}
	root := filepath.Join(p.cgroupBase, "builds")
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == root {
			return nil
		}
		if match == "" || strings.Contains(path, match) {
			dirs = append(dirs, path)
		}
		return nil
	})
	for _, d := range dirs {
		_ = os.WriteFile(filepath.Join(d, "cgroup.kill"), []byte("1"), 0)
	}
	// Deepest first; a group is only removed once it is empty.
	for i := len(dirs) - 1; i >= 0; i-- {
		for try := 0; try < 50; try++ {
			if err := os.Remove(dirs[i]); err == nil || errors.Is(err, os.ErrNotExist) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

const containerLabel = "ownpanel.build"

func containerName(id uint64) string { return fmt.Sprintf("ownpanel-build-%d", id) }

// Cleanup implements Runtime.
func (p *Podman) Cleanup(ctx context.Context) error {
	out, err := p.podman(ctx, time.Minute, "ps", "--all", "--quiet", "--no-trunc", "--filter", "label="+containerLabel)
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(out)) {
		if _, err := p.podman(ctx, time.Minute, "rm", "--force", "--time", "0", "--", id); err != nil {
			return err
		}
	}
	p.killCgroups("")
	return nil
}

// Images implements Runtime.
func (p *Podman) Images(ctx context.Context, list []build.Image) ([]build.ImageStatus, error) {
	status := make([]build.ImageStatus, len(list))
	keep := map[string]bool{}
	// In the images area (made by the worker after Check), so downloads
	// count against its quota.
	if err := os.MkdirAll(p.tmpDir(), 0o700); err != nil {
		return status, err
	}
	var firstErr error
	for i, img := range list {
		status[i].Image = img
		if !build.ValidImage(img) {
			return status, fmt.Errorf("the image %q is not valid", img.Name)
		}
		if _, err := p.podman(ctx, time.Minute, "image", "exists", img.Ref); err != nil {
			p.Log.Info("fetching a build image", "image", img.Name, "ref", img.Ref)
			if _, err := p.podman(ctx, 30*time.Minute, "pull", "--quiet", img.Ref); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("fetching %s: %v", img.Name, err)
				}
				continue
			}
		}
		out, err := p.podman(ctx, time.Minute, "image", "inspect", "--format", "{{.ID}}", img.Ref)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		keep[strings.TrimPrefix(strings.TrimSpace(string(out)), "sha256:")] = true
		status[i].Present = true
	}
	if firstErr != nil {
		return status, firstErr
	}
	out, err := p.podman(ctx, time.Minute, "images", "--all", "--quiet", "--no-trunc")
	if err != nil {
		return status, err
	}
	for _, id := range strings.Fields(string(out)) {
		id = strings.TrimPrefix(id, "sha256:")
		if keep[id] {
			continue
		}
		p.Log.Info("removing an image that is no longer on the list of build images", "id", id)
		if _, err := p.podman(ctx, time.Minute, "rmi", "--force", "--", id); err != nil {
			p.Log.Warn("removing an image", "id", id, "err", err)
		}
	}
	return status, nil
}

// runArgs is the complete command line of a build: fixed options, the
// limits as numbers, and the worker's own paths. The order is fixed; tests
// compare it.
func (p *Podman) runArgs(r Run) []string {
	l := r.Limits
	args := []string{
		"run",
		"--name", containerName(r.ID),
		"--label", fmt.Sprintf("%s=%d", containerLabel, r.ID),
		"--pull", "never",
		"--log-driver", "none",
		// Its own network stack in user space: no access to the server's
		// loopback (allow_host_loopback=false); the packet filter of the
		// build user decides what else is reachable.
		"--network", "slirp4netns:allow_host_loopback=false",
	}
	for _, a := range r.Resolvers {
		args = append(args, "--dns", a.String())
	}
	args = append(args,
		"--dns-search", ".",
		"--no-hosts",
		"--hostname", "build",
		"--http-proxy=false",
		"--cap-drop", "all",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--read-only-tmpfs",
		"--ipc", "private",
		"--pid", "private",
		"--uts", "private",
		"--cgroupns", "private",
		"--cgroups", "enabled",
		"--cgroup-parent", p.parent,
		"--memory", strconv.FormatInt(l.MemoryBytes, 10),
		"--memory-swap", strconv.FormatInt(l.MemoryBytes, 10),
		"--cpus", strconv.FormatFloat(float64(l.CPUMillis)/1000, 'f', 3, 64),
		"--pids-limit", strconv.Itoa(l.Processes),
		"--ulimit", "core=0:0",
		"--oom-score-adj", "1000",
		// Podman's own hard stop, should the worker not get to it.
		"--timeout", strconv.Itoa(l.Seconds+60),
		"--user", "0:0",
		"--workdir", containerWorkspace,
		"--entrypoint", "/bin/sh",
		"--volume", r.Workspace+":"+containerWorkspace+":rw,nosuid,nodev",
		"--volume", r.Cache+":"+containerCache+":rw,nosuid,nodev",
		"--volume", r.Control+":"+containerControl+":ro,nosuid,nodev,noexec",
		r.Image.Ref,
		containerControl+"/"+controlInit,
	)
	return args
}

// Run implements Runtime.
func (p *Podman) Run(ctx context.Context, r Run) (Result, error) {
	if p.parent == "" {
		return Result{}, errors.New("the control groups for builds are not set up")
	}
	for _, dir := range []string{r.Workspace, r.Cache, r.Control} {
		if !p.inWork(dir) || strings.ContainsAny(dir, ":,") {
			return Result{}, fmt.Errorf("%s is not a folder of the build disk", dir)
		}
	}
	name := containerName(r.ID)
	cmd := exec.Command(p.bin(), append(p.global(), p.runArgs(r)...)...)
	cmd.Env = p.env()
	cmd.Dir = p.RunDir
	cmd.Stdout = r.Output
	cmd.Stderr = r.Output
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	var res Result
	var err error
	select {
	case err = <-waited:
	case <-ctx.Done():
		res.Killed = true
		p.kill(name)
		select {
		case err = <-waited:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			err = <-waited
		}
	}
	res.ExitCode = exitCode(err)
	if err != nil && res.ExitCode < 0 {
		p.remove(name)
		return res, err
	}

	full := ""
	out, ierr := p.podman(context.Background(), time.Minute, "inspect", "--type", "container", "--format", "{{.ID}} {{.State.OOMKilled}} {{.State.ExitCode}}", "--", name)
	if f := strings.Fields(string(out)); ierr == nil && len(f) == 3 {
		full = f[0]
		res.OutOfMemory = f[1] == "true"
		if code, err := strconv.Atoi(f[2]); err == nil && !res.Killed {
			res.ExitCode = code
		}
	}
	p.remove(name)
	if full != "" {
		p.killCgroups(full)
	}
	if ierr != nil && !res.Killed {
		// The container was never made (e.g. the image is gone): podman's
		// message is in the build's log.
		return res, fmt.Errorf("the container could not be started (podman exited with %d)", res.ExitCode)
	}
	return res, nil
}

func (p *Podman) kill(name string) {
	if _, err := p.podman(context.Background(), 30*time.Second, "kill", "--signal", "KILL", "--", name); err != nil {
		p.Log.Warn("killing a build", "container", name, "err", err)
	}
}

func (p *Podman) remove(name string) {
	if _, err := p.podman(context.Background(), time.Minute, "rm", "--force", "--ignore", "--time", "0", "--", name); err != nil {
		p.Log.Warn("removing a build container", "container", name, "err", err)
	}
}

func (p *Podman) inWork(dir string) bool {
	rel, err := filepath.Rel(p.Work, dir)
	return err == nil && filepath.IsLocal(rel) && filepath.Clean(dir) == dir
}

// RemoveTree implements Runtime: inside the build user's namespace, where
// it is root over the files of every id its containers used.
func (p *Podman) RemoveTree(ctx context.Context, dir string) error {
	if !p.inWork(dir) {
		return fmt.Errorf("refusing to remove %s: not on the build disk", dir)
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := p.podman(ctx, 10*time.Minute, "unshare", "rm", "-rf", "--one-file-system", "--", dir); err != nil {
		// Without the namespace (e.g. podman broke): what this user can.
		if rerr := removeTree(dir); rerr != nil {
			return fmt.Errorf("%v; %v", err, rerr)
		}
	}
	return nil
}
