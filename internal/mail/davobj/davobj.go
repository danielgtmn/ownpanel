// Package davobj reads the objects calendars and address books hold:
// iCalendar resources (RFC 5545, one event or task with its exceptions per
// resource, as CalDAV stores them) and vCards (RFC 6350/2426). It checks
// what clients upload, extracts what the index needs (UID, kind, the time
// span of all occurrences), evaluates the filters of calendar and address
// book queries, and splits and joins files for import and export.
//
// Everything here reads untrusted input: sizes are bounded by the caller
// (MaxObjectBytes per resource, MaxFileBytes per imported file), component
// nesting is checked before parsing, and recurrence expansion stops after
// MaxInstances occurrences.
//
// Parsing uses github.com/emersion/go-ical (with github.com/teambition/rrule-go
// for recurrence rules) and github.com/emersion/go-vcard; dates, time zones
// and recurrence sets are resolved here, because go-ical only knows time
// zones by IANA name.
package davobj

import (
	"errors"
	"fmt"
	"math"
)

// Limits.
const (
	// MaxObjectBytes is the largest calendar object or vCard (with photo).
	MaxObjectBytes = 1 << 20
	// MaxFileBytes is the largest file imported at once.
	MaxFileBytes = 10 << 20
	// MaxNesting bounds component nesting (VCALENDAR > VEVENT > VALARM is 3).
	MaxNesting = 8
	// MaxInstances bounds the occurrences of one recurring component that
	// are computed for one question (its last end, a time range).
	MaxInstances = 20_000
	// MaxUIDBytes bounds a UID.
	MaxUIDBytes = 1024
)

// Bounds of an unbounded time span (index columns).
const (
	NoStart int64 = math.MinInt64
	NoEnd   int64 = math.MaxInt64
)

// Kinds of collections.
const (
	Calendar    = "calendar"
	AddressBook = "addressbook"
)

// Components a calendar collection holds.
const (
	CompEvent = "VEVENT"
	CompToDo  = "VTODO"
	// CompCard marks vCards in the index.
	CompCard = "VCARD"
)

// Content types.
const (
	CalendarType = "text/calendar"
	CardType     = "text/vcard"
)

// Error is an object a client may not store. Precondition is the CalDAV or
// CardDAV precondition element it violates (RFC 4791 section 5.3.2.1, RFC
// 6352 section 6.3.2.1), for the error body of the answer.
type Error struct {
	Precondition string
	Msg          string
}

func (e *Error) Error() string { return e.Msg }

// Preconditions.
const (
	ValidCalendarData     = "valid-calendar-data"
	ValidCalendarObject   = "valid-calendar-object-resource"
	SupportedComponent    = "supported-calendar-component"
	ValidAddressData      = "valid-address-data"
	MaxResourceSize       = "max-resource-size"
	SupportedCalendarData = "supported-calendar-data"
	SupportedAddressData  = "supported-address-data"
)

func invalid(pre, format string, args ...any) error {
	return &Error{Precondition: pre, Msg: fmt.Sprintf(format, args...)}
}

// IsInvalid tells whether err is an *Error.
func IsInvalid(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Info is what the index keeps of an object.
type Info struct {
	UID string
	// Component is VEVENT, VTODO or VCARD.
	Component string
	// Start and End span every occurrence (Unix seconds): NoStart and
	// NoEnd when open. Floating times are widened by a day's worth of
	// time zones so the span holds wherever the reader is.
	Start, End int64
}

// ValidUID tells whether a UID can be stored: not empty, bounded, one
// line.
func ValidUID(uid string) bool {
	if uid == "" || len(uid) > MaxUIDBytes {
		return false
	}
	for i := 0; i < len(uid); i++ {
		if uid[i] < 0x20 || uid[i] == 0x7f {
			return false
		}
	}
	return true
}
