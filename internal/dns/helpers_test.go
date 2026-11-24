package dns

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"testing"
)

func TestFindZone(t *testing.T) {
	zones := []Zone{
		{ID: "1", Name: "example.com"},
		{ID: "2", Name: "ample.com"},
		{ID: "3", Name: "shop.example.com"},
		{ID: "4", Name: "Other.org"},
	}
	tests := []struct {
		domain, zoneID, name string
		ok                   bool
	}{
		{"example.com", "1", "@", true},
		{"www.example.com", "1", "www", true},
		{"a.b.example.com", "1", "a.b", true},
		{"WWW.Example.COM.", "1", "www", true},
		{"shop.example.com", "3", "@", true},
		{"www.shop.example.com", "3", "www", true},
		{"example.com.evil.net", "", "", false},
		{"xexample.com", "", "", false},
		{"ample.com", "2", "@", true},
		{"www.other.org", "4", "www", true},
		{"", "", "", false},
		{"com", "", "", false},
	}
	for _, tc := range tests {
		z, name, ok := FindZone(zones, tc.domain)
		if ok != tc.ok || z.ID != tc.zoneID || name != tc.name {
			t.Errorf("FindZone(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.domain, z.ID, name, ok, tc.zoneID, tc.name, tc.ok)
		}
	}
	if _, _, ok := FindZone(nil, "example.com"); ok {
		t.Error("no zones must not match")
	}
}

type fakeResolver struct {
	addrs []netip.Addr
	err   error
	host  string
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.host = host
	return f.addrs, f.err
}

func TestCheckDomain(t *testing.T) {
	a4 := netip.MustParseAddr("198.51.100.1")
	a6 := netip.MustParseAddr("2001:db8::1")
	other := netip.MustParseAddr("203.0.113.9")
	server := []netip.Addr{a4, a6}
	tests := []struct {
		name  string
		r     fakeResolver
		state State
		found []string
	}{
		{"here v4", fakeResolver{addrs: []netip.Addr{a4}}, PointsHere, []string{"198.51.100.1"}},
		{"here both", fakeResolver{addrs: []netip.Addr{a4, a6}}, PointsHere, []string{"198.51.100.1", "2001:db8::1"}},
		{"mapped v4", fakeResolver{addrs: []netip.Addr{netip.AddrFrom16(a4.As16())}}, PointsHere, []string{"198.51.100.1"}},
		{"mixed", fakeResolver{addrs: []netip.Addr{a4, other}}, PointsElsewhere, []string{"203.0.113.9"}},
		{"elsewhere", fakeResolver{addrs: []netip.Addr{other}}, PointsElsewhere, []string{"203.0.113.9"}},
		{"empty", fakeResolver{}, NoRecord, nil},
		{"nxdomain", fakeResolver{err: &net.DNSError{Err: "no such host", IsNotFound: true}}, NoRecord, nil},
		{"timeout", fakeResolver{err: &net.DNSError{Err: "timeout", IsTimeout: true}}, LookupFailed, nil},
		{"other error", fakeResolver{err: errors.New("boom")}, LookupFailed, nil},
	}
	for _, tc := range tests {
		r := tc.r
		c := CheckDomain(context.Background(), &r, "Example.com.", server)
		if c.State != tc.state || !reflect.DeepEqual(c.Found, tc.found) || c.Domain != "Example.com." {
			t.Errorf("%s: got %+v, want state %v found %v", tc.name, c, tc.state, tc.found)
		}
		if r.host != "example.com" {
			t.Errorf("%s: resolved host %q", tc.name, r.host)
		}
	}
}

func TestStateString(t *testing.T) {
	for _, s := range []State{PointsHere, PointsElsewhere, NoRecord, LookupFailed} {
		if s.String() == "unknown" {
			t.Errorf("state %d has no name", s)
		}
	}
}

func TestAddressRecords(t *testing.T) {
	ips := []netip.Addr{
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("198.51.100.2"),
		netip.MustParseAddr("198.51.100.1"),
		netip.MustParseAddr("198.51.100.2"),
	}
	got := AddressRecords("www", ips, 300)
	want := []RecordSet{
		{Name: "www", Type: "A", TTL: 300, Values: []string{"198.51.100.1", "198.51.100.2"}},
		{Name: "www", Type: "AAAA", TTL: 300, Values: []string{"2001:db8::1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := AddressRecords("@", ips[:1], 0); len(got) != 1 || got[0].Type != "AAAA" {
		t.Errorf("v6 only: %+v", got)
	}
	if got := AddressRecords("@", nil, 0); len(got) != 0 {
		t.Errorf("none: %+v", got)
	}
}
