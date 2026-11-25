// Package web is the mail service's web listener: what Caddy passes on from
// the mail host (wiki/Groupware-Architecture.md, "G0"). It listens on its
// own Unix socket, never the admin socket, and serves:
//
//	/account/api/…                     the self-service API (GET /account/api/health here,
//	                                   everything else from Options.Account)
//	/dav/…, /.well-known/caldav|carddav calendars and contacts (from Options.DAV)
//
// Only Caddy can connect to the socket, so the X-Forwarded-For header it
// sets is trusted on this listener alone (ClientIP).
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Limits of the web listener. DAV uploads (contacts with photos, calendar
// attachments) are the largest requests it will see; Caddy has its own
// limit in front (caddy.MailWebMaxBody).
const (
	ReadHeaderTimeout = 10 * time.Second
	ReadTimeout       = 2 * time.Minute
	WriteTimeout      = 2 * time.Minute
	IdleTimeout       = 2 * time.Minute
	MaxHeaderBytes    = 64 << 10
	MaxBodyBytes      = 16 << 20
)

// Options configure the handler.
type Options struct {
	// Account serves everything below /account/api/ except
	// /account/api/health; it sees the full path. nil answers 404.
	Account http.Handler
	// DAV serves /dav/… and /.well-known/caldav|carddav (package dav).
	// nil answers 501 and 404.
	DAV http.Handler
}

// NewServer returns the HTTP server for the web socket, with its time and
// size limits.
func NewServer(o Options) *http.Server {
	return &http.Server{
		Handler:           Handler(o),
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
}

// Handler routes the requests of the web socket.
func Handler(o Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /account/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	account := o.Account
	if account == nil {
		account = http.HandlerFunc(notFound)
	}
	mux.Handle("/account/api/", account)
	if o.DAV != nil {
		mux.Handle("/.well-known/caldav", o.DAV)
		mux.Handle("/.well-known/carddav", o.DAV)
		mux.Handle("/dav", o.DAV)
		mux.Handle("/dav/", o.DAV)
	} else {
		// Without calendars and contacts, clients that look for them find
		// nothing rather than a broken service.
		mux.HandleFunc("/.well-known/caldav", notFound)
		mux.HandleFunc("/.well-known/carddav", notFound)
		mux.HandleFunc("/dav", notImplemented)
		mux.HandleFunc("/dav/", notImplemented)
	}
	mux.HandleFunc("/", notFound)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		if ip, ok := forwardedFor(r); ok {
			r = r.WithContext(context.WithValue(r.Context(), clientIPKey{}, ip))
		}
		mux.ServeHTTP(w, r)
	})
}

type clientIPKey struct{}

// ClientIP is the address of the client that sent a request to the web
// socket, as Caddy recorded it. ok is false for requests that did not come
// through the web socket's handler (the admin socket, tests) or carry no
// usable address; callers then treat the client as unknown, never as
// trusted.
func ClientIP(r *http.Request) (ip netip.Addr, ok bool) {
	ip, ok = r.Context().Value(clientIPKey{}).(netip.Addr)
	return ip, ok
}

// forwardedFor takes the right-most entry of X-Forwarded-For: the hop Caddy
// appended. Caddy does not trust the client's own header (no
// trusted_proxies), so it is normally the only one; anything to the left
// would come from the client and is ignored.
func forwardedFor(r *http.Request) (netip.Addr, bool) {
	values := r.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return netip.Addr{}, false
	}
	last := values[len(values)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(last))
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not available yet"})
}
