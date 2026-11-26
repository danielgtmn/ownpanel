package panel

import (
	"net"
	"net/http"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/caddy"
)

// The mail host as the web address of the people who read mail
// (wiki/Groupware-Architecture.md, "G0"): Caddy passes the self-service
// API, calendars and contacts to the mail service's web socket and
// everything else to the panel, which serves only the self-service web app
// there.

// mailWeb is the mail host's site for the people who read mail: only with
// the mail service's web socket and a mail host of its own (see
// selfServiceProblem), with the certificate the panel obtained for it.
func (s *Service) mailWeb() caddy.MailWeb {
	if s.selfServiceProblem() != "" {
		return caddy.MailWeb{}
	}
	w := caddy.MailWeb{Host: s.MailHost(), Socket: s.opts.MailWebSocket}
	for _, c := range s.mailCertificates() {
		w.Certificates = append(w.Certificates, caddy.Certificate{CertPEM: c.CertPEM, KeyPEM: c.KeyPEM})
	}
	return w
}

// Why mailbox users have no sign-in page of their own on the mail host
// ("" when they have one, or will once the certificate is there).
const (
	selfServiceNoHost   = "no mail host"
	selfServiceSameHost = "same as panel domain"
	selfServiceNoSocket = "no web socket"
)

func (s *Service) selfServiceProblem() string {
	switch host := s.MailHost(); {
	case host == "":
		return selfServiceNoHost
	case host == s.PanelDomain():
		return selfServiceSameHost
	case s.opts.MailWebSocket == "" || s.opts.PanelUpstream == "":
		return selfServiceNoSocket
	}
	return ""
}

// MailHostHandler sends requests for the mail host to account, all others
// to next. Caddy already keeps the panel's API and MCP off the mail host;
// this is the second line: account never serves them. A mail host equal to
// the panel domain is the panel.
func (s *Service) MailHostHandler(account, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mh := s.MailHost(); mh != "" && mh != s.PanelDomain() && requestHost(r) == mh {
			account.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestHost is the request's Host without port and trailing dot, in
// lower case.
func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}
