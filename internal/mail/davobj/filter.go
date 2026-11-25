package davobj

import (
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
)

// Filters of calendar-query (RFC 4791 section 9.7) and addressbook-query
// (RFC 6352 section 10.5) reports.

// TimeRange is a time-range element; a zero Start or End is open.
type TimeRange struct {
	Start, End time.Time
}

// TextMatch is a text-match element.
type TextMatch struct {
	Text string
	// Collation is i;ascii-casemap (default for calendars), i;unicode-casemap
	// (default for contacts) or i;octet.
	Collation string
	Negate    bool
	// MatchType is equals, contains (default), starts-with or ends-with
	// (address books only).
	MatchType string
}

// Collations.
const (
	CollationASCII   = "i;ascii-casemap"
	CollationUnicode = "i;unicode-casemap"
	CollationOctet   = "i;octet"
)

// KnownCollation tells whether a collation is supported.
func KnownCollation(c string) bool {
	return c == "" || c == CollationASCII || c == CollationUnicode || c == CollationOctet
}

// Match tells whether value matches.
func (m TextMatch) Match(value string) bool {
	a, b := value, m.Text
	switch m.Collation {
	case CollationOctet:
	case CollationUnicode:
		a, b = strings.ToLower(a), strings.ToLower(b)
	default:
		a, b = asciiLower(a), asciiLower(b)
	}
	var ok bool
	switch m.MatchType {
	case "equals":
		ok = a == b
	case "starts-with":
		ok = strings.HasPrefix(a, b)
	case "ends-with":
		ok = strings.HasSuffix(a, b)
	default:
		ok = strings.Contains(a, b)
	}
	return ok != m.Negate
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// ParamFilter is a param-filter element.
type ParamFilter struct {
	Name         string
	IsNotDefined bool
	TextMatch    *TextMatch
}

// PropFilter is a prop-filter element of a calendar query.
type PropFilter struct {
	Name         string
	IsNotDefined bool
	TimeRange    *TimeRange
	TextMatch    *TextMatch
	Params       []ParamFilter
}

// CompFilter is a comp-filter element of a calendar query.
type CompFilter struct {
	Name         string
	IsNotDefined bool
	TimeRange    *TimeRange
	Comps        []CompFilter
	Props        []PropFilter
}

// MatchCalendar tells whether the calendar object cal matches the filter
// (whose top comp-filter names VCALENDAR).
func MatchCalendar(cal *ical.Calendar, f CompFilter) bool {
	tz := newTZ(cal)
	if cal.Name != f.Name {
		return f.IsNotDefined
	}
	if f.IsNotDefined {
		return false
	}
	return matchComp(cal.Component, f, tz)
}

// matchComp tells whether c (whose name is f.Name) meets f's tests.
func matchComp(c *ical.Component, f CompFilter, tz *tzSet) bool {
	if f.TimeRange != nil && (c.Name == CompEvent || c.Name == CompToDo) &&
		!overlaps(c, tz, f.TimeRange.Start, f.TimeRange.End) {
		return false
	}
	for _, cf := range f.Comps {
		found := false
		for _, child := range c.Children {
			if child.Name != cf.Name {
				continue
			}
			if cf.IsNotDefined {
				found = true
				break
			}
			if matchComp(child, cf, tz) {
				found = true
				break
			}
		}
		if found == cf.IsNotDefined {
			return false
		}
	}
	for _, pf := range f.Props {
		props := c.Props.Values(pf.Name)
		if pf.IsNotDefined {
			if len(props) > 0 {
				return false
			}
			continue
		}
		found := false
		for i := range props {
			if matchProp(&props[i], pf) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func matchProp(p *ical.Prop, f PropFilter) bool {
	// A time-range on a property (rarely asked) does not narrow the
	// answer: clients filter again.
	if f.TextMatch != nil && !f.TextMatch.Match(unescapeText(p.Value)) {
		return false
	}
	for _, pf := range f.Params {
		vals := p.Params.Values(pf.Name)
		if pf.IsNotDefined {
			if len(vals) > 0 {
				return false
			}
			continue
		}
		if len(vals) == 0 {
			return false
		}
		if pf.TextMatch != nil {
			ok := false
			for _, v := range vals {
				ok = ok || pf.TextMatch.Match(v)
			}
			if !ok {
				return false
			}
		}
	}
	return true
}

// unescapeText undoes the escapes of an iCalendar TEXT value.
func unescapeText(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
			switch v[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte(v[i])
			}
			continue
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// CardPropFilter is a prop-filter element of an address book query.
type CardPropFilter struct {
	Name string
	// AllOf: every text-match and param-filter must match (test="allof");
	// otherwise any one.
	AllOf        bool
	IsNotDefined bool
	TextMatches  []TextMatch
	Params       []ParamFilter
}

// CardFilter is the filter element of an address book query.
type CardFilter struct {
	AllOf bool
	Props []CardPropFilter
}

// MatchCard tells whether a vCard matches the filter. An empty filter
// matches every card.
func MatchCard(card vcard.Card, f CardFilter) bool {
	if len(f.Props) == 0 {
		return true
	}
	for _, pf := range f.Props {
		ok := matchCardProp(card, pf)
		if f.AllOf && !ok {
			return false
		}
		if !f.AllOf && ok {
			return true
		}
	}
	return f.AllOf
}

func matchCardProp(card vcard.Card, f CardPropFilter) bool {
	fields := card[strings.ToUpper(f.Name)]
	if f.IsNotDefined {
		return len(fields) == 0
	}
	if len(fields) == 0 {
		return false
	}
	if len(f.TextMatches) == 0 && len(f.Params) == 0 {
		return true
	}
	tests := 0
	passed := 0
	for _, tm := range f.TextMatches {
		tests++
		for _, fl := range fields {
			if tm.Match(fl.Value) {
				passed++
				break
			}
		}
	}
	for _, pf := range f.Params {
		tests++
		for _, fl := range fields {
			vals := fl.Params[strings.ToUpper(pf.Name)]
			if pf.IsNotDefined && len(vals) == 0 || !pf.IsNotDefined && len(vals) > 0 && (pf.TextMatch == nil || anyMatch(vals, *pf.TextMatch)) {
				passed++
				break
			}
		}
	}
	if f.AllOf {
		return passed == tests
	}
	return passed > 0
}

func anyMatch(vals []string, tm TextMatch) bool {
	for _, v := range vals {
		if tm.Match(v) {
			return true
		}
	}
	return false
}
