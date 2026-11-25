// Package signin checks the passwords of mailboxes for IMAP and submission
// (SMTP AUTH): the same throttling, cache and timing rules for every
// protocol, so one cannot be used to get around the other.
//
//   - Failures are counted per (IP, address) and per IP before any hashing,
//     never per address alone: that would let anyone lock a person out.
//     IPv6 addresses count per /64.
//   - A successful sign-in is cached in memory for CacheTTL under an HMAC of
//     address, password and the stored hash, so mail clients that open many
//     connections do not cost an argon2id hash each. A new hash in a new
//     configuration misses the cache by construction.
//   - Unknown addresses are verified against a dummy hash, so the answer
//     takes as long as for a real mailbox. Errors never tell whether an
//     address exists.
//   - Many failures for one address from many IPs are recorded as an event
//     (at most once per EventEvery per address) for the panel.
package signin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/passwd"
)

// Limits. Failures are counted in fixed windows that start with the first
// failure.
const (
	Window      = 15 * time.Minute
	PairLimit   = 5  // failures per (IP, address) per Window
	IPLimit     = 30 // failures per IP per Window
	CacheTTL    = 15 * time.Minute
	EventWindow = time.Hour
	// EventFailures failures for one address from at least EventIPs
	// different IPs within EventWindow record an event.
	EventFailures = 20
	EventIPs      = 5
	EventEvery    = time.Hour
	// MaxPasswordBytes: longer passwords are refused without hashing.
	MaxPasswordBytes = 1024
	// HashSlots is how many hashes the service computes at the same time.
	HashSlots = 2

	maxEntries = 100_000 // per map, bounds memory under attack
)

// Errors of Check. Protocols map them to their answers; neither says
// whether the address exists.
var (
	ErrFailed    = errors.New("wrong address or password")
	ErrThrottled = errors.New("too many failed sign-ins, try again later")
)

// Options configure a Checker.
type Options struct {
	// Slots bound concurrent hashing; nil means passwd.NewSlots(HashSlots).
	Slots passwd.Slots
	// Event records an event for the panel; nil drops them. It is called
	// without locks held but on the signing-in goroutine.
	Event func(config.Event)
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
}

// Checker checks sign-ins. It is safe for concurrent use; one Checker is
// shared by all protocols of the service.
type Checker struct {
	opts Options
	key  [32]byte
	// hashes counts argon2id verifications (tests).
	hashes atomic.Int64

	dummyOnce sync.Once
	dummy     string

	mu       sync.Mutex
	pairs    map[pairKey]*window
	ips      map[netip.Prefix]*window
	cache    map[[32]byte]time.Time
	byAddr   map[string]*addrFailures
	reported map[string]time.Time
	swept    time.Time
}

type pairKey struct {
	ip   netip.Prefix
	addr string
}

type window struct {
	start time.Time
	n     int
}

type addrFailures struct {
	start time.Time
	n     int
	ips   map[netip.Prefix]bool // bounded by EventIPs
}

// New returns a Checker.
func New(opts Options) *Checker {
	if opts.Slots == nil {
		opts.Slots = passwd.NewSlots(HashSlots)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	c := &Checker{
		opts:     opts,
		pairs:    map[pairKey]*window{},
		ips:      map[netip.Prefix]*window{},
		cache:    map[[32]byte]time.Time{},
		byAddr:   map[string]*addrFailures{},
		reported: map[string]time.Time{},
	}
	if _, err := rand.Read(c.key[:]); err != nil {
		panic(err)
	}
	return c
}

// ipKey is the unit IP limits count in: the address for IPv4, the /64 for
// IPv6 (one customer usually has at least a /64).
func ipKey(ip netip.Addr) netip.Prefix {
	ip = ip.Unmap()
	bits := 32
	if ip.Is6() {
		bits = 64
	}
	p, err := ip.Prefix(bits)
	if err != nil {
		return netip.Prefix{}
	}
	return p
}

// Check signs in address with password from ip against dir. On success it
// returns the mailbox (a copy). address is normalized here (lowercase).
func (c *Checker) Check(dir *directory.Directory, ip netip.Addr, address, password string) (config.Mailbox, error) {
	address = directory.Normalize(address)
	ipk := ipKey(ip)
	pk := pairKey{ipk, address}
	now := c.opts.Now()

	// Charge the attempt as a failure before any hashing, so parallel
	// attempts cannot get past the limits; a success refunds it.
	c.mu.Lock()
	c.sweepLocked(now)
	throttled := over(c.ips, ipk, now, IPLimit) || over(c.pairs, pk, now, PairLimit)
	if !throttled {
		charge(c.ips, ipk, now)
		charge(c.pairs, pk, now)
	}
	c.mu.Unlock()
	if throttled {
		return config.Mailbox{}, ErrThrottled
	}

	m, ok := dir.Mailbox(address)
	if ok && password != "" && len(password) <= MaxPasswordBytes {
		ck := c.cacheKey(address, password, m.PasswordHash)
		c.mu.Lock()
		exp, cached := c.cache[ck]
		c.mu.Unlock()
		cached = cached && now.Before(exp)
		if cached || c.verify(m.PasswordHash, password) {
			c.mu.Lock()
			if !cached && len(c.cache) < maxEntries {
				c.cache[ck] = now.Add(CacheTTL)
			}
			delete(c.pairs, pk)
			if w := c.ips[ipk]; w != nil && w.n > 0 {
				w.n--
			}
			c.mu.Unlock()
			return *m, nil
		}
	} else if len(password) <= MaxPasswordBytes {
		// Same work as for a mailbox, so the time does not tell.
		c.verify(c.dummyHash(), password)
	}
	if ok {
		c.addressFailed(now, ipk, m)
	}
	return config.Mailbox{}, ErrFailed
}

// Valid tells whether a mailbox signed in earlier still exists in dir with
// the same storage and password hash. Sessions call it after a
// configuration change and end themselves when it is false.
func Valid(dir *directory.Directory, m config.Mailbox) bool {
	cur, ok := dir.Mailbox(m.Address)
	return ok && cur.StorageID == m.StorageID && cur.PasswordHash == m.PasswordHash
}

func (c *Checker) verify(hash, password string) bool {
	c.hashes.Add(1)
	return c.opts.Slots.Verify(hash, password)
}

func (c *Checker) dummyHash() string {
	c.dummyOnce.Do(func() {
		pw := make([]byte, 16)
		rand.Read(pw)
		h, err := c.opts.Slots.Hash(string(pw))
		if err != nil {
			panic(err)
		}
		c.dummy = h
	})
	return c.dummy
}

func (c *Checker) cacheKey(address, password, hash string) [32]byte {
	mac := hmac.New(sha256.New, c.key[:])
	for _, s := range []string{address, password, hash} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		mac.Write(n[:])
		mac.Write([]byte(s))
	}
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}

func over[K comparable](m map[K]*window, k K, now time.Time, limit int) bool {
	w := m[k]
	return w != nil && now.Sub(w.start) < Window && w.n >= limit
}

func charge[K comparable](m map[K]*window, k K, now time.Time) {
	w := m[k]
	if w == nil || now.Sub(w.start) >= Window {
		if w == nil && len(m) >= maxEntries {
			return
		}
		w = &window{start: now}
		m[k] = w
	}
	w.n++
}

// addressFailed tracks failures of an existing mailbox across IPs and
// records an event when they look like a distributed attack.
func (c *Checker) addressFailed(now time.Time, ipk netip.Prefix, m *config.Mailbox) {
	var ev *config.Event
	c.mu.Lock()
	a := c.byAddr[m.Address]
	if a == nil || now.Sub(a.start) >= EventWindow {
		if a == nil && len(c.byAddr) >= maxEntries {
			c.mu.Unlock()
			return
		}
		a = &addrFailures{start: now, ips: map[netip.Prefix]bool{}}
		c.byAddr[m.Address] = a
	}
	a.n++
	if len(a.ips) < EventIPs {
		a.ips[ipk] = true
	}
	if a.n >= EventFailures && len(a.ips) >= EventIPs {
		if last, ok := c.reported[m.Address]; !ok || now.Sub(last) >= EventEvery {
			c.reported[m.Address] = now
			ev = &config.Event{
				Kind: config.EventSignInFailures, StorageID: m.StorageID, Address: m.Address,
				Detail: fmt.Sprintf("%d failed sign-ins from at least %d different networks within %s", a.n, len(a.ips), shortDur(now.Sub(a.start))),
			}
		}
	}
	c.mu.Unlock()
	if ev != nil && c.opts.Event != nil {
		c.opts.Event(*ev)
	}
}

func shortDur(d time.Duration) string {
	if d < time.Minute {
		return "a minute"
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes())+1)
}

// sweepLocked drops expired entries, at most once a minute.
func (c *Checker) sweepLocked(now time.Time) {
	if now.Sub(c.swept) < time.Minute {
		return
	}
	c.swept = now
	for k, w := range c.pairs {
		if now.Sub(w.start) >= Window {
			delete(c.pairs, k)
		}
	}
	for k, w := range c.ips {
		if now.Sub(w.start) >= Window {
			delete(c.ips, k)
		}
	}
	for k, exp := range c.cache {
		if !now.Before(exp) {
			delete(c.cache, k)
		}
	}
	for k, a := range c.byAddr {
		if now.Sub(a.start) >= EventWindow {
			delete(c.byAddr, k)
		}
	}
	for k, t := range c.reported {
		if now.Sub(t) >= EventEvery {
			delete(c.reported, k)
		}
	}
}
