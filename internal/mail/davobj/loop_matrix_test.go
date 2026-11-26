package davobj

import (
	"testing"
	"time"
)

// TestRuleMatrixTerminates runs many combinations of frequency, interval
// and BY parts, including impossible ones, and requires each to be read and
// matched quickly.
func TestRuleMatrixTerminates(t *testing.T) {
	freqs := []string{"YEARLY", "MONTHLY", "WEEKLY", "DAILY", "HOURLY", "MINUTELY", "SECONDLY"}
	intervals := []string{"1", "2", "7", "13", "25"}
	parts := []string{"", ";BYHOUR=1", ";BYMINUTE=59", ";BYSECOND=30", ";BYMONTH=2;BYMONTHDAY=30", ";BYDAY=-5MO",
		";BYSETPOS=400;BYDAY=MO", ";BYYEARDAY=366;BYMONTH=1", ";BYWEEKNO=53;BYDAY=SU", ";COUNT=50", ";BYMONTHDAY=-31;BYMONTH=4"}
	q := CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: CompEvent, TimeRange: &TimeRange{
		Start: time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2040, 1, 2, 0, 0, 0, 0, time.UTC)}}}}
	start := time.Now()
	for _, f := range freqs {
		for _, iv := range intervals {
			for _, p := range parts {
				rule := "FREQ=" + f + ";INTERVAL=" + iv + p
				within(t, 5*time.Second, rule, func() {
					_, c, err := ParseCalendar(cal(ev("DTSTART:20260101T000000Z\r\nRRULE:"+rule+"\r\n")), nil)
					if err == nil {
						MatchCalendar(c, q)
					}
				})
			}
		}
	}
	if d := time.Since(start); d > 60*time.Second {
		t.Fatalf("took %s", d)
	}
}
