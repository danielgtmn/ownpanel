package api

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/panel"
)

// wantBody checks the status and that the answer contains s.
func wantBody(t *testing.T, rec *httptest.ResponseRecorder, status int, s string) {
	t.Helper()
	if rec.Code != status || !strings.Contains(rec.Body.String(), s) {
		t.Fatalf("status %d (want %d), body %s (want %q in it)", rec.Code, status, rec.Body, s)
	}
}

func TestMailAccountRoutes(t *testing.T) {
	e, _ := newMailEnv(t)
	wantStatus(t, e.as("PUT", "/api/settings", `{"mail_host":"mail.example.net"}`), 200, "")
	wantStatus(t, e.as("POST", "/api/mail/domains", `{"domain":"example.com"}`), 201, "")

	// Organisations.
	rec := e.as("POST", "/api/mail/organisations", `{"name":"Example Ltd"}`)
	wantStatus(t, rec, 201, "")
	org := decode[panel.Organisation](t, rec)
	if org.Name != "Example Ltd" || !strings.HasPrefix(org.ID, "org-") {
		t.Fatalf("org %s", rec.Body)
	}
	rec = e.as("PUT", "/api/mail/domains/example.com/organisation", `{"organisation":"`+org.ID+`"}`)
	wantStatus(t, rec, 200, "")
	if d := decode[panel.MailDomain](t, rec); d.Organisation == nil || d.Organisation.Name != "Example Ltd" {
		t.Fatalf("domain %s", rec.Body)
	}
	wantBody(t, e.as("PATCH", "/api/mail/organisations/"+org.ID, `{"name":"Example GmbH"}`), 200, "Example GmbH")
	wantBody(t, e.as("GET", "/api/mail/organisations", ""), 200, "example.com")
	wantStatus(t, e.as("PUT", "/api/mail/domains/example.com/organisation", `{"organisation":"org-nope"}`), 404, "")
	wantStatus(t, e.as("DELETE", "/api/mail/organisations/"+org.ID, ""), 204, "")

	// Display name on create and change.
	rec = e.as("POST", "/api/mail/domains/example.com/mailboxes", `{"local_part":"anna","password":"mailbox-password-1","display_name":"Anna"}`)
	wantBody(t, rec, 201, `"display_name":"Anna"`)
	box := decode[panel.Mailbox](t, rec)
	id := strconv.FormatInt(box.ID, 10)
	wantBody(t, e.as("PATCH", "/api/mail/mailboxes/"+id, `{"display_name":"Anna B.","app_passwords_only":true}`), 200, `"app_passwords_only":true`)
	wantStatus(t, e.as("PATCH", "/api/mail/mailboxes/"+id, `{"display_name":"a\nb"}`), 400, "")
	wantBody(t, e.as("PATCH", "/api/mail/mailboxes/"+id, `{"send_per_hour":10}`), 200, `"display_name":"Anna B."`)

	// App passwords: the token once, then never again.
	rec = e.as("POST", "/api/mail/mailboxes/"+id+"/app-passwords", `{"name":"Phone"}`)
	wantStatus(t, rec, 201, "")
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token answer may be cached")
	}
	ap := decode[panel.NewAppPassword](t, rec)
	if !config.LooksLikeAppPassword(ap.Password) || ap.Name != "Phone" {
		t.Fatalf("created %s", rec.Body)
	}
	_, secret, _ := config.ParseAppPassword(ap.Password)
	rec = e.as("GET", "/api/mail/mailboxes/"+id+"/app-passwords", "")
	wantBody(t, rec, 200, "Phone")
	if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), `"password"`) {
		t.Fatalf("list shows the token: %s", rec.Body)
	}
	if rec := e.as("GET", "/api/activity", ""); strings.Contains(rec.Body.String(), secret) {
		t.Fatal("activity shows the token")
	}
	wantStatus(t, e.as("DELETE", "/api/mail/mailboxes/"+id+"/app-passwords/"+ap.ID, ""), 204, "")
	wantStatus(t, e.as("DELETE", "/api/mail/mailboxes/"+id+"/app-passwords/"+ap.ID, ""), 404, "")
}
