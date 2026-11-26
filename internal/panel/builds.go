package panel

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// Builds. Sites are built on the server (#58) by a separate service
// running as a user of its own (internal/build/worker); the panel only
// talks to its socket. Here: whether this server can build sites, and why
// not; building a site is in sitebuild.go.

// BuildService is the build service as the panel uses it. *build.Client
// satisfies it; see there for what each method does.
type BuildService interface {
	Status(ctx context.Context) (build.Status, error)
	List(ctx context.Context) ([]build.Build, error)
	Create(ctx context.Context, spec build.Spec) (build.Build, error)
	Source(ctx context.Context, id uint64, tree io.Reader, strip int) (build.Build, error)
	Start(ctx context.Context, id uint64) (build.Build, error)
	Cancel(ctx context.Context, id uint64) (build.Build, error)
	Get(ctx context.Context, id uint64) (build.Build, error)
	Log(ctx context.Context, id uint64, offset int64, follow bool) (io.ReadCloser, error)
	Output(ctx context.Context, id uint64) (io.ReadCloser, error)
	Remove(ctx context.Context, id uint64) error
}

// buildStatusTimeout bounds the question to the build service.
const buildStatusTimeout = 3 * time.Second

// BuildAvailability says whether this server can build sites.
type BuildAvailability struct {
	Available bool `json:"available"`
	// Reason is one of the build.Unavailable* codes; Detail the technical
	// cause for administrators.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Slots is how many builds run at once; Running and Waiting how many do
	// now.
	Slots   int `json:"slots"`
	Running int `json:"running"`
	Waiting int `json:"waiting"`
	// MaxMemoryBytes, MaxCPUMillis and MaxSeconds are the most a build may
	// use; WorkspaceBytes its disk space, CacheBytes that of a site's cache.
	MaxMemoryBytes int64 `json:"max_memory_bytes"`
	MaxCPUMillis   int   `json:"max_cpu_millis"`
	MaxSeconds     int   `json:"max_seconds"`
	WorkspaceBytes int64 `json:"workspace_bytes"`
	CacheBytes     int64 `json:"cache_bytes"`
	// DiskBytes and FreeBytes describe the disk builds have together.
	DiskBytes int64 `json:"disk_bytes"`
	FreeBytes int64 `json:"free_bytes"`
	// Images names the build images (e.g. "node-24").
	Images []string `json:"images"`
}

// BuildAvailability tells administrators whether this server can build
// sites, and if not, why.
func (s *Service) BuildAvailability(ctx context.Context, a Actor) (BuildAvailability, error) {
	if err := needAdmin(a); err != nil {
		return BuildAvailability{}, err
	}
	out := BuildAvailability{Images: []string{}}
	if s.builds == nil {
		out.Reason = build.UnavailableNotConfigured
		out.Detail = "the panel was started without -build-worker"
		if reason, detail, ok := readBuildState(s.buildsStateFile); ok {
			out.Reason, out.Detail = reason, detail
		}
		return out, nil
	}
	qctx, cancel := context.WithTimeout(ctx, buildStatusTimeout)
	defer cancel()
	st, err := s.builds.Status(qctx)
	if err != nil {
		// The service is not running: install.sh may know why.
		out.Reason = build.UnavailableWorker
		out.Detail = "the build service does not answer (systemctl status ownpanel-build)"
		if reason, detail, ok := readBuildState(s.buildsStateFile); ok {
			out.Reason, out.Detail = reason, detail
		}
		return out, nil
	}
	out.Available = st.Available
	out.Reason, out.Detail = st.Reason, st.Detail
	if out.Available {
		out.Reason, out.Detail = "", ""
	} else if !knownBuildReason(out.Reason) {
		out.Reason = build.UnavailableWorker
	}
	out.Slots, out.Running, out.Waiting = st.Slots, st.Running, st.Waiting
	out.MaxMemoryBytes, out.MaxCPUMillis, out.MaxSeconds = st.MaxLimits.MemoryBytes, st.MaxLimits.CPUMillis, st.MaxLimits.Seconds
	out.WorkspaceBytes, out.CacheBytes = st.WorkspaceBytes, st.CacheBytes
	out.DiskBytes, out.FreeBytes = st.DiskBytes, st.FreeBytes
	for _, img := range st.Images {
		if build.ValidImage(img.Image) {
			out.Images = append(out.Images, img.Name)
		}
	}
	out.Detail = cleanDetail(out.Detail)
	return out, nil
}

var buildReasons = map[string]bool{
	build.UnavailableOff: true, build.UnavailableSystem: true, build.UnavailableRuntime: true,
	build.UnavailableInstall: true, build.UnavailableLimits: true, build.UnavailableDisk: true,
	build.UnavailableNetwork: true, build.UnavailableImages: true, build.UnavailablePreparing: true,
	build.UnavailableWorker: true, build.UnavailableNotConfigured: true,
}

func knownBuildReason(r string) bool { return buildReasons[r] }

// cleanDetail keeps a detail to one line of printable text of moderate
// length; it comes from another service (or a file) and is shown as text.
func cleanDetail(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "?"))
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = strings.ToValidUTF8(s[:300], "") + "…"
	}
	return s
}

// readBuildState reads install.sh's record (build.StateFile):
//
//	state=ready|unavailable
//	reason=<build.Unavailable* code>
//	detail=<text>
//
// ok is false when there is no record, or it says builds were set up (the
// service is then just not answering).
func readBuildState(path string) (reason, detail string, ok bool) {
	if path == "" {
		return "", "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	state := ""
	sc := bufio.NewScanner(io.LimitReader(f, 4<<10))
	for sc.Scan() {
		key, value, found := strings.Cut(sc.Text(), "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "state":
			state = strings.TrimSpace(value)
		case "reason":
			reason = strings.TrimSpace(value)
		case "detail":
			detail = cleanDetail(value)
		}
	}
	if state != "unavailable" || !knownBuildReason(reason) {
		return "", "", false
	}
	return reason, detail, true
}
