//go:build caddy

// Runs previews against a real Caddy (bin/caddy of the repository, or
// $OWNPANEL_CADDY): go test -tags caddy ./internal/caddy
package caddy

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// startCaddy runs bin/caddy with an empty config and returns its admin.
func startCaddy(t *testing.T, dir string) Admin {
	t.Helper()
	bin := os.Getenv("OWNPANEL_CADDY")
	if bin == "" {
		bin, _ = filepath.Abs("../../bin/caddy")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no Caddy binary at %s", bin)
	}
	adminPort := freePort(t)
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
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(dir, "caddy.log"))
			t.Logf("caddy log:\n%s", b)
		}
	})
	return admin
}

func loadConfig(t *testing.T, admin Admin, opts Options, sites []Site) {
	t.Helper()
	cfg, err := Build(opts, sites)
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
}

// askLog is the panel's permission endpoint: yes only for known names.
type askLog struct {
	mu    sync.Mutex
	asked []string
	known map[string]bool
}

func (a *askLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d := r.URL.Query().Get("domain")
	a.mu.Lock()
	a.asked = append(a.asked, d)
	ok := a.known[d]
	a.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
	}
}

func (a *askLog) list() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.asked)
}

const (
	testPreviewDomain = "preview.example.test"
	testLabel         = "k7q2m9x4c8v1b6n3z5t0w2r8"
	testKey           = "s3cr3tk3ys3cr3tk3ys3cr3tk3y"
)

type previewClient struct {
	t      *testing.T
	scheme string
	port   int
	client *http.Client
}

func newPreviewClient(t *testing.T, scheme string, port int) *previewClient {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &previewClient{t: t, scheme: scheme, port: port, client: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			// Every name is this machine.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", port))
			},
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type answer struct {
	code int
	h    http.Header
	body string
}

func (c *previewClient) get(host, path, cookie string) (answer, error) {
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s://%s:%d%s", c.scheme, host, c.port, path), nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, resp.Header, string(b)}, nil
}

func (c *previewClient) mustGet(host, path, cookie string) answer {
	c.t.Helper()
	var a answer
	var err error
	for i := 0; i < 50; i++ { // certificates are issued on first use
		if a, err = c.get(host, path, cookie); err == nil {
			return a
		}
		time.Sleep(200 * time.Millisecond)
	}
	c.t.Fatalf("%s%s: %v", host, path, err)
	return a
}

func previewSites(t *testing.T, dir string) []Site {
	root := filepath.Join(dir, "release")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "index.html"), []byte("preview content"), 0o644)
	return []Site{{Name: "a", Domains: []string{"a.example.test"}, Root: filepath.Join(dir, "current"), Live: false,
		Previews: []Preview{{Label: testLabel, Root: root, Key: testKey}}}}
}

// checkPreviewFlow walks through a preview: the link sets the cookie, the
// cookie opens the files, nothing else does.
func checkPreviewFlow(t *testing.T, c *previewClient, cookieName string) {
	t.Helper()
	host := testLabel + "." + testPreviewDomain
	a := c.mustGet(host, "/?"+PreviewQuery+"="+testKey, "")
	wantLoc := fmt.Sprintf("%s://%s:%d/", c.scheme, host, c.port)
	if a.code != 303 || a.h.Get("Location") != wantLoc {
		t.Fatalf("link: %d %q, want 303 %q", a.code, a.h.Get("Location"), wantLoc)
	}
	set := a.h.Get("Set-Cookie")
	if !strings.HasPrefix(set, cookieName+"="+testKey+";") || !strings.Contains(set, "HttpOnly") {
		t.Fatalf("cookie: %q", set)
	}
	if (cookieName == securePreviewCookie) != strings.Contains(set, "Secure") {
		t.Fatalf("cookie Secure flag: %q", set)
	}
	if a.h.Get("X-Robots-Tag") != "noindex, nofollow" || a.h.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers of the link answer: %v", a.h)
	}
	// A path that looks like another host stays on this one.
	if a := c.mustGet(host, "//other.example/x?"+PreviewQuery+"="+testKey, ""); a.code != 303 || !strings.HasPrefix(a.h.Get("Location"), wantLoc) {
		t.Fatalf("link with //: %d %q", a.code, a.h.Get("Location"))
	}

	cookie := "other=1; " + cookieName + "=" + testKey + "; more=2"
	if a := c.mustGet(host, "/", cookie); a.code != 200 || a.body != "preview content" || a.h.Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("with cookie: %d %q %v", a.code, a.body, a.h)
	}
	for name, ck := range map[string]string{
		"no cookie":      "",
		"wrong key":      cookieName + "=" + strings.Repeat("x", len(testKey)),
		"wrong name":     "something=" + testKey,
		"key in a query": "",
	} {
		path := "/"
		if name == "key in a query" {
			path = "/?" + PreviewQuery + "=" + strings.Repeat("y", len(testKey))
		}
		if a := c.mustGet(host, path, ck); a.code != 403 || strings.Contains(a.body, "preview content") {
			t.Errorf("%s: %d %q", name, a.code, a.body)
		}
	}
}

func TestRealCaddyPreviews(t *testing.T) {
	dir := t.TempDir()
	admin := startCaddy(t, dir)
	ask := &askLog{known: map[string]bool{testLabel + "." + testPreviewDomain: true}}
	askServer := httptest.NewServer(ask)
	defer askServer.Close()

	httpPort, httpsPort := freePort(t), freePort(t)
	opts := Options{
		AdminListen: admin.Listen(), StorageDir: filepath.Join(dir, "storage"), TLSMode: TLSInternal,
		HTTPPort: httpPort, HTTPSPort: httpsPort,
		PreviewDomain: testPreviewDomain, PreviewPermission: askServer.URL + "/allowed",
	}
	sites := previewSites(t, dir)
	loadConfig(t, admin, opts, sites)

	t.Run("internal", func(t *testing.T) {
		c := newPreviewClient(t, "https", httpsPort)
		checkPreviewFlow(t, c, securePreviewCookie)
		if got := ask.list(); len(got) == 0 || !slices.Contains(got, testLabel+"."+testPreviewDomain) {
			t.Fatalf("Caddy did not ask before the certificate: %v", got)
		}

		// A name that is no preview gets no certificate: the handshake fails
		// after Caddy asked.
		unknown := "unknownlabel." + testPreviewDomain
		if _, err := c.get(unknown, "/", ""); err == nil {
			t.Fatal("unknown preview name got a certificate")
		}
		if !slices.Contains(ask.list(), unknown) {
			t.Fatal("Caddy did not ask for the unknown name")
		}
		// Neither does the wildcard's parent, nor a name two levels below.
		for _, h := range []string{testPreviewDomain, "a." + testLabel + "." + testPreviewDomain} {
			if _, err := c.get(h, "/", ""); err == nil {
				t.Errorf("%s got a certificate", h)
			}
		}
		storage, _ := filepath.Glob(filepath.Join(dir, "storage", "certificates", "*", "*"))
		for _, s := range storage {
			if strings.Contains(s, "unknownlabel") || strings.Contains(s, "wildcard_") {
				t.Errorf("certificate stored for %s", s)
			}
		}

		// Plain HTTP goes to HTTPS.
		hc := newPreviewClient(t, "http", httpPort)
		if a := hc.mustGet(testLabel+"."+testPreviewDomain, "/x", ""); a.code < 300 || a.code > 399 || !strings.HasPrefix(a.h.Get("Location"), "https://"+testLabel+"."+testPreviewDomain) {
			t.Fatalf("HTTP: %d %q", a.code, a.h.Get("Location"))
		}
	})

	t.Run("unknown name with a certificate", func(t *testing.T) {
		// Should a certificate exist for a name whose preview is gone (it
		// was pruned), the name still answers 404.
		ask.mu.Lock()
		ask.known["gone."+testPreviewDomain] = true
		ask.mu.Unlock()
		c := newPreviewClient(t, "https", httpsPort)
		if a := c.mustGet("gone."+testPreviewDomain, "/?"+PreviewQuery+"="+testKey, ""); a.code != 404 || a.body != "Unknown preview.\n" || a.h.Get("X-Robots-Tag") == "" {
			t.Fatalf("gone preview: %d %q", a.code, a.body)
		}
	})

	t.Run("off", func(t *testing.T) {
		opts := opts
		opts.TLSMode, opts.PreviewPermission = TLSOff, ""
		loadConfig(t, admin, opts, sites)
		c := newPreviewClient(t, "http", httpPort)
		checkPreviewFlow(t, c, previewCookie)
		if a := c.mustGet("unknownlabel."+testPreviewDomain, "/", ""); a.code != 404 {
			t.Fatalf("unknown: %d", a.code)
		}
		// The site itself still has no content.
		if a := c.mustGet("a.example.test", "/", ""); a.code != 503 {
			t.Fatalf("site: %d %q", a.code, a.body)
		}
	})

	t.Run("acme", func(t *testing.T) {
		// Public certificates cannot be obtained here; this checks that the
		// config loads and that a name that is no preview is refused before
		// any certificate authority is contacted.
		opts := opts
		opts.TLSMode = TLSACME
		opts.ACMEEmail = "admin@example.test"
		loadConfig(t, admin, opts, sites)
		c := newPreviewClient(t, "https", httpsPort)
		unknown := "acmeunknown." + testPreviewDomain
		if _, err := c.get(unknown, "/", ""); err == nil {
			t.Fatal("unknown preview name got a certificate")
		}
		if !slices.Contains(ask.list(), unknown) {
			t.Fatal("Caddy did not ask for the unknown name")
		}
		b, _ := os.ReadFile(filepath.Join(dir, "caddy.log"))
		log := string(b)
		if strings.Contains(log, `"identifiers":["*.`+testPreviewDomain) || strings.Contains(log, "obtaining certificate\",\"identifier\":\"*."+testPreviewDomain) {
			t.Fatalf("Caddy tried to obtain a wildcard certificate:\n%s", log)
		}
		if strings.Contains(log, "YOUR SERVER MAY BE VULNERABLE") {
			t.Fatal("on-demand TLS without permission")
		}
	})
}
