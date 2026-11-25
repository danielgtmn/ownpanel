package server

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
)

// TestSendEndToEnd: a mail client sends through both submission ports to a
// local mailbox (read back over IMAP) and to an outside address (delivered
// by the queue to a local sink).
func TestSendEndToEnd(t *testing.T) {
	ctx := context.Background()
	sink := mailtest.NewSink(t, mailtest.SinkStartTLS)
	in := run(t, t.TempDir(), socketDir(t), func(o *Options) {
		o.QueueResolver = &mailtest.Resolver{
			MX: map[string][]*net.MX{"remote.test": {{Host: "mx.remote.test", Pref: 10}}},
			IP: map[string][]net.IPAddr{"mx.remote.test": mailtest.Localhost},
		}
		o.QueuePort = sink.Port
		o.AllowPrivateMX = true // the sink listens on 127.0.0.1
	})
	defer in.stop(t)
	key, _ := mailtest.DKIMKey(t)
	cfg := testConfig(t)
	cfg.Domains[0].DKIM = []config.DKIMKey{{Selector: "s1", PrivateKeyPEM: key, Active: true}}
	cfg.Mailboxes[0].PasswordHash = mailtest.CheapHash("pw")
	cfg.Mailboxes = append(cfg.Mailboxes, config.Mailbox{StorageID: idBob, Address: "bob@example.org", PasswordHash: mailtest.CheapHash("pw")})
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st, _ := in.client.Status(ctx)
	sub, subs := listener(st, "submission"), listener(st, "submissions")
	if !sub.Listening || !subs.Listening || sub.Error != "" {
		t.Fatalf("listeners %+v %+v", sub, subs)
	}

	c, err := smtp.DialStartTLS(sub.Address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Auth(sasl.NewPlainClient("", "alice@example.org", "pw")); err != nil {
		t.Fatal(err)
	}
	body := "From: alice@example.org\r\nTo: bob@example.org, carol@remote.test\r\nSubject: Hi\r\n\r\nHello Bob and Carol.\r\n"
	if err := c.SendMail("alice@example.org", []string{"bob@example.org", "carol@remote.test"}, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}

	got := sink.Wait(1, 10*time.Second)
	if len(got) != 1 || got[0].To[0] != "carol@remote.test" || !strings.Contains(string(got[0].Data), "DKIM-Signature: ") {
		t.Fatalf("sink %+v", got)
	}
	for i := 0; ; i++ {
		st, _ = in.client.Status(ctx)
		if st.QueueLength == 0 {
			break
		}
		if i > 200 {
			t.Fatalf("queue length %d", st.QueueLength)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Bob reads it over IMAP.
	ic, err := imapclient.DialTLS(listener(st, "imaps").Address, &imapclient.Options{TLSConfig: &tls.Config{InsecureSkipVerify: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer ic.Close()
	if err := ic.Login("bob@example.org", "pw").Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	msgs, err := ic.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{{}}}).Collect()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("fetch %v %d", err, len(msgs))
	}
	var text string
	for _, b := range msgs[0].BodySection {
		text = string(b.Bytes)
	}
	if !strings.Contains(text, "Hello Bob and Carol.") || !strings.Contains(text, "(authenticated)") {
		t.Fatalf("message %q", text)
	}

	// Port 465 works the same.
	tc, err := smtp.DialTLS(subs.Address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	if err := tc.Auth(sasl.NewPlainClient("", "bob@example.org", "pw")); err != nil {
		t.Fatal(err)
	}
	if err := tc.SendMail("bob@example.org", []string{"alice@example.org"}, strings.NewReader("From: bob@example.org\r\n\r\nx\r\n")); err != nil {
		t.Fatal(err)
	}
}
