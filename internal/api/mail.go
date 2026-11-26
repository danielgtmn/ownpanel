package api

import (
	"net/http"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

func (s *Server) mailRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mail/status", s.mailStatus)
	mux.HandleFunc("POST /api/mail/checks/outbound", s.mailOutbound)
	mux.HandleFunc("GET /api/mail/health", s.mailHealth)
	mux.HandleFunc("POST /api/mail/host-spf", s.setUpMailHostSPF)
	mux.HandleFunc("GET /api/mail/domains", s.listMailDomains)
	mux.HandleFunc("POST /api/mail/domains", s.enableMailDomain)
	mux.HandleFunc("GET /api/mail/domains/{domain}", s.getMailDomain)
	mux.HandleFunc("DELETE /api/mail/domains/{domain}", s.deleteMailDomain)
	mux.HandleFunc("POST /api/mail/domains/{domain}/check", s.checkMailDomain)
	mux.HandleFunc("POST /api/mail/domains/{domain}/activate", s.activateMailDomain)
	mux.HandleFunc("GET /api/mail/domains/{domain}/records", s.mailRecords)
	mux.HandleFunc("POST /api/mail/domains/{domain}/records", s.setUpMailRecords)
	mux.HandleFunc("GET /api/mail/domains/{domain}/mailboxes", s.listMailboxes)
	mux.HandleFunc("POST /api/mail/domains/{domain}/mailboxes", s.createMailbox)
	mux.HandleFunc("GET /api/mail/domains/{domain}/aliases", s.listMailAliases)
	mux.HandleFunc("POST /api/mail/domains/{domain}/aliases", s.createMailAlias)
	mux.HandleFunc("GET /api/mail/mailboxes", s.listMailboxes)
	mux.HandleFunc("GET /api/mail/mailboxes/{id}", s.getMailbox)
	mux.HandleFunc("PATCH /api/mail/mailboxes/{id}", s.setMailboxLimits)
	mux.HandleFunc("DELETE /api/mail/mailboxes/{id}", s.deleteMailbox)
	mux.HandleFunc("POST /api/mail/mailboxes/{id}/password", s.setMailboxPassword)
	mux.HandleFunc("POST /api/mail/mailboxes/{id}/unblock", s.unblockMailbox)
	mux.HandleFunc("GET /api/mail/mailboxes/{id}/apple-profile", s.appleMailProfile)
	mux.HandleFunc("PUT /api/mail/mailboxes/{id}/kind", s.setMailboxKind)
	mux.HandleFunc("PUT /api/mail/mailboxes/{id}/keep-sent-copy", s.setMailboxKeepSentCopy)
	mux.HandleFunc("PUT /api/mail/mailboxes/{id}/grants", s.setMailboxGrant)
	mux.HandleFunc("DELETE /api/mail/mailboxes/{id}/grants/{grantee}", s.removeMailboxGrant)
	mux.HandleFunc("GET /api/mail/aliases", s.listMailAliases)
	mux.HandleFunc("GET /api/mail/aliases/{id}", s.getMailAlias)
	mux.HandleFunc("PUT /api/mail/aliases/{id}", s.updateMailAlias)
	mux.HandleFunc("DELETE /api/mail/aliases/{id}", s.deleteMailAlias)
}

// answer writes v with status, or the failure (a change that was saved but
// not applied yet is a NotSynced failure, 502, like in the other areas).
func answer(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, status, v)
}

func (s *Server) mailStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.MailStatus(r.Context(), actorOf(r))
	answer(w, http.StatusOK, v, err)
}

func (s *Server) mailOutbound(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.CheckMailOutbound(r.Context(), actorOf(r))
	answer(w, http.StatusOK, v, err)
}

func (s *Server) mailHealth(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.MailHealth(r.Context(), actorOf(r))
	answer(w, http.StatusOK, v, err)
}

func (s *Server) mailRecords(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.MailRecords(r.Context(), actorOf(r), r.PathValue("domain"))
	answer(w, http.StatusOK, v, err)
}

// setUpMailRecords returns the planned changes; with confirm (or when no
// change needs it) they are written.
func (s *Server) setUpMailRecords(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm bool `json:"confirm"`
	}
	// An empty body is {"confirm": false}.
	if r.ContentLength != 0 && !decodeJSON(w, r, &body, `{"confirm": false}`) {
		return
	}
	v, err := s.Panel.SetUpMailRecords(r.Context(), actorOf(r), r.PathValue("domain"), body.Confirm)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) listMailDomains(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.MailDomains(r.Context(), actorOf(r))
	answer(w, http.StatusOK, v, err)
}

func (s *Server) enableMailDomain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain string `json:"domain"`
	}
	if !decodeJSON(w, r, &body, `{"domain": "example.com"}`) {
		return
	}
	v, err := s.Panel.EnableMailDomain(r.Context(), actorOf(r), body.Domain)
	answer(w, http.StatusCreated, v, err)
}

func (s *Server) getMailDomain(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.MailDomainOf(r.Context(), actorOf(r), r.PathValue("domain"))
	answer(w, http.StatusOK, v, err)
}

func (s *Server) checkMailDomain(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.CheckMailDomain(r.Context(), actorOf(r), r.PathValue("domain"))
	answer(w, http.StatusOK, v, err)
}

func (s *Server) activateMailDomain(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.ActivateMailDomain(r.Context(), actorOf(r), r.PathValue("domain"))
	answer(w, http.StatusOK, v, err)
}

func (s *Server) deleteMailDomain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if !decodeJSON(w, r, &body, `{"confirm": "example.com"}`) {
		return
	}
	if err := s.Panel.DeleteMailDomain(r.Context(), actorOf(r), r.PathValue("domain"), body.Confirm); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listMailboxes serves both /api/mail/mailboxes (every visible mailbox) and
// /api/mail/domains/{domain}/mailboxes.
func (s *Server) listMailboxes(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.Mailboxes(r.Context(), actorOf(r), r.PathValue("domain"))
	answer(w, http.StatusOK, v, err)
}

const mailboxExample = `{"local_part": "info", "password": "a long password", "display_name": "Example Info", "quota_bytes": 1073741824, "send_per_hour": 100, "send_per_day": 500}`

func (s *Server) createMailbox(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LocalPart    string `json:"local_part"`
		Kind         string `json:"kind"`
		KeepSentCopy bool   `json:"keep_sent_copy"`
		Password     string `json:"password"`
		DisplayName  string `json:"display_name"`
		QuotaBytes   *int64 `json:"quota_bytes"`
		SendPerHour  *int   `json:"send_per_hour"`
		SendPerDay   *int   `json:"send_per_day"`
	}
	if !decodeJSON(w, r, &body, mailboxExample) {
		return
	}
	v, err := s.Panel.CreateMailbox(r.Context(), actorOf(r), r.PathValue("domain"), panel.MailboxInput{
		LocalPart: body.LocalPart, Kind: body.Kind, KeepSentCopy: body.KeepSentCopy, Password: body.Password, DisplayName: body.DisplayName,
		QuotaBytes: body.QuotaBytes, SendPerHour: body.SendPerHour, SendPerDay: body.SendPerDay,
	})
	answer(w, http.StatusCreated, v, err)
}

func (s *Server) setMailboxKind(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Kind     string `json:"kind"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body, `{"kind": "shared"}`) {
		return
	}
	v, err := s.Panel.SetMailboxKind(r.Context(), actorOf(r), id, body.Kind, body.Password)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) setMailboxKeepSentCopy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		KeepSentCopy *bool `json:"keep_sent_copy"`
	}
	if !decodeJSON(w, r, &body, `{"keep_sent_copy": true}`) {
		return
	}
	if body.KeepSentCopy == nil {
		writeError(w, http.StatusBadRequest, `say whether copies are kept: {"keep_sent_copy": true}`)
		return
	}
	v, err := s.Panel.SetMailboxKeepSentCopy(r.Context(), actorOf(r), id, *body.KeepSentCopy)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) setMailboxGrant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Address string   `json:"address"`
		Rights  []string `json:"rights"`
	}
	if !decodeJSON(w, r, &body, `{"address": "anna@example.com", "rights": ["read", "write", "send-as"]}`) {
		return
	}
	v, err := s.Panel.SetMailboxGrant(r.Context(), actorOf(r), id, body.Address, body.Rights)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) removeMailboxGrant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.Panel.RemoveMailboxGrant(r.Context(), actorOf(r), id, r.PathValue("grantee"))
	answer(w, http.StatusOK, v, err)
}

func (s *Server) getMailbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.Panel.MailboxOf(r.Context(), actorOf(r), id)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) setMailboxLimits(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// A missing field stays as it is.
	var body struct {
		QuotaBytes       *int64  `json:"quota_bytes"`
		SendPerHour      *int    `json:"send_per_hour"`
		SendPerDay       *int    `json:"send_per_day"`
		DisplayName      *string `json:"display_name"`
		AppPasswordsOnly *bool   `json:"app_passwords_only"`
	}
	if !decodeJSON(w, r, &body, `{"quota_bytes": 1073741824, "send_per_hour": 50, "send_per_day": 200, "display_name": "Anna Example", "app_passwords_only": false}`) {
		return
	}
	ctx, a := r.Context(), actorOf(r)
	var v panel.Mailbox
	var err error
	if body.DisplayName != nil || body.AppPasswordsOnly != nil {
		v, err = s.Panel.SetMailboxAccount(ctx, a, id, panel.MailboxAccount{DisplayName: body.DisplayName, AppPasswordsOnly: body.AppPasswordsOnly})
		if err != nil && panel.KindOf(err) != panel.NotSynced {
			writeFailure(w, err)
			return
		}
	}
	if body.QuotaBytes != nil || body.SendPerHour != nil || body.SendPerDay != nil || (body.DisplayName == nil && body.AppPasswordsOnly == nil) {
		var lerr error
		v, lerr = s.Panel.SetMailboxLimits(ctx, a, id, panel.MailboxLimits{QuotaBytes: body.QuotaBytes, SendPerHour: body.SendPerHour, SendPerDay: body.SendPerDay})
		if lerr != nil {
			err = lerr
		}
	}
	answer(w, http.StatusOK, v, err)
}

func (s *Server) setMailboxPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body, `{"password": "a long password"}`) {
		return
	}
	if err := s.Panel.SetMailboxPassword(r.Context(), actorOf(r), id, body.Password); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) unblockMailbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Panel.UnblockMailbox(r.Context(), actorOf(r), id); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteMailbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Panel.DeleteMailbox(r.Context(), actorOf(r), id); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listMailAliases serves both /api/mail/aliases and
// /api/mail/domains/{domain}/aliases.
func (s *Server) listMailAliases(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.MailAliases(r.Context(), actorOf(r), r.PathValue("domain"))
	answer(w, http.StatusOK, v, err)
}

const aliasExample = `{"local_part": "sales", "targets": ["info@example.com", "someone@example.org"], "sender_policy": "anyone", "hide_from_directory": false}`

type aliasBody struct {
	LocalPart string   `json:"local_part"`
	Targets   []string `json:"targets"`
	// A missing field keeps the current value (anyone, shown on create).
	SenderPolicy      *string `json:"sender_policy"`
	HideFromDirectory *bool   `json:"hide_from_directory"`
}

// group returns the group settings of the body over cur, or nil when the
// body has none.
func (b aliasBody) group(cur panel.AliasGroup) []panel.AliasGroup {
	if b.SenderPolicy == nil && b.HideFromDirectory == nil {
		return nil
	}
	if b.SenderPolicy != nil {
		cur.SenderPolicy = *b.SenderPolicy
	}
	if b.HideFromDirectory != nil {
		cur.HideFromDirectory = *b.HideFromDirectory
	}
	return []panel.AliasGroup{cur}
}

func (s *Server) createMailAlias(w http.ResponseWriter, r *http.Request) {
	var body aliasBody
	if !decodeJSON(w, r, &body, aliasExample) {
		return
	}
	v, err := s.Panel.CreateMailAlias(r.Context(), actorOf(r), r.PathValue("domain"), body.LocalPart, body.Targets, body.group(panel.AliasGroup{})...)
	answer(w, http.StatusCreated, v, err)
}

func (s *Server) getMailAlias(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.Panel.MailAliasOf(r.Context(), actorOf(r), id)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) updateMailAlias(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body aliasBody
	if !decodeJSON(w, r, &body, aliasExample) {
		return
	}
	var group []panel.AliasGroup
	if body.SenderPolicy == nil || body.HideFromDirectory == nil {
		// Only one of them given: the other stays as it is.
		if cur, err := s.Panel.MailAliasOf(r.Context(), actorOf(r), id); err == nil {
			group = body.group(panel.AliasGroup{SenderPolicy: cur.SenderPolicy, HideFromDirectory: cur.HideFromDirectory})
		}
	} else {
		group = body.group(panel.AliasGroup{})
	}
	v, err := s.Panel.UpdateMailAlias(r.Context(), actorOf(r), id, body.LocalPart, body.Targets, group...)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) deleteMailAlias(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Panel.DeleteMailAlias(r.Context(), actorOf(r), id); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
