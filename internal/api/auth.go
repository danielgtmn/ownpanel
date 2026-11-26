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
	mux.HandleFunc("POST /api/password-reset", s.requestPasswordReset)
	mux.HandleFunc("POST /api/password-reset/confirm", s.resetPassword)
}

func (s *Server) accountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/account/password", s.changePassword)
	mux.HandleFunc("PUT /api/account/email", s.setOwnEmail)
	mux.HandleFunc("POST /api/account/logout-everywhere", s.logoutEverywhere)
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
		// PasswordReset: a forgotten password can be reset by e-mail.
		PasswordReset bool `json:"password_reset"`
	}{SetupRequired: setup, SetupComplete: complete, PasswordReset: s.Panel.PasswordResetAvailable(r.Context())}
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
	u, secret, err := s.Panel.Setup(r.Context(), body.Code, credentials(r, body.Name, body.Password))
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
	u, secret, err := s.Panel.Login(r.Context(), credentials(r, body.Name, body.Password))
	if err != nil {
		writeFailure(w, err)
		return
	}
	setSessionCookie(w, r, secret)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName(r)); err == nil {
		if err := s.Panel.Logout(r.Context(), c.Value); err != nil {
			writeFailure(w, err)
			return
		}
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// logoutEverywhere ends all of the person's sessions, this one included.
func (s *Server) logoutEverywhere(w http.ResponseWriter, r *http.Request) {
	if err := s.Panel.SignOutEverywhere(r.Context(), actorOf(r)); err != nil {
		writeFailure(w, err)
		return
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
	if err := s.Panel.ChangePassword(r.Context(), actorOf(r), body.Current, body.New); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setOwnEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email   string `json:"email"`
		Current string `json:"current"`
	}
	if !decodeJSON(w, r, &body, `{"email": "…", "current": "current password"} ("" removes the address)`) {
		return
	}
	u, err := s.Panel.SetOwnEmail(r.Context(), actorOf(r), body.Current, body.Email)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// requestPasswordReset sends a reset link if the address belongs to
// someone. The answer is the same either way.
func (s *Server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &body, `{"email": "…"}`) {
		return
	}
	if err := s.Panel.RequestPasswordReset(r.Context(), body.Email, clientIP(r)); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body, `{"token": "…", "password": "…"}`) {
		return
	}
	if err := s.Panel.ResetPassword(r.Context(), body.Token, body.Password); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func credentials(r *http.Request, name, password string) panel.Credentials {
	return panel.Credentials{Name: name, Password: password, ClientIP: clientIP(r), UserAgent: r.UserAgent()}
}
