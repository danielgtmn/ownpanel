// Package buildtest is a fake build service for tests: it answers on a
// Unix socket like the real one (internal/build/worker), so the panel's
// real build.Client talks to it, but runs nothing. Each build ends as its
// Result says.
package buildtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// Result is how a fake build ends.
type Result struct {
	// State is build.StateSucceeded (the default), StateFailed or
	// StateCancelled.
	State    string
	Reason   string
	Message  string
	ExitCode int
	// Output is the output archive after success.
	Output []byte
	// Log is the build's log.
	Log string
	// Hold, when set, keeps the build running until it is closed (or the
	// build is cancelled).
	Hold chan struct{}
}

// Fake is the fake service.
type Fake struct {
	// Socket is the path to give build.NewClient.
	Socket string
	// Result decides how a build ends; nil means success with a small
	// site.
	Result func(spec build.Spec) Result
	// Busy makes Create answer 429.
	Busy bool

	mu      sync.Mutex
	builds  map[uint64]*fakeBuild
	specs   []build.Spec
	removed []uint64
	started []uint64
	cancels []uint64
}

type fakeBuild struct {
	b      build.Build
	spec   build.Spec
	source []byte
	res    Result
	stop   chan struct{}
}

// Start runs a fake service until the test ends.
func Start(t testing.TB) *Fake {
	t.Helper()
	// Socket paths are short (about 100 bytes): not below t.TempDir().
	dir, err := os.MkdirTemp("", "obt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &Fake{Socket: filepath.Join(dir, "w.sock"), builds: map[uint64]*fakeBuild{}}
	ln, err := net.Listen("unix", f.Socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f.handler()}
	go srv.Serve(ln)
	t.Cleanup(func() {
		f.mu.Lock()
		for _, fb := range f.builds {
			if fb.res.Hold != nil {
				select {
				case <-fb.stop:
				default:
					close(fb.stop)
				}
			}
		}
		f.mu.Unlock()
		srv.Close()
	})
	return f
}

// Client is a client of the fake.
func (f *Fake) Client() *build.Client { return build.NewClient(f.Socket) }

// Add puts a build into the fake as if an earlier run had made it.
func (f *Fake) Add(id uint64, site, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds[id] = &fakeBuild{b: build.Build{ID: id, Site: site, State: state}, stop: make(chan struct{})}
}

// Specs returns every spec created, in order.
func (f *Fake) Specs() []build.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]build.Spec(nil), f.specs...)
}

// Source returns the source tree a build received.
func (f *Fake) Source(id uint64) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fb := f.builds[id]; fb != nil {
		return fb.source
	}
	return nil
}

// Removed, Started and Cancelled return the builds removed, started and
// cancelled, in order.
func (f *Fake) Removed() []uint64   { return f.ids(&f.removed) }
func (f *Fake) Started() []uint64   { return f.ids(&f.started) }
func (f *Fake) Cancelled() []uint64 { return f.ids(&f.cancels) }

func (f *Fake) ids(p *[]uint64) []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint64(nil), (*p)...)
}

// Known returns how many builds the fake knows.
func (f *Fake) Known() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.builds)
}

// Running tells whether a build of the site runs now.
func (f *Fake) Running(site string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fb := range f.builds {
		if fb.b.Site == site && fb.b.State == build.StateRunning {
			return true
		}
	}
	return false
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (f *Fake) handler() http.Handler {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, status int, msg string) {
		w.WriteHeader(status)
		io.WriteString(w, msg)
	}
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	get := func(w http.ResponseWriter, r *http.Request) *fakeBuild {
		id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
		fb := f.builds[id]
		if fb == nil {
			fail(w, http.StatusNotFound, fmt.Sprintf("there is no build %d", id))
		}
		return fb
	}
	mux.HandleFunc("GET /builds", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []build.Build{}
		for _, fb := range f.builds {
			out = append(out, fb.b)
		}
		reply(w, out)
	})
	mux.HandleFunc("POST /builds", func(w http.ResponseWriter, r *http.Request) {
		var spec build.Spec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			fail(w, http.StatusBadRequest, "not JSON")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.Busy {
			fail(w, http.StatusTooManyRequests, "20 builds are waiting already")
			return
		}
		if err := spec.Validate(); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		f.specs = append(f.specs, spec)
		fb := &fakeBuild{spec: spec, stop: make(chan struct{}),
			b: build.Build{ID: spec.ID, Site: spec.Site, Image: spec.Image, State: build.StateCreated, CreatedAt: now()}}
		f.builds[spec.ID] = fb
		reply(w, fb.b)
	})
	mux.HandleFunc("PUT /builds/{id}/source", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		fb := get(w, r)
		if fb == nil {
			return
		}
		if err != nil {
			fail(w, http.StatusBadRequest, "the source could not be read")
			return
		}
		if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
			fail(w, http.StatusBadRequest, "the source tree is not a tar archive")
			return
		}
		fb.source, fb.b.HasSource = data, true
		reply(w, fb.b)
	})
	mux.HandleFunc("POST /builds/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		fb := get(w, r)
		if fb == nil {
			return
		}
		f.started = append(f.started, fb.b.ID)
		res := Result{State: build.StateSucceeded, Output: SiteArchive(map[string]string{"index.html": "built"})}
		if f.Result != nil {
			res = f.Result(fb.spec)
			if res.State == "" {
				res.State = build.StateSucceeded
			}
		}
		fb.res = res
		fb.b.State, fb.b.StartedAt = build.StateRunning, now()
		if res.Hold == nil {
			f.finish(fb)
		} else {
			go func() {
				select {
				case <-res.Hold:
				case <-fb.stop:
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if fb.b.State == build.StateRunning {
					f.finish(fb)
				}
			}()
		}
		reply(w, fb.b)
	})
	mux.HandleFunc("POST /builds/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		fb := get(w, r)
		if fb == nil {
			return
		}
		f.cancels = append(f.cancels, fb.b.ID)
		if !build.Finished(fb.b.State) {
			fb.b.State, fb.b.Message, fb.b.FinishedAt = build.StateCancelled, "cancelled", now()
		}
		reply(w, fb.b)
	})
	mux.HandleFunc("GET /builds/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if fb := get(w, r); fb != nil {
			reply(w, fb.b)
		}
	})
	mux.HandleFunc("GET /builds/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		fb := get(w, r)
		if fb == nil {
			return
		}
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		log := fb.res.Log
		if off > len(log) {
			off = len(log)
		}
		io.WriteString(w, log[off:])
	})
	mux.HandleFunc("GET /builds/{id}/output", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		fb := get(w, r)
		if fb == nil {
			f.mu.Unlock()
			return
		}
		if fb.b.State != build.StateSucceeded {
			f.mu.Unlock()
			fail(w, http.StatusConflict, "the build has not succeeded")
			return
		}
		out := fb.res.Output
		f.mu.Unlock()
		w.Write(out)
	})
	mux.HandleFunc("DELETE /builds/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		fb := get(w, r)
		if fb == nil {
			return
		}
		select {
		case <-fb.stop:
		default:
			close(fb.stop)
		}
		delete(f.builds, fb.b.ID)
		f.removed = append(f.removed, fb.b.ID)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// finish ends a running build as its Result says; f.mu is held.
func (f *Fake) finish(fb *fakeBuild) {
	res := fb.res
	fb.b.State, fb.b.Reason, fb.b.Message, fb.b.ExitCode = res.State, res.Reason, res.Message, res.ExitCode
	fb.b.FinishedAt = now()
	fb.b.LogBytes = int64(len(res.Log))
}

// SiteArchive packs files into a tar.gz.
func SiteArchive(files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// SymlinkArchive is a tar.gz with index.html and a link to /etc/passwd.
func SymlinkArchive() []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "index.html", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	tw.Write([]byte("hi"))
	tw.WriteHeader(&tar.Header{Name: "passwd", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
