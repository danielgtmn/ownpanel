// Package config is the contract between the panel (ownpaneld) and the mail
// service (ownpaneld mail): the complete configuration the panel pushes over
// the admin socket, what the service reports back, and a client for the
// socket. Both sides validate the configuration with Validate.
//
// See wiki/E-Mail-Architecture.md.
package config

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/passwd"
)

// Config is everything the mail service needs to know. It is replaced as a
// whole on every load. It contains secrets (password hashes, DKIM keys, TLS
// keys) and must never be logged.
type Config struct {
	// Hostname is the server's mail name (the panel's mail_host): the MX
	// target, the name in SMTP greetings and what clients connect to.
	Hostname string `json:"hostname"`
	// Certificates for Hostname (chain and key in PEM). The service presents
	// them whatever the SNI; several (ECDSA and RSA) let old clients connect.
	Certificates []Certificate `json:"certificates"`
	// Domains are the active mail domains. Only these receive mail, are
	// delivered to locally and may send.
	Domains []Domain `json:"domains"`
	// Mailboxes of all domains.
	Mailboxes []Mailbox `json:"mailboxes"`
	// Aliases of all domains. A local part "*" is the domain's catch-all.
	Aliases []Alias `json:"aliases"`
	// PostmasterAddress receives postmaster@ and abuse@ of Hostname, and of
	// domains without an alias of that name and without mailboxes. Empty:
	// such mail is refused with a temporary error.
	PostmasterAddress string `json:"postmaster_address"`
	// Limits apply to the whole service; zero values mean the defaults.
	Limits Limits `json:"limits"`
	// Filter configures the checks of incoming mail; zero values mean the
	// defaults.
	Filter Filter `json:"filter"`
	// AppPasswordKey is the server key app passwords are checked with
	// (AppPasswordMAC; 64 hex characters). The panel generates it once and
	// keeps it sealed. Empty: no app passwords (then no mailbox may have
	// any). It is a secret like the rest of the configuration.
	AppPasswordKey string `json:"app_password_key,omitempty"`
}

// Filter configures the checks of incoming mail on port 25.
type Filter struct {
	// Blocklists are DNS blocklist zones the client's IPv4 address is
	// looked up in. nil (or a missing field) means DefaultBlocklists; an
	// empty list turns blocklists off. A Spamhaus DQS zone
	// ("<key>.zen.dq.spamhaus.net") is given here like any other zone.
	Blocklists []string `json:"blocklists"`
	// JunkScore is the score from which a message goes to Junk instead of
	// INBOX; 0 means DefaultJunkScore.
	JunkScore float64 `json:"junk_score"`
	// Greylisting delays the first message of an unknown (network, sender,
	// recipient) from servers without reverse DNS or without an SPF pass.
	// Off by default.
	Greylisting bool `json:"greylisting"`
}

// Defaults for a zero Filter.
const (
	DefaultJunkScore = 5.0
	// MaxBlocklists bounds the blocklist zones.
	MaxBlocklists = 10
)

// DefaultBlocklists are used when Filter.Blocklists is nil.
var DefaultBlocklists = []string{"zen.spamhaus.org"}

// SecretZoneSuffix marks blocklist zones whose first label is an access
// key (Spamhaus DQS: "<key>.zen.dq.spamhaus.net").
const SecretZoneSuffix = ".dq.spamhaus.net"

// ZoneMask replaces the access key of a secret zone wherever it is shown.
const ZoneMask = "••••"

// SecretZone tells whether the first label of zone is an access key.
func SecretZone(zone string) bool {
	return strings.HasSuffix(zone, SecretZoneSuffix) && strings.Count(zone, ".") > strings.Count(SecretZoneSuffix, ".")
}

// MaskZone is zone as it may be shown: in replies, logs, events and
// headers. The access key of a secret zone is replaced by ZoneMask; the
// raw zone is only for the DNS lookup.
func MaskZone(zone string) string {
	if !SecretZone(zone) {
		return zone
	}
	return ZoneMask + zone[strings.IndexByte(zone, '.'):]
}

// WithDefaults returns f with zero values replaced by the defaults.
func (f Filter) WithDefaults() Filter {
	if f.Blocklists == nil {
		f.Blocklists = DefaultBlocklists
	}
	if f.JunkScore <= 0 {
		f.JunkScore = DefaultJunkScore
	}
	return f
}

// Certificate is a certificate chain and its private key in PEM.
type Certificate struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

// Domain is an active mail domain.
type Domain struct {
	// Name is lowercase ASCII (punycode for IDNs).
	Name string `json:"name"`
	// DKIM keys of the domain. Exactly one is active and signs.
	DKIM []DKIMKey `json:"dkim"`
	// SendPerHour caps the messages all mailboxes of the domain send per hour.
	SendPerHour int `json:"send_per_hour"`
	// Organisation is an opaque, stable id of the organisation the domain
	// belongs to (ValidOrganisation): mailboxes may share only within one.
	// It says nothing about who owns the domain. Empty means the domain is
	// its own organisation (StandaloneOrganisation).
	Organisation string `json:"organisation,omitempty"`
}

// DKIMKey is one DKIM key of a domain.
type DKIMKey struct {
	Selector string `json:"selector"`
	// PrivateKeyPEM is a PKCS#8 RSA key.
	PrivateKeyPEM string `json:"private_key_pem"`
	Active        bool   `json:"active"`
}

// Mailbox is a mailbox people sign in to.
type Mailbox struct {
	// StorageID keys the mailbox's data in the service. It is random, never
	// reused, and stays the same for the life of the mailbox.
	StorageID string `json:"storage_id"`
	// Address is local@domain, lowercase; the domain is one of Domains.
	Address string `json:"address"`
	// PasswordHash is an argon2id hash (internal/passwd).
	PasswordHash string `json:"password_hash"`
	// QuotaBytes is the most the mailbox may store; 0 means no limit.
	QuotaBytes int64 `json:"quota_bytes"`
	// SendPerHour and SendPerDay cap the messages the mailbox sends.
	SendPerHour int `json:"send_per_hour"`
	SendPerDay  int `json:"send_per_day"`
	// DisplayName is the person's or team's name (ValidDisplayName); may be
	// empty.
	DisplayName string `json:"display_name,omitempty"`
	// AppPasswords are the mailbox's app passwords (at most
	// MaxAppPasswords).
	AppPasswords []AppPassword `json:"app_passwords,omitempty"`
	// AppPasswordsOnly: mail programs (IMAP, SMTP) sign in only with app
	// passwords; the main password works only for the self-service area.
	AppPasswordsOnly bool `json:"app_passwords_only,omitempty"`
	// Revision is the highest revision of the mailbox's own changes in the
	// mail service (AccountChange.Revision) that the panel has recorded. The
	// mail service keeps newer changes of its own over what this
	// configuration says until a push carries a Revision that includes them.
	Revision int64 `json:"revision,omitempty"`
	// PasswordSetAt, DisplayNameSetAt and AppPasswordsOnlySetAt are when
	// the panel last set that field itself (an admin; Unix seconds, 0:
	// never). A change of the same field the mailbox made in the mail
	// service at or before that time is dropped even if Revision does not
	// include it yet, so a reset by an admin takes effect with this push.
	// The panel decides the same way when it records the change.
	PasswordSetAt         int64 `json:"password_set_at,omitempty"`
	DisplayNameSetAt      int64 `json:"display_name_set_at,omitempty"`
	AppPasswordsOnlySetAt int64 `json:"app_passwords_only_set_at,omitempty"`
}

// Alias delivers mail for Address to Targets.
type Alias struct {
	// Address is local@domain; the local part "*" is the catch-all.
	Address string `json:"address"`
	// Targets are addresses: mailboxes of this service or external ones.
	Targets []string `json:"targets"`
}

// Limits of the whole service.
type Limits struct {
	MaxMessageBytes  int64 `json:"max_message_bytes"`
	MaxRecipients    int   `json:"max_recipients"`
	ConnectionsPerIP int   `json:"connections_per_ip"`
}

// Defaults for zero Limits and send limits.
const (
	DefaultMaxMessageBytes   = 50 << 20
	DefaultMaxRecipients     = 100
	DefaultConnectionsPerIP  = 20
	DefaultSendPerHour       = 100
	DefaultSendPerDay        = 500
	DefaultDomainSendPerHour = 300
	// MaxTargets bounds the targets of one alias.
	MaxTargets = 50
)

// WithDefaults returns l with zero values replaced by the defaults.
func (l Limits) WithDefaults() Limits {
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if l.MaxRecipients <= 0 {
		l.MaxRecipients = DefaultMaxRecipients
	}
	if l.ConnectionsPerIP <= 0 {
		l.ConnectionsPerIP = DefaultConnectionsPerIP
	}
	return l
}

var (
	labelRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	localPartRe = regexp.MustCompile(`^[a-z0-9!#$%&'*+/=?^_{|}~-]+(\.[a-z0-9!#$%&'*+/=?^_{|}~-]+)*$`)
	selectorRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	storageIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// ValidDomain tells whether d is a lowercase ASCII host name with at least
// two labels.
func ValidDomain(d string) bool {
	if len(d) > 253 || !strings.Contains(d, ".") {
		return false
	}
	for _, l := range strings.Split(d, ".") {
		if !labelRe.MatchString(l) {
			return false
		}
	}
	return true
}

// ValidLocalPart tells whether l is a lowercase dot-atom local part of at
// most 64 characters. "*" is not a valid local part (it means catch-all only
// in aliases).
func ValidLocalPart(l string) bool {
	return l != "*" && len(l) <= 64 && localPartRe.MatchString(l)
}

// SplitAddress splits local@domain. It does not validate.
func SplitAddress(addr string) (local, domain string, ok bool) {
	i := strings.LastIndexByte(addr, '@')
	if i <= 0 || i == len(addr)-1 {
		return "", "", false
	}
	return addr[:i], addr[i+1:], true
}

// ValidAddress tells whether addr is local@domain in the normalized form
// (lowercase, ASCII) this package uses.
func ValidAddress(addr string) bool {
	l, d, ok := SplitAddress(addr)
	return ok && ValidLocalPart(l) && ValidDomain(d)
}

// ValidStorageID tells whether id has the form of a storage id (32 lowercase
// hex characters).
func ValidStorageID(id string) bool { return storageIDRe.MatchString(id) }

// Validate checks the configuration. It does not need the network.
func (c *Config) Validate() error {
	if c.Hostname != "" && !ValidDomain(c.Hostname) {
		return fmt.Errorf("hostname %q is not a valid host name", c.Hostname)
	}
	if c.Hostname == "" && (len(c.Domains) > 0 || len(c.Mailboxes) > 0) {
		return errors.New("domains need a hostname")
	}
	for i, cert := range c.Certificates {
		if _, err := tls.X509KeyPair([]byte(cert.CertPEM), []byte(cert.KeyPEM)); err != nil {
			return fmt.Errorf("certificate %d: %v", i+1, err)
		}
	}
	domains := map[string]bool{}
	for _, d := range c.Domains {
		if err := validateDomain(d); err != nil {
			return err
		}
		if domains[d.Name] {
			return fmt.Errorf("domain %q is listed twice", d.Name)
		}
		domains[d.Name] = true
	}
	if c.AppPasswordKey != "" {
		if k, err := hex.DecodeString(c.AppPasswordKey); err != nil || len(k) != AppPasswordKeyBytes || strings.ToLower(c.AppPasswordKey) != c.AppPasswordKey {
			return errors.New("the app password key is not valid")
		}
	}
	addresses := map[string]bool{}
	storage := map[string]bool{}
	appIDs := map[string]bool{}
	for _, m := range c.Mailboxes {
		if err := validateMailbox(m); err != nil {
			return err
		}
		_, d, _ := SplitAddress(m.Address)
		if !domains[d] {
			return fmt.Errorf("mailbox %s: domain %s is not configured", m.Address, d)
		}
		if addresses[m.Address] {
			return fmt.Errorf("address %s is used twice", m.Address)
		}
		addresses[m.Address] = true
		if storage[m.StorageID] {
			return fmt.Errorf("mailbox %s: storage id is not valid or used twice", m.Address)
		}
		storage[m.StorageID] = true
		if len(m.AppPasswords) > MaxAppPasswords {
			return fmt.Errorf("mailbox %s: more than %d app passwords", m.Address, MaxAppPasswords)
		}
		if len(m.AppPasswords) > 0 && c.AppPasswordKey == "" {
			return fmt.Errorf("mailbox %s: app passwords need an app password key", m.Address)
		}
		for _, ap := range m.AppPasswords {
			if err := validateAppPassword(ap); err != nil || appIDs[ap.ID] {
				if err == nil {
					err = fmt.Errorf("app password id %q is not valid or used twice", ap.ID)
				}
				return fmt.Errorf("mailbox %s: %w", m.Address, err)
			}
			appIDs[ap.ID] = true
		}
	}
	for _, a := range c.Aliases {
		_, d, _ := SplitAddress(a.Address)
		if err := validateAlias(a); err != nil {
			return err
		}
		if !domains[d] {
			return fmt.Errorf("alias %q is not valid or its domain is not configured", a.Address)
		}
		if addresses[a.Address] {
			return fmt.Errorf("address %s is used twice", a.Address)
		}
		addresses[a.Address] = true
	}
	if c.PostmasterAddress != "" {
		if _, err := mail.ParseAddress(c.PostmasterAddress); err != nil || !ValidAddress(c.PostmasterAddress) {
			return fmt.Errorf("postmaster address %q is not valid", c.PostmasterAddress)
		}
	}
	if c.Limits.MaxMessageBytes < 0 || c.Limits.MaxRecipients < 0 || c.Limits.ConnectionsPerIP < 0 {
		return errors.New("negative limit")
	}
	if len(c.Filter.Blocklists) > MaxBlocklists {
		return fmt.Errorf("at most %d blocklists", MaxBlocklists)
	}
	for _, z := range c.Filter.Blocklists {
		if !ValidDomain(z) {
			return fmt.Errorf("blocklist %q is not a valid zone", MaskZone(z))
		}
	}
	if c.Filter.JunkScore < 0 || c.Filter.JunkScore > 1000 {
		return errors.New("junk score out of range")
	}
	return nil
}

// validateDomain checks one domain on its own.
func validateDomain(d Domain) error {
	if !ValidDomain(d.Name) {
		return fmt.Errorf("domain %q is not valid", d.Name)
	}
	if err := validateDKIM(d.DKIM); err != nil {
		return fmt.Errorf("domain %s: %w", d.Name, err)
	}
	if d.SendPerHour < 0 {
		return fmt.Errorf("domain %s: negative send limit", d.Name)
	}
	if d.Organisation != "" && !ValidOrganisation(d.Organisation) {
		return fmt.Errorf("domain %s: organisation %q is not valid", d.Name, d.Organisation)
	}
	return nil
}

func validateDKIMKey(k DKIMKey) error {
	if !selectorRe.MatchString(k.Selector) {
		return fmt.Errorf("DKIM selector %q is not valid or listed twice", k.Selector)
	}
	if _, err := ParseDKIMKey(k.PrivateKeyPEM); err != nil {
		return fmt.Errorf("DKIM key %s: %v", k.Selector, err)
	}
	return nil
}

func validateDKIM(keys []DKIMKey) error {
	active := 0
	selectors := map[string]bool{}
	for _, k := range keys {
		if selectors[k.Selector] {
			return fmt.Errorf("DKIM selector %q is not valid or listed twice", k.Selector)
		}
		if err := validateDKIMKey(k); err != nil {
			return err
		}
		selectors[k.Selector] = true
		if k.Active {
			active++
		}
	}
	if len(keys) > 0 && active != 1 {
		return errors.New("exactly one DKIM key must be active")
	}
	return nil
}

// validateMailbox checks one mailbox on its own, without its app
// passwords and without what depends on the others.
func validateMailbox(m Mailbox) error {
	if !ValidAddress(m.Address) {
		return fmt.Errorf("mailbox address %q is not valid", m.Address)
	}
	if !ValidStorageID(m.StorageID) {
		return fmt.Errorf("mailbox %s: storage id is not valid or used twice", m.Address)
	}
	if !passwd.WellFormed(m.PasswordHash) {
		return fmt.Errorf("mailbox %s: password hash is not an argon2id hash", m.Address)
	}
	if m.QuotaBytes < 0 || m.SendPerHour < 0 || m.SendPerDay < 0 || m.Revision < 0 ||
		m.PasswordSetAt < 0 || m.DisplayNameSetAt < 0 || m.AppPasswordsOnlySetAt < 0 {
		return fmt.Errorf("mailbox %s: negative limit", m.Address)
	}
	if !ValidDisplayName(m.DisplayName) {
		return fmt.Errorf("mailbox %s: the display name is not valid", m.Address)
	}
	return nil
}

func validateAppPassword(ap AppPassword) error {
	if !ValidAppPasswordID(ap.ID) {
		return fmt.Errorf("app password id %q is not valid or used twice", ap.ID)
	}
	if !macRe.MatchString(ap.MAC) || ap.Name == "" || !ValidDisplayName(ap.Name) {
		return fmt.Errorf("app password %s is not valid", ap.ID)
	}
	return nil
}

// validateAlias checks one alias on its own, without its domain.
func validateAlias(a Alias) error {
	l, _, ok := SplitAddress(a.Address)
	if !ok || (l != "*" && !ValidLocalPart(l)) {
		return fmt.Errorf("alias %q is not valid or its domain is not configured", a.Address)
	}
	if len(a.Targets) == 0 || len(a.Targets) > MaxTargets {
		return fmt.Errorf("alias %s needs 1 to %d targets", a.Address, MaxTargets)
	}
	for _, t := range a.Targets {
		if !ValidAddress(t) {
			return fmt.Errorf("alias %s: target %q is not a valid address", a.Address, t)
		}
	}
	return nil
}

// DropInvalid takes out of c, or cuts down, every domain, mailbox and alias
// that Validate would refuse, so that the data of one of them never stops
// the configuration of everyone from being loaded. It returns what it did,
// one line each, without secrets. What concerns the whole service
// (hostname, certificates, keys, filter, limits, postmaster) is left to
// Validate.
//
//   - A domain with an invalid name, listed twice or with a negative limit
//     is left out, and with it its mailboxes and aliases. Invalid DKIM keys
//     are left out; if then not exactly one is active, the domain is pushed
//     without DKIM keys. An invalid organisation id makes it stand alone.
//   - A mailbox that is not valid on its own, or whose address or storage id
//     is used twice, is left out. An invalid display name is removed.
//     Invalid app passwords and ids used twice are left out, and beyond
//     MaxAppPasswords the newest ones (the list is oldest first).
//   - An alias that is not valid or whose address is used twice is left
//     out; invalid targets are left out, beyond MaxTargets the last ones.
func (c *Config) DropInvalid() []string {
	var out []string
	note := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	domains := map[string]bool{}
	keptDomains := make([]Domain, 0, len(c.Domains))
	for _, d := range c.Domains {
		if !ValidDomain(d.Name) || domains[d.Name] || d.SendPerHour < 0 {
			note("domain %q left out: not valid or listed twice", d.Name)
			continue
		}
		if d.Organisation != "" && !ValidOrganisation(d.Organisation) {
			note("domain %s: organisation id not valid; it stands alone", d.Name)
			d.Organisation = ""
		}
		if validateDKIM(d.DKIM) != nil {
			keys := make([]DKIMKey, 0, len(d.DKIM))
			selectors := map[string]bool{}
			for _, k := range d.DKIM {
				if err := validateDKIMKey(k); err != nil || selectors[k.Selector] {
					note("domain %s: DKIM key %q left out", d.Name, k.Selector)
					continue
				}
				selectors[k.Selector] = true
				keys = append(keys, k)
			}
			if validateDKIM(keys) != nil {
				note("domain %s: no single active DKIM key; mail is sent without a DKIM signature", d.Name)
				keys = []DKIMKey{}
			}
			d.DKIM = keys
		}
		domains[d.Name] = true
		keptDomains = append(keptDomains, d)
	}
	c.Domains = keptDomains

	addresses := map[string]bool{}
	storage := map[string]bool{}
	appIDs := map[string]bool{}
	keptMailboxes := make([]Mailbox, 0, len(c.Mailboxes))
	for _, m := range c.Mailboxes {
		if !ValidDisplayName(m.DisplayName) {
			note("mailbox %q: display name not valid; removed", m.Address)
			m.DisplayName = ""
		}
		_, d, _ := SplitAddress(m.Address)
		if err := validateMailbox(m); err != nil || !domains[d] || addresses[m.Address] || storage[m.StorageID] {
			note("mailbox %q left out: not valid, its domain left out, or its address or storage id used twice", m.Address)
			continue
		}
		addresses[m.Address], storage[m.StorageID] = true, true
		if len(m.AppPasswords) > 0 && c.AppPasswordKey == "" {
			note("mailbox %s: app passwords left out: no app password key", m.Address)
			m.AppPasswords = nil
		}
		if len(m.AppPasswords) > 0 {
			aps := make([]AppPassword, 0, min(len(m.AppPasswords), MaxAppPasswords))
			for _, ap := range m.AppPasswords {
				if validateAppPassword(ap) != nil || appIDs[ap.ID] {
					note("mailbox %s: app password %q left out: not valid or its id used twice", m.Address, ap.ID)
					continue
				}
				if len(aps) == MaxAppPasswords {
					note("mailbox %s: app password %q left out: more than %d", m.Address, ap.ID, MaxAppPasswords)
					continue
				}
				appIDs[ap.ID] = true
				aps = append(aps, ap)
			}
			m.AppPasswords = aps
		}
		keptMailboxes = append(keptMailboxes, m)
	}
	c.Mailboxes = keptMailboxes

	keptAliases := make([]Alias, 0, len(c.Aliases))
	for _, a := range c.Aliases {
		targets := make([]string, 0, min(len(a.Targets), MaxTargets))
		for _, t := range a.Targets {
			switch {
			case !ValidAddress(t):
				note("alias %q: target %q left out: not a valid address", a.Address, t)
			case len(targets) == MaxTargets:
				note("alias %q: target %q left out: more than %d", a.Address, t, MaxTargets)
			default:
				targets = append(targets, t)
			}
		}
		a.Targets = targets
		_, d, _ := SplitAddress(a.Address)
		if validateAlias(a) != nil || !domains[d] || addresses[a.Address] {
			note("alias %q left out: not valid, its domain left out, or its address used twice", a.Address)
			continue
		}
		addresses[a.Address] = true
		keptAliases = append(keptAliases, a)
	}
	c.Aliases = keptAliases
	return out
}

// Hash is a fingerprint of the configuration: the panel compares it with
// the one the service reports to decide whether a push is needed. Slices are
// hashed in a canonical order, so the order the panel builds them in does
// not matter.
func (c *Config) Hash() string {
	cp := *c
	cp.Domains = slices.Clone(c.Domains)
	cp.Mailboxes = slices.Clone(c.Mailboxes)
	cp.Aliases = slices.Clone(c.Aliases)
	slices.SortFunc(cp.Domains, func(a, b Domain) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(cp.Mailboxes, func(a, b Mailbox) int { return strings.Compare(a.Address, b.Address) })
	slices.SortFunc(cp.Aliases, func(a, b Alias) int { return strings.Compare(a.Address, b.Address) })
	b, err := json.Marshal(cp)
	if err != nil {
		panic(err) // only plain types
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
