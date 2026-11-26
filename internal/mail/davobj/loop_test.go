package davobj

import (
	"testing"
	"time"
)

// within fails the test when fn does not return in d (rules that make the
// recurrence library loop for ever must never reach it).
func within(t *testing.T, d time.Duration, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: did not finish within %s", name, d)
	}
}

// TestRulesThatNeverMatch: sub-daily rules whose interval never reaches
// their BYHOUR/BYMINUTE/BYSECOND loop for ever in rrule-go; they are not
// expanded, and count as going on from their start.
func TestRulesThatNeverMatch(t *testing.T) {
	rules := []string{
		"FREQ=HOURLY;INTERVAL=2;BYHOUR=1",
		"FREQ=HOURLY;INTERVAL=2;BYHOUR=1;COUNT=3",
		"FREQ=MINUTELY;INTERVAL=2;BYMINUTE=1",
		"FREQ=SECONDLY;INTERVAL=2;BYSECOND=1;UNTIL=20270101T000000Z",
		"FREQ=HOURLY;INTERVAL=24;BYMINUTE=30",
		"FREQ=MINUTELY;INTERVAL=7",
	}
	for _, rule := range rules {
		data := cal(ev("DTSTART:20260101T000000Z\r\nRRULE:" + rule + "\r\n"))
		within(t, 5*time.Second, rule, func() {
			info, c, err := ParseCalendar(data, nil)
			if err != nil {
				t.Errorf("%s: %v", rule, err)
				return
			}
			if info.Start != time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix() {
				t.Errorf("%s: start %d", rule, info.Start)
			}
			// Any range from the start on may hold an occurrence.
			in := CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: CompEvent, TimeRange: &TimeRange{
				Start: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)}}}}
			before := CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: CompEvent, TimeRange: &TimeRange{
				Start: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC)}}}}
			if !MatchCalendar(c, in) || MatchCalendar(c, before) {
				t.Errorf("%s: matching", rule)
			}
		})
	}
	// UNTIL still bounds such a rule.
	_, c, err := ParseCalendar(cal(ev("DTSTART:20260101T000000Z\r\nRRULE:FREQ=HOURLY;INTERVAL=2;BYHOUR=1;UNTIL=20260201T000000Z\r\n")), nil)
	if err != nil {
		t.Fatal(err)
	}
	after := CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: CompEvent, TimeRange: &TimeRange{
		Start: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)}}}}
	if MatchCalendar(c, after) {
		t.Error("matched after UNTIL")
	}
	// The import must not hang either.
	within(t, 5*time.Second, "split", func() {
		if _, _, err := SplitCalendars(cal(ev("DTSTART:20260101T000000Z\r\nRRULE:FREQ=HOURLY;INTERVAL=2;BYHOUR=1\r\n"))); err != nil {
			t.Error(err)
		}
	})
}
