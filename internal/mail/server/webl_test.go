package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/web"
)

func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}}
}

// The web socket is its own socket, next to the admin one, and serves only
// the web routes; the account handler plugs in and sees the client's
// address that Caddy recorded.
func TestWebSocket(t *testing.T) {
	sock := socketDir(t)
	webDir := filepath.Join(sock, "web")
	if err := os.Mkdir(webDir, 0o750); err != nil {
		t.Fatal(err)
	}
	webSock := filepath.Join(webDir, "web.sock")
	var seen string
	in := run(t, t.TempDir(), sock, func(o *Options) {
		o.WebSocket = webSock
		o.AccountHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip, _ := web.ClientIP(r)
			seen = ip.String()
			w.WriteHeader(http.StatusNoContent)
		})
	})
	fi, err := os.Stat(webSock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o660 {
		t.Fatalf("web socket mode %v", fi.Mode().Perm())
	}
	c := unixClient(webSock)
	get := func(method, path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://mail/"+strings.TrimPrefix(path, "/"), nil)
		req.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.4")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("GET", "/account/api/health"); code != 200 || !strings.Contains(body, `"ok"`) {
		t.Fatalf("health: %d %q", code, body)
	}
	if code, _ := get("POST", "/account/api/signin"); code != 204 || seen != "203.0.113.4" {
		t.Fatalf("account handler: %d, client %q", code, seen)
	}
	// The admin API is not on the web socket.
	if code, _ := get("GET", "/status"); code != 404 {
		t.Fatalf("admin route on the web socket: %d", code)
	}
	in.stop(t)
	if _, err := os.Lstat(webSock); !os.IsNotExist(err) {
		t.Fatalf("web socket left behind: %v", err)
	}
}

func TestWebSocketRefusesOtherFiles(t *testing.T) {
	sock := socketDir(t)
	path := filepath.Join(sock, "web.sock")
	os.WriteFile(path, []byte("x"), 0o600)
	srv, err := New(Options{DataDir: t.TempDir(), AdminSocket: filepath.Join(sock, "a.sock"), WebSocket: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("run: %v", err)
	}
	// The admin socket was cleaned up again.
	if _, err := os.Lstat(filepath.Join(sock, "a.sock")); !os.IsNotExist(err) {
		t.Fatalf("admin socket left behind: %v", err)
	}
}

func TestWebSocketGroup(t *testing.T) {
	g, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Skip(err)
	}
	sock := socketDir(t)
	webDir := filepath.Join(sock, "web")
	os.Mkdir(webDir, 0o750)
	in := run(t, t.TempDir(), sock, func(o *Options) {
		o.WebSocket, o.WebGroup = filepath.Join(webDir, "web.sock"), g.Name
	})
	in.stop(t)

	srv, err := New(Options{DataDir: t.TempDir(), AdminSocket: filepath.Join(sock, "b.sock"),
		WebSocket: filepath.Join(webDir, "web.sock"), WebGroup: "no-such-group-ownpanel"})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "no-such-group-ownpanel") {
		t.Fatalf("unknown group: %v", err)
	}
}
