// Package github is a small client for the parts of GitHub the panel uses
// to connect repositories: creating a GitHub App from a manifest, signing in
// as that App (a short JWT signed with its private key), its installations,
// installation access tokens and the repositories an installation may see.
//
// Everything GitHub answers is untrusted input: answers are size limited,
// decoded into fixed types and every value the panel keeps or shows is
// checked. Errors never contain a URL (the manifest code is part of one),
// a token, a JWT or a private key.
//
// GitHub Enterprise Server is not supported, but nothing here assumes
// github.com: the web address is configurable and the API address follows
// it (api.github.com for github.com, <web>/api/v3 otherwise).
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// DefaultURL is the address of GitHub itself.
	DefaultURL = "https://github.com"

	// apiVersion is sent with every API request. GitHub supports a version
	// for at least 24 months after its successor appears.
	apiVersion = "2022-11-28"
	// userAgent identifies the panel without anything about the server.
	userAgent = "ownpanel"

	maxBodyBytes = 4 << 20 // one answer; a page of 100 repositories is far below
	perPage      = 100
	// defaultMaxPages caps the repositories listed for one installation
	// (3000) and the installations of one App.
	defaultMaxPages = 30
)

// Client talks to one GitHub (github.com by default).
type Client struct {
	web      *url.URL
	api      string
	hc       *http.Client
	now      func() time.Time
	maxPages int
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client (30 s timeout, no
// redirects followed).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithClock replaces time.Now, for the JWT's times.
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// WithMaxPages limits how many pages of 100 are read from a list.
func WithMaxPages(n int) Option { return func(c *Client) { c.maxPages = n } }

// New returns a client for the GitHub at webURL ("" means github.com). It
// must be https, or http on a loopback address (a stand-in for tests and
// development), without user, query or fragment.
func New(webURL string, opts ...Option) (*Client, error) {
	if webURL == "" {
		webURL = DefaultURL
	}
	u, err := url.Parse(strings.TrimRight(webURL, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("the GitHub address %q must look like https://github.com", webURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return nil, fmt.Errorf("the GitHub address %q must use https", webURL)
		}
	default:
		return nil, fmt.Errorf("the GitHub address %q must use https", webURL)
	}
	if u.Path != "" {
		return nil, fmt.Errorf("the GitHub address %q must not have a path", webURL)
	}
	c := &Client{
		web: u,
		hc: &http.Client{
			Timeout: 30 * time.Second,
			// GitHub answers the calls used here directly; a redirect
			// would carry the Authorization header somewhere unexpected.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now:      time.Now,
		maxPages: defaultMaxPages,
	}
	if strings.EqualFold(u.Host, "github.com") {
		c.api = "https://api.github.com"
	} else {
		c.api = u.String() + "/api/v3"
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// WebURL is GitHub's address for browsers, without a trailing slash.
func (c *Client) WebURL() string { return c.web.String() }

// ---- addresses for the browser ----

// ManifestFormURL is where the manifest form is posted: the person's own
// account when org is "", else that organisation. state comes back with
// the person.
func (c *Client) ManifestFormURL(org, state string) string {
	p := "/settings/apps/new"
	if org != "" {
		p = "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	return c.WebURL() + p + "?" + url.Values{"state": {state}}.Encode()
}

// InstallURL is the page where the App is installed and its repositories
// are chosen. state comes back with the person, if GitHub passes it on.
func (c *Client) InstallURL(slug, state string) string {
	u := c.WebURL() + "/apps/" + url.PathEscape(slug) + "/installations/new"
	if state != "" {
		u += "?" + url.Values{"state": {state}}.Encode()
	}
	return u
}

// AppSettingsURL is the App's settings page on GitHub, where it can be
// deleted (under "Advanced").
func (c *Client) AppSettingsURL(owner Account, slug string) string {
	if owner.Type == AccountOrganization {
		return c.WebURL() + "/organizations/" + url.PathEscape(owner.Login) + "/settings/apps/" + url.PathEscape(slug)
	}
	return c.WebURL() + "/settings/apps/" + url.PathEscape(slug)
}

// InstallationSettingsURL is where the owner changes which repositories an
// installation may see.
func (c *Client) InstallationSettingsURL(account Account, id int64) string {
	if account.Type == AccountOrganization {
		return fmt.Sprintf("%s/organizations/%s/settings/installations/%d", c.WebURL(), url.PathEscape(account.Login), id)
	}
	return fmt.Sprintf("%s/settings/installations/%d", c.WebURL(), id)
}

// ---- errors ----

// Kinds of failure. Every error of this package wraps one of them.
var (
	ErrUnauthorized = errors.New("GitHub did not accept the credentials")
	ErrForbidden    = errors.New("GitHub refused the request")
	ErrNotFound     = errors.New("not found at GitHub")
	ErrInvalid      = errors.New("GitHub could not process the request")
	ErrRateLimited  = errors.New("GitHub's request limit is reached")
	ErrUnavailable  = errors.New("GitHub could not be reached")
	// ErrBadAnswer: GitHub's answer was not what the documentation
	// describes (or too large).
	ErrBadAnswer = errors.New("GitHub sent an unexpected answer")
)

// Error is a failed request. Message is GitHub's own explanation, cleaned
// up and shortened; it never contains a secret the panel sent.
type Error struct {
	Kind    error
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Kind.Error()
	}
	return e.Kind.Error() + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Kind }

func badAnswer(format string, args ...any) error {
	return &Error{Kind: ErrBadAnswer, Message: fmt.Sprintf(format, args...)}
}

// cleanMessage keeps a short, single-line, printable message.
func cleanMessage(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > 200 {
		s = string([]rune(s)[:200]) + "…"
	}
	return s
}

// ---- requests ----

// do sends one API request. auth is the Authorization header ("" for
// none); body, if not nil, is sent as JSON. The answer must have status
// want and is decoded into out (if not nil).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, auth string, body, out any, want int) (http.Header, error) {
	target := c.api + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		// Only possible with a broken base URL; the path may hold a code.
		return nil, &Error{Kind: ErrInvalid, Message: "the request could not be built"}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, transportError(ctx, err)
	}
	if len(data) > maxBodyBytes {
		return nil, badAnswer("the answer is larger than %d MB", maxBodyBytes>>20)
	}
	if resp.StatusCode != want {
		return nil, statusError(resp, data)
	}
	if out != nil {
		if err := decodeStrict(data, out); err != nil {
			return nil, err
		}
	}
	return resp.Header, nil
}

// transportError drops the *url.Error around a network failure: its text
// contains the request URL, and with it the manifest code.
func transportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
		if ue.Timeout() {
			return &Error{Kind: ErrUnavailable, Message: "no answer in time"}
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{Kind: ErrUnavailable, Message: "no answer in time"}
	}
	return &Error{Kind: ErrUnavailable, Message: cleanMessage(err.Error())}
}

func statusError(resp *http.Response, data []byte) error {
	var body struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &body)
	e := &Error{Status: resp.StatusCode, Message: cleanMessage(body.Message)}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		e.Kind = ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""):
		e.Kind = ErrRateLimited
	case resp.StatusCode == http.StatusForbidden:
		e.Kind = ErrForbidden
	case resp.StatusCode == http.StatusNotFound:
		e.Kind = ErrNotFound
	case resp.StatusCode == http.StatusUnprocessableEntity, resp.StatusCode == http.StatusBadRequest:
		e.Kind = ErrInvalid
	case resp.StatusCode >= 500:
		e.Kind = ErrUnavailable
	default:
		e.Kind = ErrBadAnswer
		if e.Message == "" {
			e.Message = fmt.Sprintf("status %d", resp.StatusCode)
		}
	}
	return e
}

// decodeStrict decodes exactly one JSON value into out. Unknown fields are
// allowed (GitHub adds fields all the time); trailing data is not.
func decodeStrict(data []byte, out any) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return badAnswer("the answer is not the expected JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return badAnswer("the answer has data after its end")
	}
	return nil
}
