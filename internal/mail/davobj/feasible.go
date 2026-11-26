package davobj

import (
	"time"

	"github.com/teambition/rrule-go"
)

// neverOccurs tells whether a rule's BY parts can never select a date, so
// the rule adds no occurrence to DTSTART (which RFC 5545 always counts as
// the first). rrule-go searches such a rule up to the year 9999 before it
// gives up, whatever UNTIL says: up to seconds of CPU per object for
// FREQ=HOURLY;BYMONTH=2;BYMONTHDAY=30. The Gregorian calendar repeats every
// 400 years, so a date the filters accept exists in one cycle or never.
//
// It answers true only when sure: rules it cannot judge (BYEASTER, week
// numbers outside YEARLY or with a week start other than Monday, ordinal
// weekdays outside MONTHLY and YEARLY, BYSETPOS on sub-daily rules) count
// as possible and are left to rrule-go.
func neverOccurs(o *rrule.ROption) bool {
	if len(o.Bymonthday) == 0 && len(o.Byyearday) == 0 && len(o.Byweekno) == 0 &&
		len(o.Bysetpos) == 0 && !hasOrdinal(o.Byweekday) {
		return false // nothing that could exclude every date
	}
	if len(o.Byeaster) > 0 {
		return false
	}
	if len(o.Byweekno) > 0 && (o.Freq != rrule.YEARLY || o.Wkst.Day() != rrule.MO.Day()) {
		return false
	}
	// Ordinal weekdays (2MO, -1FR) count within the month for MONTHLY and
	// for YEARLY with BYMONTH, within the year for YEARLY otherwise; other
	// frequencies don't allow them.
	inMonth := o.Freq == rrule.MONTHLY || o.Freq == rrule.YEARLY && len(o.Bymonth) > 0
	if hasOrdinal(o.Byweekday) && o.Freq != rrule.MONTHLY && o.Freq != rrule.YEARLY {
		return false
	}
	var perPeriod func(time.Time) int64 // the period a day belongs to, for BYSETPOS
	if len(o.Bysetpos) > 0 {
		switch o.Freq {
		case rrule.YEARLY:
			perPeriod = func(d time.Time) int64 { return int64(d.Year()) }
		case rrule.MONTHLY:
			perPeriod = func(d time.Time) int64 { return int64(d.Year())*12 + int64(d.Month()) }
		case rrule.WEEKLY:
			if o.Wkst.Day() != rrule.MO.Day() {
				return false
			}
			perPeriod = func(d time.Time) int64 { y, w := d.ISOWeek(); return int64(y)*54 + int64(w) }
		case rrule.DAILY:
			perPeriod = func(d time.Time) int64 { return d.Unix() / 86400 }
		default:
			return false
		}
	}

	matched := false
	var period int64
	var inPeriod, maxPerPeriod int
	start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(400, 0, 0)
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		if len(o.Bymonth) > 0 && !containsInt(o.Bymonth, int(d.Month())) {
			// Skip a month the rule excludes: to the last day of it.
			d = time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !dayMatches(o, d, inMonth) {
			continue
		}
		matched = true
		if perPeriod == nil {
			return false
		}
		if p := perPeriod(d); p != period {
			period, inPeriod = p, 0
		}
		inPeriod++
		maxPerPeriod = max(maxPerPeriod, inPeriod)
	}
	if !matched {
		return true
	}
	// BYSETPOS picks among the occurrences of a period: days times the
	// times of day the rule lists.
	most := maxPerPeriod * max(1, len(o.Byhour)) * max(1, len(o.Byminute)) * max(1, len(o.Bysecond))
	for _, p := range o.Bysetpos {
		if p != 0 && abs(p) <= most {
			return false
		}
	}
	return true
}

func hasOrdinal(ws []rrule.Weekday) bool {
	for _, w := range ws {
		if w.N() != 0 {
			return true
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// dayMatches applies the date filters of a rule to one day.
func dayMatches(o *rrule.ROption, d time.Time, ordinalInMonth bool) bool {
	if len(o.Bymonth) > 0 && !containsInt(o.Bymonth, int(d.Month())) {
		return false
	}
	monthDays := daysIn(d.Year(), d.Month())
	if len(o.Bymonthday) > 0 && !matchesIndex(o.Bymonthday, d.Day(), monthDays) {
		return false
	}
	yearDays := 365
	if daysIn(d.Year(), time.February) == 29 {
		yearDays = 366
	}
	if len(o.Byyearday) > 0 && !matchesIndex(o.Byyearday, d.YearDay(), yearDays) {
		return false
	}
	if len(o.Byweekno) > 0 {
		y, w := d.ISOWeek()
		if !matchesIndex(o.Byweekno, w, weeksIn(y)) {
			return false
		}
	}
	if len(o.Byweekday) > 0 {
		// rrule-go numbers Monday 0; Go numbers Sunday 0.
		wd := (int(d.Weekday()) + 6) % 7
		ok := false
		for _, w := range o.Byweekday {
			if w.Day() != wd {
				continue
			}
			if w.N() == 0 {
				ok = true
				break
			}
			// The day's position among the same weekdays of its month or
			// year, from the start and from the end.
			pos, total := d.Day(), monthDays
			if !ordinalInMonth {
				pos, total = d.YearDay(), yearDays
			}
			first := (pos-1)/7 + 1
			last := -((total-pos)/7 + 1)
			if w.N() == first || w.N() == last {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// matchesIndex tells whether position i (1-based) of n is listed, counting
// negative values from the end.
func matchesIndex(list []int, i, n int) bool {
	for _, v := range list {
		if v == i || v < 0 && n+v+1 == i {
			return true
		}
	}
	return false
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func daysIn(year int, m time.Month) int {
	return time.Date(year, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// weeksIn is the number of ISO weeks of a year (52 or 53).
func weeksIn(year int) int {
	_, w := time.Date(year, 12, 28, 0, 0, 0, 0, time.UTC).ISOWeek()
	return w
}
