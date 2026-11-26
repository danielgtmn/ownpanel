package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

func prep(t *testing.T, msg string, meta bool) (*prepared, string) {
	t.Helper()
	var out bytes.Buffer
	p, err := prepare(strings.NewReader(msg), &out, prepOptions{host: "mail.example.net", meta: meta})
	if err != nil {
		t.Fatal(err)
	}
	return p, out.String()
}

func TestPrepare(t *testing.T) {
	msg := "Received: from x by y; Tue, 2 Jan 2024 10:00:00 +0000\n" +
		"Authentication-Results: mail.example.net; spf=pass\n" +
		"Authentication-Results: mx.other.test; spf=fail\n" +
		"Status: RO\nX-Status: AF\nX-Keywords: $Forwarded, Work, bad(kw\n" +
		"X-Mozilla-Status: 0001\n" +
		"Date: Mon, 1 Jan 2024 09:00:00 +0000\n" +
		"Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\n\nline1\nline2\r\n"
	p, out := prep(t, msg, true)
	want := "Received: from x by y; Tue, 2 Jan 2024 10:00:00 +0000\r\n" +
		"Authentication-Results: mx.other.test; spf=fail\r\n" +
		"Date: Mon, 1 Jan 2024 09:00:00 +0000\r\n" +
		"Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\n\r\nline1\r\nline2\r\n"
	if out != want {
		t.Fatalf("stored %q", out)
	}
	if p.size != int64(len(want)) || p.subject != "Grüße" {
		t.Fatalf("size %d subject %q", p.size, p.subject)
	}
	if !p.received.Equal(time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC)) || !p.date.Equal(time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("dates %v %v", p.received, p.date)
	}
	if want := []string{"$Forwarded", "Work", storage.FlagAnswered, storage.FlagFlagged, storage.FlagSeen}; !slices.Equal(p.flags, want) {
		t.Fatalf("flags %v", p.flags)
	}

	// Without meta the fields stay, but the key is the same.
	p2, out2 := prep(t, msg, false)
	if !strings.Contains(out2, "X-Status: AF\r\n") || !bytes.Equal(p.key, p2.key) || len(p2.flags) != 0 {
		t.Fatalf("no meta: %q %v", out2, p2.flags)
	}
	// Other metadata, same message: same key.
	p3, _ := prep(t, strings.Replace(msg, "Status: RO\n", "Status: O\nX-Gmail-Labels: Inbox\n", 1), true)
	if !bytes.Equal(p.key, p3.key) || !slices.Equal(p3.labels, []string{"Inbox"}) {
		t.Fatal("key depends on metadata")
	}
	// A different body: another key.
	p4, _ := prep(t, msg+"x", true)
	if bytes.Equal(p.key, p4.key) {
		t.Fatal("same key")
	}

	// Mozilla flags and deletion.
	p5, _ := prep(t, "X-Mozilla-Status: 100a\nX-Mozilla-Keys: $label1\n\nx", true)
	if !p5.deleted || !slices.Equal(p5.flags, []string{"$Forwarded", "$label1", storage.FlagAnswered}) {
		t.Fatalf("mozilla %v %v", p5.deleted, p5.flags)
	}
}

func TestPrepareLimits(t *testing.T) {
	_, err := prepare(strings.NewReader("Subject: x\r\n\r\n"+strings.Repeat("a", 100)), io.Discard, prepOptions{max: 50})
	if !errors.Is(err, errTooLarge) {
		t.Fatal(err)
	}
	_, err = prepare(strings.NewReader("X: "+strings.Repeat("a", maxHeaderBytes)+"\r\n\r\n"), io.Discard, prepOptions{})
	if !errors.Is(err, errHeaderTooLong) {
		t.Fatal(err)
	}
	// No header at all: the text is the body.
	_, out := prep(t, "just text\nmore", false)
	if out != "just text\r\nmore" {
		t.Fatalf("%q", out)
	}
}

func TestCleanFlags(t *testing.T) {
	in := []string{`\Seen`, `\Deleted`, `\Recent`, `\Bogus`, "ok", "bad kw", "$Junk"}
	for i := 0; i < 60; i++ {
		in = append(in, "k"+strings.Repeat("x", i+1))
	}
	out := cleanFlags(in)
	kw := 0
	for _, f := range out {
		if f == `\Deleted` || f == `\Recent` || f == "bad kw" {
			t.Fatalf("kept %s", f)
		}
		if !strings.HasPrefix(f, `\`) {
			kw++
		}
	}
	if kw != maxKeywords || !slices.Contains(out, `\Seen`) {
		t.Fatalf("%d keywords: %v", kw, out)
	}
}

func TestSplitLabels(t *testing.T) {
	got := splitLabels(`Inbox,"Work, old",Category Updates, Unread`)
	if !slices.Equal(got, []string{"Inbox", "Work, old", "Category Updates", "Unread"}) {
		t.Fatalf("%q", got)
	}
}

func mboxMessages(t *testing.T, data string, rd bool) ([]string, []time.Time) {
	t.Helper()
	mr, err := newMboxReader(context.Background(), strings.NewReader(data), rd)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	var dates []time.Time
	for {
		m, err := mr.nextMessage()
		if err != nil {
			t.Fatal(err)
		}
		if m == nil {
			return out, dates
		}
		b, err := io.ReadAll(m)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(b))
		dates = append(dates, m.from)
	}
}

func TestMbox(t *testing.T) {
	data := "From a@b Thu Jan  4 10:00:00 2024\nSubject: one\n\nbody\n>From quoted\nFrom here on, plain text\n\n" +
		"From - Fri Jan 05 11:00:00 2024\nSubject: two\n\n\n>>From x\n"
	msgs, dates := mboxMessages(t, data, false)
	if len(msgs) != 2 {
		t.Fatalf("%d messages: %q", len(msgs), msgs)
	}
	if msgs[0] != "Subject: one\n\nbody\n>From quoted\nFrom here on, plain text\n" {
		t.Fatalf("first %q", msgs[0])
	}
	if msgs[1] != "Subject: two\n\n\n>>From x\n" {
		t.Fatalf("second %q", msgs[1])
	}
	if !dates[0].Equal(time.Date(2024, 1, 4, 10, 0, 0, 0, time.UTC)) || !dates[1].Equal(time.Date(2024, 1, 5, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("dates %v", dates)
	}
	// mboxrd unquotes one level.
	msgs, _ = mboxMessages(t, data, true)
	if msgs[0] != "Subject: one\n\nbody\nFrom quoted\nFrom here on, plain text\n" || msgs[1] != "Subject: two\n\n\n>From x\n" {
		t.Fatalf("rd %q", msgs)
	}
	if _, err := newMboxReader(context.Background(), strings.NewReader("Subject: x\n\n"), false); !errors.Is(err, errNotMbox) {
		t.Fatal(err)
	}
	// Takeout's form of the date.
	if d := fromLineDate("From 1787095557347826@xxx Tue Jan 21 06:36:06 +0000 2020\r\n"); !d.Equal(time.Date(2020, 1, 21, 6, 36, 6, 0, time.UTC)) {
		t.Fatalf("takeout date %v", d)
	}
}

func TestFolderNames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		delim  rune
		prefix string
		want   string
	}{
		{"INBOX", '.', "INBOX.", "INBOX"},
		{"INBOX.Work.2024", '.', "INBOX.", "Work/2024"},
		{"Work/a.b", '/', "", "Work/a.b"},
		{"Kunden.A/B*C%", '.', "", "Kunden/A_B_C_"},
		{"flat/name", 0, "", "flat_name"},
		{"inbox/Sub", '/', "", "INBOX/Sub"},
		{"a..b", '.', "", "a/_/b"},
		{"ctl\x01x", '/', "", "ctl_x"},
	} {
		if got := joinFolder(remoteLevels(tc.name, tc.delim, tc.prefix)); got != tc.want {
			t.Errorf("%q → %q, want %q", tc.name, got, tc.want)
		}
	}
	deep := strings.Repeat("a/", maxFolderDepth) + "a"
	if got := joinFolder(remoteLevels(deep, '/', "")); got != "" {
		t.Errorf("too deep: %q", got)
	}
	long := joinFolder([]string{strings.Repeat("ü", 200)})
	if len(long) > 255 || !strings.HasPrefix(long, "ü") {
		t.Errorf("long: %d", len(long))
	}
}

func TestPlanGmail(t *testing.T) {
	src := []sourceFolder{
		{remote: "INBOX", levels: []string{"INBOX"}, messages: 2, bytes: 20},
		{remote: "[Gmail]", levels: []string{"[Gmail]"}, noSelect: true},
		{remote: "[Gmail]/All Mail", levels: []string{"[Gmail]", "All Mail"}, attrs: []string{`\All`}, messages: 5, bytes: 50},
		{remote: "[Gmail]/Sent Mail", levels: []string{"[Gmail]", "Sent Mail"}, attrs: []string{`\Sent`}, messages: 1, bytes: 10},
		{remote: "[Gmail]/Starred", levels: []string{"[Gmail]", "Starred"}, attrs: []string{`\Flagged`}, messages: 1},
		{remote: "[Gmail]/Important", levels: []string{"[Gmail]", "Important"}, attrs: []string{`\Important`}, messages: 1},
		{remote: "[Gmail]/Spam", levels: []string{"[Gmail]", "Spam"}, attrs: []string{`\Junk`}, messages: 1},
		{remote: "[Gmail]/Bin", levels: []string{"[Gmail]", "Bin"}, messages: 1}, // no attribute: by name
		{remote: "Gesendete Objekte", levels: []string{"Gesendete Objekte"}, messages: 1},
		{remote: "Work", levels: []string{"Work"}, messages: 1, bytes: 10},
	}
	local := newLocalFolders([]storage.Folder{{Name: "Gesendet", SpecialUse: storage.SpecialSent}})
	p := makePlan(src, local)
	c := p.check()
	byRemote := map[string]config.ImportFolder{}
	for _, f := range c.Folders {
		byRemote[f.Remote] = f
	}
	if _, ok := byRemote["[Gmail]"]; ok {
		t.Fatal("noselect listed")
	}
	if f := byRemote["[Gmail]/All Mail"]; f.Local != "Archive" || !f.Selected || f.Note != config.ImportNoteAllMail {
		t.Fatalf("all mail %+v", f)
	}
	if f := byRemote["[Gmail]/Sent Mail"]; f.Local != "Gesendet" || f.SpecialUse != storage.SpecialSent {
		t.Fatalf("sent %+v", f)
	}
	if f := byRemote["Gesendete Objekte"]; f.Local != "Gesendet" {
		t.Fatalf("sent by name %+v", f)
	}
	for _, n := range []string{"[Gmail]/Starred", "[Gmail]/Important", "[Gmail]/Spam", "[Gmail]/Bin"} {
		if byRemote[n].Selected {
			t.Fatalf("%s selected", n)
		}
	}
	if c.Messages != 2+5+1+1+1 || c.Bytes != 20+50+10+10 {
		t.Fatalf("totals %d %d", c.Messages, c.Bytes)
	}
	chosen, err := p.choose(config.ImportChoices{IncludeJunk: true})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, f := range chosen {
		order = append(order, f.Remote)
	}
	if !slices.Equal(order, []string{"INBOX", "[Gmail]/Sent Mail", "[Gmail]/Spam", "Gesendete Objekte", "Work", "[Gmail]/All Mail"}) {
		t.Fatalf("order %v", order)
	}
	if _, err := p.choose(config.ImportChoices{Folders: []string{"Nope"}}); err == nil {
		t.Fatal("unknown folder chosen")
	}
	chosen, _ = p.choose(config.ImportChoices{Folders: []string{"[Gmail]/All Mail", "Work"}})
	if len(chosen) != 2 || chosen[0].Remote != "Work" {
		t.Fatalf("explicit %v", chosen)
	}
}

func TestTakeoutFolder(t *testing.T) {
	for _, tc := range []struct {
		labels string
		want   string
		r      role
	}{
		{"Inbox,Unread", "INBOX", roleNone},
		{"Spam,Unread", "Junk", roleJunk},
		{"Sent,Opened", "Sent", roleSent},
		{"Important,Work/Clients,Opened", "Work/Clients", roleNone},
		{"Opened,Category Updates", "Archive", roleArchive},
		{"", "Archive", roleArchive},
	} {
		lv, r := takeoutFolder(splitLabels(tc.labels))
		if strings.Join(lv, "/") != tc.want || r != tc.r {
			t.Errorf("%q → %v %v", tc.labels, lv, r)
		}
	}
	if f := takeoutFlags([]string{"Starred"}, nil); !slices.Equal(f, []string{storage.FlagFlagged, storage.FlagSeen}) {
		t.Fatalf("%v", f)
	}
	if f := takeoutFlags([]string{"Unread"}, nil); len(f) != 0 {
		t.Fatalf("%v", f)
	}
}

func TestCleanText(t *testing.T) {
	got := cleanText("imap: NO [AUTHENTICATIONFAILED] bad\r\nlogin for hunter2 \"hunter2\"", "hunter2")
	if strings.Contains(got, "hunter2") || strings.Contains(got, "\n") || !strings.HasPrefix(got, "NO") {
		t.Fatalf("%q", got)
	}
	if n := len([]rune(cleanText(strings.Repeat("x", 500), ""))); n != 201 {
		t.Fatalf("len %d", n)
	}
}

func TestExportPaths(t *testing.T) {
	ps := newPathSet()
	for _, tc := range []struct{ name, want string }{
		{"INBOX", "INBOX.mbox"},
		{"Work", "Work.mbox"},
		{"work", "work~2.mbox"},
		{"a/../b", "a/_../b.mbox"},
		{"x:y\\z", "x_y_z.mbox"},
		{".", "_..mbox"},
	} {
		if got := ps.add(tc.name); got != tc.want {
			t.Errorf("%q → %q, want %q", tc.name, got, tc.want)
		}
	}
}
