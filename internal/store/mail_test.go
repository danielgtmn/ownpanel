package store

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func dkim(sel string) DKIMKey {
	return DKIMKey{Selector: sel, PrivateKey: []byte("sealed"), PublicKey: "MIIB", State: DKIMActive}
}

func TestMailDomainsMailboxesAliases(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		if _, err := s.CreateUser(ctx, "root", "", RoleAdmin, false); err != nil {
			t.Fatal(err)
		}
		bob, err := s.CreateUser(ctx, "bob", "", RoleMember, false)
		if err != nil {
			t.Fatal(err)
		}

		d, err := s.CreateMailDomain(ctx, "example.com", bob.ID, dkim("op202611"), 2)
		if err != nil {
			t.Fatal(err)
		}
		if d.Domain != "example.com" || d.Owner != "bob" || d.Active || d.ActivatedAt != nil || d.CreatedAt.IsZero() {
			t.Fatalf("created %+v", d)
		}
		if _, err := s.CreateMailDomain(ctx, "example.com", bob.ID, dkim("op202611"), 0); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate domain: %v", err)
		}
		if _, err := s.CreateMailDomain(ctx, "example.org", bob.ID, dkim("op202611"), 2); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateMailDomain(ctx, "example.net", bob.ID, dkim("op202611"), 2); !errors.Is(err, ErrConflict) {
			t.Fatalf("over the limit: %v", err)
		}
		keys, err := s.DKIMKeys(ctx, "example.com")
		if err != nil || len(keys) != 1 || string(keys[0].PrivateKey) != "sealed" || keys[0].State != DKIMActive {
			t.Fatalf("keys %+v %v", keys, err)
		}

		// Activation is recorded and kept.
		at := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
		if err := s.RecordMailDomainCheck(ctx, "example.com", at, false, "no MX"); err != nil {
			t.Fatal(err)
		}
		d, _ = s.MailDomain(ctx, "example.com")
		if d.Active || d.CheckError != "no MX" || d.CheckedAt == nil || !d.CheckedAt.Equal(at) {
			t.Fatalf("after a failed check %+v", d)
		}
		if err := s.RecordMailDomainCheck(ctx, "example.com", at.Add(time.Hour), true, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordMailDomainCheck(ctx, "example.com", at.Add(2*time.Hour), false, "later problem"); err != nil {
			t.Fatal(err)
		}
		d, _ = s.MailDomain(ctx, "example.com")
		if !d.Active || d.ActivatedAt == nil || !d.ActivatedAt.Equal(at.Add(time.Hour)) {
			t.Fatalf("after activation %+v", d)
		}
		if err := s.RecordMailDomainCheck(ctx, "missing.com", at, true, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing domain: %v", err)
		}

		// Mailboxes.
		mb := Mailbox{StorageID: "00112233445566778899aabbccddeeff", Domain: "example.com", LocalPart: "info",
			PasswordHash: "h", QuotaBytes: 5 << 30, SendPerHour: 100, SendPerDay: 500}
		m1, err := s.CreateMailbox(ctx, mb, 2)
		if err != nil {
			t.Fatal(err)
		}
		if m1.ID == "" || m1.Owner != "bob" || m1.OwnerID != bob.ID || m1.QuotaBytes != 5<<30 {
			t.Fatalf("mailbox %+v", m1)
		}
		dup := mb
		dup.StorageID = "ffeeddccbbaa99887766554433221100"
		if _, err := s.CreateMailbox(ctx, dup, 0); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate local part: %v", err)
		}
		sameID := mb
		sameID.LocalPart = "other"
		if _, err := s.CreateMailbox(ctx, sameID, 0); !errors.Is(err, ErrConflict) {
			t.Fatalf("reused storage id: %v", err)
		}
		m2 := mb
		m2.StorageID, m2.Domain, m2.LocalPart = "ffeeddccbbaa99887766554433221100", "example.org", "me"
		if _, err := s.CreateMailbox(ctx, m2, 2); err != nil {
			t.Fatal(err)
		}
		m3 := m2
		m3.StorageID, m3.LocalPart = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "you"
		if _, err := s.CreateMailbox(ctx, m3, 2); !errors.Is(err, ErrConflict) {
			t.Fatalf("over the mailbox limit: %v", err)
		}
		if err := s.SetMailboxLimits(ctx, m1.ID, 1<<30, 10, 20); err != nil {
			t.Fatal(err)
		}
		if err := s.SetMailboxPassword(ctx, m1.ID, "h2"); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Mailbox(ctx, m1.ID)
		if got.QuotaBytes != 1<<30 || got.SendPerHour != 10 || got.SendPerDay != 20 || got.PasswordHash != "h2" {
			t.Fatalf("updated %+v", got)
		}
		if err := s.SetMailboxPassword(ctx, "999", "h"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing mailbox: %v", err)
		}
		all, _ := s.Mailboxes(ctx, "")
		if len(all) != 2 || all[0].ID != m1.ID {
			t.Fatalf("all mailboxes %+v", all)
		}

		// Aliases: never the local part of a mailbox, and the other way round.
		if _, err := s.CreateMailAlias(ctx, MailAlias{Domain: "example.com", LocalPart: "info", Targets: []string{"a@b.com"}}); !errors.Is(err, ErrConflict) {
			t.Fatalf("alias over a mailbox: %v", err)
		}
		a, err := s.CreateMailAlias(ctx, MailAlias{Domain: "example.com", LocalPart: "sales", Targets: []string{"info@example.com", "x@y.org"}})
		if err != nil {
			t.Fatal(err)
		}
		if a.Owner != "bob" || !slices.Equal(a.Targets, []string{"info@example.com", "x@y.org"}) {
			t.Fatalf("alias %+v", a)
		}
		mbAlias := mb
		mbAlias.StorageID, mbAlias.LocalPart = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "sales"
		if _, err := s.CreateMailbox(ctx, mbAlias, 0); !errors.Is(err, ErrConflict) {
			t.Fatalf("mailbox over an alias: %v", err)
		}
		if _, err := s.CreateMailAlias(ctx, MailAlias{Domain: "example.com", LocalPart: "sales", Targets: []string{"a@b.com"}}); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate alias: %v", err)
		}
		catch, err := s.CreateMailAlias(ctx, MailAlias{Domain: "example.com", LocalPart: "*", Targets: []string{"info@example.com"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateMailAlias(ctx, MailAlias{ID: catch.ID, LocalPart: "info", Targets: []string{"a@b.com"}}); !errors.Is(err, ErrConflict) {
			t.Fatalf("alias renamed onto a mailbox: %v", err)
		}
		if _, err := s.UpdateMailAlias(ctx, MailAlias{ID: catch.ID, LocalPart: "sales", Targets: []string{"a@b.com"}}); !errors.Is(err, ErrConflict) {
			t.Fatalf("alias renamed onto another alias: %v", err)
		}
		up, err := s.UpdateMailAlias(ctx, MailAlias{ID: catch.ID, LocalPart: "*", Targets: []string{"z@example.com"}})
		if err != nil || up.Domain != "example.com" || !slices.Equal(up.Targets, []string{"z@example.com"}) {
			t.Fatalf("update %+v %v", up, err)
		}
		if err := s.DeleteMailAlias(ctx, catch.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteMailAlias(ctx, catch.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted twice: %v", err)
		}
		d, _ = s.MailDomain(ctx, "example.com")
		if d.Mailboxes != 1 || d.Aliases != 1 {
			t.Fatalf("counts %+v", d)
		}

		// A user who owns mail domains cannot be deleted.
		if err := s.DeleteUser(ctx, bob.ID); !errors.Is(err, ErrConflict) {
			t.Fatalf("delete owner: %v", err)
		}

		// Deleting keeps the storage ids for the mail service.
		id, err := s.DeleteMailbox(ctx, m1.ID)
		if err != nil || id != mb.StorageID {
			t.Fatalf("delete mailbox %q %v", id, err)
		}
		ids, err := s.DeleteMailDomain(ctx, "example.org")
		if err != nil || !slices.Equal(ids, []string{"ffeeddccbbaa99887766554433221100"}) {
			t.Fatalf("delete domain %v %v", ids, err)
		}
		if _, err := s.DeleteMailDomain(ctx, "example.org"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted twice: %v", err)
		}
		pending, _ := s.PendingMailDeletes(ctx)
		if len(pending) != 2 {
			t.Fatalf("pending %v", pending)
		}
		if err := s.DonePendingMailDelete(ctx, mb.StorageID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DeleteMailDomain(ctx, "example.com"); err != nil {
			t.Fatal(err)
		}
		if keys, _ := s.DKIMKeys(ctx, "example.com"); len(keys) != 0 {
			t.Fatalf("keys survive their domain: %v", keys)
		}
		if a, _ := s.MailAliases(ctx, ""); len(a) != 0 {
			t.Fatalf("aliases survive their domain: %v", a)
		}
		pending, _ = s.PendingMailDeletes(ctx)
		if !slices.Equal(pending, []string{"ffeeddccbbaa99887766554433221100"}) {
			t.Fatalf("pending %v", pending)
		}
		if err := s.DeleteUser(ctx, bob.ID); err != nil {
			t.Fatalf("delete user without mail domains: %v", err)
		}
	})
}
