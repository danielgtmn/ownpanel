package panel

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner/hetznertest"
	"github.com/danielgtmn/ownpanel/internal/secrets"
)

const (
	rootToken = "hz-root-0123456789abcdef"
	bobToken  = "hz-bob-0123456789abcdef"
	newToken  = "hz-new-0123456789abcdef"
)

var (
	serverV4 = netip.MustParseAddr("203.0.113.5")
	serverV6 = netip.MustParseAddr("2001:db8::5")
)

type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][]netip.Addr
	errs    map[string]error
	calls   int
}

func (r *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if err := r.errs[host]; err != nil {
		return nil, err
	}
	if a, ok := r.answers[host]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (r *fakeResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// dnsWorld is the permission world plus DNS: root owns the connection
// "root-main" and bob "bob-main"; both projects hold example.com, bob's also
// other.org. The sites' domains are <site>.example.com.
type dnsWorld struct {
	world
	t        *testing.T
	hz       map[string]*hetznertest.Server // by token
	reject   *hetznertest.Server            // answers every unknown token with 401
	res      *fakeResolver
	now      time.Time
	rootConn int64
	bobConn  int64
}

func newDNSWorld(t *testing.T) *dnsWorld {
	t.Helper()
	d := &dnsWorld{
		world: newWorld(t), t: t, hz: map[string]*hetznertest.Server{},
		res: &fakeResolver{answers: map[string][]netip.Addr{}, errs: map[string]error{}},
		now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC),
	}
	d.reject = hetznertest.New(t, "never-accepted")
	key := make([]byte, secrets.KeySize)
	rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	svc := d.svc
	svc.secrets = box
	svc.serverIPs = []netip.Addr{serverV4, serverV6}
	svc.resolver = d.res
	svc.now = func() time.Time { return d.now }
	svc.newProvider = func(provider, token string) (dns.Provider, error) {
		if provider != "hetzner" {
			return nil, fmt.Errorf("unknown provider %q", provider)
		}
		srv := d.hz[token]
		if srv == nil {
			srv = d.reject
		}
		return hetzner.New(token, hetzner.WithBaseURL(srv.BaseURL()),
			hetzner.WithPollIntervals(time.Millisecond, 2*time.Millisecond), hetzner.WithRetryDelay(time.Millisecond)), nil
	}
	d.addHetzner(rootToken, "example.com")
	d.addHetzner(bobToken, "example.com", "other.org")
	d.addHetzner(newToken, "new.example")
	ctx := context.Background()
	rc, err := svc.CreateDNSConnection(ctx, admin, "hetzner", "root-main", rootToken)
	if err != nil {
		t.Fatal(err)
	}
	bc, err := svc.CreateDNSConnection(ctx, bob, "hetzner", "bob-main", bobToken)
	if err != nil {
		t.Fatal(err)
	}
	d.rootConn, d.bobConn = rc.ID, bc.ID
	return d
}

func (d *dnsWorld) addHetzner(token string, zones ...string) *hetznertest.Server {
	srv := hetznertest.New(d.t, token)
	for _, z := range zones {
		srv.AddZone(z, "primary")
	}
	d.hz[token] = srv
	return srv
}

func (d *dnsWorld) ownConn(a Actor) int64 {
	if a.UserID == 1 {
		return d.rootConn
	}
	return d.bobConn
}

func (d *dnsWorld) otherConn(a Actor) int64 {
	if a.UserID == 1 {
		return d.bobConn
	}
	return d.rootConn
}

func (d *dnsWorld) conn(id int64) DNSConnection {
	d.t.Helper()
	c, err := d.svc.store.DNSConnection(context.Background(), id)
	if err != nil {
		d.t.Fatal(err)
	}
	return connectionView(c)
}

func countCalls(srv *hetznertest.Server, call string) int {
	n := 0
	for _, c := range srv.Calls() {
		if c == call {
			n++
		}
	}
	return n
}

func TestDNSPermissionMatrix(t *testing.T) {
	ctx := context.Background()
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", admin}, {"admin write token", rootWriteTok}, {"member", bob}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody},
	}
	F, N, U := Forbidden, NotFound, Unauthenticated
	ops := []struct {
		name string
		run  func(d *dnsWorld, a Actor) error
		want []Kind // per actor, in the order above
	}{
		{"server addresses", func(d *dnsWorld, a Actor) error { _, err := d.svc.ServerIPs(ctx, a); return err },
			[]Kind{ok, ok, ok, ok, ok, ok, ok, U}},
		{"list connections", func(d *dnsWorld, a Actor) error { _, err := d.svc.DNSConnections(ctx, a); return err },
			[]Kind{ok, ok, ok, ok, F, ok, F, U}},
		{"create connection", func(d *dnsWorld, a Actor) error {
			_, err := d.svc.CreateDNSConnection(ctx, a, "hetzner", "fresh", newToken)
			return err
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"check own connection", func(d *dnsWorld, a Actor) error {
			_, err := d.svc.CheckDNSConnection(ctx, a, d.ownConn(a))
			return err
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"check other's connection", func(d *dnsWorld, a Actor) error {
			_, err := d.svc.CheckDNSConnection(ctx, a, d.otherConn(a))
			return err
		}, []Kind{F, F, N, F, F, F, F, U}},
		{"delete own connection", func(d *dnsWorld, a Actor) error { return d.svc.DeleteDNSConnection(ctx, a, d.ownConn(a)) },
			[]Kind{ok, ok, ok, F, F, F, F, U}},
		{"delete other's connection", func(d *dnsWorld, a Actor) error { return d.svc.DeleteDNSConnection(ctx, a, d.otherConn(a)) },
			[]Kind{ok, ok, N, N, N, F, N, U}},
		{"list zones", func(d *dnsWorld, a Actor) error { _, err := d.svc.DNSZones(ctx, a); return err },
			[]Kind{ok, ok, ok, ok, F, ok, F, U}},
		{"list available zones", func(d *dnsWorld, a Actor) error { _, err := d.svc.AvailableDNSZones(ctx, a); return err },
			[]Kind{ok, ok, ok, ok, F, ok, F, U}},
		{"adopt own zone", func(d *dnsWorld, a Actor) error {
			_, err := d.svc.AdoptDNSZone(ctx, a, d.ownConn(a), "example.com")
			return err
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"adopt other's zone", func(d *dnsWorld, a Actor) error {
			_, err := d.svc.AdoptDNSZone(ctx, a, d.otherConn(a), "example.com")
			return err
		}, []Kind{F, F, N, F, F, F, F, U}},
		{"release own zone", func(d *dnsWorld, a Actor) error {
			if _, err := d.svc.store.AdoptDNSZone(ctx, d.ownConn(a), "unused.example"); err != nil {
				d.t.Fatal(err)
			}
			return d.svc.ReleaseDNSZone(ctx, a, d.ownConn(a), "unused.example")
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"release other's zone", func(d *dnsWorld, a Actor) error {
			if _, err := d.svc.store.AdoptDNSZone(ctx, d.otherConn(a), "unused.example"); err != nil {
				d.t.Fatal(err)
			}
			return d.svc.ReleaseDNSZone(ctx, a, d.otherConn(a), "unused.example")
		}, []Kind{F, F, N, F, F, F, F, U}},
		{"list own records", func(d *dnsWorld, a Actor) error {
			_, err := d.svc.DNSRecords(ctx, a, d.ownConn(a), "example.com")
			return err
		}, []Kind{ok, ok, ok, ok, F, ok, F, U}},
		{"list other's records", func(d *dnsWorld, a Actor) error {
			_, err := d.svc.DNSRecords(ctx, a, d.otherConn(a), "example.com")
			return err
		}, []Kind{F, F, N, N, F, F, F, U}},
		{"set own record", func(d *dnsWorld, a Actor) error {
			_, err := d.svc.SetDNSRecord(ctx, a, d.ownConn(a), "example.com", "www", "A", nil, []string{"198.51.100.7"})
			return err
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"set other's record", func(d *dnsWorld, a Actor) error {
			_, err := d.svc.SetDNSRecord(ctx, a, d.otherConn(a), "example.com", "www", "A", nil, []string{"198.51.100.7"})
			return err
		}, []Kind{F, F, N, F, F, F, F, U}},
		{"delete own record", func(d *dnsWorld, a Actor) error {
			return d.svc.DeleteDNSRecord(ctx, a, d.ownConn(a), "example.com", "www", "A")
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"delete other's record", func(d *dnsWorld, a Actor) error {
			return d.svc.DeleteDNSRecord(ctx, a, d.otherConn(a), "example.com", "www", "A")
		}, []Kind{F, F, N, F, F, F, F, U}},
		{"domain status", func(d *dnsWorld, a Actor) error { _, err := d.svc.DomainStatuses(ctx, a, "bobsite"); return err },
			[]Kind{ok, ok, ok, ok, ok, ok, N, U}},
		{"point here", func(d *dnsWorld, a Actor) error {
			_, err := d.svc.PointDomain(ctx, a, "bobsite", "bobsite.example.com")
			return err
		}, []Kind{ok, ok, ok, F, ok, F, N, U}},
	}
	for _, op := range ops {
		for i, ac := range actors {
			t.Run(op.name+"/"+ac.name, func(t *testing.T) {
				d := newDNSWorld(t)
				err := op.run(d, ac.a)
				if want := op.want[i]; want == ok {
					if err != nil {
						t.Fatalf("want success, got %v: %v", KindOf(err), err)
					}
				} else {
					wantKind(t, err, want)
				}
			})
		}
	}
}

func TestDNSConnectionsSeenByAdminAndOwner(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	list, err := d.svc.DNSConnections(ctx, admin)
	if err != nil || len(list) != 2 || list[0].Owner != "bob" || list[1].Owner != "root" {
		t.Fatalf("admin: %+v %v", list, err)
	}
	list, err = d.svc.DNSConnections(ctx, bob)
	if err != nil || len(list) != 1 || list[0].Name != "bob-main" {
		t.Fatalf("bob: %+v %v", list, err)
	}
	c := list[0]
	if c.Provider != "hetzner" || c.ZoneCount == nil || *c.ZoneCount != 2 || c.CanWrite != nil || c.LastError != "" ||
		c.LastCheckedAt == nil || !c.LastCheckedAt.Equal(d.now) {
		t.Fatalf("new connection: %+v", c)
	}
	// The token is stored encrypted.
	raw, _ := d.svc.store.DNSConnection(ctx, c.ID)
	if bytes.Contains(raw.Secret, []byte(bobToken)) {
		t.Fatal("the token is stored in plain text")
	}
	// Admins cannot use other people's connections, but can remove them.
	_, err = d.svc.DNSRecords(ctx, admin, d.bobConn, "example.com")
	wantKind(t, err, Forbidden)
	if !strings.Contains(err.Error(), "bob") {
		t.Fatalf("message %q", err)
	}
	if err := d.svc.DeleteDNSConnection(ctx, admin, d.bobConn); err != nil {
		t.Fatal(err)
	}
	acts, _ := d.svc.store.Activities(ctx, 1)
	if acts[0].Action != "dns.disconnect" || acts[0].Target != "bob-main" || !strings.Contains(acts[0].Detail, "owner bob") {
		t.Fatalf("activity: %+v", acts[0])
	}
}

func TestCreateDNSConnectionValidation(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, provider, connName, token string
		kind                            Kind
		msg                             string
	}{
		{"rejected token", "hetzner", "x", "wrong-token-value", Invalid, "The token was rejected by Hetzner"},
		{"unknown provider", "cloudflare", "x", newToken, Invalid, "unknown DNS provider"},
		{"empty name", "hetzner", "  ", newToken, Invalid, "name"},
		{"empty token", "hetzner", "x", " ", Invalid, "token"},
		{"token with a line break", "hetzner", "x", "abc\ndef", Invalid, "line breaks"},
		{"duplicate name", "hetzner", "bob-main", newToken, Conflict, "already have"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.svc.CreateDNSConnection(ctx, bob, tc.provider, tc.connName, tc.token)
			wantKind(t, err, tc.kind)
			if !strings.Contains(err.Error(), tc.msg) || strings.Contains(err.Error(), tc.token) && tc.token != " " {
				t.Fatalf("message %q", err)
			}
		})
	}
	// Empty provider means Hetzner; a read-only token is accepted.
	d.hz[newToken].ReadOnlyToken = true
	c, err := d.svc.CreateDNSConnection(ctx, bob, "", "readonly", newToken)
	if err != nil || c.Provider != "hetzner" || c.CanWrite != nil {
		t.Fatalf("%+v %v", c, err)
	}
	if n := len(d.hz[newToken].Writes()); n != 0 {
		t.Fatalf("creating a connection must not write: %d writes", n)
	}
}

func TestCheckDNSConnectionReportsProblems(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	srv := d.hz[bobToken]
	srv.Token = "rotated" // the token was deleted at Hetzner
	d.now = d.now.Add(time.Hour)
	c, err := d.svc.CheckDNSConnection(ctx, bob, d.bobConn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.LastError, "rejected by Hetzner") || !c.LastCheckedAt.Equal(d.now) || *c.ZoneCount != 2 {
		t.Fatalf("%+v", c)
	}
	srv.Token = bobToken
	srv.AddZone("third.example", "secondary")
	c, err = d.svc.CheckDNSConnection(ctx, bob, d.bobConn)
	if err != nil || c.LastError != "" || *c.ZoneCount != 3 {
		t.Fatalf("%+v %v", c, err)
	}
	// With another secret key the stored token cannot be read.
	other, _ := secrets.New(bytes.Repeat([]byte{7}, secrets.KeySize))
	d.svc.secrets = other
	c, err = d.svc.CheckDNSConnection(ctx, bob, d.bobConn)
	if err != nil || !strings.Contains(c.LastError, "secret key has changed") {
		t.Fatalf("%+v %v", c, err)
	}
	_, err = d.svc.DNSRecords(ctx, bob, d.bobConn, "example.com")
	wantKind(t, err, Conflict)
}

func TestReadOnlyProviderTokenIsFlagged(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	d.hz[bobToken].ReadOnlyToken = true
	_, err := d.svc.SetDNSRecord(ctx, bob, d.bobConn, "example.com", "www", "A", nil, []string{"198.51.100.7"})
	wantKind(t, err, Conflict)
	c := d.conn(d.bobConn)
	if c.CanWrite == nil || *c.CanWrite || !strings.Contains(c.LastError, "may only read") {
		t.Fatalf("%+v", c)
	}
	// A successful listing does not hide that writes are refused.
	c, err = d.svc.CheckDNSConnection(ctx, bob, d.bobConn)
	if err != nil || !strings.Contains(c.LastError, "may only read") {
		t.Fatalf("%+v %v", c, err)
	}
	// A successful write proves the opposite.
	d.hz[bobToken].ReadOnlyToken = false
	if _, err := d.svc.SetDNSRecord(ctx, bob, d.bobConn, "example.com", "www", "A", nil, []string{"198.51.100.7"}); err != nil {
		t.Fatal(err)
	}
	c = d.conn(d.bobConn)
	if c.CanWrite == nil || !*c.CanWrite || c.LastError != "" {
		t.Fatalf("%+v", c)
	}
}

func TestProviderErrorKinds(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		err  hetznertest.Error
		kind Kind
	}{
		{"rate limit", hetznertest.Error{Status: 429, Code: "rate_limit_exceeded", Message: "limit"}, Throttled},
		{"server error", hetznertest.Error{Status: 503, Code: "unavailable", Message: "maintenance"}, Unavailable},
		{"bad gateway without body", hetznertest.Error{Status: 502}, Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDNSWorld(t)
			d.hz[bobToken].Fail("GET /zones/example.com/rrsets", -1, tc.err)
			_, err := d.svc.DNSRecords(ctx, bob, d.bobConn, "example.com")
			wantKind(t, err, tc.kind)
			if strings.Contains(err.Error(), bobToken) {
				t.Fatal("token in message")
			}
		})
	}
	d := newDNSWorld(t)
	_, err := d.svc.DNSRecords(ctx, bob, d.bobConn, "unknown.example")
	wantKind(t, err, NotFound)
}

func TestZoneCacheExpires(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	srv := d.hz[bobToken]
	if _, err := d.svc.AdoptDNSZone(ctx, bob, d.bobConn, "other.org"); err != nil {
		t.Fatal(err)
	}
	srv.ResetCalls()
	zones := func() []DNSZone {
		t.Helper()
		z, err := d.svc.DNSZones(ctx, bob)
		if err != nil || len(z.Problems) != 0 {
			t.Fatalf("%+v %v", z, err)
		}
		return z.Zones
	}
	// Creating the connection filled the cache.
	if z := zones(); len(z) != 2 || z[0].Name != "example.com" || z[1].Name != "other.org" || z[0].ConnectionName != "bob-main" {
		t.Fatalf("%+v", z)
	}
	d.now = d.now.Add(59 * time.Second)
	zones()
	if n := countCalls(srv, "GET /zones"); n != 0 {
		t.Fatalf("%d zone listings within 60 s", n)
	}
	d.now = d.now.Add(time.Second)
	zones()
	zones()
	if n := countCalls(srv, "GET /zones"); n != 1 {
		t.Fatalf("%d zone listings after 60 s, want 1", n)
	}
	// Writes invalidate the cache.
	if _, err := d.svc.SetDNSRecord(ctx, bob, d.bobConn, "example.com", "www", "A", nil, []string{"198.51.100.7"}); err != nil {
		t.Fatal(err)
	}
	zones()
	if n := countCalls(srv, "GET /zones"); n != 2 {
		t.Fatalf("%d zone listings after a write, want 2", n)
	}
}

func TestZonesListSkipsFailingConnection(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	if _, err := d.svc.CreateDNSConnection(ctx, bob, "hetzner", "second", newToken); err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.AdoptDNSZone(ctx, bob, d.bobConn, "other.org"); err != nil {
		t.Fatal(err)
	}
	d.hz[newToken].Token = "rotated"
	d.now = d.now.Add(time.Minute)
	z, err := d.svc.DNSZones(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	if len(z.Zones) != 2 || len(z.Problems) != 1 || z.Problems[0].ConnectionName != "second" ||
		!strings.Contains(z.Problems[0].Error, "rejected") {
		t.Fatalf("%+v", z)
	}
}

func TestRecordsRoundTrip(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	srv := d.hz[bobToken]
	srv.AddRRSet("example.com", hetznertest.RRSet{Name: "@", Type: "SOA", Records: []string{"hydrogen.ns.hetzner.com. dns.hetzner.com. 1 86400 10800 3600000 3600"}})
	ttl := 600
	txt := `v=spf1 include:_spf.example.net "quoted" -all`
	rs, err := d.svc.SetDNSRecord(ctx, bob, d.bobConn, "Example.com.", "@", "txt", &ttl, []string{txt})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rs, RecordSet{Name: "@", Type: "TXT", TTL: &ttl, Values: []string{txt}}) {
		t.Fatalf("%+v", rs)
	}
	if _, err := d.svc.SetDNSRecord(ctx, bob, d.bobConn, "example.com", "mail", "MX", nil, []string{"10   Mx.Example.net"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.RRSet("example.com", "mail", "MX"); !reflect.DeepEqual(got.Records, []string{"10 mx.example.net."}) || got.TTL != 0 {
		t.Fatalf("stored MX %+v", got)
	}
	list, err := d.svc.DNSRecords(ctx, bob, d.bobConn, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range list {
		keys = append(keys, r.Name+"/"+r.Type)
	}
	if !reflect.DeepEqual(keys, []string{"@/SOA", "@/TXT", "mail/MX"}) {
		t.Fatalf("%v", keys)
	}
	if list[1].Values[0] != txt || *list[1].TTL != 600 || list[2].TTL != nil {
		t.Fatalf("%+v", list)
	}
	err = d.svc.DeleteDNSRecord(ctx, bob, d.bobConn, "example.com", "@", "SOA")
	wantKind(t, err, Invalid)
	err = d.svc.DeleteDNSRecord(ctx, bob, d.bobConn, "example.com", "@", "NS")
	wantKind(t, err, Invalid)
	if err := d.svc.DeleteDNSRecord(ctx, bob, d.bobConn, "example.com", "mail", "MX"); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.RRSet("example.com", "mail", "MX"); ok {
		t.Fatal("MX not deleted")
	}
	// A secondary zone cannot be changed.
	srv.AddZone("secondary.example", "secondary")
	d.now = d.now.Add(time.Hour)
	_, err = d.svc.SetDNSRecord(ctx, bob, d.bobConn, "secondary.example", "www", "A", nil, []string{"198.51.100.7"})
	wantKind(t, err, Conflict)
	// Protected records are reported as such.
	srv.AddRRSet("example.com", hetznertest.RRSet{Name: "locked", Type: "A", Records: []string{"198.51.100.1"}, Protected: true})
	_, err = d.svc.SetDNSRecord(ctx, bob, d.bobConn, "example.com", "locked", "A", nil, []string{"198.51.100.2"})
	wantKind(t, err, Conflict)
}

func TestRecordValidation(t *testing.T) {
	ttl := func(n int) *int { return &n }
	for _, tc := range []struct {
		name, rname, typ string
		ttl              *int
		values           []string
		want             []string // normalized values; nil = must fail
		wantName         string
	}{
		{"A", "www", "A", nil, []string{"198.51.100.7", " 198.51.100.7 "}, []string{"198.51.100.7"}, "www"},
		{"A refuses IPv6", "www", "A", nil, []string{"2001:db8::1"}, nil, ""},
		{"A refuses mapped IPv6", "www", "A", nil, []string{"::ffff:198.51.100.7"}, nil, ""},
		{"A refuses junk", "www", "A", nil, []string{"198.51.100"}, nil, ""},
		{"AAAA normalizes", "www", "AAAA", nil, []string{"2001:DB8:0::1"}, []string{"2001:db8::1"}, "www"},
		{"AAAA refuses IPv4", "www", "AAAA", nil, []string{"198.51.100.7"}, nil, ""},
		{"CNAME adds the dot", "www", "CNAME", nil, []string{"Target.Example.net"}, []string{"target.example.net."}, "www"},
		{"CNAME keeps the dot", "www", "CNAME", nil, []string{"target.example.net."}, []string{"target.example.net."}, "www"},
		{"CNAME needs a full name", "www", "CNAME", nil, []string{"target"}, nil, ""},
		{"CNAME not at the apex", "@", "CNAME", nil, []string{"target.example.net"}, nil, ""},
		{"CNAME only one value", "www", "CNAME", nil, []string{"a.example.net", "b.example.net"}, nil, ""},
		{"MX", "@", "MX", nil, []string{"10 mail.example.com"}, []string{"10 mail.example.com."}, "@"},
		{"MX null", "@", "MX", nil, []string{"0 ."}, []string{"0 ."}, "@"},
		{"MX without priority", "@", "MX", nil, []string{"mail.example.com"}, nil, ""},
		{"MX priority too big", "@", "MX", nil, []string{"70000 mail.example.com"}, nil, ""},
		{"TXT kept as it is", "_dmarc", "TXT", nil, []string{` v=DMARC1; p=none "x" `}, []string{` v=DMARC1; p=none "x" `}, "_dmarc"},
		{"TXT without line breaks", "@", "TXT", nil, []string{"a\nb"}, nil, ""},
		{"TXT too long", "@", "TXT", nil, []string{strings.Repeat("a", 4001)}, nil, ""},
		{"CAA quotes", "@", "CAA", nil, []string{"0 ISSUE letsencrypt.org"}, []string{`0 issue "letsencrypt.org"`}, "@"},
		{"CAA quoted", "@", "CAA", nil, []string{`128 iodef "mailto:a@example.com"`}, []string{`128 iodef "mailto:a@example.com"`}, "@"},
		{"CAA bad flags", "@", "CAA", nil, []string{`300 issue "x"`}, nil, ""},
		{"SRV", "_sip._tcp", "SRV", nil, []string{"10 5 5060 sip.example.com"}, []string{"10 5 5060 sip.example.com."}, "_sip._tcp"},
		{"SRV short", "_sip._tcp", "SRV", nil, []string{"10 5060 sip.example.com"}, nil, ""},
		{"NS delegation", "sub", "NS", nil, []string{"ns1.example.net"}, []string{"ns1.example.net."}, "sub"},
		{"NS at the apex", "@", "NS", nil, []string{"ns1.example.net"}, nil, ""},
		{"SOA", "@", "SOA", nil, []string{"x"}, nil, ""},
		{"unsupported type", "@", "PTR", nil, []string{"x.example.com"}, nil, ""},
		{"garbage type", "@", "A A", nil, []string{"198.51.100.7"}, nil, ""},
		{"TTL lower bound", "www", "A", ttl(60), []string{"198.51.100.7"}, []string{"198.51.100.7"}, "www"},
		{"TTL too low", "www", "A", ttl(59), []string{"198.51.100.7"}, nil, ""},
		{"TTL upper bound", "www", "A", ttl(86400), []string{"198.51.100.7"}, []string{"198.51.100.7"}, "www"},
		{"TTL too high", "www", "A", ttl(86401), []string{"198.51.100.7"}, nil, ""},
		{"no values", "www", "A", nil, nil, nil, ""},
		{"full name inside the zone", "WWW.example.com.", "A", nil, []string{"198.51.100.7"}, []string{"198.51.100.7"}, "www"},
		{"zone itself", "example.com.", "A", nil, []string{"198.51.100.7"}, []string{"198.51.100.7"}, "@"},
		{"empty name is the apex", "", "A", nil, []string{"198.51.100.7"}, []string{"198.51.100.7"}, "@"},
		{"full name outside the zone", "www.example.org.", "A", nil, []string{"198.51.100.7"}, nil, ""},
		{"wildcard", "*.dev", "A", nil, []string{"198.51.100.7"}, []string{"198.51.100.7"}, "*.dev"},
		{"wildcard in the middle", "a.*", "A", nil, []string{"198.51.100.7"}, nil, ""},
		{"bad name", "a b", "A", nil, []string{"198.51.100.7"}, nil, ""},
		{"non-ASCII name", "bücher", "A", nil, []string{"198.51.100.7"}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs, err := validateRecordSet("example.com", tc.rname, tc.typ, tc.ttl, tc.values)
			if tc.want == nil {
				wantKind(t, err, Invalid)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rs.Values, tc.want) || rs.Name != tc.wantName {
				t.Fatalf("got %q %q, want %q %q", rs.Name, rs.Values, tc.wantName, tc.want)
			}
			if tc.ttl != nil && rs.TTL != *tc.ttl || tc.ttl == nil && rs.TTL != 0 {
				t.Fatalf("ttl %d", rs.TTL)
			}
		})
	}
}

func TestPointHere(t *testing.T) {
	ctx := context.Background()

	t.Run("creates A and AAAA, then nothing", func(t *testing.T) {
		d := newDNSWorld(t)
		srv := d.hz[bobToken]
		res, err := d.svc.PointDomain(ctx, bob, "bobsite", "BobSite.example.com")
		if err != nil {
			t.Fatal(err)
		}
		ttl := 300
		want := []RecordSet{
			{Name: "bobsite", Type: "A", TTL: &ttl, Values: []string{"203.0.113.5"}},
			{Name: "bobsite", Type: "AAAA", TTL: &ttl, Values: []string{"2001:db8::5"}},
		}
		if !reflect.DeepEqual(res.Changed, want) {
			t.Fatalf("%+v", res.Changed)
		}
		a, _ := srv.RRSet("example.com", "bobsite", "A")
		aaaa, _ := srv.RRSet("example.com", "bobsite", "AAAA")
		if !reflect.DeepEqual(a.Records, []string{"203.0.113.5"}) || a.TTL != 300 || !reflect.DeepEqual(aaaa.Records, []string{"2001:db8::5"}) {
			t.Fatalf("stored %+v %+v", a, aaaa)
		}
		acts, _ := d.svc.store.Activities(ctx, 1)
		if acts[0].Action != "dns.point" || acts[0].Target != "bobsite.example.com" || !strings.Contains(acts[0].Detail, "A, AAAA") {
			t.Fatalf("activity %+v", acts[0])
		}
		writes := len(srv.Writes())
		res, err = d.svc.PointDomain(ctx, bob, "bobsite", "bobsite.example.com")
		if err != nil || res.Changed == nil || len(res.Changed) != 0 {
			t.Fatalf("second time: %+v %v", res, err)
		}
		if len(srv.Writes()) != writes {
			t.Fatal("a correct domain must not be written again")
		}
		if acts2, _ := d.svc.store.Activities(ctx, 1); acts2[0].ID != acts[0].ID {
			t.Fatal("nothing changed, nothing to record")
		}
	})

	t.Run("replaces foreign addresses and removes a stale family", func(t *testing.T) {
		d := newDNSWorld(t)
		srv := d.hz[bobToken]
		srv.AddRRSet("example.com", hetznertest.RRSet{Name: "bobsite", Type: "A", TTL: 3600, Records: []string{"198.51.100.9"}})
		srv.AddRRSet("example.com", hetznertest.RRSet{Name: "bobsite", Type: "AAAA", Records: []string{"2001:db8::99"}})
		d.svc.serverIPs = []netip.Addr{serverV4}
		res, err := d.svc.PointDomain(ctx, bob, "bobsite", "bobsite.example.com")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Changed) != 2 || res.Changed[0].Type != "A" || res.Changed[1].Type != "AAAA" || len(res.Changed[1].Values) != 0 {
			t.Fatalf("%+v", res.Changed)
		}
		if a, _ := srv.RRSet("example.com", "bobsite", "A"); !reflect.DeepEqual(a.Records, []string{"203.0.113.5"}) {
			t.Fatalf("A %+v", a)
		}
		if _, ok := srv.RRSet("example.com", "bobsite", "AAAA"); ok {
			t.Fatal("the foreign AAAA must be gone")
		}
	})

	t.Run("refuses a CNAME", func(t *testing.T) {
		d := newDNSWorld(t)
		srv := d.hz[bobToken]
		srv.AddRRSet("example.com", hetznertest.RRSet{Name: "bobsite", Type: "CNAME", Records: []string{"elsewhere.example.net."}})
		_, err := d.svc.PointDomain(ctx, bob, "bobsite", "bobsite.example.com")
		wantKind(t, err, Conflict)
		if !strings.Contains(err.Error(), "bobsite.example.com has a CNAME record; remove it first") {
			t.Fatalf("message %q", err)
		}
		if len(srv.Writes()) != 0 {
			t.Fatal("nothing may be written")
		}
	})

	t.Run("no matching zone", func(t *testing.T) {
		d := newDNSWorld(t)
		if err := d.svc.DeleteDNSConnection(ctx, bob, d.bobConn); err != nil {
			t.Fatal(err)
		}
		_, err := d.svc.PointDomain(ctx, bob, "bobsite", "bobsite.example.com")
		wantKind(t, err, Conflict)
		if !strings.Contains(err.Error(), "none of your DNS connections") {
			t.Fatalf("message %q", err)
		}
	})

	t.Run("same zone in two connections", func(t *testing.T) {
		d := newDNSWorld(t)
		d.addHetzner("hz-dup-token-0123456789", "example.com")
		if _, err := d.svc.CreateDNSConnection(ctx, bob, "hetzner", "duplicate", "hz-dup-token-0123456789"); err != nil {
			t.Fatal(err)
		}
		_, err := d.svc.PointDomain(ctx, bob, "bobsite", "bobsite.example.com")
		wantKind(t, err, Conflict)
		if !strings.Contains(err.Error(), "more than one") || !strings.Contains(err.Error(), `"duplicate"`) {
			t.Fatalf("message %q", err)
		}
		st, err := d.svc.DomainStatuses(ctx, bob, "bobsite")
		if err != nil || st[0].Zone != nil {
			t.Fatalf("an ambiguous zone is not offered: %+v %v", st, err)
		}
	})

	t.Run("longest zone wins across connections", func(t *testing.T) {
		d := newDNSWorld(t)
		sub := d.addHetzner("hz-sub-token-0123456789", "bobsite.example.com")
		if _, err := d.svc.CreateDNSConnection(ctx, bob, "hetzner", "sub", "hz-sub-token-0123456789"); err != nil {
			t.Fatal(err)
		}
		res, err := d.svc.PointDomain(ctx, bob, "bobsite", "bobsite.example.com")
		if err != nil || len(res.Changed) != 2 || res.Changed[0].Name != "@" {
			t.Fatalf("%+v %v", res, err)
		}
		if _, ok := sub.RRSet("bobsite.example.com", "@", "A"); !ok {
			t.Fatal("not written into the more specific zone")
		}
		if len(d.hz[bobToken].Writes()) != 0 {
			t.Fatal("the parent zone must not be touched")
		}
	})

	t.Run("without server addresses", func(t *testing.T) {
		d := newDNSWorld(t)
		d.svc.serverIPs = nil
		_, err := d.svc.PointDomain(ctx, bob, "bobsite", "bobsite.example.com")
		wantKind(t, err, Conflict)
		if err.Error() != "The server's public address is not configured (an admin can enter it in the panel settings)" {
			t.Fatalf("message %q", err)
		}
		_, err = d.svc.DomainStatuses(ctx, bob, "bobsite")
		wantKind(t, err, Conflict)
	})

	t.Run("domain of another site", func(t *testing.T) {
		d := newDNSWorld(t)
		_, err := d.svc.PointDomain(ctx, bob, "bobsite", "bobother.example.com")
		wantKind(t, err, NotFound)
	})

	t.Run("read-only provider token", func(t *testing.T) {
		d := newDNSWorld(t)
		d.hz[bobToken].ReadOnlyToken = true
		_, err := d.svc.PointDomain(ctx, bob, "bobsite", "bobsite.example.com")
		wantKind(t, err, Conflict)
		if c := d.conn(d.bobConn); c.CanWrite == nil || *c.CanWrite {
			t.Fatalf("%+v", c)
		}
	})
}

func TestDomainStatuses(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	if _, err := d.svc.SaveSite(ctx, bob, "bobsite", []string{"bobsite.example.com", "away.example.com", "gone.other.org", "broken.example.org"}); err != nil {
		t.Fatal(err)
	}
	d.res.answers["bobsite.example.com"] = []netip.Addr{serverV4, serverV6}
	d.res.answers["away.example.com"] = []netip.Addr{serverV4, netip.MustParseAddr("198.51.100.1")}
	d.res.errs["broken.example.org"] = errors.New("server misbehaving")

	list, err := d.svc.DomainStatuses(ctx, bob, "bobsite")
	if err != nil {
		t.Fatal(err)
	}
	want := []DomainStatus{
		{Domain: "away.example.com", State: DomainPointsElsewhere, Found: []string{"198.51.100.1"},
			Zone: &DomainZone{ConnectionID: d.bobConn, ConnectionName: "bob-main", Zone: "example.com", RecordName: "away"}},
		{Domain: "bobsite.example.com", State: DomainPointsHere, Found: []string{"2001:db8::5", "203.0.113.5"},
			Zone: &DomainZone{ConnectionID: d.bobConn, ConnectionName: "bob-main", Zone: "example.com", RecordName: "bobsite"}},
		{Domain: "broken.example.org", State: DomainLookupFailed, Found: []string{}},
		{Domain: "gone.other.org", State: DomainNoRecord, Found: []string{},
			Zone: &DomainZone{ConnectionID: d.bobConn, ConnectionName: "bob-main", Zone: "other.org", RecordName: "gone"}},
	}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("got  %+v\nwant %+v", list, want)
	}
	// Lookups are reused for 30 s.
	calls := d.res.count()
	d.now = d.now.Add(29 * time.Second)
	d.svc.DomainStatuses(ctx, bob, "bobsite")
	if d.res.count() != calls {
		t.Fatal("looked up again within 30 s")
	}
	d.now = d.now.Add(time.Second)
	d.svc.DomainStatuses(ctx, bob, "bobsite")
	if d.res.count() != calls+4 {
		t.Fatalf("%d lookups after 30 s, want %d", d.res.count(), calls+4)
	}
	// The admin sees the status, with the admin's own zone.
	list, err = d.svc.DomainStatuses(ctx, admin, "bobsite")
	if err != nil || list[1].Zone == nil || list[1].Zone.ConnectionID != d.rootConn {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestDNSSecretsNeverRecorded(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	long := "google-site-verification=" + strings.Repeat("x", 60)
	if _, err := d.svc.SetDNSRecord(ctx, bob, d.bobConn, "example.com", "@", "TXT", nil, []string{long, "short"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.PointDomain(ctx, bob, "bobsite", "bobsite.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := d.svc.DeleteDNSRecord(ctx, bob, d.bobConn, "example.com", "@", "TXT"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.CreateDNSConnection(ctx, bob, "hetzner", "bad", "hz-wrong-token-0123456789"); err == nil {
		t.Fatal("a wrong token must be refused")
	}
	if err := d.svc.DeleteDNSConnection(ctx, bob, d.bobConn); err != nil {
		t.Fatal(err)
	}
	acts, err := d.svc.store.Activities(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range acts {
		seen[a.Action] = true
		row := a.Actor + a.Action + a.Target + a.Detail
		for _, secret := range []string{rootToken, bobToken, "hz-wrong-token-0123456789", strings.Repeat("x", 60)} {
			if strings.Contains(row, secret) {
				t.Fatalf("activity %+v contains a secret", a)
			}
		}
	}
	for _, want := range []string{"dns.connect", "dns.disconnect", "dns.record.set", "dns.record.delete", "dns.point"} {
		if !seen[want] {
			t.Errorf("no %s recorded", want)
		}
	}
	for _, a := range acts {
		if a.Action == "dns.record.set" && !strings.Contains(a.Detail, `"short"`) {
			t.Errorf("short TXT values are recorded: %q", a.Detail)
		}
	}
}

func TestParseServerIPs(t *testing.T) {
	for _, tc := range []struct {
		in        []string
		nonPublic bool
		want      string // joined by space; "!" = error
	}{
		{[]string{"95.216.1.2"}, false, "95.216.1.2"},
		{[]string{"2a01:4f9:c010::1, 95.216.1.2", "95.216.1.2"}, false, "95.216.1.2 2a01:4f9:c010::1"},
		{[]string{"::ffff:95.216.1.2"}, false, "95.216.1.2"},
		{nil, false, ""},
		{[]string{" , "}, false, ""},
		{[]string{"10.0.0.1"}, false, "!"},
		{[]string{"127.0.0.1"}, false, "!"},
		{[]string{"203.0.113.5"}, false, "!"},
		{[]string{"100.64.0.1"}, false, "!"},
		{[]string{"fd00::1"}, false, "!"},
		{[]string{"fe80::1"}, false, "!"},
		{[]string{"fe80::1%eth0"}, true, "!"},
		{[]string{"example.com"}, false, "!"},
		{[]string{"0.0.0.0"}, true, "!"},
		{[]string{"ff02::1"}, true, "!"},
		{[]string{"127.0.0.1,203.0.113.5"}, true, "127.0.0.1 203.0.113.5"},
	} {
		got, err := ParseServerIPs(tc.in, tc.nonPublic)
		var s []string
		for _, ip := range got {
			s = append(s, ip.String())
		}
		joined := strings.Join(s, " ")
		if err != nil {
			joined = "!"
		}
		if joined != tc.want {
			t.Errorf("%q (non-public %v): got %q (%v), want %q", tc.in, tc.nonPublic, joined, err, tc.want)
		}
	}
}

// A status looked up before the record exists must not be served from the
// cache after "point here" wrote it.
func TestPointHereDropsCachedStatus(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	if _, err := d.svc.SaveSite(ctx, bob, "bobsite", []string{"bobsite.example.com"}); err != nil {
		t.Fatal(err)
	}
	list, err := d.svc.DomainStatuses(ctx, bob, "bobsite")
	if err != nil || len(list) != 1 || list[0].State != DomainNoRecord {
		t.Fatalf("%+v %v", list, err)
	}
	if _, err := d.svc.PointDomain(ctx, bob, "bobsite", "bobsite.example.com"); err != nil {
		t.Fatal(err)
	}
	// The record is now visible to the lookup; no time has passed.
	d.res.answers["bobsite.example.com"] = []netip.Addr{serverV4, serverV6}
	list, err = d.svc.DomainStatuses(ctx, bob, "bobsite")
	if err != nil || len(list) != 1 || list[0].State != DomainPointsHere {
		t.Fatalf("%+v %v", list, err)
	}
}

// zoneNames lists "connection/zone" of a zone list.
func zoneNames(t *testing.T, z DNSZones, err error) []string {
	t.Helper()
	if err != nil || len(z.Problems) != 0 {
		t.Fatalf("%+v %v", z, err)
	}
	out := []string{}
	for _, x := range z.Zones {
		out = append(out, x.ConnectionName+"/"+x.Name)
	}
	return out
}

func TestChosenDNSZones(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	inPanel := func() []string { z, err := d.svc.DNSZones(ctx, bob); return zoneNames(t, z, err) }
	available := func() []string { z, err := d.svc.AvailableDNSZones(ctx, bob); return zoneNames(t, z, err) }
	want := func(what string, got []string, want ...string) {
		t.Helper()
		if want == nil {
			want = []string{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %v, want %v", what, got, want)
		}
	}

	// Connecting adopts nothing; example.com is in because sites use it.
	if _, err := d.svc.CreateDNSConnection(ctx, bob, "hetzner", "second", newToken); err != nil {
		t.Fatal(err)
	}
	want("in the panel", inPanel(), "bob-main/example.com")
	want("available", available(), "second/new.example", "bob-main/other.org")
	z, _ := d.svc.DNSZones(ctx, bob)
	// Only bob's own sites count; root's site in the zone is left out.
	if used := z.Zones[0].UsedBy; z.Zones[0].Adopted || !reflect.DeepEqual(used,
		[]string{"the site bobother (bobother.example.com)", "the site bobsite (bobsite.example.com)"}) {
		t.Fatalf("example.com: %+v", z.Zones[0])
	}

	// Adopting brings a zone in, once; nothing changes at the provider.
	srv := d.hz[bobToken]
	srv.ResetCalls()
	for range 2 {
		got, err := d.svc.AdoptDNSZone(ctx, bob, d.bobConn, " Other.ORG. ")
		if err != nil || got.Name != "other.org" || !got.Adopted || len(got.UsedBy) != 0 {
			t.Fatalf("adopt: %+v %v", got, err)
		}
	}
	for _, c := range srv.Calls() {
		if !strings.HasPrefix(c, "GET ") {
			t.Fatalf("adopting changed something at the provider: %v", srv.Calls())
		}
	}
	want("after adopting", inPanel(), "bob-main/example.com", "bob-main/other.org")
	want("available after adopting", available(), "second/new.example")
	if _, err := d.svc.AdoptDNSZone(ctx, bob, d.bobConn, "missing.org"); KindOf(err) != NotFound {
		t.Fatalf("unknown zone: %v", err)
	}

	// Use keeps a zone in without adopting it: e-mail, redirects and the panel itself.
	d.svc.mx = &fakeMX{hosts: map[string][]string{}, errs: map[string]error{}}
	if _, err := d.svc.UpdateSettings(ctx, admin, SettingsUpdate{MailHost: strp(testMailHost)}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.EnableMailDomain(ctx, bob, "shop.new.example"); err != nil {
		t.Fatal(err)
	}
	want("with e-mail", inPanel(), "bob-main/example.com", "second/new.example", "bob-main/other.org")
	if err := d.svc.ReleaseDNSZone(ctx, bob, d.bobConn, "example.com"); KindOf(err) != Conflict ||
		!strings.Contains(err.Error(), "stays in the panel while it is used by the site bobother") {
		t.Fatalf("release a used zone: %v", err)
	}
	z, _ = d.svc.DNSZones(ctx, bob)
	if used := z.Zones[1].UsedBy; z.Zones[1].Name != "new.example" || !reflect.DeepEqual(used, []string{"e-mail for shop.new.example"}) {
		t.Fatalf("new.example: %+v", z.Zones[1])
	}

	// Releasing takes an adopted zone out; it is available again.
	if err := d.svc.ReleaseDNSZone(ctx, bob, d.bobConn, "other.org"); err != nil {
		t.Fatal(err)
	}
	if err := d.svc.ReleaseDNSZone(ctx, bob, d.bobConn, "other.org"); KindOf(err) != NotFound {
		t.Fatalf("release twice: %v", err)
	}
	want("after releasing", available(), "bob-main/other.org")
	if !srv.HasZone("other.org") {
		t.Fatal("releasing deleted the zone at the provider")
	}

	// Another person's things in a zone don't bring it into bob's panel, but
	// they still keep it from being deleted at the provider.
	if _, err := d.svc.SaveSite(ctx, admin, "rootother", []string{"blog.other.org"}); err != nil {
		t.Fatal(err)
	}
	want("with another person's site", available(), "bob-main/other.org")
	if err := d.svc.DeleteDNSZone(ctx, bob, d.bobConn, "other.org", "other.org"); KindOf(err) != Conflict ||
		!strings.Contains(err.Error(), "a site of another person (blog.other.org)") {
		t.Fatalf("delete a zone another person uses: %v", err)
	}
	if err := d.svc.DeleteSite(ctx, admin, "rootother"); err != nil {
		t.Fatal(err)
	}

	// The panel's own address counts for administrators only.
	d.svc.settings.panelDomain = "panel.other.org"
	want("with the panel domain", available(), "bob-main/other.org")
	rootSecond, err := d.svc.CreateDNSConnection(ctx, admin, "hetzner", "root-second", bobToken)
	if err != nil {
		t.Fatal(err)
	}
	rz, err := d.svc.DNSZones(ctx, admin)
	if names := zoneNames(t, rz, err); !slices.Contains(names, "root-second/other.org") {
		t.Fatalf("panel domain for an administrator: %v", names)
	}
	if err := d.svc.DeleteDNSConnection(ctx, admin, rootSecond.ID); err != nil {
		t.Fatal(err)
	}
	d.svc.settings.panelDomain = ""

	// A created zone is adopted; a deleted one is forgotten.
	created, err := d.svc.CreateDNSZone(ctx, bob, d.bobConn, "fresh.org")
	if err != nil || !created.Adopted {
		t.Fatalf("create: %+v %v", created, err)
	}
	want("after creating", inPanel(), "bob-main/example.com", "bob-main/fresh.org", "second/new.example")
	if err := d.svc.DeleteDNSZone(ctx, bob, d.bobConn, "fresh.org", "fresh.org"); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.svc.store.AdoptedDNSZones(ctx, bob.UserID); len(list) != 0 {
		t.Fatalf("deleted zone still adopted: %+v", list)
	}

	// Someone else's adoption does not count for bob, and the root sees only its own.
	if _, err := d.svc.AdoptDNSZone(ctx, admin, d.rootConn, "example.com"); err != nil {
		t.Fatal(err)
	}
	rz, err = d.svc.DNSZones(ctx, admin)
	want("root's zones", zoneNames(t, rz, err), "root-main/example.com")

	acts, err := d.svc.store.Activities(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, a := range acts {
		if strings.HasPrefix(a.Action, "dns.zone.") {
			seen = append(seen, a.Action+" "+a.Target)
		}
	}
	slices.Reverse(seen)
	want("activity", seen, "dns.zone.adopt other.org", "dns.zone.release other.org", "dns.zone.create fresh.org",
		"dns.zone.delete fresh.org", "dns.zone.adopt example.com")
}
