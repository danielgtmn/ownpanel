package smtpd

import (
	"context"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// A shared mailbox (G3) runs its own filters at delivery (G2): \Seen from
// a filter is left out (every reader has their own read state), other
// flags stay, and the out-of-office reply comes from the shared address or
// one of its own forwarding addresses.
func TestSieveSharedMailbox(t *testing.T) {
	const idInfo = "dddddddddddddddddddddddddddddddd"
	cfg := fwdConfig(t)
	cfg.Mailboxes = append(cfg.Mailboxes, config.Mailbox{StorageID: idInfo, Address: "info@example.org", DisplayName: "Info",
		Kind: config.KindShared, Grants: []config.Grant{{StorageID: idAlice, Rights: []string{config.RightRead, config.RightManage}}}})
	cfg.Aliases = append(cfg.Aliases, config.Alias{Address: "sales@example.org", Targets: []string{"info@example.org"}})
	e := start(t, cfg)
	ctx := context.Background()
	key := senderDNS(t, e, "v=DMARC1; p=reject")
	e.from("192.0.2.10")
	c := dial(t, e.addr, false)
	setScript(t, e, idInfo, `require ["imap4flags", "vacation"];
vacation :from "sales@example.org" :subject "Closed" "We are closed.";
keep :flags ["\\Seen", "\\Flagged"];`)
	msg := "From: A <a@sender.test>\r\nTo: info@example.org\r\nSubject: hi\r\nDate: Mon, 23 Nov 2026 10:00:00 +0000\r\nMessage-ID: <1@sender.test>\r\n\r\nHello.\r\n"
	if err := send(c, "a@sender.test", []string{"info@example.org"}, sign(t, key, msg)); err != nil {
		t.Fatal(err)
	}
	_, msgs, err := e.store.Select(ctx, idInfo, storage.Inbox)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("info inbox: %d %v", len(msgs), err)
	}
	if msgs[0].HasFlag(storage.FlagSeen) || !msgs[0].HasFlag(storage.FlagFlagged) {
		t.Fatalf("flags %v", msgs[0].Flags)
	}
	// Unread for the person who works with it, too.
	_, mine, err := e.store.SelectAs(ctx, idInfo, storage.Inbox, idAlice)
	if err != nil || len(mine) != 1 || mine[0].HasFlag(storage.FlagSeen) {
		t.Fatalf("alice's view: %+v %v", mine, err)
	}
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 1 || got[0].To[0] != "a@sender.test" {
		t.Fatalf("reply %+v", got)
	}
	if data := string(got[0].Data); !strings.Contains(data, "From: \"Info\" <sales@example.org>\r\n") || !strings.Contains(data, "We are closed.") {
		t.Fatalf("reply:\n%s", data)
	}

	// A personal mailbox keeps \Seen from its filter (TestSieveFileinto),
	// and an address of someone with access is no :from for the shared one.
	setScript(t, e, idInfo, `require ["vacation"]; vacation :handle "b" :from "alice@example.org" :subject "Closed" "We are closed.";`)
	if err := send(c, "a@sender.test", []string{"info@example.org"}, sign(t, key, strings.Replace(msg, "<1@", "<2@", 1))); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	got = e.sink.Messages()
	if len(got) != 2 || !strings.Contains(string(got[1].Data), "From: \"Info\" <info@example.org>\r\n") {
		t.Fatalf("second reply %+v", got)
	}
}
