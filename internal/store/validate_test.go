package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeDomains(t *testing.T) {
	got, err := NormalizeDomains([]string{"WWW.Example.com.", "example.com", "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"example.com", "www.example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("domains %v, want %v", got, want)
	}
	for _, bad := range []string{"localhost", "exa mple.com", "*.example.com", "a..com", "example.com/x", ""} {
		if _, err := NormalizeDomain(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("domain %q: %v", bad, err)
		}
	}
}

func TestValidateName(t *testing.T) {
	for _, bad := range []string{"", "Blog", "a_b", "-a", "../x"} {
		if err := ValidateName(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("site name %q accepted", bad)
		}
	}
	for _, good := range []string{"a", "blog", "my-site-2"} {
		if err := ValidateName(good); err != nil {
			t.Errorf("site name %q: %v", good, err)
		}
	}
}

// The checks the tables made before ablage are made in Go now.
func TestFormerTableChecks(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		u, err := s.CreateUser(ctx, "root", "", RoleAdmin, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateRedirect(ctx, Redirect{OwnerID: u.ID, SourceDomain: "a.example.com", Target: "https://b.example.com", Code: 200}, 0); !errors.Is(err, ErrInvalid) {
			t.Errorf("status code: %v", err)
		}
		if _, err := s.CreateMailDomain(ctx, "m.example.com", u.ID, DKIMKey{Selector: "s", State: "bogus"}, 0); !errors.Is(err, ErrInvalid) {
			t.Errorf("DKIM state: %v", err)
		}
		if _, err := s.CreateMailDomain(ctx, "m.example.com", u.ID, DKIMKey{Selector: "s", State: DKIMActive}, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateMailbox(ctx, Mailbox{StorageID: "x", Domain: "m.example.com", LocalPart: "a", QuotaBytes: -1}, 0); !errors.Is(err, ErrInvalid) {
			t.Errorf("negative quota: %v", err)
		}
		m, err := s.CreateMailbox(ctx, Mailbox{StorageID: "x", Domain: "m.example.com", LocalPart: "a"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetMailboxLimits(ctx, m.ID, 0, -1, 0); !errors.Is(err, ErrInvalid) {
			t.Errorf("negative limit: %v", err)
		}
		if _, err := s.CreateMailAlias(ctx, MailAlias{Domain: "m.example.com", LocalPart: "b", Targets: []string{"a@m.example.com"}, SenderPolicy: "nobody"}); !errors.Is(err, ErrInvalid) {
			t.Errorf("sender policy: %v", err)
		}
		if _, err := s.SetMailboxGrant(ctx, m.ID, m.ID, []string{"read"}, 0); !errors.Is(err, ErrInvalid) {
			t.Errorf("grant to itself: %v", err)
		}
	})
}
