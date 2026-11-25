package dav

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func rangesQuery(n int) string {
	var b strings.Builder
	b.WriteString(`<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/></d:prop><c:filter><c:comp-filter name="VCALENDAR">`)
	for i := range n {
		fmt.Fprintf(&b, `<c:comp-filter name="VEVENT"><c:time-range start="202701%02dT000000Z" end="202701%02dT000000Z"/></c:comp-filter>`, 1+i%27, 2+i%27)
	}
	b.WriteString(`</c:comp-filter></c:filter></c:calendar-query>`)
	return b.String()
}

// TestQueryLimits: a query has a bounded number of filters, expands each
// object's recurrence once whatever the number of time ranges, and stops
// with 507 when its work budget is used up.
func TestQueryLimits(t *testing.T) {
	e := newEnv(t)
	expect(t, e.as("REPORT", calPath, map[string]string{"Depth": "1"}, rangesQuery(40)), http.StatusForbidden, "supported-filter")
	for i := range 40 {
		uid := fmt.Sprintf("h%d", i)
		expect(t, e.as("PUT", calPath+uid+".ics", nil, calObj(uid, "DTSTART:20260101T000000Z\r\nDTEND:20260101T003000Z\r\nRRULE:FREQ=HOURLY\r\n")), http.StatusCreated)
	}
	start := time.Now()
	ms := parseMS(t, e.as("REPORT", calPath, map[string]string{"Depth": "1"}, rangesQuery(30)))
	if len(ms.Responses) != 40 {
		t.Fatalf("%d responses", len(ms.Responses))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("query took %s", d)
	}
	e.h.opts.QueryBudget = time.Nanosecond
	r := e.as("REPORT", calPath, map[string]string{"Depth": "1"}, rangesQuery(1))
	if r.code != http.StatusMultiStatus && r.code != http.StatusInsufficientStorage || !strings.Contains(r.body, "507") && !strings.Contains(r.body, "number-of-matches-within-limits") {
		t.Fatalf("over budget: %d %s", r.code, r.body)
	}
}
