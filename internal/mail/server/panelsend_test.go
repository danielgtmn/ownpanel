package server

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// readInbox returns the raw messages in the INBOX of a mailbox.
func readInbox(t *testing.T, st *storage.Store, id string) []string {
	t.Helper()
	_, msgs, err := st.Select(context.Background(), id, storage.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		f, err := st.Open(m)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		out = append(out, string(b))
	}
	return out
}

// TestPanelSendLocal: a message of the panel lands in a local mailbox,
// signed, with the headers the panel cannot set itself.
func TestPanelSendLocal(t *testing.T) {
	ctx := context.Background()
	in := run(t, t.TempDir(), socketDir(t))
	defer in.stop(t)
	key, _ := mailtest.DKIMKey(t)
	cfg := testConfig(t)
	cfg.Domains[0].DKIM = []config.DKIMKey{{Selector: "s1", PrivateKeyPEM: key, Active: true}}
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	err := in.client.Send(ctx, config.OutgoingMail{
		From: "no-reply@example.org", FromName: "Pänel", To: "Alice@Example.org",
		Subject: "Passwort zurücksetzen", Text: "Hallo,\nöffne den Link.", HTML: "<p>Hallo</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := readInbox(t, in.srv.Store(), idAlice)
	if len(got) != 1 {
		t.Fatalf("%d messages", len(got))
	}
	raw := got[0]
	if !strings.HasPrefix(raw, "Return-Path: <no-reply@example.org>\r\nDKIM-Signature: ") {
		t.Fatalf("message starts %q", raw[:min(len(raw), 120)])
	}
	if strings.Contains(strings.ReplaceAll(raw, "\r\n", ""), "\n") {
		t.Fatal("bare LF in message")
	}
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mime.WordDecoder)
	subj, _ := dec.DecodeHeader(msg.Header.Get("Subject"))
	from, _ := msg.Header.AddressList("From")
	if subj != "Passwort zurücksetzen" || len(from) != 1 || from[0].Name != "Pänel" || from[0].Address != "no-reply@example.org" ||
		msg.Header.Get("To") != "<alice@example.org>" || msg.Header.Get("Auto-Submitted") != "auto-generated" ||
		!strings.HasSuffix(msg.Header.Get("Message-Id"), "@mail.example.net>") || msg.Header.Get("Date") == "" {
		t.Fatalf("header %v (subject %q)", msg.Header, subj)
	}
	typ, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || typ != "multipart/alternative" {
		t.Fatalf("content type %q %v", typ, err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextPart() // decodes quoted-printable
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		parts = append(parts, p.Header.Get("Content-Type")+"|"+string(b))
	}
	if len(parts) != 2 || parts[0] != "text/plain; charset=utf-8|Hallo,\r\nöffne den Link.\r\n" ||
		parts[1] != "text/html; charset=utf-8|<p>Hallo</p>\r\n" {
		t.Fatalf("parts %q", parts)
	}

	// Text only, without a key: plain text/plain, unsigned.
	cfg.Domains[0].DKIM = nil
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := in.client.Send(ctx, config.OutgoingMail{From: "no-reply@example.org", To: "alice@example.org", Subject: "Hi", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	got = readInbox(t, in.srv.Store(), idAlice)
	if len(got) != 2 || strings.Contains(got[1], "DKIM-Signature") ||
		!strings.Contains(got[1], "Subject: Hi\r\n") || !strings.Contains(got[1], "Content-Type: text/plain; charset=utf-8\r\n") ||
		!strings.HasSuffix(got[1], "\r\n\r\nx\r\n") || !strings.Contains(got[1], "From: <no-reply@example.org>\r\n") {
		t.Fatalf("message %q", got[1])
	}
}

// TestPanelSendOutside: a message to an outside address goes through the
// queue, signed, with From as the envelope sender.
func TestPanelSendOutside(t *testing.T) {
	ctx := context.Background()
	sink := mailtest.NewSink(t, mailtest.SinkStartTLS)
	in := run(t, t.TempDir(), socketDir(t), func(o *Options) {
		o.QueueResolver = &mailtest.Resolver{
			MX: map[string][]*net.MX{"remote.test": {{Host: "mx.remote.test", Pref: 10}}},
			IP: map[string][]net.IPAddr{"mx.remote.test": mailtest.Localhost},
		}
		o.QueuePort = sink.Port
		o.AllowPrivateMX = true
	})
	defer in.stop(t)
	key, txt := mailtest.DKIMKey(t)
	cfg := testConfig(t)
	cfg.Domains[0].DKIM = []config.DKIMKey{{Selector: "s1", PrivateKeyPEM: key, Active: true}}
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := in.client.Send(ctx, config.OutgoingMail{From: "no-reply@example.org", To: "carol@remote.test",
		Subject: "Neues Passwort für dein Konto bei einem sehr langen Anbieternamen, damit der Betreff gefaltet wird", Text: "Link", HTML: "<a href=\"https://x.test/r?t=abc\">Link</a>"}); err != nil {
		t.Fatal(err)
	}
	got := sink.Wait(1, 10*time.Second)
	if len(got) != 1 || got[0].From != "no-reply@example.org" || len(got[0].To) != 1 || got[0].To[0] != "carol@remote.test" ||
		!strings.HasPrefix(string(got[0].Data), "DKIM-Signature: ") || !strings.Contains(string(got[0].Data), "\r\n\r\nLink\r\n") {
		t.Fatalf("sink %+v", got)
	}
	vs, err := dkim.VerifyWithOptions(strings.NewReader(string(got[0].Data)), &dkim.VerifyOptions{
		LookupTXT: func(string) ([]string, error) { return []string{txt}, nil },
	})
	if err != nil || len(vs) != 1 || vs[0].Err != nil || vs[0].Domain != "example.org" {
		t.Fatalf("verify: %v %+v", err, vs)
	}
	for _, line := range strings.Split(string(got[0].Data), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d bytes", len(line))
		}
	}
}

func TestPanelSendRefused(t *testing.T) {
	ctx := context.Background()
	in := run(t, t.TempDir(), socketDir(t))
	defer in.stop(t)
	cfg := testConfig(t)
	cfg.Mailboxes[0].QuotaBytes = 1
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	ok := config.OutgoingMail{From: "no-reply@example.org", To: "alice@example.org", Subject: "S", Text: "T"}
	for _, c := range []struct {
		name string
		mod  func(*config.OutgoingMail)
		want string
	}{
		{"foreign sender", func(m *config.OutgoingMail) { m.From = "x@other.test" }, "the sender's domain is not a mail domain of this server"},
		{"display name in from", func(m *config.OutgoingMail) { m.From = "Panel <no-reply@example.org>" }, "the sender is not a valid address"},
		{"subject injection", func(m *config.OutgoingMail) { m.Subject = "S\r\nBcc: x@remote.test" }, "header fields must not contain line breaks or control characters"},
		{"name injection", func(m *config.OutgoingMail) { m.FromName = "P\nX: y" }, "header fields must not contain line breaks or control characters"},
		{"two recipients", func(m *config.OutgoingMail) { m.To = "a@remote.test, b@remote.test" }, "the recipient must be a single valid address"},
		{"unknown local", func(m *config.OutgoingMail) { m.To = "nobody@example.org" }, "unknown recipient"},
		{"empty subject", func(m *config.OutgoingMail) { m.Subject = " " }, "the subject is empty"},
		{"empty text", func(m *config.OutgoingMail) { m.Text = "" }, "the text is empty"},
		{"too large", func(m *config.OutgoingMail) { m.Text = strings.Repeat("x", maxSendBytes) }, "the message is too large"},
		{"mailbox full", func(*config.OutgoingMail) {}, "the recipient's mailbox is full"},
	} {
		m := ok
		c.mod(&m)
		err := in.client.Send(ctx, m)
		var e *config.Error
		if !errors.As(err, &e) || e.Status != http.StatusBadRequest || e.Msg != c.want {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if n := len(readInbox(t, in.srv.Store(), idAlice)); n != 0 {
		t.Fatalf("%d messages stored", n)
	}
}
