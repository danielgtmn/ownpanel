package api

import (
	"net/http"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

func (s *Server) sessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
}

func (s *Server) accountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/account/password", s.changePassword)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	setup, err := s.Panel.SetupRequired(r.Context())
	if err != nil {
		writeFailure(w, err)
		return
	}
	complete, err := s.Panel.SetupComplete(r.Context())
	if err != nil {
		writeFailure(w, err)
		return
	}
	body := struct {
		SetupRequired bool        `json:"setup_required"`
		SetupComplete bool        `json:"setup_complete"`
		User          *panel.User `json:"user"`
		Via           *string     `json:"via"`
	}{SetupRequired: setup, SetupComplete: complete}
	if a := actorOf(r); a.Via != "" {
		u, err := s.Panel.Me(r.Context(), a)
		if err != nil {
			writeFailure(w, err)
			return
		}
		body.User, body.Via = &u, &a.Via
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code     string `json:"setup_code"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body, `{"setup_code": "…", "name": "…", "password": "…"}`) {
		return
	}
	u, secret, err := s.Panel.Setup(r.Context(), body.Code, panel.Credentials{Name: body.Name, Password: body.Password, ClientIP: clientIP(r)})
	if err != nil {
		writeFailure(w, err)
		return
	}
	setSessionCookie(w, r, secret)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body, `{"name": "…", "password": "…"}`) {
		return
	}
	u, secret, err := s.Panel.Login(r.Context(), panel.Credentials{Name: body.Name, Password: body.Password, ClientIP: clientIP(r)})
	if err != nil {
		writeFailure(w, err)
		return
	}
	setSessionCookie(w, r, secret)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		if err := s.Panel.Logout(r.Context(), c.Value); err != nil {
			writeFailure(w, err)
			return
		}
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeJSON(w, r, &body, `{"current": "…", "new": "…"}`) {
		return
	}
	if err := s.Panel.ChangePassword(r.Context(), actorOf(r), body.Current, body.New, clientIP(r)); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
