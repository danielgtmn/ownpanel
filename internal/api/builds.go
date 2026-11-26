package api

import "net/http"

func (s *Server) buildRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/builds/status", s.getBuildStatus)
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
