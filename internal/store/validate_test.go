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
