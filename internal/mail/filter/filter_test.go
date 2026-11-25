package filter

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"net/netip"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

func ip4(s string) []net.IPAddr { return []net.IPAddr{{IP: net.ParseIP(s)}} }

func TestBlocklists(t *testing.T) {
	st, err := storage.Open(t.TempDir(), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var events []config.Event
	now := time.Now()
	f := New(Options{
		Store: st,
		Now:   func() time.Time { return now },
		Event: func(e config.Event) { events = append(events, e) },
		Resolver: &mailtest.Resolver{IP: map[string][]net.IPAddr{
			"1.2.0.192.zen.spamhaus.org": ip4("127.0.0.2"),       // SBL
			"4.2.0.192.zen.spamhaus.org": ip4("127.0.0.4"),       // XBL
			"2.2.0.192.zen.spamhaus.org": ip4("127.0.0.11"),      // PBL
			"2.2.0.192.bl.example":       ip4("127.0.0.2"),       // another list
			"3.2.0.192.zen.spamhaus.org": ip4("127.255.255.254"), // refused
			"3.2.0.192.bl.example":       ip4("127.255.255.254"),
		}},
	})
	zones := []string{"zen.spamhaus.org", "bl.example"}
	ctx := context.Background()
	for _, ip := range []string{"192.0.2.1", "192.0.2.4"} {
		if r := f.Blocklists(ctx, netip.MustParseAddr(ip), zones); r.Refuse != "zen.spamhaus.org" {
			t.Errorf("%s: %+v", ip, r)
		}
	}
	r := f.Blocklists(ctx, netip.MustParseAddr("192.0.2.2"), zones)
	if r.Refuse != "" || r.Score.Points != 2*PointsListed || r.Score.String() != "6 (listed:zen.spamhaus.org listed:bl.example)" {
		t.Errorf("PBL: %+v", r)
	}
	if r := f.Blocklists(ctx, netip.MustParseAddr("192.0.2.9"), zones); r.Refuse != "" || r.Score.Points != 0 {
		t.Errorf("clean: %+v", r)
	}
	if r := f.Blocklists(ctx, netip.MustParseAddr("2001:db8::1"), zones); r.Refuse != "" || r.Score.Points != 0 {
		t.Errorf("IPv6 looked up: %+v", r)
	}
	// Refused queries: no listing, one event per zone and day.
	for i := 0; i < 3; i++ {
		if r := f.Blocklists(ctx, netip.MustParseAddr("192.0.2.3"), zones); r.Refuse != "" || r.Score.Points != 0 {
			t.Errorf("refused counted as listing: %+v", r)
		}
	}
	if len(events) != 2 || events[0].Kind != config.EventBlocklistRefused {
		t.Fatalf("events %+v", events)
	}
	now = now.Add(25 * time.Hour)
	f.Blocklists(ctx, netip.MustParseAddr("192.0.2.3"), zones[:1])
	if len(events) != 3 {
		t.Fatalf("no new event after a day: %d", len(events))
	}
}

func TestCheckHeader(t *testing.T) {
	own := func(d string) bool { return d == "example.org" }
	h := func(lines ...string) textproto.MIMEHeader {
		r := textproto.NewReader(bufioReader(strings.Join(lines, "\r\n") + "\r\n\r\n"))
		m, err := r.ReadMIMEHeader()
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	ok := h("From: A <a@sender.test>", "Date: Mon, 23 Nov 2026 10:00:00 +0000", "Message-ID: <1@x>")
	if sc, d := CheckHeader(ok, []string{"example.org"}, own); sc.Points != 0 || len(d) != 1 || d[0] != "sender.test" {
		t.Errorf("clean: %v %v", sc, d)
	}
	sc, _ := CheckHeader(h("From: a@sender.test"), nil, own)
	if sc.String() != "2 (no-message-id no-date)" {
		t.Errorf("missing: %v", sc)
	}
	sc, d := CheckHeader(h("From: a@one.test", "From: b@two.test", "Date: x", "Message-ID: <1@x>"), nil, own)
	if sc.String() != "3.5 (bad-date multiple-from)" || len(d) != 2 {
		t.Errorf("multiple: %v %v", sc, d)
	}
	if sc, _ := CheckHeader(h("From: <<<", "Date: Mon, 23 Nov 2026 10:00:00 +0000", "Message-ID: <1@x>"), nil, own); sc.String() != "2.5 (bad-from)" {
		t.Errorf("malformed: %v", sc)
	}
	spoof := h(`From: "boss@example.org" <evil@attacker.test>`, "Date: Mon, 23 Nov 2026 10:00:00 +0000", "Message-ID: <1@x>")
	if sc, _ := CheckHeader(spoof, []string{"example.org"}, own); sc.String() != "3 (spoofed-name)" {
		t.Errorf("spoof: %v", sc)
	}
	// From our own domain the name is not suspicious.
	self := h(`From: "boss@example.org" <boss@example.org>`, "Date: Mon, 23 Nov 2026 10:00:00 +0000", "Message-ID: <1@x>")
	if sc, _ := CheckHeader(self, []string{"example.org"}, own); sc.Points != 0 {
		t.Errorf("self: %v", sc)
	}
}

func zipOf(t *testing.T, names ...string) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, n := range names {
		f, err := w.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte("x"))
	}
	w.Close()
	return b.Bytes()
}

func multipart(parts ...string) string {
	var b strings.Builder
	b.WriteString("From: a@sender.test\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n")
	b.WriteString("--b1\r\nContent-Type: text/plain\r\n\r\nSee attached.\r\n")
	for _, p := range parts {
		b.WriteString("--b1\r\n" + p + "\r\n")
	}
	b.WriteString("--b1--\r\n")
	return b.String()
}

func attachment(name, ctype string, body []byte) string {
	return "Content-Type: " + ctype + "\r\nContent-Disposition: attachment; filename=\"" + name + "\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString(body)
}

func TestDangerousAttachment(t *testing.T) {
	nested := "Content-Type: message/rfc822\r\n\r\n" + strings.ReplaceAll(multipart(attachment("run.ps1", "text/plain", []byte("x"))), "b1", "b2")
	cases := []struct {
		name, msg, found string
		parsed           bool
	}{
		{"plain", "From: a@b.test\r\n\r\nHello.\r\n", "", true},
		{"pdf", multipart(attachment("invoice.pdf", "application/pdf", []byte("%PDF"))), "", true},
		{"exe", multipart(attachment("invoice.pdf.exe", "application/octet-stream", []byte("MZ"))), "invoice.pdf.exe", true},
		{"exe with trailing dot", multipart(attachment("setup.EXE.", "application/octet-stream", []byte("MZ"))), "setup.EXE.", true},
		{"docm", multipart(attachment("report.docm", "application/vnd.ms-word.document.macroEnabled.12", []byte("x"))), "report.docm", true},
		{"name in content type", multipart("Content-Type: application/octet-stream; name=\"x.vbs\"\r\n\r\nx"), "x.vbs", true},
		{"rfc 2231", multipart("Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename*=utf-8''r%C3%A9sum%C3%A9.js\r\n\r\nx"), "résumé.js", true},
		{"encoded word", multipart("Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"=?utf-8?q?a.scr?=\"\r\n\r\nx"), "a.scr", true},
		{"zip with js", multipart(attachment("docs.zip", "application/zip", zipOf(t, "readme.txt", "dir/payload.js"))), "dir/payload.js", true},
		{"zip clean", multipart(attachment("docs.zip", "application/zip", zipOf(t, "readme.txt"))), "", true},
		{"nested message", multipart(nested), "run.ps1", true},
		{"broken boundary", "From: a@b.test\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n--b1\r\nContent-Type: text/plain\r\n\r\nno end", "", false},
	}
	for _, c := range cases {
		found, parsed := DangerousAttachment(strings.NewReader(c.msg))
		if found != c.found || parsed != c.parsed {
			t.Errorf("%s: %q %v, want %q %v", c.name, found, parsed, c.found, c.parsed)
		}
	}
}

func TestNetwork(t *testing.T) {
	if Network(netip.MustParseAddr("192.0.2.77")) != "192.0.2.0/24" || Network(netip.MustParseAddr("2001:db8:1:2:3::9")) != "2001:db8:1:2::/64" {
		t.Fatal("network")
	}
}

func bufioReader(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }
