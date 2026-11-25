package dns

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeNS is a tiny authoritative nameserver on a local UDP port.
type fakeNS struct {
	conn    *net.UDPConn
	mu      sync.Mutex
	records map[string]fakeRecord // by lowercase name without trailing dot
	queries int
	rd      bool // a query had the recursion-desired bit
}

type fakeRecord struct {
	a, aaaa []string
	cname   string
	mx      map[string]uint16 // host -> preference
	txt     [][]string        // records, each one or more strings
	srv     []net.SRV
}

func newFakeNS(t *testing.T, records map[string]fakeRecord) *fakeNS {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeNS{conn: conn, records: records}
	t.Cleanup(func() { conn.Close() })
	go s.serve()
	return s
}

func (s *fakeNS) addr() string { return s.conn.LocalAddr().String() }

func (s *fakeNS) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries
}

func (s *fakeNS) serve() {
	buf := make([]byte, 1500)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		var q dnsmessage.Message
		if q.Unpack(buf[:n]) != nil || len(q.Questions) != 1 {
			continue
		}
		s.mu.Lock()
		s.queries++
		s.rd = s.rd || q.RecursionDesired
		resp := s.answer(q)
		s.mu.Unlock()
		if out, err := resp.Pack(); err == nil {
			s.conn.WriteToUDP(out, from)
		}
	}
}

func (s *fakeNS) answer(q dnsmessage.Message) dnsmessage.Message {
	question := q.Questions[0]
	resp := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: q.ID, Response: true, Authoritative: true},
		Questions: q.Questions,
	}
	name := strings.TrimSuffix(strings.ToLower(question.Name.String()), ".")
	rec, ok := s.records[name]
	if !ok {
		resp.RCode = dnsmessage.RCodeNameError
		return resp
	}
	hdr := func(n string, t dnsmessage.Type) dnsmessage.ResourceHeader {
		return dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(n + "."), Type: t, Class: dnsmessage.ClassINET, TTL: 60}
	}
	if rec.cname != "" {
		resp.Answers = append(resp.Answers, dnsmessage.Resource{
			Header: hdr(name, dnsmessage.TypeCNAME),
			Body:   &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(rec.cname + ".")},
		})
		name, rec = rec.cname, s.records[rec.cname]
	}
	if question.Type == dnsmessage.TypeA {
		for _, v := range rec.a {
			resp.Answers = append(resp.Answers, dnsmessage.Resource{
				Header: hdr(name, dnsmessage.TypeA), Body: &dnsmessage.AResource{A: netip.MustParseAddr(v).As4()}})
		}
	} else if question.Type == dnsmessage.TypeMX {
		for h, pref := range rec.mx {
			resp.Answers = append(resp.Answers, dnsmessage.Resource{
				Header: hdr(name, dnsmessage.TypeMX), Body: &dnsmessage.MXResource{Pref: pref, MX: dnsmessage.MustNewName(strings.TrimSuffix(h, ".") + ".")}})
		}
	} else if question.Type == dnsmessage.TypeTXT {
		for _, v := range rec.txt {
			resp.Answers = append(resp.Answers, dnsmessage.Resource{
				Header: hdr(name, dnsmessage.TypeTXT), Body: &dnsmessage.TXTResource{TXT: v}})
		}
	} else if question.Type == dnsmessage.TypeSRV {
		for _, v := range rec.srv {
			resp.Answers = append(resp.Answers, dnsmessage.Resource{
				Header: hdr(name, dnsmessage.TypeSRV), Body: &dnsmessage.SRVResource{Priority: v.Priority, Weight: v.Weight, Port: v.Port,
					Target: dnsmessage.MustNewName(strings.TrimSuffix(v.Target, ".") + ".")}})
		}
	} else if question.Type == dnsmessage.TypeAAAA {
		for _, v := range rec.aaaa {
			resp.Answers = append(resp.Answers, dnsmessage.Resource{
				Header: hdr(name, dnsmessage.TypeAAAA), Body: &dnsmessage.AAAAResource{AAAA: netip.MustParseAddr(v).As16()}})
		}
	}
	return resp
}

// fakeSystem is the "normal" resolver: it knows delegations and addresses.
type fakeSystem struct {
	mu      sync.Mutex
	ns      map[string][]string     // name -> nameserver hosts
	addrs   map[string][]netip.Addr // host -> addresses
	nsCalls []string
	ipCalls []string
}

func (f *fakeSystem) LookupNS(_ context.Context, name string) ([]*net.NS, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nsCalls = append(f.nsCalls, name)
	hosts, ok := f.ns[name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	var out []*net.NS
	for _, h := range hosts {
		out = append(out, &net.NS{Host: h + "."})
	}
	return out, nil
}

func (f *fakeSystem) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ipCalls = append(f.ipCalls, host)
	if a, ok := f.addrs[host]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (f *fakeSystem) nsLookups() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.nsCalls...)
}

func (f *fakeSystem) ipLookups() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ipCalls...)
}

// deadAddr is a local UDP address nothing listens on (an immediate refusal).
func deadAddr(t *testing.T) string {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	a := c.LocalAddr().String()
	c.Close()
	return a
}

// route sends queries for a nameserver IP ("ip:53") to a local fake.
func route(m map[string]string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		to, ok := m[addr]
		if !ok {
			return nil, &net.OpError{Op: "dial", Err: net.UnknownNetworkError("unrouted " + addr)}
		}
		return (&net.Dialer{}).DialContext(ctx, network, to)
	}
}

func newAuth(sys *fakeSystem, routes map[string]string) *Authoritative {
	return &Authoritative{System: sys, Dial: route(routes), Timeout: 300 * time.Millisecond}
}

func ips(a []netip.Addr) []string {
	var out []string
	for _, x := range a {
		out = append(out, x.String())
	}
	return out
}

func TestAuthoritativeAnswers(t *testing.T) {
	ns := newFakeNS(t, map[string]fakeRecord{
		"op.example.org": {a: []string{"192.0.2.44"}, aaaa: []string{"2001:db8::44"}},
		"v4.example.org": {a: []string{"192.0.2.45"}},
		"example.org":    {a: []string{"192.0.2.1"}},
	})
	sys := &fakeSystem{
		ns:    map[string][]string{"example.org": {"ns1.example.org"}},
		addrs: map[string][]netip.Addr{"ns1.example.org": {netip.MustParseAddr("198.51.100.1")}},
	}
	a := newAuth(sys, map[string]string{"198.51.100.1:53": ns.addr()})
	ctx := context.Background()

	got, err := a.LookupNetIP(ctx, "ip", "OP.example.org.")
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if s := ips(got); !(s[0] == "192.0.2.44" && s[1] == "2001:db8::44") && !(s[1] == "192.0.2.44" && s[0] == "2001:db8::44") {
		t.Fatalf("%v", s)
	}
	if got, err := a.LookupNetIP(ctx, "ip", "v4.example.org"); err != nil || len(got) != 1 || got[0].String() != "192.0.2.45" {
		t.Fatalf("%v %v", got, err)
	}
	ns.mu.Lock()
	rd := ns.rd
	ns.mu.Unlock()
	if rd {
		t.Fatal("queries must not ask for recursion")
	}
	// None of this touched the system resolver for the domain itself.
	for _, h := range sys.ipLookups() {
		if h != "ns1.example.org" {
			t.Fatalf("system lookup of %q", h)
		}
	}
}

func TestAuthoritativeNoRecord(t *testing.T) {
	ns := newFakeNS(t, map[string]fakeRecord{
		"example.org":        {a: []string{"192.0.2.1"}},
		"nodata.example.org": {}, // exists, no addresses
	})
	sys := &fakeSystem{
		ns:    map[string][]string{"example.org": {"ns1.example.org"}},
		addrs: map[string][]netip.Addr{"ns1.example.org": {netip.MustParseAddr("198.51.100.1")}},
	}
	a := newAuth(sys, map[string]string{"198.51.100.1:53": ns.addr()})
	for _, host := range []string{"nope.example.org", "nodata.example.org"} {
		_, err := a.LookupNetIP(context.Background(), "ip", host)
		if !isNotFound(err) {
			t.Fatalf("%s: %v", host, err)
		}
		// And CheckDomain turns that into NoRecord, without any fallback.
		if c := CheckDomain(context.Background(), a, host, nil); c.State != NoRecord {
			t.Fatalf("%s: %v", host, c.State)
		}
	}
	for _, h := range sys.ipLookups() {
		if h != "ns1.example.org" {
			t.Fatalf("fell back to the system resolver for %q", h)
		}
	}
}

func TestAuthoritativeNewRecordIsSeenAtOnce(t *testing.T) {
	// The reported bug: a missing record, then the record is created; the
	// next lookup must see it although the zone data was NXDOMAIN before.
	recs := map[string]fakeRecord{"example.org": {a: []string{"192.0.2.1"}}}
	ns := newFakeNS(t, recs)
	sys := &fakeSystem{
		ns:    map[string][]string{"example.org": {"ns1.example.org"}},
		addrs: map[string][]netip.Addr{"ns1.example.org": {netip.MustParseAddr("198.51.100.1")}},
	}
	a := newAuth(sys, map[string]string{"198.51.100.1:53": ns.addr()})
	if _, err := a.LookupNetIP(context.Background(), "ip", "new.example.org"); !isNotFound(err) {
		t.Fatal(err)
	}
	ns.mu.Lock()
	recs["new.example.org"] = fakeRecord{a: []string{"192.0.2.44"}}
	ns.mu.Unlock()
	got, err := a.LookupNetIP(context.Background(), "ip", "new.example.org")
	if err != nil || len(got) != 1 || got[0].String() != "192.0.2.44" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAuthoritativeCNAME(t *testing.T) {
	ns := newFakeNS(t, map[string]fakeRecord{
		"example.org":        {a: []string{"192.0.2.1"}},
		"target.example.org": {a: []string{"192.0.2.9"}},
		"in.example.org":     {cname: "target.example.org"},
		"out.example.org":    {cname: "lb.provider.net"},
	})
	sys := &fakeSystem{
		ns: map[string][]string{"example.org": {"ns1.example.org"}},
		addrs: map[string][]netip.Addr{
			"ns1.example.org": {netip.MustParseAddr("198.51.100.1")},
			"lb.provider.net": {netip.MustParseAddr("203.0.113.7")},
		},
	}
	a := newAuth(sys, map[string]string{"198.51.100.1:53": ns.addr()})
	ctx := context.Background()

	got, err := a.LookupNetIP(ctx, "ip", "in.example.org")
	if err != nil || len(got) != 1 || got[0].String() != "192.0.2.9" {
		t.Fatalf("in-zone CNAME: %v %v", got, err)
	}
	got, err = a.LookupNetIP(ctx, "ip", "out.example.org")
	if err != nil || len(got) != 1 || got[0].String() != "203.0.113.7" {
		t.Fatalf("out-of-zone CNAME: %v %v", got, err)
	}
	found := false
	for _, h := range sys.ipLookups() {
		found = found || h == "lb.provider.net"
	}
	if !found {
		t.Fatalf("target not resolved by the normal resolver: %v", sys.ipLookups())
	}
}

func TestAuthoritativeFallsBackToSystem(t *testing.T) {
	sys := &fakeSystem{
		ns: map[string][]string{"example.org": {"ns1.example.org", "ns2.example.org"}},
		addrs: map[string][]netip.Addr{
			"ns1.example.org":  {netip.MustParseAddr("198.51.100.1")},
			"ns2.example.org":  {netip.MustParseAddr("198.51.100.2")},
			"down.example.org": {netip.MustParseAddr("192.0.2.77")},
		},
	}
	dead := deadAddr(t)
	a := newAuth(sys, map[string]string{"198.51.100.1:53": dead, "198.51.100.2:53": dead})
	got, err := a.LookupNetIP(context.Background(), "ip", "down.example.org")
	if err != nil || len(got) != 1 || got[0].String() != "192.0.2.77" {
		t.Fatalf("%v %v", got, err)
	}
	// ... and when the system resolver fails too, the caller sees a failure.
	_, err = a.LookupNetIP(context.Background(), "ip", "other.example.org")
	if err == nil || !isNotFound(err) {
		// The fake system resolver says "not found" for unknown hosts; that is
		// the system's own answer and passes through.
		t.Fatalf("%v", err)
	}
	b := &Authoritative{System: brokenSystem{sys}, Dial: a.Dial, Timeout: a.Timeout}
	if c := CheckDomain(context.Background(), b, "x.example.org", nil); c.State != LookupFailed {
		t.Fatalf("%v", c.State)
	}
}

type brokenSystem struct{ SystemResolver }

func (brokenSystem) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, &net.DNSError{Err: "server misbehaving", IsTemporary: true}
}

func TestAuthoritativeSecondServerAnswers(t *testing.T) {
	ns := newFakeNS(t, map[string]fakeRecord{"example.org": {a: []string{"192.0.2.1"}}, "w.example.org": {a: []string{"192.0.2.5"}}})
	sys := &fakeSystem{
		ns: map[string][]string{"example.org": {"ns1.example.org", "ns2.example.org"}},
		addrs: map[string][]netip.Addr{
			"ns1.example.org": {netip.MustParseAddr("198.51.100.1")},
			"ns2.example.org": {netip.MustParseAddr("198.51.100.2")},
		},
	}
	a := newAuth(sys, map[string]string{"198.51.100.1:53": deadAddr(t), "198.51.100.2:53": ns.addr()})
	for range 5 { // whichever server is tried first
		got, err := a.LookupNetIP(context.Background(), "ip", "w.example.org")
		if err != nil || len(got) != 1 || got[0].String() != "192.0.2.5" {
			t.Fatalf("%v %v", got, err)
		}
	}
	for _, h := range sys.ipLookups() {
		if h == "w.example.org" {
			t.Fatal("fell back although one server was fine")
		}
	}
}

func TestAuthoritativeZoneDiscoveryAndCache(t *testing.T) {
	ns := newFakeNS(t, map[string]fakeRecord{"example.org": {a: []string{"192.0.2.1"}}, "a.b.example.org": {a: []string{"192.0.2.6"}}})
	sys := &fakeSystem{
		ns:    map[string][]string{"example.org": {"ns1.example.org"}},
		addrs: map[string][]netip.Addr{"ns1.example.org": {netip.MustParseAddr("198.51.100.1")}},
	}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	a := newAuth(sys, map[string]string{"198.51.100.1:53": ns.addr()})
	a.Now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := a.LookupNetIP(ctx, "ip", "a.b.example.org"); err != nil {
		t.Fatal(err)
	}
	want := []string{"a.b.example.org", "b.example.org", "example.org"}
	if got := sys.nsLookups(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("walked %v, want %v", got, want)
	}
	// Cached for 10 minutes ...
	now = now.Add(9 * time.Minute)
	a.LookupNetIP(ctx, "ip", "a.b.example.org")
	if n := len(sys.nsLookups()); n != 3 {
		t.Fatalf("%d NS lookups within the cache time", n)
	}
	// ... then looked up again.
	now = now.Add(2 * time.Minute)
	a.LookupNetIP(ctx, "ip", "a.b.example.org")
	if n := len(sys.nsLookups()); n != 6 {
		t.Fatalf("%d NS lookups after the cache time", n)
	}
}

func TestAuthoritativeNoZoneUsesSystem(t *testing.T) {
	sys := &fakeSystem{addrs: map[string][]netip.Addr{"lonely.test": {netip.MustParseAddr("192.0.2.3")}}}
	a := newAuth(sys, nil)
	got, err := a.LookupNetIP(context.Background(), "ip", "lonely.test")
	if err != nil || len(got) != 1 || got[0].String() != "192.0.2.3" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAuthoritativeMX(t *testing.T) {
	ns := newFakeNS(t, map[string]fakeRecord{
		"example.org":      {mx: map[string]uint16{"backup.example.net": 20, "mail.example.org": 10}},
		"none.example.org": {a: []string{"192.0.2.1"}},
		"null.example.org": {mx: map[string]uint16{"": 0}},
	})
	sys := &fakeSystem{
		ns:    map[string][]string{"example.org": {"ns1.example.org"}},
		addrs: map[string][]netip.Addr{"ns1.example.org": {netip.MustParseAddr("198.51.100.1")}},
	}
	a := newAuth(sys, map[string]string{"198.51.100.1:53": ns.addr()})
	ctx := context.Background()

	mx, err := a.LookupMX(ctx, "Example.org.")
	if err != nil {
		t.Fatal(err)
	}
	if len(mx) != 2 || mx[0].Host != "mail.example.org." || mx[0].Pref != 10 || mx[1].Host != "backup.example.net." {
		t.Fatalf("got %+v %+v", mx[0], mx[1])
	}
	for _, name := range []string{"none.example.org", "missing.example.org"} {
		if _, err := a.LookupMX(ctx, name); !isNotFound(err) {
			t.Fatalf("%s: want not found, got %v", name, err)
		}
	}
	mx, err = a.LookupMX(ctx, "null.example.org")
	if err != nil || len(mx) != 1 || mx[0].Host != "." {
		t.Fatalf("null MX: %v %v", mx, err)
	}
}

type mxSystem struct {
	*fakeSystem
	mx []*net.MX
}

func (m mxSystem) LookupMX(context.Context, string) ([]*net.MX, error) { return m.mx, nil }

func TestAuthoritativeMXFallsBackToSystem(t *testing.T) {
	sys := mxSystem{
		fakeSystem: &fakeSystem{
			ns:    map[string][]string{"example.org": {"ns1.example.org"}},
			addrs: map[string][]netip.Addr{"ns1.example.org": {netip.MustParseAddr("198.51.100.1")}},
		},
		mx: []*net.MX{{Host: "mx.example.org.", Pref: 5}},
	}
	a := &Authoritative{System: sys, Dial: route(map[string]string{"198.51.100.1:53": deadAddr(t)}), Timeout: 200 * time.Millisecond}
	mx, err := a.LookupMX(context.Background(), "example.org")
	if err != nil || len(mx) != 1 || mx[0].Host != "mx.example.org." {
		t.Fatalf("got %v %v", mx, err)
	}
}
