//go:build live

// Live test against the real Hetzner Cloud API. It creates and removes
// records named _ownpanel-live-test* in the given zone and leaves nothing else
// changed. Run with:
//
//	HETZNER_TOKEN=... HETZNER_TEST_ZONE=example.com go test -tags live -run Live -v ./internal/dns/hetzner
package hetzner_test

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner"
)

func TestLive(t *testing.T) {
	token, zone := os.Getenv("HETZNER_TOKEN"), os.Getenv("HETZNER_TEST_ZONE")
	if token == "" || zone == "" {
		t.Skip("HETZNER_TOKEN and HETZNER_TEST_ZONE are not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := hetzner.New(token)

	zones, err := c.Zones(ctx)
	if err != nil {
		t.Fatalf("zones: %v", err)
	}
	found := false
	for _, z := range zones {
		found = found || z.Name == zone
	}
	if !found {
		t.Fatalf("zone %s not visible to this token (%d zones)", zone, len(zones))
	}

	const txtName, aName = "_ownpanel-live-test", "_ownpanel-live-test-a"
	t.Cleanup(func() {
		ctx := context.Background()
		for _, k := range [][2]string{{txtName, "TXT"}, {aName, "A"}, {aName, "AAAA"}} {
			if err := c.DeleteRecordSet(ctx, zone, k[0], k[1]); err != nil {
				t.Errorf("cleanup %s %s: %v", k[0], k[1], err)
			}
		}
	})

	get := func(name, typ string) (dns.RecordSet, bool) {
		t.Helper()
		sets, err := c.RecordSets(ctx, zone)
		if err != nil {
			t.Fatalf("record sets: %v", err)
		}
		for _, rs := range sets {
			if rs.Name == name && rs.Type == typ {
				return rs, true
			}
		}
		return dns.RecordSet{}, false
	}

	long := strings.Repeat("x", 300) + ` with "quotes" and \backslash`
	txt := dns.RecordSet{Name: txtName, Type: "TXT", TTL: 300, Values: []string{"v=ownpanel1 hello", long}}
	if err := c.SetRecordSet(ctx, zone, txt); err != nil {
		t.Fatalf("create TXT: %v", err)
	}
	got, ok := get(txtName, "TXT")
	if !ok {
		t.Fatal("TXT not found after create")
	}
	slices.Sort(got.Values)
	want := slices.Clone(txt.Values)
	slices.Sort(want)
	if !slices.Equal(got.Values, want) || got.TTL != 300 {
		t.Fatalf("TXT read back as %+v, want %+v", got, txt)
	}

	if err := c.SetRecordSet(ctx, zone, txt); err != nil {
		t.Fatalf("unchanged set: %v", err)
	}

	txt.Values, txt.TTL = []string{"v=ownpanel2"}, 0
	if err := c.SetRecordSet(ctx, zone, txt); err != nil {
		t.Fatalf("replace TXT: %v", err)
	}
	if got, _ := get(txtName, "TXT"); !slices.Equal(got.Values, txt.Values) || got.TTL != 0 {
		t.Fatalf("TXT after replace: %+v", got)
	}

	for _, rs := range dns.AddressRecords(aName, mustAddrs(t, "192.0.2.10", "2001:db8::10"), 300) {
		if err := c.SetRecordSet(ctx, zone, rs); err != nil {
			t.Fatalf("set %s: %v", rs.Type, err)
		}
	}
	if got, _ := get(aName, "AAAA"); !slices.Equal(got.Values, []string{"2001:db8::10"}) {
		t.Fatalf("AAAA: %+v", got)
	}

	if err := c.DeleteRecordSet(ctx, zone, txtName, "TXT"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := get(txtName, "TXT"); ok {
		t.Fatal("TXT still there after delete")
	}
	if err := c.DeleteRecordSet(ctx, zone, txtName, "TXT"); err != nil {
		t.Fatalf("deleting a missing set must be nil: %v", err)
	}

	bad := hetzner.New("not-a-real-token")
	if _, err := bad.Zones(ctx); !errors.Is(err, dns.ErrUnauthorized) {
		t.Fatalf("bad token: %v", err)
	} else if strings.Contains(err.Error(), token) {
		t.Fatal("token leaked into error")
	}
}

func mustAddrs(t *testing.T, ss ...string) []netip.Addr {
	t.Helper()
	var out []netip.Addr
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}
