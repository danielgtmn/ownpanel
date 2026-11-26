// Package webui serves the single-page web interface. The built app is only
// embedded with the "webui" build tag, so plain `go build` and `go test` work
// without running the web build first.
package webui

import (
	"bytes"
	"html"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

const indexFile = "index.html"

// ContentSecurityPolicy is sent with every answer of the web interface.
// Scripts and style sheets only come from the panel itself; index.html has
// no inline script. ui-sdk adds its CSS as constructed style sheets (the
// empty csp-nonce meta tag in index.html asks for that), which style-src
// does not restrict, and style properties set from script are not inline
// style attributes, so no 'unsafe-inline' and no nonce are needed. data:
// images are the small SVG icons inside the style sheet; site icons come
// from /api.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Handler serves fsys as a single-page app: existing files as they are, every
// other page address gets index.html so deep links survive a reload. Paths
// below /api/ are never answered here. A nil fsys means no app is built in.
func Handler(fsys fs.FS) http.Handler {
	return handler(fsys, nil)
}

// AccountMeta names the meta tag that tells the web app it was opened on
// the mail host (AccountHandler); its content is the mail host.
const AccountMeta = "ownpanel-mail-host"

// AccountHandler serves the web app on the mail host, where mailbox users
// sign in to their own account (wiki/Groupware-Architecture.md, "G0"):
// like Handler, but index.html carries a meta tag AccountMeta with the mail
// host, so the app shows the account area instead of the panel, and the
// panel's API and MCP are never answered here.
func AccountHandler(fsys fs.FS, mailHost func() string) http.Handler {
	return handler(fsys, func(index []byte) []byte {
		tag := []byte(`<meta name="` + AccountMeta + `" content="` + html.EscapeString(mailHost()) + `" />`)
		i := bytes.Index(index, []byte("<head>"))
		if i < 0 {
			return append(append([]byte(nil), tag...), index...)
		}
		i += len("<head>")
		out := append([]byte(nil), index[:i]...)
		out = append(out, tag...)
		return append(out, index[i:]...)
	})
}

func handler(fsys fs.FS, rewriteIndex func([]byte) []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", ContentSecurityPolicy)

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") ||
			(rewriteIndex != nil && (r.URL.Path == "/mcp" || strings.HasPrefix(r.URL.Path, "/mcp/"))) {
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
		case name == indexFile && rewriteIndex != nil:
			b, err := fs.ReadFile(fsys, indexFile)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			h.Set("Cache-Control", "no-cache")
			http.ServeContent(w, r, indexFile, time.Time{}, bytes.NewReader(rewriteIndex(b)))
			return
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
