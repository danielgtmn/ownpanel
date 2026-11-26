package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// Admin is where Caddy's admin API listens.
type Admin struct {
	// Socket is the absolute path of a Unix socket, or "" for TCP.
	Socket string
	// Base is the URL requests go to.
	Base string
}

// ParseAdmin accepts "http://host:port" or "unix:///abs/path.sock".
func ParseAdmin(s string) (Admin, error) {
	if path, ok := strings.CutPrefix(s, "unix://"); ok {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return Admin{}, fmt.Errorf("invalid Caddy admin address %q: the socket path must be absolute, like unix:///run/ownpanel/caddy-admin.sock", s)
		}
		// The host part is ignored by Caddy for Unix sockets.
		return Admin{Socket: path, Base: "http://caddy"}, nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "http" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return Admin{}, fmt.Errorf("invalid Caddy admin address %q: use http://host:port or unix:///path/to.sock", s)
	}
	return Admin{Base: "http://" + u.Host}, nil
}

// Listen is the value for admin.listen in Caddy's config. A Unix socket is
// created with mode 0660, so access is controlled by its group.
func (a Admin) Listen() string {
	if a.Socket != "" {
		return "unix/" + a.Socket + "|0660"
	}
	return strings.TrimPrefix(a.Base, "http://")
}

// Client talks to Caddy's admin API.
type Client struct {
	base string
	http *http.Client
}

// NewClient returns a client for the admin API at a.
func NewClient(a Admin) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if a.Socket != "" {
		var d net.Dialer
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "unix", a.Socket)
		}
	}
	return &Client{
		base: strings.TrimRight(a.Base, "/"),
		http: &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// Load replaces Caddy's running config. Caddy rolls back by itself if the new
// config cannot be applied, and does nothing if it equals the current one.
func (c *Client) Load(ctx context.Context, cfg any) error {
	body, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/load", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin API unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("caddy rejected config (%s): %s", resp.Status, redactKeys(strings.TrimSpace(string(msg))))
	}
	return nil
}

// redactKeys keeps private keys (the mail host's certificate is loaded as
// PEM, see MailWeb) out of error messages, should Caddy ever quote them.
func redactKeys(msg string) string {
	if strings.Contains(msg, "PRIVATE KEY") {
		return "the configuration was refused (details withheld: they contain a private key)"
	}
	return msg
}
