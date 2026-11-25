package store

import (
	"errors"
	"testing"
)

// TestGroupMailDomain: the check that an organisation holds only domains
// of the domain's owner and the change are one transaction.
func TestGroupMailDomain(t *testing.T) {
	s, _ := open(t)
	root, _ := s.CreateUser(ctx, "root", "pw-hash", RoleAdmin, false)
	bob, _ := s.CreateUser(ctx, "bob", "pw-hash", RoleMember, false)
	for _, d := range []struct {
		name  string
		owner int64
	}{{"bob.example", bob.ID}, {"bob2.example", bob.ID}, {"root.example", root.ID}} {
		if _, err := s.CreateMailDomain(ctx, d.name, d.owner, dkim("op202611"), 0); err != nil {
			t.Fatal(err)
		}
	}
	org, err := s.CreateOrganisation(ctx, "org-1", bob.ID, "Bob", 0)
	if err != nil {
		t.Fatal(err)
	}
	before, mixed, err := s.GroupMailDomain(ctx, "bob.example", org.ID, false)
	if err != nil || mixed || len(before.Domains) != 0 || before.Name != "Bob" {
		t.Fatalf("own domain: %+v %v %v", before, mixed, err)
	}
	// Another owner's domain: refused unless allowed, and nothing changes.
	if _, _, err := s.GroupMailDomain(ctx, "root.example", org.ID, false); !errors.Is(err, ErrMixedOwners) {
		t.Fatalf("mixed without allowing: %v", err)
	}
	if o, _ := s.Organisation(ctx, org.ID); len(o.Domains) != 1 {
		t.Fatalf("changed anyway: %v", o.Domains)
	}
	if _, mixed, err := s.GroupMailDomain(ctx, "root.example", org.ID, true); err != nil || !mixed {
		t.Fatalf("mixed allowed: %v %v", mixed, err)
	}
	// Now the organisation holds root's domain: bob's next one is mixed.
	if _, _, err := s.GroupMailDomain(ctx, "bob2.example", org.ID, false); !errors.Is(err, ErrMixedOwners) {
		t.Fatalf("into a mixed organisation: %v", err)
	}
	if _, _, err := s.GroupMailDomain(ctx, "bob2.example", "org-missing", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing organisation: %v", err)
	}
	if _, _, err := s.GroupMailDomain(ctx, "missing.example", org.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing domain: %v", err)
	}
	if _, _, err := s.GroupMailDomain(ctx, "bob.example", "", false); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.MailDomain(ctx, "bob.example"); d.OrganisationID != "" {
		t.Fatal("still grouped")
	}
}
