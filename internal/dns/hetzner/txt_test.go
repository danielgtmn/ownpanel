package hetzner

import (
	"strings"
	"testing"
)

func TestQuoteTXT(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", `""`},
		{"v=spf1 -all", `"v=spf1 -all"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
	}
	for _, tc := range tests {
		if got := quoteTXT(tc.in); got != tc.want {
			t.Errorf("quoteTXT(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestTXTChunking(t *testing.T) {
	for _, n := range []int{1, 254, 255, 256, 510, 511, 600} {
		in := strings.Repeat("a", n)
		q := quoteTXT(in)
		parts := strings.Split(q, `" "`)
		wantParts := (n + 254) / 255
		if len(parts) != wantParts {
			t.Errorf("len %d: %d parts, want %d", n, len(parts), wantParts)
		}
		for i, p := range parts {
			p = strings.Trim(p, `"`)
			if len(p) > 255 {
				t.Errorf("len %d: part %d has %d bytes", n, i, len(p))
			}
		}
		back, err := unquoteTXT(q)
		if err != nil || back != in {
			t.Errorf("len %d: round trip failed (err %v)", n, err)
		}
	}
	// 255 stays one string, 256 becomes two.
	if strings.Contains(quoteTXT(strings.Repeat("a", 255)), `" "`) {
		t.Error("255 bytes must not be split")
	}
	if !strings.Contains(quoteTXT(strings.Repeat("a", 256)), `" "`) {
		t.Error("256 bytes must be split")
	}
}

func TestTXTChunkingKeepsRunesIntact(t *testing.T) {
	in := strings.Repeat("a", 254) + "äöü" + strings.Repeat("é", 200)
	q := quoteTXT(in)
	for _, p := range strings.Split(q, `" "`) {
		if len(strings.Trim(p, `"`)) > 255 {
			t.Fatalf("part too long: %d", len(p))
		}
	}
	back, err := unquoteTXT(q)
	if err != nil || back != in {
		t.Fatalf("round trip: %v", err)
	}
	if strings.Contains(q, "�") {
		t.Error("invalid UTF-8 produced")
	}
}

func TestTXTRoundTripEscapes(t *testing.T) {
	long := strings.Repeat(`x"y\`, 100) // 400 bytes, many escapes, chunked
	for _, in := range []string{"", `a"b`, `a\b`, `\"`, `""`, long, "tab\there"} {
		back, err := unquoteTXT(quoteTXT(in))
		if err != nil || back != in {
			t.Errorf("round trip %q: got %q, %v", in, back, err)
		}
	}
}

func TestUnquoteTXT(t *testing.T) {
	good := []struct{ in, want string }{
		{`"abc"`, "abc"},
		{`"abc" "def"`, "abcdef"},
		{`  "abc"   "def"  `, "abcdef"},
		{`"a\"b"`, `a"b`},
		{`"a\\b"`, `a\b`},
		{`"a\065b"`, "aAb"},
		{`""`, ""},
		{"unquoted value", "unquoted value"},
	}
	for _, tc := range good {
		got, err := unquoteTXT(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("unquoteTXT(%s) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{`"abc`, `"abc\`, `"abc" junk`, `"a\9"`, `"a\999"`} {
		if _, err := unquoteTXT(in); err == nil {
			t.Errorf("unquoteTXT(%s) should fail", in)
		}
	}
}
