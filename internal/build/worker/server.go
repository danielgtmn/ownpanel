package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// maxSpecBytes bounds the JSON of a Spec (command and variables included).
const maxSpecBytes = 1 << 20

// Handler is the worker's side of the socket (see build.PathStatus for the
// endpoints). Whoever can open the socket may use it: the socket's folder
// lets in only the panel's user (ownpanel-build.service).
func (w *Worker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+build.PathStatus, func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, w.Status())
	})
	mux.HandleFunc("GET "+build.PathBuilds, func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, w.List())
	})
	mux.HandleFunc("POST "+build.PathBuilds, func(rw http.ResponseWriter, r *http.Request) {
		var spec build.Spec
		dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxSpecBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&spec); err != nil {
			// The decoder's message may quote the request; it is not repeated.
			writeError(rw, errorf(statusBadRequest, "the build request is not valid JSON of a build"))
			return
		}
		b, err := w.Create(spec)
		reply(rw, b, err)
	})
	mux.HandleFunc("GET "+build.PathBuilds+"/{id}", w.withID(func(rw http.ResponseWriter, r *http.Request, id uint64) {
		b, err := w.Get(id)
		reply(rw, b, err)
	}))
	mux.HandleFunc("DELETE "+build.PathBuilds+"/{id}", w.withID(func(rw http.ResponseWriter, r *http.Request, id uint64) {
		if err := w.Remove(id); err != nil {
			writeError(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("PUT "+build.PathBuilds+"/{id}/source", w.withID(func(rw http.ResponseWriter, r *http.Request, id uint64) {
		strip := 0
		if v := r.URL.Query().Get("strip"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeError(rw, errorf(statusBadRequest, "strip must be a number"))
				return
			}
			strip = n
		}
		b, err := w.Source(r.Context(), id, r.Body, strip)
		reply(rw, b, err)
	}))
	mux.HandleFunc("POST "+build.PathBuilds+"/{id}/start", w.withID(func(rw http.ResponseWriter, r *http.Request, id uint64) {
		b, err := w.Start(id)
		reply(rw, b, err)
	}))
	mux.HandleFunc("POST "+build.PathBuilds+"/{id}/cancel", w.withID(func(rw http.ResponseWriter, r *http.Request, id uint64) {
		b, err := w.Cancel(id)
		reply(rw, b, err)
	}))
	mux.HandleFunc("GET "+build.PathBuilds+"/{id}/log", w.withID(func(rw http.ResponseWriter, r *http.Request, id uint64) {
		q := r.URL.Query()
		var offset int64
		if v := q.Get("offset"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				writeError(rw, errorf(statusBadRequest, "offset must be a number"))
				return
			}
			offset = n
		}
		if _, err := w.Get(id); err != nil {
			writeError(rw, err)
			return
		}
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		flusher, _ := rw.(http.Flusher)
		flush := func() {
			if flusher != nil {
				flusher.Flush()
			}
		}
		// A followed log may take as long as the build: no write deadline.
		_ = http.NewResponseController(rw).SetWriteDeadline(time.Time{})
		if err := w.Log(r.Context(), id, offset, q.Get("follow") == "1", rw, flush); err != nil {
			w.log.Debug("log stream", "build", id, "err", err)
		}
	}))
	mux.HandleFunc("GET "+build.PathBuilds+"/{id}/output", w.withID(func(rw http.ResponseWriter, r *http.Request, id uint64) {
		f, err := w.Output(id)
		if err != nil {
			writeError(rw, err)
			return
		}
		defer f.Close()
		rw.Header().Set("Content-Type", "application/gzip")
		if st, err := f.Stat(); err == nil {
			rw.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		}
		_ = http.NewResponseController(rw).SetWriteDeadline(time.Time{})
		_, _ = io.Copy(rw, f)
	}))
	mux.HandleFunc("GET "+build.PathCaches, func(rw http.ResponseWriter, r *http.Request) {
		cs, err := w.Caches()
		reply(rw, cs, err)
	})
	mux.HandleFunc("DELETE "+build.PathCaches+"/{site}", func(rw http.ResponseWriter, r *http.Request) {
		if err := w.ClearCache(r.Context(), r.PathValue("site")); err != nil {
			writeError(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (w *Worker) withID(h func(http.ResponseWriter, *http.Request, uint64)) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
		if err != nil || id == 0 {
			writeError(rw, errorf(statusBadRequest, "the build number is not valid"))
			return
		}
		h(rw, r, id)
	}
}

func reply(rw http.ResponseWriter, v any, err error) {
	if err != nil {
		writeError(rw, err)
		return
	}
	writeJSON(rw, v)
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(v)
}

// writeError answers with the message of an apiError; anything else is an
// internal error, logged by the caller's logger, never sent: it may name
// the worker's own paths.
func writeError(rw http.ResponseWriter, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		ae = &apiError{http.StatusInternalServerError, "the build service failed; see its log"}
		slog.Default().Error("request failed", "err", err)
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.WriteHeader(ae.status)
	_, _ = io.WriteString(rw, ae.msg)
}

// Listen opens the socket at path, mode 0660: its folder decides who gets
// in. A stale socket of an earlier run is replaced; anything else there is
// an error.
func Listen(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("%s is in use: is the build service already running?", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve runs the worker and answers on ln until ctx ends.
func Serve(ctx context.Context, w *Worker, ln net.Listener) error {
	srv := &http.Server{
		Handler:           w.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
	}
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shut); err != nil {
			srv.Close() // followed logs and downloads are cut off
		}
	}()
	err := srv.Serve(ln)
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
