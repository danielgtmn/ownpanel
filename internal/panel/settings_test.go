package panel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// detectedAddrs is what the fake interface enumeration reports: a public
// and a private IPv4, a public IPv6, loopback and link-local.
var detectedAddrs = []netip.Addr{
	netip.MustParseAddr("198.51.100.20"), // documentation range: not public
	netip.MustParseAddr("192.168.1.10"),
	netip.MustParseAddr("2a01:4f8::20"),
	netip.MustParseAddr("8.8.4.4"),
	netip.MustParseAddr("127.0.0.1"),
	netip.MustParseAddr("fe80::1"),
}

type settingsEnv struct {
	svc  *Service
	web  *fakeWeb
	st   *store.Store
	dir  string
	opts caddy.Options
	ips  []netip.Addr
}

// newSettingsEnv is a service as main builds it (with the panel upstream),
// with an admin "root" (id 1) and flags as given in opts and ips.
func newSettingsEnv(t *testing.T, opts caddy.Options, ips ...netip.Addr) *settingsEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.NewUserID = countingIDs()
	if _, err := st.CreateUser(context.Background(), admin.Name, "", store.RoleAdmin, true); err != nil {
		t.Fatal(err)
	}
	e := &settingsEnv{web: &fakeWeb{}, st: st, dir: dir, ips: ips}
	if opts.TLSMode == "" {
		opts.TLSMode = caddy.TLSACME
	}
	opts.AdminListen, opts.StorageDir, opts.HTTPPort, opts.HTTPSPort = "127.0.0.1:2019", filepath.Join(dir, "caddy"), 80, 443
	opts.PanelUpstream = "127.0.0.1:8600"
	e.opts = opts
	e.svc = e.reopen(t)
	return e
}

func (e *settingsEnv) reopen(t *testing.T) *Service {
	t.Helper()
	svc, err := New(Config{
		Store:         e.st,
		Deploys:       &deploy.Manager{Base: filepath.Join(e.dir, "sites")},
		Web:           e.web,
		Caddy:         e.opts,
		SetupCodeFile: filepath.Join(e.dir, "setup-code"),
		ServerIPs:     e.ips,
		Interfaces:    func() ([]netip.Addr, error) { return detectedAddrs, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func str(s string) *string { return &s }

func strs(s ...string) *[]string { return &s }

func TestSettingsDefaultsAndDetectedAddresses(t *testing.T) {
	e := newSettingsEnv(t, caddy.Options{})
	got, err := e.svc.Settings(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{
		ServerIPs: []string{},
		DetectedIPs: []DetectedIP{
			{"8.8.4.4", true}, {"192.168.1.10", false}, {"198.51.100.20", false}, {"2a01:4f8::20", true},
		},
		TLSMode: "acme", IPAccess: true,
		MailBlocklists: []string{"zen.spamhaus.org"}, MailBlocklistsDefault: true, MailJunkScore: 5,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings\n got %+v\nwant %+v", got, want)
	}
}

func TestSettingsUpdateAppliesLiveAndPersists(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, caddy.Options{})
	got, err := e.svc.UpdateSettings(ctx, admin, SettingsUpdate{
		PanelDomain: str(" Panel.Example.COM. "), ServerIPs: strs("2001:db8::1, 8.8.8.8"), ACMEEmail: str("ops@example.com"),
	})
	// 2001:db8::/32 is documentation, not public.
	wantKind(t, err, Invalid)
	got, err = e.svc.UpdateSettings(ctx, admin, SettingsUpdate{
		PanelDomain: str(" Panel.Example.COM. "), ServerIPs: strs("2a01:4f8::1", "8.8.8.8"), ACMEEmail: str("ops@example.com"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.PanelDomain != "panel.example.com" || !reflect.DeepEqual(got.ServerIPs, []string{"8.8.8.8", "2a01:4f8::1"}) ||
		got.ACMEEmail != "ops@example.com" || !got.IPAccess || got.SetupComplete {
		t.Fatalf("after update: %+v", got)
	}

	cfg := e.web.last()
	for _, want := range []string{
		`{"handle":[{"handler":"reverse_proxy","transport":{"dial_timeout":"5s","protocol":"http","response_header_timeout":"10m"},"upstreams":[{"dial":"127.0.0.1:8600"}]}],"match":[{"host":["panel.example.com"]}],"terminal":true}`,
		// Configured, detected public and loopback addresses serve the panel.
		`{"handle":[{"handler":"reverse_proxy","transport":{"dial_timeout":"5s","protocol":"http","response_header_timeout":"10m"},"upstreams":[{"dial":"127.0.0.1:8600"}]}],"match":[{"host":["8.8.4.4","8.8.8.8","127.0.0.1","::1","2a01:4f8::1","2a01:4f8::20"]}],"terminal":true}`,
		`{"email":"ops@example.com","module":"acme"}`,
		`"subjects":["8.8.4.4","8.8.8.8","127.0.0.1","::1","2a01:4f8::1","2a01:4f8::20"]`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config misses %s\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "192.168.1.10") {
		t.Error("private detected addresses are not served")
	}

	// The DNS features use the addresses from the settings.
	if ips, _ := e.svc.ServerIPs(ctx, admin); !reflect.DeepEqual(ips, []string{"8.8.8.8", "2a01:4f8::1"}) {
		t.Errorf("server IPs %v", ips)
	}

	// A new service on the same database sees the same settings.
	again, err := e.reopen(t).Settings(ctx, admin)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("after restart: %+v %v", again, err)
	}

	// Empty values clear; omitted ones stay.
	got, err = e.svc.UpdateSettings(ctx, admin, SettingsUpdate{ServerIPs: strs(), ACMEEmail: str("")})
	if err != nil || len(got.ServerIPs) != 0 || got.ACMEEmail != "" || got.PanelDomain != "panel.example.com" {
		t.Fatalf("clearing: %+v %v", got, err)
	}
	if strings.Contains(e.web.last(), `"acme"`) {
		t.Error("cleared e-mail must leave the config")
	}

	acts, _ := e.st.Activities(ctx, 10)
	var details []string
	for _, a := range acts {
		if a.Action == "settings.update" {
			details = append(details, a.Detail)
		}
	}
	if len(details) != 2 || !strings.Contains(details[1], "panel domain panel.example.com") ||
		!strings.Contains(details[1], "server addresses 8.8.8.8, 2a01:4f8::1") || !strings.Contains(details[1], "certificate e-mail changed") {
		t.Fatalf("activity %q", details)
	}
	for _, d := range details {
		if strings.Contains(d, "ops@example.com") {
			t.Fatalf("the e-mail address must not be logged: %q", d)
		}
	}
}

func TestCompleteSetupRedirectsAddresses(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, caddy.Options{})
	if done, _ := e.svc.SetupComplete(ctx); done {
		t.Fatal("a new install is not set up")
	}
	// Completing without a domain keeps the panel on the addresses.
	got, err := e.svc.CompleteSetup(ctx, admin)
	if err != nil || !got.SetupComplete || !got.IPAccess {
		t.Fatalf("complete without domain: %+v %v", got, err)
	}
	if done, _ := e.svc.SetupComplete(ctx); !done {
		t.Fatal("setup must be complete")
	}
	if !strings.Contains(e.web.last(), `{"handle":[{"handler":"reverse_proxy","transport":{"dial_timeout":"5s","protocol":"http","response_header_timeout":"10m"},"upstreams":[{"dial":"127.0.0.1:8600"}]}],"match":[{"host":["8.8.4.4","127.0.0.1","::1","2a01:4f8::20"]}]`) {
		t.Fatalf("without a domain the addresses serve the panel: %s", e.web.last())
	}

	got, err = e.svc.UpdateSettings(ctx, admin, SettingsUpdate{PanelDomain: str("panel.example.com")})
	if err != nil || got.IPAccess {
		t.Fatalf("with a domain after completion: %+v %v", got, err)
	}
	want := `{"handle":[{"handler":"static_response","headers":{"Location":["https://panel.example.com{http.request.uri}"]},"status_code":308}],"match":[{"host":["8.8.4.4","127.0.0.1","::1","2a01:4f8::20"]}],"terminal":true}`
	if !strings.Contains(e.web.last(), want) {
		t.Fatalf("addresses must redirect to the domain: %s", e.web.last())
	}
	n := e.web.count()
	if _, err := e.svc.CompleteSetup(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if e.web.count() != n {
		t.Error("completing again changes nothing")
	}
	acts, _ := e.st.Activities(ctx, 10)
	count := 0
	for _, a := range acts {
		if a.Action == "setup.complete" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("setup.complete logged %d times", count)
	}

	// Removing the domain opens the addresses again.
	if got, _ := e.svc.UpdateSettings(ctx, admin, SettingsUpdate{PanelDomain: str("")}); !got.IPAccess {
		t.Fatal("without a domain the addresses serve the panel again")
	}
}

func TestSessionSetupCompleteFlow(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newEmptyService(t)
	if done, err := svc.SetupComplete(ctx); err != nil || done {
		t.Fatalf("no user yet: %v %v", done, err)
	}
	// Even with the flag set, no user means not complete.
	svc.settings.setupComplete = true
	if done, _ := svc.SetupComplete(ctx); done {
		t.Fatal("without users setup is never complete")
	}
}

func TestSettingsFlagsOverrideAndLock(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, caddy.Options{PanelDomain: "flag.example.com", ACMEEmail: "flag@example.com"}, netip.MustParseAddr("8.8.8.8"))
	// Values stored earlier (e.g. before the flags were added) lose.
	if err := e.st.PutSettings(ctx, map[string]string{keyPanelDomain: "stored.example.com", keyACMEEmail: "stored@example.com", keyServerIPs: "9.9.9.9"}); err != nil {
		t.Fatal(err)
	}
	e.svc = e.reopen(t)
	got, err := e.svc.Settings(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if got.PanelDomain != "flag.example.com" || !got.PanelDomainLocked || got.ACMEEmail != "flag@example.com" || !got.ACMEEmailLocked ||
		!reflect.DeepEqual(got.ServerIPs, []string{"8.8.8.8"}) || !got.ServerIPsLocked {
		t.Fatalf("flags must win: %+v", got)
	}
	for flag, u := range map[string]SettingsUpdate{
		"-panel-domain": {PanelDomain: str("other.example.com")},
		"-server-ip":    {ServerIPs: strs("9.9.9.9")},
		"-acme-email":   {ACMEEmail: str("")},
	} {
		_, err := e.svc.UpdateSettings(ctx, admin, u)
		wantKind(t, err, Conflict)
		if want := "This value is set in the server configuration (" + flag + ") and cannot be changed here."; err.Error() != want {
			t.Errorf("%s: %q", flag, err)
		}
	}
	if _, err := e.svc.SaveSite(ctx, admin, "s", []string{"flag.example.com"}); KindOf(err) != Invalid {
		t.Fatalf("the flag's panel domain is not available for sites: %v", err)
	}
	if err := e.svc.Sync(ctx, System); err != nil {
		t.Fatal(err)
	}
	cfg := e.web.last()
	if !strings.Contains(cfg, `"flag@example.com"`) || !strings.Contains(cfg, `"flag.example.com"`) || strings.Contains(cfg, "stored") || strings.Contains(cfg, "9.9.9.9") {
		t.Errorf("config must use the flags: %s", cfg)
	}
}

func TestSettingsValidation(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, caddy.Options{})
	if _, err := e.svc.SaveSite(ctx, admin, "blog", []string{"blog.example.com"}); err != nil && KindOf(err) != NotSynced {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		u    SettingsUpdate
		want Kind
	}{
		{"domain", SettingsUpdate{PanelDomain: str("panel.example.com")}, ok},
		{"domain trailing dot and case", SettingsUpdate{PanelDomain: str("Admin.Example.com.")}, ok},
		{"no dot", SettingsUpdate{PanelDomain: str("localhost")}, Invalid},
		{"ipv4", SettingsUpdate{PanelDomain: str("8.8.8.8")}, Invalid},
		{"ipv6", SettingsUpdate{PanelDomain: str("2a01:4f8::1")}, Invalid},
		{"bracketed ipv6", SettingsUpdate{PanelDomain: str("[2a01:4f8::1]")}, Invalid},
		{"numeric last label", SettingsUpdate{PanelDomain: str("panel.123")}, Invalid},
		{"url", SettingsUpdate{PanelDomain: str("https://panel.example.com/")}, Invalid},
		{"empty label", SettingsUpdate{PanelDomain: str("panel..example.com")}, Invalid},
		{"used by a site", SettingsUpdate{PanelDomain: str("blog.example.com")}, Conflict},
		{"public addresses", SettingsUpdate{ServerIPs: strs("8.8.8.8", "2a01:4f8::1")}, ok},
		{"private address", SettingsUpdate{ServerIPs: strs("192.168.1.10")}, Invalid},
		{"loopback", SettingsUpdate{ServerIPs: strs("127.0.0.1")}, Invalid},
		{"not an address", SettingsUpdate{ServerIPs: strs("example.com")}, Invalid},
		{"e-mail", SettingsUpdate{ACMEEmail: str("ops@example.com")}, ok},
		{"e-mail with name", SettingsUpdate{ACMEEmail: str("Ops <ops@example.com>")}, Invalid},
		{"e-mail without domain dot", SettingsUpdate{ACMEEmail: str("ops@localhost")}, Invalid},
		{"not an e-mail", SettingsUpdate{ACMEEmail: str("ops")}, Invalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.UpdateSettings(ctx, admin, tc.u)
			if tc.want == ok {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				return
			}
			wantKind(t, err, tc.want)
		})
	}

	// SaveSite refuses the panel domain from the settings.
	if _, err := e.svc.UpdateSettings(ctx, admin, SettingsUpdate{PanelDomain: str("panel.example.com")}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.SaveSite(ctx, admin, "other", []string{"panel.example.com"})
	wantKind(t, err, Invalid)

	// With self-signed certificates (local testing) private addresses are fine.
	in := newSettingsEnv(t, caddy.Options{TLSMode: caddy.TLSInternal})
	if _, err := in.svc.UpdateSettings(ctx, admin, SettingsUpdate{ServerIPs: strs("192.168.1.10")}); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsSavedWhenWebServerFails(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, caddy.Options{})
	e.web.setFail(errors.New("caddy down"))
	got, err := e.svc.UpdateSettings(ctx, admin, SettingsUpdate{PanelDomain: str("panel.example.com")})
	wantKind(t, err, NotSynced)
	if got.PanelDomain != "panel.example.com" {
		t.Fatalf("saved anyway: %+v", got)
	}
}

func TestSettingsPermissions(t *testing.T) {
	ctx := context.Background()
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", admin}, {"admin write token", rootWriteTok}, {"member", bob}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody},
	}
	F, U := Forbidden, Unauthenticated
	ops := []struct {
		name string
		run  func(svc *Service, a Actor) error
		want []Kind
	}{
		{"get", func(svc *Service, a Actor) error { _, err := svc.Settings(ctx, a); return err },
			[]Kind{ok, ok, F, F, F, ok, F, U}},
		{"checks", func(svc *Service, a Actor) error { _, err := svc.SettingsChecks(ctx, a); return err },
			[]Kind{ok, ok, F, F, F, ok, F, U}},
		{"update", func(svc *Service, a Actor) error {
			_, err := svc.UpdateSettings(ctx, a, SettingsUpdate{ServerIPs: strs("192.168.1.10")})
			return err
		}, []Kind{ok, ok, F, F, F, F, F, U}},
		{"complete", func(svc *Service, a Actor) error { _, err := svc.CompleteSetup(ctx, a); return err },
			[]Kind{ok, ok, F, F, F, F, F, U}},
	}
	for _, op := range ops {
		for i, ac := range actors {
			t.Run(op.name+"/"+ac.name, func(t *testing.T) {
				w := newWorld(t)
				w.svc.interfaces = func() ([]netip.Addr, error) { return nil, nil }
				err := op.run(w.svc, ac.a)
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

// ---- readiness checks ----

func newCert(t *testing.T, template, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if parent == nil {
		parent, parentKey = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

// tlsServer serves leaf on loopback and returns its address.
func tlsServer(t *testing.T, leaf *x509.Certificate, key *ecdsa.PrivateKey) string {
	t.Helper()
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leaf.Raw}, PrivateKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				c.(*tls.Conn).Handshake()
				c.Close()
			}()
		}
	}()
	return l.Addr().String()
}

func TestSettingsChecks(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	ca, caKey := newCert(t, &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}, nil, nil)
	leafTemplate := func(serial int64) *x509.Certificate {
		return &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "panel.example.com"}, DNSNames: []string{"panel.example.com"},
			NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
	}
	leaf, leafKey := newCert(t, leafTemplate(2), ca, caKey)
	trusted := tlsServer(t, leaf, leafKey)
	self, selfKey := newCert(t, leafTemplate(3), nil, nil)
	selfSigned := tlsServer(t, self, selfKey)
	roots := x509.NewCertPool()
	roots.AddCert(ca)

	e := newSettingsEnv(t, caddy.Options{})
	res := &fakeResolver{answers: map[string][]netip.Addr{}, errs: map[string]error{}}
	e.svc.resolver = res
	e.svc.certRoots = roots

	got, err := e.svc.SettingsChecks(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	want := SettingsChecks{
		DNS:         DNSCheck{State: CheckNotConfigured, Found: []string{}, Expected: []string{}},
		Certificate: CertificateCheck{State: CheckNotConfigured, Detail: "No panel domain is set yet."},
		PreviewDNS:  PreviewDNS{State: CheckNotConfigured, Found: []string{}, Expected: []string{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("without a domain: %+v", got)
	}

	if _, err := e.svc.UpdateSettings(ctx, admin, SettingsUpdate{PanelDomain: str("panel.example.com"), ServerIPs: strs("8.8.8.8")}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		answer    []string
		lookupErr error
		certAddr  string
		dns, cert string
		found     []string
	}{
		{"points here, trusted", []string{"8.8.8.8"}, nil, trusted, DomainPointsHere, CertReady, []string{"8.8.8.8"}},
		{"elsewhere, self-signed", []string{"8.8.8.8", "1.1.1.1"}, nil, selfSigned, DomainPointsElsewhere, CertPending, []string{"1.1.1.1"}},
		{"no record, nothing listening", nil, nil, "127.0.0.1:1", DomainNoRecord, CertPending, []string{}},
		{"lookup failed", nil, errors.New("timeout"), selfSigned, DomainLookupFailed, CertPending, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delete(res.answers, "panel.example.com")
			delete(res.errs, "panel.example.com")
			for _, a := range tc.answer {
				res.answers["panel.example.com"] = append(res.answers["panel.example.com"], netip.MustParseAddr(a))
			}
			if tc.lookupErr != nil {
				res.errs["panel.example.com"] = tc.lookupErr
			}
			e.svc.certAddr = tc.certAddr
			got, err := e.svc.SettingsChecks(ctx, admin)
			if err != nil {
				t.Fatal(err)
			}
			if got.DNS.State != tc.dns || !reflect.DeepEqual(got.DNS.Found, tc.found) || !reflect.DeepEqual(got.DNS.Expected, []string{"8.8.8.8"}) {
				t.Errorf("dns %+v", got.DNS)
			}
			if got.Certificate.State != tc.cert || got.Certificate.Detail == "" {
				t.Errorf("certificate %+v", got.Certificate)
			}
		})
	}

	// The system roots do not trust the test CA.
	e.svc.certRoots, e.svc.certAddr = nil, trusted
	if got, _ := e.svc.SettingsChecks(ctx, admin); got.Certificate.State != CertPending {
		t.Errorf("unknown CA must be pending: %+v", got.Certificate)
	}

	in := newSettingsEnv(t, caddy.Options{TLSMode: caddy.TLSInternal})
	in.svc.resolver = res
	got, _ = in.svc.SettingsChecks(ctx, admin)
	if got.Certificate.State != CertNotApplicable || got.DNS.State != CheckNotConfigured {
		t.Errorf("internal mode: %+v", got)
	}
}

func TestDetectAddressesRuns(t *testing.T) {
	addrs, err := DetectAddresses()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if !a.IsValid() {
			t.Fatalf("invalid address %v", a)
		}
	}
}
