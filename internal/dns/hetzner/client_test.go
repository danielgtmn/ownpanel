package hetzner_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner/hetznertest"
)

const token = "s3cr3t-token-value"

func setup(t *testing.T) (*hetznertest.Server, *hetzner.Client) {
	t.Helper()
	srv := hetznertest.New(t, token)
	srv.AddZone("example.com", "primary")
	return srv, newClient(srv, token)
}

func newClient(srv *hetznertest.Server, tok string) *hetzner.Client {
	return hetzner.New(tok,
		hetzner.WithBaseURL(srv.BaseURL()),
		hetzner.WithPollIntervals(time.Millisecond, 2*time.Millisecond),
		hetzner.WithRetryDelay(time.Millisecond),
	)
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls:\n got %q\nwant %q", got, want)
	}
}

func TestZonesPagination(t *testing.T) {
	srv := hetznertest.New(t, token)
	for i := 0; i < 120; i++ {
		mode := "primary"
		if i == 7 {
			mode = "secondary"
		}
		srv.AddZone(fmt.Sprintf("Zone%03d.example", i), mode)
	}
	zones, err := newClient(srv, token).Zones(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 120 {
		t.Fatalf("got %d zones, want 120", len(zones))
	}
	eq(t, srv.Calls(), []string{"GET /zones", "GET /zones", "GET /zones"})
	if zones[0].Name != "zone000.example" || zones[0].ID != "1001" || zones[0].ReadOnly {
		t.Errorf("zone 0: %+v", zones[0])
	}
	if !zones[7].ReadOnly || zones[8].ReadOnly {
		t.Errorf("secondary must be read-only: %+v %+v", zones[7], zones[8])
	}
}

func TestRecordSetsPaginationAndConversion(t *testing.T) {
	srv, c := setup(t)
	for i := 0; i < 55; i++ {
		srv.AddRRSet("example.com", hetznertest.RRSet{Name: fmt.Sprintf("h%02d", i), Type: "A", TTL: 300, Records: []string{"198.51.100.1"}})
	}
	srv.AddRRSet("example.com", hetznertest.RRSet{Name: "@", Type: "TXT", Records: []string{`"v=spf1" " -all"`, `"a\"b"`}, Protected: true})
	rs, err := c.RecordSets(context.Background(), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 56 {
		t.Fatalf("got %d record sets", len(rs))
	}
	eq(t, srv.Calls(), []string{"GET /zones/example.com/rrsets", "GET /zones/example.com/rrsets"})
	var txt dns.RecordSet
	for _, r := range rs {
		if r.Type == "TXT" {
			txt = r
		}
	}
	want := dns.RecordSet{Name: "@", Type: "TXT", TTL: 0, Values: []string{"v=spf1 -all", `a"b`}, Locked: true}
	if !reflect.DeepEqual(txt, want) {
		t.Errorf("got %+v, want %+v", txt, want)
	}
}

func TestRecordSetsSecondaryZone(t *testing.T) {
	srv := hetznertest.New(t, token)
	srv.AddZone("sec.example", "secondary")
	_, err := newClient(srv, token).RecordSets(context.Background(), "sec.example")
	if !errors.Is(err, dns.ErrReadOnlyZone) {
		t.Fatalf("got %v", err)
	}
}

func TestSetRecordSetCreate(t *testing.T) {
	srv, c := setup(t)
	srv.ActionPolls = 2
	err := c.SetRecordSet(context.Background(), "example.com", dns.RecordSet{Name: "WWW", Type: "a", TTL: 300, Values: []string{"198.51.100.1", "198.51.100.1", "198.51.100.2"}})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, srv.Calls(), []string{
		"GET /zones/example.com/rrsets/www/A",
		"POST /zones/example.com/rrsets",
		"GET /actions/1", "GET /actions/1", "GET /actions/1",
	})
	got, _ := srv.RRSet("example.com", "www", "A")
	if got.TTL != 300 || !reflect.DeepEqual(got.Records, []string{"198.51.100.1", "198.51.100.2"}) {
		t.Errorf("stored %+v", got)
	}
}

func TestSetRecordSetCreateDefaultTTLApex(t *testing.T) {
	srv, c := setup(t)
	if err := c.SetRecordSet(context.Background(), "example.com", dns.RecordSet{Name: "@", Type: "AAAA", Values: []string{"2001:db8::1"}}); err != nil {
		t.Fatal(err)
	}
	eq(t, srv.Calls(), []string{"GET /zones/example.com/rrsets/@/AAAA", "POST /zones/example.com/rrsets"})
	if got, _ := srv.RRSet("example.com", "@", "AAAA"); got.TTL != 0 {
		t.Errorf("ttl %d, want zone default", got.TTL)
	}
}

func TestSetRecordSetReplaceNoopAndTTL(t *testing.T) {
	srv, c := setup(t)
	srv.AddRRSet("example.com", hetznertest.RRSet{Name: "www", Type: "A", TTL: 300, Records: []string{"198.51.100.1", "198.51.100.2"}})
	ctx := context.Background()
	get := "GET /zones/example.com/rrsets/www/A"
	setRecords := "POST /zones/example.com/rrsets/www/A/actions/set_records"
	changeTTL := "POST /zones/example.com/rrsets/www/A/actions/change_ttl"

	// Identical (order differs): zero writes.
	if err := c.SetRecordSet(ctx, "example.com", dns.RecordSet{Name: "www", Type: "A", TTL: 300, Values: []string{"198.51.100.2", "198.51.100.1"}}); err != nil {
		t.Fatal(err)
	}
	eq(t, srv.Calls(), []string{get})
	srv.ResetCalls()

	// Only values differ.
	if err := c.SetRecordSet(ctx, "example.com", dns.RecordSet{Name: "www", Type: "A", TTL: 300, Values: []string{"203.0.113.9"}}); err != nil {
		t.Fatal(err)
	}
	eq(t, srv.Calls(), []string{get, setRecords})
	srv.ResetCalls()

	// Only TTL differs.
	if err := c.SetRecordSet(ctx, "example.com", dns.RecordSet{Name: "www", Type: "A", TTL: 60, Values: []string{"203.0.113.9"}}); err != nil {
		t.Fatal(err)
	}
	eq(t, srv.Calls(), []string{get, changeTTL})
	if got, _ := srv.RRSet("example.com", "www", "A"); got.TTL != 60 {
		t.Errorf("ttl %d", got.TTL)
	}
	srv.ResetCalls()

	// TTL back to the zone default (null) and values change: set_records first.
	if err := c.SetRecordSet(ctx, "example.com", dns.RecordSet{Name: "www", Type: "A", Values: []string{"203.0.113.10"}}); err != nil {
		t.Fatal(err)
	}
	eq(t, srv.Calls(), []string{get, setRecords, changeTTL})
	if got, _ := srv.RRSet("example.com", "www", "A"); got.TTL != 0 || got.Records[0] != "203.0.113.10" {
		t.Errorf("stored %+v", got)
	}
	srv.ResetCalls()

	// Default TTL again: no-op.
	if err := c.SetRecordSet(ctx, "example.com", dns.RecordSet{Name: "www", Type: "A", Values: []string{"203.0.113.10"}}); err != nil {
		t.Fatal(err)
	}
	eq(t, srv.Calls(), []string{get})
}

func TestSetRecordSetIPv6Canonical(t *testing.T) {
	srv, c := setup(t)
	srv.AddRRSet("example.com", hetznertest.RRSet{Name: "@", Type: "AAAA", Records: []string{"2001:db8::1"}})
	if err := c.SetRecordSet(context.Background(), "example.com", dns.RecordSet{Name: "@", Type: "AAAA", Values: []string{"2001:0db8:0:0:0:0:0:1"}}); err != nil {
		t.Fatal(err)
	}
	if w := srv.Writes(); len(w) != 0 {
		t.Errorf("unexpected writes %q", w)
	}
}

func TestSetRecordSetProtected(t *testing.T) {
	srv, c := setup(t)
	srv.AddRRSet("example.com", hetznertest.RRSet{Name: "www", Type: "A", Records: []string{"198.51.100.1"}, Protected: true})
	err := c.SetRecordSet(context.Background(), "example.com", dns.RecordSet{Name: "www", Type: "A", Values: []string{"203.0.113.9"}})
	if !errors.Is(err, dns.ErrLocked) {
		t.Fatalf("got %v", err)
	}
	if w := srv.Writes(); len(w) != 0 {
		t.Errorf("unexpected writes %q", w)
	}
	// Unchanged protected set is fine.
	if err := c.SetRecordSet(context.Background(), "example.com", dns.RecordSet{Name: "www", Type: "A", Values: []string{"198.51.100.1"}}); err != nil {
		t.Errorf("no-op on protected set: %v", err)
	}
}

func TestSetRecordSetValidation(t *testing.T) {
	srv, c := setup(t)
	bad := []dns.RecordSet{
		{Name: "bücher", Type: "A", Values: []string{"198.51.100.1"}},
		{Name: "www", Type: "A"},
		{Name: "www", Type: "A", Values: []string{" "}},
		{Name: "www", Type: "A", TTL: -1, Values: []string{"198.51.100.1"}},
		{Name: "a/b", Type: "A", Values: []string{"198.51.100.1"}},
	}
	for _, rs := range bad {
		err := c.SetRecordSet(context.Background(), "example.com", rs)
		if !errors.Is(err, dns.ErrInvalid) {
			t.Errorf("%+v: got %v", rs, err)
		}
	}
	err := c.SetRecordSet(context.Background(), "example.com", dns.RecordSet{Name: "bücher", Type: "A", Values: []string{"198.51.100.1"}})
	if err == nil || !strings.Contains(err.Error(), "punycode") {
		t.Errorf("message should mention punycode: %v", err)
	}
	if n := len(srv.Calls()); n != 0 {
		t.Errorf("validation must not call the API, got %d calls", n)
	}
}

func TestTXTEndToEnd(t *testing.T) {
	srv, c := setup(t)
	ctx := context.Background()
	values := []string{"v=spf1 include:example.net -all", `quote " and \ backslash`, strings.Repeat("k", 600)}
	if err := c.SetRecordSet(ctx, "example.com", dns.RecordSet{Name: "@", Type: "TXT", TTL: 3600, Values: values}); err != nil {
		t.Fatal(err)
	}
	stored, _ := srv.RRSet("example.com", "@", "TXT")
	if stored.Records[0] != `"v=spf1 include:example.net -all"` || stored.Records[1] != `"quote \" and \\ backslash"` {
		t.Errorf("wire values: %q", stored.Records)
	}
	if n := strings.Count(stored.Records[2], `" "`); n != 2 {
		t.Errorf("600 bytes should be 3 sub-strings, got %d separators", n)
	}
	rs, err := c.RecordSets(ctx, "example.com")
	if err != nil || len(rs) != 1 || !reflect.DeepEqual(rs[0].Values, values) {
		t.Fatalf("read back: %+v %v", rs, err)
	}
	// Setting the same plain values again must be a no-op.
	srv.ResetCalls()
	if err := c.SetRecordSet(ctx, "example.com", dns.RecordSet{Name: "@", Type: "TXT", TTL: 3600, Values: values}); err != nil {
		t.Fatal(err)
	}
	if w := srv.Writes(); len(w) != 0 {
		t.Errorf("unexpected writes %q", w)
	}
}

func TestDelete(t *testing.T) {
	srv, c := setup(t)
	srv.AddRRSet("example.com", hetznertest.RRSet{Name: "www", Type: "A", Records: []string{"198.51.100.1"}})
	srv.ActionPolls = 1
	ctx := context.Background()
	if err := c.DeleteRecordSet(ctx, "example.com", "www", "A"); err != nil {
		t.Fatal(err)
	}
	eq(t, srv.Calls(), []string{"DELETE /zones/example.com/rrsets/www/A", "GET /actions/1", "GET /actions/1"})
	if _, ok := srv.RRSet("example.com", "www", "A"); ok {
		t.Error("record set still there")
	}
	srv.ResetCalls()
	if err := c.DeleteRecordSet(ctx, "example.com", "www", "A"); err != nil {
		t.Fatalf("missing record set must give nil, got %v", err)
	}
	eq(t, srv.Calls(), []string{"DELETE /zones/example.com/rrsets/www/A"})
}

func TestDeleteProtected(t *testing.T) {
	srv, c := setup(t)
	srv.AddRRSet("example.com", hetznertest.RRSet{Name: "www", Type: "A", Records: []string{"198.51.100.1"}, Protected: true})
	err := c.DeleteRecordSet(context.Background(), "example.com", "www", "A")
	if !errors.Is(err, dns.ErrLocked) {
		t.Fatalf("got %v", err)
	}
}

func TestActionEndsInError(t *testing.T) {
	srv, c := setup(t)
	srv.ActionPolls = 1
	srv.ActionFailure = &hetznertest.Error{Code: "server_error", Message: "zone could not be updated"}
	err := c.SetRecordSet(context.Background(), "example.com", dns.RecordSet{Name: "www", Type: "A", Values: []string{"198.51.100.1"}})
	if err == nil || !strings.Contains(err.Error(), "zone could not be updated") || !strings.Contains(err.Error(), "server_error") {
		t.Fatalf("got %v", err)
	}
}

func TestActionErrorImmediately(t *testing.T) {
	srv, c := setup(t)
	srv.ActionFailure = &hetznertest.Error{Code: "server_error", Message: "nope"}
	err := c.SetRecordSet(context.Background(), "example.com", dns.RecordSet{Name: "www", Type: "A", Values: []string{"198.51.100.1"}})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("got %v", err)
	}
}

func TestPollingHonoursContext(t *testing.T) {
	srv := hetznertest.New(t, token)
	srv.AddZone("example.com", "primary")
	srv.ActionPolls = 1 << 30
	c := hetzner.New(token, hetzner.WithBaseURL(srv.BaseURL()), hetzner.WithPollIntervals(5*time.Millisecond, 5*time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.SetRecordSet(ctx, "example.com", dns.RecordSet{Name: "www", Type: "A", Values: []string{"198.51.100.1"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("did not stop promptly")
	}

	// Explicit cancel while sleeping between polls.
	c = hetzner.New(token, hetzner.WithBaseURL(srv.BaseURL()), hetzner.WithPollIntervals(time.Hour, time.Hour))
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel2() }()
	err = c.SetRecordSet(ctx2, "example.com", dns.RecordSet{Name: "www2", Type: "A", Values: []string{"198.51.100.1"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		e        hetznertest.Error
		want     error
		contains string
	}{
		{"401", hetznertest.Error{Status: 401, Code: "unauthorized", Message: "bad key"}, dns.ErrUnauthorized, "bad key"},
		{"403", hetznertest.Error{Status: 403, Code: "forbidden", Message: "read only"}, dns.ErrForbidden, "read only"},
		{"404", hetznertest.Error{Status: 404, Code: "not_found", Message: "zone gone"}, dns.ErrNotFound, "zone gone"},
		{"zone mode", hetznertest.Error{Status: 422, Code: "incorrect_zone_mode", Message: "not primary"}, dns.ErrReadOnlyZone, "not primary"},
		{"invalid", hetznertest.Error{Status: 422, Code: "invalid_input", Message: "invalid input", Fields: []hetznertest.Field{{Name: "records[0].value", Messages: []string{"not an IPv4 address"}}}}, dns.ErrInvalid, "records[0].value: not an IPv4 address"},
		{"protected", hetznertest.Error{Status: 423, Code: "protected", Message: "protected"}, dns.ErrLocked, "protected"},
		{"rate", hetznertest.Error{Status: 429, Code: "rate_limit_exceeded", Message: "slow down"}, dns.ErrRateLimited, "slow down"},
		{"rate far reset", hetznertest.Error{Status: 429, Code: "rate_limit_exceeded", Message: "slow down", Header: http.Header{"Ratelimit-Reset": {"3600"}}}, dns.ErrRateLimited, "slow down"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := setup(t)
			srv.Fail("GET /zones", -1, tc.e)
			_, err := c.Zones(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("%q should contain %q", err, tc.contains)
			}
			if strings.Contains(err.Error(), token) {
				t.Error("token leaked")
			}
			if n := len(srv.Calls()); n != 1 {
				t.Errorf("%d calls, want 1 (no retry)", n)
			}
		})
	}
}

func TestUnknownErrorKeepsMessageAndStatus(t *testing.T) {
	srv, c := setup(t)
	srv.Fail("GET /zones", -1, hetznertest.Error{Status: 500, Code: "server_error", Message: "kaputt"})
	_, err := c.Zones(context.Background())
	if err == nil || !strings.Contains(err.Error(), "kaputt") || !strings.Contains(err.Error(), "500") {
		t.Fatalf("got %v", err)
	}
	for _, s := range []error{dns.ErrUnauthorized, dns.ErrForbidden, dns.ErrNotFound, dns.ErrInvalid} {
		if errors.Is(err, s) {
			t.Errorf("must not map to %v", s)
		}
	}
}

func TestWrongTokenAndReadOnlyToken(t *testing.T) {
	srv, _ := setup(t)
	_, err := newClient(srv, "wrong-token").Zones(context.Background())
	if !errors.Is(err, dns.ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "wrong-token") {
		t.Error("token leaked")
	}
	srv.ReadOnlyToken = true
	c := newClient(srv, token)
	if _, err := c.Zones(context.Background()); err != nil {
		t.Fatal(err)
	}
	err = c.SetRecordSet(context.Background(), "example.com", dns.RecordSet{Name: "www", Type: "A", Values: []string{"198.51.100.1"}})
	if !errors.Is(err, dns.ErrForbidden) {
		t.Fatalf("got %v", err)
	}
}

func TestRetryOnLocked(t *testing.T) {
	post := "POST /zones/example.com/rrsets"
	rs := dns.RecordSet{Name: "www", Type: "A", Values: []string{"198.51.100.1"}}
	locked := hetznertest.Error{Status: 423, Code: "locked", Message: "resource is locked"}

	t.Run("recovers", func(t *testing.T) {
		srv, c := setup(t)
		srv.Fail(post, 2, locked)
		if err := c.SetRecordSet(context.Background(), "example.com", rs); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, call := range srv.Calls() {
			if call == post {
				n++
			}
		}
		if n != 3 {
			t.Errorf("%d POSTs, want 3", n)
		}
	})
	t.Run("gives up", func(t *testing.T) {
		srv, c := setup(t)
		srv.Fail(post, -1, locked)
		err := c.SetRecordSet(context.Background(), "example.com", rs)
		if !errors.Is(err, dns.ErrLocked) {
			t.Fatalf("got %v", err)
		}
		n := 0
		for _, call := range srv.Calls() {
			if call == post {
				n++
			}
		}
		if n != 5 {
			t.Errorf("%d POSTs, want 5 (1 + 4 retries)", n)
		}
	})
	t.Run("conflict", func(t *testing.T) {
		srv, c := setup(t)
		srv.Fail(post, 1, hetznertest.Error{Status: 409, Code: "conflict", Message: "busy"})
		if err := c.SetRecordSet(context.Background(), "example.com", rs); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("conflict gives up", func(t *testing.T) {
		srv, c := setup(t)
		srv.Fail(post, -1, hetznertest.Error{Status: 409, Code: "conflict", Message: "busy"})
		err := c.SetRecordSet(context.Background(), "example.com", rs)
		if err == nil || !strings.Contains(err.Error(), "busy") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("context stops retries", func(t *testing.T) {
		srv := hetznertest.New(t, token)
		srv.AddZone("example.com", "primary")
		srv.Fail(post, -1, locked)
		c := hetzner.New(token, hetzner.WithBaseURL(srv.BaseURL()), hetzner.WithRetryDelay(time.Hour))
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := c.SetRecordSet(ctx, "example.com", rs); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestRateLimitShortResetRetriesOnce(t *testing.T) {
	srv, c := setup(t)
	srv.Fail("GET /zones", 1, hetznertest.Error{Status: 429, Code: "rate_limit_exceeded", Message: "slow", Header: http.Header{"Ratelimit-Reset": {"0"}}})
	if _, err := c.Zones(context.Background()); err != nil {
		t.Fatal(err)
	}
	eq(t, srv.Calls(), []string{"GET /zones", "GET /zones"})

	// Reset given as a UNIX timestamp in the past counts as "now".
	srv.ResetCalls()
	srv.Fail("GET /zones", 1, hetznertest.Error{Status: 429, Code: "rate_limit_exceeded", Message: "slow", Header: http.Header{"Ratelimit-Reset": {"1700000000"}}})
	if _, err := c.Zones(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Only one wait: a second 429 is returned.
	srv.ResetCalls()
	srv.Fail("GET /zones", -1, hetznertest.Error{Status: 429, Code: "rate_limit_exceeded", Message: "slow", Header: http.Header{"Ratelimit-Reset": {"0"}}})
	_, err := c.Zones(context.Background())
	if !errors.Is(err, dns.ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	eq(t, srv.Calls(), []string{"GET /zones", "GET /zones"})
}

func TestTokenNeverInTransportErrors(t *testing.T) {
	srv := hetznertest.New(t, token)
	base := srv.BaseURL()
	srv.Close()
	// A base URL that contains the token makes the transport error mention it.
	c := hetzner.New(token, hetzner.WithBaseURL(base+"/"+token))
	_, err := c.Zones(context.Background())
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("token leaked: %v", err)
	}
}

func TestSendsAuthAndUserAgent(t *testing.T) {
	// The fake rejects wrong Authorization / User-Agent, so success proves both.
	_, c := setup(t)
	if _, err := c.Zones(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProviderInterface(t *testing.T) {
	var _ dns.Provider = hetzner.New("x")
}
