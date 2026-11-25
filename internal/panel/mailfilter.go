package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// Spam check settings of the mail service (wiki/E-Mail-Architecture.md,
// "Filtering"): the blocklists incoming mail is checked against, the score
// from which it goes to Junk, and greylisting. Admins change them; the panel
// pushes them in config.Filter.
//
// A Spamhaus DQS zone ("<key>.zen.dq.spamhaus.net") contains an access key.
// Such zones are stored sealed, answered masked ("••••.zen.dq.spamhaus.net")
// and kept when the client sends the mask back; the key never reaches the
// activity log.

// Keys of the settings table.
const (
	// keyMailBlocklists: missing means the default list; otherwise a JSON
	// array of blocklistEntry ("[]" turns blocklists off).
	keyMailBlocklists = "mail_blocklists"
	// keyMailJunkScore: missing means config.DefaultJunkScore.
	keyMailJunkScore = "mail_junk_score"
	// keyMailGreylisting: "true" or missing.
	keyMailGreylisting = "mail_greylisting"
	// keyBlocklistRefused: a JSON object of (masked) zone → last time the
	// mail service reported that the zone refused its queries.
	keyBlocklistRefused = "mail_blocklist_refused"
)

const (
	// blocklistPurpose binds sealed blocklist zones to their use.
	blocklistPurpose = "ownpanel blocklist zone"
	// secretZoneSuffix marks zones whose first label is an access key.
	secretZoneSuffix = config.SecretZoneSuffix
	// zoneMask replaces the access key in answers.
	zoneMask = config.ZoneMask
	// Bounds of the junk threshold.
	minJunkScore = 1
	maxJunkScore = 50
	// blocklistRefusedFor is how long a refusal shows in mail health.
	blocklistRefusedFor = 7 * 24 * time.Hour
)

// blocklistEntry is one stored zone: Zone in plain text, or, for a zone with
// an access key, the sealed zone and the part after the key.
type blocklistEntry struct {
	Zone   string `json:"zone,omitempty"`
	Suffix string `json:"suffix,omitempty"`
	Sealed []byte `json:"sealed,omitempty"`
}

// masked is how the entry is shown.
func (e blocklistEntry) masked() string {
	if e.Sealed != nil {
		return zoneMask + e.Suffix
	}
	return e.Zone
}

func (e blocklistEntry) equal(o blocklistEntry) bool {
	return e.Zone == o.Zone && e.Suffix == o.Suffix && bytes.Equal(e.Sealed, o.Sealed)
}

// isSecretZone tells whether the first label of zone is an access key.
func isSecretZone(zone string) bool {
	return config.SecretZone(zone)
}

// maskZone hides the access key of a secret zone.
func maskZone(zone string) string {
	return config.MaskZone(zone)
}

// secretZoneRe finds zones with an access key in free text.
var secretZoneRe = regexp.MustCompile(`(?i)[a-z0-9_-]+((?:\.[a-z0-9-]+)+\.dq\.spamhaus\.net)\b`)

// maskSecretZones hides access keys of zones mentioned in text (the mail
// service's event details name the zone).
func maskSecretZones(text string) string {
	return secretZoneRe.ReplaceAllString(text, zoneMask+"$1")
}

// parseBlocklists reads the stored value: nil means the default list.
func parseBlocklists(v string) ([]blocklistEntry, error) {
	if v == "" {
		return nil, nil
	}
	out := []blocklistEntry{}
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []blocklistEntry{}
	}
	return out, nil
}

// encodeBlocklists is the stored value: "" for the default list.
func encodeBlocklists(e []blocklistEntry) string {
	if e == nil {
		return ""
	}
	b, _ := json.Marshal(e)
	return string(b)
}

func sameBlocklists(a, b []blocklistEntry) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return slices.EqualFunc(a, b, blocklistEntry.equal)
}

// blocklistView returns the zones as shown (the default list when nil) and
// whether they are the default.
func blocklistView(e []blocklistEntry) ([]string, bool) {
	if e == nil {
		return slices.Clone(config.DefaultBlocklists), true
	}
	out := make([]string, 0, len(e))
	for _, z := range e {
		out = append(out, z.masked())
	}
	return out, false
}

func describeBlocklists(e []blocklistEntry) string {
	zones, def := blocklistView(e)
	switch {
	case def:
		return "default"
	case len(zones) == 0:
		return "off"
	}
	return strings.Join(zones, ", ")
}

// resolveBlocklists validates zones entered by an admin. A masked zone
// stands for the stored zone with the same suffix.
func (s *Service) resolveBlocklists(input []string, cur []blocklistEntry) ([]blocklistEntry, error) {
	if len(input) > config.MaxBlocklists {
		return nil, errorf(Invalid, "at most %d blocklists can be used", config.MaxBlocklists)
	}
	used := make([]bool, len(cur))
	seen := map[string]bool{}
	out := []blocklistEntry{}
	for _, raw := range input {
		z := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
		if rest, ok := strings.CutPrefix(z, zoneMask); ok {
			i := -1
			for j, e := range cur {
				if e.Sealed != nil && e.Suffix == rest && !used[j] {
					i = j
					break
				}
			}
			if i < 0 {
				return nil, errorf(Invalid, "enter the whole name of the blocklist %s again, with the key", z)
			}
			used[i] = true
			out = append(out, cur[i])
			continue
		}
		if !config.ValidDomain(z) {
			return nil, errorf(Invalid, "%q is not a valid blocklist name; enter one like zen.spamhaus.org", maskZone(strings.TrimSpace(raw)))
		}
		if seen[z] {
			return nil, errorf(Invalid, "the blocklist %s is listed twice", maskZone(z))
		}
		seen[z] = true
		if !isSecretZone(z) {
			out = append(out, blocklistEntry{Zone: z})
			continue
		}
		if s.secrets == nil {
			return nil, errorf(Conflict, "no secret key is configured on this server, so a blocklist with a key cannot be stored")
		}
		out = append(out, blocklistEntry{Suffix: z[strings.IndexByte(z, '.'):], Sealed: s.secrets.Seal([]byte(z), blocklistPurpose)})
	}
	// A secret zone entered in full that equals a stored one keeps the
	// stored entry, so saving the same key again is no change.
	for i, e := range out {
		if e.Sealed == nil || slices.ContainsFunc(cur, e.equal) {
			continue
		}
		plain, _ := s.secrets.Open(e.Sealed, blocklistPurpose)
		for j, c := range cur {
			if c.Sealed == nil {
				continue
			}
			if p, err := s.secrets.Open(c.Sealed, blocklistPurpose); err == nil && bytes.Equal(p, plain) {
				if used[j] {
					return nil, errorf(Invalid, "the blocklist %s is listed twice", e.masked())
				}
				out[i], used[j] = c, true
				break
			}
		}
	}
	return out, nil
}

// blocklistZones are the zones to push: nil for the default list.
func (s *Service) blocklistZones(e []blocklistEntry) ([]string, error) {
	if e == nil {
		return nil, nil
	}
	out := make([]string, 0, len(e))
	for _, z := range e {
		if z.Sealed == nil {
			out = append(out, z.Zone)
			continue
		}
		if s.secrets == nil {
			return nil, fmt.Errorf("no secret key configured; the blocklist %s cannot be read", z.masked())
		}
		plain, err := s.secrets.Open(z.Sealed, blocklistPurpose)
		if err != nil {
			return nil, fmt.Errorf("the blocklist %s cannot be read (has the server's secret key changed?)", z.masked())
		}
		out = append(out, string(plain))
	}
	return out, nil
}

// mailFilter is the spam check configuration to push.
func (s *Service) mailFilter() (config.Filter, error) {
	st := s.state()
	zones, err := s.blocklistZones(st.blocklists)
	if err != nil {
		return config.Filter{}, err
	}
	return config.Filter{Blocklists: zones, JunkScore: st.junkScore, Greylisting: st.greylisting}, nil
}

// loadFilterSettings fills the spam check part of st from the stored values.
func loadFilterSettings(st *settingsState, m map[string]string) {
	bl, err := parseBlocklists(m[keyMailBlocklists])
	if err != nil {
		slog.Warn("ignoring stored blocklists; using the default list", "err", err)
	}
	st.blocklists = bl
	if v := m[keyMailJunkScore]; v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || !validJunkScore(f) {
			slog.Warn("ignoring stored junk threshold", "value", v)
		} else {
			st.junkScore = f
		}
	}
	st.greylisting = m[keyMailGreylisting] == "true"
}

func validJunkScore(f float64) bool {
	return !math.IsNaN(f) && f >= minJunkScore && f <= maxJunkScore
}

// effectiveJunkScore is the threshold in effect.
func effectiveJunkScore(f float64) float64 {
	if f <= 0 {
		return config.DefaultJunkScore
	}
	return f
}

// updateFilterSettings applies the spam check part of u to next.
func (s *Service) updateFilterSettings(u SettingsUpdate, cur settingsState, next *settingsState, values map[string]string, changed *[]string) error {
	if u.MailBlocklistsDefault != nil || u.MailBlocklists != nil {
		var entries []blocklistEntry
		switch {
		case u.MailBlocklistsDefault != nil && *u.MailBlocklistsDefault:
			if u.MailBlocklists != nil {
				return errorf(Invalid, "send either mail_blocklists_default true or a list in mail_blocklists, not both")
			}
		case u.MailBlocklists != nil:
			var err error
			if entries, err = s.resolveBlocklists(*u.MailBlocklists, cur.blocklists); err != nil {
				return err
			}
		case cur.blocklists == nil:
			// Leaving the default without a list: the default zones become
			// the own list.
			entries = []blocklistEntry{}
			for _, z := range config.DefaultBlocklists {
				entries = append(entries, blocklistEntry{Zone: z})
			}
		default:
			entries = cur.blocklists
		}
		if !sameBlocklists(entries, cur.blocklists) {
			next.blocklists, values[keyMailBlocklists] = entries, encodeBlocklists(entries)
			if entries != nil && len(entries) == 0 {
				// "" would mean the default list.
				values[keyMailBlocklists] = "[]"
			}
			*changed = append(*changed, "blocklists "+describeBlocklists(entries))
		}
	}
	if u.MailJunkScore != nil {
		f := *u.MailJunkScore
		if !validJunkScore(f) {
			return errorf(Invalid, "the junk threshold must be between %d and %d", minJunkScore, maxJunkScore)
		}
		if f == config.DefaultJunkScore {
			f = 0
		}
		if f != cur.junkScore {
			v := ""
			if f != 0 {
				v = strconv.FormatFloat(f, 'f', -1, 64)
			}
			next.junkScore, values[keyMailJunkScore] = f, v
			*changed = append(*changed, "junk threshold "+strconv.FormatFloat(effectiveJunkScore(f), 'f', -1, 64))
		}
	}
	if u.MailGreylisting != nil && *u.MailGreylisting != cur.greylisting {
		next.greylisting = *u.MailGreylisting
		if next.greylisting {
			values[keyMailGreylisting] = "true"
			*changed = append(*changed, "greylisting on")
		} else {
			values[keyMailGreylisting] = ""
			*changed = append(*changed, "greylisting off")
		}
	}
	return nil
}

// ---- blocklist refusals ----

// refusedZoneRe takes the zone from the detail of a blocklist.refused event.
var refusedZoneRe = regexp.MustCompile(`^the blocklist (\S+) refused`)

// blocklistRefusals reads the stored refusal times.
func blocklistRefusals(m map[string]string) map[string]time.Time {
	out := map[string]time.Time{}
	if v := m[keyBlocklistRefused]; v != "" {
		if err := json.Unmarshal([]byte(v), &out); err != nil {
			slog.Warn("ignoring stored blocklist refusals", "err", err)
			return map[string]time.Time{}
		}
	}
	return out
}

// encodeRefusals drops refusals older than blocklistRefusedFor.
func encodeRefusals(r map[string]time.Time, now time.Time) string {
	for z, t := range r {
		if now.Sub(t) > blocklistRefusedFor {
			delete(r, z)
		}
	}
	if len(r) == 0 {
		return ""
	}
	b, _ := json.Marshal(r)
	return string(b)
}

func (s *Service) healthBlocklists(m map[string]string, mErr error) MailHealthCheck {
	if s.mail == nil {
		return check("", HealthUnknown, noService)
	}
	zones, _ := blocklistView(s.state().blocklists)
	if len(zones) == 0 {
		return check("", HealthOK, "Incoming mail is not checked against blocklists; they are turned off in the settings.")
	}
	if mErr != nil {
		return check("", HealthUnknown, "The reports of the mail service could not be read.")
	}
	c := check("", HealthOK, "")
	c.Expected = zones
	now := s.now()
	refused := blocklistRefusals(m)
	for _, z := range zones {
		if t, ok := refused[z]; ok && now.Sub(t) <= blocklistRefusedFor {
			c.Found = append(c.Found, z)
		}
	}
	if len(c.Found) == 0 {
		c.Detail = "Incoming mail is checked against " + strings.Join(zones, ", ") + "; no blocklist refused this server's queries in the last 7 days."
		return c
	}
	c.State = HealthWarning
	c.Detail = fmt.Sprintf("%s refused to answer this server's queries in the last 7 days, so incoming mail is not checked against %s. "+
		"Blocklists refuse queries that come through big public DNS resolvers. Let the server use its own resolver, "+
		"or enter a Spamhaus DQS key (a blocklist like <key>.zen.dq.spamhaus.net) in the settings.",
		strings.Join(c.Found, ", "), map[bool]string{true: "it", false: "them"}[len(c.Found) == 1])
	return c
}
