package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// handler serves the admin socket endpoints documented in
// internal/mail/config/api.go. The socket is reachable only by the panel's
// user (file permissions), so there is no authentication.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+config.PathLoad, s.handleLoad)
	mux.HandleFunc("GET "+config.PathStatus, s.handleStatus)
	mux.HandleFunc("GET "+config.PathEvents, s.handleEvents)
	mux.HandleFunc("POST "+config.PathMailbox+"{id}/unblock", s.handleUnblock)
	mux.HandleFunc("DELETE "+config.PathMailbox+"{id}", s.handleDelete)
	mux.HandleFunc("POST "+config.PathOutbound, s.handleOutbound)
	mux.HandleFunc("POST "+config.PathSend, s.handleSend)
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) internalError(w http.ResponseWriter, what string, err error) {
	s.log.Error("mail admin: "+what, "err", err)
	http.Error(w, what+" failed", http.StatusInternalServerError)
}

func (s *Server) handleLoad(w http.ResponseWriter, r *http.Request) {
	// Unknown fields are ignored on purpose: a newer panel may send fields
	// this service does not know yet.
	var cfg config.Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigBytes)).Decode(&cfg); err != nil {
		// The decoder's message names a position or type, never content.
		http.Error(w, "invalid configuration: "+err.Error(), http.StatusBadRequest)
		return
	}
	err := s.Load(r.Context(), &cfg)
	var le *loadError
	switch {
	case errors.As(err, &le):
		http.Error(w, le.Error(), http.StatusBadRequest)
	case err != nil:
		s.internalError(w, "load", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Status(r.Context())
	if err != nil {
		s.internalError(w, "status", err)
		return
	}
	writeJSON(w, st)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	var after int64
	if a := r.URL.Query().Get("after"); a != "" {
		var err error
		if after, err = strconv.ParseInt(a, 10, 64); err != nil || after < 0 {
			http.Error(w, "after must be a non-negative number", http.StatusBadRequest)
			return
		}
	}
	ev, err := s.store.Events(r.Context(), after)
	if err != nil {
		s.internalError(w, "events", err)
		return
	}
	writeJSON(w, ev)
}

func (s *Server) handleUnblock(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !config.ValidStorageID(id) {
		http.Error(w, "invalid storage id", http.StatusBadRequest)
		return
	}
	was, err := s.store.Unblock(r.Context(), id)
	if err != nil {
		s.internalError(w, "unblock", err)
		return
	}
	_, configured := s.Directory().MailboxByStorageID(id)
	if !was && !configured {
		http.Error(w, "unknown mailbox", http.StatusNotFound)
		return
	}
	if was {
		s.log.Info("mail: mailbox unblocked", "storage_id", id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !config.ValidStorageID(id) {
		http.Error(w, "invalid storage id", http.StatusBadRequest)
		return
	}
	// The panel removes a mailbox from the configuration before it asks
	// for its data to be deleted; a mailbox still configured keeps its
	// data. Holding loadMu keeps a load from adding it back meanwhile.
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if _, ok := s.Directory().MailboxByStorageID(id); ok {
		http.Error(w, "the mailbox is still configured", http.StatusConflict)
		return
	}
	if err := s.store.DeleteMailbox(r.Context(), id); err != nil {
		s.internalError(w, "delete", err)
		return
	}
	s.log.Info("mail: mailbox data deleted", "storage_id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOutbound(w http.ResponseWriter, r *http.Request) {
	check := s.opts.Outbound
	if check == nil {
		check = checkOutbound
	}
	writeJSON(w, check(r.Context()))
}
