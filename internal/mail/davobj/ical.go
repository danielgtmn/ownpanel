package davobj

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// floatWiden is how far a floating time (or one in a time zone that is
// only approximated) can be from UTC: the span in the index and every
// time-range test are widened by it, so they never miss an event wherever
// the reader is. Clients filter again on their side.
const floatWiden = 14 * time.Hour

// checkText refuses what is not text: invalid UTF-8 and control
// characters other than tab, CR and LF (they cannot appear in the XML
// answers that carry the object either). It also checks the nesting of
// BEGIN/END lines before any parser recurses into it.
func checkText(data []byte, pre string) error {
	if !utf8.Valid(data) {
		return invalid(pre, "the data is not UTF-8 text")
	}
	for _, c := range data {
		if c < 0x20 && c != '\t' && c != '\r' && c != '\n' || c == 0x7f {
			return invalid(pre, "the data contains control characters")
		}
	}
	if err := checkHeads(data, pre); err != nil {
		return err
	}
	depth := 0
	for len(data) > 0 {
		line := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			data = nil
		}
		switch {
		case hasPrefixFold(line, "BEGIN:"):
			depth++
			if depth > MaxNesting {
				return invalid(pre, "components are nested too deeply")
			}
		case hasPrefixFold(line, "END:"):
			depth--
		}
	}
	return nil
}

// maxHead bounds the name and parameters of one content line (everything
// before the value). The iCalendar parser builds parameter values a byte
// at a time, which costs quadratic time on a huge one.
const maxHead = 8 << 10

// checkHeads checks maxHead on every unfolded content line, in one pass.
func checkHeads(data []byte, pre string) error {
	inHead, quoted, n := true, false, 0
	for i, c := range data {
		if i == 0 || data[i-1] == '\n' {
			if c == ' ' || c == '\t' {
				continue // a folded line goes on
			}
			inHead, quoted, n = true, false, 0
		}
		if !inHead || c == '\r' || c == '\n' {
			continue
		}
		switch {
		case c == '"':
			quoted = !quoted
		case c == ':' && !quoted:
			inHead = false
			continue
		}
		if n++; n > maxHead {
			return invalid(pre, "a line of the data has too many parameters")
		}
	}
	return nil
}

func hasPrefixFold(b []byte, prefix string) bool {
	return len(b) >= len(prefix) && strings.EqualFold(string(b[:len(prefix)]), prefix)
}

// DecodeCalendar parses one iCalendar object (VCALENDAR) and nothing
// after it.
func DecodeCalendar(data []byte) (*ical.Calendar, error) {
	if err := checkText(data, ValidCalendarData); err != nil {
		return nil, err
	}
	dec := ical.NewDecoder(bytes.NewReader(data))
	cal, err := dec.Decode()
	if err != nil {
		return nil, invalid(ValidCalendarData, "the calendar data cannot be read: %v", shortErr(err))
	}
	if _, err := dec.Decode(); !errors.Is(err, io.EOF) {
		return nil, invalid(ValidCalendarData, "there is more than one calendar in the data")
	}
	return cal, nil
}

func shortErr(err error) string {
	s := strings.TrimPrefix(err.Error(), "ical: ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ParseCalendar checks a calendar object resource (RFC 4791 section 4.1)
// and returns what the index keeps: exactly one kind of component (VEVENT
// or VTODO, besides VTIMEZONE), one UID shared by all of them, at most one
// without RECURRENCE-ID. allowed lists the components the calendar
// collection takes (nil: VEVENT and VTODO).
func ParseCalendar(data []byte, allowed []string) (Info, *ical.Calendar, error) {
	if len(data) > MaxObjectBytes {
		return Info{}, nil, invalid(MaxResourceSize, "the object is larger than %d bytes", MaxObjectBytes)
	}
	cal, err := DecodeCalendar(data)
	if err != nil {
		return Info{}, nil, err
	}
	info := Info{Start: NoEnd, End: NoStart}
	masters := 0
	tz := newTZ(cal)
	for _, c := range cal.Children {
		switch c.Name {
		case ical.CompTimezone:
			continue
		case CompEvent, CompToDo:
		default:
			return Info{}, nil, invalid(SupportedComponent, "%s components cannot be stored in a calendar here", c.Name)
		}
		if allowed != nil && !contains(allowed, c.Name) {
			return Info{}, nil, invalid(SupportedComponent, "this calendar does not take %s components", c.Name)
		}
		if info.Component == "" {
			info.Component = c.Name
		} else if info.Component != c.Name {
			return Info{}, nil, invalid(ValidCalendarObject, "one calendar object holds one kind of component")
		}
		uids := c.Props.Values(ical.PropUID)
		if len(uids) != 1 || !ValidUID(uids[0].Value) {
			return Info{}, nil, invalid(ValidCalendarObject, "every %s needs exactly one UID", c.Name)
		}
		if info.UID == "" {
			info.UID = uids[0].Value
		} else if info.UID != uids[0].Value {
			return Info{}, nil, invalid(ValidCalendarObject, "all components of one calendar object share one UID")
		}
		if c.Props.Get(ical.PropRecurrenceID) == nil {
			masters++
		}
		sp, err := spanOf(c, tz)
		if err != nil {
			return Info{}, nil, err
		}
		info.Start = min(info.Start, sp.start)
		info.End = max(info.End, sp.end)
	}
	if info.Component == "" {
		return Info{}, nil, invalid(ValidCalendarObject, "the calendar object holds no event or task")
	}
	if masters > 1 {
		return Info{}, nil, invalid(ValidCalendarObject, "a calendar object holds one event or task (with its exceptions)")
	}
	return info, cal, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// span is the index span of one component, in Unix seconds.
type span struct{ start, end int64 }

// timing is when one component happens.
type timing struct {
	start, end time.Time
	// open: no dates at all (a task without dates), it matches every range.
	open bool
	// widen: floating or approximated times.
	widen bool
	// recur is the recurrence set of a master component with RRULE or
	// RDATE; nil otherwise.
	recur *rrule.Set
	// capped: the rule was cut at the expansion horizon (it has no end, or
	// ends later); count is its COUNT (0: none).
	capped bool
	capAt  time.Time
	count  int
	// unexpanded: a rule that is not expanded (unsafeRule); its
	// occurrences may fall anywhere from the start until lastStart (zero:
	// for ever).
	unexpanded bool
	lastStart  time.Time
}

// unsafeRule tells whether a rule must never be handed to rrule-go:
// sub-daily rules with BYHOUR, BYMINUTE or BYSECOND loop for ever there
// when the interval never reaches them (FREQ=HOURLY;INTERVAL=2;BYHOUR=1),
// without looking at UNTIL or COUNT; MINUTELY and SECONDLY rules produce
// too many occurrences to be worth expanding at all. Such rules count as
// happening at any time from their start on (until UNTIL, if given).
func unsafeRule(o *rrule.ROption) bool {
	switch o.Freq {
	case rrule.MINUTELY, rrule.SECONDLY:
		return true
	case rrule.HOURLY:
		return len(o.Byhour) > 0 || len(o.Byminute) > 0 || len(o.Bysecond) > 0
	}
	return false
}

// horizonYears bounds recurrence expansion: occurrences more than this
// after the start are never computed. Rules that may go on beyond count as
// going on for ever (span) and as matching any range that reaches past it.
// It also bounds the time a rule that never yields an occurrence costs.
const horizonYears = 20

// ended tells whether expansion that produced n occurrences reached the
// real end of the rule, not the horizon.
func (t timing) ended(n int) bool {
	return !t.capped || t.count > 0 && n >= t.count
}

func (t timing) duration() time.Duration { return t.end.Sub(t.start) }

// timingOf reads the dates of an event or task.
func timingOf(c *ical.Component, tz *tzSet) (timing, error) {
	var t timing
	dtstart := c.Props.Get(ical.PropDateTimeStart)
	var start time.Time
	var date bool
	if dtstart != nil {
		var w bool
		var err error
		start, date, w, err = tz.parse(dtstart)
		if err != nil {
			return t, invalid(ValidCalendarData, "DTSTART cannot be read: %v", err)
		}
		t.widen = t.widen || w
	}
	switch c.Name {
	case CompEvent:
		if dtstart == nil {
			return t, invalid(ValidCalendarObject, "an event needs a start (DTSTART)")
		}
		t.start, t.end = start, start
		if p := c.Props.Get(ical.PropDateTimeEnd); p != nil {
			end, _, w, err := tz.parse(p)
			if err != nil {
				return t, invalid(ValidCalendarData, "DTEND cannot be read: %v", err)
			}
			t.widen = t.widen || w
			if end.After(start) {
				t.end = end
			}
		} else if p := c.Props.Get(ical.PropDuration); p != nil {
			d, err := p.Duration()
			if err != nil {
				return t, invalid(ValidCalendarData, "DURATION cannot be read")
			}
			if d > 0 {
				t.end = start.Add(d)
			}
		} else if date {
			t.end = start.AddDate(0, 0, 1)
		}
	case CompToDo:
		var due time.Time
		hasDue := false
		if p := c.Props.Get(ical.PropDue); p != nil {
			var w bool
			var err error
			due, _, w, err = tz.parse(p)
			if err != nil {
				return t, invalid(ValidCalendarData, "DUE cannot be read: %v", err)
			}
			t.widen = t.widen || w
			hasDue = true
		}
		switch {
		case dtstart != nil:
			t.start, t.end = start, start
			if hasDue && due.After(start) {
				t.end = due
			} else if p := c.Props.Get(ical.PropDuration); p != nil {
				if d, err := p.Duration(); err == nil && d > 0 {
					t.end = start.Add(d)
				}
			}
		case hasDue:
			t.start, t.end = due, due
		default:
			t.open = true
			return t, nil
		}
	}
	if c.Props.Get(ical.PropRecurrenceID) != nil {
		return t, nil
	}
	rules := c.Props.Values(ical.PropRecurrenceRule)
	rdates := c.Props.Values(ical.PropRecurrenceDates)
	if len(rules) == 0 && len(rdates) == 0 {
		return t, nil
	}
	set := &rrule.Set{}
	if len(rules) > 0 {
		opt, err := rrule.StrToROptionInLocation(rules[0].Value, t.start.Location())
		if err != nil {
			return t, invalid(ValidCalendarData, "the recurrence rule cannot be read")
		}
		opt.Dtstart = t.start
		if unsafeRule(opt) {
			t.unexpanded, t.lastStart = true, opt.Until
			return t, nil
		}
		t.capAt = t.start.AddDate(horizonYears, 0, 0)
		if opt.Until.IsZero() || opt.Until.After(t.capAt) {
			opt.Until, t.capped = t.capAt, true
		}
		t.count = opt.Count
		r, err := rrule.NewRRule(*opt)
		if err != nil {
			return t, invalid(ValidCalendarData, "the recurrence rule cannot be used")
		}
		set.RRule(r)
		if len(rules) > 1 {
			// Only the first rule is expanded (several are deprecated);
			// the others may add occurrences anywhere.
			t.capped, t.count = true, 0
		}
	}
	set.DTStart(t.start)
	for _, p := range rdates {
		for _, v := range tz.parseList(&p) {
			set.RDate(v)
		}
	}
	for _, p := range c.Props.Values(ical.PropExceptionDates) {
		for _, v := range tz.parseList(&p) {
			set.ExDate(v)
		}
	}
	t.recur = set
	return t, nil
}

// spanOf is the index span of a component: from its first start to the
// end of its last occurrence (NoEnd when the rule never ends or has more
// than MaxInstances occurrences).
func spanOf(c *ical.Component, tz *tzSet) (span, error) {
	t, err := timingOf(c, tz)
	if err != nil {
		return span{}, err
	}
	if t.open {
		return span{NoStart, NoEnd}, nil
	}
	sp := span{t.start.Unix(), t.end.Unix()}
	if t.unexpanded {
		sp.end = NoEnd
		if !t.lastStart.IsZero() {
			sp.end = max(t.end.Unix(), t.lastStart.Add(t.duration()).Unix())
		}
	}
	if t.recur != nil {
		next := t.recur.Iterator()
		last, n := t.start, 0
		for {
			v, ok := next()
			if !ok {
				break
			}
			if v.Before(sp0(sp)) {
				sp.start = v.Unix()
			}
			last = v
			if n++; n > MaxInstances {
				sp.end = NoEnd
				break
			}
		}
		if sp.end != NoEnd && !t.ended(n) {
			sp.end = NoEnd
		}
		if sp.end != NoEnd {
			sp.end = max(sp.end, last.Add(t.duration()).Unix())
		}
	}
	if t.widen {
		w := int64(floatWiden / time.Second)
		if sp.start != NoStart {
			sp.start -= w
		}
		if sp.end != NoEnd {
			sp.end += w
		}
	}
	return sp, nil
}

func sp0(s span) time.Time { return time.Unix(s.start, 0) }
