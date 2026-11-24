//go:build caddy

// Runs redirects against a real Caddy (bin/caddy of the repository, or
// $OWNPANEL_CADDY) with certificates from its internal CA:
// go test -tags caddy ./internal/caddy
package caddy

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRealCaddyRedirects(t *testing.T) {
	bin := os.Getenv("OWNPANEL_CADDY")
	if bin == "" {
		bin, _ = filepath.Abs("../../bin/caddy")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no Caddy binary at %s", bin)
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "site")
	os.MkdirAll(filepath.Join(root, "blog"), 0o755)
	os.WriteFile(filepath.Join(root, "index.html"), []byte("home"), 0o644)
	os.WriteFile(filepath.Join(root, "stay.txt"), []byte("stay"), 0o644)
	os.WriteFile(filepath.Join(root, "blog", "file.txt"), []byte("blog file"), 0o644)

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

	opts := Options{
		AdminListen: admin.Listen(), StorageDir: filepath.Join(dir, "storage"), TLSMode: TLSInternal,
		HTTPPort: httpPort, HTTPSPort: httpsPort,
	}
	sites := []Site{{Name: "site", Domains: []string{"site.test", "www.site.test"}, Root: root, Live: true}}
	redirects := []Redirect{
		{Domain: "site.test", Path: "/old", Target: "/new", Code: 301},
		{Domain: "site.test", Path: "/q", Target: "/target", Code: 302, KeepQuery: true},
		{Domain: "site.test", Path: "/blog/*", Target: "https://news.test/articles", Code: 308, KeepPath: true, KeepQuery: true},
		{Domain: "site.test", Path: "/blog/special", Target: "/special", Code: 301},
		{Domain: "site.test", Path: "/docs/*", Target: "/manual", Code: 307},
		{Domain: "site.test", Path: "/legacy/*", Target: "/", Code: 301, KeepPath: true},
		{Domain: "www.site.test", Path: "/*", Target: "https://site.test", Code: 301, KeepPath: true, KeepQuery: true},
		{Domain: "old.test", Target: "https://site.test", Code: 301, KeepPath: true, KeepQuery: true},
		{Domain: "plain.test", Target: "https://site.test/landing", Code: 302},
		{Domain: "qonly.test", Target: "https://site.test/", Code: 307, KeepQuery: true},
		{Domain: "moved.test", Target: "https://site.test/", Code: 308, KeepPath: true},
	}
	cfg, err := Build(opts, sites, redirects...)
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

	// Every name resolves to the local Caddy; certificates are from its
	// internal CA (like curl -k --resolve).
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, _ := net.SplitHostPort(addr)
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}
	hc := &http.Client{
		Timeout:       10 * time.Second,
		Transport:     &http.Transport{DialContext: dial, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	get := func(url string) (int, string, string) {
		t.Helper()
		var resp *http.Response
		var err error
		for i := 0; i < 50; i++ { // certificates are issued right after the load
			resp, err = hc.Get(url)
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
	https := func(host, path string) string { return fmt.Sprintf("https://%s:%d%s", host, httpsPort, path) }

	for _, c := range []struct {
		host, path string
		code       int
		location   string
	}{
		// Exact path; the query is dropped without keep_query.
		{"site.test", "/old", 301, "/new"},
		{"site.test", "/old?x=1", 301, "/new"},
		{"site.test", "/old/", 404, ""},
		{"site.test", "/OLD", 404, ""},
		// keep_query: appended when present, no lonely "?" otherwise.
		{"site.test", "/q?a=1&b=2", 302, "/target?a=1&b=2"},
		{"site.test", "/q", 302, "/target"},
		// Prefix with the rest of the path and the query.
		{"site.test", "/blog/a/b?x=1", 308, "https://news.test/articles/a/b?x=1"},
		{"site.test", "/blog/a/b", 308, "https://news.test/articles/a/b"},
		{"site.test", "/blog", 308, "https://news.test/articles"},
		{"site.test", "/blog/", 308, "https://news.test/articles/"},
		{"site.test", "/blog/file.txt", 308, "https://news.test/articles/file.txt"},
		{"site.test", "/blogger", 404, ""},
		// An exact path wins over a prefix.
		{"site.test", "/blog/special", 301, "/special"},
		// Prefix without keep_path: always the target.
		{"site.test", "/docs/x/y?z=1", 307, "/manual"},
		{"site.test", "/docs", 307, "/manual"},
		// A target "/" with keep_path never becomes "//host".
		{"site.test", "/legacy/page", 301, "/page"},
		{"site.test", "/legacy//evil.example/x", 301, "/evil.example/x"},
		// Whole domain via the prefix form "/*".
		{"www.site.test", "/", 301, "https://site.test/"},
		{"www.site.test", "/a/b?c=d", 301, "https://site.test/a/b?c=d"},
		// Domain redirects.
		{"old.test", "/", 301, "https://site.test/"},
		{"old.test", "/a/b?c=d", 301, "https://site.test/a/b?c=d"},
		{"plain.test", "/x?y=1", 302, "https://site.test/landing"},
		{"qonly.test", "/x?y=1", 307, "https://site.test/?y=1"},
		{"qonly.test", "/x", 307, "https://site.test/"},
		{"moved.test", "/p/q?r=1", 308, "https://site.test/p/q"},
	} {
		code, loc, body := get(https(c.host, c.path))
		if code != c.code || loc != c.location {
			t.Errorf("%s%s: %d %q (%q), want %d %q", c.host, c.path, code, loc, body, c.code, c.location)
		}
	}
	// The files are still served.
	for path, want := range map[string]string{"/": "home", "/stay.txt": "stay"} {
		if code, _, body := get(https("site.test", path)); code != 200 || body != want {
			t.Errorf("%s: %d %q", path, code, body)
		}
	}
	// Plain HTTP first goes to HTTPS (Caddy's automatic redirect, which
	// leaves out a non-standard port), for redirect domains as for sites.
	code, loc, _ := get(fmt.Sprintf("http://old.test:%d/a?b=1", httpPort))
	if code != 308 || loc != "https://old.test/a?b=1" {
		t.Errorf("HTTP of a redirect domain: %d %q", code, loc)
	}
	// Redirect domains get a certificate.
	conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", httpsPort), &tls.Config{ServerName: "old.test", InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("TLS for a redirect domain: %v", err)
	}
	names := conn.ConnectionState().PeerCertificates[0].DNSNames
	conn.Close()
	if len(names) != 1 || names[0] != "old.test" {
		t.Errorf("certificate of old.test is for %v", names)
	}
	// Paths are matched and passed on decoded, like Caddy's {path}; Go
	// turns line breaks into spaces, so nothing can be added to the headers.
	code, loc, _ = get(https("site.test", "/blog/a%20b"))
	if code != 308 || loc != "https://news.test/articles/a b" {
		t.Errorf("encoded rest: %d %q", code, loc)
	}
	code, loc, _ = get(https("old.test", "/x%0d%0aSet-Cookie:%20a=b"))
	if code != 301 || loc != "https://site.test/x  Set-Cookie: a=b" {
		t.Errorf("line break in the path: %d %q", code, loc)
	}
}
