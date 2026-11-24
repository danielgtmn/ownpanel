// Package webui serves the single-page web interface. The built app is only
// embedded with the "webui" build tag, so plain `go build` and `go test` work
// without running the web build first.
package webui

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

const indexFile = "index.html"

// Handler serves fsys as a single-page app: existing files as they are, every
// other page address gets index.html so deep links survive a reload. Paths
// below /api/ are never answered here. A nil fsys means no app is built in.
func Handler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if fsys == nil {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			h.Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, "The web interface is not included in this build of ownpaneld. The control API is available under /api.\n")
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || !isFile(fsys, name) {
			name = indexFile
		}
		switch {
		case name == indexFile:
			h.Set("Cache-Control", "no-cache")
		case strings.HasPrefix(name, "assets/"):
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		serveFile(w, r, fsys, name)
	})
}

func isFile(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && info.Mode().IsRegular()
}

func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	f, err := fsys.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		// Not seekable (some fs.FS implementations): read it into memory.
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(b)
	}
	// ServeContent sets the content type from the extension and handles HEAD
	// and conditional requests; it never lists directories.
	http.ServeContent(w, r, name, time.Time{}, rs)
}
