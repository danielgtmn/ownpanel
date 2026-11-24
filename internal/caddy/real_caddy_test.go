//go:build caddy

// Runs the generated config against a real Caddy (bin/caddy of the
// repository, or $OWNPANEL_CADDY): go test -tags caddy ./internal/caddy
package caddy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func hasIPv6Loopback() bool {
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func TestRealCaddyIPAccess(t *testing.T) {
	bin := os.Getenv("OWNPANEL_CADDY")
	if bin == "" {
		bin, _ = filepath.Abs("../../bin/caddy")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no Caddy binary at %s", bin)
	}

	// The "panel": echoes what arrived through Caddy.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "panel host=%s proto=%s", r.Host, r.Header.Get("X-Forwarded-Proto"))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	adminPort, httpPort, httpsPort := freePort(t), freePort(t), freePort(t)
	admin := Admin{Base: fmt.Sprintf("http://127.0.0.1:%d", adminPort)}
	boot := filepath.Join(dir, "boot.json")
	os.WriteFile(boot, []byte(fmt.Sprintf(`{"admin":{"listen":%q}}`, admin.Listen())), 0o600)
	cmd := exec.Command(bin, "run", "--config", boot)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+dir, "XDG_CONFIG_HOME="+dir)
	logFile, _ := os.Create(filepath.Join(dir, "caddy.log"))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(dir, "caddy.log"))
			t.Logf("caddy log:\n%s", b)
		}
	}()

	ips := []string{"127.0.0.1"}
	v6 := hasIPv6Loopback()
	if v6 {
		ips = append(ips, "::1")
	}
	opts := Options{
		AdminListen: admin.Listen(), StorageDir: filepath.Join(dir, "storage"), TLSMode: TLSACME,
		HTTPPort: httpPort, HTTPSPort: httpsPort,
		PanelUpstream: strings.TrimPrefix(upstream.URL, "http://"),
		PanelIPs:      ips,
	}
	client := NewClient(admin)
	load := func() {
		t.Helper()
		cfg, err := Build(opts, nil)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			err = client.Load(context.Background(), cfg)
			if err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	load()

	// Like curl -k: Go sends no SNI for an IP address.
	insecure := &http.Client{
		Timeout:       10 * time.Second,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	get := func(url, host string) (int, string, string) {
		t.Helper()
		var resp *http.Response
		var err error
		for i := 0; i < 50; i++ { // the internal certificate is issued on first use
			req, _ := http.NewRequest("GET", url, nil)
			if host != "" {
				req.Host = host
			}
			resp, err = insecure.Do(req)
			if err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Location"), string(b)
	}

	if code, _, body := get(fmt.Sprintf("https://127.0.0.1:%d/", httpsPort), ""); code != 200 || !strings.Contains(body, "proto=https") {
		t.Fatalf("IPv4 access: %d %s", code, body)
	}

	// Like openssl s_client without -servername: no SNI at all.
	conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", httpsPort), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("handshake without SNI: %v", err)
	}
	leaf := conn.ConnectionState().PeerCertificates[0]
	conn.Close()
	if len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("certificate without SNI is for %v %v", leaf.IPAddresses, leaf.DNSNames)
	}
	rootPEM, err := os.ReadFile(filepath.Join(dir, "storage", "pki", "authorities", "local", "root.crt"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(rootPEM)
	root, _ := x509.ParseCertificate(block.Bytes)
	pool := x509.NewCertPool()
	pool.AddCert(root)
	inter := x509.NewCertPool()
	for _, c := range conn.ConnectionState().PeerCertificates[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter}); err != nil {
		t.Fatalf("certificate is not from Caddy's internal CA: %v", err)
	}

	if v6 {
		if code, _, body := get(fmt.Sprintf("https://[::1]:%d/", httpsPort), ""); code != 200 || !strings.Contains(body, fmt.Sprintf("host=[::1]:%d", httpsPort)) {
			t.Fatalf("IPv6 access: %d %s", code, body)
		}
		if code, _, body := get(fmt.Sprintf("https://[::1]:%d/", httpsPort), "[::1]"); code != 200 || !strings.Contains(body, "host=[::1]") {
			t.Fatalf("IPv6 Host without port: %d %s", code, body)
		}
		if code, loc, _ := get(fmt.Sprintf("http://[::1]:%d/a?b=1", httpPort), ""); code != 308 || loc != fmt.Sprintf("https://[::1]:%d/a?b=1", httpsPort) {
			t.Fatalf("IPv6 HTTP redirect: %d %q", code, loc)
		}
	}
	if code, loc, _ := get(fmt.Sprintf("http://127.0.0.1:%d/a?b=1", httpPort), ""); code != 308 || loc != fmt.Sprintf("https://127.0.0.1:%d/a?b=1", httpsPort) {
		t.Fatalf("HTTP redirect: %d %q", code, loc)
	}
	if code, _, body := get(fmt.Sprintf("https://127.0.0.1:%d/", httpsPort), "unknown.example"); code != 404 {
		t.Fatalf("other hosts are not the panel: %d %s", code, body)
	}

	// Set up with a domain: the addresses redirect to it. Internal mode, so
	// the test never asks a public CA for the domain.
	opts.PanelDomain, opts.IPRedirect, opts.TLSMode = "panel.localtest.me", true, TLSInternal
	load()
	want := fmt.Sprintf("https://panel.localtest.me:%d/x?y=1", httpsPort)
	if code, loc, _ := get(fmt.Sprintf("https://127.0.0.1:%d/x?y=1", httpsPort), ""); code != 308 || loc != want {
		t.Fatalf("HTTPS redirect to the domain: %d %q", code, loc)
	}
	if code, loc, _ := get(fmt.Sprintf("http://127.0.0.1:%d/x?y=1", httpPort), ""); code != 308 || loc != want {
		t.Fatalf("HTTP redirect to the domain: %d %q", code, loc)
	}
	if v6 {
		if code, loc, _ := get(fmt.Sprintf("https://[::1]:%d/x?y=1", httpsPort), ""); code != 308 || loc != want {
			t.Fatalf("IPv6 redirect to the domain: %d %q", code, loc)
		}
	}
}
