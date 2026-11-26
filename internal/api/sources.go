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

// Builds from the repository: settings, a proposal from the repository's
// files, "Build now", cancelling and the log of a run.
func (s *Server) siteBuildRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sites/{name}/build-settings", s.getBuildSettings)
	mux.HandleFunc("PUT /api/sites/{name}/build-settings", s.putBuildSettings)
	mux.HandleFunc("GET /api/sites/{name}/build-settings/proposal", s.proposeBuildSettings)
	mux.HandleFunc("POST /api/sites/{name}/runs", s.startSiteBuild)
	mux.HandleFunc("POST /api/sites/{name}/runs/{id}/cancel", s.cancelSiteRun)
	mux.HandleFunc("GET /api/sites/{name}/runs/{id}/log", s.siteRunLog)
}

func (s *Server) getBuildSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.GetSiteBuildSettings(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putBuildSettings(w http.ResponseWriter, r *http.Request) {
	var body panel.SiteBuildSettingsInput
	if !decodeJSON(w, r, &body, `{"command": "npm ci\nnpm run build", "output": "dist", "image": "node-24", "variables": [{"name": "API_KEY", "value": "…", "secret": true}]}`) {
		return
	}
	out, err := s.Panel.SetSiteBuildSettings(r.Context(), actorOf(r), r.PathValue("name"), body)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) proposeBuildSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.DetectBuildSettings(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) startSiteBuild(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.StartSiteBuild(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) cancelSiteRun(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.CancelSiteRun(r.Context(), actorOf(r), r.PathValue("name"), r.PathValue("id"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) siteRunLog(w http.ResponseWriter, r *http.Request) {
	// An offset that is not a number starts at the beginning.
	off, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	out, err := s.Panel.SiteRunLog(r.Context(), actorOf(r), r.PathValue("name"), r.PathValue("id"), off)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
