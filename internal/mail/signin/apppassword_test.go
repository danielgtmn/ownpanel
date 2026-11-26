package signin

import (
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
)

// appWorld is a mailbox with the main password "pw" and one app password.
type appWorld struct {
	key, hash, token, id string
	cfg                  *config.Config
}

func newAppWorld(t *testing.T) *appWorld {
	t.Helper()
	h, err := slots.Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	w := &appWorld{key: config.NewAppPasswordKey(), hash: h}
	id, secret, token := config.NewAppPassword()
	w.id, w.token = id, token
	w.cfg = &config.Config{
		Hostname:       "mail.example.net",
		AppPasswordKey: w.key,
		Domains:        []config.Domain{{Name: "example.org"}},
		Mailboxes: []config.Mailbox{{StorageID: mailtest.ID('a'), Address: "alice@example.org", PasswordHash: h,
			AppPasswords: []config.AppPassword{{ID: id, Name: "Phone", MAC: config.AppPasswordMAC(w.key, mailtest.ID('a'), id, secret)}}}},
	}
	return w
}

// dir builds a directory from a copy of the configuration changed by mod.
func (w *appWorld) dir(mod func(m *config.Mailbox)) *directory.Directory {
	cp := *w.cfg
	cp.Mailboxes = []config.Mailbox{cp.Mailboxes[0]}
	m := &cp.Mailboxes[0]
	m.AppPasswords = append([]config.AppPassword(nil), m.AppPasswords...)
	if mod != nil {
		mod(m)
	}
	return directory.New(&cp)
}

func TestAppPasswordSignIn(t *testing.T) {
	t.Parallel()
	w := newAppWorld(t)
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	var mu sync.Mutex
	var events []config.Event
	c := New(Options{Slots: slots, Now: clk.now, Event: func(e config.Event) { mu.Lock(); events = append(events, e); mu.Unlock() }})
	dir := w.dir(nil)

	// An app password signs in with one HMAC, never argon2id.
	g, err := c.Check(dir, ip("192.0.2.10"), "Alice@example.org", w.token)
	if err != nil || g.AppPassword != w.id || g.StorageID != mailtest.ID('a') || c.hashes.Load() != 0 {
		t.Fatalf("app password: %+v %v, %d hashes", g, err, c.hashes.Load())
	}
	if !Valid(dir, g) {
		t.Fatal("not valid")
	}
	// Its use is reported once per hour, with the network only.
	mu.Lock()
	if len(events) != 1 || events[0].Kind != config.EventAppPasswordUsed || events[0].Account == nil ||
		events[0].Account.AppPasswordID != w.id || events[0].Account.Network != "192.0.2.0/24" ||
		strings.Contains(events[0].Detail, "192.0.2.10") || strings.Contains(events[0].Detail, w.token) {
		t.Fatalf("events %+v", events)
	}
	mu.Unlock()
	c.Check(dir, ip("2001:db8:5:6::7"), "alice@example.org", w.token)
	clk.add(config.AppPasswordUseEvery)
	c.Check(dir, ip("2001:db8:5:6::7"), "alice@example.org", w.token)
	mu.Lock()
	if len(events) != 2 || events[1].Account.Network != "2001:db8:5::/48" {
		t.Fatalf("events %+v", events)
	}
	mu.Unlock()

	// Wrong secret, unknown id, another mailbox's token, anything of the
	// form: refused without argon2id, and never tried as the main password.
	_, _, other := config.NewAppPassword()
	wrongSecret := w.token[:len(w.token)-1] + "a"
	if wrongSecret == w.token {
		wrongSecret = w.token[:len(w.token)-1] + "b"
	}
	for _, pw := range []string{wrongSecret, other} {
		if _, err := c.Check(dir, ip("192.0.2.11"), "alice@example.org", pw); !errors.Is(err, ErrFailed) {
			t.Fatalf("%q: %v", pw, err)
		}
	}
	if _, err := c.Check(dir, ip("192.0.2.11"), "nobody@example.org", w.token); !errors.Is(err, ErrFailed) {
		t.Fatal("unknown address")
	}
	if c.hashes.Load() != 0 {
		t.Fatalf("%d argon2id runs for app passwords", c.hashes.Load())
	}
	// The main password still works, and its sign-in survives a change of
	// app passwords; an app password sign-in survives a password change.
	main, err := c.Check(dir, ip("192.0.2.12"), "alice@example.org", "pw")
	if err != nil || main.AppPassword != "" {
		t.Fatal(err)
	}
	newHash, _ := slots.Hash("pw2")
	if !Valid(w.dir(func(m *config.Mailbox) { m.PasswordHash = newHash }), g) || Valid(w.dir(func(m *config.Mailbox) { m.PasswordHash = newHash }), main) {
		t.Fatal("a password change must end main-password sign-ins only")
	}

	// Revoked: open sessions notice it (Valid), new sign-ins fail.
	revoked := w.dir(func(m *config.Mailbox) { m.AppPasswords = nil })
	if Valid(revoked, g) {
		t.Fatal("revoked app password still valid")
	}
	if _, err := c.Check(revoked, ip("192.0.2.10"), "alice@example.org", w.token); !errors.Is(err, ErrFailed) {
		t.Fatal("revoked app password signs in")
	}
	if !Valid(revoked, main) {
		t.Fatal("revoking an app password ended the main-password sign-in")
	}
	// A new MAC for the same id (key change) ends the session too.
	rekeyed := w.dir(func(m *config.Mailbox) { m.AppPasswords[0].MAC = strings.Repeat("0", 64) })
	if Valid(rekeyed, g) {
		t.Fatal("changed MAC still valid")
	}
}

func TestAppPasswordsOnly(t *testing.T) {
	t.Parallel()
	w := newAppWorld(t)
	c := New(Options{Slots: slots})
	dir := w.dir(nil)
	// Signed in with the main password (and cached) before the switch.
	main, err := c.Check(dir, ip("192.0.2.1"), "alice@example.org", "pw")
	if err != nil {
		t.Fatal(err)
	}
	only := w.dir(func(m *config.Mailbox) { m.AppPasswordsOnly = true })
	if Valid(only, main) {
		t.Fatal("main-password session survives app passwords only")
	}
	// The cached sign-in does not get around it.
	if _, err := c.Check(only, ip("192.0.2.1"), "alice@example.org", "pw"); !errors.Is(err, ErrFailed) {
		t.Fatalf("main password with app passwords only: %v", err)
	}
	g, err := c.Check(only, ip("192.0.2.1"), "alice@example.org", w.token)
	if err != nil || !Valid(only, g) {
		t.Fatalf("app password with app passwords only: %v", err)
	}
}

func TestAppPasswordFailuresAreThrottled(t *testing.T) {
	t.Parallel()
	w := newAppWorld(t)
	c := New(Options{Slots: slots})
	dir := w.dir(nil)
	_, _, other := config.NewAppPassword()
	for range PairLimit {
		c.Check(dir, ip("192.0.2.1"), "alice@example.org", other)
	}
	if _, err := c.Check(dir, ip("192.0.2.1"), "alice@example.org", w.token); !errors.Is(err, ErrThrottled) {
		t.Fatalf("not throttled: %v", err)
	}
}

func TestCacheKeyIncludesCredential(t *testing.T) {
	c := New(Options{Slots: slots})
	if c.cacheKey("a@example.org", "", "pw", "h") == c.cacheKey("a@example.org", "x", "pw", "h") {
		t.Fatal("credential not in the cache key")
	}
}

// TestCheckMain: the self-service sign-in takes the main password only,
// also with AppPasswordsOnly, and throttles per address and network.
func TestCheckMain(t *testing.T) {
	t.Parallel()
	w := newAppWorld(t)
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	c := New(Options{Slots: slots, Now: clk.now})
	only := w.dir(func(m *config.Mailbox) { m.AppPasswordsOnly = true })
	ip := netip.MustParseAddr("192.0.2.1")

	if g, err := c.CheckMain(only, ip, "Alice@Example.org", "pw"); err != nil || g.StorageID != mailtest.ID('a') || g.AppPassword != "" {
		t.Fatalf("main password with app passwords only: %+v %v", g, err)
	}
	if _, err := c.CheckMain(only, ip, "alice@example.org", w.token); !errors.Is(err, ErrFailed) {
		t.Fatalf("a valid app password signed in: %v", err)
	}
	// Mail programs still may not use the main password.
	if _, err := New(Options{Slots: slots}).Check(only, ip, "alice@example.org", "pw"); !errors.Is(err, ErrFailed) {
		t.Fatalf("Check took the main password: %v", err)
	}

	// Per (network, address): PairLimit failures, then refused even with
	// the right password; another address from the same network still
	// gets through.
	v6 := netip.MustParseAddr("2001:db8:1:2::1")
	for range PairLimit - 1 { // one failure already counted above for ip, not v6
		c.CheckMain(only, v6, "alice@example.org", "wrong")
	}
	if _, err := c.CheckMain(only, v6, "alice@example.org", "wrong"); !errors.Is(err, ErrFailed) {
		t.Fatalf("last failure: %v", err)
	}
	// The same /48, another /64: still throttled.
	if _, err := c.CheckMain(only, netip.MustParseAddr("2001:db8:1:ffff::9"), "alice@example.org", "pw"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("same /48: %v", err)
	}
	if _, err := c.CheckMain(only, netip.MustParseAddr("2001:db8:2::1"), "alice@example.org", "pw"); err != nil {
		t.Fatalf("other /48: %v", err)
	}
	clk.add(Window)
	if _, err := c.CheckMain(only, v6, "alice@example.org", "pw"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}
