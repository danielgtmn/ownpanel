// Package timeouts holds the time limits of ownpaneld's HTTP server, so a
// slow or stalled client cannot hold a connection forever.
//
// The panel normally sits behind Caddy, which has its own limits towards
// clients (see internal/caddy). These are the second line: they also apply
// when the panel is reached directly.
package timeouts

import (
	"net/http"
	"time"
)

const (
	// ReadHeader bounds reading the request headers.
	ReadHeader = 10 * time.Second
	// Read bounds reading a whole request, headers and body. JSON bodies
	// are at most 1 MB; archive uploads get Long instead.
	Read = time.Minute
	// Write bounds a request from the end of reading its headers until the
	// response is written, so it includes the work of the handler (DNS
	// lookups, provider calls).
	Write = 5 * time.Minute
	// Idle is how long a kept-alive connection may wait for the next
	// request. It is longer than Caddy's 2 minutes for idle connections to
	// the panel, so Caddy, not the panel, closes them: a connection closed
	// by the panel while Caddy reuses it would fail the request.
	Idle = 3 * time.Minute

	// LongRead bounds reading the body of a request that carries an archive
	// (up to 512 MB: about 1.2 Mbit/s for an hour).
	LongRead = time.Hour
	// LongWrite leaves the panel 10 minutes after LongRead to unpack and
	// publish the archive and answer.
	LongWrite = LongRead + 10*time.Minute
)

// Server returns an http.Server with the limits above.
func Server(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: ReadHeader,
		ReadTimeout:       Read,
		WriteTimeout:      Write,
		IdleTimeout:       Idle,
	}
}

// Long gives requests to h the longer limits for archive uploads, counted
// from the moment h is called.
func Long(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Extend(w)
		h.ServeHTTP(w, r)
	})
}

// Extend applies LongRead and LongWrite to the request of w. It does
// nothing where the connection does not support deadlines (e.g. in tests).
func Extend(w http.ResponseWriter) {
	now := time.Now()
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(now.Add(LongRead))
	_ = rc.SetWriteDeadline(now.Add(LongWrite))
}
