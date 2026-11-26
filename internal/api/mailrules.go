package api

import (
	"net/http"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// Out-of-office and forwarding of a mailbox for its domain owner
// (internal/panel/mailrules.go).
func (s *Server) mailRulesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mail/mailboxes/{id}/rules", s.getMailboxRules)
	mux.HandleFunc("PUT /api/mail/mailboxes/{id}/out-of-office", s.setMailboxOutOfOffice)
	mux.HandleFunc("PUT /api/mail/mailboxes/{id}/forwarding", s.setMailboxForwarding)
	mux.HandleFunc("PUT /api/mail/mailboxes/{id}/may-forward", s.setMailboxMayForward)
}

func (s *Server) getMailboxRules(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.Panel.MailboxRulesOf(r.Context(), actorOf(r), id)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) setMailboxOutOfOffice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Enabled bool   `json:"enabled"`
		Subject string `json:"subject"`
		Text    string `json:"text"`
		Start   string `json:"start"`
		End     string `json:"end"`
		Zone    string `json:"zone"`
		Days    int    `json:"days"`
		Replace bool   `json:"replace"`
	}
	if !decodeJSON(w, r, &body, `{"enabled": true, "subject": "Out of office", "text": "…", "start": "2026-12-01", "end": "2026-12-24", "zone": "+0100", "days": 7, "replace": false}`) {
		return
	}
	v, err := s.Panel.SetMailboxOutOfOffice(r.Context(), actorOf(r), id, panel.OutOfOfficeInput{
		Enabled: body.Enabled, Subject: body.Subject, Text: body.Text, Start: body.Start, End: body.End,
		Zone: body.Zone, Days: body.Days, Replace: body.Replace,
	})
	answer(w, http.StatusOK, v, err)
}

func (s *Server) setMailboxForwarding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Enabled  bool   `json:"enabled"`
		To       string `json:"to"`
		KeepCopy bool   `json:"keep_copy"`
		Replace  bool   `json:"replace"`
	}
	if !decodeJSON(w, r, &body, `{"enabled": true, "to": "someone@example.com", "keep_copy": true, "replace": false}`) {
		return
	}
	v, err := s.Panel.SetMailboxForwarding(r.Context(), actorOf(r), id, panel.ForwardingInput{
		Enabled: body.Enabled, To: body.To, KeepCopy: body.KeepCopy, Replace: body.Replace,
	})
	answer(w, http.StatusOK, v, err)
}

func (s *Server) setMailboxMayForward(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Allowed bool `json:"allowed"`
	}
	if !decodeJSON(w, r, &body, `{"allowed": false}`) {
		return
	}
	if err := s.Panel.SetMailboxMayForward(r.Context(), actorOf(r), id, body.Allowed); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
