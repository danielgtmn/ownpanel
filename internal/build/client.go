package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is the panel's side of the worker's socket. The worker runs
// builds, that is other people's code, so nothing it answers is trusted:
// answers are limited in size, the log is text for people and never
// interpreted, and the output is an archive that goes through the same
// checks as an upload (internal/deploy).
type Client struct {
	// quick is for requests that are answered at once; stream for the
	// source, the log and the output, which take as long as they take (the
	// caller's context ends them).
	quick  *http.Client
	stream *http.Client
}

const (
	// maxAnswerBytes bounds a JSON answer and an error text.
	maxAnswerBytes = 1 << 20
	quickTimeout   = 15 * time.Second
	dialTimeout    = 3 * time.Second
)

// NewClient returns a client for the worker listening on the Unix socket at
// path.
func NewClient(path string) *Client {
	transport := func() *http.Transport {
		return &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: dialTimeout}
				return d.DialContext(ctx, "unix", path)
			},
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
			ResponseHeaderTimeout: 2 * time.Minute,
		}
	}
	return &Client{
		quick:  &http.Client{Transport: transport(), Timeout: quickTimeout},
		stream: &http.Client{Transport: transport()},
	}
}

// Error is an answer of the worker that is not a success. Message is meant
// for people.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// IsNotFound tells whether err says the worker does not know the build:
// it was removed, it expired, or the worker restarted since (which stops
// and forgets every build).
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// IsUnavailable tells whether err says builds cannot run on this server
// right now (Status says why).
func IsUnavailable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusServiceUnavailable
}

func (c *Client) request(ctx context.Context, hc *http.Client, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://build-worker"+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("build worker: %w", err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		msg := strings.TrimSpace(strings.Map(printable, strings.ToValidUTF8(string(text), "?")))
		if msg == "" {
			msg = "the build service answered " + resp.Status
		}
		return nil, &Error{Status: resp.StatusCode, Message: msg}
	}
	return resp, nil
}

// printable keeps an error text of the worker to one line of plain text.
func printable(r rune) rune {
	if r == '\n' || r == '\t' {
		return ' '
	}
	if r < 0x20 || r == 0x7f {
		return -1
	}
	return r
}

func (c *Client) json(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, contentType = bytes.NewReader(raw), "application/json"
	}
	resp, err := c.request(ctx, c.quick, method, path, body, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswerBytes)).Decode(out); err != nil {
		return fmt.Errorf("build worker: unreadable answer: %w", err)
	}
	return nil
}

func buildPath(id uint64, rest string) string {
	return PathBuilds + "/" + strconv.FormatUint(id, 10) + rest
}

// Status asks the worker whether builds can run and with which limits.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	err := c.json(ctx, http.MethodGet, PathStatus, nil, &st)
	return st, err
}

// Create registers a build. It does not run before Source and Start.
func (c *Client) Create(ctx context.Context, spec Spec) (Build, error) {
	var b Build
	err := c.json(ctx, http.MethodPost, PathBuilds, spec, &b)
	return b, err
}

// Source hands the build its source tree: a tar stream, gzipped or not,
// of plain files, folders and links; strip leading folders are dropped from
// every name (1 for the archive GitHub makes of a commit). The worker checks
// it now and unpacks it as untrusted when the build starts (limits, no
// devices, no owners, nothing outside the workspace).
func (c *Client) Source(ctx context.Context, id uint64, tree io.Reader, strip int) (Build, error) {
	p := buildPath(id, "/source")
	if strip > 0 {
		p += "?strip=" + strconv.Itoa(strip)
	}
	resp, err := c.request(ctx, c.stream, http.MethodPut, p, io.NopCloser(tree), "application/x-tar")
	if err != nil {
		return Build{}, err
	}
	defer resp.Body.Close()
	var b Build
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswerBytes)).Decode(&b); err != nil {
		return Build{}, fmt.Errorf("build worker: unreadable answer: %w", err)
	}
	return b, nil
}

// Start puts the build in line; it runs as soon as a slot is free.
func (c *Client) Start(ctx context.Context, id uint64) (Build, error) {
	var b Build
	err := c.json(ctx, http.MethodPost, buildPath(id, "/start"), nil, &b)
	return b, err
}

// Cancel stops the build if it has not ended yet.
func (c *Client) Cancel(ctx context.Context, id uint64) (Build, error) {
	var b Build
	err := c.json(ctx, http.MethodPost, buildPath(id, "/cancel"), nil, &b)
	return b, err
}

// Get returns the state of a build.
func (c *Client) Get(ctx context.Context, id uint64) (Build, error) {
	var b Build
	err := c.json(ctx, http.MethodGet, buildPath(id, ""), nil, &b)
	return b, err
}

// List returns every build the worker knows, oldest first.
func (c *Client) List(ctx context.Context) ([]Build, error) {
	var bs []Build
	err := c.json(ctx, http.MethodGet, PathBuilds, nil, &bs)
	return bs, err
}

// Log returns the build's log from offset on. With follow the stream stays
// open until the build has ended (or ctx is done), so a running build can
// be watched. The log is at most Limits.LogBytes long; it is what the
// build printed, with the values of Spec.Secrets blanked out: show it as
// text, never interpret it.
func (c *Client) Log(ctx context.Context, id uint64, offset int64, follow bool) (io.ReadCloser, error) {
	q := url.Values{}
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	if follow {
		q.Set("follow", "1")
	}
	p := buildPath(id, "/log")
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	resp, err := c.request(ctx, c.stream, http.MethodGet, p, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Output returns the output folder of a successful build as a tar.gz of
// plain files and folders. Publish it through deploy.Manager.Deploy, which
// checks it like any upload; do not unpack it any other way.
func (c *Client) Output(ctx context.Context, id uint64) (io.ReadCloser, error) {
	resp, err := c.request(ctx, c.stream, http.MethodGet, buildPath(id, "/output"), nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Remove stops the build if it runs and deletes its workspace, log and
// output. Unknown builds are fine (the worker forgets them by itself).
func (c *Client) Remove(ctx context.Context, id uint64) error {
	err := c.json(ctx, http.MethodDelete, buildPath(id, ""), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// Caches lists the build caches and their sizes.
func (c *Client) Caches(ctx context.Context) ([]Cache, error) {
	var cs []Cache
	err := c.json(ctx, http.MethodGet, PathCaches, nil, &cs)
	return cs, err
}

// ClearCache deletes the build cache of a site. Call it when a site is
// deleted, so a later site of the same name never gets its files.
func (c *Client) ClearCache(ctx context.Context, site string) error {
	if !ValidSite(site) {
		return errors.New("the site name is not valid")
	}
	return c.json(ctx, http.MethodDelete, PathCaches+"/"+site, nil, nil)
}
