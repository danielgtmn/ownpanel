package api

import (
	"encoding/json"
	"net/http"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

func (s *Server) settingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("GET /api/settings/checks", s.settingsChecks)
	mux.HandleFunc("POST /api/settings/complete", s.completeSetup)
	mux.HandleFunc("POST /api/settings/preview-domain/point", s.pointPreviewDomain)
}

func (s *Server) pointPreviewDomain(w http.ResponseWriter, r *http.Request) {
	res, err := s.Panel.PointPreviewDomain(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.Settings(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	// A field that is missing (or null) stays unchanged; "" or [] clears it.
	var body struct {
		PanelDomain       *string          `json:"panel_domain"`
		ServerIPs         *json.RawMessage `json:"server_ips"`
		ACMEEmail         *string          `json:"acme_email"`
		MailHost          *string          `json:"mail_host"`
		PostmasterAddress *string          `json:"postmaster_address"`
		PreviewDomain     *string          `json:"preview_domain"`
		// null or missing: unchanged; [] turns blocklists off.
		MailBlocklists        *json.RawMessage `json:"mail_blocklists"`
		MailBlocklistsDefault *bool            `json:"mail_blocklists_default"`
		MailJunkScore         *float64         `json:"mail_junk_score"`
		MailGreylisting       *bool            `json:"mail_greylisting"`
	}
	example := `{"panel_domain": "panel.example.com", "server_ips": ["203.0.113.5"], "acme_email": "you@example.com", "mail_host": "mail.example.com", "postmaster_address": "you@example.com"}`
	if !decodeJSON(w, r, &body, example) {
		return
	}
	u := panel.SettingsUpdate{PanelDomain: body.PanelDomain, ACMEEmail: body.ACMEEmail, MailHost: body.MailHost, PostmasterAddress: body.PostmasterAddress, PreviewDomain: body.PreviewDomain,
		MailBlocklistsDefault: body.MailBlocklistsDefault, MailJunkScore: body.MailJunkScore, MailGreylisting: body.MailGreylisting}
	if body.MailBlocklists != nil && string(*body.MailBlocklists) != "null" {
		var zones []string
		if err := json.Unmarshal(*body.MailBlocklists, &zones); err != nil {
			writeError(w, http.StatusBadRequest, `mail_blocklists must be a list of names like ["zen.spamhaus.org"]`)
			return
		}
		if zones == nil {
			zones = []string{}
		}
		u.MailBlocklists = &zones
	}
	if body.ServerIPs != nil && string(*body.ServerIPs) != "null" {
		var ips []string
		if err := json.Unmarshal(*body.ServerIPs, &ips); err != nil {
			writeError(w, http.StatusBadRequest, "request body must be JSON like "+example)
			return
		}
		if ips == nil {
			ips = []string{}
		}
		u.ServerIPs = &ips
	}
	v, err := s.Panel.UpdateSettings(r.Context(), actorOf(r), u)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) settingsChecks(w http.ResponseWriter, r *http.Request) {
	c, err := s.Panel.SettingsChecks(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) completeSetup(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.CompleteSetup(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
