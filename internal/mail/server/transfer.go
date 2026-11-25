package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/transfer"
)

// maxRequestBytes bounds the JSON bodies of import requests.
const maxRequestBytes = 64 << 10

func (s *Server) transferRoutes(mux *http.ServeMux) {
	mb := config.PathMailbox + "{id}"
	mux.HandleFunc("POST "+mb+config.PathImportsCheck, s.handleImportCheck)
	mux.HandleFunc("POST "+mb+config.PathImports, s.handleImportStart)
	mux.HandleFunc("GET "+mb+config.PathImports, s.handleImportList)
	mux.HandleFunc("GET "+mb+config.PathImports+"/{job}", s.handleImportGet)
	mux.HandleFunc("DELETE "+mb+config.PathImports+"/{job}", s.handleImportCancel)
	mux.HandleFunc("POST "+mb+config.PathImports+"/{job}/start", s.handleImportResume)
	mux.HandleFunc("POST "+mb+config.PathUploads, s.handleUploadCreate)
	mux.HandleFunc("PUT "+mb+config.PathUploads+"/{job}", s.handleUploadPart)
	mux.HandleFunc("GET "+mb+config.PathExport, s.handleExport)
}

// transferError answers an error of the import and export code.
func (s *Server) transferError(w http.ResponseWriter, what string, err error) {
	var te *transfer.Error
	switch {
	case errors.As(err, &te) && te.Status != 0:
		http.Error(w, te.Msg, te.Status)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "cancelled", http.StatusServiceUnavailable)
	default:
		s.internalError(w, what, err)
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(v); err != nil {
		// The decoder's message names a position or type, never content.
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func jobID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("job"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "unknown import", http.StatusNotFound)
		return 0, false
	}
	return id, true
}

func (s *Server) handleImportCheck(w http.ResponseWriter, r *http.Request) {
	var src config.ImportSource
	if !decodeBody(w, r, &src) {
		return
	}
	c, err := s.imports.Check(r.Context(), r.PathValue("id"), src)
	if err != nil {
		s.transferError(w, "import check", err)
		return
	}
	writeJSON(w, c)
}

func (s *Server) handleImportStart(w http.ResponseWriter, r *http.Request) {
	var st config.ImportStart
	if !decodeBody(w, r, &st) {
		return
	}
	j, err := s.imports.StartIMAP(r.Context(), r.PathValue("id"), st)
	if err != nil {
		s.transferError(w, "import start", err)
		return
	}
	writeJSON(w, j)
}

func (s *Server) handleImportList(w http.ResponseWriter, r *http.Request) {
	js, err := s.imports.Jobs(r.Context(), r.PathValue("id"))
	if err != nil {
		s.transferError(w, "imports", err)
		return
	}
	writeJSON(w, js)
}

func (s *Server) handleImportGet(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	j, err := s.imports.Job(r.Context(), r.PathValue("id"), id)
	if err != nil {
		s.transferError(w, "import", err)
		return
	}
	writeJSON(w, j)
}

func (s *Server) handleImportCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	if err := s.imports.Cancel(r.Context(), r.PathValue("id"), id); err != nil {
		s.transferError(w, "import cancel", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleImportResume(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	var res config.ImportResume
	if !decodeBody(w, r, &res) {
		return
	}
	j, err := s.imports.Resume(r.Context(), r.PathValue("id"), id, res)
	if err != nil {
		s.transferError(w, "import start", err)
		return
	}
	writeJSON(w, j)
}

func (s *Server) handleUploadCreate(w http.ResponseWriter, r *http.Request) {
	var req config.UploadRequest
	if !decodeBody(w, r, &req) {
		return
	}
	j, err := s.imports.CreateUpload(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.transferError(w, "upload", err)
		return
	}
	writeJSON(w, j)
}

// idleReader pushes the read deadline forward before every read, so an
// upload fails only when no byte arrived for StreamIdleTimeout.
type idleReader struct {
	r  io.Reader
	rc *http.ResponseController
}

func (ir *idleReader) Read(b []byte) (int, error) {
	ir.rc.SetReadDeadline(time.Now().Add(config.StreamIdleTimeout))
	return ir.r.Read(b)
}

// idleWriter does the same for writes of an export.
type idleWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (iw *idleWriter) Write(b []byte) (int, error) {
	iw.rc.SetWriteDeadline(time.Now().Add(config.StreamIdleTimeout))
	return iw.w.Write(b)
}

func (s *Server) handleUploadPart(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		http.Error(w, "offset must be a non-negative number", http.StatusBadRequest)
		return
	}
	if r.ContentLength < 0 {
		http.Error(w, "the length of the part is required", http.StatusLengthRequired)
		return
	}
	if r.ContentLength > config.MaxUploadPart {
		http.Error(w, "a part may have at most 256 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	rc := http.NewResponseController(w)
	body := &idleReader{r: http.MaxBytesReader(w, r.Body, config.MaxUploadPart), rc: rc}
	received, err := s.imports.WritePart(r.Context(), r.PathValue("id"), id, offset, body, r.ContentLength)
	rc.SetReadDeadline(time.Time{})
	if err != nil {
		s.transferError(w, "upload", err)
		return
	}
	writeJSON(w, config.UploadProgress{Received: received})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	release, err := s.imports.BeginExport(id)
	if err != nil {
		s.transferError(w, "export", err)
		return
	}
	defer release()
	w.Header().Set("Content-Type", "application/zip")
	rc := http.NewResponseController(w)
	out := &idleWriter{w: w, rc: rc}
	if err := transfer.Export(r.Context(), s.store, id, out, time.Now()); err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Error("mail: export failed", "storage_id", id, "err", err)
		}
		// The zip is incomplete: cut the connection so the reader sees
		// an error instead of a short file.
		panic(http.ErrAbortHandler)
	}
	s.log.Info("mail: mailbox exported", "storage_id", id)
}
