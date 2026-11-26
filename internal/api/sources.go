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
	// Kind git: any Git repository by address (#111).
	mux.HandleFunc("POST /api/sites/{name}/source/git/inspect", s.inspectGitRepository)
	mux.HandleFunc("PUT /api/sites/{name}/source/git", s.putGitSource)
	mux.HandleFunc("POST /api/sites/{name}/source/git/check", s.checkGitSource)
	mux.HandleFunc("POST /api/sites/{name}/source/git/host-key", s.acceptGitHostKey)
	mux.HandleFunc("POST /api/sites/{name}/source/git/deploy-key", s.replaceGitDeployKey)
	mux.HandleFunc("POST /api/sites/{name}/source/git/notify-secret", s.replaceGitNotifySecret)
}

func (s *Server) inspectGitRepository(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repository string `json:"repository"`
	}
	if !decodeJSON(w, r, &body, `{"repository": "git@codeberg.org:owner/site.git"}`) {
		return
	}
	out, err := s.Panel.InspectGitRepository(r.Context(), actorOf(r), r.PathValue("name"), body.Repository)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putGitSource(w http.ResponseWriter, r *http.Request) {
	var body panel.GitSourceInput
	if !decodeJSON(w, r, &body, `{"repository": "git@codeberg.org:owner/site.git", "branch": "main", "publish_on_push": true, "host_key": "SHA256:…"}`) {
		return
	}
	out, err := s.Panel.SetGitSiteSource(r.Context(), actorOf(r), r.PathValue("name"), body)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) checkGitSource(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.CheckGitSiteSource(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) acceptGitHostKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		HostKey string `json:"host_key"`
	}
	if !decodeJSON(w, r, &body, `{"host_key": "SHA256:…"}`) {
		return
	}
	out, err := s.Panel.AcceptGitHostKey(r.Context(), actorOf(r), r.PathValue("name"), body.HostKey)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) replaceGitDeployKey(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.ReplaceGitDeployKey(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) replaceGitNotifySecret(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.ReplaceGitNotifySecret(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
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
