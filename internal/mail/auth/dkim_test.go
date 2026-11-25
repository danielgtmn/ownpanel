package auth

import (
	"errors"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
)

func TestSignature(t *testing.T) {
	key, txt := mailtest.DKIMKey(t)
	other, _ := mailtest.DKIMKey(t)
	d := &config.Domain{Name: "example.org", DKIM: []config.DKIMKey{
		{Selector: "old", PrivateKeyPEM: other},
		{Selector: "s1", PrivateKeyPEM: key, Active: true},
	}}
	msg := "From: a@example.org\r\nTo: b@remote.test\r\nSubject:  Hi  there\r\nX-Other: 1\r\n\r\nBody  line \r\n\r\n\r\n"
	s := NewSigner()
	sig, err := s.Signature(d, strings.NewReader(msg))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sig, "DKIM-Signature: ") || !strings.HasSuffix(sig, "\r\n") ||
		!strings.Contains(sig, "c=relaxed/relaxed") || !strings.Contains(sig, "a=rsa-sha256") || !strings.Contains(sig, "s=s1") {
		t.Fatalf("signature %q", sig)
	}
	// Relaxed canonicalization survives whitespace changes in transit.
	transit := strings.Replace(sig+msg, "Subject:  Hi  there", "Subject: Hi there", 1)
	vs, err := dkim.VerifyWithOptions(strings.NewReader(transit), &dkim.VerifyOptions{
		LookupTXT: func(string) ([]string, error) { return []string{txt}, nil },
	})
	if err != nil || len(vs) != 1 || vs[0].Err != nil {
		t.Fatalf("verify: %v %+v", err, vs)
	}
	if h := strings.Join(vs[0].HeaderKeys, ","); h != "From,To,Subject" {
		t.Fatalf("signed headers %s", h)
	}
	// A changed body fails.
	vs, _ = dkim.VerifyWithOptions(strings.NewReader(sig+strings.Replace(msg, "Body", "body", 1)), &dkim.VerifyOptions{
		LookupTXT: func(string) ([]string, error) { return []string{txt}, nil },
	})
	if len(vs) != 1 || vs[0].Err == nil {
		t.Fatal("tampered body verified")
	}

	if _, err := s.Signature(&config.Domain{Name: "x.test"}, strings.NewReader(msg)); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
}
