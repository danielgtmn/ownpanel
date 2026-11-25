package account

import (
	"context"
	"slices"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
)

// Delegating one's own mailbox from the self-service area (G3).
func TestDelegations(t *testing.T) {
	e := newEnv(t)
	idTeam := mailtest.ID('d')
	cfg := *e.cfg
	cfg.Domains = append(cfg.Domains, config.Domain{Name: "other.example"})
	cfg.Mailboxes = append(cfg.Mailboxes,
		config.Mailbox{StorageID: mailtest.ID('c'), Address: "carl@other.example", PasswordHash: mailtest.CheapHash("carl-password-1")},
		config.Mailbox{StorageID: idTeam, Address: "team@example.org", Kind: config.KindShared,
			Grants: []config.Grant{{StorageID: idAlice, Rights: []string{config.RightRead, config.RightSendAs}}}},
	)
	if err := e.srv.Load(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}
	cookie := e.signIn(address, password)
	v, _ := e.me(cookie)
	if len(v.SharedWith) != 0 || len(v.Access) != 1 || v.Access[0].Address != "team@example.org" || !v.Access[0].Shared ||
		v.Access[0].Folder != "Shared/team@example.org" || !slices.Equal(v.Access[0].Rights, []string{config.RightRead, config.RightSendAs}) {
		t.Fatalf("me %+v", v)
	}

	put := func(body string) string {
		rec := e.do(req{method: "PUT", path: "/account/api/delegations", body: body, cookie: cookie})
		return rec.Body.String()
	}
	for body, msg := range map[string]string{
		`{"grants":[{"address":"alice@example.org","rights":["read"]}]}`:                                                "yourself",
		`{"grants":[{"address":"carl@other.example","rights":["read"]}]}`:                                               "no mailbox carl@other.example in your organisation",
		`{"grants":[{"address":"nobody@example.org","rights":["read"]}]}`:                                               "no mailbox",
		`{"grants":[{"address":"team@example.org","rights":["read"]}]}`:                                                 "shared mailbox",
		`{"grants":[{"address":"bob@example.org","rights":[]}]}`:                                                        "Choose what",
		`{"grants":[{"address":"bob@example.org","rights":["read"]},{"address":"BOB@example.org","rights":["write"]}]}`: "twice",
	} {
		rec := e.do(req{method: "PUT", path: "/account/api/delegations", body: body, cookie: cookie})
		want(t, rec, 400, msg)
	}

	want(t, e.do(req{method: "PUT", path: "/account/api/delegations", cookie: cookie,
		body: `{"grants":[{"address":"Bob@example.org","rights":["write","send-on-behalf"]}]}`}), 200, `"shared_with":[{"address":"bob@example.org"`)
	if !e.srv.Directory().Rights(idAlice, mailtest.ID('b')).Has(config.Write | config.Read | config.SendOnBehalf) {
		t.Error("not in effect")
	}
	evs := e.accountEvents()
	last := evs[len(evs)-1]
	if last.Kind != config.EventGrantsChanged || last.Account.Grants == nil || len(*last.Account.Grants) != 1 ||
		last.Account.Revision <= 3 || last.Detail != "shared with bob@example.org (read, write, send-on-behalf)" {
		t.Fatalf("event %+v %+v", last, last.Account)
	}
	// The same list again changes nothing.
	n := len(e.accountEvents())
	put(`{"grants":[{"address":"bob@example.org","rights":["send-on-behalf","read","write"]}]}`)
	if len(e.accountEvents()) != n {
		t.Error("unchanged list recorded again")
	}
	// Bob sees it.
	bob := e.signIn("bob@example.org", "bob-password-1")
	bv, _ := e.me(bob)
	if len(bv.Access) != 1 || bv.Access[0].Address != address || bv.Access[0].Shared {
		t.Errorf("bob's access %+v", bv.Access)
	}
	// Nobody.
	want(t, e.do(req{method: "PUT", path: "/account/api/delegations", cookie: cookie, body: `{"grants":[]}`}), 200, `"shared_with":[]`)
	if e.srv.Directory().Rights(idAlice, mailtest.ID('b')) != 0 {
		t.Error("still in effect")
	}
}

// TestSharedEndsSessions: a mailbox made shared can no longer sign in, and
// its open sessions end.
func TestSharedEndsSessions(t *testing.T) {
	e := newEnv(t)
	cookie := e.signIn(address, password)
	cfg := *e.cfg
	cfg.Mailboxes = slices.Clone(cfg.Mailboxes)
	cfg.Mailboxes[0].Kind, cfg.Mailboxes[0].PasswordHash = config.KindShared, ""
	if err := e.srv.Load(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}
	if _, code := e.me(cookie); code != 401 {
		t.Errorf("session after conversion: %d", code)
	}
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: signInBody(address, password)}), 401, "Wrong address or password")
}
