package api

import (
	"net/http"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

func (s *Server) redirectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/redirects", s.listRedirects)
	mux.HandleFunc("POST /api/redirects", s.createRedirect)
	mux.HandleFunc("PUT /api/redirects/{id}", s.updateRedirect)
	mux.HandleFunc("DELETE /api/redirects/{id}", s.deleteRedirect)
	mux.HandleFunc("GET /api/sites/{name}/redirects", s.siteRedirects)
	mux.HandleFunc("GET /api/dns/domain-status", s.domainStatus)
	mux.HandleFunc("POST /api/dns/point", s.pointAnyDomain)
	mux.HandleFunc("POST /api/dns/connections/{id}/zones", s.createDNSZone)
	mux.HandleFunc("DELETE /api/dns/connections/{id}/zones/{zone}", s.deleteDNSZone)
}

const redirectExample = `{"source_domain": "www.example.com", "source_path": "", "target": "https://example.com", "code": 301, "keep_path": true, "keep_query": true}`

func decodeRedirect(w http.ResponseWriter, r *http.Request) (panel.RedirectInput, bool) {
	var body struct {
		SourceDomain string `json:"source_domain"`
		SourcePath   string `json:"source_path"`
		Target       string `json:"target"`
		Code         int    `json:"code"`
		KeepPath     bool   `json:"keep_path"`
		KeepQuery    bool   `json:"keep_query"`
	}
	if !decodeJSON(w, r, &body, redirectExample) {
		return panel.RedirectInput{}, false
	}
	return panel.RedirectInput(body), true
}

func (s *Server) listRedirects(w http.ResponseWriter, r *http.Request) {
	list, err := s.Panel.Redirects(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) siteRedirects(w http.ResponseWriter, r *http.Request) {
	list, err := s.Panel.SiteRedirects(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createRedirect(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeRedirect(w, r)
	if !ok {
		return
	}
	red, err := s.Panel.CreateRedirect(r.Context(), actorOf(r), in)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, red)
}

func (s *Server) updateRedirect(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "redirect")
	if !ok {
		return
	}
	in, ok := decodeRedirect(w, r)
	if !ok {
		return
	}
	red, err := s.Panel.UpdateRedirect(r.Context(), actorOf(r), id, in)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, red)
}

func (s *Server) deleteRedirect(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "redirect")
	if !ok {
		return
	}
	if err := s.Panel.DeleteRedirect(r.Context(), actorOf(r), id); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) domainStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Panel.DomainStatusOf(r.Context(), actorOf(r), r.URL.Query().Get("domain"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) pointAnyDomain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain string `json:"domain"`
	}
	if !decodeJSON(w, r, &body, `{"domain": "www.example.com"}`) {
		return
	}
	res, err := s.Panel.PointDomainOf(r.Context(), actorOf(r), body.Domain)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) createDNSZone(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "DNS connection")
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body, `{"name": "example.com"}`) {
		return
	}
	z, err := s.Panel.CreateDNSZone(r.Context(), actorOf(r), id, body.Name)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, z)
}

func (s *Server) deleteDNSZone(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "DNS connection")
	if !ok {
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
	}
	if !decodeJSON(w, r, &body, `{"confirm": "example.com"}`) {
		return
	}
	if err := s.Panel.DeleteDNSZone(r.Context(), actorOf(r), id, r.PathValue("zone"), body.Confirm); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
