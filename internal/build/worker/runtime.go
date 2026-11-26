package worker

import (
	"context"
	"fmt"
	"io"
	"net/netip"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// Runtime is what the worker needs from the container runtime. The real one
// is Podman (podman_linux.go); tests use a fake so everything around it
// (the socket, the queue, the limits, the files) is tested without one.
type Runtime interface {
	// Check tells whether builds can run here: the runtime exists and is
	// new enough, it works for this user, and the limits of a build can be
	// enforced. A reason why not comes back as *Unavailable.
	Check(ctx context.Context) (RuntimeInfo, error)
	// Cleanup removes every container and every process left by an earlier
	// run of the worker. Called before the first build.
	Cleanup(ctx context.Context) error
	// Images makes sure exactly the images of the list are in the local
	// store: it fetches what is missing and removes everything else.
	Images(ctx context.Context, list []build.Image) ([]build.ImageStatus, error)
	// Run runs one build and returns when nothing of it is left: no
	// process, no container. When ctx ends first, everything is killed at
	// once and Result.Killed is set.
	Run(ctx context.Context, run Run) (Result, error)
	// RemoveTree deletes a folder a build wrote to, including files that
	// belong to other users inside the build's container. dir is always
	// one of the worker's own folders.
	RemoveTree(ctx context.Context, dir string) error
}

// RuntimeInfo describes the machine and the runtime.
type RuntimeInfo struct {
	// Name is the runtime and its version, e.g. "podman 4.9.3".
	Name string
	// CPUs and MemoryBytes are what the machine has.
	CPUs        int
	MemoryBytes int64
}

// Unavailable is why builds cannot run (build.Unavailable* and a technical
// detail for administrators).
type Unavailable struct {
	Reason string
	Detail string
}

func (u *Unavailable) Error() string { return u.Reason + ": " + u.Detail }

func unavailable(reason, format string, args ...any) *Unavailable {
	return &Unavailable{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// Run is one build for the runtime. Every path is the worker's own: made
// from its directories and the build's number, slot or the validated site
// name.
type Run struct {
	ID    uint64
	Image build.Image
	// Workspace becomes /workspace (read-write), Cache /cache (read-write)
	// and Control /run/build (read-only: the start script, the
	// variables and the command).
	Workspace string
	Cache     string
	Control   string
	Limits    build.Limits
	// Resolvers are the name servers of the container.
	Resolvers []netip.Addr
	// Output receives what the build prints.
	Output io.Writer
}

// Result is how a build ended.
type Result struct {
	ExitCode int
	// OutOfMemory: the kernel stopped it at its memory limit.
	OutOfMemory bool
	// Killed: the context ended (time limit, cancel, lost protection) and
	// everything of the build was killed.
	Killed bool
}

// Paths inside a build's container.
const (
	containerWorkspace = "/workspace"
	containerCache     = "/cache"
	containerControl   = "/run/build"
	containerHome      = containerCache + "/home"
)

// Files in a build's control directory.
const (
	controlInit    = "init.sh"
	controlEnv     = "env.sh"
	controlCommand = "command.sh"
)

// initScript is what a build's container runs: fixed text, the same for
// every build. The variables and the command are files of their own, so
// neither is ever part of a command line (which other users of the server
// could read in the process list).
const initScript = `# Written by the ownpanel build service.
umask 022
export HOME=` + containerHome + `
export TMPDIR=/tmp
export OWNPANEL_BUILD=1
mkdir -p "$HOME" || exit 125
cd ` + containerWorkspace + ` || exit 125
. ` + containerControl + `/` + controlEnv + ` || exit 125
exec /bin/sh -e ` + containerControl + `/` + controlCommand + `
`
