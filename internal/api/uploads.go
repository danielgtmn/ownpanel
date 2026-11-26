package api

import (
	"net/http"

	"github.com/danielgtmn/ownpanel/internal/timeouts"
)

func (s *Server) uploadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/uploads/{id}", s.uploadWithLink)
}

// uploadWithLink needs no other authentication: the id of the one-time link
// is the credential, and it publishes as the user who created the link.
func (s *Server) uploadWithLink(w http.ResponseWriter, r *http.Request) {
	timeouts.Extend(w) // large archives on slow links
	p, err := s.Panel.PublishWithUploadLink(r.Context(), r.PathValue("id"), http.MaxBytesReader(w, r.Body, MaxUploadBytes))
	writePublication(w, p, err)
}
