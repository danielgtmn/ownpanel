//go:build podman

// Isolation tests against a real build service: run as root on a server
// where install.sh set builds up (Debian 12+, Ubuntu 24.04+):
//
//	GOOS=linux go test -c -tags podman -o isolation.test ./internal/build
//	sudo ./isolation.test -test.v
//
// Each test runs a hostile build and checks from the inside (its log) and
// from the outside (as root on the server) that it stayed in its place.
// OWNPANEL_BUILD_SOCKET overrides the socket, OWNPANEL_PANEL_ADDR the
// panel's address (default 127.0.0.1:8600).
package build_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
)

var nextID atomic.Uint64

func init() { nextID.Store(uint64(time.Now().Unix()) * 1000) }

func client(t *testing.T) *build.Client {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("run as root on a server with the build environment")
	}
	sock := os.Getenv("OWNPANEL_BUILD_SOCKET")
	if sock == "" {
		sock = build.DefaultSocket
	}
	c := build.NewClient(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for {
		st, err := c.Status(ctx)
		if err != nil {
			// Restarting: the socket comes back in a moment.
			select {
			case <-ctx.Done():
				t.Fatalf("build service: %v", err)
			case <-time.After(time.Second):
			}
			continue
		}
		if st.Available {
			return c
		}
		if st.Reason != build.UnavailablePreparing && st.Reason != build.UnavailableImages && st.Reason != build.UnavailableOffline {
			t.Fatalf("builds are unavailable: %s: %s", st.Reason, st.Detail)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("builds did not become available: %s: %s", st.Reason, st.Detail)
		case <-time.After(3 * time.Second):
		}
	}
}

// tarball packs files (name → content; a name ending in "@x" is executable).
func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		mode := int64(0o644)
		if n, ok := strings.CutSuffix(name, "@x"); ok {
			name, mode = n, 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(content))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

type result struct {
	b   build.Build
	log string
	out []byte
}

// runBuild creates, fills, starts and waits for a build; during calls
// fn (if any) while it runs.
func runBuild(t *testing.T, c *build.Client, spec build.Spec, files map[string]string, during func(id uint64)) result {
	t.Helper()
	ctx := context.Background()
	if spec.ID == 0 {
		spec.ID = nextID.Add(1)
	}
	if spec.Image == "" {
		spec.Image = "node-22"
	}
	if spec.Output == "" {
		spec.Output = "."
	}
	if files == nil {
		files = map[string]string{"README": "test\n"}
	}
	if _, err := c.Create(ctx, spec); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { c.Remove(context.Background(), spec.ID) })
	if _, err := c.Source(ctx, spec.ID, bytes.NewReader(tarball(t, files)), 0); err != nil {
		t.Fatalf("source: %v", err)
	}
	if _, err := c.Start(ctx, spec.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if during != nil {
		for {
			b, err := c.Get(ctx, spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if b.State == build.StateRunning || build.Finished(b.State) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		during(spec.ID)
	}
	rc, err := c.Log(ctx, spec.ID, 0, true)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	logText, _ := io.ReadAll(rc)
	rc.Close()
	var b build.Build
	for {
		b, err = c.Get(ctx, spec.ID)
		if err != nil {
			t.Fatal(err)
		}
		if build.Finished(b.State) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	r := result{b: b, log: string(logText)}
	if b.State == build.StateSucceeded {
		out, err := c.Output(ctx, spec.ID)
		if err != nil {
			t.Fatal(err)
		}
		r.out, _ = io.ReadAll(out)
		out.Close()
	}
	t.Logf("build %d: %s %s %q (exit %d)\n--- log ---\n%s--- end ---", b.ID, b.State, b.Reason, b.Message, b.ExitCode, r.log)
	return r
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func buildUID(t *testing.T) int {
	t.Helper()
	uid, err := strconv.Atoi(strings.TrimSpace(run(t, "id", "-u", "ownpanel-build")))
	if err != nil {
		t.Fatal(err)
	}
	return uid
}

// unitCgroup is the build service's control group.
func unitCgroup(t *testing.T) string {
	t.Helper()
	cg := strings.TrimSpace(run(t, "systemctl", "show", "-p", "ControlGroup", "--value", "ownpanel-build.service"))
	return filepath.Join("/sys/fs/cgroup", cg)
}

// leftovers lists control groups below builds that still hold processes
// (Podman 5 keeps an empty shared group for its monitors there).
func leftovers(t *testing.T) []string {
	t.Helper()
	root := filepath.Join(unitCgroup(t), "builds")
	var out []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			return nil
		}
		ev, _ := os.ReadFile(filepath.Join(p, "cgroup.events"))
		if strings.Contains(string(ev), "populated 1") {
			out = append(out, strings.TrimPrefix(p, root+"/"))
		}
		return nil
	})
	return out
}

func waitNoLeftovers(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		l := leftovers(t)
		if len(l) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("control groups of builds are left: %v", l)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// --- the tests ---------------------------------------------------------

// A build sees none of the panel's data, the secret key, the database,
// other services' sockets, the build service's own socket or Podman's.
func TestBuildCannotReadPanelData(t *testing.T) {
	c := client(t)
	key, err := os.ReadFile("/var/lib/ownpanel/secret.key")
	if err != nil {
		t.Fatalf("this test needs an installed panel: %v", err)
	}
	sum := sha256.Sum256(key)
	keyHash := hex.EncodeToString(sum[:])
	script := `
for p in /var/lib/ownpanel /var/lib/ownpanel/secret.key /var/lib/ownpanel/ownpanel.db /var/lib/ownpanel/sites \
  /var/lib/ownpanel-mail /var/lib/ownpanel-caddy /var/lib/ownpanel-update /etc/ownpanel \
  /run/ownpanel /run/ownpanel/caddy-admin.sock /run/ownpanel-mail/admin.sock /run/ownpanel-mail-web \
  /run/ownpanel-build/worker.sock /run/ownpanel-build /run/podman /run/user /var/run/docker.sock \
  /var/lib/ownpanel-build /var/lib/ownpanel-build/work /var/lib/ownpanel-build/disk.img; do
  if [ -e "$p" ]; then echo "VISIBLE $p"; else echo "absent $p"; fi
done
# Every file of the secret key's size, anywhere the build can look.
{ find / \( -path /proc -o -path /sys \) -prune -o -type f -size ` + strconv.Itoa(len(key)) + `c -print 2>/dev/null || true; } | while read -r f; do
  sha256sum "$f" 2>/dev/null || true
done
echo "sockets:"; find / \( -path /proc -o -path /sys \) -prune -o -type s -print 2>/dev/null || true
echo "mounts:"; cat /proc/self/mounts
`
	r := runBuild(t, c, build.Spec{Site: "iso-data", Command: script}, nil, nil)
	if r.b.State != build.StateSucceeded {
		t.Fatalf("the probe build failed: %s", r.b.Message)
	}
	if strings.Contains(r.log, "VISIBLE") {
		t.Errorf("a build can see a path of the panel or another service")
	}
	if strings.Contains(r.log, keyHash) {
		t.Errorf("a build can read the panel's secret key")
	}
	// /proc/self/mounts names the image layers on the build disk (that is
	// how overlay mounts look); nothing else of the server may show up.
	for _, line := range strings.Split(r.log, "\n") {
		if strings.HasPrefix(line, "absent") {
			continue
		}
		for _, p := range []string{"/var/lib/ownpanel/", "/var/lib/ownpanel ", "/var/lib/ownpanel-mail", "/var/lib/ownpanel-caddy", "/etc/ownpanel", "/run/ownpanel/", "/run/ownpanel-mail"} {
			if strings.Contains(line, p) {
				t.Errorf("a path of the panel shows up inside the build: %q", line)
			}
		}
	}
	// The build service itself (and so every container it starts) does not
	// see the panel's data either: its mount namespace hides it.
	pid := strings.TrimSpace(run(t, "systemctl", "show", "-p", "MainPID", "--value", "ownpanel-build.service"))
	for _, p := range []string{"/var/lib/ownpanel/secret.key", "/var/lib/ownpanel-mail/config.json", "/etc/ownpanel/ownpaneld.env"} {
		if _, err := os.Stat("/proc/" + pid + "/root" + p); err == nil {
			t.Errorf("the build service can see %s", p)
		}
	}
}

// No capabilities, no new privileges, a system call filter, no devices,
// its own namespaces, and root inside is the unprivileged build user
// outside.
func TestBuildPrivileges(t *testing.T) {
	c := client(t)
	uid := buildUID(t)
	marker := "privileges-probe-" + strconv.FormatUint(nextID.Add(1), 10)
	script := `
grep -E '^(Cap(Inh|Prm|Eff|Bnd|Amb)|NoNewPrivs|Seccomp):' /proc/self/status
echo "uid_map: $(cat /proc/self/uid_map | tr -s ' ' | tr '\n' ';')"
echo "hostname: $(hostname)"
echo "pid1: $(tr '\0' ' ' </proc/1/cmdline)"
echo "netdevs: $(ls /sys/class/net | tr '\n' ' ')"
echo "devices: $(ls /dev | tr '\n' ' ')"
mknod /tmp/mem c 1 1 2>/dev/null && echo "MKNOD-OK" || echo "mknod refused"
mount -t tmpfs none /mnt 2>/dev/null && echo "MOUNT-OK" || echo "mount refused"
cp /bin/sh /tmp/suid 2>/dev/null; chmod u+s /tmp/suid 2>/dev/null
touch /usr/bin/x 2>/dev/null && echo "ROOTFS-WRITABLE" || echo "root file system read-only"
echo ` + marker + ` >/dev/null; sleep 8
`
	var hostUIDs []string
	r := runBuild(t, c, build.Spec{Site: "iso-priv", Command: script}, nil, func(uint64) {
		time.Sleep(3 * time.Second)
		out := run(t, "ps", "-eo", "uid=,args=")
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "sleep 8") {
				hostUIDs = append(hostUIDs, strings.Fields(line)[0])
			}
		}
	})
	if r.b.State != build.StateSucceeded {
		t.Fatalf("the probe build failed: %s", r.b.Message)
	}
	for _, want := range []string{
		"CapInh:\t0000000000000000", "CapPrm:\t0000000000000000", "CapEff:\t0000000000000000",
		"CapBnd:\t0000000000000000", "CapAmb:\t0000000000000000", "NoNewPrivs:\t1", "Seccomp:\t2",
		"mknod refused", "mount refused", "root file system read-only", "hostname: build",
	} {
		if !strings.Contains(r.log, want) {
			t.Errorf("log lacks %q", want)
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(r.log), " "), "uid_map: 0 "+strconv.Itoa(uid)+" 1;") {
		t.Errorf("root in the container is not the build user (uid %d) outside", uid)
	}
	if strings.Contains(r.log, "pid1: /sbin/init") || strings.Contains(r.log, "pid1: /lib/systemd") {
		t.Errorf("the build shares the server's process namespace")
	}
	for _, dev := range []string{"sda", "vda", "nvme", "loop", "mem ", "kmsg"} {
		if strings.Contains(r.log[strings.Index(r.log, "devices:"):], dev) {
			t.Errorf("the device %s is visible in the build", dev)
		}
	}
	if len(hostUIDs) == 0 {
		t.Errorf("the build's process was not found on the server")
	}
	for _, u := range hostUIDs {
		if u != strconv.Itoa(uid) {
			t.Errorf("a build process runs as uid %s on the server, not as the build user %d", u, uid)
		}
	}
}

// localAddrs are the server's own non-loopback addresses.
func localAddrs(t *testing.T) []net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var out []net.IP
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			out = append(out, ipn.IP)
		}
	}
	return out
}

// The panel, the server's other services, loopback, private and
// link-local addresses and port 25 are unreachable; the Internet is not.
func TestBuildNetwork(t *testing.T) {
	c := client(t)
	panel := os.Getenv("OWNPANEL_PANEL_ADDR")
	if panel == "" {
		panel = "127.0.0.1:8600"
	}
	// A service on every address of the server, as another program would
	// listen there.
	var targets []string
	for _, ip := range append(localAddrs(t), net.IPv4(127, 0, 0, 1)) {
		ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				conn.Write([]byte("hello\n"))
				conn.Close()
			}
		}()
		port := ln.Addr().(*net.TCPAddr).Port
		targets = append(targets, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		if ip.IsLoopback() {
			// slirp4netns' address for the server's loopback.
			targets = append(targets, net.JoinHostPort("10.0.2.2", strconv.Itoa(port)))
		}
	}
	_, panelPort, _ := net.SplitHostPort(panel)
	blocked := append(targets,
		panel, "10.0.2.2:"+panelPort,
		"169.254.169.254:80", "10.0.0.1:80", "172.16.0.1:80", "192.168.0.1:80", "100.64.0.1:80",
		"1.1.1.1:25", "[::1]:"+panelPort, "[fd00::1]:80", "[fe80::1]:80",
		// systemd-resolved's local stub.
		"127.0.0.53:53")
	allowed := []string{"1.1.1.1:443", "registry.npmjs.org:443"}
	probe := `
const net = require('net');
const targets = JSON.parse(process.argv[2]);
let left = targets.length;
for (const t of targets) {
  const i = t.lastIndexOf(':');
  const host = t.slice(0, i).replace(/^\[|\]$/g, ''), port = +t.slice(i + 1);
  const s = net.connect({host, port, timeout: 4000});
  const done = (r) => { console.log('PROBE ' + t + ' ' + r); s.destroy(); if (--left === 0) process.exit(0); };
  s.on('connect', () => done('CONNECTED'));
  s.on('timeout', () => done('timeout'));
  s.on('error', (e) => done('error ' + e.code));
}
`
	list, _ := jsonList(append(append([]string{}, blocked...), allowed...))
	r := runBuild(t, c, build.Spec{Site: "iso-net", Command: "node probe.js '" + list + "'\nnode -e \"require('dns').lookup('example.com',(e,a)=>console.log('DNS',e?e.code:a))\""},
		map[string]string{"probe.js": probe}, nil)
	if r.b.State != build.StateSucceeded {
		t.Fatalf("the probe build failed: %s", r.b.Message)
	}
	for _, target := range blocked {
		line := probeLine(r.log, target)
		if line == "" {
			t.Errorf("no result for %s", target)
		} else if strings.Contains(line, "CONNECTED") {
			t.Errorf("a build reached %s", target)
		}
	}
	for _, target := range allowed {
		if line := probeLine(r.log, target); !strings.Contains(line, "CONNECTED") {
			t.Errorf("a build cannot reach %s on the Internet: %q", target, line)
		}
	}
	if !strings.Contains(r.log, "DNS null") && strings.Contains(r.log, "DNS E") {
		t.Errorf("a build cannot resolve names")
	}
}

func jsonList(items []string) (string, error) {
	var b strings.Builder
	b.WriteString("[")
	for i, s := range items {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(strconv.Quote(s))
	}
	b.WriteString("]")
	return b.String(), nil
}

func probeLine(log, target string) string {
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "PROBE "+target+" ") {
			return line
		}
	}
	return ""
}

// A fork bomb hits the process limit and the time limit; the server and
// the build service stay fine and nothing of it is left.
func TestBuildForkBomb(t *testing.T) {
	c := client(t)
	start := time.Now()
	r := runBuild(t, c, build.Spec{
		Site:    "iso-fork",
		Command: "cat /sys/fs/cgroup/pids.max\nbomb() { bomb | bomb & }\nbomb\nsleep 1000",
		Limits:  build.Limits{Processes: 64, Seconds: 15},
	}, nil, nil)
	if took := time.Since(start); took > 90*time.Second {
		t.Errorf("the fork bomb took %s to end", took)
	}
	if !strings.Contains(r.log, "64") {
		t.Errorf("the process limit inside is not 64")
	}
	if r.b.State != build.StateFailed {
		t.Errorf("the fork bomb ended as %s", r.b.State)
	}
	if _, err := c.Status(context.Background()); err != nil {
		t.Errorf("the build service does not answer after the fork bomb: %v", err)
	}
	waitNoLeftovers(t)
}

// A build cannot fill the server's disk: its writes end at its quota, and
// the server's free space does not move.
func TestBuildDiskLimit(t *testing.T) {
	c := client(t)
	var before, after syscall.Statfs_t
	syscall.Statfs("/", &before)
	r := runBuild(t, c, build.Spec{
		Site: "iso-disk",
		Command: `fallocate -l 500G /workspace/huge 2>&1 || echo "fallocate refused"
dd if=/dev/zero of=/workspace/fill bs=1M count=100000 2>&1 | tail -n 1
dd if=/dev/zero of=/cache/fill bs=1M count=100000 2>&1 | tail -n 1
df -k /workspace /cache | tail -n 2
exit 1`,
		Limits: build.Limits{MemoryBytes: 256 << 20},
	}, nil, nil)
	syscall.Statfs("/", &after)
	// The build wrote its whole quota (workspace and cache, several GB) and
	// tried 500 GB; the server's disk must not notice. (Other programs on
	// the server write meanwhile, so a little movement is allowed.)
	grew := (int64(before.Bavail) - int64(after.Bavail)) * int64(before.Bsize)
	t.Logf("the server's free space changed by %d MB", -grew>>20)
	if grew > 256<<20 {
		t.Errorf("the server's free space dropped by %d MB during the build", grew>>20)
	}
	if !strings.Contains(r.log, "Disk quota exceeded") && !strings.Contains(r.log, "No space left") {
		t.Errorf("the build's writes were not stopped by its quota")
	}
	if r.b.Reason != build.ReasonDisk {
		t.Errorf("the reason is %q, want %q", r.b.Reason, build.ReasonDisk)
	}
	// The next build gets an empty workspace and a working disk.
	r2 := runBuild(t, c, build.Spec{Site: "iso-disk", Command: "ls -A /workspace; df -k /workspace | tail -n 1; rm -f /cache/fill"}, nil, nil)
	if r2.b.State != build.StateSucceeded || strings.Contains(r2.log, "fill") {
		t.Errorf("the workspace of the next build is not fresh")
	}
}

// Links, devices and other special files in the output are refused, so
// nothing outside the workspace can be published; set-user-ID bits do not
// survive.
func TestBuildOutputEscape(t *testing.T) {
	c := client(t)
	cases := map[string]string{
		"link to a file":   "mkdir dist && echo hi > dist/index.html && ln -s /etc/passwd dist/passwd",
		"link to the root": "mkdir dist && echo hi > dist/index.html && ln -s / dist/root",
		"output is a link": "ln -s / dist",
		"named pipe":       "mkdir dist && echo hi > dist/index.html && mkfifo dist/pipe",
		"link out of tree": "mkdir dist && ln -s ../../../../../../etc dist/etc && echo hi > dist/index.html",
	}
	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			r := runBuild(t, c, build.Spec{Site: "iso-out", Command: cmd, Output: "dist"}, nil, nil)
			if r.b.State != build.StateFailed || r.b.Reason != build.ReasonOutput {
				t.Errorf("state %s reason %s, want failed output", r.b.State, r.b.Reason)
			}
			if _, err := c.Output(context.Background(), r.b.ID); err == nil {
				t.Errorf("the output of the build can be fetched")
			}
		})
	}
	r := runBuild(t, c, build.Spec{Site: "iso-out", Command: "mkdir dist && cp /bin/sh dist/sh && chmod 4755 dist/sh && echo hi > dist/index.html", Output: "dist"}, nil, nil)
	if r.b.State != build.StateSucceeded {
		t.Fatalf("the build failed: %s", r.b.Message)
	}
	gz, err := gzip.NewReader(bytes.NewReader(r.out))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Mode&^0o777 != 0 || h.Mode&0o111 != 0 && h.Typeflag == tar.TypeReg || h.Uid != 0 || h.Gid != 0 {
			t.Errorf("%s has mode %o, owner %d:%d in the output", h.Name, h.Mode, h.Uid, h.Gid)
		}
	}
}

// A build that ignores signals is killed at its time limit, with every
// process of it.
func TestBuildTimeLimit(t *testing.T) {
	c := client(t)
	start := time.Now()
	r := runBuild(t, c, build.Spec{
		Site:    "iso-time",
		Command: "trap '' TERM INT HUP\n(trap '' TERM INT HUP; while :; do sleep 1; done) &\n(setsid sh -c 'trap \"\" TERM; while :; do sleep 1; done') &\nwhile :; do sleep 1; done",
		Limits:  build.Limits{Seconds: 5},
	}, nil, nil)
	took := time.Since(start)
	if r.b.Reason != build.ReasonTimeout {
		t.Errorf("reason %q, want timeout", r.b.Reason)
	}
	if took > 45*time.Second {
		t.Errorf("the build was stopped only after %s", took)
	}
	waitNoLeftovers(t)
	uid := strconv.Itoa(buildUID(t))
	for _, line := range strings.Split(run(t, "ps", "-eo", "uid=,args="), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == uid && strings.Contains(line, "sleep") {
			t.Errorf("a process of the build is still running: %s", line)
		}
	}
}

// A build that uses more memory than allowed is stopped and says so.
func TestBuildMemoryLimit(t *testing.T) {
	c := client(t)
	r := runBuild(t, c, build.Spec{
		Site:    "iso-mem",
		Command: "cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory.swap.max\nnode -e 'const a=[]; for(;;){ a.push(Buffer.alloc(16<<20, 1)) }'",
		Limits:  build.Limits{MemoryBytes: 128 << 20},
	}, nil, nil)
	if !strings.Contains(r.log, strconv.Itoa(128<<20)) {
		t.Errorf("the memory limit inside is not 128 MB")
	}
	if r.b.State != build.StateFailed {
		t.Errorf("state %s", r.b.State)
	}
	if r.b.Reason != build.ReasonMemory && r.b.Reason != build.ReasonExit {
		t.Errorf("reason %q", r.b.Reason)
	}
}

// Secrets never show in the process list, the service's journal, files on
// the build disk after the build, or the build's log.
func TestBuildSecrets(t *testing.T) {
	c := client(t)
	secret := "S3cr3t-" + rand.Text()
	since := time.Now().Add(-time.Second).Format("2006-01-02 15:04:05")
	var inProcs []string
	r := runBuild(t, c, build.Spec{
		Site:    "iso-secret",
		Command: "echo \"the secret is $API_TOKEN\"\necho \"$API_TOKEN\" | rev | rev\nsleep 6",
		Secrets: map[string]string{"API_TOKEN": secret},
		Env:     map[string]string{"PLAIN": "visible"},
	}, nil, func(uint64) {
		time.Sleep(2 * time.Second)
		procs, _ := filepath.Glob("/proc/[0-9]*/cmdline")
		for _, p := range procs {
			b, _ := os.ReadFile(p)
			if bytes.Contains(b, []byte(secret)) {
				inProcs = append(inProcs, p)
			}
		}
		// The variables of build processes can only be read by their user.
		for _, p := range procs {
			dir := filepath.Dir(p)
			b, _ := os.ReadFile(filepath.Join(dir, "environ"))
			if !bytes.Contains(b, []byte(secret)) {
				continue
			}
			var st syscall.Stat_t
			if err := syscall.Stat(dir, &st); err == nil && int(st.Uid) != buildUID(t) {
				t.Errorf("a process of uid %d has the secret in its environment", st.Uid)
			}
		}
	})
	if len(inProcs) > 0 {
		t.Errorf("the secret is on a command line: %v", inProcs)
	}
	if strings.Contains(r.log, secret) {
		t.Errorf("the secret is in the build's log")
	}
	if !strings.Contains(r.log, "the secret is ***") {
		t.Errorf("the secret is not masked in the log")
	}
	journal := run(t, "journalctl", "--no-pager", "--since", since)
	if strings.Contains(journal, secret) {
		t.Errorf("the secret is in the system journal")
	}
	if out, _ := exec.Command("grep", "-rlF", secret, "/var/lib/ownpanel-build/work/slots", "/var/lib/ownpanel-build/work/results", "/var/lib/ownpanel-build/work/sources").CombinedOutput(); len(bytes.TrimSpace(out)) > 0 {
		t.Errorf("the secret is in files on the build disk: %s", out)
	}
	// Errors name the variable, never the value.
	long := strings.Repeat("x", build.MaxEnvValueBytes+1) + secret
	_, err := c.Create(context.Background(), build.Spec{ID: nextID.Add(1), Site: "iso-secret", Image: "node-22", Command: "true", Output: ".", Secrets: map[string]string{"API_TOKEN": long}})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("error %v", err)
	}
}

// A site's cache is its own: another site's build does not see it, the
// same site's next build does; workspaces are always fresh.
func TestBuildCacheIsolation(t *testing.T) {
	c := client(t)
	mark := "mark-" + rand.Text()
	r := runBuild(t, c, build.Spec{Site: "iso-cache-a", Command: "echo " + mark + " > /cache/mark; echo ws > /workspace/left-behind"}, nil, nil)
	if r.b.State != build.StateSucceeded {
		t.Fatal(r.b.Message)
	}
	// The command itself names the mark, so its own file is left out.
	r = runBuild(t, c, build.Spec{Site: "iso-cache-b", Command: "cat /cache/mark 2>&1 || true\necho \"cache: $(ls -A /cache | tr '\\n' ' ')\"\ngrep -rsl " + mark + " / --exclude-dir=proc --exclude-dir=sys --exclude-dir=build || echo not-found"}, nil, nil)
	if !strings.Contains(r.log, "not-found") || !strings.Contains(r.log, "cache: home \n") {
		t.Errorf("another site's build can see the cache")
	}
	r = runBuild(t, c, build.Spec{Site: "iso-cache-a", Command: "cat /cache/mark; ls -A /workspace"}, nil, nil)
	if !strings.Contains(r.log, mark) {
		t.Errorf("the site's own cache is gone")
	}
	if strings.Contains(r.log, "left-behind") {
		t.Errorf("the workspace of the last build is still there")
	}
	if err := c.ClearCache(context.Background(), "iso-cache-a"); err != nil {
		t.Fatal(err)
	}
	r = runBuild(t, c, build.Spec{Site: "iso-cache-a", Command: "cat /cache/mark 2>&1 || echo cleared"}, nil, nil)
	if !strings.Contains(r.log, "cleared") {
		t.Errorf("clearing the cache did not clear it")
	}
}

// Only images of the list, named by their name.
func TestBuildImages(t *testing.T) {
	c := client(t)
	for _, img := range []string{"docker.io/library/alpine:latest", "alpine", "node-99", "../node-22"} {
		_, err := c.Create(context.Background(), build.Spec{ID: nextID.Add(1), Site: "iso-img", Image: img, Command: "true", Output: "."})
		if err == nil {
			t.Errorf("the image %q was accepted", img)
		}
	}
}

// Source trees that try to leave the workspace are refused.
func TestBuildHostileSource(t *testing.T) {
	c := client(t)
	ctx := context.Background()
	entry := func(h tar.Header, content string) []byte {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		h.Size = int64(len(content))
		tw.WriteHeader(&h)
		tw.Write([]byte(content))
		tw.Close()
		return buf.Bytes()
	}
	refusedAtUpload := map[string][]byte{
		"dot-dot":  entry(tar.Header{Typeflag: tar.TypeReg, Name: "../../escape", Mode: 0o644}, "x"),
		"absolute": entry(tar.Header{Typeflag: tar.TypeReg, Name: "/etc/cron.d/x", Mode: 0o644}, "x"),
		"device":   entry(tar.Header{Typeflag: tar.TypeChar, Name: "mem", Mode: 0o644, Devmajor: 1, Devminor: 1}, ""),
		"hardlink": entry(tar.Header{Typeflag: tar.TypeLink, Name: "passwd", Linkname: "/etc/passwd"}, ""),
	}
	for name, tarData := range refusedAtUpload {
		id := nextID.Add(1)
		if _, err := c.Create(ctx, build.Spec{ID: id, Site: "iso-src", Image: "node-22", Command: "true", Output: "."}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Source(ctx, id, bytes.NewReader(tarData), 0); err == nil {
			t.Errorf("%s: the source was accepted", name)
		}
		c.Remove(ctx, id)
	}
	// A link, then a file through it: refused when it is unpacked.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: "out", Linkname: "/tmp"})
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "out/x", Mode: 0o644, Size: 1})
	tw.Write([]byte("x"))
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "suid", Mode: 0o4755, Size: 1, Uid: 0})
	tw.Write([]byte("x"))
	tw.Close()
	id := nextID.Add(1)
	if _, err := c.Create(ctx, build.Spec{ID: id, Site: "iso-src", Image: "node-22", Command: "true", Output: "."}); err != nil {
		t.Fatal(err)
	}
	defer c.Remove(ctx, id)
	if _, err := c.Source(ctx, id, bytes.NewReader(buf.Bytes()), 0); err != nil {
		t.Fatal(err)
	}
	c.Start(ctx, id)
	for {
		b, _ := c.Get(ctx, id)
		if build.Finished(b.State) {
			if b.Reason != build.ReasonSource {
				t.Errorf("a file through a link was unpacked: %s %s", b.State, b.Reason)
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat("/tmp/x"); err == nil {
		t.Errorf("a file was written outside the workspace")
	}
}

// Stopping or killing the build service leaves no container, process or
// workspace behind; it forgets the builds.
func TestBuildServiceRestart(t *testing.T) {
	c := client(t)
	ctx := context.Background()
	for _, how := range []string{"restart", "kill"} {
		t.Run(how, func(t *testing.T) {
			id := nextID.Add(1)
			c.Create(ctx, build.Spec{ID: id, Site: "iso-restart", Image: "node-22", Command: "echo started; sleep 600", Output: "."})
			c.Source(ctx, id, bytes.NewReader(tarball(t, map[string]string{"a": "b"})), 0)
			c.Start(ctx, id)
			for {
				b, _ := c.Get(ctx, id)
				if b.State == build.StateRunning && b.LogBytes > 0 {
					break
				}
				time.Sleep(300 * time.Millisecond)
			}
			if how == "restart" {
				run(t, "systemctl", "restart", "ownpanel-build.service")
			} else {
				pid := strings.TrimSpace(run(t, "systemctl", "show", "-p", "MainPID", "--value", "ownpanel-build.service"))
				run(t, "kill", "-KILL", pid)
				time.Sleep(7 * time.Second) // RestartSec=5
			}
			c = client(t)
			if _, err := c.Get(ctx, id); !build.IsNotFound(err) {
				t.Errorf("the build survived: %v", err)
			}
			waitNoLeftovers(t)
			uid := strconv.Itoa(buildUID(t))
			for _, line := range strings.Split(run(t, "ps", "-eo", "uid=,args="), "\n") {
				f := strings.Fields(line)
				if len(f) > 1 && f[0] == uid && strings.Contains(line, "sleep 600") {
					t.Errorf("a process of the build is still running: %s", line)
				}
			}
			slots, _ := filepath.Glob("/var/lib/ownpanel-build/work/slots/*/*")
			if len(slots) > 0 {
				t.Errorf("workspaces are left: %v", slots)
			}
			out := run(t, "runuser", "-u", "ownpanel-build", "--", "env", "XDG_RUNTIME_DIR=/run/ownpanel-build/podman", "HOME=/var/lib/ownpanel-build/home",
				"CONTAINERS_STORAGE_CONF=/run/ownpanel-build/podman/conf/storage.conf", "CONTAINERS_CONF=/run/ownpanel-build/podman/conf/containers.conf",
				"podman", "--root", "/var/lib/ownpanel-build/work/images/storage", "--runroot", "/run/ownpanel-build/podman/storage", "--cgroup-manager", "cgroupfs", "--events-backend", "none", "ps", "--all", "--quiet")
			if strings.TrimSpace(out) != "" {
				t.Errorf("containers are left: %s", out)
			}
		})
	}
}

// When the packet filter disappears, no build runs, and a running one is
// stopped.
func TestBuildWithoutFilter(t *testing.T) {
	c := client(t)
	ctx := context.Background()
	id := nextID.Add(1)
	c.Create(ctx, build.Spec{ID: id, Site: "iso-filter", Image: "node-22", Command: "echo running; sleep 120", Output: "."})
	c.Source(ctx, id, bytes.NewReader(tarball(t, map[string]string{"a": "b"})), 0)
	c.Start(ctx, id)
	defer c.Remove(ctx, id)
	for {
		b, _ := c.Get(ctx, id)
		if b.State == build.StateRunning && b.LogBytes > 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	run(t, "nft", "delete", "table", "inet", "ownpanel_build")
	defer run(t, "systemctl", "reload", "ownpanel-build-setup.service")
	deadline := time.Now().Add(30 * time.Second)
	for {
		b, _ := c.Get(ctx, id)
		if build.Finished(b.State) {
			if b.Reason != build.ReasonUnprotected {
				t.Errorf("the build ended as %s %s", b.State, b.Reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the build still runs without the packet filter")
		}
		time.Sleep(500 * time.Millisecond)
	}
	st, _ := c.Status(ctx)
	if st.Available {
		t.Errorf("builds are reported available without the packet filter")
	}
	if _, err := c.Create(ctx, build.Spec{ID: nextID.Add(1), Site: "iso-filter", Image: "node-22", Command: "true", Output: "."}); !build.IsUnavailable(err) {
		t.Errorf("a build was accepted without the packet filter: %v", err)
	}
	// Back in place, builds become available again (the worker looks again
	// within a few minutes).
	run(t, "systemctl", "reload", "ownpanel-build-setup.service")
	fmt.Println("packet filter restored")
}
