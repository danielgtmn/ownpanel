package rules

import (
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/sieve"
)

// A script whose settings regenerate it but would not pass the checks is
// not managed: it could run something else than the panel shows.
func TestParseRefusesCraftedSettings(t *testing.T) {
	crafted := Settings{Filters: []config.FilterRule{{Name: "x", Folder: "F", Conditions: []config.FilterCondition{
		{Field: "subject", Op: `contains "subject" "nothing" { stop; } if true { discard; } if header :is`, Value: "x"}}}}}
	src := Generate(crafted)
	if _, err := sieve.Compile(src); err != nil {
		t.Fatalf("the crafted script must be valid Sieve for this test: %v\n%s", err, src)
	}
	if _, ok := Parse(src); ok {
		t.Fatal("crafted settings accepted")
	}
}
