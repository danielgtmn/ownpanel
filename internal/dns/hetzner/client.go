// Package hetzner implements dns.Provider on top of the DNS part of the
// Hetzner Cloud API (https://api.hetzner.cloud/v1).
//
// A token belongs to one project and is either Read or Read & Write. The API
// allows 3600 requests per hour per project; SetRecordSet therefore issues no
// write when nothing changes.
package hetzner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/dns"
)

const (
	// DefaultBaseURL is the Hetzner Cloud API endpoint.
	DefaultBaseURL = "https://api.hetzner.cloud/v1"

	perPage       = 50
	maxPages      = 1000
	maxRetries    = 4               // retries for 423 locked / 409 conflict
	maxRateWait   = 5 * time.Second // longest RateLimit-Reset we sleep for
	maxBodyBytes  = 8 << 20
	maxRecordsSet = 50
)

// Client is a dns.Provider for one Hetzner Cloud project.
type Client struct {
	token      string
	baseURL    string
	hc         *http.Client
	pollMin    time.Duration
	pollMax    time.Duration
	pollTotal  time.Duration // give up waiting for an action after this long
	retryDelay time.Duration
}

var _ dns.Provider = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API base URL (e.g. an httptest server).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient replaces the default HTTP client (30s timeout).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithPollIntervals sets the first and the maximum wait between action polls.
func WithPollIntervals(first, max time.Duration) Option {
	return func(c *Client) { c.pollMin, c.pollMax = first, max }
}

// WithRetryDelay sets the first wait before retrying a locked/conflicting
// request; it doubles on each retry.
func WithRetryDelay(d time.Duration) Option { return func(c *Client) { c.retryDelay = d } }

// New returns a client that authenticates with token.
func New(token string, opts ...Option) *Client {
	c := &Client{
		token:      token,
		baseURL:    DefaultBaseURL,
		hc:         &http.Client{Timeout: 30 * time.Second},
		pollMin:    500 * time.Millisecond,
		pollMax:    2 * time.Second,
		pollTotal:  2 * time.Minute,
		retryDelay: 500 * time.Millisecond,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ---- wire types ----

type apiZone struct {
	ID   json.Number `json:"id"`
	Name string      `json:"name"`
	Mode string      `json:"mode"`
}

type apiRecord struct {
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

type apiRRSet struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	TTL        *int   `json:"ttl"`
	Protection struct {
		Change bool `json:"change"`
	} `json:"protection"`
	Records []apiRecord `json:"records"`
}

type apiAction struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type apiMeta struct {
	Pagination struct {
		Page     int  `json:"page"`
		NextPage *int `json:"next_page"`
	} `json:"pagination"`
}

type apiErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details struct {
			Fields []struct {
				Name     string   `json:"name"`
				Messages []string `json:"messages"`
			} `json:"fields"`
		} `json:"details"`
	} `json:"error"`
}

// ---- dns.Provider ----

// Zones lists all zones of the project. Zones that are not primary are
// reported read-only.
func (c *Client) Zones(ctx context.Context) ([]dns.Zone, error) {
	var out []dns.Zone
	err := c.paginate(ctx, "/zones", nil, func(raw json.RawMessage) error {
		var page struct {
			Zones []apiZone `json:"zones"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, z := range page.Zones {
			out = append(out, dns.Zone{
				ID:       z.ID.String(),
				Name:     strings.ToLower(strings.TrimSuffix(z.Name, ".")),
				ReadOnly: z.Mode != "primary",
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RecordSets lists all record sets of a primary zone. For other zones it
// returns dns.ErrReadOnlyZone.
func (c *Client) RecordSets(ctx context.Context, zone string) ([]dns.RecordSet, error) {
	var out []dns.RecordSet
	err := c.paginate(ctx, "/zones/"+url.PathEscape(zone)+"/rrsets", nil, func(raw json.RawMessage) error {
		var page struct {
			RRSets []apiRRSet `json:"rrsets"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, r := range page.RRSets {
			rs, err := fromAPI(r)
			if err != nil {
				return err
			}
			out = append(out, rs)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetRecordSet creates or replaces a record set and waits until Hetzner has
// applied it. It reads the current state first and only writes what differs.
func (c *Client) SetRecordSet(ctx context.Context, zone string, rs dns.RecordSet) error {
	name, typ, err := normalizeKey(rs.Name, rs.Type)
	if err != nil {
		return err
	}
	if rs.TTL < 0 {
		return invalidf("the TTL must not be negative")
	}
	plain, err := cleanValues(typ, rs.Values)
	if err != nil {
		return err
	}
	base := "/zones/" + url.PathEscape(zone) + "/rrsets"
	one := base + "/" + url.PathEscape(name) + "/" + url.PathEscape(typ)

	var cur struct {
		RRSet apiRRSet `json:"rrset"`
	}
	err = c.do(ctx, http.MethodGet, one, nil, nil, &cur)
	if errors.Is(err, dns.ErrNotFound) {
		body := map[string]any{"name": name, "type": typ, "records": toAPIRecords(typ, plain)}
		if rs.TTL > 0 {
			body["ttl"] = rs.TTL
		}
		return c.write(ctx, http.MethodPost, base, body)
	}
	if err != nil {
		return err
	}

	existing, err := fromAPI(cur.RRSet)
	if err != nil {
		return err
	}
	sameValues := equalSets(typ, existing.Values, plain)
	sameTTL := existing.TTL == rs.TTL
	if sameValues && sameTTL {
		return nil
	}
	if cur.RRSet.Protection.Change {
		return fmt.Errorf("%w: %s %s", dns.ErrLocked, name, typ)
	}
	if !sameValues {
		body := map[string]any{"records": toAPIRecords(typ, plain)}
		if err := c.write(ctx, http.MethodPost, one+"/actions/set_records", body); err != nil {
			return err
		}
	}
	if !sameTTL {
		var ttl any // the key is mandatory; null = zone default
		if rs.TTL > 0 {
			ttl = rs.TTL
		}
		if err := c.write(ctx, http.MethodPost, one+"/actions/change_ttl", map[string]any{"ttl": ttl}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteRecordSet removes a record set; it returns nil if there was none.
func (c *Client) DeleteRecordSet(ctx context.Context, zone, name, typ string) error {
	name, typ, err := normalizeKey(name, typ)
	if err != nil {
		return err
	}
	p := "/zones/" + url.PathEscape(zone) + "/rrsets/" + url.PathEscape(name) + "/" + url.PathEscape(typ)
	err = c.write(ctx, http.MethodDelete, p, nil)
	if errors.Is(err, dns.ErrNotFound) {
		return nil
	}
	return err
}

// ---- conversion helpers ----

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", dns.ErrInvalid, fmt.Sprintf(format, args...))
}

// normalizeKey validates and normalizes a record set name and type.
func normalizeKey(name, typ string) (string, string, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" {
		name = "@"
	}
	for i := 0; i < len(name); i++ {
		if name[i] >= 0x80 {
			return "", "", invalidf("the name %q contains non-ASCII characters; use the punycode form (xn--...)", name)
		}
	}
	if strings.ContainsAny(name, "/ \t") {
		return "", "", invalidf("the name %q contains invalid characters", name)
	}
	typ = strings.ToUpper(strings.TrimSpace(typ))
	if typ == "" || strings.ContainsAny(typ, "/ ") {
		return "", "", invalidf("the record type %q is invalid", typ)
	}
	return name, typ, nil
}

// cleanValues checks the plain values and removes duplicates (keeping order).
func cleanValues(typ string, in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		if typ != "TXT" {
			v = strings.TrimSpace(v)
		}
		if v == "" && typ != "TXT" {
			return nil, invalidf("a record value must not be empty")
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, invalidf("a record set needs at least one value")
	}
	if len(out) > maxRecordsSet {
		return nil, invalidf("a record set can have at most %d values", maxRecordsSet)
	}
	return out, nil
}

func toAPIRecords(typ string, plain []string) []apiRecord {
	out := make([]apiRecord, len(plain))
	for i, v := range plain {
		if typ == "TXT" {
			v = quoteTXT(v)
		}
		out[i] = apiRecord{Value: v}
	}
	return out
}

func fromAPI(r apiRRSet) (dns.RecordSet, error) {
	typ := strings.ToUpper(r.Type)
	rs := dns.RecordSet{Name: r.Name, Type: typ, Locked: r.Protection.Change}
	if r.TTL != nil {
		rs.TTL = *r.TTL
	}
	for _, rec := range r.Records {
		v := rec.Value
		if typ == "TXT" {
			u, err := unquoteTXT(v)
			if err != nil {
				return rs, fmt.Errorf("hetzner: %s %s: %w", r.Name, typ, err)
			}
			v = u
		}
		rs.Values = append(rs.Values, v)
	}
	return rs, nil
}

// equalSets compares value lists as sets. Addresses are compared in their
// canonical text form.
func equalSets(typ string, a, b []string) bool {
	norm := func(in []string) []string {
		out := make([]string, len(in))
		for i, v := range in {
			if typ == "A" || typ == "AAAA" {
				if ip, err := netip.ParseAddr(strings.TrimSpace(v)); err == nil {
					v = ip.String()
				}
			}
			out[i] = v
		}
		sort.Strings(out)
		return out
	}
	x, y := norm(a), norm(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// ---- HTTP ----

// paginate walks all pages of a list endpoint, following meta.pagination.next_page.
func (c *Client) paginate(ctx context.Context, path string, q url.Values, handle func(json.RawMessage) error) error {
	page := 1
	for n := 0; n < maxPages; n++ {
		qq := url.Values{}
		for k, v := range q {
			qq[k] = v
		}
		qq.Set("page", strconv.Itoa(page))
		qq.Set("per_page", strconv.Itoa(perPage))
		var raw json.RawMessage
		if err := c.do(ctx, http.MethodGet, path, qq, nil, &raw); err != nil {
			return err
		}
		if err := handle(raw); err != nil {
			return fmt.Errorf("hetzner: reading response: %w", err)
		}
		var m struct {
			Meta apiMeta `json:"meta"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("hetzner: reading response: %w", err)
		}
		next := m.Meta.Pagination.NextPage
		if next == nil {
			return nil
		}
		if *next <= page {
			return fmt.Errorf("hetzner: pagination does not advance (page %d -> %d)", page, *next)
		}
		page = *next
	}
	return errors.New("hetzner: too many pages")
}

// write sends a modifying request and waits for the resulting action.
func (c *Client) write(ctx context.Context, method, path string, body any) error {
	var resp struct {
		Action *apiAction `json:"action"`
	}
	if err := c.do(ctx, method, path, nil, body, &resp); err != nil {
		return err
	}
	if resp.Action == nil {
		return nil
	}
	return c.waitAction(ctx, *resp.Action)
}

// waitAction polls the action until it is no longer running.
func (c *Client) waitAction(ctx context.Context, a apiAction) error {
	deadline := time.Now().Add(c.pollTotal)
	delay := c.pollMin
	for {
		switch a.Status {
		case "success":
			return nil
		case "error":
			msg, code := "unknown error", ""
			if a.Error != nil {
				msg, code = a.Error.Message, a.Error.Code
			}
			if code != "" {
				return fmt.Errorf("hetzner: the change failed: %s (%s)", msg, code)
			}
			return fmt.Errorf("hetzner: the change failed: %s", msg)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hetzner: the change was not applied in time (action %d still %q)", a.ID, a.Status)
		}
		if err := sleep(ctx, delay); err != nil {
			return err
		}
		if delay *= 2; delay > c.pollMax {
			delay = c.pollMax
		}
		var r struct {
			Action apiAction `json:"action"`
		}
		if err := c.do(ctx, http.MethodGet, "/actions/"+strconv.FormatInt(a.ID, 10), nil, nil, &r); err != nil {
			return err
		}
		a = r.Action
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// scrubbed hides the token in transport errors while keeping the cause
// reachable through errors.Is/As (e.g. context.Canceled).
type scrubbed struct {
	msg string
	err error
}

func (e *scrubbed) Error() string { return e.msg }
func (e *scrubbed) Unwrap() error { return e.err }

// do performs one logical request, retrying locked/conflict/rate-limit
// responses. out may be nil.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	retries, rateWaited := 0, false
	for {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return c.scrub(err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("User-Agent", "ownpanel")
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return c.scrub(err)
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()
		if rerr != nil {
			return c.scrub(rerr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out != nil && len(data) > 0 {
				if err := json.Unmarshal(data, out); err != nil {
					return fmt.Errorf("hetzner: unexpected response: %w", err)
				}
			}
			return nil
		}

		var eb apiErrorBody
		_ = json.Unmarshal(data, &eb)
		code, msg := eb.Error.Code, eb.Error.Message
		status := resp.StatusCode

		retryable := (status == http.StatusLocked && code == "locked") || status == http.StatusConflict
		if retryable && retries < maxRetries {
			if err := sleep(ctx, c.retryDelay<<retries); err != nil {
				return err
			}
			retries++
			continue
		}
		if status == http.StatusTooManyRequests && !rateWaited {
			if wait, ok := rateLimitWait(resp.Header.Get("RateLimit-Reset"), time.Now()); ok && wait <= maxRateWait {
				rateWaited = true
				if err := sleep(ctx, wait); err != nil {
					return err
				}
				continue
			}
		}
		return mapError(status, code, msg, eb)
	}
}

func (c *Client) scrub(err error) error {
	if c.token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), c.token, "***")
	return &scrubbed{msg: msg, err: err}
}

// rateLimitWait interprets RateLimit-Reset, which may be a number of seconds
// or a UNIX timestamp.
func rateLimitWait(v string, now time.Time) (time.Duration, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	if n > 1_000_000_000 { // UNIX timestamp
		d := time.Unix(n, 0).Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return time.Duration(n) * time.Second, true
}

func mapError(status int, code, msg string, eb apiErrorBody) error {
	detail := msg
	if detail == "" {
		detail = http.StatusText(status)
	}
	switch {
	case status == http.StatusUnauthorized:
		return fmt.Errorf("%w (%s)", dns.ErrUnauthorized, detail)
	case status == http.StatusForbidden:
		return fmt.Errorf("%w (%s)", dns.ErrForbidden, detail)
	case status == http.StatusNotFound:
		return fmt.Errorf("%w (%s)", dns.ErrNotFound, detail)
	case status == http.StatusUnprocessableEntity && code == "incorrect_zone_mode":
		return fmt.Errorf("%w (%s)", dns.ErrReadOnlyZone, detail)
	case status == http.StatusUnprocessableEntity && code == "invalid_input":
		var parts []string
		for _, f := range eb.Error.Details.Fields {
			if len(f.Messages) > 0 {
				parts = append(parts, f.Name+": "+strings.Join(f.Messages, ", "))
			}
		}
		if len(parts) > 0 {
			detail += " [" + strings.Join(parts, "; ") + "]"
		}
		return fmt.Errorf("%w: %s", dns.ErrInvalid, detail)
	case status == http.StatusLocked && code == "protected":
		return fmt.Errorf("%w (%s)", dns.ErrLocked, detail)
	case status == http.StatusLocked:
		return fmt.Errorf("%w (still locked after retries: %s)", dns.ErrLocked, detail)
	case status == http.StatusConflict:
		return fmt.Errorf("hetzner: conflicting change in progress, try again (%s)", detail)
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w (%s)", dns.ErrRateLimited, detail)
	}
	if code != "" {
		return fmt.Errorf("hetzner: %s (HTTP %d, %s)", detail, status, code)
	}
	return fmt.Errorf("hetzner: %s (HTTP %d)", detail, status)
}
