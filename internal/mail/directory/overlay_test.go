package directory

import (
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

const sidA = "0123456789abcdef0123456789abcdef"

func pushedCfg(rev int64, hash, name string, aps ...string) *config.Config {
	m := config.Mailbox{StorageID: sidA, Address: "a@example.com", PasswordHash: hash, DisplayName: name, Revision: rev}
	for _, id := range aps {
		m.AppPasswords = append(m.AppPasswords, config.AppPassword{ID: id, Name: id})
	}
	return &config.Config{
		Hostname: "mail.example.net",
		Domains:  []config.Domain{{Name: "example.com", Organisation: "org-1"}, {Name: "example.de"}},
		Mailboxes: []config.Mailbox{m,
			{StorageID: "fedcba9876543210fedcba9876543210", Address: "b@example.de"}},
	}
}

func strp(s string) *string { return &s }

func TestOverlayKeepsLocalChangesUntilPushed(t *testing.T) {
	o := &Overlay{}
	// The mailbox changes its password (rev 6) while the panel is at 5.
	r := o.NextRevision(5)
	if r != 6 {
		t.Fatalf("next %d", r)
	}
	o.Apply(config.AccountChange{Revision: r, PasswordHash: "new"}, time.Now())
	// Then creates an app password (rev 7) and names itself (rev 8).
	o.Apply(config.AccountChange{Revision: o.NextRevision(5), AppPassword: &config.AppPassword{ID: "local", Name: "Phone"}}, time.Now())
	o.Apply(config.AccountChange{Revision: o.NextRevision(5), DisplayName: strp("Anna")}, time.Now())
	if o.Revision != 8 {
		t.Fatalf("revision %d", o.Revision)
	}

	// A push the panel built before it saw any of it (old values, rev 5)
	// does not undo them.
	cfg := pushedCfg(5, "old", "", "p1")
	o.Prune(5)
	m := Merge(cfg, map[string]*Overlay{sidA: o}).Mailboxes[0]
	if m.PasswordHash != "new" || m.DisplayName != "Anna" || len(m.AppPasswords) != 2 || m.AppPasswords[1].ID != "local" {
		t.Fatalf("merged %+v", m)
	}
	if cfg.Mailboxes[0].PasswordHash != "old" || len(cfg.Mailboxes[0].AppPasswords) != 1 {
		t.Fatal("Merge changed the pushed configuration")
	}

	// The panel recorded the first two (rev 7) and, in the same moment, an
	// admin reset the password: the pushed value wins for what rev 7
	// includes; the display name (rev 8) stays local.
	cfg = pushedCfg(7, "admin", "", "p1", "local")
	o.Prune(7)
	if o.PasswordHash != nil || len(o.Added) != 0 || o.DisplayName == nil {
		t.Fatalf("pruned %+v", o)
	}
	m = Merge(cfg, map[string]*Overlay{sidA: o}).Mailboxes[0]
	if m.PasswordHash != "admin" || m.DisplayName != "Anna" || len(m.AppPasswords) != 2 {
		t.Fatalf("merged %+v", m)
	}

	// Revoking hides an app password the push still carries.
	o.Apply(config.AccountChange{Revision: o.NextRevision(7), AppPasswordID: "p1"}, time.Now())
	m = Merge(cfg, map[string]*Overlay{sidA: o}).Mailboxes[0]
	if len(m.AppPasswords) != 1 || m.AppPasswords[0].ID != "local" {
		t.Fatalf("after revoke %+v", m.AppPasswords)
	}
	// Creating and revoking before any push leaves nothing behind.
	o.Apply(config.AccountChange{Revision: o.NextRevision(7), AppPassword: &config.AppPassword{ID: "tmp", Name: "x"}}, time.Now())
	o.Apply(config.AccountChange{Revision: o.NextRevision(7), AppPasswordID: "tmp"}, time.Now())
	m = Merge(cfg, map[string]*Overlay{sidA: o}).Mailboxes[0]
	if len(m.AppPasswords) != 1 {
		t.Fatalf("tmp %+v", m.AppPasswords)
	}

	// Once the panel's push includes everything, the overlay is empty.
	o.Prune(o.Revision)
	if !o.Empty() {
		t.Fatalf("not empty: %+v", o)
	}
	if next := o.NextRevision(o.Revision); next != o.Revision+1 {
		t.Fatal("revisions must keep growing")
	}
}

func TestOrganisationOf(t *testing.T) {
	d := New(pushedCfg(0, "h", ""))
	if o, ok := d.OrganisationOf(sidA); !ok || o != "org-1" {
		t.Fatalf("a: %q %v", o, ok)
	}
	if o, ok := d.OrganisationOf("fedcba9876543210fedcba9876543210"); !ok || o != config.StandaloneOrganisation("example.de") {
		t.Fatalf("b: %q %v", o, ok)
	}
	if _, ok := d.OrganisationOf("00000000000000000000000000000000"); ok {
		t.Fatal("unknown mailbox")
	}
}
