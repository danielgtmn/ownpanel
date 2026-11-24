// Package hetznertest is an in-memory fake of the DNS endpoints of the Hetzner
// Cloud API, served by httptest. It is meant for tests of the hetzner client
// and of any package that uses it.
//
// The fake applies a write immediately but reports the resulting action as
// "running" for Server.ActionPolls polls, so callers must poll. It validates
// what the real API is documented to validate (record count, distinct values,
// quoted TXT values with sub-strings of at most 255 bytes, mandatory ttl key).
package hetznertest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Field is one invalid input field of an Error.
type Field struct {
	Name     string
	Messages []string
}

// Error is an API error to inject.
type Error struct {
	Status  int
	Code    string
	Message string
	Fields  []Field
	Header  http.Header
}

// RRSet is a record set as stored by the fake. Records hold wire values (TXT
// values are quoted). TTL 0 means null (zone default).
type RRSet struct {
	Name      string
	Type      string
	TTL       int
	Records   []string
	Protected bool
}

type zone struct {
	id   int
	name string
	mode string
	ttl  int
	sets map[string]*RRSet // key: name/type
}

type action struct {
	id        int
	command   string
	remaining int // polls that still report "running"
	err       *Error
}

type injected struct {
	match string
	times int // <0: forever
	err   Error
}

// Server is the fake API. Create it with New.
type Server struct {
	*httptest.Server

	// Token is the accepted bearer token.
	Token string
	// ReadOnlyToken makes every write fail with 403 forbidden.
	ReadOnlyToken bool
	// ActionPolls is the number of polls an action reports "running" before it
	// succeeds. 0 means the write response already says "success".
	ActionPolls int
	// ActionFailure, if set, makes every new action end with this error.
	ActionFailure *Error

	mu      sync.Mutex
	zones   []*zone
	actions map[int]*action
	nextID  int
	calls   []string
	rules   []*injected
}

// New starts a fake that accepts token. It is closed when the test ends.
func New(t testing.TB, token string) *Server {
	s := &Server{Token: token, actions: map[int]*action{}, nextID: 1}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Server.Close)
	return s
}

// BaseURL is the value for hetzner.WithBaseURL.
func (s *Server) BaseURL() string { return s.URL + "/v1" }

// AddZone adds a zone with mode "primary" or "secondary" and returns its id.
func (s *Server) AddZone(name, mode string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	z := &zone{id: 1000 + len(s.zones) + 1, name: name, mode: mode, ttl: 3600, sets: map[string]*RRSet{}}
	s.zones = append(s.zones, z)
	return z.id
}

// AddRRSet stores a record set directly (no call is recorded). records are
// wire values.
func (s *Server) AddRRSet(zoneName string, rs RRSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	z := s.findZone(zoneName)
	if z == nil {
		panic("hetznertest: unknown zone " + zoneName)
	}
	cp := rs
	z.sets[rs.Name+"/"+rs.Type] = &cp
}

// RRSet returns a stored record set.
func (s *Server) RRSet(zoneName, name, typ string) (RRSet, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	z := s.findZone(zoneName)
	if z == nil {
		return RRSet{}, false
	}
	rs, ok := z.sets[name+"/"+typ]
	if !ok {
		return RRSet{}, false
	}
	return *rs, true
}

// Fail makes the next `times` requests whose "METHOD /path" (without query,
// path relative to /v1, e.g. "POST /zones/example.com/rrsets") equals match
// fail with e. times < 0 means forever.
func (s *Server) Fail(match string, times int, e Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, &injected{match: match, times: times, err: e})
}

// Calls returns every request received so far as "METHOD /path" (without
// query, path relative to /v1).
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// Writes returns the Calls that modify state (POST and DELETE).
func (s *Server) Writes() []string {
	var out []string
	for _, c := range s.Calls() {
		if strings.HasPrefix(c, "POST ") || strings.HasPrefix(c, "DELETE ") {
			out = append(out, c)
		}
	}
	return out
}

// ResetCalls forgets the recorded calls.
func (s *Server) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

func (s *Server) findZone(idOrName string) *zone {
	for _, z := range s.zones {
		if z.name == idOrName || strconv.Itoa(z.id) == idOrName {
			return z
		}
	}
	return nil
}

// ---- HTTP ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, e Error) {
	for k, vs := range e.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	type fld struct {
		Name     string   `json:"name"`
		Messages []string `json:"messages"`
	}
	fields := []fld{}
	for _, f := range e.Fields {
		fields = append(fields, fld{f.Name, f.Messages})
	}
	body := map[string]any{"error": map[string]any{
		"code": e.Code, "message": e.Message,
		"details": map[string]any{"fields": fields},
	}}
	writeJSON(w, e.Status, body)
}

func notFound(w http.ResponseWriter) {
	writeErr(w, Error{Status: 404, Code: "not_found", Message: "resource not found"})
}

func invalid(w http.ResponseWriter, field, msg string) {
	writeErr(w, Error{Status: 422, Code: "invalid_input", Message: "invalid input in field '" + field + "'",
		Fields: []Field{{Name: field, Messages: []string{msg}}}})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := strings.TrimPrefix(r.URL.EscapedPath(), "/v1")
	call := r.Method + " " + path
	s.calls = append(s.calls, call)

	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		writeErr(w, Error{Status: 401, Code: "unauthorized", Message: "unable to authenticate"})
		return
	}
	if r.Header.Get("User-Agent") != "ownpanel" {
		writeErr(w, Error{Status: 400, Code: "invalid_input", Message: "unexpected user agent"})
		return
	}
	for _, ru := range s.rules {
		if ru.match == call && ru.times != 0 {
			if ru.times > 0 {
				ru.times--
			}
			writeErr(w, ru.err)
			return
		}
	}
	if s.ReadOnlyToken && r.Method != http.MethodGet {
		writeErr(w, Error{Status: 403, Code: "forbidden", Message: "insufficient permissions"})
		return
	}

	seg := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == "GET" && len(seg) == 1 && seg[0] == "zones":
		s.listZones(w, r)
	case r.Method == "GET" && len(seg) == 2 && seg[0] == "actions":
		s.getAction(w, seg[1])
	case len(seg) >= 3 && seg[0] == "zones" && seg[2] == "rrsets":
		z := s.findZone(seg[1])
		if z == nil {
			notFound(w)
			return
		}
		s.rrsets(w, r, z, seg[3:])
	default:
		notFound(w)
	}
}

func paginate(w http.ResponseWriter, r *http.Request, total int) (lo, hi int, meta map[string]any, ok bool) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if per < 1 {
		per = 25
	}
	if per > 50 {
		invalid(w, "per_page", "must be at most 50")
		return 0, 0, nil, false
	}
	last := (total + per - 1) / per
	if last < 1 {
		last = 1
	}
	lo = (page - 1) * per
	if lo > total {
		lo = total
	}
	hi = lo + per
	if hi > total {
		hi = total
	}
	var prev, next any
	if page > 1 {
		prev = page - 1
	}
	if page < last {
		next = page + 1
	}
	meta = map[string]any{"pagination": map[string]any{
		"page": page, "per_page": per, "previous_page": prev, "next_page": next,
		"last_page": last, "total_entries": total,
	}}
	return lo, hi, meta, true
}

func (s *Server) listZones(w http.ResponseWriter, r *http.Request) {
	lo, hi, meta, ok := paginate(w, r, len(s.zones))
	if !ok {
		return
	}
	out := []map[string]any{}
	for _, z := range s.zones[lo:hi] {
		out = append(out, map[string]any{"id": z.id, "name": z.name, "mode": z.mode, "ttl": z.ttl, "status": "ok"})
	}
	writeJSON(w, 200, map[string]any{"zones": out, "meta": meta})
}

func rrsetJSON(z *zone, rs *RRSet) map[string]any {
	recs := []map[string]any{}
	for _, v := range rs.Records {
		recs = append(recs, map[string]any{"value": v, "comment": nil})
	}
	var ttl any
	if rs.TTL > 0 {
		ttl = rs.TTL
	}
	return map[string]any{
		"id": rs.Name + "/" + rs.Type, "name": rs.Name, "type": rs.Type, "ttl": ttl,
		"protection": map[string]any{"change": rs.Protected}, "records": recs, "zone": z.id,
	}
}

func (s *Server) newAction(w http.ResponseWriter, command string) {
	a := &action{id: s.nextID, command: command, remaining: s.ActionPolls, err: s.ActionFailure}
	s.nextID++
	s.actions[a.id] = a
	writeJSON(w, 201, map[string]any{"action": s.actionJSON(a, a.remaining > 0)})
}

func (s *Server) actionJSON(a *action, running bool) map[string]any {
	m := map[string]any{"id": a.id, "command": a.command, "status": "success", "progress": 100, "error": nil}
	switch {
	case running:
		m["status"], m["progress"] = "running", 50
	case a.err != nil:
		m["status"] = "error"
		m["error"] = map[string]any{"code": a.err.Code, "message": a.err.Message}
	}
	return m
}

func (s *Server) getAction(w http.ResponseWriter, id string) {
	n, _ := strconv.Atoi(id)
	a := s.actions[n]
	if a == nil {
		notFound(w)
		return
	}
	running := a.remaining > 0
	if running {
		a.remaining--
	}
	writeJSON(w, 200, map[string]any{"action": s.actionJSON(a, running)})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// checkRecords validates a "records" payload and returns the wire values.
func checkRecords(w http.ResponseWriter, typ string, recs []struct {
	Value string `json:"value"`
}) ([]string, bool) {
	if len(recs) < 1 || len(recs) > 50 {
		invalid(w, "records", "must contain between 1 and 50 records")
		return nil, false
	}
	seen := map[string]bool{}
	var out []string
	for i, rec := range recs {
		if seen[rec.Value] {
			invalid(w, fmt.Sprintf("records[%d].value", i), "duplicate value")
			return nil, false
		}
		seen[rec.Value] = true
		if typ == "TXT" {
			if msg := checkTXT(rec.Value); msg != "" {
				invalid(w, fmt.Sprintf("records[%d].value", i), msg)
				return nil, false
			}
		}
		out = append(out, rec.Value)
	}
	return out, true
}

// checkTXT verifies the quoted sub-string form with sub-strings <= 255 bytes
// (after resolving backslash escapes).
func checkTXT(v string) string {
	i := 0
	count := 0
	for i < len(v) {
		if v[i] == ' ' {
			i++
			continue
		}
		if v[i] != '"' {
			return "TXT value must be enclosed in double quotes"
		}
		i++
		n := 0
		closed := false
		for i < len(v) {
			c := v[i]
			if c == '\\' {
				if i+1 >= len(v) {
					return "dangling escape"
				}
				i += 2
				n++
				continue
			}
			if c == '"' {
				closed = true
				i++
				break
			}
			i++
			n++
		}
		if !closed {
			return "missing closing quote"
		}
		if n > 255 {
			return "TXT sub-string longer than 255 bytes"
		}
		count++
	}
	if count == 0 {
		return "TXT value must be enclosed in double quotes"
	}
	return ""
}

func (s *Server) rrsets(w http.ResponseWriter, r *http.Request, z *zone, rest []string) {
	if z.mode != "primary" {
		writeErr(w, Error{Status: 422, Code: "incorrect_zone_mode", Message: "the zone is not in primary mode"})
		return
	}
	switch {
	case len(rest) == 0 && r.Method == "GET":
		var keys []string
		for k, rs := range z.sets {
			if t := r.URL.Query()["type"]; len(t) > 0 && !contains(t, rs.Type) {
				continue
			}
			if n := r.URL.Query().Get("name"); n != "" && n != rs.Name {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lo, hi, meta, ok := paginate(w, r, len(keys))
		if !ok {
			return
		}
		out := []map[string]any{}
		for _, k := range keys[lo:hi] {
			out = append(out, rrsetJSON(z, z.sets[k]))
		}
		writeJSON(w, 200, map[string]any{"rrsets": out, "meta": meta})

	case len(rest) == 0 && r.Method == "POST":
		var body struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			TTL     *int   `json:"ttl"`
			Records []struct {
				Value string `json:"value"`
			} `json:"records"`
		}
		if err := decode(r, &body); err != nil {
			invalid(w, "body", "invalid JSON")
			return
		}
		if _, exists := z.sets[body.Name+"/"+body.Type]; exists {
			invalid(w, "name", "record set already exists")
			return
		}
		vals, ok := checkRecords(w, body.Type, body.Records)
		if !ok {
			return
		}
		rs := &RRSet{Name: body.Name, Type: body.Type, Records: vals}
		if body.TTL != nil {
			rs.TTL = *body.TTL
		}
		z.sets[body.Name+"/"+body.Type] = rs
		s.newAction(w, "create_rrset")

	case len(rest) == 2 && (r.Method == "GET" || r.Method == "DELETE"):
		rs := z.sets[rest[0]+"/"+rest[1]]
		if rs == nil {
			notFound(w)
			return
		}
		if r.Method == "GET" {
			writeJSON(w, 200, map[string]any{"rrset": rrsetJSON(z, rs)})
			return
		}
		if rs.Protected {
			writeErr(w, Error{Status: 423, Code: "protected", Message: "resource is protected"})
			return
		}
		delete(z.sets, rest[0]+"/"+rest[1])
		s.newAction(w, "delete_rrset")

	case len(rest) == 4 && rest[2] == "actions" && r.Method == "POST":
		rs := z.sets[rest[0]+"/"+rest[1]]
		if rs == nil {
			notFound(w)
			return
		}
		if rs.Protected {
			writeErr(w, Error{Status: 423, Code: "protected", Message: "resource is protected"})
			return
		}
		switch rest[3] {
		case "set_records":
			var body struct {
				Records []struct {
					Value string `json:"value"`
				} `json:"records"`
			}
			if err := decode(r, &body); err != nil {
				invalid(w, "body", "invalid JSON")
				return
			}
			vals, ok := checkRecords(w, rs.Type, body.Records)
			if !ok {
				return
			}
			rs.Records = vals
			s.newAction(w, "set_rrset_records")
		case "change_ttl":
			var body map[string]json.RawMessage
			if err := decode(r, &body); err != nil {
				invalid(w, "body", "invalid JSON")
				return
			}
			raw, present := body["ttl"]
			if !present {
				invalid(w, "ttl", "the ttl key is required (use null for the zone default)")
				return
			}
			var ttl *int
			if err := json.Unmarshal(raw, &ttl); err != nil {
				invalid(w, "ttl", "must be an integer or null")
				return
			}
			rs.TTL = 0
			if ttl != nil {
				rs.TTL = *ttl
			}
			s.newAction(w, "change_rrset_ttl")
		default:
			notFound(w)
		}
	default:
		notFound(w)
	}
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}
