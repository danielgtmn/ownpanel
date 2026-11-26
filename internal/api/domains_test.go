package api

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDomainRoutes(t *testing.T) {
	d := newDNSEnv(t, "203.0.113.5")

	rec := d.bob("GET", "/api/domains", "")
	if rec.Code != 200 || rec.Body.String() != "[]\n" {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body)
	}
	wantStatus(t, d.bob("POST", "/api/domains", `{"name":"www.example.com"}`), 400, "not a domain you can register")
	wantStatus(t, d.bob("POST", "/api/domains", `{"name":"203.0.113.5"}`), 400, "not an IP address")
	wantStatus(t, d.bob("POST", "/api/domains", `{"nme":"x"}`), 400, "")

	rec = d.bob("POST", "/api/domains", `{"name":"Elsewhere.ORG"}`)
	wantStatus(t, rec, 200, "")
	wantKeys(t, rec.Body.Bytes(), "name", "created_at", "used_by")
	var got struct {
		Name      string   `json:"name"`
		CreatedAt string   `json:"created_at"`
		UsedBy    []string `json:"used_by"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if _, err := time.Parse(time.RFC3339, got.CreatedAt); err != nil || got.Name != "elsewhere.org" || got.UsedBy == nil {
		t.Fatalf("added: %s", rec.Body)
	}
	wantStatus(t, d.bob("POST", "/api/domains", `{"name":"elsewhere.org"}`), 200, "")

	rec = d.bob("GET", "/api/domains", "")
	if list := decode[[]json.RawMessage](t, rec); rec.Code != 200 || len(list) != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	// Per person: root neither sees nor removes bob's entry.
	if rec := d.root("GET", "/api/domains", ""); rec.Body.String() != "[]\n" {
		t.Fatalf("root sees: %s", rec.Body)
	}
	wantStatus(t, d.root("DELETE", "/api/domains/elsewhere.org", ""), 404, "not in your panel")

	// A read-only token may list, not change.
	_, read := d.createToken("read", "null")
	wantStatus(t, d.call(req{method: "GET", path: "/api/domains", token: read}), 200, "")
	wantStatus(t, d.call(req{method: "POST", path: "/api/domains", body: `{"name":"x.org"}`, token: read}), 403, "write access")

	rec = d.bob("DELETE", "/api/domains/elsewhere.org", "")
	if rec.Code != 200 || rec.Body.String() != `{"name":"elsewhere.org","used_by":[]}`+"\n" {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	wantStatus(t, d.bob("DELETE", "/api/domains/elsewhere.org", ""), 404, "")
}
