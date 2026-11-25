package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/panel"
)

// Settings for mail programs (internal/panel/mailsetup.go): the public
// answers mail programs ask for before they have a password, and the
// profile for Apple devices a mailbox's owner downloads.

// maxAutodiscoverBytes bounds an autodiscover request; a real one is a few
// hundred bytes.
const maxAutodiscoverBytes = 8 << 10

// mailSetupHeaders are sent with every public answer.
func mailSetupHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "application/xml; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=300")
	h.Set("Content-Security-Policy", apiContentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
}

// requestHost is the request's host name, lowercase, without port and
// trailing dot.
func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// MailSetupHandler answers where mail programs look for their settings
// (caddy.MailSetupPaths, matched without regard to case like Caddy does)
// and hands every other request to next. Which domain is asked about comes
// from the host name: autoconfig.<domain> and autodiscover.<domain>, and
// <domain> itself for the well-known path.
func (s *Server) MailSetupHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.EqualFold(p, caddy.MailAutoconfigPath):
			domain, ok := strings.CutPrefix(requestHost(r), "autoconfig.")
			s.autoconfig(w, r, domain, ok)
		case strings.EqualFold(p, caddy.MailAutoconfigWellKnown):
			host := requestHost(r)
			s.autoconfig(w, r, strings.TrimPrefix(host, "autoconfig."), true)
		case strings.EqualFold(p, caddy.MailAutodiscoverPath):
			domain, ok := strings.CutPrefix(requestHost(r), "autodiscover.")
			s.autodiscover(w, r, domain, ok)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func notFoundText(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "Not found.\n")
}

// ---- Thunderbird and others ----

type acServer struct {
	Type           string `xml:"type,attr"`
	Hostname       string `xml:"hostname"`
	Port           int    `xml:"port"`
	SocketType     string `xml:"socketType"`
	Authentication string `xml:"authentication"`
	Username       string `xml:"username"`
}

type acProvider struct {
	ID               string     `xml:"id,attr"`
	Domain           string     `xml:"domain"`
	DisplayName      string     `xml:"displayName"`
	DisplayShortName string     `xml:"displayShortName"`
	Incoming         []acServer `xml:"incomingServer"`
	Outgoing         []acServer `xml:"outgoingServer"`
}

type acConfig struct {
	XMLName  xml.Name   `xml:"clientConfig"`
	Version  string     `xml:"version,attr"`
	Provider acProvider `xml:"emailProvider"`
}

// AutoconfigXML is the client configuration (version 1.1) for a mail
// domain; the user name is the whole address.
func AutoconfigXML(set panel.MailClientSetup) ([]byte, error) {
	c := set.Client
	server := func(typ string, port int, socket string) acServer {
		return acServer{Type: typ, Hostname: c.Server, Port: port, SocketType: socket, Authentication: "password-cleartext", Username: "%EMAILADDRESS%"}
	}
	cfg := acConfig{Version: "1.1", Provider: acProvider{
		ID: set.Domain, Domain: set.Domain, DisplayName: set.Domain, DisplayShortName: set.Domain,
		Incoming: []acServer{server("imap", c.IMAPPort, "SSL")},
		Outgoing: []acServer{server("smtp", c.SMTPPort, "SSL"), server("smtp", c.SMTPStartTLS, "STARTTLS")},
	}}
	return marshalXML(cfg)
}

func marshalXML(v any) ([]byte, error) {
	b, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(b, '\n')...), nil
}

func (s *Server) autoconfig(w http.ResponseWriter, r *http.Request, domain string, ok bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	if !ok {
		notFoundText(w)
		return
	}
	set, err := s.Panel.MailSetupFor(r.Context(), domain)
	if err != nil {
		if panel.KindOf(err) == panel.NotFound {
			notFoundText(w)
			return
		}
		writeFailure(w, err)
		return
	}
	body, err := AutoconfigXML(set)
	if err != nil {
		writeFailure(w, err)
		return
	}
	mailSetupHeaders(w)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

// ---- Outlook ----

// adRequest is the "plain old XML" autodiscover request. Elements match
// in any namespace.
type adRequest struct {
	XMLName xml.Name `xml:"Autodiscover"`
	Request struct {
		EMailAddress             string `xml:"EMailAddress"`
		AcceptableResponseSchema string `xml:"AcceptableResponseSchema"`
	} `xml:"Request"`
}

type adProtocol struct {
	Type           string `xml:"Type"`
	Server         string `xml:"Server"`
	Port           int    `xml:"Port"`
	DomainRequired string `xml:"DomainRequired"`
	LoginName      string `xml:"LoginName"`
	SPA            string `xml:"SPA"`
	SSL            string `xml:"SSL"`
	Encryption     string `xml:"Encryption"`
	AuthRequired   string `xml:"AuthRequired"`
	UsePOPAuth     string `xml:"UsePOPAuth,omitempty"`
	SMTPLast       string `xml:"SMTPLast,omitempty"`
}

type adResponse struct {
	XMLName  xml.Name `xml:"http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006 Autodiscover"`
	Response struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a Response"`
		Account struct {
			AccountType string       `xml:"AccountType"`
			Action      string       `xml:"Action"`
			Protocol    []adProtocol `xml:"Protocol"`
		} `xml:"Account"`
	}
}

// parseAutodiscover reads the address from an autodiscover request:
// lowercase, a valid address, nothing else. mobile is true for a request
// for the Exchange ActiveSync schema, which this server does not offer.
func parseAutodiscover(body []byte) (address string, mobile bool, err error) {
	var req adRequest
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict = true
	if err := d.Decode(&req); err != nil {
		return "", false, fmt.Errorf("not an autodiscover request")
	}
	if strings.Contains(strings.ToLower(req.Request.AcceptableResponseSchema), "mobilesync") {
		return "", true, nil
	}
	addr := strings.ToLower(strings.TrimSpace(req.Request.EMailAddress))
	if len(addr) > 254 || !config.ValidAddress(addr) {
		return "", false, fmt.Errorf("not an e-mail address")
	}
	return addr, false, nil
}

// AutodiscoverXML is the answer for address: IMAP and SMTP with TLS, the
// whole address as user name.
func AutodiscoverXML(set panel.MailClientSetup, address string) ([]byte, error) {
	c := set.Client
	var resp adResponse
	resp.Response.Account.AccountType, resp.Response.Account.Action = "email", "settings"
	resp.Response.Account.Protocol = []adProtocol{
		{Type: "IMAP", Server: c.Server, Port: c.IMAPPort, DomainRequired: "off", LoginName: address, SPA: "off", SSL: "on", Encryption: "SSL", AuthRequired: "on"},
		{Type: "SMTP", Server: c.Server, Port: c.SMTPPort, DomainRequired: "off", LoginName: address, SPA: "off", SSL: "on", Encryption: "SSL", AuthRequired: "on", UsePOPAuth: "off", SMTPLast: "off"},
	}
	return marshalXML(resp)
}

func (s *Server) autodiscover(w http.ResponseWriter, r *http.Request, domain string, ok bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if !ok {
		notFoundText(w)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAutodiscoverBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "the request is too large")
		return
	}
	address, mobile, err := parseAutodiscover(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Only mail programs set up for this domain's addresses; nothing about
	// the address itself (whether it is a mailbox) is looked at.
	if _, d, _ := config.SplitAddress(address); mobile || d != domain {
		notFoundText(w)
		return
	}
	set, err := s.Panel.MailSetupFor(r.Context(), domain)
	if err != nil {
		if panel.KindOf(err) == panel.NotFound {
			notFoundText(w)
			return
		}
		writeFailure(w, err)
		return
	}
	out, err := AutodiscoverXML(set, address)
	if err != nil {
		writeFailure(w, err)
		return
	}
	mailSetupHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// ---- Apple devices ----

// profileUUID is a stable UUID (version 5 layout) for a part of the
// profile of address, so downloading it again replaces the installed one.
func profileUUID(part, address, host string) string {
	h := sha256.Sum256([]byte("ownpanel mail profile\x00" + part + "\x00" + address + "\x00" + host))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// plist writes an Apple property list of dictionaries, strings, integers
// and booleans; every string is escaped.
type plist struct{ bytes.Buffer }

func (p *plist) key(k string) {
	p.WriteString("<key>")
	_ = xml.EscapeText(p, []byte(k))
	p.WriteString("</key>")
}

func (p *plist) str(k, v string) {
	p.key(k)
	p.WriteString("<string>")
	_ = xml.EscapeText(p, []byte(v))
	p.WriteString("</string>\n")
}

func (p *plist) integer(k string, v int) {
	p.key(k)
	p.WriteString("<integer>" + strconv.Itoa(v) + "</integer>\n")
}

func (p *plist) boolean(k string, v bool) {
	p.key(k)
	if v {
		p.WriteString("<true/>\n")
	} else {
		p.WriteString("<false/>\n")
	}
}

// AppleMailProfile is an unsigned configuration profile with one mail
// account (com.apple.mail.managed) for the mailbox; the device asks for
// the password when the profile is installed.
func AppleMailProfile(set panel.MailboxSetup) []byte {
	c := set.Client
	addr := set.Address
	var p plist
	p.WriteString(xml.Header)
	p.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	p.WriteString("<plist version=\"1.0\">\n<dict>\n")
	p.key("PayloadContent")
	p.WriteString("\n<array>\n<dict>\n")
	p.str("EmailAccountType", "EmailTypeIMAP")
	p.str("EmailAccountDescription", addr)
	p.str("EmailAddress", addr)
	p.str("IncomingMailServerHostName", c.Server)
	p.integer("IncomingMailServerPortNumber", c.IMAPPort)
	p.boolean("IncomingMailServerUseSSL", true)
	p.str("IncomingMailServerUsername", addr)
	p.str("IncomingMailServerAuthentication", "EmailAuthPassword")
	p.str("OutgoingMailServerHostName", c.Server)
	p.integer("OutgoingMailServerPortNumber", c.SMTPPort)
	p.boolean("OutgoingMailServerUseSSL", true)
	p.str("OutgoingMailServerUsername", addr)
	p.str("OutgoingMailServerAuthentication", "EmailAuthPassword")
	p.boolean("OutgoingPasswordSameAsIncomingPassword", true)
	p.str("PayloadDescription", "Mail account "+addr)
	p.str("PayloadDisplayName", addr)
	p.str("PayloadIdentifier", "ownpanel.mail."+profileUUID("account", addr, c.Server))
	p.str("PayloadType", "com.apple.mail.managed")
	p.str("PayloadUUID", profileUUID("account", addr, c.Server))
	p.integer("PayloadVersion", 1)
	p.WriteString("</dict>\n</array>\n")
	p.str("PayloadDescription", "Sets up the mail account "+addr+". The password is asked for when the profile is installed.")
	p.str("PayloadDisplayName", "Mail: "+addr)
	p.str("PayloadIdentifier", "ownpanel.mail.profile."+profileUUID("profile", addr, c.Server))
	p.boolean("PayloadRemovalDisallowed", false)
	p.str("PayloadType", "Configuration")
	p.str("PayloadUUID", profileUUID("profile", addr, c.Server))
	p.integer("PayloadVersion", 1)
	p.WriteString("</dict>\n</plist>\n")
	return p.Bytes()
}

// profileFileName keeps the characters of an address that are safe in a
// file name everywhere.
func profileFileName(address string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '@', r == '-', r == '_':
			return r
		}
		return '-'
	}, address)
	return name + ".mobileconfig"
}

func (s *Server) appleMailProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "mailbox")
	if !ok {
		return
	}
	set, err := s.Panel.MailboxSetupOf(r.Context(), actorOf(r), id)
	if err != nil {
		writeFailure(w, err)
		return
	}
	body := AppleMailProfile(set)
	h := w.Header()
	h.Set("Content-Type", "application/x-apple-aspen-config")
	h.Set("Content-Disposition", `attachment; filename="`+profileFileName(set.Address)+`"`)
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) setUpMailHostSPF(w http.ResponseWriter, r *http.Request) {
	v, err := s.Panel.SetUpMailHostSPF(r.Context(), actorOf(r))
	answer(w, http.StatusOK, v, err)
}
