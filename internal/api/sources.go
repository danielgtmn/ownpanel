package api

import (
	"net/http"
	"strconv"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// A site's source: the repository and branch its code comes from.
func (s *Server) sourceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sites/{name}/source", s.getSiteSource)
	mux.HandleFunc("PUT /api/sites/{name}/source", s.putSiteSource)
	mux.HandleFunc("DELETE /api/sites/{name}/source", s.deleteSiteSource)
	mux.HandleFunc("GET /api/sites/{name}/source/repositories", s.sourceRepositories)
	mux.HandleFunc("GET /api/sites/{name}/source/branches", s.sourceBranches)
	mux.HandleFunc("GET /api/sites/{name}/runs", s.siteRuns)
}

func (s *Server) siteRuns(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.ListSiteRuns(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getSiteSource(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.GetSiteSource(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putSiteSource(w http.ResponseWriter, r *http.Request) {
	var body panel.SiteSourceInput
	if !decodeJSON(w, r, &body, `{"installation_id": 1, "repository_id": 2, "branch": "main", "publish_on_push": true}`) {
		return
	}
	out, err := s.Panel.SetSiteSource(r.Context(), actorOf(r), r.PathValue("name"), body)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteSiteSource(w http.ResponseWriter, r *http.Request) {
	if err := s.Panel.RemoveSiteSource(r.Context(), actorOf(r), r.PathValue("name")); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sourceRepositories(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.LinkableRepositories(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) sourceBranches(w http.ResponseWriter, r *http.Request) {
	// Ids that are not numbers are passed as 0, which nothing matches.
	inst, _ := strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64)
	repo, _ := strconv.ParseInt(r.URL.Query().Get("repository_id"), 10, 64)
	out, err := s.Panel.SourceBranches(r.Context(), actorOf(r), r.PathValue("name"), inst, repo)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
