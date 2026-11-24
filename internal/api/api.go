// Package api is the HTTP front door of the panel: it authenticates
// requests, decodes them, calls the panel service and encodes the result.
// Permissions are decided by the panel service, not here.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// MaxUploadBytes caps the size of an uploaded archive.
const MaxUploadBytes = 512 << 20

// maxJSONBytes caps request bodies that are JSON.
const maxJSONBytes = 1 << 20

// SessionCookie holds the session secret.
const SessionCookie = "ownpanel_session"

// CSRFHeader must be sent (with value "1") on every changing request that
// is authenticated by the session cookie. Other sites cannot set custom
// headers without a CORS preflight, which this API never allows.
const CSRFHeader = "X-Ownpanel-Request"

// Server exposes the panel service over HTTP.
type Server struct {
	Panel *panel.Service
}

// routeGroups registers every group of routes. A new group is a new file
// with a method like these, added here.
var routeGroups = []func(*Server, *http.ServeMux){
	(*Server).sessionRoutes,
	(*Server).accountRoutes,
	(*Server).tokenRoutes,
	(*Server).siteRoutes,
	(*Server).userRoutes,
	(*Server).activityRoutes,
	(*Server).uploadRoutes,
	(*Server).dnsRoutes,
}

// Handler returns the API routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, register := range routeGroups {
		register(s, mux)
	}
	// Anything else below /api/ is a JSON 404, like every other API error.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such API endpoint")
	})
	return s.authenticate(mux)
}

type actorKey struct{}

// actorOf returns the authenticated actor, or the zero Actor (which the
// panel refuses with Unauthenticated).
func actorOf(r *http.Request) panel.Actor {
	a, _ := r.Context().Value(actorKey{}).(panel.Actor)
	return a
}

// authenticate resolves the actor: a bearer token first, else the session
// cookie. A wrong token is refused right away; a stale cookie just means
// nobody is signed in. Changing requests by cookie must pass the CSRF
// checks.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var actor panel.Actor
		if h := r.Header.Get("Authorization"); h != "" {
			token, ok := strings.CutPrefix(h, "Bearer ")
			if !ok {
				writeError(w, http.StatusUnauthorized, "the Authorization header must be \"Bearer <access token>\"")
				return
			}
			a, err := s.Panel.TokenActor(r.Context(), strings.TrimSpace(token))
			if err != nil {
				writeFailure(w, err)
				return
			}
			actor = a
		} else if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
			a, err := s.Panel.SessionActor(r.Context(), c.Value)
			if err == nil {
				actor = a
			} else if panel.KindOf(err) != panel.Unauthenticated {
				writeFailure(w, err)
				return
			}
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead && actor.Via != panel.ViaToken {
			if origin := r.Header.Get("Origin"); origin != "" && !sameHost(origin, r.Host) {
				writeError(w, http.StatusForbidden, "requests from other web sites are not allowed")
				return
			}
			if actor.Via == panel.ViaSession && r.Header.Get(CSRFHeader) != "1" {
				writeError(w, http.StatusForbidden, "this request is missing the "+CSRFHeader+" header")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
	})
}

func sameHost(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, host)
}

// peerIsLoopback: only a proxy on this machine (Caddy) is trusted to set
// X-Forwarded-* headers.
func peerIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// clientIP is the direct peer, or the last X-Forwarded-For entry (the one
// the local proxy added) when the peer is loopback.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if peerIsLoopback(r) {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
				return last
			}
		}
	}
	return host
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || (r.Header.Get("X-Forwarded-Proto") == "https" && peerIsLoopback(r))
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, secret string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    secret,
		Path:     "/",
		MaxAge:   int(panel.SessionLifetime / time.Second),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// decodeJSON reads a JSON body into v; on failure it answers 400 with
// example as a hint and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, example string) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBytes)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "request body must be JSON like "+example)
		return false
	}
	return true
}

// writeFailure maps a service error to an HTTP answer. Internal errors are
// logged, never sent to the client.
func writeFailure(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch panel.KindOf(err) {
	case panel.NotFound:
		status = http.StatusNotFound
	case panel.Invalid:
		status = http.StatusBadRequest
	case panel.Conflict:
		status = http.StatusConflict
	case panel.NotSynced:
		status = http.StatusBadGateway
	case panel.Forbidden:
		status = http.StatusForbidden
	case panel.Unauthenticated:
		status = http.StatusUnauthorized
	case panel.Throttled:
		status = http.StatusTooManyRequests
	case panel.Unavailable:
		status = http.StatusBadGateway
	default:
		slog.Error("request failed", "err", err)
		writeError(w, status, "something went wrong on the server")
		return
	}
	writeError(w, status, err.Error())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	// Messages are shown to people as they are; Go errors start lowercase.
	if msg != "" {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}
	writeJSON(w, status, map[string]string{"error": msg})
}
