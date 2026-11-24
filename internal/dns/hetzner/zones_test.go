package hetzner_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner/hetznertest"
)

var wantNS = []string{"hydrogen.ns.hetzner.com", "oxygen.ns.hetzner.com", "helium.ns.hetzner.de"}

func TestCreateZone(t *testing.T) {
	srv, c := setup(t)
	srv.ActionPolls = 2
	ctx := context.Background()
	z, err := c.CreateZone(ctx, "Fresh.COM.")
	if err != nil {
		t.Fatal(err)
	}
	if z.Name != "fresh.com" || z.ReadOnly || z.ID == "" || !reflect.DeepEqual(z.Nameservers, wantNS) {
		t.Fatalf("zone %+v", z)
	}
	eq(t, srv.Calls(), []string{"POST /zones", "GET /actions/1", "GET /actions/1", "GET /actions/1"})
	if !srv.HasZone("fresh.com") {
		t.Fatal("zone not created")
	}
	// The list carries the nameservers too.
	zones, err := c.Zones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, z := range zones {
		if !reflect.DeepEqual(z.Nameservers, wantNS) {
			t.Fatalf("listed zone %+v", z)
		}
	}

	if _, err := c.CreateZone(ctx, "fresh.com"); !errors.Is(err, dns.ErrExists) {
		t.Fatalf("existing zone: %v", err)
	}
	for _, name := range []string{"www.fresh.com", "com", "co.uk", "", "localhost", "fresh.invalidtld"} {
		srv.ResetCalls()
		if _, err := c.CreateZone(ctx, name); !errors.Is(err, dns.ErrInvalid) {
			t.Errorf("%q: %v", name, err)
		}
		if len(srv.Calls()) != 0 {
			t.Errorf("%q was sent to Hetzner", name)
		}
	}
	if _, err := c.CreateZone(ctx, "example.co.uk"); err != nil {
		t.Fatalf("example.co.uk: %v", err)
	}

	srv.Fail("POST /zones", 1, hetznertest.Error{Status: 403, Code: "resource_limit_exceeded", Message: "project limit exceeded"})
	if _, err := c.CreateZone(ctx, "more.com"); !errors.Is(err, dns.ErrInvalid) || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("limit: %v", err)
	}
	srv.ActionFailure = &hetznertest.Error{Code: "action_failed", Message: "boom"}
	if _, err := c.CreateZone(ctx, "failing.com"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failed action: %v", err)
	}
}

func TestCreateZoneReadOnlyToken(t *testing.T) {
	srv, c := setup(t)
	srv.ReadOnlyToken = true
	if _, err := c.CreateZone(context.Background(), "fresh.com"); !errors.Is(err, dns.ErrForbidden) {
		t.Fatalf("read-only token: %v", err)
	}
}

func TestDeleteZone(t *testing.T) {
	srv, c := setup(t)
	srv.ActionPolls = 1
	ctx := context.Background()
	if err := c.DeleteZone(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	eq(t, srv.Calls(), []string{"GET /zones/example.com", "DELETE /zones/example.com", "GET /actions/1", "GET /actions/1"})
	if srv.HasZone("example.com") {
		t.Fatal("zone still there")
	}
	if err := c.DeleteZone(ctx, "example.com"); !errors.Is(err, dns.ErrNotFound) {
		t.Fatalf("missing zone: %v", err)
	}

	srv.AddZone("locked.com", "primary")
	srv.ProtectZone("locked.com", true)
	srv.ResetCalls()
	if err := c.DeleteZone(ctx, "locked.com"); !errors.Is(err, dns.ErrLocked) {
		t.Fatalf("protected zone: %v", err)
	}
	eq(t, srv.Writes(), nil)
	// Protected between the read and the delete: Hetzner's 423 protected.
	srv.Fail("GET /zones/locked.com", 1, hetznertest.Error{Status: 404, Code: "not_found", Message: "x"})
	if err := c.DeleteZone(ctx, "locked.com"); !errors.Is(err, dns.ErrNotFound) {
		t.Fatalf("read failure: %v", err)
	}
	srv.ProtectZone("locked.com", false)
	srv.Fail("DELETE /zones/locked.com", 1, hetznertest.Error{Status: 423, Code: "protected", Message: "protected"})
	if err := c.DeleteZone(ctx, "locked.com"); !errors.Is(err, dns.ErrLocked) {
		t.Fatalf("423 protected: %v", err)
	}
	if err := c.DeleteZone(ctx, "locked.com"); err != nil {
		t.Fatal(err)
	}
}
