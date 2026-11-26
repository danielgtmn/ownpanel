//go:build caddy

package caddy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
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

func selfSignedPEM(t *testing.T, host string) (Certificate, *big.Int) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	return Certificate{
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})),
	}, serial
}

// The mail host's site: the mail service's paths go to its web socket, the
// panel's API is blocked, the rest goes to the panel; Caddy presents the
// panel's certificate and never asks a CA for the name, even with ACME.
func TestRealCaddyMailWeb(t *testing.T) {
	bin := os.Getenv("OWNPANEL_CADDY")
	if bin == "" {
		bin, _ = filepath.Abs("../../bin/caddy")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no Caddy binary at %s", bin)
	}
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "panel host=%s path=%s", r.Host, r.URL.Path)
	}))
	defer panel.Close()
	sockDir, err := os.MkdirTemp("", "ow")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "web.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mail := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, "mail path=%s xff=%s body=%d", r.URL.Path, r.Header.Get("X-Forwarded-For"), n)
	})}
	go mail.Serve(ln)
	defer mail.Close()

	dir := t.TempDir()
	adminPort, httpPort, httpsPort := freePort(t), freePort(t), freePort(t)
	admin := Admin{Base: fmt.Sprintf("http://127.0.0.1:%d", adminPort)}
	boot := filepath.Join(dir, "boot.json")
	os.WriteFile(boot, []byte(fmt.Sprintf(`{"admin":{"listen":%q}}`, admin.Listen())), 0o600)
	cmd := exec.Command(bin, "run", "--config", boot)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+dir, "XDG_CONFIG_HOME="+dir)
	logPath := filepath.Join(dir, "caddy.log")
	logFile, _ := os.Create(logPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Logf("caddy log:\n%s", b)
		}
	}()
	client := NewClient(admin)
	const host = "mail.localtest.me"
	load := func(opts Options) {
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

	var wantSerial *big.Int
	dial := func(addr string) (net.Conn, error) {
		var d net.Dialer
		return d.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", httpsPort))
	}
	httpsClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) { return dial(addr) },
			TLSClientConfig: &tls.Config{ServerName: host, InsecureSkipVerify: true,
				VerifyConnection: func(cs tls.ConnectionState) error {
					if got := cs.PeerCertificates[0].SerialNumber; got.Cmp(wantSerial) != 0 {
						return fmt.Errorf("certificate %v, want the panel's %v", got, wantSerial)
					}
					return nil
				}},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	do := func(method, path, body string) (int, string) {
		t.Helper()
		var resp *http.Response
		var err error
		for range 50 {
			req, _ := http.NewRequest(method, "https://"+host+path, strings.NewReader(body))
			if resp, err = httpsClient.Do(req); err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	for _, mode := range []string{TLSInternal, TLSACME} {
		t.Run(mode, func(t *testing.T) {
			cert, serial := selfSignedPEM(t, host)
			wantSerial = serial
			// No panel domain with ACME: the test must not ask a public CA.
			panelDomain := "panel.localtest.me"
			if mode == TLSACME {
				panelDomain = ""
			}
			load(Options{
				AdminListen: admin.Listen(), StorageDir: filepath.Join(dir, "storage"), TLSMode: mode,
				HTTPPort: httpPort, HTTPSPort: httpsPort, PanelUpstream: strings.TrimPrefix(panel.URL, "http://"),
				PanelDomain: panelDomain, MailHost: host,
				MailWeb: MailWeb{Host: host, Socket: sock, Certificates: []Certificate{cert}},
			})
			for _, p := range []string{"/account/api/health", "/dav/x", "/.well-known/caldav", "/.well-known/carddav"} {
				if code, body := do("GET", p, ""); code != 200 || !strings.HasPrefix(body, "mail path="+p) || !strings.Contains(body, "xff=127.0.0.1") {
					t.Fatalf("%s: %d %q", p, code, body)
				}
			}
			for _, p := range []string{"/api/settings", "/api", "/mcp", "/mcp/x"} {
				if code, body := do("GET", p, ""); code != 404 || strings.HasPrefix(body, "panel") || strings.HasPrefix(body, "mail") {
					t.Fatalf("%s: %d %q", p, code, body)
				}
			}
			for _, p := range []string{"/", "/assets/app.js", "/account"} {
				if code, body := do("GET", p, ""); code != 200 || body != "panel host="+host+" path="+p {
					t.Fatalf("%s: %d %q", p, code, body)
				}
			}
			// Request size limits.
			if code, _ := do("PUT", "/dav/big", strings.Repeat("a", MailWebMaxBody+1)); code != 413 {
				t.Fatalf("large DAV upload: %d", code)
			}
			if code, body := do("PUT", "/dav/small", strings.Repeat("a", 1000)); code != 200 || !strings.HasSuffix(body, "body=1000") {
				t.Fatalf("small DAV upload: %d %q", code, body)
			}
			if code, _ := do("POST", "/", strings.Repeat("a", MailWebAppMaxBody+1)); code != 413 {
				t.Fatalf("large request to the web app: %d", code)
			}
		})
	}
	// A new certificate (a renewal) is picked up by the next load.
	cert, serial := selfSignedPEM(t, host)
	wantSerial = serial
	load(Options{
		AdminListen: admin.Listen(), StorageDir: filepath.Join(dir, "storage"), TLSMode: TLSACME,
		HTTPPort: httpPort, HTTPSPort: httpsPort, PanelUpstream: strings.TrimPrefix(panel.URL, "http://"),
		MailHost: host, MailWeb: MailWeb{Host: host, Socket: sock, Certificates: []Certificate{cert}},
	})
	if code, _ := do("GET", "/account/api/health", ""); code != 200 {
		t.Fatalf("after renewal: %d", code)
	}
	// Caddy never managed a certificate for the mail host, and the private
	// key does not appear in its log.
	b, _ := os.ReadFile(logPath)
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, host) && (strings.Contains(line, "obtain") || strings.Contains(line, "acme")) {
			t.Fatalf("Caddy manages the mail host's certificate: %s", line)
		}
	}
	if strings.Contains(string(b), "PRIVATE KEY") {
		t.Fatal("a private key is in Caddy's log")
	}
	// The challenge route on the HTTP port still reaches the panel.
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/.well-known/acme-challenge/tok", httpPort), nil)
	req.Host = host
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "panel host="+host) {
		t.Fatalf("challenge: %d %q", resp.StatusCode, body)
	}
}
