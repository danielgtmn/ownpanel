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

// The paths where mail programs look for their settings reach the panel on
// the names for setting up mail programs and, for the well-known path, on
// a site's domain that is a mail domain; nothing else does.
func TestRealCaddyMailSetupRoutes(t *testing.T) {
	bin := os.Getenv("OWNPANEL_CADDY")
	if bin == "" {
		bin, _ = filepath.Abs("../../bin/caddy")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no Caddy binary at %s", bin)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "panel host=%s method=%s path=%s", r.Host, r.Method, r.URL.Path)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("site files"), 0o644)
	os.MkdirAll(filepath.Join(dir, "mail"), 0o755)
	os.WriteFile(filepath.Join(dir, "mail", "config-v1.1.xml"), []byte("site file"), 0o644)
	adminPort, httpPort := freePort(t), freePort(t)
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
	cfg, err := Build(Options{
		AdminListen: admin.Listen(), StorageDir: filepath.Join(dir, "storage"), TLSMode: TLSOff,
		HTTPPort: httpPort, HTTPSPort: freePort(t), PanelUpstream: strings.TrimPrefix(upstream.URL, "http://"),
		MailSetupHosts:        []string{"autoconfig.mail-domain.localtest.me", "autodiscover.mail-domain.localtest.me"},
		MailAutoconfigDomains: []string{"mail-domain.localtest.me"},
	}, []Site{
		{Name: "m", Domains: []string{"mail-domain.localtest.me"}, Root: dir, Live: true},
		{Name: "o", Domains: []string{"other.localtest.me"}, Root: dir, Live: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(admin)
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
	do := func(method, host, path string) (int, string) {
		t.Helper()
		var resp *http.Response
		var err error
		for range 50 {
			req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", httpPort, path), strings.NewReader("<x/>"))
			req.Host = host
			if resp, err = http.DefaultClient.Do(req); err == nil {
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
	for _, c := range []struct {
		method, host, path string
		panel              bool
	}{
		{"GET", "autoconfig.mail-domain.localtest.me", "/mail/config-v1.1.xml", true},
		{"POST", "autodiscover.mail-domain.localtest.me", "/Autodiscover/Autodiscover.xml", true},
		{"POST", "autodiscover.mail-domain.localtest.me", "/autodiscover/autodiscover.xml", true},
		{"GET", "mail-domain.localtest.me", "/.well-known/autoconfig/mail/config-v1.1.xml", true},
		{"GET", "autoconfig.mail-domain.localtest.me", "/", false},
		{"GET", "autoconfig.mail-domain.localtest.me", "/api/session", false},
		{"GET", "mail-domain.localtest.me", "/mail/config-v1.1.xml", false},
		{"GET", "other.localtest.me", "/.well-known/autoconfig/mail/config-v1.1.xml", false},
		{"GET", "autoconfig.other.localtest.me", "/mail/config-v1.1.xml", false},
	} {
		code, body := do(c.method, c.host, c.path)
		if got := strings.HasPrefix(body, "panel"); got != c.panel {
			t.Errorf("%s %s%s: %d %q", c.method, c.host, c.path, code, body)
		}
		if c.panel && !strings.Contains(body, "host="+c.host) {
			t.Errorf("%s %s%s: host not kept: %q", c.method, c.host, c.path, body)
		}
	}
	// The site's own files are still there.
	if code, body := do("GET", "mail-domain.localtest.me", "/"); code != 200 || body != "site files" {
		t.Fatalf("site: %d %q", code, body)
	}
}
