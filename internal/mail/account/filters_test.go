package account

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/deliver"
	"github.com/danielgtmn/ownpanel/internal/mail/rules"
)

func (e *env) filters(cookie string) filtersView {
	e.t.Helper()
	rec := e.do(req{method: "GET", path: "/account/api/filters", cookie: cookie})
	want(e.t, rec, 200, "")
	var v filtersView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

func TestFilters(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st := e.srv.Store()
	c := e.signIn(address, password)

	v := e.filters(c)
	if !v.Rules.Managed || v.Rules.OutOfOffice.Enabled || len(v.Folders) == 0 || v.Folders[0] != "INBOX" || !v.MayForward {
		t.Fatalf("empty: %+v", v)
	}
	// Signed out: nothing.
	want(t, e.do(req{method: "GET", path: "/account/api/filters"}), 401, "")

	// Out-of-office.
	want(t, e.do(req{method: "PUT", path: "/account/api/out-of-office", cookie: c,
		body: `{"out_of_office": {"enabled": true, "text": "", "days": 7}}`}), 400, "Write the text")
	rec := e.do(req{method: "PUT", path: "/account/api/out-of-office", cookie: c,
		body: `{"out_of_office": {"enabled": true, "subject": "Away", "text": "Back on Monday.", "start": "2026-12-01", "end": "2026-12-24", "zone": "+0100", "days": 3, "set_by": "forged"}}`})
	want(t, rec, 200, `"subject":"Away"`)
	if strings.Contains(rec.Body.String(), "forged") {
		t.Fatal("set_by taken from the person")
	}
	name, src, ok, err := st.ActiveSieveScript(ctx, idAlice)
	if err != nil || !ok || name != rules.ScriptName || !strings.Contains(src, `vacation :days 3`) {
		t.Fatalf("script %q %v %v:\n%s", name, ok, err, src)
	}

	// Filters: a rule and forwarding.
	rec = e.do(req{method: "PUT", path: "/account/api/filters", cookie: c, body: `{"filters": [
		{"name": "News", "conditions": [{"field": "from", "op": "contains", "value": "news@"}], "folder": "Archive", "mark_read": true}]}`})
	want(t, rec, 200, `"name":"News"`)
	want(t, e.do(req{method: "PUT", path: "/account/api/filters", cookie: c, body: `{"filters": [
		{"name": "x", "conditions": [{"field": "from", "op": "matches", "value": "a"}], "folder": "Archive"}]}`}), 400, "")
	want(t, e.do(req{method: "PUT", path: "/account/api/forwarding", cookie: c,
		body: `{"forwarding": {"enabled": true, "to": "me@elsewhere.test", "keep_copy": true}}`}), 200, `"to":"me@elsewhere.test"`)
	v = e.filters(c)
	if !v.Rules.OutOfOffice.Enabled || len(v.Rules.Filters) != 1 || !v.Rules.Forwarding.Enabled || v.Rules.Script != "" {
		t.Fatalf("after changes: %+v", v.Rules)
	}

	// The domain owner sets out-of-office through the admin socket path
	// (server.UpdateRules); the person sees who.
	if _, err := e.srv.UpdateRules(ctx, idAlice, false, func(s *rules.Settings) error {
		s.OutOfOffice = config.OutOfOffice{Enabled: true, Text: "Left.", Days: 7, SetBy: "admin"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if v := e.filters(c); v.Rules.OutOfOffice.SetBy != "admin" || len(v.Rules.Filters) != 1 {
		t.Fatalf("set by: %+v", v.Rules)
	}

	// A script written in a mail program: read-only here, replaced only
	// on request.
	if err := st.PutSieveScript(ctx, idAlice, "mine", `keep;`, deliver.MaxScripts, true); err != nil {
		t.Fatal(err)
	}
	v = e.filters(c)
	if v.Rules.Managed || v.Rules.ActiveScript != "mine" || v.Rules.Script != "keep;" {
		t.Fatalf("hand written: %+v", v.Rules)
	}
	want(t, e.do(req{method: "PUT", path: "/account/api/out-of-office", cookie: c,
		body: `{"out_of_office": {"enabled": false, "text": "", "days": 7}}`}), 409, "mail program")
	want(t, e.do(req{method: "PUT", path: "/account/api/out-of-office", cookie: c,
		body: `{"out_of_office": {"enabled": false, "text": "", "days": 7}, "replace": true}`}), 200, `"managed":true`)
	if name, _, _, _ := st.ActiveSieveScript(ctx, idAlice); name != rules.ScriptName {
		t.Fatalf("active %q", name)
	}

	// Turning everything off removes the script.
	want(t, e.do(req{method: "PUT", path: "/account/api/filters", cookie: c, body: `{"filters": []}`}), 200, "")
	want(t, e.do(req{method: "PUT", path: "/account/api/forwarding", cookie: c, body: `{"forwarding": {"enabled": false, "to": ""}}`}), 200, "")
	if _, _, ok, _ := st.ActiveSieveScript(ctx, idAlice); ok {
		t.Fatal("script still active")
	}

	// Forwarding not allowed for the mailbox.
	e.cfg.Mailboxes[0].NoForwarding = true
	if err := e.srv.Load(ctx, e.cfg); err != nil {
		t.Fatal(err)
	}
	if v := e.filters(c); v.MayForward {
		t.Fatal("may forward")
	}
	want(t, e.do(req{method: "PUT", path: "/account/api/forwarding", cookie: c,
		body: `{"forwarding": {"enabled": true, "to": "me@elsewhere.test"}}`}), 400, "turned off")

	// CSRF rules apply as everywhere.
	want(t, e.do(req{method: "PUT", path: "/account/api/filters", cookie: c, body: `{"filters": []}`,
		header: map[string]string{CSRFHeader: ""}}), 403, "")
}
