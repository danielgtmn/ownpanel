package sieve

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type msg struct {
	h    map[string][]string
	size int64
	body []string
}

func (m *msg) Header(name string) []string { return m.h[name] }
func (m *msg) Size() int64                 { return m.size }
func (m *msg) Body(transform string, types []string) []string {
	if transform == "raw" {
		return []string{strings.Join(m.body, "\n")}
	}
	return m.body
}

func testMsg() *msg {
	return &msg{
		h: map[string][]string{
			"from":       {`"Alice Example" <alice@example.org>`},
			"to":         {"Bob <bob@example.com>, carol@example.com"},
			"cc":         {"team@lists.example.net"},
			"subject":    {"=?utf-8?q?Gr=C3=BC=C3=9Fe?= from the Project"},
			"date":       {"Tue, 24 Nov 2026 10:30:00 +0100"},
			"message-id": {"<abc@example.org>"},
			"list-id":    {"Team list <team.lists.example.net>"},
			"x-spam":     {"yes", "no"},
		},
		size: 2048,
		body: []string{"Hello Bob,\r\nthe invoice number 4711 is attached.\r\n"},
	}
}

func run(t *testing.T, src string, env *Env) *Result {
	t.Helper()
	s, err := Compile(src)
	if err != nil {
		t.Fatalf("compile: %v\n%s", err, src)
	}
	if env == nil {
		env = &Env{}
	}
	if env.Message == nil {
		env.Message = testMsg()
	}
	res, err := s.Run(context.Background(), env)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, src)
	}
	return res
}

// folders lists the fileinto targets of a result.
func folders(r *Result) []string {
	var out []string
	for _, a := range r.Actions {
		if a.Kind == ActFileinto {
			out = append(out, a.Folder)
		}
	}
	return out
}

// matches runs "if <test> { fileinto "yes"; }" and tells whether it filed.
func matches(t *testing.T, requires, test string, env *Env) bool {
	t.Helper()
	src := `require ["fileinto"` + requires + `];` + "\nif " + test + ` { fileinto "yes"; }`
	return len(folders(run(t, src, env))) == 1
}

func TestImplicitKeepAndBasicActions(t *testing.T) {
	r := run(t, ``, nil)
	if !r.ImplicitKeep || len(r.Actions) != 0 {
		t.Fatalf("empty script: %+v", r)
	}
	r = run(t, `discard;`, nil)
	if r.ImplicitKeep || r.Actions[0].Kind != ActDiscard {
		t.Fatalf("discard: %+v", r)
	}
	r = run(t, `require "fileinto"; fileinto "Work"; keep;`, nil)
	if r.ImplicitKeep || len(r.Actions) != 2 || r.Actions[0].Folder != "Work" || r.Actions[1].Kind != ActKeep {
		t.Fatalf("fileinto+keep: %+v", r)
	}
	r = run(t, `require ["fileinto", "copy"]; fileinto :copy "Work";`, nil)
	if !r.ImplicitKeep || !r.Actions[0].Copy {
		t.Fatalf("fileinto :copy keeps: %+v", r)
	}
	r = run(t, `redirect "x@example.net"; stop; discard;`, nil)
	if r.ImplicitKeep || len(r.Actions) != 1 || r.Actions[0].Address != "x@example.net" {
		t.Fatalf("redirect+stop: %+v", r)
	}
	r = run(t, `require "copy"; redirect :copy "x@example.net";`, nil)
	if !r.ImplicitKeep {
		t.Fatalf("redirect :copy keeps: %+v", r)
	}
	r = run(t, `require ["fileinto", "mailbox"]; fileinto :create "New/Sub";`, nil)
	if !r.Actions[0].Create {
		t.Fatalf(":create: %+v", r)
	}
}

func TestIfElsifElse(t *testing.T) {
	src := `require "fileinto";
if header :contains "subject" "nothing" { fileinto "a"; }
elsif header :contains "subject" "project" { fileinto "b"; }
else { fileinto "c"; }`
	if got := folders(run(t, src, nil)); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("got %v", got)
	}
}

func TestHeaderMatching(t *testing.T) {
	cases := []struct {
		req, test string
		want      bool
	}{
		{"", `header :contains "subject" "PROJECT"`, true}, // casemap default
		{"", `header :comparator "i;octet" :contains "subject" "PROJECT"`, false},
		{"", `header :is "subject" "Grüße from the Project"`, true}, // decoded
		{"", `header :matches "subject" "Gr*e from *"`, true},
		{"", `header :matches "subject" "?r??e*"`, true},
		{"", `header :is "x-spam" "no"`, true}, // any value
		{"", `header :contains ["x-none", "from"] "alice"`, true},
		{"", `exists ["from", "date"]`, true},
		{"", `exists ["from", "x-none"]`, false},
		{"", `not exists "x-none"`, true},
		{"", `size :over 1K`, true},
		{"", `size :under 1K`, false},
		{"", `allof (true, header :contains "from" "alice")`, true},
		{"", `anyof (false, header :contains "from" "nobody")`, false},
		{"", `address :domain :is "from" "EXAMPLE.org"`, true},
		{"", `address :localpart :is "to" "carol"`, true},
		{"", `address :all :is "to" "bob@example.com"`, true},
		{"", `address :is "to" "Bob"`, false},
		{`, "envelope"`, `envelope :is "to" "alias@example.com"`, true},
		{`, "envelope"`, `envelope :domain :is "from" "example.org"`, true},
		{`, "body"`, `body :contains "4711"`, true},
		{`, "body"`, `body :raw :contains "invoice"`, true},
		{`, "body"`, `body :content "text" :contains "nothing"`, false},
		{`, "regex"`, `header :regex "subject" "^Gr.*Project$"`, true},
		{`, "regex"`, `header :regex "message-id" "^<[0-9]+@"`, false},
		{`, "relational", "comparator-i;ascii-numeric"`, `header :count "ge" :comparator "i;ascii-numeric" "x-spam" "2"`, true},
		{`, "relational", "comparator-i;ascii-numeric"`, `header :count "eq" :comparator "i;ascii-numeric" ["to", "cc"] "2"`, true},
		{`, "relational"`, `header :count "eq" "x-spam" "2"`, true},
		{`, "relational", "comparator-i;ascii-numeric"`, `header :value "gt" :comparator "i;ascii-numeric" "x-spam" "0"`, true}, // not numbers: infinity,
		{`, "relational"`, `header :value "lt" "subject" "H"`, true},
	}
	for _, c := range cases {
		env := &Env{EnvelopeTo: "alias@example.com", EnvelopeFrom: "alice@example.org"}
		if got := matches(t, c.req, c.test, env); got != c.want {
			t.Errorf("%s: got %v, want %v", c.test, got, c.want)
		}
	}
}

func TestNumericComparator(t *testing.T) {
	m := testMsg()
	m.h["x-n"] = []string{"10", "abc"}
	env := &Env{Message: m}
	if !matches(t, `, "relational", "comparator-i;ascii-numeric"`, `header :value "gt" :comparator "i;ascii-numeric" "x-n" "9"`, env) {
		t.Fatal("10 > 9 numerically")
	}
	// "abc" is positive infinity.
	if !matches(t, `, "relational", "comparator-i;ascii-numeric"`, `header :value "gt" :comparator "i;ascii-numeric" "x-n" "99999"`, env) {
		t.Fatal("abc is infinity")
	}
	if _, err := Compile(`if header :comparator "i;ascii-numeric" :is "x" "1" { stop; }`); err == nil {
		t.Fatal("numeric comparator needs require")
	}
	if _, err := Compile(`require "comparator-i;ascii-numeric"; if header :comparator "i;ascii-numeric" :contains "x" "1" { stop; }`); err == nil {
		t.Fatal("numeric comparator cannot contain")
	}
}

func TestVariables(t *testing.T) {
	src := `require ["variables", "fileinto"];
if header :matches "from" "* <*@*>" { set "who" "${2}"; set :upperfirst "dom" "${3}"; }
set :length "len" "${who}";
fileinto "From/${who}-${dom}-${len}";
set :lower :upperfirst "x" "HELLO";
fileinto "${x}";
set "q" "a*b?";
set :quotewildcard "q2" "${q}";
if string :matches "a*b?" "${q2}" { fileinto "quoted"; }
if string :is "${unset}" "" { fileinto "empty"; }
fileinto "${nope.ns}";`
	got := folders(run(t, src, nil))
	want := []string{"From/alice-Example.org-5", "Hello", "quoted", "empty", "${nope.ns}"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Without require, ${…} stays.
	got = folders(run(t, `require "fileinto"; fileinto "${x}";`, nil))
	if got[0] != "${x}" {
		t.Fatalf("no variables: %q", got)
	}
	if _, err := Compile(`set "a" "b";`); err == nil {
		t.Fatal("set needs require")
	}
}

func TestRegexCaptures(t *testing.T) {
	src := `require ["variables", "fileinto", "regex"];
if header :regex "subject" "from the (\\w+)" { fileinto "${1}"; }`
	if got := folders(run(t, src, nil)); !slices.Equal(got, []string{"Project"}) {
		t.Fatalf("got %q", got)
	}
	if _, err := Compile(`require "regex"; if header :regex "s" "(" { stop; }`); err == nil {
		t.Fatal("bad regex refused at compile")
	}
	long := strings.Repeat("a", MaxRegexBytes+1)
	if _, err := Compile(`require "regex"; if header :regex "s" "` + long + `" { stop; }`); err == nil {
		t.Fatal("long regex refused")
	}
}

func TestImap4Flags(t *testing.T) {
	src := `require ["imap4flags", "fileinto", "variables"];
setflag "\\Seen";
addflag ["\\Flagged", "$Work"];
removeflag "$work";
if hasflag :is "\\flagged" { fileinto "flagged"; }
addflag "myvar" "a b";
if hasflag "myvar" "b" { fileinto "var"; }
fileinto :flags "\\Answered" "explicit";`
	r := run(t, src, nil)
	if got := folders(r); !slices.Equal(got, []string{"flagged", "var", "explicit"}) {
		t.Fatalf("folders %v", got)
	}
	if !slices.Equal(r.Actions[0].Flags, []string{`\Seen`, `\Flagged`}) || !r.Actions[0].HasFlags {
		t.Fatalf("flags of first fileinto: %v", r.Actions[0].Flags)
	}
	if !slices.Equal(r.Actions[2].Flags, []string{`\Answered`}) {
		t.Fatalf("explicit flags: %v", r.Actions[2].Flags)
	}
	if !r.HasFlags || !slices.Equal(r.KeepFlags, []string{`\Seen`, `\Flagged`}) {
		t.Fatalf("keep flags: %v", r.KeepFlags)
	}
}

func TestDate(t *testing.T) {
	now := time.Date(2026, 12, 24, 23, 30, 0, 0, time.UTC)
	env := func() *Env { return &Env{Now: now} }
	req := `, "date", "relational"`
	cases := []struct {
		test string
		want bool
	}{
		{`currentdate :value "ge" "date" "2026-12-01"`, true},
		{`currentdate :value "le" "date" "2026-12-23"`, false},
		{`currentdate :zone "+0100" :is "date" "2026-12-25"`, true},
		{`currentdate :is "weekday" "4"`, true},
		{`currentdate :is "julian" "61398"`, true},
		{`currentdate :is "time" "23:30:00"`, true},
		{`currentdate :zone "-0500" :is "hour" "18"`, true},
		{`date :is "date" "date" "2026-11-24"`, true},
		{`date :originalzone :is "hour" ["10"]`, false},
		{`date :originalzone :is "date" "hour" "10"`, true},
		{`date :zone "+0000" :is "date" "hour" "09"`, true},
		{`date :is "x-none" "year" "2026"`, false},
		{`currentdate :is "zone" "+0000"`, true},
		{`currentdate :is "iso8601" "2026-12-24T23:30:00+00:00"`, true},
	}
	for _, c := range cases {
		if strings.Contains(c.test, `:is "hour" ["10"]`) {
			if _, err := Compile(`require "date"; if ` + c.test + ` { stop; }`); err == nil {
				t.Errorf("%s should not compile", c.test)
			}
			continue
		}
		if got := matches(t, req, c.test, env()); got != c.want {
			t.Errorf("%s: got %v, want %v", c.test, got, c.want)
		}
	}
	if _, err := Compile(`require "date"; if currentdate :zone "CET" :is "year" "2026" { stop; }`); err == nil {
		t.Fatal("bad zone refused")
	}
	if _, err := Compile(`require "date"; if currentdate :is "fortnight" "1" { stop; }`); err == nil {
		t.Fatal("bad date part refused")
	}
}

func TestVacation(t *testing.T) {
	r := run(t, `require "vacation"; vacation :days 3 :subject "Away" :from "me@example.com" :addresses ["alt@example.com"] "I am away.";`, nil)
	if !r.ImplicitKeep || len(r.Actions) != 1 {
		t.Fatalf("vacation keeps: %+v", r)
	}
	v := r.Actions[0].Vacation
	if v.Period != 3*24*time.Hour || v.Subject != "Away" || v.From != "me@example.com" || v.Reason != "I am away." ||
		!slices.Equal(v.Addresses, []string{"alt@example.com"}) || v.Handle == "" {
		t.Fatalf("vacation: %+v", v)
	}
	// Days below the minimum are raised, seconds need the extension.
	r = run(t, `require "vacation-seconds"; vacation :seconds 60 "x";`, nil)
	if r.Actions[0].Vacation.Period != MinVacationPeriod {
		t.Fatalf("period %v", r.Actions[0].Vacation.Period)
	}
	r = run(t, `require "vacation"; vacation :days 1000 "x";`, nil)
	if r.Actions[0].Vacation.Period != MaxVacationPeriod {
		t.Fatalf("period %v", r.Actions[0].Vacation.Period)
	}
	if _, err := Compile(`require "vacation"; vacation :seconds 60 "x";`); err == nil {
		t.Fatal(":seconds needs vacation-seconds")
	}
	if _, err := Compile(`require "vacation-seconds"; vacation :days 1 :seconds 60 "x";`); err == nil {
		t.Fatal(":days and :seconds")
	}
	// Same text, same handle; a second vacation is an error.
	a := run(t, `require "vacation"; vacation "same";`, nil).Actions[0].Vacation.Handle
	b := run(t, `require "vacation"; vacation "same";`, nil).Actions[0].Vacation.Handle
	if a != b {
		t.Fatal("handle not stable")
	}
	s, _ := Compile(`require "vacation"; vacation "a"; vacation "b";`)
	if _, err := s.Run(context.Background(), &Env{Message: testMsg()}); err == nil {
		t.Fatal("two vacations")
	}
	// Multi-line text with dot-stuffing.
	r = run(t, "require \"vacation\";\nvacation text:\nline one\n..dot\n.\n;", nil)
	if r.Actions[0].Vacation.Reason != "line one\r\n.dot\r\n" {
		t.Fatalf("multi-line: %q", r.Actions[0].Vacation.Reason)
	}
}

func TestInclude(t *testing.T) {
	scripts := map[string]string{
		"lib":    `require ["fileinto", "include", "variables"]; global "who"; fileinto "lib-${who}"; return; fileinto "never";`,
		"loop":   `require "include"; include "loop";`,
		"broken": `fileinto "x";`, // missing require: compile error
	}
	env := &Env{Include: func(_ context.Context, name string) (*Script, bool, error) {
		src, ok := scripts[name]
		if !ok {
			return nil, false, nil
		}
		s, err := Compile(src)
		return s, err == nil, err
	}}
	src := `require ["include", "variables", "fileinto"];
global "who";
set "who" "me";
include :personal "lib";
include :once "lib";
include :optional "missing";
fileinto "main";`
	// :once skips a script that was included before.
	if got := folders(run(t, src, env)); !slices.Equal(got, []string{"lib-me", "main"}) {
		t.Fatalf("got %q", got)
	}
	src = `require ["include", "fileinto"]; include :once "lib"; include :once "lib";`
	scripts["lib"] = `require "fileinto"; fileinto "lib";`
	if got := folders(run(t, src, env)); len(got) != 1 {
		t.Fatalf(":once: %q", got)
	}
	for _, bad := range []string{`include "loop";`, `include "missing";`, `include "broken";`} {
		s, err := Compile(`require "include"; ` + bad)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Run(context.Background(), &Env{Message: testMsg(), Include: env.Include}); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	if _, err := Compile(`require "include"; include :global "x";`); err == nil {
		t.Fatal(":global refused")
	}
}

func TestDuplicate(t *testing.T) {
	seen := map[string]bool{}
	env := &Env{Duplicate: func(id string) bool { return seen[id] }}
	// The run only looks; the caller records the ids it reports once the
	// message is delivered (RFC 7352), so a failed delivery never makes
	// the retry a "duplicate".
	commit := func(r *Result) {
		for _, d := range r.Duplicates {
			if d.Period != time.Hour || d.Last {
				t.Errorf("mark %+v", d)
			}
			seen[d.ID] = true
		}
	}
	src := `require ["duplicate", "fileinto"]; if duplicate :seconds 3600 { fileinto "dup"; }`
	r := run(t, src, env)
	if len(folders(r)) != 0 || len(r.Duplicates) != 1 {
		t.Fatalf("first is no duplicate: %+v", r)
	}
	if len(seen) != 0 {
		t.Fatal("recorded during the run")
	}
	if got := folders(run(t, src, env)); len(got) != 0 {
		t.Fatal("not delivered yet: no duplicate")
	}
	commit(r)
	if got := folders(run(t, src, env)); len(got) != 1 {
		t.Fatal("second is a duplicate")
	}
	// Other handle, other id.
	src = `require ["duplicate", "fileinto"]; if duplicate :handle "x" :seconds 3600 :uniqueid "1" { fileinto "dup"; }`
	if got := folders(run(t, src, env)); len(got) != 0 {
		t.Fatal("other handle")
	}
}

func TestSpamtestAndMailbox(t *testing.T) {
	env := &Env{SpamTest: "8", SpamPercent: "77", FolderExists: func(n string) bool { return n == "Work" }}
	req := `, "spamtest", "relational", "comparator-i;ascii-numeric"`
	if !matches(t, req, `spamtest :value "ge" :comparator "i;ascii-numeric" "7"`, env) {
		t.Fatal("spamtest ge 7")
	}
	if !matches(t, `, "spamtestplus", "relational", "comparator-i;ascii-numeric"`, `spamtest :percent :value "gt" :comparator "i;ascii-numeric" "50"`, env) {
		t.Fatal("spamtest percent")
	}
	if !matches(t, `, "mailbox"`, `mailboxexists "Work"`, env) || matches(t, `, "mailbox"`, `mailboxexists ["Work", "Other"]`, env) {
		t.Fatal("mailboxexists")
	}
}

func TestCompileErrors(t *testing.T) {
	bad := []string{
		`reject "no";`,
		`require "reject";`,
		`require "ereject";`,
		`require "nonsense";`,
		`fileinto "x";`,
		`keep; require "fileinto";`,
		`if true { stop; } else stop;`,
		`elsif true { stop; }`,
		`if true stop;`,
		`stop`,
		`"string";`,
		`if header :is :contains "a" "b" { stop; }`,
		`if header :foo "a" "b" { stop; }`,
		`if header "a" { stop; }`,
		`if size 10 { stop; }`,
		`if size :over :under 10 { stop; }`,
		`redirect "not an address";`,
		`redirect ["a@b.c", "d@e.f"];`,
		`if allof true { stop; }`,
		`if not (true, false) { stop; }`,
		`/* open comment`,
		`if header :is "a" "b { stop; }`,
		"vacation text:\nno end",
		`require "envelope"; if envelope :is "cc" "x" { stop; }`,
		`require "variables"; set "1x" "y";`,
		`require ["vacation"]; vacation :days "x" "y";`,
		`unknown;`,
		`if unknowntest { stop; }`,
		`discard { stop; }`,
		`require "include"; include "a/b";`,
		strings.Repeat("if true {", maxNesting+1) + strings.Repeat("}", maxNesting+1),
		"\xff",
	}
	for _, src := range bad {
		if _, err := Compile(src); err == nil {
			t.Errorf("compiled: %q", src)
		} else {
			var se *Error
			if !errors.As(err, &se) {
				t.Errorf("%q: error type %T", src, err)
			}
		}
	}
	if _, err := Compile(strings.Repeat("#", MaxScriptBytes+1)); err == nil {
		t.Fatal("too large")
	}
	_, err := Compile("require \"fileinto\";\n\nfileinto;")
	var se *Error
	if !errors.As(err, &se) || se.Line != 3 {
		t.Fatalf("line of error: %v", err)
	}
}

func TestLimits(t *testing.T) {
	// Many tests over a large value run out of steps.
	m := testMsg()
	m.h["x-big"] = []string{strings.Repeat("a", 64<<10)}
	src := `require "fileinto";` + strings.Repeat(`if header :contains "x-big" "b" { fileinto "x"; }`+"\n", 1000)
	s, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Run(context.Background(), &Env{Message: m, MaxSteps: 2000})
	if !errors.Is(err, ErrSteps) {
		t.Fatalf("steps: %v", err)
	}
	// An expired context is a timeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Run(ctx, &Env{Message: m}); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	// Too many actions.
	s, _ = Compile(`require ["fileinto", "copy"];` + strings.Repeat(`fileinto :copy "x";`, maxActions+1))
	if _, err := s.Run(context.Background(), &Env{Message: m}); err == nil {
		t.Fatal("too many actions")
	}
	// A pathological wildcard ends without hanging.
	m.h["x-a"] = []string{strings.Repeat("a", 5000)}
	start := time.Now()
	matches(t, "", `header :matches "x-a" "*a*a*a*a*a*a*a*a*b"`, &Env{Message: m})
	if time.Since(start) > 5*time.Second {
		t.Fatal("wildcard too slow")
	}
}

func TestWildcard(t *testing.T) {
	cases := []struct {
		v, p string
		ok   bool
		caps []string
	}{
		{"hello world", "hello *", true, []string{"hello world", "world"}},
		{"a-b-c", "*-*", true, []string{"a-b-c", "a", "b-c"}},
		{"abc", "a?c", true, []string{"abc", "b"}},
		{"a*c", `a\*c`, true, []string{"a*c"}},
		{"abc", `a\*c`, false, nil},
		{"äbc", "?bc", true, []string{"äbc", "ä"}},
		{"", "*", true, []string{"", ""}},
		{"x", "", false, nil},
	}
	for _, c := range cases {
		caps, ok := wildcard(c.v, c.p, false)
		if ok != c.ok || ok && !slices.Equal(caps, c.caps) {
			t.Errorf("%q ~ %q: %v %q", c.v, c.p, ok, caps)
		}
	}
	if caps, ok := wildcard("Hello World", "hello *", true); !ok || caps[1] != "World" {
		t.Errorf("fold: %v %q", ok, caps)
	}
}

func TestScriptNames(t *testing.T) {
	for _, n := range []string{"ownpanel", "My filters", "Ümlaut"} {
		if !ValidScriptName(n) {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range []string{"", "a/b", "a\nb", strings.Repeat("x", 129)} {
		if ValidScriptName(n) {
			t.Errorf("%q accepted", n)
		}
	}
}

func FuzzCompile(f *testing.F) {
	seeds := []string{
		``, `keep;`, `require ["fileinto", "variables"]; if header :matches "from" "*@*" { fileinto "${2}"; }`,
		"vacation text:\nx\n.\n;", `if allof (true, not false) { stop; } elsif anyof(false) {} else {}`,
		`require "regex"; if header :regex "a" "b+" { discard; }`, `/* c */ # c` + "\n", `"\"`,
		`require ["date","relational"]; if currentdate :zone "+0100" :value "ge" "date" "2026-01-01" { keep; }`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		s, err := Compile(src)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		s.Run(ctx, &Env{Message: testMsg(), MaxSteps: 10_000})
	})
}

// Regular expressions cannot hold up delivery: large programs and counted
// repetitions are refused, values are cut to maxPatternInput, and the
// steps charged grow with value × program size.
func TestRegexCost(t *testing.T) {
	for _, p := range []string{`(a|aa){1000}x`, `a{51}`, `(x+){60}`, strings.Repeat(`(a|b|c|d)`, 400)} {
		if _, err := Compile(`require "regex"; if header :regex "x" "` + p + `" { stop; }`); err == nil {
			t.Errorf("%.40s accepted", p)
		}
	}
	m := testMsg()
	m.h["x-big"] = []string{strings.Repeat("a", 1<<20)}
	src := `require ["regex", "fileinto"]; if header :regex "x-big" "(a|aa){50}x" { fileinto "x"; }`
	s, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = s.Run(context.Background(), &Env{Message: m})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	if !errors.Is(err, ErrSteps) {
		t.Fatalf("a large match must run out of steps, got %v", err)
	}
	// Ordinary expressions on ordinary values still work.
	if !matches(t, `, "regex"`, `header :regex "subject" "Project$"`, nil) {
		t.Fatal("simple regex")
	}
}

// An included script is fetched once per run, with the run's context.
func TestIncludeFetchedOncePerRun(t *testing.T) {
	calls := 0
	env := &Env{Include: func(ctx context.Context, name string) (*Script, bool, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Error("no run context")
		}
		s, err := Compile(`require "fileinto"; fileinto "lib";`)
		return s, true, err
	}}
	got := folders(run(t, `require ["include", "fileinto"]; include "lib"; include "lib"; include "lib";`, env))
	if len(got) != 3 || calls != 1 {
		t.Fatalf("folders %v, fetched %d times", got, calls)
	}
}
