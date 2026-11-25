package caddy

import (
	"encoding/json"
	"strings"
	"testing"
)

func ipOpts(mode string) Options {
	return Options{
		AdminListen: "127.0.0.1:2019", StorageDir: "/data/caddy", TLSMode: mode, HTTPPort: 80, HTTPSPort: 443,
		PanelUpstream: "127.0.0.1:8600",
		PanelIPs:      []string{"2001:db8::5", "203.0.113.5", "127.0.0.1", "203.0.113.5"},
	}
}

func buildJSON(t *testing.T, opts Options) (string, map[string]any) {
	t.Helper()
	cfg, err := Build(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return string(b), m
}

func TestBuildIPAccess(t *testing.T) {
	got, _ := buildJSON(t, ipOpts(TLSACME))
	ipRoute := `{"handle":[{"handler":"reverse_proxy","transport":{"dial_timeout":"5s","protocol":"http","response_header_timeout":"10m"},"upstreams":[{"dial":"127.0.0.1:8600"}]}],"match":[{"host":["127.0.0.1","203.0.113.5","2001:db8::5"]}],"terminal":true}`
	for _, want := range []string{
		`"routes":[` + ipRoute,
		`"tls_connection_policies":[{"default_sni":"203.0.113.5"}]`,
		`{"issuers":[{"module":"internal"}],"subjects":["127.0.0.1","203.0.113.5","2001:db8::5"]}`,
		`"install_trust":false`,
		// HTTP on the addresses goes to HTTPS on the same address.
		`"ip_http":{"idle_timeout":"2m","listen":[":80"]`,
		`"Location":["https://[2001:db8::5]{http.request.uri}"]`,
		`"Location":["https://203.0.113.5{http.request.uri}"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config misses %s\n%s", want, got)
		}
	}
	if strings.Contains(got, `"module":"acme"`) {
		t.Error("without an e-mail the ACME defaults stay")
	}

	// Without a domain, IPRedirect cannot redirect anywhere: keep serving.
	o := ipOpts(TLSACME)
	o.IPRedirect = true
	if got, _ := buildJSON(t, o); !strings.Contains(got, ipRoute) {
		t.Errorf("IP access without a panel domain must keep serving the panel: %s", got)
	}

	// Non-standard ports are kept in redirects.
	o = ipOpts(TLSInternal)
	o.HTTPPort, o.HTTPSPort = 8080, 8443
	got, _ = buildJSON(t, o)
	if !strings.Contains(got, `"https://[2001:db8::5]:8443{http.request.uri}"`) || !strings.Contains(got, `"ip_http":{"idle_timeout":"2m","listen":[":8080"]`) {
		t.Errorf("ports: %s", got)
	}
	if !strings.Contains(got, `"policies":[{"issuers":[{"module":"internal"}],"subjects":["127.0.0.1","203.0.113.5","2001:db8::5"]},{"issuers":[{"module":"internal"}]}]`) {
		t.Errorf("internal mode keeps its catch-all policy after the IP policy: %s", got)
	}
}

func TestBuildIPRedirectToPanelDomain(t *testing.T) {
	o := ipOpts(TLSACME)
	o.PanelDomain, o.IPRedirect, o.ACMEEmail = "panel.example.com", true, "admin@example.com"
	got, m := buildJSON(t, o)
	want := `{"handle":[{"handler":"static_response","headers":{"Location":["https://panel.example.com{http.request.uri}"]},"status_code":308}],"match":[{"host":["127.0.0.1","203.0.113.5","2001:db8::5"]}],"terminal":true}`
	if !strings.Contains(got, want) {
		t.Errorf("HTTPS on an address must redirect to the panel domain: %s", got)
	}
	routes := m["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["sites"].(map[string]any)["routes"].([]any)
	first, _ := json.Marshal(routes[0])
	if !strings.Contains(string(first), `"panel.example.com"`) || !strings.Contains(string(first), "reverse_proxy") {
		t.Errorf("panel domain route must come first: %s", first)
	}
	if strings.Count(got, `"Location":["https://panel.example.com{http.request.uri}"]`) != 4 {
		t.Errorf("HTTP on each address (3) and HTTPS (1) must redirect to the domain: %s", got)
	}
	// The IP policy comes before the catch-all ACME policy with the e-mail.
	if !strings.Contains(got, `"policies":[{"issuers":[{"module":"internal"}],"subjects":["127.0.0.1","203.0.113.5","2001:db8::5"]},{"issuers":[{"email":"admin@example.com","module":"acme"}]}]`) {
		t.Errorf("policies: %s", got)
	}

	o.TLSMode = TLSOff
	o.HTTPPort = 8080
	got, _ = buildJSON(t, o)
	if !strings.Contains(got, `"Location":["http://panel.example.com:8080{http.request.uri}"]`) || strings.Contains(got, "ip_http") || strings.Contains(got, `"tls"`) {
		t.Errorf("TLS off: plain HTTP redirect, no extra server, no TLS: %s", got)
	}
}

func TestBuildIPAccessErrors(t *testing.T) {
	o := ipOpts(TLSACME)
	o.PanelUpstream = ""
	if _, err := Build(o, nil); err == nil {
		t.Error("IP access without upstream must fail")
	}
	o = ipOpts(TLSACME)
	o.PanelIPs = []string{"panel.example.com"}
	if _, err := Build(o, nil); err == nil {
		t.Error("a name is not an address")
	}
	if got, _ := buildJSON(t, Options{TLSMode: TLSACME, HTTPPort: 80, HTTPSPort: 443}); strings.Contains(got, "ip_http") || strings.Contains(got, "default_sni") {
		t.Errorf("no addresses, no IP config: %s", got)
	}
}

func TestDefaultSNI(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"127.0.0.1", "203.0.113.5", "2001:db8::5"}, "203.0.113.5"},
		{[]string{"127.0.0.1", "::1", "2001:db8::5"}, "2001:db8::5"},
		{[]string{"127.0.0.1", "::1"}, "127.0.0.1"},
		{[]string{"10.0.0.2", "127.0.0.1"}, "10.0.0.2"},
	} {
		ips, _ := canonicalIPs(tc.in)
		if got := defaultSNI(ips); got != tc.want {
			t.Errorf("%v: %s, want %s", tc.in, got, tc.want)
		}
	}
}
