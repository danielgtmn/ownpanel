// Package signin checks the passwords of mailboxes for IMAP and submission
// (SMTP AUTH): the same throttling, cache and timing rules for every
// protocol, so one cannot be used to get around the other.
//
//   - Failures are counted per (IP, address) and per IP before any hashing,
//     never per address alone: that would let anyone lock a person out.
//     IPv6 addresses count per /48. When a table is full, a new IP or pair
//     is refused (fail closed) until expired entries have been swept.
//   - A successful sign-in is cached in memory for CacheTTL under an HMAC of
//     address, password and the stored hash, so mail clients that open many
//     connections do not cost an argon2id hash each. A new hash in a new
//     configuration misses the cache by construction.
//   - Unknown addresses are verified against a dummy hash, so the answer
//     takes as long as for a real mailbox. Errors never tell whether an
//     address exists.
//   - Many failures for one address from many IPs are recorded as an event
//     (at most once per EventEvery per address) for the panel.
//   - App passwords ("op_<id>_<secret>", config.ParseAppPassword) are looked
//     up by id in the mailbox and checked with one HMAC (never argon2id), so
//     the time says nothing about how many a mailbox has. Anything of that
//     form is only ever checked as an app password. With
//     Mailbox.AppPasswordsOnly the main password is refused here. The use of
//     an app password is reported (config.EventAppPasswordUsed) at most once
//     per config.AppPasswordUseEvery, with the network, not the address.
//
// Check is for mail programs (IMAP, submission; later DAV and ManageSieve).
// The self-service area must not use it: there only the main password is
// valid, also with AppPasswordsOnly; it uses CheckMain on a Checker of its
// own.
package signin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
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
	// dummyKey checks app passwords when no key is configured.
	dummyKey string
	// hashes counts argon2id verifications (tests).
	hashes atomic.Int64

	dummyOnce sync.Once
	dummy     string

	// max bounds each table (maxEntries; tests lower it).
	max int

	mu       sync.Mutex
	pairs    map[pairKey]*window
	ips      map[netip.Prefix]*window
	cache    map[[32]byte]time.Time
	byAddr   map[string]*addrFailures
	reported map[string]time.Time
	// appUsed is when the use of an app password (storage id + "/" + id)
	// was last reported.
	appUsed map[string]time.Time
	swept   time.Time
}

// Grant is a successful sign-in: the mailbox as it was then (a copy) and
// the credential used. Valid checks it again later.
type Grant struct {
	config.Mailbox
	// AppPassword is the id of the app password used; "" for the main
	// password.
	AppPassword string
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
		max:      maxEntries,
		pairs:    map[pairKey]*window{},
		ips:      map[netip.Prefix]*window{},
		cache:    map[[32]byte]time.Time{},
		byAddr:   map[string]*addrFailures{},
		reported: map[string]time.Time{},
		appUsed:  map[string]time.Time{},
	}
	if _, err := rand.Read(c.key[:]); err != nil {
		panic(err)
	}
	c.dummyKey = config.NewAppPasswordKey()
	return c
}

// ipKey is the unit IP limits count in: the address for IPv4, the /48 for
// IPv6 (one customer often gets a whole /48, and a /64 is cheap to vary).
func ipKey(ip netip.Addr) netip.Prefix {
	ip = ip.Unmap()
	bits := 32
	if ip.Is6() {
		bits = 48
	}
	p, err := ip.Prefix(bits)
	if err != nil {
		return netip.Prefix{}
	}
	return p
}

// Check signs in address with password (the main password or an app
// password) from ip against dir. On success it returns the mailbox (a copy)
// and the credential. address is normalized here (lowercase).
func (c *Checker) Check(dir *directory.Directory, ip netip.Addr, address, password string) (Grant, error) {
	address = directory.Normalize(address)
	now := c.opts.Now()
	pk, ipw, err := c.begin(ip, address, now)
	if err != nil {
		return Grant{}, err
	}
	ipk := pk.ip

	m, ok := dir.Mailbox(address)
	if ok && m.Shared() {
		// Nobody signs in to a shared mailbox: it answers like an unknown
		// address.
		m, ok = nil, false
	}
	if id, secret, isApp := config.ParseAppPassword(password); isApp {
		return c.checkApp(dir, ip, now, pk, ipw, m, ok, id, secret)
	}
	if ok && !m.AppPasswordsOnly && password != "" && len(password) <= MaxPasswordBytes {
		ck := c.cacheKey(address, "", password, m.PasswordHash)
		c.mu.Lock()
		exp, cached := c.cache[ck]
		c.mu.Unlock()
		cached = cached && now.Before(exp)
		if cached || c.verify(m.PasswordHash, password) {
			c.mu.Lock()
			if !cached && len(c.cache) < c.max {
				c.cache[ck] = now.Add(CacheTTL)
			}
			c.refundLocked(pk, ipw)
			c.mu.Unlock()
			return Grant{Mailbox: *m}, nil
		}
	} else if len(password) <= MaxPasswordBytes {
		// Same work as for a mailbox, so the time does not tell.
		c.verify(c.dummyHash(), password)
	}
	if ok {
		c.addressFailed(now, ipk, m)
	}
	return Grant{}, ErrFailed
}

// begin charges an attempt as a failure before any hashing, so parallel
// attempts cannot get past the limits; a success refunds exactly this
// charge (refundLocked), never one of another attempt.
func (c *Checker) begin(ip netip.Addr, address string, now time.Time) (pairKey, *window, error) {
	ipk := ipKey(ip)
	pk := pairKey{ipk, address}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now)
	if over(c.ips, ipk, now, IPLimit) || over(c.pairs, pk, now, PairLimit) ||
		full(c.ips, ipk, c.max) || full(c.pairs, pk, c.max) {
		return pk, nil, ErrThrottled
	}
	ipw := charge(c.ips, ipk, now)
	charge(c.pairs, pk, now)
	return pk, ipw, nil
}

// CheckMain signs in address with its main password, for the mailbox's
// own area (the self-service API of the mail service):
//
//   - only the main password counts: anything of the form of an app
//     password fails, also a valid one;
//   - Mailbox.AppPasswordsOnly does not apply (it is about mail programs);
//   - nothing is cached: sign-ins here are rare and start a session.
//
// Throttling, timing and failure events are those of Check. Use a Checker
// of its own for this (New with its own Slots): its tables and hashing
// slots are then separate from those of mail programs, so a flood here
// cannot slow IMAP or sending, nor the other way round.
func (c *Checker) CheckMain(dir *directory.Directory, ip netip.Addr, address, password string) (Grant, error) {
	address = directory.Normalize(address)
	now := c.opts.Now()
	pk, ipw, err := c.begin(ip, address, now)
	if err != nil {
		return Grant{}, err
	}
	m, ok := dir.Mailbox(address)
	if ok && m.Shared() {
		m, ok = nil, false
	}
	if ok && password != "" && len(password) <= MaxPasswordBytes && !config.LooksLikeAppPassword(password) {
		if c.verify(m.PasswordHash, password) {
			c.mu.Lock()
			c.refundLocked(pk, ipw)
			c.mu.Unlock()
			return Grant{Mailbox: *m}, nil
		}
	} else if len(password) <= MaxPasswordBytes {
		// Same work as for a mailbox, so the time does not tell.
		c.verify(c.dummyHash(), password)
	}
	if ok {
		c.addressFailed(now, pk.ip, m)
	}
	return Grant{}, ErrFailed
}

// refundLocked takes back the failure charged for a successful attempt.
func (c *Checker) refundLocked(pk pairKey, ipw *window) {
	delete(c.pairs, pk)
	// Only in the window this attempt was charged to: when that has
	// expired meanwhile (the hash waited for a slot), the new one holds
	// only failures of other attempts.
	if c.ips[pk.ip] == ipw && ipw.n > 0 {
		ipw.n--
	}
}

// checkApp checks an app password: one HMAC whether or not the mailbox
// and the id exist.
func (c *Checker) checkApp(dir *directory.Directory, ip netip.Addr, now time.Time, pk pairKey, ipw *window,
	m *config.Mailbox, ok bool, id, secret string) (Grant, error) {
	key := dir.Config().AppPasswordKey
	var ap config.AppPassword
	found := false
	if ok {
		for _, a := range m.AppPasswords {
			if a.ID == id {
				ap, found = a, true
				break
			}
		}
	}
	if !found || key == "" {
		// Same work as for a real one, so the time does not tell.
		ap = config.AppPassword{ID: id, MAC: strings.Repeat("0", 64)}
		if key == "" {
			key = c.dummyKey
		}
	}
	sid := ""
	if ok {
		sid = m.StorageID
	}
	if config.CheckAppPassword(key, sid, ap, secret) && found {
		c.mu.Lock()
		c.refundLocked(pk, ipw)
		report := false
		uk := m.StorageID + "/" + id
		if last, seen := c.appUsed[uk]; (!seen || now.Sub(last) >= config.AppPasswordUseEvery) && (seen || len(c.appUsed) < c.max) {
			c.appUsed[uk], report = now, true
		}
		c.mu.Unlock()
		if report && c.opts.Event != nil {
			network := config.Network(ip)
			c.opts.Event(config.Event{
				Kind: config.EventAppPasswordUsed, StorageID: m.StorageID, Address: m.Address,
				Detail:  fmt.Sprintf("%s from %s", ap.Name, network),
				Account: &config.AccountChange{AppPasswordID: id, Network: network},
			})
		}
		return Grant{Mailbox: *m, AppPassword: id}, nil
	}
	if ok {
		c.addressFailed(now, pk.ip, m)
	}
	return Grant{}, ErrFailed
}

// Valid tells whether a sign-in is still good in dir: the mailbox exists
// with the same storage, and the credential still works — the same main
// password hash and AppPasswordsOnly off, or the app password still there
// unchanged. Sessions call it after a configuration change (and an
// account change) and end themselves when it is false.
func Valid(dir *directory.Directory, g Grant) bool {
	cur, ok := dir.Mailbox(g.Address)
	if !ok || cur.StorageID != g.StorageID || cur.Shared() {
		return false
	}
	if g.AppPassword == "" {
		return cur.PasswordHash == g.PasswordHash && !cur.AppPasswordsOnly
	}
	mac := ""
	for _, ap := range g.AppPasswords {
		if ap.ID == g.AppPassword {
			mac = ap.MAC
		}
	}
	for _, ap := range cur.AppPasswords {
		if ap.ID == g.AppPassword {
			return mac != "" && ap.MAC == mac
		}
	}
	return false
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

// cacheKey keys a cached sign-in by address, credential id ("" for the
// main password), password and the stored hash.
func (c *Checker) cacheKey(address, credential, password, hash string) [32]byte {
	mac := hmac.New(sha256.New, c.key[:])
	for _, s := range []string{address, credential, password, hash} {
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

// full tells whether k is new and m has no room for it. Such attempts are
// refused rather than let through uncounted; the periodic sweep makes room
// again as windows expire.
func full[K comparable](m map[K]*window, k K, max int) bool {
	_, ok := m[k]
	return !ok && len(m) >= max
}

// charge counts a failure for k and returns the window it went into.
func charge[K comparable](m map[K]*window, k K, now time.Time) *window {
	w := m[k]
	if w == nil || now.Sub(w.start) >= Window {
		w = &window{start: now}
		m[k] = w
	}
	w.n++
	return w
}

// addressFailed tracks failures of an existing mailbox across IPs and
// records an event when they look like a distributed attack.
func (c *Checker) addressFailed(now time.Time, ipk netip.Prefix, m *config.Mailbox) {
	var ev *config.Event
	c.mu.Lock()
	a := c.byAddr[m.Address]
	if a == nil || now.Sub(a.start) >= EventWindow {
		if a == nil && len(c.byAddr) >= c.max {
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
	for k, t := range c.appUsed {
		if now.Sub(t) >= config.AppPasswordUseEvery {
			delete(c.appUsed, k)
		}
	}
}
