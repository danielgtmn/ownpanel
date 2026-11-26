package api

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// newSetupEnv: mail host mail.example.net, example.com active with the
// mailbox info@example.com; the public handler in front of a 404.
func newSetupEnv(t *testing.T) (*env, http.Handler, string) {
	t.Helper()
	e, _ := newMailEnv(t)
	wantStatus(t, e.as("PUT", "/api/settings", `{"mail_host":"mail.example.net"}`), 200, "")
	wantStatus(t, e.as("POST", "/api/mail/domains", `{"domain":"example.com"}`), 201, "")
	rec := e.as("POST", "/api/mail/domains/example.com/mailboxes", `{"local_part":"info","password":"mailbox-password-1"}`)
	wantStatus(t, rec, 201, "")
	m := decode[panel.Mailbox](t, rec)
	return e, (&Server{Panel: e.svc}).MailSetupHandler(http.NotFoundHandler()), m.ID
}

func public(h http.Handler, method, host, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestAutoconfig(t *testing.T) {
	_, h, _ := newSetupEnv(t)
	for _, c := range []struct{ host, path string }{
		{"autoconfig.example.com", "/mail/config-v1.1.xml?emailaddress=info%40example.com"},
		{"Autoconfig.Example.com:443", "/mail/config-v1.1.xml"},
		{"example.com", "/.well-known/autoconfig/mail/config-v1.1.xml"},
		{"autoconfig.example.com", "/.well-known/autoconfig/mail/config-v1.1.xml"},
	} {
		rec := public(h, "GET", c.host, c.path, "")
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/xml") {
			t.Fatalf("%s%s: %d %s", c.host, c.path, rec.Code, rec.Body)
		}
		var cfg acConfig
		if err := xml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
			t.Fatal(err)
		}
		p := cfg.Provider
		if cfg.Version != "1.1" || p.Domain != "example.com" || len(p.Incoming) != 1 || len(p.Outgoing) != 2 {
			t.Fatalf("%+v", cfg)
		}
		in, out, starttls := p.Incoming[0], p.Outgoing[0], p.Outgoing[1]
		if in.Type != "imap" || in.Hostname != "mail.example.net" || in.Port != 993 || in.SocketType != "SSL" || in.Username != "%EMAILADDRESS%" ||
			out.Type != "smtp" || out.Port != 465 || out.SocketType != "SSL" || starttls.Port != 587 || starttls.SocketType != "STARTTLS" ||
			in.Authentication != "password-cleartext" {
			t.Fatalf("%+v", p)
		}
	}
	if rec := public(h, "HEAD", "autoconfig.example.com", "/mail/config-v1.1.xml", ""); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD %d", rec.Code)
	}
	// Unknown domains, the panel's own name and other methods.
	for _, c := range []struct {
		method, host, path string
		code               int
	}{
		{"GET", "autoconfig.other.example", "/mail/config-v1.1.xml", 404},
		{"GET", "example.com", "/mail/config-v1.1.xml", 404},
		{"GET", "mail.example.net", "/.well-known/autoconfig/mail/config-v1.1.xml", 404},
		{"POST", "autoconfig.example.com", "/mail/config-v1.1.xml", 405},
	} {
		if rec := public(h, c.method, c.host, c.path, ""); rec.Code != c.code || strings.Contains(rec.Body.String(), "example.com") {
			t.Fatalf("%s %s%s: %d %s", c.method, c.host, c.path, rec.Code, rec.Body)
		}
	}
	// Everything else passes through.
	if rec := public(h, "GET", "autoconfig.example.com", "/other", ""); rec.Code != 404 || !strings.Contains(rec.Body.String(), "page not found") {
		t.Fatalf("pass through %d %s", rec.Code, rec.Body)
	}
}

const adBody = `<?xml version="1.0" encoding="utf-8"?>
<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/outlook/requestschema/2006">
  <Request>
    <EMailAddress>%s</EMailAddress>
    <AcceptableResponseSchema>%s</AcceptableResponseSchema>
  </Request>
</Autodiscover>`

const outlookSchema = "http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a"

func adRequestBody(addr, schema string) string {
	return strings.Replace(strings.Replace(adBody, "%s", addr, 1), "%s", schema, 1)
}

func TestAutodiscover(t *testing.T) {
	_, h, _ := newSetupEnv(t)
	rec := public(h, "POST", "autodiscover.example.com", "/Autodiscover/Autodiscover.xml", adRequestBody("Someone@Example.com", outlookSchema))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006">`,
		`<Response xmlns="` + outlookSchema + `">`,
		"<Type>IMAP</Type>", "<Server>mail.example.net</Server>", "<Port>993</Port>", "<Type>SMTP</Type>", "<Port>465</Port>",
		"<LoginName>someone@example.com</LoginName>", "<SSL>on</SSL>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("%q missing in %s", want, body)
		}
	}
	// Any address of the domain gets the same answer: nothing tells
	// whether it is a mailbox.
	other := public(h, "POST", "autodiscover.example.com", "/autodiscover/autodiscover.xml", adRequestBody("info@example.com", outlookSchema))
	if other.Code != 200 || strings.ReplaceAll(other.Body.String(), "info@", "someone@") != body {
		t.Fatalf("answers differ: %s", other.Body)
	}

	for _, c := range []struct {
		name, method, host, body string
		code                     int
	}{
		{"GET", "GET", "autodiscover.example.com", "", 405},
		{"not XML", "POST", "autodiscover.example.com", "hello", 400},
		{"no address", "POST", "autodiscover.example.com", adRequestBody("", outlookSchema), 400},
		{"markup in the address", "POST", "autodiscover.example.com", adRequestBody("a<b>@example.com", outlookSchema), 400},
		{"not an address", "POST", "autodiscover.example.com", adRequestBody("a b@example.com", outlookSchema), 400},
		{"too large", "POST", "autodiscover.example.com", adRequestBody("info@example.com", outlookSchema) + strings.Repeat(" ", maxAutodiscoverBytes), 413},
		{"other domain", "POST", "autodiscover.example.com", adRequestBody("info@other.example", outlookSchema), 404},
		{"unknown domain", "POST", "autodiscover.other.example", adRequestBody("info@other.example", outlookSchema), 404},
		{"no autodiscover name", "POST", "example.com", adRequestBody("info@example.com", outlookSchema), 404},
		{"ActiveSync", "POST", "autodiscover.example.com", adRequestBody("info@example.com", "http://schemas.microsoft.com/exchange/autodiscover/mobilesync/responseschema/2006"), 404},
		{"entity", "POST", "autodiscover.example.com", `<!DOCTYPE a [<!ENTITY x "info@example.com">]><Autodiscover><Request><EMailAddress>&x;</EMailAddress></Request></Autodiscover>`, 400},
	} {
		rec := public(h, c.method, c.host, "/autodiscover/autodiscover.xml", c.body)
		if rec.Code != c.code || strings.Contains(rec.Body.String(), "mail.example.net") {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
}

func TestAppleMailProfile(t *testing.T) {
	e, _, id := newSetupEnv(t)
	path := "/api/mail/mailboxes/" + id + "/apple-profile"
	rec := e.as("GET", path, "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-apple-aspen-config" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="info@example.com.mobileconfig"` {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<key>PayloadType</key><string>com.apple.mail.managed</string>",
		"<key>PayloadType</key><string>Configuration</string>",
		"<key>EmailAddress</key><string>info@example.com</string>",
		"<key>IncomingMailServerHostName</key><string>mail.example.net</string>",
		"<key>IncomingMailServerPortNumber</key><integer>993</integer>",
		"<key>OutgoingMailServerPortNumber</key><integer>465</integer>",
		"<key>IncomingMailServerUseSSL</key><true/>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("%q missing in %s", want, body)
		}
	}
	if strings.Contains(strings.ToLower(body), "password</key><string>") || strings.Contains(body, "mailbox-password-1") {
		t.Fatal("a password in the profile")
	}
	var v any
	if err := xml.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("not XML: %v", err)
	}
	// The same profile again (it replaces the installed one).
	if again := e.as("GET", path, ""); again.Body.String() != body {
		t.Fatal("profile differs between downloads")
	}

	wantStatus(t, e.do(req{method: "GET", path: path}), 401, "")
	wantStatus(t, e.as("GET", "/api/mail/mailboxes/999/apple-profile", ""), 404, "")
	_, read := e.createToken("read", "null")
	wantStatus(t, e.do(req{method: "GET", path: path, token: read}), 200, "")
}

func TestMailHostSPFRoute(t *testing.T) {
	e, _ := newMailEnv(t)
	wantStatus(t, e.as("POST", "/api/mail/host-spf", ""), 409, "")
	wantStatus(t, e.as("PUT", "/api/settings", `{"mail_host":"mail.example.net"}`), 200, "")
	wantStatus(t, e.as("POST", "/api/mail/host-spf", ""), 409, "DNS connections")
	_, read := e.createToken("read", "null")
	wantStatus(t, e.do(req{method: "POST", path: "/api/mail/host-spf", token: read}), 403, "")
}
