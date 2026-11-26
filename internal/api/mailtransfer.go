package api

import (
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/timeouts"
)

// Import and export of a mailbox. Request bodies may hold the password of
// another mail account: they are decoded once and passed on, never logged
// or repeated in an answer.

// exportIdle is how long the download of an export may stall before the
// connection is given up; the write deadline moves forward per chunk.
const exportIdle = 2 * time.Minute

func (s *Server) mailTransferRoutes(mux *http.ServeMux) {
	mb := "/api/mail/domains/{domain}/mailboxes/{local_part}"
	mux.HandleFunc("POST "+mb+"/imports/check", s.checkMailImport)
	mux.HandleFunc("POST "+mb+"/imports", s.startMailImport)
	mux.HandleFunc("GET "+mb+"/imports", s.listMailImports)
	mux.HandleFunc("GET "+mb+"/imports/{id}", s.getMailImport)
	mux.HandleFunc("DELETE "+mb+"/imports/{id}", s.cancelMailImport)
	mux.HandleFunc("POST "+mb+"/imports/{id}/start", s.resumeMailImport)
	mux.HandleFunc("POST "+mb+"/uploads", s.createMailUpload)
	mux.HandleFunc("PUT "+mb+"/uploads/{id}", s.uploadMailPart)
	mux.HandleFunc("GET "+mb+"/export", s.exportMailbox)
}

// mailboxOfPath resolves {domain} and {local_part}; the panel decides
// whether the actor may see the mailbox.
func (s *Server) mailboxOfPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := s.Panel.MailboxByAddress(r.Context(), actorOf(r), r.PathValue("domain"), r.PathValue("local_part"))
	if err != nil {
		writeFailure(w, err)
		return "", false
	}
	return id, true
}

func jobOfPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "import not found")
		return 0, false
	}
	return id, true
}

// sourceBody is another mail account as the API takes it.
type sourceBody struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (b sourceBody) source() config.ImportSource {
	return config.ImportSource{Host: b.Host, Port: b.Port, Username: b.Username, Password: b.Password}
}

const sourceExample = `{"host": "imap.example.org", "port": 993, "username": "me@example.org", "password": "…"}`

func (s *Server) checkMailImport(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mailboxOfPath(w, r)
	if !ok {
		return
	}
	var body sourceBody
	if !decodeJSON(w, r, &body, sourceExample) {
		return
	}
	v, err := s.Panel.CheckMailImport(r.Context(), actorOf(r), id, body.source())
	answer(w, http.StatusOK, v, err)
}

func (s *Server) startMailImport(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mailboxOfPath(w, r)
	if !ok {
		return
	}
	var body struct {
		sourceBody
		Choices config.ImportChoices `json:"choices"`
	}
	if !decodeJSON(w, r, &body, `{"host": "imap.example.org", "port": 993, "username": "me@example.org", "password": "…", "choices": {"folders": ["INBOX"]}}`) {
		return
	}
	v, err := s.Panel.StartMailImport(r.Context(), actorOf(r), id, config.ImportStart{Source: body.source(), Choices: body.Choices})
	answer(w, http.StatusCreated, v, err)
}

func (s *Server) listMailImports(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mailboxOfPath(w, r)
	if !ok {
		return
	}
	v, err := s.Panel.MailImports(r.Context(), actorOf(r), id)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) getMailImport(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mailboxOfPath(w, r)
	if !ok {
		return
	}
	job, ok := jobOfPath(w, r)
	if !ok {
		return
	}
	v, err := s.Panel.MailImportOf(r.Context(), actorOf(r), id, job)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) cancelMailImport(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mailboxOfPath(w, r)
	if !ok {
		return
	}
	job, ok := jobOfPath(w, r)
	if !ok {
		return
	}
	if err := s.Panel.CancelMailImport(r.Context(), actorOf(r), id, job); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resumeMailImport(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mailboxOfPath(w, r)
	if !ok {
		return
	}
	job, ok := jobOfPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Choices  config.ImportChoices `json:"choices"`
		Password string               `json:"password"`
	}
	// An empty body starts a file with the folders chosen by default.
	if r.ContentLength != 0 && !decodeJSON(w, r, &body, `{"choices": {"folders": ["INBOX"]}} or {"password": "…"}`) {
		return
	}
	v, err := s.Panel.ResumeMailImport(r.Context(), actorOf(r), id, job, config.ImportResume{Choices: body.Choices, Password: body.Password})
	answer(w, http.StatusOK, v, err)
}

func (s *Server) createMailUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mailboxOfPath(w, r)
	if !ok {
		return
	}
	var body config.UploadRequest
	if !decodeJSON(w, r, &body, `{"name": "mail.mbox", "size": 123456}`) {
		return
	}
	v, err := s.Panel.CreateMailUpload(r.Context(), actorOf(r), id, body)
	answer(w, http.StatusCreated, v, err)
}

// uploadMailPart streams one part (at most 256 MiB) to the mail service.
// It gets the long time limits of archive uploads; a part fits them on
// slow links.
func (s *Server) uploadMailPart(w http.ResponseWriter, r *http.Request) {
	timeouts.Extend(w)
	id, ok := s.mailboxOfPath(w, r)
	if !ok {
		return
	}
	job, ok := jobOfPath(w, r)
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, "add ?offset= with the number of bytes the server has received so far")
		return
	}
	switch {
	case r.ContentLength < 0:
		writeError(w, http.StatusLengthRequired, "the length of the part is required")
		return
	case r.ContentLength > config.MaxUploadPart:
		writeError(w, http.StatusRequestEntityTooLarge, "a part may have at most 256 MB")
		return
	}
	body := http.MaxBytesReader(w, r.Body, config.MaxUploadPart)
	n, err := s.Panel.UploadMailPart(r.Context(), actorOf(r), id, job, offset, body, r.ContentLength)
	answer(w, http.StatusOK, config.UploadProgress{Received: n}, err)
}

// idleDeadlineWriter pushes the write deadline forward before every write,
// so a long download fails only when the client stops reading.
type idleDeadlineWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (d idleDeadlineWriter) Write(b []byte) (int, error) {
	_ = d.rc.SetWriteDeadline(time.Now().Add(exportIdle))
	return d.w.Write(b)
}

func (s *Server) exportMailbox(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mailboxOfPath(w, r)
	if !ok {
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(exportIdle))
	ex, err := s.Panel.ExportMailbox(r.Context(), actorOf(r), id)
	if err != nil {
		writeFailure(w, err)
		return
	}
	defer ex.Body.Close()
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": ex.FileName}))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(idleDeadlineWriter{w: w, rc: rc}, ex.Body); err != nil {
		if r.Context().Err() == nil {
			slog.Warn("mail export stopped", "err", err)
		}
		// The zip is incomplete: cut the connection so the browser sees a
		// failed download instead of a short file.
		panic(http.ErrAbortHandler)
	}
}
