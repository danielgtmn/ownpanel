// Package build is how the panel runs the build of a site on the server.
//
// A build runs code nobody reviewed, so it never runs in the panel: the
// panel only talks to the build worker (`ownpaneld build-worker`, package
// internal/build/worker), a separate service running as the system user
// ownpanel-build, over a Unix socket only the panel's user can open. This
// package holds what both sides share (the requests, the limits, the list
// of images) and the panel's side of the socket, Client.
//
// The interface is deliberately narrow. The panel can say: build number N
// for this site, with this image from the fixed list, this command, these
// environment variables and these limits (never above the worker's own);
// here is the source tree; start; cancel; how is it going; give me the log;
// give me the output. It cannot name a path on the server, a mount, an
// image of its own choosing or an option of the container runtime; the
// worker builds the runtime's command line from fixed text and validated
// numbers and names. Source and output travel over the socket as tar
// streams and both sides treat what they receive as untrusted, so the two
// services never share a directory.
//
// See wiki/Security.md ("Builds") for the threat model.
package build

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultSocket is where ownpanel-build.service lets the worker listen.
const DefaultSocket = "/run/ownpanel-build/worker.sock"

// StateFile is where install.sh records whether it set builds up on this
// server, and why not. The panel reads it when the worker does not answer.
const StateFile = "/etc/ownpanel/builds"

// Worker socket endpoints (see Client for their use). Answers that are not
// a success carry a plain-text reason meant for people: 400 invalid input,
// 404 unknown build, 409 not possible in the build's state, 413 too large,
// 429 too many builds, 503 builds are unavailable on this server, 507 the
// waiting area for sources is full.
//
//	GET    /status                 Status
//	GET    /builds                 []Build, oldest first
//	POST   /builds                 body Spec → Build (created)
//	PUT    /builds/{id}/source     body: the source tree as tar or tar.gz → Build;
//	                               ?strip=N drops N leading folders of every name
//	POST   /builds/{id}/start      Build (queued or running)
//	POST   /builds/{id}/cancel     Build
//	GET    /builds/{id}            Build
//	GET    /builds/{id}/log        the log as text; ?offset=N skips N bytes,
//	                               ?follow=1 keeps the answer open until the build ends
//	GET    /builds/{id}/output     the output folder as tar.gz (after success)
//	DELETE /builds/{id}            204; stops the build and removes everything of it
//	GET    /caches                 []Cache
//	DELETE /caches/{site}          204; 409 while a build of the site runs
const (
	PathStatus = "/status"
	PathBuilds = "/builds"
	PathCaches = "/caches"
)

// Build states. A build is created, gets its source, is started (queued
// until a slot is free and no other build of the site runs), runs, and ends
// in one of the last three.
const (
	StateCreated   = "created"
	StateQueued    = "queued"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Finished tells whether a build in state s has ended.
func Finished(s string) bool {
	return s == StateSucceeded || s == StateFailed || s == StateCancelled
}

// Why a build failed (Build.Reason).
const (
	// ReasonExit: the command ended with an error (Build.ExitCode).
	ReasonExit = "exit"
	// ReasonTimeout: it ran longer than Limits.Seconds and was killed.
	ReasonTimeout = "timeout"
	// ReasonMemory: it used more memory than Limits.MemoryBytes.
	ReasonMemory = "memory"
	// ReasonDisk: its files did not fit into the build's disk space
	// (Status.WorkspaceBytes), or the build disk was full.
	ReasonDisk = "disk"
	// ReasonOutput: the output folder is missing, empty, too large or holds
	// something that is not a plain file or folder.
	ReasonOutput = "output"
	// ReasonSource: the source tree could not be unpacked.
	ReasonSource = "source"
	// ReasonUnprotected: the limits that keep builds away from the server
	// and its network were not in place, so the build was not run or was
	// stopped.
	ReasonUnprotected = "unprotected"
	// ReasonInternal: the worker failed; details are in its log.
	ReasonInternal = "internal"
)

// Why builds are unavailable (Status.Reason).
const (
	// UnavailableOff: an administrator turned builds off (install.sh
	// --no-builds).
	UnavailableOff = "off"
	// UnavailableSystem: the operating system, its version, the processor
	// type or the kernel cannot run isolated builds.
	UnavailableSystem = "system"
	// UnavailableRuntime: the container runtime is missing, too old (older
	// than MinRuntime) or does not work for the build user.
	UnavailableRuntime = "runtime"
	// UnavailableInstall: installing the build environment failed (see the
	// output of install.sh).
	UnavailableInstall = "install"
	// UnavailableLimits: processor, memory and process limits cannot be
	// enforced (the worker's control group is not delegated to it).
	UnavailableLimits = "limits"
	// UnavailableDisk: the build disk with its size limits is not in place
	// (not enough free space, or it could not be set up).
	UnavailableDisk = "disk"
	// UnavailableNetwork: the packet filter that keeps builds away from the
	// server's own services and private networks is not active.
	UnavailableNetwork = "network"
	// UnavailableImages: a build image is missing and could not be fetched.
	UnavailableImages = "images"
	// UnavailablePreparing: the worker is still starting (fetching images).
	UnavailablePreparing = "preparing"
	// UnavailableWorker: the build service does not answer.
	UnavailableWorker = "worker"
	// UnavailableNotConfigured: this panel was started without a build
	// service (development setups).
	UnavailableNotConfigured = "not_configured"
)

// MinRuntime is the oldest Podman builds run with. Debian 12 ships 4.3;
// Ubuntu 22.04 ships 3.4, which lacks options the worker relies on, so
// builds are unavailable there.
var MinRuntime = [2]int{4, 3}

// Limits of one build. The zero value of a field means the default; values
// above the worker's maxima are lowered to them. Disk space is not a limit
// of its own build: every build gets the same, enforced by the kernel
// (Status.WorkspaceBytes).
type Limits struct {
	// CPUMillis is processor time per second in thousandths of a CPU
	// (1500 = one and a half CPUs).
	CPUMillis int `json:"cpu_millis"`
	// MemoryBytes is memory including swap and temporary files in memory.
	MemoryBytes int64 `json:"memory_bytes"`
	// Processes is the number of processes and threads.
	Processes int `json:"processes"`
	// Seconds is the time the command may run before it is killed.
	Seconds int `json:"seconds"`
	// LogBytes is how much of the build's output is kept.
	LogBytes int64 `json:"log_bytes"`
}

// Fixed limits (not per build).
const (
	// MaxSourceBytes and MaxSourceFiles bound the source tree a build
	// receives (unpacked); MaxSourceStreamBytes the stream it arrives in.
	MaxSourceBytes       int64 = 1 << 30
	MaxSourceFiles             = 100_000
	MaxSourceStreamBytes int64 = MaxSourceBytes + MaxSourceFiles*4<<10
	// MaxOutputBytes and MaxOutputFiles bound the output folder; the same
	// as an uploaded archive (internal/deploy).
	MaxOutputBytes int64 = 1 << 30
	MaxOutputFiles       = 100_000
	// MaxCommandBytes bounds the build command.
	MaxCommandBytes = 16 << 10
	// MaxEnvVars, MaxEnvNameBytes, MaxEnvValueBytes and MaxEnvBytes bound
	// the environment variables of a build.
	MaxEnvVars       = 100
	MaxEnvNameBytes  = 128
	MaxEnvValueBytes = 32 << 10
	MaxEnvBytes      = 256 << 10
	// MaxWaiting is how many builds may wait for a free slot.
	MaxWaiting = 20
	// MaxBuilds is how many builds the worker knows at once, finished ones
	// included.
	MaxBuilds = 50
	// ResultTTL is how long a finished build (log and output) is kept for
	// the panel to fetch; UnstartedTTL how long one that was never started.
	ResultTTL    = 30 * time.Minute
	UnstartedTTL = 15 * time.Minute
	// MinSecretMask is the shortest secret value that is blanked out in
	// logs; shorter ones would blank out ordinary text.
	MinSecretMask = 4
)

// DefaultLimits and MaxLimits are what a build gets when it asks for
// nothing, and the most it can ask for, on a server with enough memory and
// processors; the worker lowers both to what the machine has (Status).
var (
	DefaultLimits = Limits{
		CPUMillis:   2000,
		MemoryBytes: 2 << 30,
		Processes:   1024,
		Seconds:     15 * 60,
		LogBytes:    2 << 20,
	}
	MaxLimits = Limits{
		CPUMillis:   4000,
		MemoryBytes: 4 << 30,
		Processes:   2048,
		Seconds:     60 * 60,
		LogBytes:    4 << 20,
	}
)

// Resolve fills the zero fields of l from def and lowers every field to
// max. Negative values and values too small to run anything are refused.
func (l Limits) Resolve(def, max Limits) (Limits, error) {
	if l.CPUMillis < 0 || l.MemoryBytes < 0 || l.Processes < 0 || l.Seconds < 0 || l.LogBytes < 0 {
		return Limits{}, errors.New("limits must not be negative")
	}
	pick := func(v, d, m int64) int64 {
		if v == 0 {
			v = d
		}
		return min(v, m)
	}
	out := Limits{
		CPUMillis:   int(pick(int64(l.CPUMillis), int64(def.CPUMillis), int64(max.CPUMillis))),
		MemoryBytes: pick(l.MemoryBytes, def.MemoryBytes, max.MemoryBytes),
		Processes:   int(pick(int64(l.Processes), int64(def.Processes), int64(max.Processes))),
		Seconds:     int(pick(int64(l.Seconds), int64(def.Seconds), int64(max.Seconds))),
		LogBytes:    pick(l.LogBytes, def.LogBytes, max.LogBytes),
	}
	switch {
	case out.CPUMillis < 100:
		return Limits{}, errors.New("the processor limit must be at least a tenth of a CPU")
	case out.MemoryBytes < 64<<20:
		return Limits{}, errors.New("the memory limit must be at least 64 MB")
	case out.Processes < 16:
		return Limits{}, errors.New("the process limit must be at least 16")
	case out.Seconds < 1:
		return Limits{}, errors.New("the time limit must be at least a second")
	case out.LogBytes < 1<<10:
		return Limits{}, errors.New("the log limit must be at least 1 KB")
	}
	return out, nil
}

// Image is a container image builds may run in. A build names it by Name;
// the worker only ever runs Ref, which pins the content by its digest, so
// neither the panel nor the registry can make a build run something else.
type Image struct {
	// Name is what a build asks for, e.g. "node-24".
	Name string `json:"name"`
	// Ref is the image with its digest: registry/repository@sha256:….
	Ref string `json:"ref"`
}

// Images is the fixed list of build images. They are the official images of
// their projects on Docker Hub, pinned to the digest of the multi-platform
// index (amd64 and arm64) at the time of the release; a new ownpanel
// release moves them forward, and the worker fetches what is missing when
// it starts and removes images that are no longer on the list. To update:
//
//	docker buildx imagetools inspect docker.io/library/node:24-bookworm-slim
//
// Which image a site uses is chosen per site later (#114); the panel can
// only ever name one of these.
var Images = []Image{
	{Name: "node-24", Ref: "docker.io/library/node@sha256:873f586f3794f52281bd039bf02c048a0be08fde619338f9941bad1eb6b34530"},
	{Name: "node-22", Ref: "docker.io/library/node@sha256:be979e5523c76fa4d5ca4ebd3099078f220aa47ec39c48ad5b5df9314debd6eb"},
}

var imageRefRe = regexp.MustCompile(`^[a-z0-9]+([.-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)+@sha256:[0-9a-f]{64}$`)

// ValidImage tells whether img can be on the list: a short name and a
// reference that names a registry and pins a digest (never a tag).
func ValidImage(img Image) bool {
	return nameRe.MatchString(img.Name) && imageRefRe.MatchString(img.Ref)
}

// FindImage returns the image of the list called name.
func FindImage(list []Image, name string) (Image, bool) {
	for _, img := range list {
		if img.Name == name {
			return img, true
		}
	}
	return Image{}, false
}

// Spec is one build as the panel asks for it.
type Spec struct {
	// ID is the build's number, chosen by the panel, never zero and never
	// reused while the worker still knows the build.
	ID uint64 `json:"id"`
	// Site names the site the build belongs to: its cache is only ever
	// given to builds with the same Site, and only one build per Site runs
	// at a time. The site's name (lower-case letters, digits, hyphens).
	Site string `json:"site"`
	// Image is the Name of one of Images.
	Image string `json:"image"`
	// Command is run by the image's /bin/sh in the source tree and stops at
	// the first failing line (sh -e), e.g. "npm ci\nnpm run build".
	Command string `json:"command"`
	// Output is the folder with the result, relative to the source tree,
	// e.g. "dist"; "." is the tree itself.
	Output string `json:"output"`
	// Env are environment variables for the command. Secrets are more of
	// them whose values are also blanked out in the log. Neither appears in
	// the worker's log, in process lists or in error messages.
	Env     map[string]string `json:"env,omitempty"`
	Secrets map[string]string `json:"secrets,omitempty"`
	// Limits lowers (or, up to the worker's maxima, raises) the limits.
	Limits Limits `json:"limits"`
	// Untrusted builds (pull requests from forks, #116) get an empty cache
	// of their own, in their slot, removed with it, instead of the site's:
	// what they leave there can never reach a later build of the site.
	Untrusted bool `json:"untrusted,omitempty"`
}

// String and GoString keep the command and the variables out of logs and
// error messages that print a Spec.
func (s Spec) String() string {
	return fmt.Sprintf("build %d (site %s, image %s)", s.ID, s.Site, s.Image)
}

// GoString is String.
func (s Spec) GoString() string { return "build.Spec{" + s.String() + "}" }

var (
	nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	envRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ValidSite tells whether s can be Spec.Site (the rule for site names in
// internal/store).
func ValidSite(s string) bool { return nameRe.MatchString(s) }

// reservedEnv are the variables the worker sets itself, and those the
// shell or the dynamic linker would act on before the command runs. A
// build's own variables cannot replace them.
var reservedEnv = map[string]bool{
	"HOME": true, "PWD": true, "OLDPWD": true, "IFS": true, "TMPDIR": true,
	"ENV": true, "BASH_ENV": true, "SHELLOPTS": true, "PS4": true,
	"OWNPANEL_BUILD": true,
}

// Validate checks everything of a Spec except its limits (Limits.Resolve)
// and that the image is known (the worker's list decides). The messages
// name what is wrong without repeating values.
func (s Spec) Validate() error {
	if s.ID == 0 {
		return errors.New("the build needs a number")
	}
	if !ValidSite(s.Site) {
		return errors.New("the site name is not valid")
	}
	if !nameRe.MatchString(s.Image) {
		return errors.New("the image name is not valid")
	}
	switch {
	case strings.TrimSpace(s.Command) == "":
		return errors.New("the build command is empty")
	case len(s.Command) > MaxCommandBytes:
		return fmt.Errorf("the build command is longer than %d KB", MaxCommandBytes>>10)
	case !utf8.ValidString(s.Command) || strings.ContainsRune(s.Command, 0):
		return errors.New("the build command is not valid text")
	}
	if _, err := CleanOutput(s.Output); err != nil {
		return err
	}
	if len(s.Env)+len(s.Secrets) > MaxEnvVars {
		return fmt.Errorf("more than %d environment variables", MaxEnvVars)
	}
	total := 0
	for _, vars := range []map[string]string{s.Env, s.Secrets} {
		for name, value := range vars {
			switch {
			case len(name) > MaxEnvNameBytes || !envRe.MatchString(name):
				return errors.New("the name of an environment variable is not valid (letters, digits and _, not starting with a digit)")
			case reservedEnv[name] || strings.HasPrefix(name, "LD_"):
				return fmt.Errorf("the environment variable %s is set by the build service and cannot be changed", name)
			case len(value) > MaxEnvValueBytes:
				return fmt.Errorf("the value of the environment variable %s is longer than %d KB", name, MaxEnvValueBytes>>10)
			case strings.ContainsRune(value, 0):
				return fmt.Errorf("the value of the environment variable %s contains a zero byte", name)
			}
			total += len(name) + len(value)
		}
	}
	for name := range s.Secrets {
		if _, twice := s.Env[name]; twice {
			return fmt.Errorf("the environment variable %s is given twice", name)
		}
	}
	if total > MaxEnvBytes {
		return fmt.Errorf("the environment variables are larger than %d KB together", MaxEnvBytes>>10)
	}
	return nil
}

// CleanOutput returns the output folder in its plain form ("dist",
// "public/site", "."), or an error if it is absolute or leaves the tree.
func CleanOutput(output string) (string, error) {
	if output == "" {
		return "", errors.New("the output folder is missing")
	}
	if len(output) > 1024 || !utf8.ValidString(output) || strings.ContainsAny(output, "\x00\\") {
		return "", errors.New("the output folder is not a valid path")
	}
	clean := path.Clean(output)
	if path.IsAbs(output) || !filepath.IsLocal(clean) {
		return "", errors.New("the output folder must be inside the source tree")
	}
	return clean, nil
}

// Build is the state of one build.
type Build struct {
	ID    uint64 `json:"id"`
	Site  string `json:"site"`
	Image string `json:"image"`
	State string `json:"state"`
	// HasSource: the source tree has arrived, so the build can be started.
	HasSource bool `json:"has_source"`
	// Reason says why a build failed (Reason* constants); Message says it
	// for people. ExitCode is the command's, when Reason is ReasonExit.
	Reason   string `json:"reason,omitempty"`
	Message  string `json:"message,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	// Limits are the limits in force.
	Limits     Limits `json:"limits"`
	CreatedAt  string `json:"created_at"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	// LogBytes is how much log there is to read; LogTruncated: output
	// beyond Limits.LogBytes was dropped.
	LogBytes     int64 `json:"log_bytes"`
	LogTruncated bool  `json:"log_truncated,omitempty"`
	// OutputBytes and OutputFiles describe the output of a successful build.
	OutputBytes int64 `json:"output_bytes,omitempty"`
	OutputFiles int   `json:"output_files,omitempty"`
}

// ImageStatus is one image of the list and whether the worker has it.
type ImageStatus struct {
	Image
	Present bool `json:"present"`
}

// Status is what the worker says about itself.
type Status struct {
	// Available: builds can run. Otherwise Reason (Unavailable* constants)
	// says why not and Detail carries the technical cause for
	// administrators (never a secret).
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
	// Runtime names the container runtime and its version.
	Runtime string        `json:"runtime,omitempty"`
	Images  []ImageStatus `json:"images"`
	// Slots is how many builds run at once; Running and Waiting how many
	// do right now.
	Slots   int `json:"slots"`
	Running int `json:"running"`
	Waiting int `json:"waiting"`
	// DefaultLimits and MaxLimits as lowered to this machine.
	DefaultLimits Limits `json:"default_limits"`
	MaxLimits     Limits `json:"max_limits"`
	// WorkspaceBytes is the disk space of one build (source tree and
	// everything it writes), CacheBytes that of one site's cache, DiskBytes
	// the build disk as a whole and FreeBytes what is free on it.
	WorkspaceBytes int64 `json:"workspace_bytes"`
	CacheBytes     int64 `json:"cache_bytes"`
	DiskBytes      int64 `json:"disk_bytes"`
	FreeBytes      int64 `json:"free_bytes"`
}

// Cache is the build cache of one site.
type Cache struct {
	Site  string `json:"site"`
	Bytes int64  `json:"bytes"`
	Files int64  `json:"files"`
}
