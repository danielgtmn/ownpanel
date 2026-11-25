package smtpd

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
)

// A well-formed message from a@sender.test.
const goodMsg = "From: A <a@sender.test>\r\nTo: alice@example.org\r\nSubject: hi\r\nDate: Mon, 23 Nov 2026 10:00:00 +0000\r\nMessage-ID: <1@sender.test>\r\n\r\nHello.\r\n"

// senderDNS publishes SPF (192.0.2.0/24), a DKIM key and a DMARC record
// for sender.test, and reverse DNS for 192.0.2.10. It returns the DKIM
// key.
func senderDNS(t *testing.T, e *env, dmarc string) string {
	keyPEM, record := mailtest.DKIMKey(t)
	e.res.TXT["sender.test"] = []string{"v=spf1 ip4:192.0.2.0/24 -all"}
	e.res.TXT["s1._domainkey.sender.test"] = []string{record}
	if dmarc != "" {
		e.res.TXT["_dmarc.sender.test"] = []string{dmarc}
	}
	e.res.PTR["192.0.2.10"] = []string{"mx.sender.test."}
	return keyPEM
}

func sign(t *testing.T, keyPEM, msg string) string {
	t.Helper()
	sig, err := auth.NewSigner().Signature(&config.Domain{Name: "sender.test",
		DKIM: []config.DKIMKey{{Selector: "s1", PrivateKeyPEM: keyPEM, Active: true}}}, strings.NewReader(msg))
	if err != nil {
		t.Fatal(err)
	}
	return sig + msg
}

func folder(t *testing.T, e *env, id, name string) []string {
	t.Helper()
	_, msgs, err := e.store.Select(context.Background(), id, name)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		f, err := e.store.Open(m)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		b.ReadFrom(f)
		f.Close()
		out = append(out, b.String())
	}
	return out
}

func bigConfig(t *testing.T) *config.Config {
	cfg := testConfig(t)
	cfg.Limits.MaxMessageBytes = 200000
	cfg.Limits.ConnectionsPerIP = 20
	return cfg
}

func TestAuthenticationResults(t *testing.T) {
	e := start(t, bigConfig(t))
	key := senderDNS(t, e, "v=DMARC1; p=reject")
	e.from("192.0.2.10")
	forged := "Authentication-Results: MAIL.example.net; spf=pass; dkim=pass; dmarc=pass\r\n" +
		"Authentication-Results: other.test; spf=fail\r\nX-Ownpanel-Score: 0\r\n"
	c := dial(t, e.addr, false)
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, sign(t, key, goodMsg)); err != nil {
		t.Fatal(err)
	}
	// Prepended forged fields: they are not covered by the signature.
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, forged+sign(t, key, goodMsg)); err != nil {
		t.Fatal(err)
	}
	msgs := inbox(t, e, idAlice)
	if len(msgs) != 2 {
		t.Fatalf("%d messages", len(msgs))
	}
	for i, m := range msgs {
		hdr := m[:strings.Index(m, "\r\n\r\n")]
		if strings.Count(hdr, "Authentication-Results: mail.example.net;") != 1 ||
			!strings.Contains(hdr, "spf=pass smtp.mailfrom=sender.test") ||
			!strings.Contains(hdr, "dkim=pass header.d=sender.test") ||
			!strings.Contains(hdr, "dmarc=pass header.from=sender.test") {
			t.Errorf("message %d header:\n%s", i, hdr)
		}
		if strings.Contains(strings.ToLower(hdr), "authentication-results: mail.example.net; spf=pass; dkim=pass") {
			t.Errorf("forged results kept:\n%s", hdr)
		}
		if strings.Count(hdr, "X-Ownpanel-Score:") != 1 || !strings.Contains(hdr, "X-Ownpanel-Score: 0\r\n") {
			t.Errorf("score header:\n%s", hdr)
		}
		if !strings.HasSuffix(m, "\r\n\r\nHello.\r\n") {
			t.Errorf("body:\n%q", m)
		}
	}
	if !strings.Contains(msgs[1], "Authentication-Results: other.test; spf=fail\r\n") {
		t.Error("results of other servers removed")
	}
}

func TestDMARCPolicies(t *testing.T) {
	e := start(t, bigConfig(t))
	senderDNS(t, e, "v=DMARC1; p=reject")
	e.res.TXT["_dmarc.quarantine.test"] = []string{"v=DMARC1; p=quarantine"}
	e.res.TXT["_dmarc.flaky.test"] = nil
	e.res.Fail["_dmarc.flaky.test"] = true
	e.from("198.51.100.7") // not in the SPF record, no signature
	c := dial(t, e.addr, false)
	err := send(c, "a@sender.test", []string{"alice@example.org"}, goodMsg)
	var se *smtp.SMTPError
	if !asSMTP(err, &se) || se.Code != 550 || se.EnhancedCode != (smtp.EnhancedCode{5, 7, 1}) || !strings.Contains(se.Message, "sender.test") {
		t.Fatalf("reject: %v", err)
	}
	c.Reset()
	q := strings.Replace(goodMsg, "a@sender.test", "a@quarantine.test", 1)
	if err := send(c, "a@quarantine.test", []string{"alice@example.org"}, q); err != nil {
		t.Fatal(err)
	}
	// A lookup error is never a reason to refuse.
	fl := strings.Replace(goodMsg, "a@sender.test", "a@flaky.test", 1)
	if err := send(c, "a@flaky.test", []string{"alice@example.org"}, fl); err != nil {
		t.Fatal(err)
	}
	junk := folder(t, e, idAlice, "Junk")
	in := inbox(t, e, idAlice)
	if len(junk) != 1 || !strings.Contains(junk[0], "dmarc=fail header.from=quarantine.test") || !strings.Contains(junk[0], "dmarc-quarantine") {
		t.Fatalf("junk %q", junk)
	}
	if len(in) != 1 || !strings.Contains(in[0], "dmarc=temperror") {
		t.Fatalf("inbox %q", in)
	}
}

func asSMTP(err error, se **smtp.SMTPError) bool {
	e, ok := err.(*smtp.SMTPError)
	if ok {
		*se = e
	}
	return ok
}

func TestBlocklistAtRcpt(t *testing.T) {
	e := start(t, bigConfig(t))
	e.res.IP["10.100.51.198.zen.spamhaus.org"] = []net.IPAddr{{IP: net.ParseIP("127.0.0.3")}}
	e.res.IP["11.100.51.198.zen.spamhaus.org"] = []net.IPAddr{{IP: net.ParseIP("127.0.0.10")}}
	e.res.IP["12.100.51.198.zen.spamhaus.org"] = []net.IPAddr{{IP: net.ParseIP("127.255.255.254")}}
	e.from("198.51.100.10")
	c := dial(t, e.addr, false)
	if err := c.Mail("a@sender.test", nil); err != nil {
		t.Fatal(err)
	}
	err := c.Rcpt("alice@example.org", nil)
	var se *smtp.SMTPError
	if !asSMTP(err, &se) || se.Code != 554 || !strings.Contains(se.Message, "zen.spamhaus.org") {
		t.Fatalf("listed: %v", err)
	}
	// Refused per recipient; the connection stays usable.
	if err := c.Rcpt("bob@example.org", nil); code(err) != 554 {
		t.Fatalf("second recipient: %v", err)
	}
	if err := c.Reset(); err != nil {
		t.Fatal(err)
	}
	// PBL: a score, not a refusal.
	e.from("198.51.100.11")
	c = dial(t, e.addr, false)
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	if m := inbox(t, e, idAlice); len(m) != 1 || !strings.Contains(m[0], "listed:zen.spamhaus.org") {
		t.Fatalf("%q", m)
	}
	// Refused queries: accepted, one event however many connections.
	e.from("198.51.100.12")
	for i := 0; i < 2; i++ {
		c = dial(t, e.addr, false)
		if err := send(c, "a@sender.test", []string{"alice@example.org"}, goodMsg); err != nil {
			t.Fatal(err)
		}
	}
	ev, _ := e.store.Events(context.Background(), 0)
	if len(ev) != 1 || ev[0].Kind != config.EventBlocklistRefused {
		t.Fatalf("events %+v", ev)
	}
}

func TestScoreToJunk(t *testing.T) {
	e := start(t, bigConfig(t))
	senderDNS(t, e, "")
	e.from("198.51.100.7") // no PTR, SPF fail (2.5 + 1.5)
	c := dial(t, e.addr, false)
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	if m := inbox(t, e, idAlice); len(m) != 1 || !strings.Contains(m[0], "X-Ownpanel-Score: 4 (no-ptr spf-fail)\r\n") {
		t.Fatalf("below threshold: %q", m)
	}
	// Without Date (1 more): 5, the default threshold.
	noDate := strings.Replace(goodMsg, "Date: Mon, 23 Nov 2026 10:00:00 +0000\r\n", "", 1)
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, noDate); err != nil {
		t.Fatal(err)
	}
	if m := folder(t, e, idAlice, "Junk"); len(m) != 1 || !strings.Contains(m[0], "X-Ownpanel-Score: 5 (no-date no-ptr spf-fail)") {
		t.Fatalf("junk: %q", m)
	}
	// A higher threshold from the configuration.
	cfg := bigConfig(t)
	cfg.Filter.JunkScore = 10
	e.dir.Store(directory.New(cfg))
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, noDate); err != nil {
		t.Fatal(err)
	}
	if len(inbox(t, e, idAlice)) != 2 {
		t.Fatal("threshold not applied")
	}
	// Display name spoofing the recipient's domain.
	spoof := strings.Replace(goodMsg, "From: A <a@sender.test>", `From: "alice@example.org" <a@sender.test>`, 1)
	e.dir.Store(directory.New(bigConfig(t)))
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, spoof); err != nil {
		t.Fatal(err)
	}
	if m := folder(t, e, idAlice, "Junk"); len(m) != 2 || !strings.Contains(m[1], "spoofed-name") {
		t.Fatalf("spoof: %q", m)
	}
}

func zipWith(t *testing.T, name string) string {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, _ := w.Create(name)
	f.Write([]byte("x"))
	w.Close()
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func withAttachment(name, ctype, b64 string) string {
	return "From: A <a@sender.test>\r\nDate: Mon, 23 Nov 2026 10:00:00 +0000\r\nMessage-ID: <2@sender.test>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"xx\"\r\n\r\n--xx\r\nContent-Type: text/plain\r\n\r\nHi.\r\n--xx\r\n" +
		"Content-Type: " + ctype + "\r\nContent-Disposition: attachment; filename=\"" + name + "\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		b64 + "\r\n--xx--\r\n"
}

func TestDangerousAttachments(t *testing.T) {
	e := start(t, bigConfig(t))
	c := dial(t, e.addr, false)
	for _, m := range []string{
		withAttachment("invoice.exe", "application/octet-stream", "TVo="),
		withAttachment("report.docm", "application/vnd.ms-word.document.macroEnabled.12", "eA=="),
		withAttachment("files.zip", "application/zip", zipWith(t, "a/b/run.js")),
	} {
		err := send(c, "a@sender.test", []string{"alice@example.org"}, m)
		var se *smtp.SMTPError
		if !asSMTP(err, &se) || se.Code != 554 || se.EnhancedCode != (smtp.EnhancedCode{5, 7, 1}) {
			t.Fatalf("not refused: %v", err)
		}
		c.Reset()
	}
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, withAttachment("files.zip", "application/zip", zipWith(t, "a.txt"))); err != nil {
		t.Fatal(err)
	}
	if len(inbox(t, e, idAlice)) != 1 {
		t.Fatal("clean zip not delivered")
	}
}

func TestGreylisting(t *testing.T) {
	cfg := bigConfig(t)
	cfg.Filter.Greylisting = true
	e := start(t, cfg)
	senderDNS(t, e, "")
	// SPF passes but there is no reverse DNS: greylisted.
	e.from("192.0.2.20")
	c := dial(t, e.addr, false)
	try := func(from, to string) int {
		c.Reset()
		if err := c.Mail(from, nil); err != nil {
			t.Fatal(err)
		}
		return code(c.Rcpt(to, nil))
	}
	if got := try("a@sender.test", "alice@example.org"); got != 451 {
		t.Fatalf("first: %d", got)
	}
	e.clock.Add(time.Minute)
	if got := try("a@sender.test", "alice@example.org"); got != 451 {
		t.Fatalf("too early: %d", got)
	}
	e.clock.Add(5 * time.Minute)
	if got := try("a@sender.test", "alice@example.org"); got != 0 {
		t.Fatalf("after the delay: %d", got)
	}
	// Same network, another address in it: passes for 30 days.
	e.from("192.0.2.21")
	c = dial(t, e.addr, false)
	e.clock.Add(29 * 24 * time.Hour)
	if got := try("a@sender.test", "alice@example.org"); got != 0 {
		t.Fatalf("known triplet: %d", got)
	}
	if got := try("other@sender.test", "alice@example.org"); got != 451 {
		t.Fatalf("new sender: %d", got)
	}
	// With reverse DNS and an SPF pass: no greylisting.
	e.from("192.0.2.10")
	c = dial(t, e.addr, false)
	if got := try("new@sender.test", "bob@example.org"); got != 0 {
		t.Fatalf("trusted client: %d", got)
	}
}

// fwdConfig has an alias that only forwards outside and one more domain
// with a forward of its own.
func fwdConfig(t *testing.T) *config.Config {
	cfg := bigConfig(t)
	cfg.Domains = append(cfg.Domains, config.Domain{Name: "limited.test", SendPerHour: 1})
	cfg.Aliases = append(cfg.Aliases,
		config.Alias{Address: "*@limited.test", Targets: []string{"far@elsewhere.test"}})
	return cfg
}

func TestForwarding(t *testing.T) {
	e := start(t, fwdConfig(t))
	key := senderDNS(t, e, "v=DMARC1; p=reject")
	e.from("192.0.2.10")
	c := dial(t, e.addr, false)
	signed := sign(t, key, goodMsg)
	if err := send(c, "a@sender.test", []string{"out@example.org"}, signed); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 1 || got[0].To[0] != "friend@elsewhere.test" {
		t.Fatalf("sink %+v", got)
	}
	if orig, err := e.srs.Reverse(got[0].From); err != nil || orig != "a@sender.test" {
		t.Fatalf("sender %q: %v", got[0].From, err)
	}
	data := string(got[0].Data)
	if strings.Contains(data, "Return-Path") || !strings.Contains(data, "From: A <a@sender.test>") {
		t.Fatalf("forwarded copy:\n%s", data)
	}
	// Only headers were prepended: the original signature still verifies.
	v, err := dkim.VerifyWithOptions(strings.NewReader(data), &dkim.VerifyOptions{LookupTXT: func(name string) ([]string, error) {
		return e.res.TXT[name], nil
	}})
	if err != nil || len(v) != 1 || v[0].Err != nil {
		t.Fatalf("DKIM after forwarding: %v %+v", err, v)
	}

	// The null sender stays null.
	if err := send(c, "", []string{"out@example.org"}, signed); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 2 || got[1].From != "" {
		t.Fatalf("null sender: %+v", got)
	}

	// The domain's hourly limit counts forwards: a forward-only address
	// answers with a temporary error once it is reached.
	if err := send(c, "a@sender.test", []string{"x@limited.test"}, signed); err != nil {
		t.Fatal(err)
	}
	if err := send(c, "a@sender.test", []string{"y@limited.test"}, signed); code(err) != 451 {
		t.Fatalf("over the limit: %v", err)
	}
}

// TestPostmasterOutside: with an outside postmaster address, mail to
// postmaster@ of the host name (and of a domain without mailboxes) is
// forwarded there through the queue with an SRS sender.
func TestPostmasterOutside(t *testing.T) {
	cfg := fwdConfig(t)
	cfg.PostmasterAddress = "admin@elsewhere.test"
	e := start(t, cfg)
	e.from("192.0.2.10")
	c := dial(t, e.addr, false)
	for _, rcpt := range []string{"postmaster@mail.example.net", "abuse@empty.test"} {
		if err := send(c, "a@sender.test", []string{rcpt}, goodMsg); err != nil {
			t.Fatalf("%s: %v", rcpt, err)
		}
	}
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 2 {
		t.Fatalf("forwarded %d", len(got))
	}
	for _, m := range got {
		if len(m.To) != 1 || m.To[0] != "admin@elsewhere.test" {
			t.Errorf("to %v", m.To)
		}
		if orig, err := e.srs.Reverse(m.From); err != nil || orig != "a@sender.test" {
			t.Errorf("sender %q: %v", m.From, err)
		}
	}
}

func TestBounceToSRSAddress(t *testing.T) {
	e := start(t, fwdConfig(t))
	e.from("192.0.2.10")
	srs, err := e.srs.Forward("a@sender.test", "example.org")
	if err != nil {
		t.Fatal(err)
	}
	c := dial(t, e.addr, false)
	// Only delivery reports (null sender) are taken.
	c.Mail("someone@elsewhere.test", nil)
	if got := code(c.Rcpt(srs, nil)); got != 550 {
		t.Fatalf("non-null sender: %d", got)
	}
	c.Reset()
	c.Mail("", nil)
	tampered := strings.Replace(srs, "=sender.test=", "=evil.test=", 1)
	if got := code(c.Rcpt(tampered, nil)); got != 550 {
		t.Fatalf("tampered: %d", got)
	}
	c.Reset()
	report := "From: MAILER-DAEMON@elsewhere.test\r\nTo: " + srs + "\r\nSubject: Undelivered\r\nDate: Mon, 23 Nov 2026 10:00:00 +0000\r\nMessage-ID: <3@elsewhere.test>\r\n\r\nGone.\r\n"
	if err := send(c, "", []string{srs}, report); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 1 || got[0].From != "" || len(got[0].To) != 1 || got[0].To[0] != "a@sender.test" {
		t.Fatalf("routed back: %+v", got)
	}
	// Too old: refused.
	e.clock.Add(22 * 24 * time.Hour)
	c.Reset()
	c.Mail("", nil)
	if got := code(c.Rcpt(srs, nil)); got != 550 {
		t.Fatalf("expired: %d", got)
	}
}

func TestJunkNotForwarded(t *testing.T) {
	e := start(t, fwdConfig(t))
	senderDNS(t, e, "")
	e.from("198.51.100.7") // no PTR, SPF fail
	c := dial(t, e.addr, false)
	noDate := strings.Replace(goodMsg, "Date: Mon, 23 Nov 2026 10:00:00 +0000\r\n", "", 1)
	if err := send(c, "a@sender.test", []string{"team@example.org"}, noDate); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 0 {
		t.Fatalf("junk forwarded: %+v", got)
	}
	if m := folder(t, e, idAlice, "Junk"); len(m) != 1 {
		t.Fatalf("local copy: %d", len(m))
	}
	ev, _ := e.store.Events(context.Background(), 0)
	if len(ev) != 1 || ev[0].Kind != config.EventNotForwarded || !strings.Contains(ev[0].Detail, "friend@elsewhere.test") || !strings.Contains(ev[0].Detail, "junk") {
		t.Fatalf("events %+v", ev)
	}
	if n, _ := e.q.Len(context.Background()); n != 0 {
		t.Fatalf("queue %d", n)
	}
}

func TestForwardedBounceReturns(t *testing.T) {
	// A forward that fails for good bounces to the original sender.
	e := start(t, fwdConfig(t))
	e.from("192.0.2.10")
	e.sink.SetRcpt(func(to string) error {
		if to == "friend@elsewhere.test" {
			return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user"}
		}
		return nil
	})
	c := dial(t, e.addr, false)
	if err := send(c, "a@sender.test", []string{"out@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 1 || got[0].From != "" || got[0].To[0] != "a@sender.test" || !strings.Contains(string(got[0].Data), "friend@elsewhere.test") {
		t.Fatalf("bounce %+v", got)
	}
}

// signAs signs msg for domain with selector s1.
func signAs(t *testing.T, domain, keyPEM, msg string) string {
	t.Helper()
	sig, err := auth.NewSigner().Signature(&config.Domain{Name: domain,
		DKIM: []config.DKIMKey{{Selector: "s1", PrivateKeyPEM: keyPEM, Active: true}}}, strings.NewReader(msg))
	if err != nil {
		t.Fatal(err)
	}
	return sig + msg
}

func TestOwnDomainFromOutside(t *testing.T) {
	e := start(t, fwdConfig(t))
	senderDNS(t, e, "")
	keyPEM, record := mailtest.DKIMKey(t)
	e.res.TXT["example.org"] = []string{"v=spf1 ip4:203.0.113.0/24 -all"}
	e.res.TXT["s1._domainkey.example.org"] = []string{record}
	ownFrom := strings.Replace(goodMsg, "From: A <a@sender.test>", "From: Alice <alice@example.org>", 1)
	e.from("198.51.100.7") // not in either SPF record
	c := dial(t, e.addr, false)
	refused := func(from, msg string) {
		t.Helper()
		c.Reset()
		err := send(c, from, []string{"bob@example.org"}, msg)
		var se *smtp.SMTPError
		if !asSMTP(err, &se) || se.Code != 550 || se.EnhancedCode != (smtp.EnhancedCode{5, 7, 1}) {
			t.Fatalf("from %s not refused: %v", from, err)
		}
	}
	// Header From of ours, envelope elsewhere: refused.
	refused("a@sender.test", ownFrom)
	// Envelope sender of ours (SPF fails), header From elsewhere: refused.
	refused("alice@example.org", goodMsg)
	// mail_host counts as ours too.
	refused("x@mail.example.net", goodMsg)

	// An aligned DKIM pass makes it genuine: accepted, and forwarded with
	// an SRS sender because SPF did not pass for our address.
	c.Reset()
	if err := send(c, "alice@example.org", []string{"out@example.org"}, signAs(t, "example.org", keyPEM, ownFrom)); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 1 {
		t.Fatalf("sink %+v", got)
	}
	if orig, err := e.srs.Reverse(got[0].From); err != nil || orig != "alice@example.org" {
		t.Fatalf("forwarded with sender %q (%v), want SRS", got[0].From, err)
	}

	// SPF pass for our domain: accepted, and the sender is kept.
	e.from("203.0.113.5")
	c = dial(t, e.addr, false)
	if err := send(c, "alice@example.org", []string{"out@example.org"}, ownFrom); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 2 || got[1].From != "alice@example.org" {
		t.Fatalf("sink %+v", got)
	}

	// Local programs (loopback) are trusted.
	e.from("127.0.0.1")
	c = dial(t, e.addr, false)
	if err := send(c, "alice@example.org", []string{"bob@example.org"}, ownFrom); err != nil {
		t.Fatalf("loopback: %v", err)
	}
}

func TestBounceToSRSAddressLimited(t *testing.T) {
	e := start(t, fwdConfig(t)) // limited.test: 1 message per hour
	e.from("192.0.2.10")
	srs, err := e.srs.Forward("victim@sender.test", "limited.test")
	if err != nil {
		t.Fatal(err)
	}
	c := dial(t, e.addr, false)
	report := "From: MAILER-DAEMON@elsewhere.test\r\nSubject: Undelivered\r\nDate: Mon, 23 Nov 2026 10:00:00 +0000\r\nMessage-ID: <4@elsewhere.test>\r\n\r\nGone.\r\n"
	if err := send(c, "", []string{srs}, report); err != nil {
		t.Fatal(err)
	}
	if err := send(c, "", []string{srs}, report); code(err) != 451 {
		t.Fatalf("over the limit: %v", err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 1 {
		t.Fatalf("passed on %d reports", len(got))
	}
	// The window slides: an hour later one more goes through.
	e.clock.Add(time.Hour + time.Minute)
	if err := send(c, "", []string{srs}, report); err != nil {
		t.Fatal(err)
	}
}
