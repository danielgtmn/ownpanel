package smtpd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/deliver"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Filters (Sieve) run at delivery on port 25.

func setScript(t *testing.T, e *env, id, src string) {
	t.Helper()
	if err := e.store.PutSieveScript(context.Background(), id, "test", src, deliver.MaxScripts, true); err != nil {
		t.Fatal(err)
	}
}

func events(t *testing.T, e *env, kind string) []config.Event {
	t.Helper()
	all, err := e.store.Events(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []config.Event
	for _, ev := range all {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// goodSender sends signed mail from a@sender.test that passes SPF, DKIM
// and has reverse DNS.
func goodSender(t *testing.T, e *env) (send1 func(rcpt []string, msg string) error) {
	key := senderDNS(t, e, "v=DMARC1; p=reject")
	e.from("192.0.2.10")
	c := dial(t, e.addr, false)
	return func(rcpt []string, msg string) error {
		return send(c, "a@sender.test", rcpt, sign(t, key, msg))
	}
}

func TestSieveFileinto(t *testing.T) {
	e := start(t, bigConfig(t))
	ctx := context.Background()
	e.store.EnsureMailbox(ctx, idAlice)
	if err := e.store.CreateFolder(ctx, idAlice, "Work"); err != nil {
		t.Fatal(err)
	}
	setScript(t, e, idAlice, `require ["fileinto", "imap4flags", "envelope"];
if header :contains "subject" "hi" { fileinto :flags "\\Seen" "Work"; }
if envelope :is "to" "team@example.org" { fileinto "Missing"; }`)
	sendIt := goodSender(t, e)
	if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	if got := folder(t, e, idAlice, "Work"); len(got) != 1 || !strings.HasPrefix(got[0], "Return-Path: <a@sender.test>\r\n") {
		t.Fatalf("Work: %q", got)
	}
	_, msgs, _ := e.store.Select(ctx, idAlice, "Work")
	if !msgs[0].HasFlag(`\Seen`) {
		t.Fatalf("flags %v", msgs[0].Flags)
	}
	if got := inbox(t, e, idAlice); len(got) != 0 {
		t.Fatalf("INBOX: %q", got)
	}
	// Through an alias, envelope :to is the alias; a missing folder keeps
	// the message in INBOX. Bob has no script.
	other := strings.Replace(goodMsg, "Subject: hi", "Subject: other", 1)
	if err := sendIt([]string{"team@example.org"}, other); err != nil {
		t.Fatal(err)
	}
	if got := inbox(t, e, idAlice); len(got) != 1 {
		t.Fatalf("INBOX after missing folder: %q", got)
	}
	if got := inbox(t, e, idBob); len(got) != 1 {
		t.Fatalf("bob: %q", got)
	}
	// A broken script keeps everything in INBOX.
	setScript(t, e, idAlice, `require "fileinto"; fileinto;`)
	if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	if got := inbox(t, e, idAlice); len(got) != 2 {
		t.Fatalf("broken script: %q", got)
	}
}

func junkMsg() string {
	return strings.Replace(goodMsg, "Date: Mon, 23 Nov 2026 10:00:00 +0000\r\n", "", 1)
}

func TestSieveJunk(t *testing.T) {
	e := start(t, fwdConfig(t))
	senderDNS(t, e, "")
	e.from("198.51.100.7") // no PTR, SPF fail; without Date: junk
	c := dial(t, e.addr, false)
	// The implicit keep goes to Junk, an explicit keep too.
	setScript(t, e, idAlice, `require "vacation"; vacation "away"; keep;`)
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, junkMsg()); err != nil {
		t.Fatal(err)
	}
	if got := folder(t, e, idAlice, "Junk"); len(got) != 1 {
		t.Fatalf("junk: %q", got)
	}
	// fileinto can take it out of Junk; redirect and vacation never run.
	setScript(t, e, idAlice, `require ["fileinto", "vacation", "copy"]; fileinto "INBOX"; redirect :copy "far@elsewhere.test"; vacation "away";`)
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, junkMsg()); err != nil {
		t.Fatal(err)
	}
	if got := inbox(t, e, idAlice); len(got) != 1 {
		t.Fatalf("inbox: %q", got)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 0 {
		t.Fatalf("sent for junk: %+v", got)
	}
	if ev := events(t, e, config.EventNotForwarded); len(ev) != 1 || !strings.Contains(ev[0].Detail, "junk") {
		t.Fatalf("events %+v", ev)
	}
	// A redirect without :copy that may not happen keeps the message.
	setScript(t, e, idAlice, `redirect "far@elsewhere.test";`)
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, junkMsg()); err != nil {
		t.Fatal(err)
	}
	if got := folder(t, e, idAlice, "Junk"); len(got) != 2 {
		t.Fatalf("refused redirect kept: %q", got)
	}
}

func TestSieveDMARCFailNotForwarded(t *testing.T) {
	e := start(t, fwdConfig(t))
	senderDNS(t, e, "v=DMARC1; p=none")
	e.from("198.51.100.7") // SPF fail, no DKIM: DMARC fails (policy none: not junk)
	c := dial(t, e.addr, false)
	setScript(t, e, idAlice, `redirect "far@elsewhere.test";`)
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 0 {
		t.Fatalf("forwarded: %+v", got)
	}
	if got := inbox(t, e, idAlice); len(got) != 1 {
		t.Fatalf("kept: %q", got)
	}
	if ev := events(t, e, config.EventNotForwarded); len(ev) != 1 || !strings.Contains(ev[0].Detail, "DMARC") {
		t.Fatalf("events %+v", ev)
	}
}

func TestSieveRedirect(t *testing.T) {
	cfg := fwdConfig(t)
	cfg.Aliases = append(cfg.Aliases, config.Alias{Address: "a2@example.org", Targets: []string{"alice@example.org"}})
	e := start(t, cfg)
	sendIt := goodSender(t, e)
	setScript(t, e, idAlice, `redirect "far@elsewhere.test";`)
	if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 1 || got[0].To[0] != "far@elsewhere.test" {
		t.Fatalf("sink %+v", got)
	}
	if orig, err := e.srs.Reverse(got[0].From); err != nil || orig != "a@sender.test" {
		t.Fatalf("SRS sender %q: %v", got[0].From, err)
	}
	data := string(got[0].Data)
	if !strings.HasPrefix(data, "Delivered-To: alice@example.org\r\n"+deliver.LoopHeader+": ") || strings.Contains(data, "Return-Path") {
		t.Fatalf("forwarded copy:\n%s", data)
	}
	if m := inbox(t, e, idAlice); len(m) != 0 {
		t.Fatalf("not kept: %q", m)
	}

	// At most 5 targets; own addresses (also aliases) are refused.
	setScript(t, e, idAlice, `require "copy";
redirect :copy "a2@example.org"; redirect :copy "ALICE@example.org";
redirect :copy "t1@elsewhere.test"; redirect :copy "t2@elsewhere.test"; redirect :copy "t3@elsewhere.test";
redirect :copy "t4@elsewhere.test"; redirect :copy "t5@elsewhere.test"; redirect :copy "t6@elsewhere.test";`)
	if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	got = e.sink.Messages()
	if len(got) != 2 || len(got[1].To) != 5 {
		t.Fatalf("targets: %+v", got)
	}
	ev := events(t, e, config.EventNotForwarded)
	if len(ev) != 3 || !strings.Contains(ev[0].Detail, "address of this mailbox") || !strings.Contains(ev[2].Detail, "at most 5") {
		t.Fatalf("events %+v", ev)
	}
	if m := inbox(t, e, idAlice); len(m) != 1 {
		t.Fatalf("copy kept: %q", m)
	}

	// Mailboxes that may not forward keep the message.
	cfg.Mailboxes[0].NoForwarding = true
	e.dir.Store(directory.New(cfg))
	setScript(t, e, idAlice, `redirect "far@elsewhere.test";`)
	if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 2 {
		t.Fatalf("forwarded although not allowed: %+v", got)
	}
	if m := inbox(t, e, idAlice); len(m) != 2 {
		t.Fatalf("not kept: %q", m)
	}
}

func TestSieveRedirectLimitAndLoops(t *testing.T) {
	cfg := fwdConfig(t)
	cfg.Mailboxes = append(cfg.Mailboxes, config.Mailbox{StorageID: "cccccccccccccccccccccccccccccccc", Address: "lim@limited.test", PasswordHash: cfg.Mailboxes[0].PasswordHash})
	e := start(t, cfg)
	sendIt := goodSender(t, e)

	// limited.test may send 1 message an hour: the second forward is not
	// sent and the message is kept.
	setScript(t, e, "cccccccccccccccccccccccccccccccc", `redirect "far@elsewhere.test";`)
	for i := 0; i < 2; i++ {
		if err := sendIt([]string{"lim@limited.test"}, goodMsg); err != nil {
			t.Fatal(err)
		}
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 1 {
		t.Fatalf("sink %+v", got)
	}
	if m := inbox(t, e, "cccccccccccccccccccccccccccccccc"); len(m) != 1 {
		t.Fatalf("kept over the limit: %q", m)
	}

	// Two mailboxes that forward to each other: the message ends up kept
	// where it started once it comes back.
	setScript(t, e, idAlice, `redirect "bob@example.org";`)
	setScript(t, e, idBob, `redirect "alice@example.org";`)
	if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	a, b := inbox(t, e, idAlice), inbox(t, e, idBob)
	if len(a) != 1 || len(b) != 0 || !strings.Contains(a[0], "Delivered-To: bob@example.org") {
		t.Fatalf("loop: alice %d, bob %d", len(a), len(b))
	}

	// A message with our loop mark from outside is not forwarded again.
	setScript(t, e, idBob, ``)
	looped := deliver.LoopHeader + ": x\r\n" + deliver.LoopHeader + ": y\r\n" + deliver.LoopHeader + ": z\r\n" +
		deliver.LoopHeader + ": v\r\n" + deliver.LoopHeader + ": w\r\n" + goodMsg
	if err := sendIt([]string{"alice@example.org"}, looped); err != nil {
		t.Fatal(err)
	}
	if got := inbox(t, e, idBob); len(got) != 0 {
		t.Fatalf("hop limit: %q", got)
	}
	if got := inbox(t, e, idAlice); len(got) != 2 {
		t.Fatalf("kept: %d", len(got))
	}
}

const vacationScript = `require ["vacation"]; vacation :days 7 :subject "Away" "I am away until Monday.";`

func TestSieveVacation(t *testing.T) {
	cfg := fwdConfig(t)
	cfg.Mailboxes[0].DisplayName = "Alice Ä"
	e := start(t, cfg)
	key := senderDNS(t, e, "v=DMARC1; p=reject")
	e.from("192.0.2.10")
	c := dial(t, e.addr, false)
	setScript(t, e, idAlice, vacationScript)
	msg := "From: A <a@sender.test>\r\nTo: alice@example.org\r\nSubject: hi\r\nDate: Mon, 23 Nov 2026 10:00:00 +0000\r\nMessage-ID: <1@sender.test>\r\nReferences: <0@sender.test>\r\n\r\nHello.\r\n"
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, sign(t, key, msg)); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 1 || got[0].From != "" || got[0].To[0] != "a@sender.test" {
		t.Fatalf("reply %+v", got)
	}
	data := string(got[0].Data)
	for _, want := range []string{"Auto-Submitted: auto-replied\r\n", "In-Reply-To: <1@sender.test>\r\n",
		"References: <0@sender.test> <1@sender.test>\r\n", "Subject: Away\r\n", "From: =?utf-8?q?Alice_=C3=84?= <alice@example.org>\r\n",
		"To: <a@sender.test>\r\n", "I am away until Monday."} {
		if !strings.Contains(data, want) {
			t.Errorf("reply lacks %q:\n%s", want, data)
		}
	}
	if m := inbox(t, e, idAlice); len(m) != 1 {
		t.Fatalf("kept: %d", len(m))
	}
	// Once per sender and period.
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, sign(t, key, msg)); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 1 {
		t.Fatalf("second reply within the period: %d", len(got))
	}
	e.clock.Add(7*24*time.Hour + time.Minute)
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, sign(t, key, msg)); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 2 {
		t.Fatalf("reply after the period: %d", len(got))
	}
	// Replies count against the mailbox's sending limit.
	n, _ := e.store.CountMarks(context.Background(), storage.KeySends+idAlice, e.clock.Now().Add(-30*24*time.Hour))
	if n != 2 {
		t.Fatalf("counted %d", n)
	}
}

func TestSieveVacationExclusions(t *testing.T) {
	base := "From: A <a@sender.test>\r\nTo: alice@example.org\r\nSubject: hi\r\nDate: Mon, 23 Nov 2026 10:00:00 +0000\r\nMessage-ID: <1@sender.test>\r\n\r\nHello.\r\n"
	cases := []struct {
		name, from, msg string
		signed          bool
	}{
		{"not in To or Cc", "a@sender.test", strings.Replace(base, "To: alice@example.org", "To: other@example.org", 1), true},
		{"auto-submitted", "a@sender.test", "Auto-Submitted: auto-generated\r\n" + base, true},
		{"precedence bulk", "a@sender.test", "Precedence: bulk\r\n" + base, true},
		{"precedence list", "a@sender.test", "Precedence: list\r\n" + base, true},
		{"list header", "a@sender.test", "List-Unsubscribe: <mailto:x@sender.test>\r\n" + base, true},
		{"null sender", "", base, true},
		{"mailer-daemon", "MAILER-DAEMON@sender.test", base, true},
		{"owner-", "owner-list@sender.test", base, true},
		{"-request", "list-request@sender.test", base, true},
		{"listserv", "listserv@sender.test", base, true},
		{"dkim fails", "a@sender.test", base, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := start(t, fwdConfig(t))
			key := senderDNS(t, e, "v=DMARC1; p=none")
			e.from("192.0.2.10")
			conn := dial(t, e.addr, false)
			setScript(t, e, idAlice, vacationScript)
			body := c.msg
			if c.signed {
				body = sign(t, key, body)
			} else {
				// A signature that does not verify.
				body = strings.Replace(sign(t, key, body), "Hello.", "Changed.", 1)
			}
			if err := send(conn, c.from, []string{"alice@example.org"}, body); err != nil {
				t.Fatal(err)
			}
			e.q.Flush()
			if got := e.sink.Messages(); len(got) != 0 {
				t.Fatalf("replied: %s", got[0].Data)
			}
		})
	}
	// "Auto-Submitted: no" still gets a reply; Cc counts, and so does an
	// alias of the mailbox and an address in :addresses.
	cfg := fwdConfig(t)
	cfg.Aliases = append(cfg.Aliases, config.Alias{Address: "a2@example.org", Targets: []string{"alice@example.org"}})
	e := start(t, cfg)
	key := senderDNS(t, e, "")
	e.from("192.0.2.10")
	conn := dial(t, e.addr, false)
	setScript(t, e, idAlice, `require "vacation"; vacation :addresses "me@private.test" :handle "h" "away";`)
	for i, hdr := range []string{"Auto-Submitted: no\r\n", "Cc: a2@example.org\r\n", "Cc: Me <me@private.test>\r\n"} {
		msg := hdr + strings.Replace(base, "To: alice@example.org", "To: someone@example.org", 1)
		if i == 0 {
			msg = hdr + base
		}
		from := []string{"a@sender.test", "b@sender.test", "c@sender.test"}[i]
		if err := send(conn, from, []string{"alice@example.org"}, sign(t, key, msg)); err != nil {
			t.Fatal(err)
		}
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 3 {
		t.Fatalf("replies %d", len(got))
	}
	// Our own address as sender: no reply (the message passes the checks
	// only from loopback, like a local tool).
	e.from("127.0.0.1")
	conn = dial(t, e.addr, false)
	if err := send(conn, "alice@example.org", []string{"alice@example.org"}, base); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 3 {
		t.Fatalf("replied to itself")
	}
}

func TestSieveVacationJunkAndSPF(t *testing.T) {
	e := start(t, fwdConfig(t))
	senderDNS(t, e, "")
	e.from("198.51.100.7") // SPF fail
	c := dial(t, e.addr, false)
	setScript(t, e, idAlice, vacationScript)
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, goodMsg); err != nil { // SPF fail, below junk
		t.Fatal(err)
	}
	if err := send(c, "a@sender.test", []string{"alice@example.org"}, junkMsg()); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 0 {
		t.Fatalf("replied: %+v", got)
	}
}
