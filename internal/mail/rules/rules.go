// Package rules turns the simple settings of the self-service area and the
// panel (out-of-office, forwarding, filter rules; config.MailRules) into
// the mailbox's Sieve script and back.
//
// The generated script is named ScriptName. Its settings travel inside it,
// as JSON on one comment line, so the script is the only state: the
// settings can be edited as long as the script is exactly what they
// generate. A script edited elsewhere (ManageSieve) or another active
// script is shown read-only, and the simple editors offer to replace it.
package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/sieve"
)

// ScriptName is the name of the generated script.
const ScriptName = "ownpanel"

const (
	header      = "# Filters and out-of-office of your mail account. This script is\n# written by the account page; if you change it here, the page can\n# no longer edit it (it offers to replace it).\n"
	settingsTag = "# ownpanel:settings "
)

// Settings are what the generated script holds.
type Settings struct {
	OutOfOffice config.OutOfOffice  `json:"out_of_office"`
	Forwarding  config.Forwarding   `json:"forwarding"`
	Filters     []config.FilterRule `json:"filters"`
}

// Generate returns the script for s. s must be valid (config.Check…).
func Generate(s Settings) string {
	if s.Filters == nil {
		s.Filters = []config.FilterRule{}
	}
	data, err := json.Marshal(s)
	if err != nil {
		panic(err) // plain types only
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteString(`require ["fileinto", "copy", "imap4flags", "vacation", "date", "relational"];` + "\n")
	b.WriteString(settingsTag)
	b.Write(data)
	b.WriteString("\n")

	if o := s.OutOfOffice; o.Enabled {
		b.WriteString("\n# Out-of-office\n")
		var conds []string
		zone := ""
		if o.Zone != "" {
			zone = ":zone " + quote(o.Zone) + " "
		}
		if o.Start != "" {
			conds = append(conds, "currentdate "+zone+`:value "ge" "date" `+quote(o.Start))
		}
		if o.End != "" {
			conds = append(conds, "currentdate "+zone+`:value "le" "date" `+quote(o.End))
		}
		days := o.Days
		if days == 0 {
			days = config.DefaultOutOfOfficeDays
		}
		h := sha256.Sum256([]byte(o.Subject + "\x00" + o.Text + "\x00" + o.Start))
		cmd := fmt.Sprintf("vacation :days %d :handle %s ", days, quote("ooo-"+hex.EncodeToString(h[:6])))
		if o.Subject != "" {
			cmd += ":subject " + quote(o.Subject) + " "
		}
		cmd += quote(o.Text) + ";\n"
		switch len(conds) {
		case 0:
			b.WriteString(cmd)
		case 1:
			b.WriteString("if " + conds[0] + " {\n    " + cmd + "}\n")
		default:
			b.WriteString("if allof (" + strings.Join(conds, ",\n          ") + ") {\n    " + cmd + "}\n")
		}
	}
	if f := s.Forwarding; f.Enabled {
		b.WriteString("\n# Forwarding\n")
		if f.KeepCopy {
			b.WriteString("redirect :copy " + quote(f.To) + ";\n")
		} else {
			b.WriteString("redirect " + quote(f.To) + ";\nstop;\n")
		}
	}
	for _, r := range s.Filters {
		b.WriteString("\n# Rule: " + oneLine(r.Name) + "\n")
		var conds []string
		for _, c := range r.Conditions {
			conds = append(conds, condition(c))
		}
		test := conds[0]
		if len(conds) > 1 {
			op := "allof"
			if r.MatchAny {
				op = "anyof"
			}
			test = op + " (" + strings.Join(conds, ",\n        ") + ")"
		}
		b.WriteString("if " + test + " {\n")
		switch {
		case r.Delete && r.ForwardTo != "":
			b.WriteString("    redirect " + quote(r.ForwardTo) + ";\n")
		case r.Delete:
			b.WriteString("    discard;\n")
		default:
			if r.ForwardTo != "" {
				b.WriteString("    redirect :copy " + quote(r.ForwardTo) + ";\n")
			}
			flags := ""
			if r.MarkRead {
				flags = `:flags "\\Seen" `
			}
			switch {
			case r.Folder != "":
				b.WriteString("    fileinto " + flags + quote(r.Folder) + ";\n")
			case r.MarkRead:
				b.WriteString("    keep " + flags + ";\n")
			}
		}
		b.WriteString("    stop;\n}\n")
	}
	return b.String()
}

func condition(c config.FilterCondition) string {
	v := quote(c.Value)
	switch c.Field {
	case config.FilterFrom:
		if c.Op == config.FilterIs {
			return "address :is \"from\" " + v
		}
		return "header :contains \"from\" " + v
	case config.FilterTo:
		if c.Op == config.FilterIs {
			return `address :is ["to", "cc"] ` + v
		}
		return `header :contains ["to", "cc"] ` + v
	case config.FilterSubject:
		return "header :" + c.Op + " \"subject\" " + v
	case config.FilterList:
		if c.Op == config.FilterIs {
			// List-Id: Name <id>; "is" means the id.
			return `header :contains "list-id" ` + quote("<"+c.Value+">")
		}
		return `header :contains "list-id" ` + v
	}
	return "false"
}

// quote writes a Sieve string.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// Parse reads the settings from a generated script. ok is false when the
// script is not exactly what its settings generate (edited elsewhere or
// not generated at all).
func Parse(script string) (Settings, bool) {
	var s Settings
	i := strings.Index(script, "\n"+settingsTag)
	if i < 0 {
		return s, false
	}
	line := script[i+1+len(settingsTag):]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	if err := json.Unmarshal([]byte(line), &s); err != nil {
		return Settings{}, false
	}
	if s.Filters == nil {
		s.Filters = []config.FilterRule{}
	}
	if Generate(s) != script {
		return Settings{}, false
	}
	return s, true
}

// Check validates and normalizes settings; the error is meant for people.
func Check(s *Settings) error {
	if err := config.CheckOutOfOffice(&s.OutOfOffice); err != nil {
		return err
	}
	if err := config.CheckForwarding(&s.Forwarding); err != nil {
		return err
	}
	if err := config.CheckFilterRules(s.Filters); err != nil {
		return err
	}
	forwards := 0
	if s.Forwarding.Enabled {
		forwards++
	}
	for _, r := range s.Filters {
		if r.ForwardTo != "" {
			forwards++
		}
	}
	if forwards > 20 {
		return errors.New("at most 20 forwarding addresses")
	}
	if _, err := sieve.Compile(Generate(*s)); err != nil {
		// A bug of Generate, or values that make the script too large.
		if len(Generate(*s)) > sieve.MaxScriptBytes {
			return errors.New("the settings are too large")
		}
		return fmt.Errorf("the settings cannot be saved: %v", err)
	}
	return nil
}

// Empty tells whether s does nothing (then no script is needed).
func (s Settings) Empty() bool {
	return !s.OutOfOffice.Enabled && !s.Forwarding.Enabled && len(s.Filters) == 0 &&
		s.OutOfOffice.Text == "" && s.Forwarding.To == ""
}

// OutOfOfficeOn tells whether the reply is on at now.
func (s Settings) OutOfOfficeOn(now time.Time) bool {
	o := s.OutOfOffice
	if !o.Enabled {
		return false
	}
	loc := time.UTC
	if o.Zone != "" {
		if t, err := time.Parse("-0700", o.Zone); err == nil {
			loc = t.Location()
		}
	}
	today := now.In(loc).Format("2006-01-02")
	return (o.Start == "" || today >= o.Start) && (o.End == "" || today <= o.End)
}
