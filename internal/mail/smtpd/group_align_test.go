package smtpd

import (
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
)

// TestGroupNeedsExactAuthentication: for a restricted group, a DKIM pass of
// a sibling domain (relaxed alignment, enough for other mail of our
// domains) does not prove the envelope sender.
func TestGroupNeedsExactAuthentication(t *testing.T) {
	cfg := testConfig(t)
	cfg.Domains = append(cfg.Domains, config.Domain{Name: "b.hosting.test"})
	cfg.Mailboxes = append(cfg.Mailboxes, config.Mailbox{StorageID: mailtest.ID('9'), Address: "staff@b.hosting.test", PasswordHash: mailtest.Hash})
	cfg.Aliases = append(cfg.Aliases, config.Alias{Address: "team@b.hosting.test", Targets: []string{"staff@b.hosting.test"},
		SenderPolicy: config.SenderOrganisation})
	e := start(t, cfg)
	keyPEM, record := mailtest.DKIMKey(t)
	e.res.TXT["s1._domainkey.a.hosting.test"] = []string{record}
	e.res.TXT["s1._domainkey.b.hosting.test"] = []string{record}
	signAs := func(domain, msg string) string {
		sig, err := auth.NewSigner().Signature(&config.Domain{Name: domain,
			DKIM: []config.DKIMKey{{Selector: "s1", PrivateKeyPEM: keyPEM, Active: true}}}, strings.NewReader(msg))
		if err != nil {
			t.Fatal(err)
		}
		return sig + msg
	}
	msg := "From: boss@b.hosting.test\r\nTo: team@b.hosting.test\r\nSubject: hi\r\nMessage-ID: <1@b.hosting.test>\r\n\r\nHello.\r\n"
	e.from("198.51.100.20")
	c := dial(t, e.addr, false)
	if err := send(c, "boss@b.hosting.test", []string{"team@b.hosting.test"}, signAs("a.hosting.test", msg)); code(err) != 550 {
		t.Errorf("sibling domain's signature: %v", err)
	}
	// Signed by the sender's own domain: accepted.
	if err := send(c, "boss@b.hosting.test", []string{"team@b.hosting.test"}, signAs("b.hosting.test", msg)); err != nil {
		t.Errorf("own domain's signature: %v", err)
	}
}
