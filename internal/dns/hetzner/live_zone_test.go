//go:build live

// Live test of creating and deleting a zone at the real Hetzner Cloud API.
// It runs only when HETZNER_TEST_NEW_ZONE names a registrable domain that is
// not yet a zone in the project; the zone is deleted again at the end.
//
//	HETZNER_TOKEN=... HETZNER_TEST_NEW_ZONE=some-unused-name.com go test -tags live -run LiveZone -v ./internal/dns/hetzner
package hetzner_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/dns/hetzner"
)

func TestLiveZone(t *testing.T) {
	token, name := os.Getenv("HETZNER_TOKEN"), os.Getenv("HETZNER_TEST_NEW_ZONE")
	if token == "" || name == "" {
		t.Skip("HETZNER_TOKEN and HETZNER_TEST_NEW_ZONE are not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := hetzner.New(token)

	z, err := c.CreateZone(ctx, name)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	deleted := false
	defer func() {
		if !deleted {
			if err := c.DeleteZone(context.Background(), name); err != nil {
				t.Errorf("cleanup: delete %s: %v", name, err)
			}
		}
	}()
	t.Logf("created %s (id %s), nameservers %v", z.Name, z.ID, z.Nameservers)
	if z.Name != name || z.ReadOnly || len(z.Nameservers) == 0 {
		t.Fatalf("created zone %+v", z)
	}

	zones, err := c.Zones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range zones {
		if x.Name == name {
			found = true
			if !slices.Equal(x.Nameservers, z.Nameservers) {
				t.Errorf("listed nameservers %v, created %v", x.Nameservers, z.Nameservers)
			}
		}
	}
	if !found {
		t.Fatalf("%s not in the zone list", name)
	}
	sets, err := c.RecordSets(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("record sets of the new zone: %+v", sets)

	if err := c.DeleteZone(ctx, name); err != nil {
		t.Fatalf("delete: %v", err)
	}
	deleted = true
	zones, err = c.Zones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range zones {
		if x.Name == name {
			t.Fatalf("%s still listed after delete", name)
		}
	}
}
