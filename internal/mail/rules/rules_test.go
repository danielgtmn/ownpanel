package rules

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/sieve"
)

type msg map[string][]string

func (m msg) Header(n string) []string       { return m[n] }
func (m msg) Size() int64                    { return 100 }
func (m msg) Body(string, []string) []string { return nil }

func full() Settings {
	return Settings{
		OutOfOffice: config.OutOfOffice{Enabled: true, Subject: `Away "now"`, Text: "Back on\nMonday \\ later", Start: "2026-12-01", End: "2026-12-24", Zone: "+0100", Days: 3},
		Forwarding:  config.Forwarding{Enabled: true, To: "boss@example.net", KeepCopy: true},
		Filters: []config.FilterRule{
			{Name: "News", Conditions: []config.FilterCondition{{Field: "list", Op: "is", Value: "news.example.org"}}, Folder: "News", MarkRead: true},
			{Name: "Spam words", MatchAny: true, Conditions: []config.FilterCondition{
				{Field: "subject", Op: "contains", Value: "viagra"}, {Field: "from", Op: "is", Value: "bad@example.com"}}, Delete: true},
			{Name: "Boss", Conditions: []config.FilterCondition{{Field: "to", Op: "contains", Value: "boss"}}, ForwardTo: "me@else.test", MarkRead: true},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	s := full()
	if err := Check(&s); err != nil {
		t.Fatal(err)
	}
	src := Generate(s)
	if _, err := sieve.Compile(src); err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	got, ok := Parse(src)
	if !ok || Generate(got) != src || got.OutOfOffice.Text != s.OutOfOffice.Text {
		t.Fatalf("parse: %v %+v", ok, got)
	}
	// Any change by hand makes it unmanaged.
	if _, ok := Parse(src + "\nkeep;\n"); ok {
		t.Fatal("edited script parsed")
	}
	if _, ok := Parse(strings.Replace(src, "stop;", "keep;", 1)); ok {
		t.Fatal("edited script parsed")
	}
	if _, ok := Parse(`keep;`); ok {
		t.Fatal("foreign script parsed")
	}
}

func runScript(t *testing.T, s Settings, m msg, now time.Time) *sieve.Result {
	t.Helper()
	sc, err := sieve.Compile(Generate(s))
	if err != nil {
		t.Fatal(err)
	}
	r, err := sc.Run(context.Background(), &sieve.Env{Message: m, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBehaviour(t *testing.T) {
	s := full()
	if err := Check(&s); err != nil {
		t.Fatal(err)
	}
	in := time.Date(2026, 12, 24, 22, 0, 0, 0, time.UTC) // 23:00 at +0100: still the 24th
	out := time.Date(2026, 12, 24, 23, 30, 0, 0, time.UTC)
	news := msg{"list-id": {"News <news.example.org>"}, "subject": {"x"}}
	r := runScript(t, s, news, in)
	kinds := []sieve.ActionKind{}
	for _, a := range r.Actions {
		kinds = append(kinds, a.Kind)
	}
	if len(r.Actions) != 3 || r.Actions[0].Kind != sieve.ActVacation || r.Actions[1].Kind != sieve.ActRedirect || !r.Actions[1].Copy ||
		r.Actions[2].Folder != "News" || r.Actions[2].Flags[0] != `\Seen` || r.ImplicitKeep {
		t.Fatalf("news: %v %+v", kinds, r)
	}
	if v := r.Actions[0].Vacation; v.Subject != `Away "now"` || v.Reason != "Back on\nMonday \\ later" || v.Period != 3*24*time.Hour {
		t.Fatalf("vacation %+v", v)
	}
	r = runScript(t, s, news, out)
	if r.Actions[0].Kind == sieve.ActVacation {
		t.Fatal("vacation after the end")
	}
	spam := msg{"from": {"Bad <bad@example.com>"}, "subject": {"hello"}}
	r = runScript(t, s, spam, out)
	if last := r.Actions[len(r.Actions)-1]; last.Kind != sieve.ActDiscard || r.ImplicitKeep {
		t.Fatalf("delete: %+v", r)
	}
	boss := msg{"to": {"The Boss <boss@example.org>"}}
	r = runScript(t, s, boss, out)
	if len(r.Actions) != 3 || r.Actions[1].Address != "me@else.test" || r.Actions[2].Kind != sieve.ActKeep || r.Actions[2].Flags[0] != `\Seen` {
		t.Fatalf("boss: %+v", r)
	}
	// Forwarding without a copy stops everything after it.
	s.Forwarding.KeepCopy = false
	r = runScript(t, s, news, out)
	if len(r.Actions) != 1 || r.ImplicitKeep {
		t.Fatalf("forward only: %+v", r)
	}
}

func TestCheck(t *testing.T) {
	bad := []Settings{
		{OutOfOffice: config.OutOfOffice{Enabled: true}},
		{OutOfOffice: config.OutOfOffice{Enabled: true, Text: "x", Days: 91}},
		{OutOfOffice: config.OutOfOffice{Enabled: true, Text: "x", Start: "2026-12-24", End: "2026-12-01"}},
		{OutOfOffice: config.OutOfOffice{Enabled: true, Text: "x", Zone: "CET"}},
		{OutOfOffice: config.OutOfOffice{Enabled: true, Text: "x", Subject: "a\nb"}},
		{Forwarding: config.Forwarding{Enabled: true, To: "nope"}},
		{Filters: []config.FilterRule{{Name: "x", Folder: "F"}}},
		{Filters: []config.FilterRule{{Name: "x", Conditions: []config.FilterCondition{{Field: "body", Op: "is", Value: "x"}}, Folder: "F"}}},
		{Filters: []config.FilterRule{{Name: "x", Conditions: []config.FilterCondition{{Field: "from", Op: "is", Value: "x"}}}}},
		{Filters: []config.FilterRule{{Name: "x", Conditions: []config.FilterCondition{{Field: "from", Op: "is", Value: "x"}}, Folder: "F", Delete: true}}},
		{Filters: []config.FilterRule{{Name: "x", Conditions: []config.FilterCondition{{Field: "from", Op: "is", Value: "x"}}, Folder: "a\"b"}}},
	}
	for i, s := range bad {
		if err := Check(&s); err == nil {
			t.Errorf("%d accepted", i)
		}
	}
	s := Settings{OutOfOffice: config.OutOfOffice{Enabled: true, Text: "x"}}
	if err := Check(&s); err != nil || s.OutOfOffice.Days != config.DefaultOutOfOfficeDays {
		t.Fatalf("defaults: %v %+v", err, s)
	}
	if !(Settings{}).Empty() || s.Empty() {
		t.Fatal("Empty")
	}
	now := time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	s.OutOfOffice.Start, s.OutOfOffice.End = "2026-12-01", "2026-12-01"
	if s.OutOfOfficeOn(now) {
		t.Fatal("after the end")
	}
	s.OutOfOffice.Zone = "-0100" // still the 1st there
	if !s.OutOfOfficeOn(now) {
		t.Fatal("zone")
	}
}
