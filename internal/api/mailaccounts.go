package api

import (
	"net/http"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// Organisations and app passwords (internal/panel/mailaccounts.go).
func (s *Server) mailAccountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mail/organisations", s.listOrganisations)
	mux.HandleFunc("POST /api/mail/organisations", s.createOrganisation)
	mux.HandleFunc("PATCH /api/mail/organisations/{id}", s.renameOrganisation)
	mux.HandleFunc("DELETE /api/mail/organisations/{id}", s.deleteOrganisation)
	mux.HandleFunc("PUT /api/mail/domains/{domain}/organisation", s.setMailDomainOrganisation)
	mux.HandleFunc("GET /api/mail/mailboxes/{id}/app-passwords", s.listAppPasswords)
	mux.HandleFunc("POST /api/mail/mailboxes/{id}/app-passwords", s.createAppPassword)
	mux.HandleFunc("DELETE /api/mail/mailboxes/{id}/app-passwords/{app}", s.revokeAppPassword)
}

func (s *Server) listOrganisations(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.Organisations(r.Context(), actorOf(r))
	answer(w, http.StatusOK, v, err)
}

type organisationBody struct {
	Name string `json:"name"`
}

func (s *Server) createOrganisation(w http.ResponseWriter, r *http.Request) {
	var body organisationBody
	if !decodeJSON(w, r, &body, `{"name": "Example Ltd"}`) {
		return
	}
	v, err := s.Panel.CreateOrganisation(r.Context(), actorOf(r), body.Name)
	answer(w, http.StatusCreated, v, err)
}

func (s *Server) renameOrganisation(w http.ResponseWriter, r *http.Request) {
	var body organisationBody
	if !decodeJSON(w, r, &body, `{"name": "Example Ltd"}`) {
		return
	}
	v, err := s.Panel.RenameOrganisation(r.Context(), actorOf(r), r.PathValue("id"), body.Name)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) deleteOrganisation(w http.ResponseWriter, r *http.Request) {
	if err := s.Panel.DeleteOrganisation(r.Context(), actorOf(r), r.PathValue("id")); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setMailDomainOrganisation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Organisation string `json:"organisation"`
	}
	if !decodeJSON(w, r, &body, `{"organisation": "org-0123456789abcdef"} (or "" to let the domain stand alone)`) {
		return
	}
	v, err := s.Panel.SetMailDomainOrganisation(r.Context(), actorOf(r), r.PathValue("domain"), body.Organisation)
	answer(w, http.StatusOK, v, err)
}

func (s *Server) listAppPasswords(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.Panel.AppPasswords(r.Context(), actorOf(r), id)
	answer(w, http.StatusOK, v, err)
}

// createAppPassword answers the app password with its token, the only
// time it is shown (no-store, like every JSON answer).
func (s *Server) createAppPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body, `{"name": "Phone"}`) {
		return
	}
	v, err := s.Panel.CreateAppPassword(r.Context(), actorOf(r), id, body.Name)
	if err != nil && panel.KindOf(err) == panel.NotSynced {
		// Saved: the token must reach the person even though the mail
		// server is not updated yet (it is retried).
		writeJSON(w, http.StatusCreated, v)
		return
	}
	answer(w, http.StatusCreated, v, err)
}

func (s *Server) revokeAppPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Panel.RevokeAppPassword(r.Context(), actorOf(r), id, r.PathValue("app")); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
