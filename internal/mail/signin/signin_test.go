package signin

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/passwd"
)

var slots = passwd.NewSlots(4)

func dirWith(t *testing.T, pw string) *directory.Directory {
	t.Helper()
	h, err := slots.Hash(pw)
	if err != nil {
		t.Fatal(err)
	}
	return directory.New(&config.Config{
		Hostname:  "mail.example.net",
		Domains:   []config.Domain{{Name: "example.org"}},
		Mailboxes: []config.Mailbox{{StorageID: mailtest.ID('a'), Address: "alice@example.org", PasswordHash: h}},
	})
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestCheck(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	var events []config.Event
	c := New(Options{Slots: slots, Now: clk.now, Event: func(e config.Event) { events = append(events, e) }})
	dir := dirWith(t, "pw")
	a := ip("192.0.2.1")

	m, err := c.Check(dir, a, " Alice@Example.org", "pw")
	if err != nil || m.StorageID != mailtest.ID('a') {
		t.Fatalf("ok: %+v %v", m, err)
	}
	// Cached: no second hash.
	n := c.hashes.Load()
	if _, err := c.Check(dir, ip("198.51.100.7"), "alice@example.org", "pw"); err != nil || c.hashes.Load() != n {
		t.Fatalf("cache: %v, %d hashes", err, c.hashes.Load()-n)
	}
	// A new hash (password changed in the panel) misses the cache.
	dir2 := dirWith(t, "pw")
	if _, err := c.Check(dir2, a, "alice@example.org", "pw"); err != nil || c.hashes.Load() != n+1 {
		t.Fatalf("new hash: %v", err)
	}
	if Valid(dir2, m) || !Valid(dir, m) {
		t.Error("Valid")
	}
	// The cache expires.
	clk.add(CacheTTL)
	if _, err := c.Check(dir, a, "alice@example.org", "pw"); err != nil || c.hashes.Load() != n+2 {
		t.Fatalf("expired: %v", err)
	}

	// Unknown addresses and wrong passwords: the same error, a hash each.
	n = c.hashes.Load()
	_, e1 := c.Check(dir, a, "nobody@example.org", "pw")
	_, e2 := c.Check(dir, a, "alice@example.org", "wrong")
	if !errors.Is(e1, ErrFailed) || !errors.Is(e2, ErrFailed) || c.hashes.Load() != n+2 {
		t.Fatalf("failures: %v %v", e1, e2)
	}
}

func TestThrottle(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	c := New(Options{Slots: slots, Now: clk.now})
	dir := dirWith(t, "pw")
	a, b := ip("192.0.2.1"), ip("192.0.2.2")

	for i := 0; i < PairLimit; i++ {
		c.Check(dir, a, "alice@example.org", "wrong")
	}
	n := c.hashes.Load()
	if _, err := c.Check(dir, a, "alice@example.org", "pw"); !errors.Is(err, ErrThrottled) || c.hashes.Load() != n {
		t.Fatalf("pair limit: %v", err)
	}
	// Another IP is not affected: never per address alone.
	if _, err := c.Check(dir, b, "alice@example.org", "pw"); err != nil {
		t.Fatalf("other IP: %v", err)
	}
	// After the window the pair may try again.
	clk.add(Window)
	if _, err := c.Check(dir, a, "alice@example.org", "pw"); err != nil {
		t.Fatalf("after window: %v", err)
	}

	// Per IP over many addresses; IPv6 counts per /48.
	for i := 0; i < IPLimit; i++ {
		c.Check(dir, ip("2001:db8::1"), "user"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@example.org", "x")
	}
	if _, err := c.Check(dir, ip("2001:db8::ffff"), "alice@example.org", "pw"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("ip limit: %v", err)
	}
	if _, err := c.Check(dir, ip("2001:db8:0:ffff::1"), "alice@example.org", "pw"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("other /64 in the same /48: %v", err)
	}
	if _, err := c.Check(dir, ip("2001:db8:1::1"), "alice@example.org", "pw"); err != nil {
		t.Fatalf("other /48: %v", err)
	}
}

// A full table refuses new IPs instead of letting them guess uncounted,
// and makes room again once the old windows have expired.
func TestThrottleFull(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	c := New(Options{Slots: slots, Now: clk.now})
	c.max = 3
	dir := dirWith(t, "pw")
	for i := 0; i < 3; i++ {
		c.Check(dir, netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}), "alice@example.org", "wrong")
	}
	n := c.hashes.Load()
	if _, err := c.Check(dir, ip("198.51.100.1"), "alice@example.org", "pw"); !errors.Is(err, ErrThrottled) || c.hashes.Load() != n {
		t.Fatalf("full table: %v", err)
	}
	// Known IPs still count as before.
	if _, err := c.Check(dir, ip("192.0.2.0"), "alice@example.org", "wrong"); !errors.Is(err, ErrFailed) {
		t.Fatalf("known IP: %v", err)
	}
	clk.add(Window)
	if _, err := c.Check(dir, ip("198.51.100.1"), "alice@example.org", "pw"); err != nil {
		t.Fatalf("after sweep: %v", err)
	}
}

func TestEvent(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	var events []config.Event
	c := New(Options{Slots: slots, Now: clk.now, Event: func(e config.Event) { events = append(events, e) }})
	dir := dirWith(t, "pw")
	attempt := func(n int) {
		for i := 0; i < n; i++ {
			c.Check(dir, netip.AddrFrom4([4]byte{203, 0, 113, byte(i % 10)}), "alice@example.org", "wrong")
		}
	}
	attempt(EventFailures - 1)
	if len(events) != 0 {
		t.Fatalf("early event %+v", events)
	}
	attempt(5)
	if len(events) != 1 || events[0].Kind != config.EventSignInFailures || events[0].StorageID != mailtest.ID('a') {
		t.Fatalf("events %+v", events)
	}
	clk.add(10 * time.Minute)
	attempt(30)
	if len(events) != 1 {
		t.Fatalf("second event within the hour: %+v", events)
	}
}

// A success refunds only its own charge: wrong guesses mixed with correct
// sign-ins still reach IPLimit.
func TestSuccessDoesNotResetIPLimit(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	c := New(Options{Slots: slots, Now: clk.now})
	dir := dirWith(t, "pw")
	a := ip("192.0.2.1")
	long := strings.Repeat("x", MaxPasswordBytes+1) // fails without hashing
	for i := 0; i < IPLimit; i++ {
		if _, err := c.Check(dir, a, fmt.Sprintf("victim%d@example.org", i), long); !errors.Is(err, ErrFailed) {
			t.Fatalf("guess %d: %v", i, err)
		}
		if i < IPLimit-1 {
			if _, err := c.Check(dir, a, "alice@example.org", "pw"); err != nil {
				t.Fatalf("sign-in %d: %v", i, err)
			}
		}
	}
	if _, err := c.Check(dir, a, "victim@example.org", long); !errors.Is(err, ErrThrottled) {
		t.Fatalf("mixed: %v", err)
	}
}

// A success whose hash waited until its window had expired does not refund
// a failure counted in the next window.
func TestSlowSuccessRefundsOwnWindowOnly(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	s := passwd.NewSlots(1)
	h, err := s.Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	dir := directory.New(&config.Config{
		Hostname:  "mail.example.net",
		Domains:   []config.Domain{{Name: "example.org"}},
		Mailboxes: []config.Mailbox{{StorageID: mailtest.ID('a'), Address: "alice@example.org", PasswordHash: h}},
	})
	c := New(Options{Slots: s, Now: clk.now})
	a := ip("192.0.2.1")
	k := ipKey(a)
	s <- struct{}{} // hold the only hash slot: the sign-in waits after its charge
	done := make(chan error)
	go func() {
		_, err := c.Check(dir, a, "alice@example.org", "pw")
		done <- err
	}()
	for {
		c.mu.Lock()
		charged := c.ips[k] != nil
		c.mu.Unlock()
		if charged {
			break
		}
		time.Sleep(time.Millisecond)
	}
	clk.add(Window)
	long := strings.Repeat("x", MaxPasswordBytes+1)
	for i := 0; i < 3; i++ {
		c.Check(dir, a, "victim@example.org", long)
	}
	<-s
	if err := <-done; err != nil {
		t.Fatalf("sign-in: %v", err)
	}
	c.mu.Lock()
	n := c.ips[k].n
	c.mu.Unlock()
	if n != 3 {
		t.Fatalf("failures in the new window: %d, want 3", n)
	}
}
