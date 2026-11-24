package api

import (
	"net/http"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

func (s *Server) updateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/updates", s.getUpdates)
	mux.HandleFunc("POST /api/updates", s.requestUpdate)
	mux.HandleFunc("PUT /api/updates/auto", s.putAutoUpdate)
}

func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.Updates(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// requestUpdate answers 202: the update itself runs outside the daemon (and
// restarts it); GET /api/updates shows the progress.
func (s *Server) requestUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channel        string `json:"channel"`
		Version        string `json:"version"`
		AllowDowngrade bool   `json:"allow_downgrade"`
	}
	if !decodeJSON(w, r, &body, `{"channel": "stable"} or {"version": "v1.2.3"}`) {
		return
	}
	v, err := s.Panel.RequestUpdate(r.Context(), actorOf(r), panel.UpdateRequest{
		Channel: body.Channel, Version: body.Version, AllowDowngrade: body.AllowDowngrade,
	})
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

func (s *Server) putAutoUpdate(w http.ResponseWriter, r *http.Request) {
	var body panel.AutoUpdate
	if !decodeJSON(w, r, &body, `{"mode": "stable", "window": "03:00-05:00"}`) {
		return
	}
	v, err := s.Panel.SetAutoUpdate(r.Context(), actorOf(r), body)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
