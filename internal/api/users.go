package api

import (
	"net/http"
	"strconv"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

func (s *Server) userRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/users", s.listUsers)
	mux.HandleFunc("POST /api/users", s.createUser)
	mux.HandleFunc("PUT /api/users/{id}", s.updateUser)
	mux.HandleFunc("DELETE /api/users/{id}", s.deleteUser)
}

func (s *Server) activityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/activity", s.listActivity)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Panel.Users(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decodeJSON(w, r, &body, `{"name": "…", "email": "… (optional)", "password": "…", "role": "member"}`) {
		return
	}
	u, err := s.Panel.CreateUser(r.Context(), actorOf(r), panel.NewUser{Name: body.Name, Email: body.Email, Password: body.Password, Role: body.Role})
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Role     *string `json:"role"`
		Password *string `json:"password"`
		Email    *string `json:"email"`
	}
	if !decodeJSON(w, r, &body, `{"role": "admin"}, {"email": "…"} ("" removes it) or {"password": "…"}`) {
		return
	}
	u, err := s.Panel.UpdateUser(r.Context(), actorOf(r), id, panel.UserUpdate{Role: body.Role, Password: body.Password, Email: body.Email})
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Panel.DeleteUser(r.Context(), actorOf(r), id); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listActivity(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = n
	}
	list, err := s.Panel.Activities(r.Context(), actorOf(r), limit)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
