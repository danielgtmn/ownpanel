package api

import "net/http"

// Domains a person keeps in the panel whose DNS is managed elsewhere.
func (s *Server) domainRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/domains", s.listDomains)
	mux.HandleFunc("POST /api/domains", s.addDomain)
	mux.HandleFunc("DELETE /api/domains/{domain}", s.removeDomain)
}

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request) {
	list, err := s.Panel.Domains(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) addDomain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body, `{"name": "example.com"}`) {
		return
	}
	d, err := s.Panel.AddDomain(r.Context(), actorOf(r), body.Name)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) removeDomain(w http.ResponseWriter, r *http.Request) {
	removed, err := s.Panel.RemoveDomain(r.Context(), actorOf(r), r.PathValue("domain"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, removed)
}
