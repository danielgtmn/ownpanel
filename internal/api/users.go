package api

import (
	"net/http"
	"strconv"
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

// pathID parses the {id} path value; a malformed one answers 404 like an
// unknown id.
func pathID(w http.ResponseWriter, r *http.Request, what string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, what+" not found")
		return 0, false
	}
	return id, true
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
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decodeJSON(w, r, &body, `{"name": "…", "password": "…", "role": "member"}`) {
		return
	}
	u, err := s.Panel.CreateUser(r.Context(), actorOf(r), body.Name, body.Password, body.Role)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "user")
	if !ok {
		return
	}
	var body struct {
		Role     *string `json:"role"`
		Password *string `json:"password"`
	}
	if !decodeJSON(w, r, &body, `{"role": "admin"} or {"password": "…"}`) {
		return
	}
	u, err := s.Panel.UpdateUser(r.Context(), actorOf(r), id, body.Role, body.Password)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "user")
	if !ok {
		return
	}
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
