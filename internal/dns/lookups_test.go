package dns

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func mailNS(t *testing.T) *Authoritative {
	t.Helper()
	long := strings.Repeat("k", 300)
	ns := newFakeNS(t, map[string]fakeRecord{
		"example.org":                {txt: [][]string{{"v=spf1 mx -all"}, {"google-site-verification=abc"}}},
		"op1._domainkey.example.org": {txt: [][]string{{"v=DKIM1; k=rsa; p=" + long[:200], long[200:]}}},
		"_submissions._tcp.example.org": {srv: []net.SRV{
			{Target: "backup.example.org", Port: 465, Priority: 10, Weight: 1},
			{Target: "mail.example.org", Port: 465, Priority: 0, Weight: 1},
		}},
		"www.example.org": {a: []string{"192.0.2.1"}},
	})
	sys := &fakeSystem{
		ns:    map[string][]string{"example.org": {"ns1.example.org"}},
		addrs: map[string][]netip.Addr{"ns1.example.org": {netip.MustParseAddr("198.51.100.1")}},
	}
	return newAuth(sys, map[string]string{"198.51.100.1:53": ns.addr()})
}

func TestAuthoritativeTXT(t *testing.T) {
	a := mailNS(t)
	ctx := context.Background()
	txt, err := a.LookupTXT(ctx, "Example.org.")
	if err != nil || len(txt) != 2 || txt[0] != "v=spf1 mx -all" || txt[1] != "google-site-verification=abc" {
		t.Fatalf("%q %v", txt, err)
	}
	txt, err = a.LookupTXT(ctx, "op1._domainkey.example.org")
	if err != nil || len(txt) != 1 || txt[0] != "v=DKIM1; k=rsa; p="+strings.Repeat("k", 300) {
		t.Fatalf("split strings are joined: %q %v", txt, err)
	}
	for _, name := range []string{"www.example.org", "_dmarc.example.org"} {
		if _, err := a.LookupTXT(ctx, name); !isNotFound(err) {
			t.Fatalf("%s: want not found, got %v", name, err)
		}
	}
}

func TestAuthoritativeSRV(t *testing.T) {
	a := mailNS(t)
	ctx := context.Background()
	cname, srv, err := a.LookupSRV(ctx, "submissions", "tcp", "example.org")
	if err != nil || cname != "_submissions._tcp.example.org." || len(srv) != 2 {
		t.Fatalf("%q %v %v", cname, srv, err)
	}
	if srv[0].Target != "mail.example.org." || srv[0].Port != 465 || srv[1].Priority != 10 {
		t.Fatalf("not sorted by priority: %+v %+v", srv[0], srv[1])
	}
	if _, _, err := a.LookupSRV(ctx, "imaps", "tcp", "example.org"); !isNotFound(err) {
		t.Fatalf("want not found, got %v", err)
	}
	if _, srv, err := a.LookupSRV(ctx, "", "", "_submissions._tcp.example.org"); err != nil || len(srv) != 2 {
		t.Fatalf("plain name: %v %v", srv, err)
	}
}

type txtSystem struct {
	*fakeSystem
}

func (txtSystem) LookupTXT(context.Context, string) ([]string, error) {
	return []string{"from system"}, nil
}
func (txtSystem) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "x.", []*net.SRV{{Target: "sys.example.org.", Port: 1}}, nil
}
func (txtSystem) LookupAddr(context.Context, string) ([]string, error) {
	return []string{"mail.example.org."}, nil
}

func TestAuthoritativeTXTSRVFallBackToSystem(t *testing.T) {
	sys := txtSystem{&fakeSystem{
		ns:    map[string][]string{"example.org": {"ns1.example.org"}},
		addrs: map[string][]netip.Addr{"ns1.example.org": {netip.MustParseAddr("198.51.100.1")}},
	}}
	a := &Authoritative{System: sys, Dial: route(map[string]string{"198.51.100.1:53": deadAddr(t)}), Timeout: 200 * time.Millisecond}
	ctx := context.Background()
	if txt, err := a.LookupTXT(ctx, "example.org"); err != nil || len(txt) != 1 || txt[0] != "from system" {
		t.Fatalf("TXT %v %v", txt, err)
	}
	if _, srv, err := a.LookupSRV(ctx, "imaps", "tcp", "example.org"); err != nil || len(srv) != 1 || srv[0].Target != "sys.example.org." {
		t.Fatalf("SRV %v %v", srv, err)
	}
	if names, err := a.LookupAddr(ctx, "192.0.2.1"); err != nil || names[0] != "mail.example.org." {
		t.Fatalf("PTR %v %v", names, err)
	}
	// Without a system resolver that can do reverse lookups, they fail.
	if _, err := newAuth(&fakeSystem{}, nil).LookupAddr(ctx, "192.0.2.1"); err == nil {
		t.Fatal("reverse lookup without a resolver")
	}
}
