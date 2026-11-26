package dav

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
)

// TestExpandPropertyNames: property names from expand-property attributes
// are never written into the answer unless they are XML names.
func TestExpandPropertyNames(t *testing.T) {
	e := newEnv(t)
	bad := `<d:expand-property xmlns:d="DAV:"><d:property name="x&quot;/&gt;&lt;d:href&gt;injected&lt;/d:href&gt;&lt;d:y"/></d:expand-property>`
	r := e.as("REPORT", "/dav/principals/alice@example.org/", map[string]string{"Depth": "0"}, bad)
	if r.code != http.StatusBadRequest || strings.Contains(r.body, "injected") {
		t.Fatalf("%d %s", r.code, r.body)
	}
	good := `<d:expand-property xmlns:d="DAV:"><d:property name="displayname"/><d:property name="x-unknown" namespace="urn:x&quot;y"/></d:expand-property>`
	r = e.as("REPORT", "/dav/principals/alice@example.org/", map[string]string{"Depth": "0"}, good)
	if r.code != http.StatusMultiStatus {
		t.Fatalf("%d %s", r.code, r.body)
	}
	var v any
	if err := xml.Unmarshal([]byte(r.body), &v); err != nil || !strings.Contains(r.body, "Alice Example") {
		t.Fatalf("not well-formed: %v %s", err, r.body)
	}
}
