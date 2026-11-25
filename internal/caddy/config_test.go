package caddy

import (
	"encoding/json"
	"strings"
	"testing"
)

func build(t *testing.T, mode string, sites ...Site) string {
	t.Helper()
	cfg, err := Build(Options{AdminListen: "127.0.0.1:2019", StorageDir: "/data/caddy", TLSMode: mode, HTTPPort: 80, HTTPSPort: 443}, sites)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuild(t *testing.T) {
	live := Site{Name: "b", Domains: []string{"b.example.com"}, Root: "/sites/b/current", Live: true}
	empty := Site{Name: "a", Domains: []string{"a.example.com"}, Root: "/sites/a/current"}
	noDomain := Site{Name: "c", Root: "/sites/c/current", Live: true}

	got := build(t, TLSACME, live, empty, noDomain)
	for _, want := range []string{`"listen":[":443"]`, `"root":"/sites/b/current"`, `"file_server"`, `"status_code":503`, `"status_code":404`} {
		if !strings.Contains(got, want) {
			t.Errorf("config misses %s", want)
		}
	}
	if strings.Contains(got, "/sites/c/current") {
		t.Error("site without domains must not get a route")
	}
	if strings.Contains(got, "/sites/a/current") {
		t.Error("site without a deployment must not be served from disk")
	}
	if got != build(t, TLSACME, noDomain, empty, live) {
		t.Error("config must not depend on site order")
	}

	if off := build(t, TLSOff, live); !strings.Contains(off, `"listen":[":80"]`) || !strings.Contains(off, `"automatic_https":{"disable":true}`) {
		t.Error("TLS off must serve plain HTTP")
	}
	if in := build(t, TLSInternal, live); !strings.Contains(in, `"install_trust":false`) {
		t.Error("internal CA must not touch the system trust store")
	}
	if _, err := Build(Options{TLSMode: "bogus"}, nil); err == nil {
		t.Error("unknown TLS mode must fail")
	}
}

func TestBuildUnixAdminAndPanelDomain(t *testing.T) {
	admin, err := ParseAdmin("unix:///run/ownpanel/caddy-admin.sock")
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		AdminListen: admin.Listen(), StorageDir: "/data/caddy", TLSMode: TLSACME, HTTPPort: 80, HTTPSPort: 443,
		PanelDomain: "panel.example.com", PanelUpstream: "127.0.0.1:8600",
	}
	site := Site{Name: "b", Domains: []string{"b.example.com"}, Root: "/sites/b/current", Live: true}
	cfg, err := Build(opts, []Site{site})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cfg)
	got := string(b)
	if !strings.Contains(got, `"admin":{"listen":"unix//run/ownpanel/caddy-admin.sock|0660"}`) {
		t.Errorf("admin socket missing: %s", got)
	}
	want := `{"handle":[{"handler":"reverse_proxy","transport":{"dial_timeout":"5s","protocol":"http","response_header_timeout":"10m"},"upstreams":[{"dial":"127.0.0.1:8600"}]}],"match":[{"host":["panel.example.com"]}],"terminal":true}`
	if !strings.Contains(got, `"routes":[`+want) {
		t.Errorf("panel route must come first: %s", got)
	}
	if strings.Count(got, "request_body") != 0 {
		t.Error("no body limit in front of the panel: uploads may be 512 MB")
	}
	opts.PanelUpstream = ""
	if _, err := Build(opts, nil); err == nil {
		t.Error("panel domain without upstream must fail")
	}
}

func TestBuildTimeouts(t *testing.T) {
	opts := Options{
		AdminListen: "127.0.0.1:2019", StorageDir: "/data/caddy", TLSMode: TLSACME, HTTPPort: 80, HTTPSPort: 443,
		PanelDomain: "panel.example.com", PanelUpstream: "127.0.0.1:8600", PanelIPs: []string{"203.0.113.5"},
	}
	cfg, err := Build(opts, []Site{{Name: "b", Domains: []string{"b.example.com"}, Root: "/sites/b/current", Live: true}})
	if err != nil {
		t.Fatal(err)
	}
	servers := cfg["apps"].(obj)["http"].(obj)["servers"].(obj)
	if len(servers) != 2 {
		t.Fatalf("want sites and ip_http servers, got %v", servers)
	}
	for name, s := range servers {
		srv := s.(obj)
		for key, want := range map[string]string{
			"read_header_timeout": "10s", "read_idle_timeout": "1m", "read_timeout": "1h",
			"write_idle_timeout": "1m", "idle_timeout": "2m",
		} {
			if srv[key] != want {
				t.Errorf("server %s: %s = %v, want %s", name, key, srv[key], want)
			}
		}
		// An overall write limit would cut off slow downloads of large files.
		if _, ok := srv["write_timeout"]; ok {
			t.Errorf("server %s must not limit the whole response", name)
		}
	}
	b, _ := json.Marshal(cfg)
	if n := strings.Count(string(b), `"response_header_timeout":"10m"`); n != 2 {
		t.Errorf("both panel routes need proxy timeouts, found %d", n)
	}
}

func TestParseAdmin(t *testing.T) {
	for _, tc := range []struct{ in, listen, socket string }{
		{"http://127.0.0.1:2019", "127.0.0.1:2019", ""},
		{"http://localhost:2019/", "localhost:2019", ""},
		{"unix:///tmp/x.sock", "unix//tmp/x.sock|0660", "/tmp/x.sock"},
	} {
		a, err := ParseAdmin(tc.in)
		if err != nil || a.Listen() != tc.listen || a.Socket != tc.socket {
			t.Errorf("%s: %+v %q %v", tc.in, a, a.Listen(), err)
		}
	}
	for _, bad := range []string{"", "127.0.0.1:2019", "unix://relative.sock", "unix:///a/../b.sock", "https://x:1", "http://x:1/path"} {
		if _, err := ParseAdmin(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
