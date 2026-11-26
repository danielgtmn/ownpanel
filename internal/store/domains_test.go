package store

import (
	"errors"
	"testing"
	"time"
)

func TestDomains(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		s.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
		alice, err := s.CreateUser(ctx, "alice", "", RoleAdmin, false)
		if err != nil {
			t.Fatal(err)
		}
		bob, err := s.CreateUser(ctx, "bob", "", RoleMember, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range []struct {
			user   string
			domain string
			new    bool
		}{{alice.ID, "example.com", true}, {alice.ID, "example.com", false}, {alice.ID, "alpha.org", true}, {bob.ID, "example.com", true}} {
			created, err := s.AddDomain(ctx, step.user, step.domain)
			if err != nil || created != step.new {
				t.Fatalf("add %s %s: %v %v", step.user, step.domain, created, err)
			}
		}
		list, err := s.Domains(ctx, alice.ID)
		if err != nil || len(list) != 2 || list[0].Domain != "alpha.org" || list[1].Domain != "example.com" ||
			!list[0].CreatedAt.Equal(time.Unix(1_800_000_000, 0)) {
			t.Fatalf("alice's domains: %+v %v", list, err)
		}
		if err := s.RemoveDomain(ctx, alice.ID, "alpha.org"); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveDomain(ctx, alice.ID, "alpha.org"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("removed twice: %v", err)
		}
		// Someone else's entry is not removed through another person.
		if err := s.RemoveDomain(ctx, alice.ID, "nope.org"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown: %v", err)
		}
		// Deleting a user forgets their domains.
		if err := s.DeleteUser(ctx, bob.ID); err != nil {
			t.Fatal(err)
		}
		if list, err := s.Domains(ctx, bob.ID); err != nil || len(list) != 0 {
			t.Fatalf("bob's domains after deleting bob: %+v %v", list, err)
		}
		if list, _ := s.Domains(ctx, alice.ID); len(list) != 1 || list[0].Domain != "example.com" {
			t.Fatalf("alice's domains: %+v", list)
		}
	})
}
