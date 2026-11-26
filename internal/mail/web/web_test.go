package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func do(h http.Handler, method, path string, header http.Header, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRoutes(t *testing.T) {
	h := Handler(Options{})
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"GET", "/account/api/health", 200},
		{"HEAD", "/account/api/health", 200},
		{"POST", "/account/api/health", 404},
		{"GET", "/account/api/session", 404},
		{"GET", "/.well-known/caldav", 404},
		{"PROPFIND", "/.well-known/carddav", 404},
		{"PROPFIND", "/dav/", 501},
		{"GET", "/dav", 501},
		{"GET", "/", 404},
		{"GET", "/api/settings", 404},
	} {
		rec := do(h, tc.method, tc.path, nil, "")
		if rec.Code != tc.code {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, rec.Code, tc.code)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s: headers %v", tc.method, tc.path, rec.Header())
		}
	}
	if rec := do(h, "GET", "/account/api/health", nil, ""); rec.Body.String() != "{\"status\":\"ok\"}\n" || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("health: %q %v", rec.Body.String(), rec.Header())
	}
}

func TestAccountHandler(t *testing.T) {
	var got string
	h := Handler(Options{Account: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.WriteHeader(204)
	})})
	if rec := do(h, "POST", "/account/api/signin", nil, ""); rec.Code != 204 || got != "/account/api/signin" {
		t.Fatalf("account: %d %q", rec.Code, got)
	}
	// Health stays the web listener's own.
	got = ""
	if rec := do(h, "GET", "/account/api/health", nil, ""); rec.Code != 200 || got != "" {
		t.Fatalf("health went to the account handler: %d %q", rec.Code, got)
	}
}

func TestBodyLimit(t *testing.T) {
	var readErr error
	h := Handler(Options{Account: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})})
	do(h, "PUT", "/account/api/x", nil, strings.Repeat("a", MaxBodyBytes+1))
	if readErr == nil {
		t.Fatal("a body over the limit was read")
	}
	do(h, "PUT", "/account/api/x", nil, strings.Repeat("a", 1000))
	if readErr != nil {
		t.Fatal(readErr)
	}
}

func TestClientIP(t *testing.T) {
	var ip netip.Addr
	var ok bool
	h := Handler(Options{Account: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, ok = ClientIP(r)
	})})
	for _, tc := range []struct {
		name   string
		header []string
		want   string
	}{
		{"none", nil, ""},
		{"one", []string{"203.0.113.7"}, "203.0.113.7"},
		{"right-most of a list", []string{"198.51.100.1, 203.0.113.7"}, "203.0.113.7"},
		{"right-most of several headers", []string{"198.51.100.1", "203.0.113.7"}, "203.0.113.7"},
		{"ipv6", []string{"2001:db8::1"}, "2001:db8::1"},
		{"mapped", []string{"::ffff:203.0.113.7"}, "203.0.113.7"},
		{"garbage at the end", []string{"203.0.113.7, nonsense"}, ""},
		{"zone", []string{"fe80::1%eth0"}, ""},
		{"with port", []string{"203.0.113.7:1234"}, ""},
	} {
		ip, ok = netip.Addr{}, false
		do(h, "GET", "/account/api/x", http.Header{"X-Forwarded-For": tc.header}, "")
		if tc.want == "" {
			if ok {
				t.Errorf("%s: got %v", tc.name, ip)
			}
			continue
		}
		if !ok || ip.String() != tc.want {
			t.Errorf("%s: got %v %v, want %s", tc.name, ip, ok, tc.want)
		}
	}
	// Outside the web handler there is no client address at all.
	if _, ok := ClientIP(httptest.NewRequest("GET", "/", nil)); ok {
		t.Fatal("client address without the web handler")
	}
}

func TestServerLimits(t *testing.T) {
	s := NewServer(Options{})
	if s.ReadHeaderTimeout == 0 || s.ReadTimeout == 0 || s.WriteTimeout == 0 || s.IdleTimeout == 0 || s.MaxHeaderBytes != MaxHeaderBytes {
		t.Fatalf("limits missing: %+v", s)
	}
}
