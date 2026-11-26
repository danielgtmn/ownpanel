package smtpd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// readOnly makes a mailbox's message directory unwritable, so storing
// fails with a local error, until the returned function undoes it.
func readOnly(t *testing.T, e *env, id string) func() {
	t.Helper()
	ctx := context.Background()
	e.store.EnsureMailbox(ctx, id)
	if _, err := e.store.Append(ctx, id, storage.Inbox, strings.NewReader("Subject: x\r\n\r\nx\r\n"), storage.AppendOptions{}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.store.Dir(), "messages", id)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	undo := func() { os.Chmod(dir, 0o700) }
	t.Cleanup(undo)
	return undo
}

// A message refused with a temporary error (mailbox full) must not count
// as seen by "duplicate": its retry is delivered.
func TestSieveDuplicateOnlyAfterDelivery(t *testing.T) {
	cfg := bigConfig(t)
	cfg.Mailboxes[0].QuotaBytes = 10
	e := start(t, cfg)
	sendIt := goodSender(t, e)
	setScript(t, e, idAlice, `require "duplicate"; if duplicate { discard; stop; }`)
	if err := sendIt([]string{"alice@example.org"}, goodMsg); code(err) != 452 {
		t.Fatalf("full mailbox: %v", err)
	}
	cfg.Mailboxes[0].QuotaBytes = 0
	e.dir.Store(directory.New(cfg))
	if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	if got := inbox(t, e, idAlice); len(got) != 1 {
		t.Fatalf("retry lost: %d messages", len(got))
	}
	// Now it was delivered: the next copy is a duplicate.
	if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	if got := inbox(t, e, idAlice); len(got) != 1 {
		t.Fatalf("duplicate stored: %d messages", len(got))
	}
}

// A local forward target that cannot store makes the whole delivery fail
// temporarily, so the message is not lost there.
func TestSieveLocalForwardFailureRetries(t *testing.T) {
	e := start(t, bigConfig(t))
	sendIt := goodSender(t, e)
	setScript(t, e, idAlice, `redirect "bob@example.org";`)
	undo := readOnly(t, e, idBob)
	if err := sendIt([]string{"alice@example.org"}, goodMsg); code(err) != 451 {
		t.Fatalf("local target failed: %v", err)
	}
	undo()
	if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	if got := inbox(t, e, idBob); len(got) != 2 { // the seed message and the forward
		t.Fatalf("bob: %d", len(got))
	}
}

// Forwards and replies of filters leave only once every mailbox stored
// the message: a retry after another mailbox failed does not repeat them.
func TestSieveSideEffectsAfterAllStores(t *testing.T) {
	e := start(t, fwdConfig(t))
	sendIt := goodSender(t, e)
	setScript(t, e, idAlice, `require ["copy", "vacation"]; redirect :copy "far@elsewhere.test"; vacation "away";`)
	undo := readOnly(t, e, idBob)
	if err := sendIt([]string{"alice@example.org", "bob@example.org"}, goodMsg); code(err) != 451 {
		t.Fatalf("bob failed: %v", err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 0 {
		t.Fatalf("sent before the message was accepted: %d", len(got))
	}
	undo()
	if err := sendIt([]string{"alice@example.org", "bob@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 2 { // one forward, one reply
		t.Fatalf("sink: %d", len(got))
	}
	// The forward counted once against alice's limit.
	n, _ := e.store.CountMarks(context.Background(), storage.KeySends+idAlice, e.clock.Now().Add(-24*3600e9))
	if n != 2 {
		t.Fatalf("counted %d", n)
	}
}

// Header lines other parsers refuse do not hide the fields after them.
func TestSieveSeesWholeHeader(t *testing.T) {
	e := start(t, fwdConfig(t))
	sendIt := goodSender(t, e)
	setScript(t, e, idAlice, vacationScript)
	// To and From first, then the odd lines, then the list's field.
	msg := strings.Replace(goodMsg, "\r\n\r\n", "\r\nX(: odd\r\nX-\xc3\xa4: odd\r\nList-Id: <team.sender.test>\r\n\r\n", 1)
	if err := sendIt([]string{"alice@example.org"}, msg); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 0 {
		t.Fatalf("replied to a list: %s", got[0].Data)
	}
}

// A blocked mailbox forwards nothing and sends no replies; forwards count
// against the mailbox's own limits.
func TestSieveBlockedAndLimitedMailbox(t *testing.T) {
	cfg := fwdConfig(t)
	cfg.Mailboxes[0].SendPerHour = 1
	e := start(t, cfg)
	sendIt := goodSender(t, e)
	ctx := context.Background()
	setScript(t, e, idAlice, `require "vacation"; redirect "far@elsewhere.test"; vacation "away";`)
	if err := e.store.SetBlocked(ctx, idAlice, "test"); err != nil {
		t.Fatal(err)
	}
	if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 0 {
		t.Fatalf("blocked mailbox sent %d", len(got))
	}
	if got := inbox(t, e, idAlice); len(got) != 1 {
		t.Fatalf("not kept: %d", len(got))
	}
	if ev := events(t, e, config.EventNotForwarded); len(ev) != 1 || !strings.Contains(ev[0].Detail, "blocked") {
		t.Fatalf("events %+v", ev)
	}
	if _, err := e.store.Unblock(ctx, idAlice); err != nil {
		t.Fatal(err)
	}
	setScript(t, e, idAlice, `redirect "far@elsewhere.test";`)
	for i := 0; i < 2; i++ {
		if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
			t.Fatal(err)
		}
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 1 {
		t.Fatalf("over the hourly limit of 1: %d sent", len(got))
	}
	if ev := events(t, e, config.EventNotForwarded); len(ev) != 2 || !strings.Contains(ev[1].Detail, "per hour") {
		t.Fatalf("events %+v", ev)
	}
	if got := inbox(t, e, idAlice); len(got) != 2 {
		t.Fatalf("kept over the limit: %d", len(got))
	}
}

// vacation :mime cannot add header fields to the signed reply.
func TestSieveVacationMimeHeaders(t *testing.T) {
	e := start(t, fwdConfig(t))
	sendIt := goodSender(t, e)
	setScript(t, e, idAlice, "require \"vacation\";\nvacation :mime text:\nFrom: boss@example.org\nBcc: victim@elsewhere.test\nReply-To: evil@elsewhere.test\nContent-Type: text/plain; charset=utf-8\n\nAway.\n.\n;")
	if err := sendIt([]string{"alice@example.org"}, goodMsg); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 1 {
		t.Fatalf("replies %d", len(got))
	}
	data := string(got[0].Data)
	if strings.Count("\r\n"+data, "\r\nFrom:") != 1 || strings.Contains(data, "boss@") || strings.Contains(data, "Bcc:") ||
		strings.Contains(data, "\r\nReply-To:") || !strings.Contains(data, "Content-Type: text/plain; charset=utf-8\r\n\r\nAway.") {
		t.Fatalf("reply:\n%s", data)
	}
}

// Out-of-office replies go only to confirmed senders: SPF "none" and no
// signature is no reply (no backscatter to forged senders).
func TestSieveVacationNeedsConfirmedSender(t *testing.T) {
	e := start(t, fwdConfig(t))
	senderDNS(t, e, "")
	e.from("192.0.2.10") // reverse DNS, but nospf.test publishes nothing
	e.res.MX["nospf.test"] = e.res.MX["elsewhere.test"]
	c := dial(t, e.addr, false)
	setScript(t, e, idAlice, vacationScript)
	msg := strings.ReplaceAll(goodMsg, "sender.test", "nospf.test")
	if err := send(c, "a@nospf.test", []string{"alice@example.org"}, msg); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 0 {
		t.Fatalf("replied to an unconfirmed sender")
	}
}
