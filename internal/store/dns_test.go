package store

import (
	"errors"
	"testing"
	"time"
)

func TestDNSConnectionsCRUD(t *testing.T) {
	s, _ := open(t)
	s.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	alice, err := s.CreateUser(ctx, "alice", "h", RoleAdmin, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser(ctx, "bob", "h", RoleMember, false)
	if err != nil {
		t.Fatal(err)
	}

	checked := time.Unix(1_800_000_000, 0).UTC()
	three := 3
	a1, err := s.CreateDNSConnection(ctx, DNSConnection{UserID: alice.ID, Provider: "hetzner", Name: "main",
		Secret: []byte{1, 2, 3}, LastCheckedAt: &checked, ZoneCount: &three})
	if err != nil {
		t.Fatal(err)
	}
	if a1.ID == 0 || a1.Owner != "alice" || a1.Provider != "hetzner" || string(a1.Secret) != "\x01\x02\x03" ||
		!a1.CreatedAt.Equal(checked) || a1.LastCheckedAt == nil || !a1.LastCheckedAt.Equal(checked) ||
		a1.ZoneCount == nil || *a1.ZoneCount != 3 || a1.CanWrite != nil || a1.LastError != "" {
		t.Fatalf("created: %+v", a1)
	}
	_, err = s.CreateDNSConnection(ctx, DNSConnection{UserID: alice.ID, Provider: "hetzner", Name: "main", Secret: []byte{9}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	// The same name is fine for another user.
	b1, err := s.CreateDNSConnection(ctx, DNSConnection{UserID: bob.ID, Provider: "hetzner", Name: "main", Secret: []byte{4}})
	if err != nil {
		t.Fatal(err)
	}
	if b1.LastCheckedAt != nil || b1.ZoneCount != nil {
		t.Fatalf("unchecked connection: %+v", b1)
	}
	a2, err := s.CreateDNSConnection(ctx, DNSConnection{UserID: alice.ID, Provider: "hetzner", Name: "agency", Secret: []byte{5}})
	if err != nil {
		t.Fatal(err)
	}

	names := func(list []DNSConnection) (out []string) {
		for _, c := range list {
			out = append(out, c.Owner+"/"+c.Name)
		}
		return out
	}
	all, err := s.DNSConnections(ctx, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("all: %v %v", names(all), err)
	}
	if got := names(all); got[0] != "alice/agency" || got[1] != "alice/main" || got[2] != "bob/main" {
		t.Fatalf("order: %v", got)
	}
	own, err := s.DNSConnections(ctx, bob.ID)
	if err != nil || len(own) != 1 || own[0].ID != b1.ID {
		t.Fatalf("bob's: %v %v", names(own), err)
	}

	later := checked.Add(time.Minute)
	if err := s.RecordDNSCheck(ctx, a1.ID, later, nil, "the token was rejected"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.DNSConnection(ctx, a1.ID)
	if !got.LastCheckedAt.Equal(later) || got.LastError != "the token was rejected" || *got.ZoneCount != 3 {
		t.Fatalf("after failed check: %+v", got)
	}
	five := 5
	s.RecordDNSCheck(ctx, a1.ID, later, &five, "")
	got, _ = s.DNSConnection(ctx, a1.ID)
	if got.LastError != "" || *got.ZoneCount != 5 {
		t.Fatalf("after check: %+v", got)
	}
	if err := s.RecordDNSWrite(ctx, a1.ID, false, "read only"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.DNSConnection(ctx, a1.ID)
	if got.CanWrite == nil || *got.CanWrite || got.LastError != "read only" {
		t.Fatalf("after refused write: %+v", got)
	}
	s.RecordDNSWrite(ctx, a1.ID, true, "")
	got, _ = s.DNSConnection(ctx, a1.ID)
	if got.CanWrite == nil || !*got.CanWrite || got.LastError != "" {
		t.Fatalf("after write: %+v", got)
	}

	if err := s.DeleteDNSConnection(ctx, a2.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDNSConnection(ctx, a2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.DNSConnection(ctx, a2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}

	// Deleting a user removes their connections.
	if err := s.DeleteUser(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DNSConnection(ctx, b1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("connection of a deleted user: %v", err)
	}
}
