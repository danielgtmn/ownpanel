package davobj

import (
	"testing"
	"time"

	"github.com/teambition/rrule-go"
)

func TestNeverOccurs(t *testing.T) {
	never := []string{
		"FREQ=HOURLY;BYMONTH=2;BYMONTHDAY=30",
		"FREQ=DAILY;BYMONTH=2;BYMONTHDAY=30",
		"FREQ=YEARLY;BYMONTHDAY=31;BYMONTH=2,4,6",
		"FREQ=MONTHLY;BYMONTHDAY=-31;BYMONTH=4",
		"FREQ=YEARLY;BYYEARDAY=366;BYMONTH=1",
		"FREQ=WEEKLY;BYSETPOS=400;BYDAY=MO",
		"FREQ=MONTHLY;BYDAY=6MO",
		"FREQ=YEARLY;BYDAY=-54MO",
		"FREQ=YEARLY;BYWEEKNO=1;BYMONTH=6",
	}
	possible := []string{
		"FREQ=DAILY",
		"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29",
		"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29;BYDAY=MO",
		"FREQ=MONTHLY;BYDAY=-1FR",
		"FREQ=MONTHLY;BYDAY=5MO",
		"FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1",
		"FREQ=YEARLY;BYWEEKNO=53;BYDAY=SU",
		"FREQ=YEARLY;BYYEARDAY=366",
		"FREQ=YEARLY;BYDAY=53MO",
		"FREQ=DAILY;BYHOUR=9,17;BYSETPOS=2",
		"FREQ=WEEKLY;BYDAY=MO;BYEASTER=0",
	}
	for want, rules := range map[bool][]string{true: never, false: possible} {
		for _, r := range rules {
			o, err := rrule.StrToROption(r)
			if err != nil {
				t.Fatalf("%s: %v", r, err)
			}
			if got := neverOccurs(o); got != want {
				t.Errorf("neverOccurs(%s) = %v, want %v", r, got, want)
			}
		}
	}
}

// A rule that selects no date is read and matched at once, and the event
// still counts at its start.
func TestImpossibleRuleIsQuick(t *testing.T) {
	q := func(y int) CompFilter {
		return CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: CompEvent, TimeRange: &TimeRange{
			Start: time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(y, 1, 2, 0, 0, 0, 0, time.UTC)}}}}
	}
	for _, f := range []string{"YEARLY", "MONTHLY", "WEEKLY", "DAILY", "HOURLY"} {
		rule := "FREQ=" + f + ";BYMONTH=2;BYMONTHDAY=30"
		start := time.Now()
		_, c, err := ParseCalendar(cal(ev("DTSTART:20260101T000000Z\r\nRRULE:"+rule+"\r\n")), nil)
		if err != nil {
			t.Fatal(err)
		}
		if MatchCalendar(c, q(2040)) {
			t.Errorf("%s matches a day it never has", rule)
		}
		if !MatchCalendar(c, q(2026)) {
			t.Errorf("%s: the start no longer counts", rule)
		}
		if d := time.Since(start); d > 200*time.Millisecond {
			t.Errorf("%s took %s", rule, d)
		}
	}
}
