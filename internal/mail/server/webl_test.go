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

// A web socket that cannot be set up never takes mail down: the service
// runs without it and its status says why.
func TestWebSocketRefusesOtherFiles(t *testing.T) {
	sock := socketDir(t)
	path := filepath.Join(sock, "web.sock")
	os.WriteFile(path, []byte("x"), 0o600)
	in := run(t, t.TempDir(), sock, func(o *Options) { o.WebSocket = path })
	defer in.stop(t)
	st, err := in.client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Web == nil || st.Web.Listening || !strings.Contains(st.Web.Error, "not a socket") || st.Web.Address != path {
		t.Fatalf("web socket status %+v", st.Web)
	}
	if b, _ := os.ReadFile(path); string(b) != "x" {
		t.Fatal("the other file was replaced")
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

	// An unknown group: mail runs on, without the web socket.
	in = run(t, t.TempDir(), sock, func(o *Options) {
		o.WebSocket, o.WebGroup = filepath.Join(webDir, "web.sock"), "no-such-group-ownpanel"
	})
	defer in.stop(t)
	st, err := in.client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Web == nil || st.Web.Listening || !strings.Contains(st.Web.Error, "no-such-group-ownpanel") {
		t.Fatalf("unknown group: %+v", st.Web)
	}
	if _, err := os.Lstat(filepath.Join(webDir, "web.sock")); !os.IsNotExist(err) {
		t.Fatalf("web socket made anyway: %v", err)
	}
}
