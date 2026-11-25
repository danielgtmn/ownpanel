package directory

import (
	"slices"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
)

func testConfig() *config.Config {
	return &config.Config{
		Hostname: "mail.example.net",
		Domains:  []config.Domain{{Name: "a.test"}, {Name: "b.test"}, {Name: "c.test"}},
		Mailboxes: []config.Mailbox{
			{StorageID: mailtest.ID('1'), Address: "first@a.test", PasswordHash: mailtest.Hash},
			{StorageID: mailtest.ID('2'), Address: "second@a.test", PasswordHash: mailtest.Hash},
			{StorageID: mailtest.ID('3'), Address: "only@b.test", PasswordHash: mailtest.Hash},
		},
		Aliases: []config.Alias{
			{Address: "postmaster@a.test", Targets: []string{"second@a.test"}},
			{Address: "team@a.test", Targets: []string{"first@a.test", "second@a.test", "loop@a.test", "x@elsewhere.test", "FIRST@a.test"}},
			{Address: "loop@a.test", Targets: []string{"team@a.test", "first@a.test"}},
			{Address: "out@b.test", Targets: []string{"someone@elsewhere.test"}},
			{Address: "*@b.test", Targets: []string{"only@b.test"}},
			{Address: "dead@b.test", Targets: []string{"nobody@c.test"}},
		},
		PostmasterAddress: "first@a.test",
	}
}

func ids(r Route) []string {
	var out []string
	for _, m := range r.Mailboxes {
		out = append(out, m.Address)
	}
	return out
}

func TestResolve(t *testing.T) {
	d := New(testConfig())
	cases := []struct {
		addr string
		out  Outcome
		to   []string
	}{
		{"first@a.test", Deliver, []string{"first@a.test"}},
		{"postmaster@a.test", Deliver, []string{"second@a.test"}}, // alias of that name
		{"abuse@a.test", Deliver, []string{"first@a.test"}},       // oldest mailbox
		{"postmaster@b.test", Deliver, []string{"only@b.test"}},   // before the catch-all
		{"abuse@c.test", Deliver, []string{"first@a.test"}},       // no mailbox: postmaster address
		{"postmaster@mail.example.net", Deliver, []string{"first@a.test"}},
		{"team@a.test", Deliver, []string{"first@a.test", "second@a.test"}},
		{"anything@b.test", Deliver, []string{"only@b.test"}}, // catch-all
		{"out@b.test", ForwardOnly, nil},
		{"dead@b.test", Unknown, nil},
		{"nobody@a.test", Unknown, nil},
		{"user@other.test", Unknown, nil},
		{"postmaster@other.test", Unknown, nil},
		{"broken", Unknown, nil},
	}
	for _, c := range cases {
		r, out := d.Resolve(c.addr)
		if out != c.out || !slices.Equal(ids(r), c.to) {
			t.Errorf("%s: %v %v, want %v %v", c.addr, out, ids(r), c.out, c.to)
		}
	}
	r, _ := d.Resolve("team@a.test")
	if !slices.Equal(r.External, []string{"x@elsewhere.test"}) {
		t.Errorf("external %v", r.External)
	}
}

func TestPostmasterMissing(t *testing.T) {
	cfg := testConfig()
	cfg.PostmasterAddress = ""
	d := New(cfg)
	if _, out := d.Resolve("postmaster@c.test"); out != NoPostmaster {
		t.Fatalf("got %v", out)
	}
	if _, out := d.Resolve("abuse@mail.example.net"); out != NoPostmaster {
		t.Fatalf("got %v", out)
	}
	// An address of ours that does not exist leads nowhere.
	cfg.PostmasterAddress = "nobody@a.test"
	d = New(cfg)
	if _, out := d.Resolve("abuse@mail.example.net"); out != NoPostmaster {
		t.Fatalf("unknown postmaster: %v", out)
	}
}

// TestPostmasterOutside: an outside postmaster address is forwarded to,
// like an alias pointing there.
func TestPostmasterOutside(t *testing.T) {
	cfg := testConfig()
	cfg.PostmasterAddress = "admin@elsewhere.test"
	d := New(cfg)
	for _, addr := range []string{"abuse@mail.example.net", "postmaster@mail.example.net", "postmaster@c.test"} {
		r, out := d.Resolve(addr)
		if out != ForwardOnly || len(r.Mailboxes) != 0 || !slices.Equal(r.External, []string{"admin@elsewhere.test"}) {
			t.Errorf("%s: %v %+v", addr, out, r)
		}
	}
}

func TestDeepAlias(t *testing.T) {
	cfg := &config.Config{Hostname: "mail.example.net", Domains: []config.Domain{{Name: "a.test"}},
		Mailboxes: []config.Mailbox{{StorageID: mailtest.ID('1'), Address: "end@a.test", PasswordHash: mailtest.Hash}}}
	prev := "end@a.test"
	for i := 0; i < 12; i++ {
		name := string(rune('a'+i)) + "@a.test"
		cfg.Aliases = append(cfg.Aliases, config.Alias{Address: name, Targets: []string{prev}})
		prev = name
	}
	d := New(cfg)
	if _, out := d.Resolve("i@a.test"); out != Deliver { // 9 aliases deep
		t.Fatalf("depth 9: %v", out)
	}
	if r, out := d.Resolve("l@a.test"); out != Unknown || len(r.Dropped) == 0 {
		t.Fatalf("depth 12: %v %+v", out, r)
	}
}
