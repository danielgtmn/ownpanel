package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/timeouts"
)

func (s *Server) buildRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/builds/status", s.getBuildStatus)
	mux.HandleFunc("POST /api/sites/{name}/builds", s.buildSite)
}

// getBuildStatus tells administrators whether this server can build sites.
func (s *Server) getBuildStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.BuildAvailability(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// buildSettingsBody is the "settings" part of a build request.
type buildSettingsBody struct {
	panel.BuildSettings
	// Live: make the new version live at once (default true).
	Live *bool `json:"live"`
}

// buildSite builds a site from an uploaded source archive and publishes the
// output. The body is multipart/form-data with two parts in this order:
// "settings" (JSON: command, output, image, env, secrets, live) and
// "source" (the source tree as .tar.gz). The answer comes when the build
// has ended.
func (s *Server) buildSite(w http.ResponseWriter, r *http.Request) {
	timeouts.Extend(w) // large archives on slow links, and the build itself
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes+maxJSONBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "the request must be multipart/form-data with the parts settings and source")
		return
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "settings" {
		writeError(w, http.StatusBadRequest, "the first part must be settings (JSON)")
		return
	}
	var settings buildSettingsBody
	dec := json.NewDecoder(io.LimitReader(part, maxJSONBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&settings); err != nil {
		// The decoder's message may quote the request (and so a secret).
		writeError(w, http.StatusBadRequest, `settings must be JSON like {"command": "npm ci && npm run build", "output": "dist", "image": "node-24", "env": {}, "secrets": {}, "live": true}`)
		return
	}
	part, err = mr.NextPart()
	if err != nil || part.FormName() != "source" {
		writeError(w, http.StatusBadRequest, "the second part must be source (the source tree as .tar.gz)")
		return
	}
	live := settings.Live == nil || *settings.Live
	res, err := s.Panel.BuildSiteFromUpload(r.Context(), actorOf(r), r.PathValue("name"), part, settings.BuildSettings, live)
	writeBuild(w, res, err)
}

// writeBuild answers a build. A build that ran answers its result, also
// when it failed (next to the error).
func writeBuild(w http.ResponseWriter, res panel.BuildResult, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, res)
	case res.Status != "" && (panel.CodeOf(err) != "" || panel.KindOf(err) == panel.NotSynced):
		status := http.StatusUnprocessableEntity
		switch panel.KindOf(err) {
		case panel.NotSynced:
			status = http.StatusBadGateway
		case panel.Conflict:
			status = http.StatusConflict
		}
		msg := err.Error()
		writeJSON(w, status, struct {
			Error string `json:"error"`
			Code  string `json:"code,omitempty"`
			panel.BuildResult
		}{strings.ToUpper(msg[:1]) + msg[1:], panel.CodeOf(err), res})
	default:
		writeFailure(w, err)
	}
}
