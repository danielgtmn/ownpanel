package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

func TestSharingRoutes(t *testing.T) {
	e, _ := newMailEnv(t)
	wantStatus(t, e.as("PUT", "/api/settings", `{"mail_host":"mail.example.net"}`), 200, "")
	wantStatus(t, e.as("POST", "/api/mail/domains", `{"domain":"example.com"}`), 201, "")
	rec := e.as("POST", "/api/mail/domains/example.com/mailboxes", `{"local_part":"anna","password":"mailbox-password-1"}`)
	wantStatus(t, rec, 201, "")
	anna := decode[panel.Mailbox](t, rec)
	rec = e.as("POST", "/api/mail/domains/example.com/mailboxes", `{"local_part":"team","kind":"shared","keep_sent_copy":true}`)
	wantStatus(t, rec, 201, "")
	team := decode[panel.Mailbox](t, rec)
	if team.Kind != "shared" || !team.KeepSentCopy {
		t.Fatalf("team %s", rec.Body)
	}
	id := fmt.Sprint(team.ID)

	rec = e.as("PUT", "/api/mail/mailboxes/"+id+"/grants", `{"address":"anna@example.com","rights":["read","send-as"]}`)
	wantBody(t, rec, 200, `"shared_with":[{"mailbox_id":`)
	wantStatus(t, e.as("PUT", "/api/mail/mailboxes/"+id+"/grants", `{"address":"nobody@example.org","rights":["read"]}`), 400, "no personal mailbox")
	rec = e.as("GET", fmt.Sprintf("/api/mail/mailboxes/%s", anna.ID), "")
	wantBody(t, rec, 200, `"access_to":[{"mailbox_id":"`+id+`"`)
	wantBody(t, e.as("PUT", "/api/mail/mailboxes/"+id+"/keep-sent-copy", `{"keep_sent_copy":false}`), 200, `"keep_sent_copy":false`)
	wantStatus(t, e.as("PUT", "/api/mail/mailboxes/"+id+"/keep-sent-copy", `{}`), 400, "")
	rec = e.as("DELETE", fmt.Sprintf("/api/mail/mailboxes/%s/grants/%s", id, anna.ID), "")
	wantBody(t, rec, 200, `"shared_with":[]`)
	wantStatus(t, e.as("DELETE", "/api/mail/mailboxes/"+id+"/grants/x", ""), 404, "")

	// Kinds.
	wantStatus(t, e.as("PUT", "/api/mail/mailboxes/"+id+"/kind", `{"kind":"personal"}`), 400, "password")
	rec = e.as("PUT", "/api/mail/mailboxes/"+id+"/kind", `{"kind":"personal","password":"mailbox-password-2"}`)
	wantBody(t, rec, 200, `"kind":"personal"`)
	if strings.Contains(rec.Body.String(), "mailbox-password") || strings.Contains(rec.Body.String(), "argon2") {
		t.Fatalf("secret in %s", rec.Body)
	}
	wantBody(t, e.as("PUT", fmt.Sprintf("/api/mail/mailboxes/%s/kind", anna.ID), `{"kind":"shared"}`), 200, `"kind":"shared"`)

	// Groups.
	rec = e.as("POST", "/api/mail/domains/example.com/aliases", `{"local_part":"staff","targets":["team@example.com"],"sender_policy":"members"}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"sender_policy":"members"`) {
		t.Fatalf("group %d %s", rec.Code, rec.Body)
	}
	al := decode[panel.MailAlias](t, rec)
	rec = e.as("PUT", fmt.Sprintf("/api/mail/aliases/%s", al.ID), `{"local_part":"staff","targets":["team@example.com"],"hide_from_directory":true}`)
	wantBody(t, rec, 200, `"sender_policy":"members","hide_from_directory":true`)
	wantStatus(t, e.as("POST", "/api/mail/domains/example.com/aliases", `{"local_part":"*","targets":["team@example.com"],"sender_policy":"members"}`), 400, "catch-all")
}
