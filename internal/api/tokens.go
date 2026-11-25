package api

import "net/http"

func (s *Server) tokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tokens", s.listTokens)
	mux.HandleFunc("POST /api/tokens", s.createToken)
	mux.HandleFunc("DELETE /api/tokens/{id}", s.deleteToken)
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.Panel.Tokens(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string   `json:"name"`
		Access string   `json:"access"`
		Sites  []string `json:"sites"`
		// ExpiresInDays: null or 0 means never.
		ExpiresInDays *int `json:"expires_in_days"`
	}
	if !decodeJSON(w, r, &body, `{"name": "…", "access": "read", "sites": null, "expires_in_days": 90}`) {
		return
	}
	days := 0
	if body.ExpiresInDays != nil {
		days = *body.ExpiresInDays
	}
	t, err := s.Panel.CreateToken(r.Context(), actorOf(r), body.Name, body.Access, body.Sites, days)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "access token")
	if !ok {
		return
	}
	if err := s.Panel.DeleteToken(r.Context(), actorOf(r), id); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
