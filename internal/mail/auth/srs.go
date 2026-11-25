package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SRS rewrites the envelope sender of forwarded mail (Sender Rewriting
// Scheme), so the receiving server checks SPF against our domain, and
// routes bounces to such addresses back to the original sender:
//
//	SRS0=<hash>=<tt>=<orig domain>=<orig local>@<alias domain>
//
// tt is the day (days since 1970 modulo 1024) in two base32 characters,
// hash the first 40 bits of an HMAC-SHA256 over "tt=domain=local"
// (lowercase) in lowercase base32. Everything is compared without case,
// because servers on the way may change it.
type SRS struct {
	key []byte
	now func() time.Time
}

// SRSMaxAge is how long an SRS address is accepted for bounces.
const SRSMaxAge = 21

// SRS errors.
var (
	ErrNotSRS     = errors.New("not an SRS address")
	ErrSRSHash    = errors.New("SRS address with a wrong signature")
	ErrSRSExpired = errors.New("SRS address too old")
)

// NewSRS returns an SRS with key (at least 16 bytes). now may be nil.
func NewSRS(key []byte, now func() time.Time) *SRS {
	if now == nil {
		now = time.Now
	}
	return &SRS{key: key, now: now}
}

// srsKeyFile is the key's file in the data directory.
const srsKeyFile = "srs.key"

// LoadSRSKey reads the SRS key from dir, creating it (32 random bytes,
// mode 0600) on first use.
func LoadSRSKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, srsKeyFile)
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) < 16 {
			return nil, fmt.Errorf("%s is too short", path)
		}
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".srs-*")
	if err != nil {
		return nil, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	// Link instead of rename: a key another process created meanwhile wins.
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadSRSKey(dir)
		}
		return nil, err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return key, nil
}

const srsAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

var hashEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func (s *SRS) day() int { return int(s.now().Unix()/86400) % 1024 }

func (s *SRS) hash(tt, domain, local string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(strings.ToLower(tt + "=" + domain + "=" + local)))
	return strings.ToLower(hashEncoding.EncodeToString(m.Sum(nil)[:5]))
}

// Forward returns the SRS address for sender, at forwarder (one of our
// domains).
func (s *SRS) Forward(sender, forwarder string) (string, error) {
	i := strings.LastIndexByte(sender, '@')
	if i <= 0 || i == len(sender)-1 {
		return "", fmt.Errorf("invalid sender %q", sender)
	}
	local, domain := sender[:i], strings.ToLower(sender[i+1:])
	d := s.day()
	tt := string([]byte{srsAlphabet[d>>5], srsAlphabet[d&31]})
	return "SRS0=" + s.hash(tt, domain, local) + "=" + tt + "=" + domain + "=" + local + "@" + forwarder, nil
}

// IsSRS tells whether the local part looks like an SRS0 address.
func IsSRS(local string) bool {
	return len(local) > 5 && strings.EqualFold(local[:5], "SRS0=")
}

// Reverse checks an SRS0 address and returns the original sender.
func (s *SRS) Reverse(addr string) (string, error) {
	i := strings.LastIndexByte(addr, '@')
	if i <= 0 {
		return "", ErrNotSRS
	}
	local := addr[:i]
	if !IsSRS(local) {
		return "", ErrNotSRS
	}
	parts := strings.SplitN(local[5:], "=", 4)
	if len(parts) != 4 || parts[2] == "" || parts[3] == "" || len(parts[1]) != 2 {
		return "", ErrNotSRS
	}
	hash, tt, domain, origLocal := strings.ToLower(parts[0]), parts[1], strings.ToLower(parts[2]), parts[3]
	if !hmac.Equal([]byte(hash), []byte(s.hash(tt, domain, origLocal))) {
		return "", ErrSRSHash
	}
	a := strings.IndexByte(srsAlphabet, toUpper(tt[0]))
	b := strings.IndexByte(srsAlphabet, toUpper(tt[1]))
	if a < 0 || b < 0 {
		return "", ErrNotSRS
	}
	age := (s.day() - (a<<5 | b) + 1024) % 1024
	if age > SRSMaxAge {
		return "", ErrSRSExpired
	}
	return origLocal + "@" + domain, nil
}

func toUpper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}
