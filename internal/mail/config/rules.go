package config

// Filters and out-of-office (G2 in wiki/Groupware-Architecture.md). The
// scripts themselves are user data of the mail service; the panel only
// sets or clears the out-of-office reply and forwarding of a mailbox of
// its domain (for staff who left), through these admin socket endpoints
// below PathMailbox + storage id:
//
//	GET  /rules            → MailRules; 404 when the mailbox is not configured
//	PUT  /out-of-office    body OutOfOfficeChange → MailRules; 409 when the mailbox runs filters written by hand and Replace is false
//	PUT  /forwarding       body ForwardingChange → MailRules; same 409
//
// Whether a mailbox may forward at all is administered state
// (Mailbox.NoForwarding).

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Admin socket paths below PathMailbox + storage id.
const (
	PathRules       = "/rules"
	PathOutOfOffice = "/out-of-office"
	PathForwarding  = "/forwarding"
)

// Limits of the simple filter settings.
const (
	MaxOutOfOfficeSubject  = 200     // characters
	MaxOutOfOfficeText     = 8 << 10 // bytes
	MinOutOfOfficeDays     = 1
	MaxOutOfOfficeDays     = 90
	DefaultOutOfOfficeDays = 7
	MaxFilterRules         = 50
	MaxFilterConditions    = 10
	MaxFilterValue         = 256 // characters
	// MaxForwardTargets bounds the addresses one message is redirected to
	// by one filter run.
	MaxForwardTargets = 5
)

// OutOfOffice is the out-of-office reply of the simple settings.
type OutOfOffice struct {
	Enabled bool   `json:"enabled"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
	// Start and End are dates (YYYY-MM-DD, inclusive) in Zone ("+hhmm");
	// empty means open.
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
	Zone  string `json:"zone,omitempty"`
	// Days: a sender gets the reply at most once per this many days.
	Days int `json:"days"`
	// SetBy names the panel user who set it for the mailbox (empty: the
	// person did); SetAt is when.
	SetBy string    `json:"set_by,omitempty"`
	SetAt time.Time `json:"set_at,omitzero"`
}

// Forwarding sends every message on to another address.
type Forwarding struct {
	Enabled bool   `json:"enabled"`
	To      string `json:"to"`
	// KeepCopy keeps a copy in the mailbox.
	KeepCopy bool      `json:"keep_copy"`
	SetBy    string    `json:"set_by,omitempty"`
	SetAt    time.Time `json:"set_at,omitzero"`
}

// Fields and operators of FilterCondition.
const (
	FilterFrom     = "from"
	FilterTo       = "to" // To or Cc
	FilterSubject  = "subject"
	FilterList     = "list"
	FilterContains = "contains"
	FilterIs       = "is"
)

// FilterCondition is one condition of a FilterRule.
type FilterCondition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// FilterRule is one rule of the simple filter editor: when the conditions
// hold (all of them, or any with MatchAny), the actions run and no later
// rule does.
type FilterRule struct {
	Name       string            `json:"name"`
	MatchAny   bool              `json:"match_any"`
	Conditions []FilterCondition `json:"conditions"`
	// Actions: move into Folder (empty: stays), mark as read, forward a
	// copy to ForwardTo, delete.
	Folder    string `json:"folder,omitempty"`
	MarkRead  bool   `json:"mark_read,omitempty"`
	ForwardTo string `json:"forward_to,omitempty"`
	Delete    bool   `json:"delete,omitempty"`
}

// MailRules is the state of a mailbox's filters.
type MailRules struct {
	// Managed: the simple settings below are what runs (no script, or the
	// script they generate). When false, a script written elsewhere is
	// active and the settings cannot be changed without replacing it.
	Managed     bool         `json:"managed"`
	OutOfOffice OutOfOffice  `json:"out_of_office"`
	Forwarding  Forwarding   `json:"forwarding"`
	Filters     []FilterRule `json:"filters"`
	// ActiveScript is the name of the active script ("" for none) and
	// Script its text (shown read-only under "Advanced").
	ActiveScript string `json:"active_script"`
	Script       string `json:"script,omitempty"`
}

// OutOfOfficeChange sets (or, with Enabled false, clears) the reply.
// Replace allows replacing a script written by hand.
type OutOfOfficeChange struct {
	OutOfOffice OutOfOffice `json:"out_of_office"`
	Replace     bool        `json:"replace"`
}

// ForwardingChange sets or clears forwarding.
type ForwardingChange struct {
	Forwarding Forwarding `json:"forwarding"`
	Replace    bool       `json:"replace"`
}

// ValidZone tells whether z is a zone offset "+hhmm"/"-hhmm" (or empty).
func ValidZone(z string) bool {
	if z == "" {
		return true
	}
	if len(z) != 5 || (z[0] != '+' && z[0] != '-') {
		return false
	}
	for _, c := range z[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return z[1:3] <= "23" && z[3:5] <= "59"
}

// ValidDate tells whether d is YYYY-MM-DD (or empty).
func ValidDate(d string) bool {
	if d == "" {
		return true
	}
	_, err := time.Parse("2006-01-02", d)
	return err == nil
}

// CheckOutOfOffice validates and normalizes o; the error is meant for
// people.
func CheckOutOfOffice(o *OutOfOffice) error {
	o.Subject = strings.TrimSpace(o.Subject)
	o.Text = strings.ReplaceAll(o.Text, "\r\n", "\n")
	if o.Days == 0 {
		o.Days = DefaultOutOfOfficeDays
	}
	switch {
	case !utf8.ValidString(o.Subject) || utf8.RuneCountInString(o.Subject) > MaxOutOfOfficeSubject || strings.ContainsAny(o.Subject, "\r\n"):
		return errorString("the subject must be one line of at most 200 characters")
	case !utf8.ValidString(o.Text) || len(o.Text) > MaxOutOfOfficeText:
		return errorString("the text must be at most 8 KB")
	case o.Enabled && strings.TrimSpace(o.Text) == "":
		return errorString("write the text of the reply")
	case o.Days < MinOutOfOfficeDays || o.Days > MaxOutOfOfficeDays:
		return errorString("reply at most once every 1 to 90 days")
	case !ValidDate(o.Start) || !ValidDate(o.End):
		return errorString("the dates must be like 2026-12-24")
	case o.Start != "" && o.End != "" && o.End < o.Start:
		return errorString("the last day is before the first day")
	case !ValidZone(o.Zone):
		return errorString("the time zone is not valid")
	}
	for _, r := range o.Text {
		if r < 0x20 && r != '\n' && r != '\t' {
			return errorString("the text contains control characters")
		}
	}
	return nil
}

// CheckForwarding validates and normalizes f.
func CheckForwarding(f *Forwarding) error {
	f.To = strings.ToLower(strings.TrimSpace(f.To))
	if !f.Enabled {
		if f.To != "" && !ValidAddress(f.To) {
			return errorString("the address to forward to is not valid")
		}
		return nil
	}
	if !ValidAddress(f.To) {
		return errorString("the address to forward to is not valid")
	}
	return nil
}

// CheckFilterRules validates and normalizes rules.
func CheckFilterRules(rules []FilterRule) error {
	if len(rules) > MaxFilterRules {
		return errorString("at most 50 rules")
	}
	for i := range rules {
		r := &rules[i]
		r.Name = strings.TrimSpace(r.Name)
		if !ValidDisplayName(r.Name) {
			return errorString("rule names must be one line of at most 64 characters, without < > and \"")
		}
		if len(r.Conditions) == 0 || len(r.Conditions) > MaxFilterConditions {
			return errorString("every rule needs 1 to 10 conditions")
		}
		for j := range r.Conditions {
			c := &r.Conditions[j]
			switch c.Field {
			case FilterFrom, FilterTo, FilterSubject, FilterList:
			default:
				return errorString("unknown condition")
			}
			if c.Op != FilterContains && c.Op != FilterIs {
				return errorString("conditions are \"contains\" or \"is\"")
			}
			c.Value = strings.TrimSpace(c.Value)
			if c.Value == "" || !utf8.ValidString(c.Value) || utf8.RuneCountInString(c.Value) > MaxFilterValue || strings.ContainsAny(c.Value, "\r\n") {
				return errorString("every condition needs a value of one line (at most 256 characters)")
			}
		}
		r.ForwardTo = strings.ToLower(strings.TrimSpace(r.ForwardTo))
		if r.ForwardTo != "" && !ValidAddress(r.ForwardTo) {
			return errorString("the address to forward to is not valid")
		}
		if r.Folder != "" && (!utf8.ValidString(r.Folder) || len(r.Folder) > 255 || strings.ContainsAny(r.Folder, "\r\n\"\\")) {
			return errorString("the folder name is not valid")
		}
		if r.Folder == "" && !r.MarkRead && r.ForwardTo == "" && !r.Delete {
			return errorString("every rule needs an action")
		}
		if r.Delete && r.Folder != "" {
			return errorString("a rule cannot both move and delete")
		}
	}
	return nil
}

type errorString string

func (e errorString) Error() string { return string(e) }

// MailRules asks for the filters of a mailbox (404 *Error for an unknown
// mailbox).
func (c *Client) MailRules(ctx context.Context, storageID string) (MailRules, error) {
	var r MailRules
	p, err := mailboxPath(storageID)
	if err != nil {
		return r, err
	}
	err = c.do(ctx, http.MethodGet, p+PathRules, nil, &r)
	return r, err
}

// SetOutOfOffice sets or clears a mailbox's out-of-office reply. A 409
// *Error means the mailbox runs filters written by hand (set Replace).
func (c *Client) SetOutOfOffice(ctx context.Context, storageID string, ch OutOfOfficeChange) (MailRules, error) {
	var r MailRules
	p, err := mailboxPath(storageID)
	if err != nil {
		return r, err
	}
	err = c.do(ctx, http.MethodPut, p+PathOutOfOffice, ch, &r)
	return r, err
}

// SetForwarding sets or clears a mailbox's forwarding; 409 as for
// SetOutOfOffice.
func (c *Client) SetForwarding(ctx context.Context, storageID string, ch ForwardingChange) (MailRules, error) {
	var r MailRules
	p, err := mailboxPath(storageID)
	if err != nil {
		return r, err
	}
	err = c.do(ctx, http.MethodPut, p+PathForwarding, ch, &r)
	return r, err
}
