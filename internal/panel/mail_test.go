package panel

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/store"
	"github.com/danielgtmn/panzerschrank"
)

func init() {
	// One DKIM key for every test: making RSA keys is slow.
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	newRSAKey = func() (*rsa.PrivateKey, error) { return k, nil }
}

// testHash is a real argon2id hash, made once (the configuration is
// validated, which checks the hash's form).
var testHash = sync.OnceValue(func() string {
	h, err := hashPassword("mailbox-password-1")
	if err != nil {
		panic(err)
	}
	return h
})

// fakeMail is the mail service's admin socket.
type fakeMail struct {
	mu        sync.Mutex
	hash      string
	loads     []*config.Config
	statusErr error
	loadErr   error
	deleteErr error
	deleted   []string
	unblocked []string
	events    []config.Event
	usage     []config.MailboxStatus
	web       *config.Listener
	sent      []config.OutgoingMail
	sendErr   error
}

func (f *fakeMail) Send(_ context.Context, m config.OutgoingMail) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeMail) Load(_ context.Context, cfg *config.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return f.loadErr
	}
	if err := cfg.Validate(); err != nil {
		return &config.Error{Status: 400, Msg: err.Error()}
	}
	f.loads = append(f.loads, cfg)
	f.hash = cfg.Hash()
	return nil
}

func (f *fakeMail) Status(context.Context) (config.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return config.Status{}, f.statusErr
	}
	return config.Status{ConfigHash: f.hash, Mailboxes: f.usage, Listeners: []config.Listener{{Name: "smtp", Address: ":25", Listening: true}}, Web: f.web}, nil
}

func (f *fakeMail) Events(_ context.Context, after int64) ([]config.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	var out []config.Event
	for _, e := range f.events {
		if e.ID > after {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeMail) Unblock(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return f.statusErr
	}
	f.unblocked = append(f.unblocked, id)
	return nil
}

func (f *fakeMail) DeleteMailbox(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	// Like the service: a mailbox still in the loaded configuration keeps
	// its data, so the panel must push the configuration first.
	if n := len(f.loads); n > 0 && slices.ContainsFunc(f.loads[n-1].Mailboxes, func(m config.Mailbox) bool { return m.StorageID == id }) {
		return &config.Error{Status: 409, Msg: "the mailbox is still configured"}
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeMail) CheckOutbound(context.Context) (config.OutboundCheck, error) {
	return config.OutboundCheck{OK: true, Target: "example.net:25"}, nil
}

func (f *fakeMail) set(fn func(f *fakeMail)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeMail) loadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.loads)
}

func (f *fakeMail) last() *config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.loads) == 0 {
		return nil
	}
	return f.loads[len(f.loads)-1]
}

// fakeMX answers MX lookups from a map; other names have no MX record.
type fakeMX struct {
	mu    sync.Mutex
	hosts map[string][]string
	errs  map[string]error
	calls int
}

func (f *fakeMX) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err := f.errs[name]; err != nil {
		return nil, err
	}
	hosts, ok := f.hosts[name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	var out []*net.MX
	for i, h := range hosts {
		out = append(out, &net.MX{Host: h + ".", Pref: uint16(10 * (i + 1))})
	}
	return out, nil
}

func (f *fakeMX) point(domain string, hosts ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts[domain] = hosts
}

func (f *fakeMX) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

const testMailHost = "mail.host.test"

func strp(s string) *string { return &s }

type mailWorld struct {
	world
	mail     *fakeMail
	mx       *fakeMX
	now      time.Time
	bobBox   int64 // info@bobsite.example.com
	rootBox  int64 // root@rootsite.example.com
	bobAlias int64 // sales@bobsite.example.com
}

// newMailWorld: the permission world with mail. bob has the active mail
// domain bobsite.example.com with a mailbox and an alias; root has
// rootsite.example.com with a mailbox.
func newMailWorld(t *testing.T) *mailWorld {
	t.Helper()
	w := &mailWorld{world: newWorld(t), mail: &fakeMail{}, mx: &fakeMX{hosts: map[string][]string{}, errs: map[string]error{}},
		now: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	svc := w.svc
	key := make([]byte, panzerschrank.KeySize)
	rand.Read(key)
	box, err := panzerschrank.New(key)
	if err != nil {
		t.Fatal(err)
	}
	svc.secrets, svc.mail, svc.mx = box, w.mail, w.mx
	svc.now = func() time.Time { return w.now }
	svc.store.Now = func() time.Time { return w.now }
	ctx := context.Background()
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailHost: strp(testMailHost), PostmasterAddress: strp("Hostmaster@Example.org")}); err != nil {
		t.Fatal(err)
	}
	w.mx.point("bobsite.example.com", testMailHost)
	w.mx.point("rootsite.example.com", testMailHost)
	for _, c := range []struct {
		a Actor
		d string
	}{{bob, "bobsite.example.com"}, {admin, "rootsite.example.com"}} {
		if d, err := svc.EnableMailDomain(ctx, c.a, c.d); err != nil || !d.Active {
			t.Fatalf("enable %s: %+v %v", c.d, d, err)
		}
	}
	add := func(domain, local string) int64 {
		m, err := svc.store.CreateMailbox(ctx, store.Mailbox{StorageID: newStorageID(), Domain: domain, LocalPart: local,
			PasswordHash: testHash(), QuotaBytes: DefaultQuotaBytes, SendPerHour: 100, SendPerDay: 500}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	w.bobBox, w.rootBox = add("bobsite.example.com", "info"), add("rootsite.example.com", "root")
	a, err := svc.CreateMailAlias(ctx, bob, "bobsite.example.com", "sales", []string{"info@bobsite.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	w.bobAlias = a.ID
	return w
}

func TestMailPermissionMatrix(t *testing.T) {
	ctx := context.Background()
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", admin}, {"admin write token", rootWriteTok}, {"member", bob}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody}, {"system", System},
	}
	F, N, U := Forbidden, NotFound, Unauthenticated
	read := []Kind{ok, ok, ok, ok, F, ok, F, U, ok}
	write := []Kind{ok, ok, ok, F, F, F, F, U, F}
	n := func(i int) *int { return &i }
	ops := []struct {
		name string
		run  func(w *mailWorld, a Actor) error
		want []Kind
	}{
		{"list domains", func(w *mailWorld, a Actor) error { _, err := w.svc.MailDomains(ctx, a); return err }, read},
		{"get member's domain", func(w *mailWorld, a Actor) error {
			_, err := w.svc.MailDomainOf(ctx, a, "bobsite.example.com")
			return err
		}, read},
		{"get admin's domain", func(w *mailWorld, a Actor) error {
			_, err := w.svc.MailDomainOf(ctx, a, "rootsite.example.com")
			return err
		}, []Kind{ok, ok, N, N, F, ok, F, U, ok}},
		{"check member's domain", func(w *mailWorld, a Actor) error {
			_, err := w.svc.CheckMailDomain(ctx, a, "bobsite.example.com")
			return err
		}, read},
		{"enable member's other site domain", func(w *mailWorld, a Actor) error {
			_, err := w.svc.EnableMailDomain(ctx, a, "bobother.example.com")
			return err
		}, write},
		{"delete member's domain", func(w *mailWorld, a Actor) error {
			return w.svc.DeleteMailDomain(ctx, a, "bobsite.example.com", "bobsite.example.com")
		}, write},
		{"delete admin's domain", func(w *mailWorld, a Actor) error {
			return w.svc.DeleteMailDomain(ctx, a, "rootsite.example.com", "rootsite.example.com")
		}, []Kind{ok, ok, N, N, F, F, F, U, F}},
		{"list mailboxes", func(w *mailWorld, a Actor) error { _, err := w.svc.Mailboxes(ctx, a, ""); return err }, read},
		{"get admin's mailbox", func(w *mailWorld, a Actor) error { _, err := w.svc.MailboxOf(ctx, a, w.rootBox); return err },
			[]Kind{ok, ok, N, N, F, ok, F, U, ok}},
		{"create mailbox", func(w *mailWorld, a Actor) error {
			_, err := w.svc.CreateMailbox(ctx, a, "bobsite.example.com", MailboxInput{LocalPart: "new", Password: "short"})
			if KindOf(err) == Invalid { // permitted; the password is too short
				return nil
			}
			return err
		}, write},
		{"set mailbox password", func(w *mailWorld, a Actor) error {
			err := w.svc.SetMailboxPassword(ctx, a, w.bobBox, "x")
			if KindOf(err) == Invalid { // permitted; the password is too short
				return nil
			}
			return err
		}, write},
		{"lower limits", func(w *mailWorld, a Actor) error {
			_, err := w.svc.SetMailboxLimits(ctx, a, w.bobBox, MailboxLimits{SendPerHour: n(10)})
			return err
		}, write},
		{"raise limits", func(w *mailWorld, a Actor) error {
			_, err := w.svc.SetMailboxLimits(ctx, a, w.bobBox, MailboxLimits{SendPerHour: n(1000)})
			return err
		}, []Kind{ok, ok, F, F, F, F, F, U, F}},
		{"unblock", func(w *mailWorld, a Actor) error { return w.svc.UnblockMailbox(ctx, a, w.bobBox) },
			[]Kind{ok, ok, F, F, F, F, F, U, ok}},
		{"delete mailbox", func(w *mailWorld, a Actor) error { return w.svc.DeleteMailbox(ctx, a, w.bobBox) }, write},
		{"list aliases", func(w *mailWorld, a Actor) error { _, err := w.svc.MailAliases(ctx, a, ""); return err }, read},
		{"create alias", func(w *mailWorld, a Actor) error {
			_, err := w.svc.CreateMailAlias(ctx, a, "bobsite.example.com", "team", []string{"a@example.org"})
			return err
		}, write},
		{"update alias", func(w *mailWorld, a Actor) error {
			_, err := w.svc.UpdateMailAlias(ctx, a, w.bobAlias, "sales", []string{"b@example.org"})
			return err
		}, write},
		{"delete alias", func(w *mailWorld, a Actor) error { return w.svc.DeleteMailAlias(ctx, a, w.bobAlias) }, write},
		{"status", func(w *mailWorld, a Actor) error { _, err := w.svc.MailStatus(ctx, a); return err }, read},
		{"outbound check", func(w *mailWorld, a Actor) error { _, err := w.svc.CheckMailOutbound(ctx, a); return err },
			[]Kind{ok, ok, F, F, F, ok, F, U, ok}},
	}
	for _, op := range ops {
		for i, ac := range actors {
			t.Run(op.name+"/"+ac.name, func(t *testing.T) {
				w := newMailWorld(t)
				err := op.run(w, ac.a)
				if want := op.want[i]; want == ok {
					if err != nil {
						t.Fatalf("want success, got %v: %v", KindOf(err), err)
					}
				} else {
					wantKind(t, err, want)
				}
			})
		}
	}
}

func TestMailListsPerActor(t *testing.T) {
	w := newMailWorld(t)
	ctx := context.Background()
	names := func(a Actor) []string {
		list, err := w.svc.MailDomains(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range list {
			out = append(out, d.Domain)
		}
		return out
	}
	if got := names(admin); !slices.Equal(got, []string{"bobsite.example.com", "rootsite.example.com"}) {
		t.Fatalf("admin sees %v", got)
	}
	if got := names(bobRead); !slices.Equal(got, []string{"bobsite.example.com"}) {
		t.Fatalf("member sees %v", got)
	}
	boxes, _ := w.svc.Mailboxes(ctx, bob, "")
	if len(boxes) != 1 || boxes[0].Address != "info@bobsite.example.com" {
		t.Fatalf("member's mailboxes %+v", boxes)
	}
	if _, err := w.svc.Mailboxes(ctx, bob, "rootsite.example.com"); KindOf(err) != NotFound {
		t.Fatalf("other's domain: %v", err)
	}
	aliases, _ := w.svc.MailAliases(ctx, admin, "")
	if len(aliases) != 1 || aliases[0].Address != "sales@bobsite.example.com" || aliases[0].Owner != "bob" {
		t.Fatalf("aliases %+v", aliases)
	}
}

func TestEnableMailDomainRules(t *testing.T) {
	d := newDNSWorld(t)
	svc := d.svc
	mx := &fakeMX{hosts: map[string][]string{}, errs: map[string]error{}}
	svc.mx = mx
	ctx := context.Background()
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailHost: strp(testMailHost)}); err != nil {
		t.Fatal(err)
	}

	// A domain in a zone of the member's own DNS connection.
	md, err := svc.EnableMailDomain(ctx, bob, "Shop.Other.org.")
	if err != nil || md.Domain != "shop.other.org" || md.Owner != "bob" || md.Active {
		t.Fatalf("zone domain: %+v %v", md, err)
	}
	if !strings.Contains(md.Problem, "no MX record") || !strings.Contains(md.Problem, testMailHost) {
		t.Fatalf("problem %q", md.Problem)
	}
	if md.DKIMSelector != "op202611" || !strings.HasPrefix(md.DKIMRecord, "v=DKIM1; k=rsa; p=MII") {
		t.Fatalf("dkim %q %q", md.DKIMSelector, md.DKIMRecord)
	}
	// Twice: refused.
	_, err = svc.EnableMailDomain(ctx, admin, "shop.other.org")
	wantKind(t, err, Conflict)
	// Another person's site: refused without naming it.
	_, err = svc.EnableMailDomain(ctx, bob, "rootsite.example.com")
	wantKind(t, err, Invalid)
	if strings.Contains(err.Error(), "rootsite\"") || strings.Contains(err.Error(), "root ") || !strings.Contains(err.Error(), "another person") {
		t.Fatalf("message %q", err)
	}
	// A domain nobody controls.
	_, err = svc.EnableMailDomain(ctx, bob, "gmail.com")
	wantKind(t, err, Forbidden)
	// The mail host itself, IP addresses and garbage.
	for _, bad := range []string{testMailHost, "192.0.2.1", "not a domain", "localhost"} {
		_, err = svc.EnableMailDomain(ctx, admin, bad)
		wantKind(t, err, Invalid)
	}
	// Admins may use any domain, even another person's.
	if _, err := svc.EnableMailDomain(ctx, admin, "gmail.com"); err != nil {
		t.Fatal(err)
	}
	// Per-person cap.
	for i := 1; i < MaxMailDomainsPerUser; i++ {
		if _, err := svc.store.CreateMailDomain(ctx, "x"+string(rune('a'+i))+".other.org", 2, store.DKIMKey{Selector: "s", PrivateKey: []byte("k"), PublicKey: "p", State: store.DKIMActive}, 0); err != nil {
			t.Fatal(err)
		}
	}
	_, err = svc.EnableMailDomain(ctx, bob, "full.other.org")
	wantKind(t, err, Conflict)
	// The user cannot be deleted while owning mail domains.
	wantKind(t, svc.DeleteUser(ctx, admin, 2), Conflict)

	// The private key is stored sealed.
	keys, _ := svc.store.DKIMKeys(ctx, "shop.other.org")
	if len(keys) != 1 || strings.Contains(string(keys[0].PrivateKey), "PRIVATE KEY") {
		t.Fatalf("key stored in the clear: %d", len(keys))
	}
}

func TestMailActivation(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()
	md, err := svc.EnableMailDomain(ctx, bob, "bobother.example.com")
	if err != nil || md.Active {
		t.Fatalf("%+v %v", md, err)
	}
	loads := w.mail.loadCount()

	// MX elsewhere, then a null MX.
	w.mx.point("bobother.example.com", "mx.elsewhere.example")
	w.now = w.now.Add(time.Minute)
	md, _ = svc.CheckMailDomain(ctx, bob, "bobother.example.com")
	if md.Active || !strings.Contains(md.Problem, "mx.elsewhere.example, not to "+testMailHost) {
		t.Fatalf("elsewhere: %+v", md)
	}
	// Asking again at once does not look up again.
	calls := w.mx.count()
	svc.CheckMailDomain(ctx, bob, "bobother.example.com")
	if w.mx.count() != calls {
		t.Fatal("checks on request must be rate-limited")
	}
	w.mx.point("bobother.example.com", "")
	w.now = w.now.Add(time.Minute)
	md, _ = svc.CheckMailDomain(ctx, bob, "bobother.example.com")
	if md.Active || !strings.Contains(md.Problem, "null MX") {
		t.Fatalf("null MX: %+v", md)
	}
	w.mx.set(func(f *fakeMX) { f.errs["bobother.example.com"] = errors.New("timeout") })
	w.now = w.now.Add(time.Minute)
	md, _ = svc.CheckMailDomain(ctx, bob, "bobother.example.com")
	if md.Active || !strings.Contains(md.Problem, "could not be looked up") {
		t.Fatalf("lookup failure: %+v", md)
	}
	if w.mail.loadCount() != loads {
		t.Fatal("an inactive domain must not be pushed")
	}

	// The background loop looks at most every 5 minutes.
	w.mx.set(func(f *fakeMX) { delete(f.errs, "bobother.example.com") })
	w.mx.point("bobother.example.com", "backup.example.net", testMailHost)
	calls = w.mx.count()
	if svc.activateMailDomains(ctx) || w.mx.count() != calls {
		t.Fatal("checked again too soon")
	}
	w.now = w.now.Add(mailCheckEvery)
	if !svc.activateMailDomains(ctx) {
		t.Fatal("not activated")
	}
	md, _ = svc.MailDomainOf(ctx, bob, "bobother.example.com")
	if !md.Active || md.Problem != "" || md.ActivatedAt == nil {
		t.Fatalf("activated %+v", md)
	}
	// Active domains are not looked at any more.
	calls = w.mx.count()
	w.now = w.now.Add(time.Hour)
	svc.activateMailDomains(ctx)
	if w.mx.count() != calls {
		t.Fatal("active domains must not be checked")
	}
	acts, _ := svc.store.Activities(ctx, 50)
	if !slices.ContainsFunc(acts, func(a store.Activity) bool {
		return a.Action == "mail.domain.activate" && a.Target == "bobother.example.com" && a.Actor == "system"
	}) {
		t.Fatalf("no activation entry: %+v", acts)
	}
}

func (f *fakeMX) set(fn func(f *fakeMX)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func TestMailConfigOnlyActiveDomainsAndNoSecretsInViews(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()
	// An inactive domain with a mailbox and an alias.
	if _, err := svc.EnableMailDomain(ctx, bob, "bobother.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateMailbox(ctx, bob, "bobother.example.com", MailboxInput{LocalPart: "Hidden", Password: "mailbox-password-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateMailAlias(ctx, bob, "bobother.example.com", "*", []string{"Hidden@bobother.example.com"}); err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "zeta", Password: "mailbox-password-2"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Address != "zeta@bobsite.example.com" || second.QuotaBytes != DefaultQuotaBytes ||
		second.SendPerHour != config.DefaultSendPerHour || second.SendPerDay != config.DefaultSendPerDay {
		t.Fatalf("defaults %+v", second)
	}

	cfg, err := svc.buildMailConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != testMailHost || cfg.PostmasterAddress != "hostmaster@example.org" {
		t.Fatalf("host %q postmaster %q", cfg.Hostname, cfg.PostmasterAddress)
	}
	var domains, boxes, aliases []string
	for _, d := range cfg.Domains {
		domains = append(domains, d.Name)
		if len(d.DKIM) != 1 || !d.DKIM[0].Active || !strings.Contains(d.DKIM[0].PrivateKeyPEM, "BEGIN PRIVATE KEY") {
			t.Fatalf("dkim of %s: %+v", d.Name, d.DKIM)
		}
		if _, err := config.ParseDKIMKey(d.DKIM[0].PrivateKeyPEM); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range cfg.Mailboxes {
		boxes = append(boxes, m.Address)
	}
	for _, a := range cfg.Aliases {
		aliases = append(aliases, a.Address)
	}
	if !slices.Equal(domains, []string{"bobsite.example.com", "rootsite.example.com"}) ||
		!slices.Equal(boxes, []string{"info@bobsite.example.com", "root@rootsite.example.com", "zeta@bobsite.example.com"}) ||
		!slices.Equal(aliases, []string{"sales@bobsite.example.com"}) {
		t.Fatalf("config: %v %v %v", domains, boxes, aliases)
	}

	// Views never carry hashes or keys.
	md, _ := svc.MailDomains(ctx, admin)
	mb, _ := svc.Mailboxes(ctx, admin, "")
	al, _ := svc.MailAliases(ctx, admin, "")
	st, _ := svc.MailStatus(ctx, admin)
	b, _ := json.Marshal([]any{md, mb, al, st})
	for _, secret := range []string{"argon2", "PRIVATE KEY", cfg.Mailboxes[0].StorageID} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("a view contains %q", secret)
		}
	}
	acts, _ := svc.store.Activities(ctx, 100)
	ab, _ := json.Marshal(acts)
	if strings.Contains(string(ab), "mailbox-password") || strings.Contains(string(ab), "argon2") {
		t.Fatal("the activity log contains a secret")
	}
}

func TestMailSync(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()

	// In sync: nothing is pushed again.
	if err := svc.mailSync(ctx); err != nil {
		t.Fatal(err)
	}
	n := w.mail.loadCount()
	if n == 0 {
		t.Fatal("nothing was pushed")
	}
	if err := svc.mailSync(ctx); err != nil || w.mail.loadCount() != n {
		t.Fatalf("pushed although in sync: %v", err)
	}
	// A change is pushed once.
	if _, err := svc.SetMailboxLimits(ctx, bob, w.bobBox, MailboxLimits{SendPerDay: func(i int) *int { return &i }(50)}); err != nil {
		t.Fatal(err)
	}
	if w.mail.loadCount() != n+1 || w.mail.last().Mailboxes[0].SendPerDay != 50 {
		t.Fatalf("not pushed: %d", w.mail.loadCount())
	}

	// Mail service down: saved, NotSynced; the web server is not affected.
	web := w.svc.web.(*fakeWeb)
	webLoads := web.count()
	w.mail.set(func(f *fakeMail) { f.statusErr = errors.New("connection refused") })
	_, err := svc.CreateMailAlias(ctx, bob, "bobsite.example.com", "team", []string{"info@bobsite.example.com"})
	wantKind(t, err, NotSynced)
	if !strings.Contains(err.Error(), "saved, but the mail server could not be updated yet") {
		t.Fatalf("message %q", err)
	}
	if list, _ := svc.MailAliases(ctx, bob, "bobsite.example.com"); len(list) != 2 {
		t.Fatalf("alias not saved: %+v", list)
	}
	if web.count() != webLoads {
		t.Fatal("a mail change must not touch the web server")
	}
	if err := svc.Sync(ctx, System); err != nil {
		t.Fatalf("web sync must not depend on the mail service: %v", err)
	}
	// Deleting while it is down: the data is deleted once it is back.
	box, _ := svc.store.Mailbox(ctx, w.bobBox)
	wantKind(t, svc.DeleteMailbox(ctx, bob, w.bobBox), NotSynced)
	if p, _ := svc.store.PendingMailDeletes(ctx); !slices.Equal(p, []string{box.StorageID}) {
		t.Fatalf("pending %v", p)
	}
	w.mail.set(func(f *fakeMail) { f.statusErr = nil })
	if err := svc.mailSync(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := svc.store.PendingMailDeletes(ctx); len(p) != 0 || !slices.Contains(w.mail.deleted, box.StorageID) {
		t.Fatalf("pending %v deleted %v", p, w.mail.deleted)
	}
	for _, m := range w.mail.last().Mailboxes {
		if m.StorageID == box.StorageID {
			t.Fatal("deleted mailbox still pushed")
		}
	}

	// A failing web server does not stop mail changes.
	web.setFail(errors.New("caddy down"))
	if _, err := svc.CreateMailAlias(ctx, bob, "bobsite.example.com", "office", []string{"x@example.org"}); err != nil {
		t.Fatalf("mail change with the web server down: %v", err)
	}

	// Deleting a domain deletes its mailboxes' data.
	rootBox, _ := svc.store.Mailbox(ctx, w.rootBox)
	wantKind(t, svc.DeleteMailDomain(ctx, admin, "rootsite.example.com", "nope"), Invalid)
	if err := svc.DeleteMailDomain(ctx, admin, "RootSite.example.com", " rootsite.example.com "); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(w.mail.deleted, rootBox.StorageID) {
		t.Fatalf("data of the domain's mailbox not deleted: %v", w.mail.deleted)
	}
	for _, d := range w.mail.last().Domains {
		if d.Name == "rootsite.example.com" {
			t.Fatal("deleted domain still pushed")
		}
	}
}

func TestMailWithoutService(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	svc.mail = nil
	ctx := context.Background()
	if _, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "new", Password: "mailbox-password-1"}); err != nil {
		t.Fatalf("stored without a mail service: %v", err)
	}
	st, err := svc.MailStatus(ctx, bob)
	if err != nil || st.ServiceSetUp || !strings.Contains(st.Problem, "not set up") || st.MailHost != testMailHost || st.Client.IMAPPort != 993 {
		t.Fatalf("%+v %v", st, err)
	}
	wantKind(t, svc.UnblockMailbox(ctx, admin, w.bobBox), Conflict)
	if err := svc.mailSync(ctx); err != nil {
		t.Fatal(err)
	}
	boxes, _ := svc.Mailboxes(ctx, bob, "")
	if boxes[0].Usage != nil {
		t.Fatal("usage without a mail service")
	}
}

func TestMailEventsGoToTheActivityLog(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()
	w.mail.set(func(f *fakeMail) {
		f.events = []config.Event{
			{ID: 1, Kind: config.EventBlocked, Address: "info@bobsite.example.com", Detail: "hourly limit reached three times"},
			{ID: 2, Kind: config.EventListenerFailed, Detail: "port 25 is in use"},
		}
	})
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.recordMailEvents(ctx); err != nil { // nothing new
		t.Fatal(err)
	}
	w.mail.set(func(f *fakeMail) {
		f.events = append(f.events, config.Event{ID: 3, Kind: config.EventBounced, StorageID: "00", Detail: "no such user"})
	})
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	acts, _ := svc.store.Activities(ctx, 100)
	var got []string
	for _, a := range acts {
		if a.Actor == "mail" {
			got = append(got, a.Action+" "+a.Target)
		}
	}
	want := []string{"mail.delivery.bounced 00", "mail.listener.failed ", "mail.mailbox.blocked info@bobsite.example.com"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
	m, _ := svc.store.Settings(ctx)
	if m[keyMailEventsCursor] != "3" {
		t.Fatalf("cursor %q", m[keyMailEventsCursor])
	}
}

func TestMailboxLimitsAndValidation(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()
	i64 := func(v int64) *int64 { return &v }
	n := func(v int) *int { return &v }

	m, err := svc.SetMailboxLimits(ctx, bob, w.bobBox, MailboxLimits{QuotaBytes: i64(1 << 30), SendPerHour: n(10), SendPerDay: n(20)})
	if err != nil || m.QuotaBytes != 1<<30 || m.SendPerHour != 10 || m.SendPerDay != 20 {
		t.Fatalf("%+v %v", m, err)
	}
	_, err = svc.SetMailboxLimits(ctx, bob, w.bobBox, MailboxLimits{QuotaBytes: i64(2 << 30)})
	wantKind(t, err, Forbidden)
	_, err = svc.SetMailboxLimits(ctx, bob, w.bobBox, MailboxLimits{QuotaBytes: i64(0)})
	wantKind(t, err, Invalid)
	_, err = svc.SetMailboxLimits(ctx, bob, w.bobBox, MailboxLimits{SendPerHour: n(0)})
	wantKind(t, err, Invalid)
	if m, err = svc.SetMailboxLimits(ctx, admin, w.bobBox, MailboxLimits{QuotaBytes: i64(0), SendPerDay: n(5000)}); err != nil || m.QuotaBytes != 0 {
		t.Fatalf("admin: %+v %v", m, err)
	}
	_, err = svc.SetMailboxLimits(ctx, admin, w.bobBox, MailboxLimits{SendPerDay: n(maxSendPerDay + 1)})
	wantKind(t, err, Invalid)
	// Created with lower limits; higher ones only by admins.
	_, err = svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "big", Password: "mailbox-password-1", QuotaBytes: i64(10 << 30)})
	wantKind(t, err, Forbidden)

	for _, bad := range []string{"", "a b", "*", "x@y", "ä", strings.Repeat("a", 65), ".a", "a..b"} {
		_, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: bad, Password: "mailbox-password-1"})
		wantKind(t, err, Invalid)
	}
	// A local part is a mailbox or an alias, not both.
	_, err = svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "Sales", Password: "mailbox-password-1"})
	wantKind(t, err, Conflict)
	_, err = svc.CreateMailAlias(ctx, bob, "bobsite.example.com", "INFO", []string{"x@example.org"})
	wantKind(t, err, Conflict)
	// Alias targets.
	for _, bad := range [][]string{nil, {""}, {"not an address"}, {"a@b"}} {
		_, err := svc.CreateMailAlias(ctx, bob, "bobsite.example.com", "list", bad)
		wantKind(t, err, Invalid)
	}
	many := make([]string, config.MaxTargets+1)
	for i := range many {
		many[i] = "u" + strings.Repeat("x", i) + "@example.org"
	}
	_, err = svc.CreateMailAlias(ctx, bob, "bobsite.example.com", "list", many)
	wantKind(t, err, Invalid)
	a, err := svc.CreateMailAlias(ctx, bob, "bobsite.example.com", "*", []string{" A@Example.org ", "a@example.org"})
	if err != nil || !a.CatchAll || !slices.Equal(a.Targets, []string{"a@example.org"}) {
		t.Fatalf("catch-all %+v %v", a, err)
	}

	// Password: validated, hashed, pushed; never logged.
	wantKind(t, svc.SetMailboxPassword(ctx, bob, w.bobBox, "short"), Invalid)
	before := w.mail.last().Mailboxes[0].PasswordHash
	if err := svc.SetMailboxPassword(ctx, bob, w.bobBox, "a-new-mailbox-password"); err != nil {
		t.Fatal(err)
	}
	after := w.mail.last().Mailboxes[0].PasswordHash
	if after == before || !mailboxHashSlots.Verify(after, "a-new-mailbox-password") {
		t.Fatal("new password not pushed")
	}

	// Unblock reaches the service with the storage id.
	box, _ := svc.store.Mailbox(ctx, w.bobBox)
	if err := svc.UnblockMailbox(ctx, admin, w.bobBox); err != nil || !slices.Equal(w.mail.unblocked, []string{box.StorageID}) {
		t.Fatalf("unblock %v %v", w.mail.unblocked, err)
	}
	// Usage and block state come from the service.
	w.mail.set(func(f *fakeMail) {
		f.usage = []config.MailboxStatus{{StorageID: box.StorageID, UsedBytes: 42, Messages: 3, Blocked: true, BlockedReason: "too many sign-ins"}}
	})
	got, _ := svc.MailboxOf(ctx, bob, w.bobBox)
	if got.Usage == nil || got.Usage.UsedBytes != 42 || !got.Usage.Blocked || got.Usage.BlockedReason != "too many sign-ins" {
		t.Fatalf("usage %+v", got.Usage)
	}
}

func TestMailHostSetting(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()

	st, _ := svc.Settings(ctx, admin)
	if st.MailHost != testMailHost || st.MailHostLocked || st.PostmasterAddress != "hostmaster@example.org" {
		t.Fatalf("%+v", st)
	}
	// Sites and redirects cannot use it.
	_, err := svc.SaveSite(ctx, bob, "bobsite", []string{"bobsite.example.com", testMailHost})
	wantKind(t, err, Invalid)
	_, err = svc.CreateRedirect(ctx, bob, RedirectInput{SourceDomain: testMailHost, Target: "https://example.com", Code: 301})
	wantKind(t, err, Invalid)
	// It cannot be a site, redirect or mail domain; it may be the panel domain.
	for _, bad := range []string{"bobsite.example.com", "rootsite.example.com", "192.0.2.1", "nodot"} {
		_, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailHost: strp(bad)})
		if err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	svc.opts.PanelUpstream = "127.0.0.1:8600"
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{PanelDomain: strp("panel.example.org")}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailHost: strp("Panel.Example.org")}); err != nil {
		t.Fatalf("the panel domain as mail host: %v", err)
	}
	if w.mail.last().Hostname != "panel.example.org" {
		t.Fatalf("pushed host %q", w.mail.last().Hostname)
	}
	_, err = svc.UpdateSettings(ctx, admin, SettingsUpdate{PostmasterAddress: strp("not an address")})
	wantKind(t, err, Invalid)
	// Members cannot change it.
	_, err = svc.UpdateSettings(ctx, bob, SettingsUpdate{MailHost: strp("mail.example.org")})
	wantKind(t, err, Forbidden)

	// -mail-host locks the setting and wins over it.
	svc.mailHostFlag = "mx.example.net"
	_, err = svc.UpdateSettings(ctx, admin, SettingsUpdate{MailHost: strp("other.example.net")})
	wantKind(t, err, Conflict)
	if st, _ := svc.Settings(ctx, admin); st.MailHost != "mx.example.net" || !st.MailHostLocked {
		t.Fatalf("%+v", st)
	}
	if ms, _ := svc.MailStatus(ctx, bobRead); ms.MailHost != "mx.example.net" || ms.Client.Server != "mx.example.net" {
		t.Fatalf("client settings %+v", ms)
	}

	// A mail host flag a site already uses stops the daemon.
	_, err = New(Config{Store: svc.store, Deploys: svc.deploys, Web: &fakeWeb{}, Caddy: svc.opts, MailHost: "bobsite.example.com"})
	if err == nil {
		t.Fatal("mail host used by a site must be refused")
	}
}

func TestMailCertificates(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()
	dir := t.TempDir()
	svc.certs = newMailCerts(dir, caddy.TLSInternal)
	svc.now = time.Now

	if svc.mailCertsLoaded() {
		t.Fatal("loaded before the first look")
	}
	if err := svc.mailSync(ctx); !errors.Is(err, errMailCertsLoading) {
		t.Fatalf("pushed without the certificate: %v", err)
	}
	if !svc.refreshMailCerts(ctx, true) {
		t.Fatal("no certificate made")
	}
	certs := svc.mailCertificates()
	if len(certs) != 1 || !strings.Contains(certs[0].CertPEM, "BEGIN CERTIFICATE") {
		t.Fatalf("%+v", certs)
	}
	// Kept: the same one again, also after a restart.
	if svc.refreshMailCerts(ctx, true) {
		t.Fatal("changed without reason")
	}
	svc.certs = newMailCerts(dir, caddy.TLSInternal)
	svc.refreshMailCerts(ctx, true)
	if svc.mailCertificates()[0].CertPEM != certs[0].CertPEM {
		t.Fatal("not reused after a restart")
	}
	if err := svc.mailSync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.mail.last().Certificates; len(got) != 1 || got[0].CertPEM != certs[0].CertPEM {
		t.Fatal("certificate not pushed")
	}
	// A new mail host gets a new certificate, pushed at once.
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailHost: strp("mx.example.net")}); err != nil {
		t.Fatal(err)
	}
	got := w.mail.last()
	if got.Hostname != "mx.example.net" || len(got.Certificates) != 1 || got.Certificates[0].CertPEM == certs[0].CertPEM {
		t.Fatal("no new certificate for the new host")
	}
	if pair, err := parseCert(got.Certificates[0]); err != nil || pair != "mx.example.net" {
		t.Fatalf("certificate for %q: %v", pair, err)
	}

	// Only with ACME are challenges answered, and only for the mail host.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("panel")) })
	h := svc.MailChallengeHandler(next)
	get := func(host, path string) string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "http://"+host+path, nil)
		h.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if get("mx.example.net", "/.well-known/acme-challenge/x") != "panel" {
		t.Fatal("internal TLS has no challenges")
	}
	res := &fakeResolver{answers: map[string][]netip.Addr{}, errs: map[string]error{}}
	svc.resolver, svc.serverIPs = res, []netip.Addr{serverV4}
	svc.certs = newMailCerts(t.TempDir(), caddy.TLSACME)
	svc.refreshMailCerts(ctx, true)
	if res.count() != 1 || svc.certs.manager() == nil || !svc.mailCertsLoaded() || len(svc.mailCertificates()) != 0 {
		t.Fatalf("ACME: %d lookups", res.count())
	}
	// HTTP-01 must be offered from the start, before any challenge request
	// (autocert keeps this in an unexported field; port 443 is Caddy's, so
	// TLS-ALPN-01 alone can never succeed).
	if !reflect.ValueOf(svc.certs.manager()).Elem().FieldByName("tryHTTP01").Bool() {
		t.Fatal("the certificate manager does not offer the HTTP-01 challenge")
	}
	svc.refreshMailCerts(ctx, true)
	if res.count() != 1 {
		t.Fatal("looked up again before the retry time")
	} // makes the manager; the host does not point here, so no CA is asked
	if body := get("mx.example.net:80", "/.well-known/acme-challenge/unknown-token"); body == "panel" {
		t.Fatal("challenge for the mail host reached the panel")
	}
	if get("other.example.net", "/.well-known/acme-challenge/x") != "panel" || get("mx.example.net", "/") != "panel" {
		t.Fatal("other requests must reach the panel")
	}
	// The web server routes the challenges only in ACME mode.
	if o := svc.caddyOptions(); o.MailHost != "" {
		t.Fatalf("mail host in the web server config without ACME and panel address: %q", o.MailHost)
	}
	svc.opts.TLSMode, svc.opts.PanelUpstream = caddy.TLSACME, "127.0.0.1:8600"
	if o := svc.caddyOptions(); o.MailHost != "mx.example.net" {
		t.Fatalf("mail host %q", o.MailHost)
	}
}

func parseCert(c config.Certificate) (string, error) {
	cfg := config.Config{Certificates: []config.Certificate{c}}
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	block, _ := pem.Decode([]byte(c.CertPEM))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	return leaf.DNSNames[0], nil
}

func TestRunKeepsMailInSyncIndependently(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	svc.web.(*fakeWeb).setFail(errors.New("caddy down"))
	svc.unhealthyRetry = 10 * time.Millisecond
	n := w.mail.loadCount()
	w.mail.set(func(f *fakeMail) {
		f.hash = "" // e.g. a restarted mail service without its configuration
		f.events = []config.Event{{ID: 7, Kind: config.EventBlocked, Address: "info@bobsite.example.com", Detail: "x"}}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx, time.Hour)
	deadline := time.Now().Add(5 * time.Second)
	for w.mail.loadCount() == n {
		if time.Now().After(deadline) {
			t.Fatal("the mail service was not synced while the web server fails")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		m, _ := svc.store.Settings(context.Background())
		if m[keyMailEventsCursor] == "7" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("events not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Only admins can make a domain receive mail without its MX pointing here;
// a member could otherwise catch mail other mailboxes send to that domain.
func TestActivateMailDomainManually(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()
	if _, err := svc.EnableMailDomain(ctx, bob, "bobother.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ActivateMailDomain(ctx, bob, "bobother.example.com"); KindOf(err) != Forbidden {
		t.Fatalf("member: %v", err)
	}
	ro := admin
	ro.ReadOnly = true
	if _, err := svc.ActivateMailDomain(ctx, ro, "bobother.example.com"); KindOf(err) != Forbidden {
		t.Fatalf("read-only admin token: %v", err)
	}
	loads := w.mail.loadCount()
	md, err := svc.ActivateMailDomain(ctx, admin, "bobother.example.com")
	if err != nil || !md.Active {
		t.Fatalf("admin: %+v %v", md, err)
	}
	if w.mail.loadCount() == loads {
		t.Fatal("activating must push the configuration")
	}
	found := false
	for _, d := range w.mail.last().Domains {
		found = found || d.Name == "bobother.example.com"
	}
	if !found {
		t.Fatal("the activated domain is not in the configuration")
	}
}
