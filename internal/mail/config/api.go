package config

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Admin socket endpoints (see Client for their use):
//
//	POST   /load                            body Config; 204, or 400 with an error text
//	GET    /status                          Status
//	GET    /events?after=<id>               []Event, oldest first, at most 500
//	POST   /mailboxes/{storage_id}/unblock  204, 404 if unknown
//	DELETE /mailboxes/{storage_id}          204 (also when there was no data)
//	POST   /checks/outbound                 OutboundCheck
const (
	PathLoad     = "/load"
	PathStatus   = "/status"
	PathEvents   = "/events"
	PathMailbox  = "/mailboxes/"
	PathOutbound = "/checks/outbound"
)

// Status is what the service reports about itself.
type Status struct {
	// ConfigHash is Config.Hash of the loaded configuration ("" if none).
	ConfigHash string `json:"config_hash"`
	// Listeners tells per listener ("smtp", "submission", "submissions",
	// "imaps") whether it is listening, and the error if it could not.
	Listeners []Listener `json:"listeners"`
	// CertificateExpires is when the first certificate expires (zero if none).
	CertificateExpires time.Time `json:"certificate_expires"`
	// QueueLength is the number of messages waiting to be delivered.
	QueueLength int `json:"queue_length"`
	// Mailboxes reports usage and block state of every configured mailbox.
	Mailboxes []MailboxStatus `json:"mailboxes"`
	// Orphans are storage ids that have data but are not configured.
	Orphans []string `json:"orphans"`
}

// Listener is the state of one port.
type Listener struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	Listening bool   `json:"listening"`
	Error     string `json:"error,omitempty"`
}

// MailboxStatus is what the service knows about one mailbox.
type MailboxStatus struct {
	StorageID string `json:"storage_id"`
	UsedBytes int64  `json:"used_bytes"`
	Messages  int    `json:"messages"`
	// Blocked is set when the service stopped the mailbox from sending.
	Blocked       bool      `json:"blocked"`
	BlockedReason string    `json:"blocked_reason,omitempty"`
	BlockedAt     time.Time `json:"blocked_at,omitzero"`
}

// Event kinds.
const (
	EventBlocked        = "mailbox.blocked"
	EventSignInFailures = "mailbox.sign-in-failures"
	EventBounced        = "delivery.bounced"
	// EventSendLimit: a mailbox or domain reached a sending limit (at most
	// one event per mailbox and kind of limit per hour).
	EventSendLimit      = "mailbox.send-limit"
	EventListenerFailed = "listener.failed"
	// EventBlocklistRefused: a DNS blocklist refused to answer this
	// server's queries (answers in 127.255.255.0/24, typical through public
	// resolvers); at most one event per blocklist and day. Mail health
	// shows it.
	EventBlocklistRefused = "blocklist.refused"
	// EventNotForwarded: a message was delivered (or accepted) but not
	// forwarded to an outside address: it was judged Junk, or the domain
	// reached its sending limit.
	EventNotForwarded = "delivery.not-forwarded"
)

// Event is something the panel should record in its activity log. Detail is
// plain text without secrets.
type Event struct {
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	StorageID string    `json:"storage_id,omitempty"`
	Address   string    `json:"address,omitempty"`
	Detail    string    `json:"detail"`
}

// OutboundCheck is the result of trying to reach port 25 of another server.
type OutboundCheck struct {
	OK     bool   `json:"ok"`
	Target string `json:"target"`
	Error  string `json:"error,omitempty"`
}

// ParseDKIMKey parses a PEM PKCS#8 (or PKCS#1) RSA private key of at least
// 1024 bits.
func ParseDKIMKey(p string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(p))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unexpected PEM type %q", block.Type)
	}
	if err != nil {
		return nil, err
	}
	k, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	if k.N.BitLen() < 1024 {
		return nil, errors.New("RSA key too short")
	}
	return k, nil
}

// Client talks to the mail service's admin socket.
type Client struct {
	http *http.Client
}

// NewClient returns a client for the Unix socket at path.
func NewClient(path string) *Client {
	return &Client{http: &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}}
}

// base is ignored for Unix sockets except as the Host header.
const base = "http://ownpanel-mail"

// Error is an answer of the service that is not a success.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("mail service: %s (%d)", e.Msg, e.Status)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &Error{Status: resp.StatusCode, Msg: string(bytes.TrimSpace(msg))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

// Load replaces the service's configuration.
func (c *Client) Load(ctx context.Context, cfg *Config) error {
	return c.do(ctx, http.MethodPost, PathLoad, cfg, nil)
}

// Status asks the service how it is.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.do(ctx, http.MethodGet, PathStatus, nil, &s)
	return s, err
}

// Events returns events with an id greater than after.
func (c *Client) Events(ctx context.Context, after int64) ([]Event, error) {
	var ev []Event
	err := c.do(ctx, http.MethodGet, PathEvents+"?after="+strconv.FormatInt(after, 10), nil, &ev)
	return ev, err
}

// Unblock lets a blocked mailbox send again.
func (c *Client) Unblock(ctx context.Context, storageID string) error {
	if !ValidStorageID(storageID) {
		return errors.New("invalid storage id")
	}
	return c.do(ctx, http.MethodPost, PathMailbox+url.PathEscape(storageID)+"/unblock", nil, nil)
}

// DeleteMailbox removes all data of a mailbox.
func (c *Client) DeleteMailbox(ctx context.Context, storageID string) error {
	if !ValidStorageID(storageID) {
		return errors.New("invalid storage id")
	}
	return c.do(ctx, http.MethodDelete, PathMailbox+url.PathEscape(storageID), nil, nil)
}

// CheckOutbound asks the service to try a connection to port 25 elsewhere.
func (c *Client) CheckOutbound(ctx context.Context) (OutboundCheck, error) {
	var r OutboundCheck
	err := c.do(ctx, http.MethodPost, PathOutbound, nil, &r)
	return r, err
}
