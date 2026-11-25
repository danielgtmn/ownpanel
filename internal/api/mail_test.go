package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/secrets"
	"github.com/danielgtmn/ownpanel/internal/store"
)

type fakeMailService struct {
	mu        sync.Mutex
	hash      string
	loads     int
	unblocked []string
}

func (f *fakeMailService) Load(_ context.Context, cfg *config.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	f.hash = cfg.Hash()
	return nil
}

func (f *fakeMailService) Status(context.Context) (config.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return config.Status{ConfigHash: f.hash}, nil
}

func (f *fakeMailService) Events(context.Context, int64) ([]config.Event, error) { return nil, nil }

func (f *fakeMailService) Unblock(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unblocked = append(f.unblocked, id)
	return nil
}

func (f *fakeMailService) DeleteMailbox(context.Context, string) error { return nil }

func (f *fakeMailService) CheckOutbound(context.Context) (config.OutboundCheck, error) {
	return config.OutboundCheck{OK: true}, nil
}

// mxHere: every domain's MX points to mail.example.net.
type mxHere struct{}

func (mxHere) LookupMX(context.Context, string) ([]*net.MX, error) {
	return []*net.MX{{Host: "mail.example.net.", Pref: 10}}, nil
}

// noRecords answers every address, TXT, SRV and reverse lookup with "no
// such record", so nothing goes to the network.
type noRecords struct{}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (noRecords) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return nil, notFound(host)
}
func (noRecords) LookupTXT(_ context.Context, name string) ([]string, error) {
	return nil, notFound(name)
}
func (noRecords) LookupSRV(_ context.Context, _, _, name string) (string, []*net.SRV, error) {
	return "", nil, notFound(name)
}
func (noRecords) LookupAddr(_ context.Context, addr string) ([]string, error) {
	return nil, notFound(addr)
}

func newMailEnv(t *testing.T) (*env, *fakeMailService) {
	t.Helper()
	mail := &fakeMailService{}
	return newMailEnvWith(t, mail), mail
}

// newMailEnvWith: a panel with the mail service mail, set up with root.
func newMailEnvWith(t *testing.T, mail panel.MailService) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secrets.Open(filepath.Join(dir, secrets.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := panel.New(panel.Config{
		Store:         st,
		Deploys:       &deploy.Manager{Base: filepath.Join(dir, "sites")},
		Web:           nopWeb{},
		Caddy:         caddy.Options{AdminListen: "127.0.0.1:2019", TLSMode: caddy.TLSOff, HTTPPort: 8080, HTTPSPort: 8443},
		SetupCodeFile: filepath.Join(dir, "setup-code"),
		Secrets:       box,
		Mail:          mail,
		MXResolver:    mxHere{},
		Resolver:      noRecords{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureSetupCode(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, h: (&Server{Panel: svc}).Handler(), svc: svc, code: filepath.Join(dir, "setup-code")}
	e.cookie = e.setup("root", "root-password-1")
	return e
}

var (
	mailDomainKeys = []string{"domain", "owner", "active", "activated_at", "checked_at", "problem", "mailboxes", "aliases", "dkim_selector", "dkim_record", "created_at"}
	mailboxKeys    = []string{"id", "address", "domain", "local_part", "owner", "quota_bytes", "send_per_hour", "send_per_day", "created_at", "usage"}
	aliasKeys      = []string{"id", "address", "domain", "local_part", "catch_all", "targets", "owner", "created_at"}
)

func TestMailRoutes(t *testing.T) {
	e, mail := newMailEnv(t)

	// Status before the mail host is set; anyone who sees mail reads it.
	rec := e.as("GET", "/api/mail/status", "")
	wantStatus(t, rec, 200, "")
	if st := decode[panel.MailStatus](t, rec); !st.ServiceSetUp || st.MailHost != "" || !strings.Contains(st.Problem, "not set yet") {
		t.Fatalf("status %s", rec.Body)
	}
	rec = e.as("PUT", "/api/settings", `{"mail_host":"Mail.Example.net","postmaster_address":"admin@example.org"}`)
	wantStatus(t, rec, 200, "")
	if s := decode[panel.Settings](t, rec); s.MailHost != "mail.example.net" || s.MailHostLocked || s.PostmasterAddress != "admin@example.org" {
		t.Fatalf("settings %s", rec.Body)
	}

	rec = e.as("POST", "/api/mail/domains", `{"domain":"Example.com"}`)
	wantStatus(t, rec, 201, "")
	wantKeys(t, rec.Body.Bytes(), mailDomainKeys...)
	if d := decode[panel.MailDomain](t, rec); d.Domain != "example.com" || !d.Active || d.Owner != "root" {
		t.Fatalf("enabled %s", rec.Body)
	}
	wantStatus(t, e.as("POST", "/api/mail/domains", `{"domain":"example.com"}`), 409, "already enabled")
	wantStatus(t, e.as("POST", "/api/mail/domains", `{"domain":5}`), 400, "JSON")
	rec = e.as("GET", "/api/mail/domains", "")
	if list := decode[[]json.RawMessage](t, rec); rec.Code != 200 || len(list) != 1 {
		t.Fatalf("list %s", rec.Body)
	}
	wantStatus(t, e.as("GET", "/api/mail/domains/example.com", ""), 200, "")
	wantStatus(t, e.as("POST", "/api/mail/domains/example.com/check", ""), 200, "")
	wantStatus(t, e.as("GET", "/api/mail/domains/nope.example", ""), 404, "")

	// Mailboxes.
	rec = e.as("POST", "/api/mail/domains/example.com/mailboxes", `{"local_part":"Info","password":"mailbox-password-1","send_per_day":300}`)
	wantStatus(t, rec, 201, "")
	wantKeys(t, rec.Body.Bytes(), mailboxKeys...)
	box := decode[panel.Mailbox](t, rec)
	if box.Address != "info@example.com" || box.SendPerDay != 300 || box.QuotaBytes != panel.DefaultQuotaBytes {
		t.Fatalf("mailbox %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "argon2") || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("mailbox answer contains a secret: %s", rec.Body)
	}
	wantStatus(t, e.as("POST", "/api/mail/domains/example.com/mailboxes", `{"local_part":"x","password":"short"}`), 400, "password")
	for _, path := range []string{"/api/mail/mailboxes", "/api/mail/domains/example.com/mailboxes"} {
		rec = e.as("GET", path, "")
		list := decode[[]json.RawMessage](t, rec)
		if rec.Code != 200 || len(list) != 1 {
			t.Fatalf("%s: %s", path, rec.Body)
		}
		wantKeys(t, list[0], mailboxKeys...)
	}
	id := fmt.Sprint(box.ID)
	wantStatus(t, e.as("GET", "/api/mail/mailboxes/"+id, ""), 200, "")
	rec = e.as("PATCH", "/api/mail/mailboxes/"+id, `{"send_per_hour":20}`)
	wantStatus(t, rec, 200, "")
	if m := decode[panel.Mailbox](t, rec); m.SendPerHour != 20 || m.SendPerDay != 300 {
		t.Fatalf("limits %s", rec.Body)
	}
	wantStatus(t, e.as("POST", "/api/mail/mailboxes/"+id+"/password", `{"password":"another-password-1"}`), 204, "")
	wantStatus(t, e.as("POST", "/api/mail/mailboxes/"+id+"/unblock", ""), 204, "")
	if len(mail.unblocked) != 1 {
		t.Fatal("unblock not sent to the mail service")
	}
	wantStatus(t, e.as("GET", "/api/mail/mailboxes/x", ""), 404, "")

	// Aliases.
	rec = e.as("POST", "/api/mail/domains/example.com/aliases", `{"local_part":"sales","targets":["info@example.com"]}`)
	wantStatus(t, rec, 201, "")
	wantKeys(t, rec.Body.Bytes(), aliasKeys...)
	al := decode[panel.MailAlias](t, rec)
	wantStatus(t, e.as("POST", "/api/mail/domains/example.com/aliases", `{"local_part":"info","targets":["a@example.org"]}`), 409, "mailbox")
	rec = e.as("PUT", fmt.Sprintf("/api/mail/aliases/%d", al.ID), `{"local_part":"*","targets":["info@example.com","boss@example.org"]}`)
	wantStatus(t, rec, 200, "")
	if a := decode[panel.MailAlias](t, rec); !a.CatchAll || len(a.Targets) != 2 {
		t.Fatalf("alias %s", rec.Body)
	}
	for _, path := range []string{"/api/mail/aliases", "/api/mail/domains/example.com/aliases", fmt.Sprintf("/api/mail/aliases/%d", al.ID)} {
		wantStatus(t, e.as("GET", path, ""), 200, "")
	}
	wantStatus(t, e.as("DELETE", fmt.Sprintf("/api/mail/aliases/%d", al.ID), ""), 204, "")

	// Read-only tokens read, but change nothing; tokens limited to sites see nothing.
	_, read := e.createToken("read", "null")
	wantStatus(t, e.do(req{method: "GET", path: "/api/mail/domains", token: read}), 200, "")
	wantStatus(t, e.do(req{method: "POST", path: "/api/mail/domains", body: `{"domain":"example.org"}`, token: read}), 403, "only read")
	wantStatus(t, e.as("PUT", "/api/sites/s", `{"domains":["s.example.org"]}`), 200, "")
	_, limited := e.createToken("write", `["s"]`)
	wantStatus(t, e.do(req{method: "GET", path: "/api/mail/status", token: limited}), 403, "limited to certain sites")

	// Deleting.
	wantStatus(t, e.as("DELETE", "/api/mail/mailboxes/"+id, ""), 204, "")
	wantStatus(t, e.as("DELETE", "/api/mail/domains/example.com", `{"confirm":"wrong"}`), 400, "repeat the domain")
	wantStatus(t, e.as("DELETE", "/api/mail/domains/example.com", `{"confirm":"example.com"}`), 204, "")
	wantStatus(t, e.as("GET", "/api/mail/domains/example.com", ""), 404, "")
	if mail.loads == 0 {
		t.Fatal("nothing pushed")
	}
	rec = e.as("POST", "/api/mail/checks/outbound", "")
	wantStatus(t, rec, 200, "")
}

func TestMailRecordsAndHealthRoutes(t *testing.T) {
	e, _ := newMailEnv(t)
	wantStatus(t, e.as("PUT", "/api/settings", `{"mail_host":"mail.example.net"}`), 200, "")
	wantStatus(t, e.as("POST", "/api/mail/domains", `{"domain":"example.com"}`), 201, "")

	rec := e.as("GET", "/api/mail/domains/example.com/records", "")
	wantStatus(t, rec, 200, "")
	wantKeys(t, rec.Body.Bytes(), "domain", "mail_host", "records", "can_apply", "connection_id", "zone", "apply_note")
	r := decode[panel.MailRecords](t, rec)
	if r.Domain != "example.com" || r.MailHost != "mail.example.net" || r.CanApply || len(r.Records) != 8 || r.ApplyNote == "" {
		t.Fatalf("records %s", rec.Body)
	}
	var raw struct {
		Records []json.RawMessage `json:"records"`
	}
	json.Unmarshal(rec.Body.Bytes(), &raw)
	wantKeys(t, raw.Records[0], "kind", "type", "name", "fqdn", "want", "found", "status", "note", "optional")
	if r.Records[0].Kind != "mx" || r.Records[0].Status != "ok" || r.Records[1].Kind != "spf" || r.Records[1].Status != "missing" {
		t.Fatalf("records %s", rec.Body)
	}
	// No DNS connection holds the zone: nothing can be applied.
	wantStatus(t, e.as("POST", "/api/mail/domains/example.com/records", `{"confirm":false}`), 409, "DNS connections")
	wantStatus(t, e.as("POST", "/api/mail/domains/example.com/records", ""), 409, "DNS connections")
	wantStatus(t, e.as("POST", "/api/mail/domains/example.com/records", `{"confirm":"yes"}`), 400, "JSON")
	wantStatus(t, e.as("GET", "/api/mail/domains/nope.example/records", ""), 404, "")

	rec = e.as("GET", "/api/mail/health", "")
	wantStatus(t, rec, 200, "")
	wantKeys(t, rec.Body.Bytes(), "mail_host", "checks")
	h := decode[panel.MailHealth](t, rec)
	if len(h.Checks) != 10 || h.Checks[0].ID != "service" || h.Checks[0].State != "warning" && h.Checks[0].State != "ok" {
		t.Fatalf("health %s", rec.Body)
	}
	var rawH struct {
		Checks []json.RawMessage `json:"checks"`
	}
	json.Unmarshal(rec.Body.Bytes(), &rawH)
	wantKeys(t, rawH.Checks[0], "id", "state", "found", "expected", "detail", "can_apply")

	// Read-only tokens read records, but do not apply them.
	_, read := e.createToken("read", "null")
	wantStatus(t, e.do(req{method: "GET", path: "/api/mail/domains/example.com/records", token: read}), 200, "")
	wantStatus(t, e.do(req{method: "POST", path: "/api/mail/domains/example.com/records", body: `{"confirm":true}`, token: read}), 403, "only read")
	wantStatus(t, e.do(req{method: "GET", path: "/api/mail/health", token: read}), 200, "")
	wantStatus(t, e.as("PUT", "/api/sites/s", `{"domains":["s.example.org"]}`), 200, "")
	_, limited := e.createToken("write", `["s"]`)
	wantStatus(t, e.do(req{method: "GET", path: "/api/mail/domains/example.com/records", token: limited}), 403, "limited")
	wantStatus(t, e.do(req{method: "GET", path: "/api/mail/health", token: limited}), 403, "Only admins")
}
