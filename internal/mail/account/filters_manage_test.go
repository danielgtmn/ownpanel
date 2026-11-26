package account

import (
	"context"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/rules"
)

// The Manage right (G3) on another mailbox opens its filters and
// out-of-office (G2) in the self-service area, checked on every request.
func TestFiltersOfManagedMailbox(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st := e.srv.Store()
	idBob, idTeam := mailtest.ID('b'), mailtest.ID('d')
	manage := []string{config.RightRead, config.RightManage}
	load := func(teamGrants []config.Grant) {
		t.Helper()
		cfg := *e.cfg
		// Grants across organisations are refused when the configuration
		// loads and ignored by the directory (TestDelegations and the
		// directory tests); Rights, which the check uses, never has them.
		cfg.Mailboxes = append(cfg.Mailboxes,
			config.Mailbox{StorageID: idTeam, Address: "team@example.org", Kind: config.KindShared, NoForwarding: true, Grants: teamGrants})
		if err := e.srv.Load(ctx, &cfg); err != nil {
			t.Fatal(err)
		}
	}
	load([]config.Grant{{StorageID: idAlice, Rights: manage}, {StorageID: idBob, Rights: []string{config.RightRead, config.RightWrite}}})
	alice := e.signIn(address, password)
	bob := e.signIn("bob@example.org", "bob-password-1")

	get := func(cookie, mailbox string) (int, string) {
		rec := e.do(req{method: "GET", path: "/account/api/filters?mailbox=" + mailbox, cookie: cookie})
		return rec.Code, rec.Body.String()
	}
	if code, body := get(alice, "Team@example.org"); code != 200 || !strings.Contains(body, `"mailbox":"team@example.org"`) ||
		!strings.Contains(body, `"may_forward":false`) {
		t.Fatalf("alice on team: %d %s", code, body)
	}
	// Her own mailbox by its address: as without ?mailbox=.
	if code, body := get(alice, address); code != 200 || strings.Contains(body, `"mailbox"`) {
		t.Fatalf("alice on herself: %d %s", code, body)
	}

	rec := e.do(req{method: "PUT", path: "/account/api/out-of-office?mailbox=team@example.org", cookie: alice,
		body: `{"out_of_office": {"enabled": true, "subject": "Closed", "text": "We are closed until Monday.", "zone": "+0100", "days": 3}}`})
	want(t, rec, 200, `"set_by":"alice@example.org"`)
	if name, src, ok, err := st.ActiveSieveScript(ctx, idTeam); err != nil || !ok || name != rules.ScriptName || !strings.Contains(src, "We are closed") {
		t.Fatalf("team script %q %v %v", name, ok, err)
	}
	if _, _, ok, _ := st.ActiveSieveScript(ctx, idAlice); ok {
		t.Fatal("alice's own filters changed")
	}
	// The shared mailbox's own limit on forwarding holds.
	want(t, e.do(req{method: "PUT", path: "/account/api/forwarding?mailbox=team@example.org", cookie: alice,
		body: `{"forwarding": {"enabled": true, "to": "x@elsewhere.test"}}`}), 400, "")
	want(t, e.do(req{method: "PUT", path: "/account/api/filters?mailbox=team@example.org", cookie: alice,
		body: `{"filters": [{"name": "Invoices", "conditions": [{"field": "subject", "op": "contains", "value": "invoice"}], "folder": "INBOX"}]}`}),
		200, `"name":"Invoices"`)

	// Without Manage, on a personal mailbox without a grant, or on none:
	// not found, and nothing changes.
	notFound := map[string]string{
		"bob read and write": bob + " team@example.org",
		"no grant":           alice + " bob@example.org",
		"no mailbox":         alice + " nobody@example.org",
	}
	for name, c := range notFound {
		cookie, mailbox, _ := strings.Cut(c, " ")
		if code, _ := get(cookie, mailbox); code != 404 {
			t.Errorf("%s: GET %d", name, code)
		}
		rec := e.do(req{method: "PUT", path: "/account/api/out-of-office?mailbox=" + mailbox, cookie: cookie,
			body: `{"out_of_office": {"enabled": false, "text": "", "days": 7}}`})
		if rec.Code != 404 {
			t.Errorf("%s: PUT %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if _, src, _, _ := st.ActiveSieveScript(ctx, idTeam); !strings.Contains(src, "We are closed") {
		t.Fatal("team filters changed by someone without Manage")
	}

	// The right is taken away: the next request is refused.
	load([]config.Grant{{StorageID: idAlice, Rights: []string{config.RightRead}}})
	if code, _ := get(alice, "team@example.org"); code != 404 {
		t.Fatalf("after Manage was removed: %d", code)
	}
}
