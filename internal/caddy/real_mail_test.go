//go:build caddy

package caddy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The mail host's ACME challenges reach the panel through Caddy's HTTP
// port, whether or not the mail host is also the panel domain (which then
// has a route on the HTTPS port; Caddy answers only its own tokens).
func TestRealCaddyMailHostChallenge(t *testing.T) {
	bin := os.Getenv("OWNPANEL_CADDY")
	if bin == "" {
		bin, _ = filepath.Abs("../../bin/caddy")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no Caddy binary at %s", bin)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "panel host=%s path=%s", r.Host, r.URL.Path)
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
	client := NewClient(admin)
	load := func(opts Options) {
		t.Helper()
		cfg, err := Build(opts, []Site{{Name: "s", Domains: []string{"site.localtest.me"}, Root: dir, Live: true}})
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
	noRedirect := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path, host string) (int, string) {
		t.Helper()
		var resp *http.Response
		var err error
		for range 50 {
			req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d%s", httpPort, path), nil)
			req.Host = host
			if resp, err = noRedirect.Do(req); err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Internal CA, so the test never asks a public CA for anything; the
	// routes on the HTTP port are the same as with ACME.
	for _, tc := range []struct {
		name        string
		panelDomain string
		ips         []string
	}{
		{"own HTTP server", "", nil},
		{"merged into the address server", "", []string{"127.0.0.1"}},
		{"mail host is the panel domain", "mail.localtest.me", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			load(Options{
				AdminListen: admin.Listen(), StorageDir: filepath.Join(dir, "storage"), TLSMode: TLSInternal,
				HTTPPort: httpPort, HTTPSPort: httpsPort, PanelUpstream: strings.TrimPrefix(upstream.URL, "http://"),
				PanelDomain: tc.panelDomain, PanelIPs: tc.ips, MailHost: "mail.localtest.me",
			})
			code, body := get("/.well-known/acme-challenge/some-token", "mail.localtest.me")
			if code != 200 || body != "panel host=mail.localtest.me path=/.well-known/acme-challenge/some-token" {
				t.Fatalf("challenge: %d %q", code, body)
			}
			// Anything else for the mail host on plain HTTP is not the panel.
			if code, body := get("/", "mail.localtest.me"); code == 200 && strings.HasPrefix(body, "panel") {
				t.Fatalf("other path reached the panel: %d %q", code, body)
			}
			// Challenges of other hosts are not passed to the panel.
			if code, body := get("/.well-known/acme-challenge/some-token", "site.localtest.me"); strings.HasPrefix(body, "panel") {
				t.Fatalf("site challenge reached the panel: %d %q", code, body)
			}
		})
	}
}
