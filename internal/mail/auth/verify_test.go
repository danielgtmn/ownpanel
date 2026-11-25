package auth

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
)

// signedMessage returns a message From from, signed by the key of domain
// d (selector s1).
func signedMessage(t *testing.T, keyPEM, d, from string) string {
	t.Helper()
	msg := "From: Someone <" + from + ">\r\nTo: x@example.org\r\nSubject: hi\r\nDate: Mon, 23 Nov 2026 10:00:00 +0000\r\nMessage-ID: <1@" + d + ">\r\n\r\nHello.\r\n"
	sig, err := NewSigner().Signature(&config.Domain{Name: d, DKIM: []config.DKIMKey{{Selector: "s1", PrivateKeyPEM: keyPEM, Active: true}}}, strings.NewReader(msg))
	if err != nil {
		t.Fatal(err)
	}
	return sig + msg
}

func TestVerdicts(t *testing.T) {
	keyPEM, record := mailtest.DKIMKey(t)
	res := &mailtest.Resolver{
		TXT: map[string][]string{
			"sender.test":                {"v=spf1 ip4:192.0.2.0/24 -all"},
			"bounce.other.test":          {"v=spf1 ip4:192.0.2.0/24 -all"},
			"sub.sender.test":            {"v=spf1 ip4:192.0.2.0/24 -all"},
			"soft.test":                  {"v=spf1 ~all"},
			"s1._domainkey.sender.test":  {record},
			"_dmarc.sender.test":         {"v=DMARC1; p=reject; sp=quarantine"},
			"_dmarc.strict.test":         {"v=DMARC1; p=reject; aspf=s"},
			"strict.test":                {"v=spf1 ip4:192.0.2.0/24 -all"},
			"mail.strict.test":           {"v=spf1 ip4:192.0.2.0/24 -all"},
			"_dmarc.pct.test":            {"v=DMARC1; p=reject; pct=50"},
			"_dmarc.none.test":           {"v=DMARC1; p=none"},
			"s1._domainkey.elsewhere.ex": {record},
		},
		Fail: map[string]bool{"_dmarc.flaky.test": true, "tempspf.test": true},
	}
	v := Verifier{Resolver: res}
	ctx := context.Background()
	in := netip.MustParseAddr("192.0.2.10")
	out := netip.MustParseAddr("198.51.100.7")
	signed := signedMessage(t, keyPEM, "sender.test", "a@sender.test")

	cases := []struct {
		name       string
		ip         netip.Addr
		helo, from string
		msg        string
		fromDomain string
		spf, dkim  string
		dmarc      string
		policy     string
	}{
		{"all pass", in, "mx.sender.test", "a@sender.test", signed, "sender.test", Pass, Pass, Pass, "reject"},
		{"dkim alone aligns", out, "mx.sender.test", "a@sender.test", signed, "sender.test", Fail, Pass, Pass, "reject"},
		{"spf of another domain", in, "x", "b@bounce.other.test", "From: a@sender.test\r\n\r\nx\r\n", "sender.test", Pass, None, Fail, "reject"},
		{"relaxed spf alignment", in, "x", "b@sub.sender.test", "From: a@sender.test\r\n\r\nx\r\n", "sender.test", Pass, None, Pass, "reject"},
		{"strict spf alignment", in, "x", "b@mail.strict.test", "From: a@strict.test\r\n\r\nx\r\n", "strict.test", Pass, None, Fail, "reject"},
		{"tampered body", out, "x", "a@sender.test", strings.Replace(signed, "Hello.", "Hello!", 1), "sender.test", Fail, Fail, Fail, "reject"},
		{"signed by another domain", out, "x", "a@sender.test", signedMessage(t, keyPEM, "elsewhere.ex", "a@sender.test"), "sender.test", Fail, Pass, Fail, "reject"},
		{"no record", out, "x", "a@nodmarc.test", "From: a@nodmarc.test\r\n\r\nx\r\n", "nodmarc.test", None, None, None, "none"},
		{"organizational record", out, "x", "a@sub.sender.test", "From: a@sub.sender.test\r\n\r\nx\r\n", "sub.sender.test", Fail, None, Fail, "quarantine"},
		{"dmarc lookup fails", out, "x", "a@flaky.test", "From: a@flaky.test\r\n\r\nx\r\n", "flaky.test", None, None, TempError, "none"},
		{"spf lookup fails", out, "x", "a@tempspf.test", "From: a@sender.test\r\n\r\nx\r\n", "sender.test", TempError, None, TempError, "reject"},
		{"pct lowers policy", out, "x", "a@pct.test", "From: a@pct.test\r\n\r\nx\r\n", "pct.test", None, None, Fail, "quarantine"},
		{"softfail", out, "x", "a@soft.test", "From: a@none.test\r\n\r\nx\r\n", "none.test", SoftFail, None, Fail, "none"},
		{"null sender uses helo", in, "mx.sender.test", "", "From: a@sender.test\r\n\r\nx\r\n", "sender.test", None, None, Fail, "reject"},
		{"null sender helo pass", in, "sender.test", "", "From: a@sender.test\r\n\r\nx\r\n", "sender.test", Pass, None, Pass, "reject"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := v.SPF(ctx, c.ip, c.helo, c.from)
			if s.Result != c.spf {
				t.Errorf("spf %s, want %s", s.Result, c.spf)
			}
			d := v.DKIM(ctx, strings.NewReader(c.msg))
			got := None
			if len(d) > 0 {
				got = d[0].Result
			}
			if got != c.dkim {
				t.Errorf("dkim %s, want %s (%+v)", got, c.dkim, d)
			}
			m := v.DMARC(ctx, c.fromDomain, s, d)
			if m.Result != c.dmarc || m.Policy != c.policy {
				t.Errorf("dmarc %s/%s, want %s/%s", m.Result, m.Policy, c.dmarc, c.policy)
			}
		})
	}
}

func TestResultsHeader(t *testing.T) {
	h := ResultsHeader("mail.example.net",
		SPFResult{Result: Pass, Domain: "sender.test"},
		[]DKIMResult{{Result: Pass, Domain: "sender.test"}},
		[]DMARCResult{{Result: Fail, Domain: "sender.test", Policy: "none"}})
	for _, want := range []string{"Authentication-Results: mail.example.net;\r\n", "spf=pass smtp.mailfrom=sender.test", "dkim=pass header.d=sender.test", "dmarc=fail header.from=sender.test\r\n"} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in %q", want, h)
		}
	}
	if AuthServID(" Mail.Example.NET 1; spf=pass") != "mail.example.net" {
		t.Error("authserv-id")
	}
}

func TestSRS(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := NewSRS([]byte("0123456789abcdef0123456789abcdef"), clock)
	addr, err := s.Forward("Jane.Doe@Sender.Test", "example.org")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(addr, "SRS0=") || !strings.HasSuffix(addr, "=sender.test=Jane.Doe@example.org") {
		t.Fatalf("address %q", addr)
	}
	// Servers on the way may change case: still valid.
	for _, a := range []string{addr, strings.ToLower(addr), strings.ToUpper(addr)} {
		orig, err := s.Reverse(a)
		if err != nil || !strings.EqualFold(orig, "jane.doe@sender.test") {
			t.Fatalf("reverse %q: %q %v", a, orig, err)
		}
	}
	// Tampered: another original address with the same hash.
	tampered := strings.Replace(addr, "=sender.test=", "=evil.test=", 1)
	if _, err := s.Reverse(tampered); err != ErrSRSHash {
		t.Fatalf("tampered: %v", err)
	}
	// Another key does not accept it.
	if _, err := NewSRS([]byte("another key of at least 16 bytes"), clock).Reverse(addr); err != ErrSRSHash {
		t.Fatalf("other key: %v", err)
	}
	if _, err := s.Reverse("someone@example.org"); err != ErrNotSRS {
		t.Fatalf("plain: %v", err)
	}
	now = now.Add(SRSMaxAge * 24 * time.Hour)
	if _, err := s.Reverse(addr); err != nil {
		t.Fatalf("21 days: %v", err)
	}
	now = now.Add(24 * time.Hour)
	if _, err := s.Reverse(addr); err != ErrSRSExpired {
		t.Fatalf("22 days: %v", err)
	}
}

func TestLoadSRSKey(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadSRSKey(dir)
	if err != nil || len(a) != 32 {
		t.Fatal(err, len(a))
	}
	if fi, err := os.Stat(filepath.Join(dir, "srs.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", err, fi.Mode())
	}
	b, err := LoadSRSKey(dir)
	if err != nil || string(a) != string(b) {
		t.Fatal("key changed")
	}
}
