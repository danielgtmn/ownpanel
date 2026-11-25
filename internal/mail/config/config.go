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
	return len(l) <= 64 && localPartRe.MatchString(l)
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
		if !ValidDomain(d.Name) {
			return fmt.Errorf("domain %q is not valid", d.Name)
		}
		if domains[d.Name] {
			return fmt.Errorf("domain %q is listed twice", d.Name)
		}
		domains[d.Name] = true
		active := 0
		selectors := map[string]bool{}
		for _, k := range d.DKIM {
			if !selectorRe.MatchString(k.Selector) || selectors[k.Selector] {
				return fmt.Errorf("domain %s: DKIM selector %q is not valid or listed twice", d.Name, k.Selector)
			}
			selectors[k.Selector] = true
			if _, err := ParseDKIMKey(k.PrivateKeyPEM); err != nil {
				return fmt.Errorf("domain %s: DKIM key %s: %v", d.Name, k.Selector, err)
			}
			if k.Active {
				active++
			}
		}
		if len(d.DKIM) > 0 && active != 1 {
			return fmt.Errorf("domain %s: exactly one DKIM key must be active", d.Name)
		}
		if d.SendPerHour < 0 {
			return fmt.Errorf("domain %s: negative send limit", d.Name)
		}
	}
	addresses := map[string]bool{}
	storage := map[string]bool{}
	for _, m := range c.Mailboxes {
		if !ValidAddress(m.Address) {
			return fmt.Errorf("mailbox address %q is not valid", m.Address)
		}
		_, d, _ := SplitAddress(m.Address)
		if !domains[d] {
			return fmt.Errorf("mailbox %s: domain %s is not configured", m.Address, d)
		}
		if addresses[m.Address] {
			return fmt.Errorf("address %s is used twice", m.Address)
		}
		addresses[m.Address] = true
		if !ValidStorageID(m.StorageID) || storage[m.StorageID] {
			return fmt.Errorf("mailbox %s: storage id is not valid or used twice", m.Address)
		}
		storage[m.StorageID] = true
		if !passwd.WellFormed(m.PasswordHash) {
			return fmt.Errorf("mailbox %s: password hash is not an argon2id hash", m.Address)
		}
		if m.QuotaBytes < 0 || m.SendPerHour < 0 || m.SendPerDay < 0 {
			return fmt.Errorf("mailbox %s: negative limit", m.Address)
		}
	}
	for _, a := range c.Aliases {
		l, d, ok := SplitAddress(a.Address)
		if !ok || (l != "*" && !ValidLocalPart(l)) || !domains[d] {
			return fmt.Errorf("alias %q is not valid or its domain is not configured", a.Address)
		}
		if addresses[a.Address] {
			return fmt.Errorf("address %s is used twice", a.Address)
		}
		addresses[a.Address] = true
		if len(a.Targets) == 0 || len(a.Targets) > MaxTargets {
			return fmt.Errorf("alias %s needs 1 to %d targets", a.Address, MaxTargets)
		}
		for _, t := range a.Targets {
			if !ValidAddress(t) {
				return fmt.Errorf("alias %s: target %q is not a valid address", a.Address, t)
			}
		}
	}
	if c.PostmasterAddress != "" {
		if _, err := mail.ParseAddress(c.PostmasterAddress); err != nil || !ValidAddress(c.PostmasterAddress) {
			return fmt.Errorf("postmaster address %q is not valid", c.PostmasterAddress)
		}
	}
	if c.Limits.MaxMessageBytes < 0 || c.Limits.MaxRecipients < 0 || c.Limits.ConnectionsPerIP < 0 {
		return errors.New("negative limit")
	}
	return nil
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
