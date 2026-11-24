package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) siteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sites", s.listSites)
	mux.HandleFunc("GET /api/sites/{name}", s.getSite)
	mux.HandleFunc("PUT /api/sites/{name}", s.putSite)
	mux.HandleFunc("DELETE /api/sites/{name}", s.deleteSite)
	mux.HandleFunc("POST /api/sites/{name}/deployments", s.deploy)
	mux.HandleFunc("POST /api/sites/{name}/rollback", s.rollback)
}

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.Panel.Sites(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sites)
}

func (s *Server) getSite(w http.ResponseWriter, r *http.Request) {
	site, err := s.Panel.Site(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, site)
}

func (s *Server) putSite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domains []string `json:"domains"`
	}
	if !decodeJSON(w, r, &body, `{"domains": ["example.com"]}`) {
		return
	}
	site, err := s.Panel.SaveSite(r.Context(), actorOf(r), r.PathValue("name"), body.Domains)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, site)
}

func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request) {
	if err := s.Panel.DeleteSite(r.Context(), actorOf(r), r.PathValue("name")); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deploy(w http.ResponseWriter, r *http.Request) {
	site, err := s.Panel.Publish(r.Context(), actorOf(r), r.PathValue("name"), http.MaxBytesReader(w, r.Body, MaxUploadBytes))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, site)
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Release string `json:"release"`
	}
	// An empty body means "previous release".
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBytes)).Decode(&body)
	site, err := s.Panel.MakeLive(r.Context(), actorOf(r), r.PathValue("name"), body.Release)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, site)
}
