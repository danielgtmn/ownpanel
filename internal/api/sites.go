package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/timeouts"
)

func (s *Server) siteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sites", s.listSites)
	mux.HandleFunc("GET /api/sites/{name}", s.getSite)
	mux.HandleFunc("GET /api/sites/{name}/icon", s.siteIcon)
	mux.HandleFunc("PUT /api/sites/{name}", s.putSite)
	mux.HandleFunc("DELETE /api/sites/{name}", s.deleteSite)
	mux.HandleFunc("POST /api/sites/{name}/deployments", s.deploy)
	mux.HandleFunc("POST /api/sites/{name}/rollback", s.rollback)
	mux.HandleFunc("GET "+panel.PreviewPermissionPath, s.previewCertificateAllowed)
}

// previewCertificateAllowed is asked by the web server before it obtains a
// certificate for a name below the preview domain: 200 for the address of a
// kept version, 404 for anything else. Only the web server on this machine
// may ask (it reaches the panel directly, not through a proxy), so the
// answers cannot be used from outside to find out which names exist.
func (s *Server) previewCertificateAllowed(w http.ResponseWriter, r *http.Request) {
	if !peerIsLoopback(r) || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Via") != "" {
		writeError(w, http.StatusNotFound, "no such API endpoint")
		return
	}
	ok, err := s.Panel.PreviewCertificateAllowed(r.Context(), r.URL.Query().Get("domain"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not a preview")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
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

// deploy publishes the raw archive in the body. ?live=false adds the new
// version without making it live.
func (s *Server) deploy(w http.ResponseWriter, r *http.Request) {
	timeouts.Extend(w) // large archives on slow links
	live := true
	switch v := r.URL.Query().Get("live"); v {
	case "", "true", "1":
	case "false", "0":
		live = false
	default:
		writeError(w, http.StatusBadRequest, "live must be true or false")
		return
	}
	p, err := s.Panel.PublishVersion(r.Context(), actorOf(r), r.PathValue("name"), http.MaxBytesReader(w, r.Body, MaxUploadBytes), live)
	writePublication(w, p, err)
}

// writePublication answers a publish. When only the web server could not be
// updated, the version exists: the answer carries it next to the error.
func writePublication(w http.ResponseWriter, p panel.Publication, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, p)
	case panel.KindOf(err) == panel.NotSynced:
		msg := err.Error()
		writeJSON(w, http.StatusBadGateway, struct {
			Error string `json:"error"`
			panel.Publication
		}{strings.ToUpper(msg[:1]) + msg[1:], p})
	default:
		writeFailure(w, err)
	}
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

// siteIcon serves the icon found in the site's live release. The page shows
// it in an <img>, but an SVG opened directly must not run scripts either.
func (s *Server) siteIcon(w http.ResponseWriter, r *http.Request) {
	icon, err := s.Panel.SiteIcon(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	etag := `"` + url.PathEscape(icon.Release) + "/" + url.PathEscape(icon.Path) + `"`
	h := w.Header()
	h.Set("Content-Type", icon.ContentType)
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=300")
	h.Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match == etag || match == "W/"+etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(icon.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(icon.Data)
}
