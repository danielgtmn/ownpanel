package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner/hetznertest"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/panzerschrank"
)

const (
	hzRootToken = "hz-root-token-0123456789abcdef"
	hzBobToken  = "hz-bob-token-0123456789abcdef"
)

// dnsEnv is an env whose panel talks to fake Hetzner projects: root's
// connection holds example.com, bob's (a member) holds bob.example.
type dnsEnv struct {
	*env
	hz      map[string]*hetznertest.Server
	rootDNS int64 // root's connection
	bobDNS  int64 // bob's connection
	bob     string
	bobRead string
	seen    []string // every tool answer, to check that no provider token leaks
}

func newDNSEnv(t *testing.T) *dnsEnv {
	t.Helper()
	d := &dnsEnv{hz: map[string]*hetznertest.Server{}}
	reject := hetznertest.New(t, "never-accepted")
	for token, zone := range map[string]string{hzRootToken: "example.com", hzBobToken: "bob.example"} {
		srv := hetznertest.New(t, token)
		srv.AddZone(zone, "primary")
		d.hz[token] = srv
	}
	d.env = newEnvWith(t, "", func(c *panel.Config) {
		box, err := panzerschrank.OpenKeyFile(filepath.Join(t.TempDir(), panzerschrank.KeyFile))
		if err != nil {
			t.Fatal(err)
		}
		c.Secrets = box
		c.NewDNSProvider = func(_, token string) (dns.Provider, error) {
			srv := d.hz[token]
			if srv == nil {
				srv = reject
			}
			return hetzner.New(token, hetzner.WithBaseURL(srv.BaseURL()),
				hetzner.WithPollIntervals(time.Millisecond, 2*time.Millisecond), hetzner.WithRetryDelay(time.Millisecond)), nil
		}
	})
	ctx := context.Background()
	conn, err := d.svc.CreateDNSConnection(ctx, d.admin, "hetzner", "Main", hzRootToken)
	if err != nil {
		t.Fatal(err)
	}
	d.rootDNS = conn.ID

	if _, err := d.svc.CreateUser(ctx, d.admin, panel.NewUser{Name: "bob", Password: "bob-password-1", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	_, session, err := d.svc.Login(ctx, panel.Credentials{Name: "bob", Password: "bob-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := d.svc.SessionActor(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	conn, err = d.svc.CreateDNSConnection(ctx, bob, "hetzner", "Bob's", hzBobToken)
	if err != nil {
		t.Fatal(err)
	}
	d.bobDNS = conn.ID
	for access, into := range map[string]*string{"write": &d.bob, "read": &d.bobRead} {
		tok, err := d.svc.CreateToken(ctx, bob, "bob-"+access, access, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		*into = tok.Secret
	}
	t.Cleanup(func() {
		for _, s := range d.seen {
			if strings.Contains(s, hzRootToken) || strings.Contains(s, hzBobToken) || strings.Contains(s, "0123456789abcdef") {
				t.Errorf("a provider token leaked into a tool answer: %s", s)
			}
		}
	})
	return d
}

// call runs a tool and keeps its answer for the leak check.
func (d *dnsEnv) call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res := call_(t, cs, name, args)
	b, _ := json.Marshal(res)
	d.seen = append(d.seen, string(b))
	return res
}

func TestDNSRecordTools(t *testing.T) {
	d := newDNSEnv(t)
	root := d.connect(t, d.write)
	zone := map[string]any{"connection_id": d.rootDNS, "domain": "example.com"}
	www := func(extra map[string]any) map[string]any {
		m := map[string]any{"connection_id": d.rootDNS, "domain": "example.com", "name": "www", "type": "A"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	has := func(recs []dnsRecordInfo, name, typ string) *dnsRecordInfo {
		for i, r := range recs {
			if r.Name == name && r.Type == typ {
				return &recs[i]
			}
		}
		return nil
	}

	// Connections: an administrator sees everyone's, never a token.
	var conns dnsConnectionsOutput
	ok(t, d.call(t, root, "list_dns_connections", nil), &conns)
	if len(conns.Connections) != 2 || conns.Connections[0].CreatedAt == "" {
		t.Fatalf("connections %+v", conns)
	}
	var bobConns dnsConnectionsOutput
	ok(t, d.call(t, d.connect(t, d.bob), "list_dns_connections", nil), &bobConns)
	if len(bobConns.Connections) != 1 || bobConns.Connections[0].ID != d.bobDNS || bobConns.Connections[0].Owner != "bob" {
		t.Fatalf("bob's connections %+v", bobConns)
	}

	// list → set → list → delete → list.
	var recs dnsRecordsOutput
	ok(t, d.call(t, root, "list_dns_records", zone), &recs)
	if recs.Records == nil || has(recs.Records, "www", "A") != nil {
		t.Fatalf("records before %+v", recs)
	}
	var set dnsRecordOutput
	ok(t, d.call(t, root, "set_dns_record", www(map[string]any{"ttl": 600, "values": []string{"203.0.113.5", "203.0.113.6"}})), &set)
	if set.Record.Name != "www" || set.Record.Type != "A" || set.Record.TTL == nil || *set.Record.TTL != 600 || len(set.Record.Values) != 2 {
		t.Fatalf("set %+v", set)
	}
	// Without ttl the domain's default is used; values are replaced.
	ok(t, d.call(t, root, "set_dns_record", www(map[string]any{"values": []string{"203.0.113.7"}})), &set)
	if set.Record.TTL != nil || len(set.Record.Values) != 1 {
		t.Fatalf("set again %+v", set)
	}
	ok(t, d.call(t, root, "set_dns_record", map[string]any{
		"connection_id": d.rootDNS, "domain": "example.com", "name": "@", "type": "TXT", "values": []string{"v=spf1 -all"},
	}), &set)
	ok(t, d.call(t, root, "list_dns_records", zone), &recs)
	if r := has(recs.Records, "www", "A"); r == nil || len(r.Values) != 1 || r.Values[0] != "203.0.113.7" {
		t.Fatalf("records after set %+v", recs)
	}
	if r := has(recs.Records, "@", "TXT"); r == nil || r.Values[0] != "v=spf1 -all" {
		t.Fatalf("TXT %+v", recs)
	}
	if recs.Records[0].Name != "@" {
		t.Fatalf("@ is not first: %+v", recs)
	}
	var del deletedDNSRecordOutput
	ok(t, d.call(t, root, "delete_dns_record", www(nil)), &del)
	if del.Deleted != "www A in example.com" {
		t.Fatalf("deleted %+v", del)
	}
	ok(t, d.call(t, root, "delete_dns_record", www(nil)), &del) // already gone: fine
	ok(t, d.call(t, root, "list_dns_records", zone), &recs)
	if has(recs.Records, "www", "A") != nil {
		t.Fatalf("records after delete %+v", recs)
	}

	// Mistakes read like sentences.
	refused(t, d.call(t, root, "set_dns_record", www(map[string]any{"values": []string{"not-an-address"}})), "is not an IP address")
	refused(t, d.call(t, root, "list_dns_records", map[string]any{"connection_id": d.rootDNS, "domain": "other.example"}), "not part of the DNS connection")
	refused(t, d.call(t, root, "list_dns_records", map[string]any{"connection_id": 9999, "domain": "example.com"}), "not found")

	// A read-only token reads but never writes.
	ro := d.connect(t, d.read)
	ok(t, d.call(t, ro, "list_dns_records", zone), &recs)
	refused(t, d.call(t, ro, "set_dns_record", www(map[string]any{"values": []string{"203.0.113.5"}})), "can only read")
	refused(t, d.call(t, ro, "delete_dns_record", map[string]any{"connection_id": d.rootDNS, "domain": "example.com", "name": "@", "type": "TXT"}), "can only read")
	if _, there := d.hz[hzRootToken].RRSet("example.com", "@", "TXT"); !there {
		t.Fatal("a read-only token deleted a record")
	}

	// A member does not see anybody else's connection: "not found".
	bob := d.connect(t, d.bob)
	refused(t, d.call(t, bob, "list_dns_records", zone), "not found")
	refused(t, d.call(t, bob, "set_dns_record", www(map[string]any{"values": []string{"203.0.113.5"}})), "not found")
	refused(t, d.call(t, bob, "delete_dns_record", map[string]any{"connection_id": d.rootDNS, "domain": "example.com", "name": "@", "type": "TXT"}), "not found")
	// An administrator cannot use a member's connection either.
	refused(t, d.call(t, root, "list_dns_records", map[string]any{"connection_id": d.bobDNS, "domain": "bob.example"}), "belongs to bob")
	// bob uses his own.
	ok(t, d.call(t, bob, "set_dns_record", map[string]any{
		"connection_id": d.bobDNS, "domain": "bob.example", "name": "@", "type": "A", "values": []string{"203.0.113.9"},
	}), &set)
	refused(t, d.call(t, d.connect(t, d.bobRead), "set_dns_record", map[string]any{
		"connection_id": d.bobDNS, "domain": "bob.example", "name": "@", "type": "A", "values": []string{"203.0.113.1"},
	}), "can only read")

	// A token limited to sites gets no DNS at all.
	if _, err := d.svc.SaveSite(context.Background(), d.admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	limited := d.connect(t, d.token(t, "write", "blog-only", []string{"blog"}))
	refused(t, d.call(t, limited, "list_dns_connections", nil), "limited to certain sites")
	refused(t, d.call(t, limited, "list_dns_records", zone), "limited to certain sites")
	refused(t, d.call(t, limited, "set_dns_record", www(map[string]any{"values": []string{"203.0.113.5"}})), "limited to certain sites")
}

func TestPointDomainTools(t *testing.T) {
	d := newDNSEnv(t)
	ctx := context.Background()
	if _, err := d.svc.SaveSite(ctx, d.admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.CreateRedirect(ctx, d.admin, panel.RedirectInput{SourceDomain: "go.example.com", Target: "https://example.com", Code: 301}); err != nil {
		t.Fatal(err)
	}
	d.hz[hzRootToken].AddRRSet("example.com", hetznertest.RRSet{Name: "blog", Type: "AAAA", TTL: 300, Records: []string{"2001:db8::1"}})
	root := d.connect(t, d.write)

	var st domainCheckOutput
	ok(t, d.call(t, root, "check_domain", map[string]any{"domain": "Blog.Example.com"}), &st)
	if st.Domain != "blog.example.com" || st.State != "no_record" || st.Found == nil || st.Zone == nil ||
		st.Zone.ConnectionID != d.rootDNS || st.Zone.Zone != "example.com" || st.Zone.RecordName != "blog" {
		t.Fatalf("check %+v (zone %+v)", st, st.Zone)
	}

	// Read-only tokens and other members cannot point a domain.
	refused(t, d.call(t, d.connect(t, d.read), "point_domain_at_server", map[string]any{"domain": "blog.example.com"}), "can only read")
	bob := d.connect(t, d.bob)
	refused(t, d.call(t, bob, "check_domain", map[string]any{"domain": "blog.example.com"}), "no site or redirect")
	refused(t, d.call(t, bob, "point_domain_at_server", map[string]any{"domain": "blog.example.com"}), "no site or redirect")
	if _, there := d.hz[hzRootToken].RRSet("example.com", "blog", "A"); there {
		t.Fatal("a refused call wrote a record")
	}

	var pt pointDomainOutput
	ok(t, d.call(t, root, "point_domain_at_server", map[string]any{"domain": "blog.example.com"}), &pt)
	got := []string{}
	for _, c := range pt.Changed {
		got = append(got, fmt.Sprintf("%s %s %v", c.Name, c.Type, c.Values))
	}
	if strings.Join(got, "; ") != "blog A [192.0.2.10]; blog AAAA []" {
		t.Fatalf("changed %v", got)
	}
	ok(t, d.call(t, root, "point_domain_at_server", map[string]any{"domain": "blog.example.com"}), &pt)
	if pt.Changed == nil || len(pt.Changed) != 0 {
		t.Fatalf("second time %+v", pt)
	}
	// The domain of a whole-domain redirect works too.
	ok(t, d.call(t, root, "point_domain_at_server", map[string]any{"domain": "go.example.com"}), &pt)
	if len(pt.Changed) != 1 || pt.Changed[0].Name != "go" {
		t.Fatalf("redirect domain %+v", pt)
	}
	refused(t, d.call(t, root, "check_domain", map[string]any{"domain": "nothing.example.com"}), "no site or redirect")

	// A token limited to the site may check its domain (as in the site's page
	// of the web interface), but not other domains.
	limited := d.connect(t, d.token(t, "write", "blog-only", []string{"blog"}))
	ok(t, d.call(t, limited, "check_domain", map[string]any{"domain": "blog.example.com"}), &st)
	refused(t, d.call(t, limited, "check_domain", map[string]any{"domain": "go.example.com"}), "no site or redirect")
}
