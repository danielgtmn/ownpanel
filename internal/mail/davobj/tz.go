package davobj

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // time zones by name on every system, also without a tzdata package

	"github.com/emersion/go-ical"
)

// Time zones of iCalendar data. A TZID is resolved, in this order, as
//
//  1. an IANA name ("Europe/Berlin"), also at the end of a longer id
//     ("/mozilla.org/20050126_1/Europe/Berlin", "/citadel.org/…/Europe/Berlin");
//  2. the X-LIC-LOCATION of its VTIMEZONE (Thunderbird, Google);
//  3. a Windows name (Outlook: "W. Europe Standard Time");
//  4. the standard offset of its VTIMEZONE, as a fixed zone (approximate:
//     daylight saving time is not applied, so such times are widened);
//  5. otherwise UTC, widened like a floating time.

// tzSet resolves the TZIDs of one calendar.
type tzSet struct {
	defs map[string]*ical.Component
}

func newTZ(cal *ical.Calendar) *tzSet {
	s := &tzSet{defs: map[string]*ical.Component{}}
	for _, c := range cal.Children {
		if c.Name != ical.CompTimezone {
			continue
		}
		if p := c.Props.Get(ical.PropTimezoneID); p != nil {
			s.defs[p.Value] = c
		}
	}
	return s
}

var (
	locMu    sync.Mutex
	locCache = map[string]*time.Location{} // nil: not a known zone
)

const maxLocCache = 1000

// loadIANA returns the IANA zone of a name, or nil.
func loadIANA(name string) *time.Location {
	if name == "" || len(name) > 64 || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		return nil
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/_+-", r)) {
			return nil
		}
	}
	if name == "Local" {
		return nil
	}
	locMu.Lock()
	loc, ok := locCache[name]
	locMu.Unlock()
	if ok {
		return loc
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = nil
	}
	locMu.Lock()
	if len(locCache) < maxLocCache {
		locCache[name] = loc
	}
	locMu.Unlock()
	return loc
}

// windowsZones maps the Windows names Outlook writes most often.
var windowsZones = map[string]string{
	"W. Europe Standard Time":        "Europe/Berlin",
	"Central Europe Standard Time":   "Europe/Budapest",
	"Central European Standard Time": "Europe/Warsaw",
	"Romance Standard Time":          "Europe/Paris",
	"GMT Standard Time":              "Europe/London",
	"Greenwich Standard Time":        "Atlantic/Reykjavik",
	"E. Europe Standard Time":        "Europe/Chisinau",
	"FLE Standard Time":              "Europe/Kiev",
	"GTB Standard Time":              "Europe/Bucharest",
	"Russian Standard Time":          "Europe/Moscow",
	"Turkey Standard Time":           "Europe/Istanbul",
	"Israel Standard Time":           "Asia/Jerusalem",
	"Arabian Standard Time":          "Asia/Dubai",
	"India Standard Time":            "Asia/Kolkata",
	"China Standard Time":            "Asia/Shanghai",
	"Tokyo Standard Time":            "Asia/Tokyo",
	"Singapore Standard Time":        "Asia/Singapore",
	"AUS Eastern Standard Time":      "Australia/Sydney",
	"New Zealand Standard Time":      "Pacific/Auckland",
	"Eastern Standard Time":          "America/New_York",
	"Central Standard Time":          "America/Chicago",
	"Mountain Standard Time":         "America/Denver",
	"US Mountain Standard Time":      "America/Phoenix",
	"Pacific Standard Time":          "America/Los_Angeles",
	"Alaskan Standard Time":          "America/Anchorage",
	"Hawaiian Standard Time":         "Pacific/Honolulu",
	"Atlantic Standard Time":         "America/Halifax",
	"E. South America Standard Time": "America/Sao_Paulo",
	"South Africa Standard Time":     "Africa/Johannesburg",
	"UTC":                            "UTC",
	"Coordinated Universal Time":     "UTC",
}

// location resolves a TZID; approx is true when the result is not the
// exact zone.
func (s *tzSet) location(tzid string) (loc *time.Location, approx bool) {
	tzid = strings.Trim(tzid, `"`)
	if loc := loadIANA(tzid); loc != nil {
		return loc, false
	}
	// The IANA name at the end of a longer id: try the last three, two
	// and one segments.
	if parts := strings.Split(tzid, "/"); len(parts) > 1 {
		for n := min(3, len(parts)-1); n >= 1; n-- {
			if loc := loadIANA(strings.Join(parts[len(parts)-n:], "/")); loc != nil {
				return loc, false
			}
		}
	}
	def := s.defs[tzid]
	if def != nil {
		if p := def.Props.Get("X-LIC-LOCATION"); p != nil {
			if loc := loadIANA(p.Value); loc != nil {
				return loc, false
			}
		}
	}
	if name, ok := windowsZones[tzid]; ok {
		if loc := loadIANA(name); loc != nil {
			return loc, false
		}
	}
	if def != nil {
		for _, c := range def.Children {
			if c.Name != ical.CompTimezoneStandard {
				continue
			}
			if p := c.Props.Get(ical.PropTimezoneOffsetTo); p != nil {
				if off, ok := parseOffset(p.Value); ok {
					return time.FixedZone(tzid, off), true
				}
			}
		}
	}
	return time.UTC, true
}

// parseOffset reads a UTC offset (+0100, -053000).
func parseOffset(v string) (int, bool) {
	if len(v) != 5 && len(v) != 7 || v[0] != '+' && v[0] != '-' {
		return 0, false
	}
	n := 0
	for i, mul := range []int{3600, 60, 1} {
		if 1+2*i+2 > len(v) {
			break
		}
		x, err := strconv.Atoi(v[1+2*i : 1+2*i+2])
		if err != nil || x < 0 {
			return 0, false
		}
		n += x * mul
	}
	if n > 18*3600 {
		return 0, false
	}
	if v[0] == '-' {
		n = -n
	}
	return n, true
}

const (
	dateFormat        = "20060102"
	dateTimeFormat    = "20060102T150405"
	dateTimeUTCFormat = "20060102T150405Z"
)

var errValue = errors.New("not a date or date-time")

// parse reads a DATE or DATE-TIME property. date: the value is a date.
// widen: floating, or a time zone that is only approximated.
func (s *tzSet) parse(p *ical.Prop) (t time.Time, date, widen bool, err error) {
	return s.parseValue(strings.TrimSpace(p.Value), p.Params)
}

func (s *tzSet) parseValue(v string, params ical.Params) (t time.Time, date, widen bool, err error) {
	vt := strings.ToUpper(params.Get(ical.ParamValue))
	switch {
	case vt == "DATE" || vt == "" && len(v) == len(dateFormat):
		t, err = time.ParseInLocation(dateFormat, v, time.UTC)
		return t, true, true, err
	case vt != "" && vt != "DATE-TIME":
		return t, false, false, errValue
	case strings.HasSuffix(v, "Z"):
		t, err = time.ParseInLocation(dateTimeUTCFormat, v, time.UTC)
		return t, false, false, err
	}
	loc, approx := time.UTC, true
	if tzid := params.Get(ical.ParamTimezoneID); tzid != "" {
		loc, approx = s.location(tzid)
	}
	t, err = time.ParseInLocation(dateTimeFormat, v, loc)
	return t, false, approx, err
}

// parseList reads the comma-separated dates of RDATE or EXDATE; values
// that cannot be read are left out (a PERIOD counts by its start).
func (s *tzSet) parseList(p *ical.Prop) []time.Time {
	var out []time.Time
	params := p.Params
	if strings.EqualFold(params.Get(ical.ParamValue), "PERIOD") {
		params = ical.Params{}
		for k, v := range p.Params {
			if !strings.EqualFold(k, ical.ParamValue) {
				params[k] = v
			}
		}
	}
	for _, v := range strings.Split(p.Value, ",") {
		if i := strings.IndexByte(v, '/'); i >= 0 {
			v = v[:i]
		}
		if t, _, _, err := s.parseValue(strings.TrimSpace(v), params); err == nil {
			out = append(out, t)
		}
		if len(out) >= MaxInstances {
			break
		}
	}
	return out
}
