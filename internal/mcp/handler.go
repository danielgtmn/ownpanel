// Package mcp is the Model Context Protocol front door of the panel: it
// authenticates requests with an access token, turns tool calls into calls to
// the panel service and the results back into tool output. Like the HTTP API
// it decides nothing about permissions; internal/panel does.
package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/timeouts"
)

// Path is where the MCP endpoint is mounted.
const Path = "/mcp"

// maxRequestBytes bounds one request: 10 MB of files may be base64 encoded
// (4/3) or, as text, JSON-escaped.
const maxRequestBytes = 64 << 20

const instructions = "ownpanel is a hosting panel for static web sites: each site is a set of files " +
	"(HTML, CSS, JavaScript, images) that ownpanel serves on one or more domains with automatic HTTPS. " +
	"Every publish creates a new version of the site and makes it live; older versions are kept and " +
	"can be made live again with make_version_live. Use publish_files for small sites you write " +
	"yourself and create_upload_link for large archives. It also hosts e-mail: mail domains, " +
	"mailboxes and aliases (mailbox passwords are only set in the web interface, never through " +
	"these tools). Destructive tools such as delete_site require an explicit confirmation."

type config struct {
	panelDomain func() string
}

func (c config) domain() string {
	if c.panelDomain == nil {
		return ""
	}
	return strings.ToLower(c.panelDomain())
}

// Option configures Handler.
type Option func(*config)

// WithPanelDomain tells the handler the domain the panel is served on
// through the web server (-panel-domain). Requests for that Host that arrive
// from the local web server are accepted despite the loopback listener, and
// upload links are built on it.
func WithPanelDomain(domain string) Option {
	return WithPanelDomainFunc(func() string { return domain })
}

// WithPanelDomainFunc is WithPanelDomain for a domain that can change while
// running (set in the panel settings); "" means none.
func WithPanelDomainFunc(domain func() string) Option {
	return func(c *config) { c.panelDomain = domain }
}

// call is what the authentication step hands to a tool.
type call struct {
	actor   panel.Actor
	baseURL string // scheme and host for links, no trailing slash
}

type callKey struct{}

func callFrom(ctx context.Context) (call, bool) {
	c, ok := ctx.Value(callKey{}).(call)
	return c, ok
}

// Handler serves MCP at Path. Only "Authorization: Bearer op_..." access
// tokens are accepted, never cookies, so a browser session cannot use it.
func Handler(p *panel.Service, version string, opts ...Option) http.Handler {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	srv := newServer(p, version)
	addRedirectTools(srv, p)
	addMailTools(srv, p)
	addMailAccountTools(srv, p)
	addSharingTools(srv, p)
	addMailRulesTools(srv, p)
	addDNSTools(srv, p)
	addDomainTools(srv, p)
	addDNSTemplateTools(srv, p)
	// Stateless: every request carries its own token, so there is no session
	// to remember or to hijack.
	sdkHandler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return srv },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: maxRequestBytes},
	)
	// The SDK's rebinding protection stays on; panelHost lets the panel
	// domain through. Cross-origin protection refuses browsers' cross-site
	// requests (clients that are not browsers send no Origin).
	h := http.NewCrossOriginProtection().Handler(authenticate(p, cfg, panelHost(cfg, sdkHandler)))
	mux := http.NewServeMux()
	// publish_files carries up to 64 MB in one request.
	mux.Handle(Path, timeouts.Long(h))
	return mux
}

// authenticate resolves the bearer token to an actor and puts it in the
// request context, where the SDK passes it on to the tool handlers.
func authenticate(p *panel.Service, cfg config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		scheme, token, _ := strings.Cut(h, " ")
		if !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
			unauthorized(w, `Bearer realm="ownpanel"`, "send an access token as \"Authorization: Bearer op_...\"")
			return
		}
		actor, err := p.TokenActor(r.Context(), strings.TrimSpace(token))
		if err != nil {
			if panel.KindOf(err) == panel.Unauthenticated {
				unauthorized(w, `Bearer realm="ownpanel", error="invalid_token"`, "the access token is not valid")
				return
			}
			slog.Error("mcp: token lookup failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "something went wrong on the server")
			return
		}
		c := call{actor: actor, baseURL: baseURL(r, cfg)}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callKey{}, c)))
	})
}

func unauthorized(w http.ResponseWriter, challenge, msg string) {
	w.Header().Set("WWW-Authenticate", challenge)
	writeJSONError(w, http.StatusUnauthorized, msg)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// panelHost makes the SDK's localhost protection accept the panel domain.
// ownpaneld listens on loopback and the web server forwards requests with the
// original Host, which the protection would take for a rebinding attack. For
// that one Host the request is presented to the SDK as not coming through a
// loopback listener; every other Host keeps the SDK's check.
func panelHost(cfg config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if d := cfg.domain(); d != "" && host == d {
			// Documentation address (RFC 5737); only its not being loopback matters.
			addr := &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 443}
			r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, net.Addr(addr)))
		}
		next.ServeHTTP(w, r)
	})
}

// baseURL is the address links should point to: the panel domain when it is
// configured, else the address this request came in on.
func baseURL(r *http.Request, cfg config) string {
	if d := cfg.domain(); d != "" {
		return "https://" + d
	}
	scheme := "http"
	if r.TLS != nil || (r.Header.Get("X-Forwarded-Proto") == "https" && peerIsLoopback(r)) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// peerIsLoopback: only a proxy on this machine is trusted to set
// X-Forwarded-* headers.
func peerIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
