package panel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/dns"
)

func TestMergeSPF(t *testing.T) {
	long := "v=spf1"
	for i := range 10 {
		long += fmt.Sprintf(" include:_spf%d.example.net", i)
	}
	cases := []struct {
		name, in, want string
		ok             bool
		conflict       string // part of the reason; "" if none
	}{
		{"soft fail kept", "v=spf1 include:_spf.google.com ~all", "v=spf1 mx include:_spf.google.com ~all", false, ""},
		{"redirect kept", "v=spf1 redirect=_spf.example.net", "v=spf1 mx redirect=_spf.example.net", false, ""},
		{"only version", "v=spf1", "v=spf1 mx", false, ""},
		{"upper case version", "V=SPF1 a -all", "v=spf1 mx a -all", false, ""},
		{"mx already", "v=spf1 mx -all", "v=spf1 mx -all", true, ""},
		{"+mx already", "v=spf1 a +mx ~all", "v=spf1 a +mx ~all", true, ""},
		{"mx of another domain", "v=spf1 mx:other.example -all", "v=spf1 mx mx:other.example -all", false, ""},
		{"+all", "v=spf1 a +all", "", false, "+all"},
		{"bare all", "v=spf1 all", "", false, "+all"},
		{"?all", "v=spf1 include:x.example ?all", "", false, "?all"},
		{"-mx", "v=spf1 -mx -all", "", false, "not allowed"},
		{"ten lookups plus mx", long + " -all", "", false, "11 DNS lookups"},
		{"nine lookups plus mx", "v=spf1 a ptr exists:x.example include:1 include:2 include:3 include:4 include:5 include:6 -all",
			"v=spf1 mx a ptr exists:x.example include:1 include:2 include:3 include:4 include:5 include:6 -all", false, ""},
		{"redirect counts", "v=spf1 include:1 include:2 include:3 include:4 include:5 include:6 include:7 include:8 include:9 redirect=x.example", "", false, "11 DNS lookups"},
		{"ip and exp do not count", "v=spf1 ip4:192.0.2.0/24 ip6:2001:db8::/32 exp=explain.example -all", "v=spf1 mx ip4:192.0.2.0/24 ip6:2001:db8::/32 exp=explain.example -all", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := mergeSPF(c.in)
			if c.conflict != "" {
				if !strings.Contains(m.conflict, c.conflict) || m.value != c.in {
					t.Fatalf("want conflict %q, got %+v", c.conflict, m)
				}
				return
			}
			if m.conflict != "" || m.ok != c.ok || m.value != c.want {
				t.Fatalf("got %+v, want %q ok=%v", m, c.want, c.ok)
			}
		})
	}
	// Judging: none, one, two SPF records.
	spec := mailRecordSpec{kind: MailRecordSPF, typ: "TXT", sub: "@", want: defaultSPF}
	if j := judge(spec, testMailHost, []string{"google-site-verification=x"}); j.status != RecordMissing || j.want != defaultSPF {
		t.Fatalf("none: %+v", j)
	}
	if j := judge(spec, testMailHost, []string{"v=spf1 -all", "v=spf1 mx -all"}); j.status != RecordConflict || !strings.Contains(j.note, "several SPF") {
		t.Fatalf("two: %+v", j)
	}
	if j := judge(spec, testMailHost, []string{"v=spf1 a +all"}); j.status != RecordConflict {
		t.Fatalf("+all: %+v", j)
	}
}

// memDNS is a DNS provider account in memory.
type memDNS struct {
	mu     sync.Mutex
	zones  []dns.Zone
	sets   map[string][]dns.RecordSet // by zone
	nWrite int                        // SetRecordSet calls that succeeded
	fail   error                      // returned by SetRecordSet
}

func newMemDNS(zones ...string) *memDNS {
	m := &memDNS{sets: map[string][]dns.RecordSet{}}
	for _, z := range zones {
		m.zones = append(m.zones, dns.Zone{ID: z, Name: z})
	}
	return m
}

func (m *memDNS) Zones(context.Context) ([]dns.Zone, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.zones), nil
}

func (m *memDNS) RecordSets(_ context.Context, zone string) ([]dns.RecordSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []dns.RecordSet
	for _, rs := range m.sets[zone] {
		rs.Values = slices.Clone(rs.Values)
		out = append(out, rs)
	}
	return out, nil
}

func (m *memDNS) SetRecordSet(_ context.Context, zone string, rs dns.RecordSet) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.nWrite++
	m.putLocked(zone, rs)
	return nil
}

func (m *memDNS) put(zone, name, typ string, values ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putLocked(zone, dns.RecordSet{Name: name, Type: typ, Values: values})
}

func (m *memDNS) putLocked(zone string, rs dns.RecordSet) {
	sets := m.sets[zone]
	for i := range sets {
		if sets[i].Name == rs.Name && sets[i].Type == rs.Type {
			sets[i] = rs
			return
		}
	}
	m.sets[zone] = append(sets, rs)
}

func (m *memDNS) get(zone, name, typ string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rs := range m.sets[zone] {
		if rs.Name == name && rs.Type == typ {
			return slices.Clone(rs.Values)
		}
	}
	return nil
}

func (m *memDNS) writes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nWrite
}

func (m *memDNS) DeleteRecordSet(context.Context, string, string, string) error { return nil }
func (m *memDNS) CreateZone(context.Context, string) (dns.Zone, error) {
	return dns.Zone{}, errors.New("not here")
}
func (m *memDNS) DeleteZone(context.Context, string) error { return errors.New("not here") }

// zoneLookups answers MX, TXT and SRV lookups from the records of memDNS
// accounts, like their authoritative nameservers would; names in errs fail.
type zoneLookups struct {
	*fakeResolver
	accounts []*memDNS
	mu       sync.Mutex
	errs     map[string]error
}

func (z *zoneLookups) fail(name string, err error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.errs[name] = err
}

func (z *zoneLookups) values(name, typ string) ([]string, error) {
	z.mu.Lock()
	err := z.errs[name]
	z.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, acc := range z.accounts {
		zones, _ := acc.Zones(context.Background())
		if zone, rel, ok := dns.FindZone(zones, name); ok {
			if v := acc.get(zone.Name, rel, typ); len(v) > 0 {
				return v, nil
			}
		}
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (z *zoneLookups) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	v, err := z.values(name, "MX")
	var out []*net.MX
	for _, x := range v {
		var pref uint16
		var host string
		fmt.Sscanf(x, "%d %s", &pref, &host)
		out = append(out, &net.MX{Pref: pref, Host: host})
	}
	return out, err
}

func (z *zoneLookups) LookupTXT(_ context.Context, name string) ([]string, error) {
	return z.values(name, "TXT")
}

func (z *zoneLookups) LookupSRV(_ context.Context, _, _, name string) (string, []*net.SRV, error) {
	v, err := z.values(name, "SRV")
	var out []*net.SRV
	for _, x := range v {
		var r net.SRV
		fmt.Sscanf(x, "%d %d %d %s", &r.Priority, &r.Weight, &r.Port, &r.Target)
		out = append(out, &r)
	}
	return name + ".", out, err
}

type recordsWorld struct {
	*mailWorld
	bobDNS, rootDNS *memDNS
	look            *zoneLookups
	bobConn         int64
}

// newRecordsWorld: the mail world where bob's DNS connection holds the
// zones bobzone.org and fresh.org and root's holds root.org; bob has mail
// for bobzone.org (MX elsewhere, so not active) and fresh.org (no records).
func newRecordsWorld(t *testing.T) *recordsWorld {
	t.Helper()
	w := &recordsWorld{mailWorld: newMailWorld(t), bobDNS: newMemDNS("bobzone.org", "fresh.org"), rootDNS: newMemDNS("root.org")}
	svc := w.svc
	w.look = &zoneLookups{fakeResolver: &fakeResolver{answers: map[string][]netip.Addr{}, errs: map[string]error{}},
		accounts: []*memDNS{w.bobDNS, w.rootDNS}, errs: map[string]error{}}
	svc.resolver, svc.mx = w.look, w.look
	svc.newProvider = func(_, token string) (dns.Provider, error) {
		switch token {
		case bobToken:
			return w.bobDNS, nil
		case rootToken:
			return w.rootDNS, nil
		}
		return nil, errors.New("unknown token")
	}
	ctx := context.Background()
	c, err := svc.CreateDNSConnection(ctx, bob, "hetzner", "bob-main", bobToken)
	if err != nil {
		t.Fatal(err)
	}
	w.bobConn = c.ID
	if _, err := svc.CreateDNSConnection(ctx, admin, "hetzner", "root-main", rootToken); err != nil {
		t.Fatal(err)
	}
	b := w.bobDNS
	b.put("bobzone.org", "@", "MX", "10 mx.other.net.")
	b.put("bobzone.org", "@", "TXT", "google-site-verification=xyz", "v=spf1 include:_spf.google.com ~all")
	b.put("bobzone.org", "_dmarc", "TXT", "v=DMARC1; p=reject")
	b.put("bobzone.org", "_imaps._tcp", "SRV", "0 1 993 mail.host.test.")
	b.put("bobzone.org", "www", "A", "192.0.2.1")
	for _, d := range []string{"bobzone.org", "fresh.org"} {
		md, err := svc.EnableMailDomain(ctx, bob, d)
		if err != nil || md.Active {
			t.Fatalf("enable %s: %+v %v", d, md, err)
		}
	}
	return w
}

func recordOf(t *testing.T, r MailRecords, kind string) MailRecord {
	t.Helper()
	for _, x := range r.Records {
		if x.Kind == kind {
			return x
		}
	}
	t.Fatalf("no %s record in %+v", kind, r.Records)
	return MailRecord{}
}

func TestMailRecordStatuses(t *testing.T) {
	w := newRecordsWorld(t)
	ctx := context.Background()
	w.look.fail("_submissions._tcp.bobzone.org", errors.New("timeout"))

	r, err := w.svc.MailRecords(ctx, bob, "BobZone.org")
	if err != nil {
		t.Fatal(err)
	}
	if r.Domain != "bobzone.org" || r.MailHost != testMailHost || !r.CanApply || r.ConnectionID != w.bobConn || r.Zone != "bobzone.org" || r.ApplyNote != "" {
		t.Fatalf("records %+v", r)
	}
	if len(r.Records) != 6 {
		t.Fatalf("%d records", len(r.Records))
	}
	mx := recordOf(t, r, MailRecordMX)
	if mx.Status != RecordDifferent || mx.Want != "10 mail.host.test." || !slices.Equal(mx.Found, []string{"10 mx.other.net."}) ||
		mx.Name != "@" || mx.FQDN != "bobzone.org" || !strings.Contains(mx.Note, "mx.other.net") {
		t.Fatalf("mx %+v", mx)
	}
	spf := recordOf(t, r, MailRecordSPF)
	if spf.Status != RecordDifferent || spf.Want != "v=spf1 mx include:_spf.google.com ~all" || len(spf.Found) != 2 {
		t.Fatalf("spf %+v", spf)
	}
	dkim := recordOf(t, r, MailRecordDKIM)
	md, _ := w.svc.MailDomainOf(ctx, bob, "bobzone.org")
	if dkim.Status != RecordMissing || dkim.Want != md.DKIMRecord || dkim.Name != md.DKIMSelector+"._domainkey" || dkim.Type != "TXT" {
		t.Fatalf("dkim %+v", dkim)
	}
	if strings.Contains(dkim.Note, "split") {
		t.Fatal("split hint although the records can be applied")
	}
	dmarc := recordOf(t, r, MailRecordDMARC)
	if dmarc.Status != RecordOK || dmarc.Want != "v=DMARC1; p=reject" || !strings.Contains(dmarc.Note, "left as it is") || dmarc.FQDN != "_dmarc.bobzone.org" {
		t.Fatalf("dmarc %+v", dmarc)
	}
	if sub := recordOf(t, r, MailRecordSRVSubmissions); sub.Status != RecordUnknown || sub.Want != "0 1 465 mail.host.test." || len(sub.Found) != 0 {
		t.Fatalf("submissions %+v", sub)
	}
	if imaps := recordOf(t, r, MailRecordSRVIMAPS); imaps.Status != RecordOK || imaps.Name != "_imaps._tcp" {
		t.Fatalf("imaps %+v", imaps)
	}

	// A read-only token reads, but cannot apply; neither can an admin
	// without a connection of their own for the zone.
	for _, a := range []Actor{bobRead, admin, rootRead} {
		r, err := w.svc.MailRecords(ctx, a, "bobzone.org")
		if err != nil || r.CanApply || r.ConnectionID != 0 || r.Zone != "" || r.ApplyNote == "" {
			t.Fatalf("%s: %+v %v", a.Name, r, err)
		}
		if d := recordOf(t, r, MailRecordDKIM); !strings.Contains(d.Note, "split") {
			t.Fatalf("no split hint for a manual DKIM record: %q", d.Note)
		}
	}
	_, err = w.svc.MailRecords(ctx, bobLimited, "bobzone.org")
	wantKind(t, err, Forbidden)
	_, err = w.svc.MailRecords(ctx, bob, "rootsite.example.com")
	wantKind(t, err, NotFound)
	_, err = w.svc.MailRecords(ctx, nobody, "bobzone.org")
	wantKind(t, err, Unauthenticated)

	// Several SPF records are a conflict; the mail host must be set.
	w.bobDNS.put("bobzone.org", "@", "TXT", "v=spf1 -all", "v=spf1 mx ~all")
	r, _ = w.svc.MailRecords(ctx, bob, "bobzone.org")
	if spf := recordOf(t, r, MailRecordSPF); spf.Status != RecordConflict || spf.Note == "" {
		t.Fatalf("two SPF %+v", spf)
	}
	w.svc.settingsMu.Lock()
	w.svc.settings.mailHost = ""
	w.svc.settingsMu.Unlock()
	_, err = w.svc.MailRecords(ctx, bob, "bobzone.org")
	wantKind(t, err, Conflict)
	if !strings.Contains(err.Error(), "set the mail server name in Settings first") {
		t.Fatal(err)
	}
	_, err = w.svc.SetUpMailRecords(ctx, bob, "bobzone.org", true)
	wantKind(t, err, Conflict)
}

func TestSetUpMailRecords(t *testing.T) {
	w := newRecordsWorld(t)
	ctx := context.Background()
	b := w.bobDNS

	// Without confirm: the MX change needs it, nothing is written.
	res, err := w.svc.SetUpMailRecords(ctx, bob, "bobzone.org", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || b.writes() != 0 || len(res.Changes) != 6 {
		t.Fatalf("plan %+v, %d writes", res, b.writes())
	}
	byKind := map[string]MailRecordChange{}
	for _, c := range res.Changes {
		byKind[c.Kind] = c
		if c.NeedsConfirm != (c.Kind == MailRecordMX) {
			t.Fatalf("needs_confirm %+v", c)
		}
	}
	if c := byKind[MailRecordMX]; c.Action != ChangeReplace || !slices.Equal(c.Old, []string{"10 mx.other.net."}) || !slices.Equal(c.New, []string{"10 mail.host.test."}) {
		t.Fatalf("mx %+v", c)
	}
	if c := byKind[MailRecordSPF]; c.Action != ChangeReplace || !slices.Equal(c.Old, []string{"v=spf1 include:_spf.google.com ~all"}) ||
		!slices.Equal(c.New, []string{"v=spf1 mx include:_spf.google.com ~all"}) || !strings.Contains(c.Note, "kept") {
		t.Fatalf("spf %+v", c)
	}
	if c := byKind[MailRecordDMARC]; c.Action != ChangeKeep || !slices.Equal(c.Old, []string{"v=DMARC1; p=reject"}) {
		t.Fatalf("dmarc %+v", c)
	}
	if byKind[MailRecordDKIM].Action != ChangeCreate || byKind[MailRecordSRVSubmissions].Action != ChangeCreate || byKind[MailRecordSRVIMAPS].Action != ChangeKeep {
		t.Fatalf("plan %+v", res.Changes)
	}

	// With confirm everything is written; the domain becomes active.
	res, err = w.svc.SetUpMailRecords(ctx, bob, "bobzone.org", true)
	if err != nil || !res.Applied {
		t.Fatalf("%+v %v", res, err)
	}
	if b.writes() != 4 {
		t.Fatalf("%d writes, want mx, spf, dkim, submissions", b.writes())
	}
	if v := b.get("bobzone.org", "@", "MX"); !slices.Equal(v, []string{"10 mail.host.test."}) {
		t.Fatalf("MX %v", v)
	}
	if v := b.get("bobzone.org", "@", "TXT"); !slices.Equal(v, []string{"google-site-verification=xyz", "v=spf1 mx include:_spf.google.com ~all"}) {
		t.Fatalf("TXT @ %v", v)
	}
	if v := b.get("bobzone.org", "_dmarc", "TXT"); !slices.Equal(v, []string{"v=DMARC1; p=reject"}) {
		t.Fatalf("DMARC changed: %v", v)
	}
	md, _ := w.svc.MailDomainOf(ctx, bob, "bobzone.org")
	if v := b.get("bobzone.org", md.DKIMSelector+"._domainkey", "TXT"); !slices.Equal(v, []string{md.DKIMRecord}) {
		t.Fatalf("DKIM %v", v)
	}
	if v := b.get("bobzone.org", "_submissions._tcp", "SRV"); !slices.Equal(v, []string{"0 1 465 mail.host.test."}) {
		t.Fatalf("SRV %v", v)
	}
	if !md.Active {
		t.Fatalf("not activated: %+v", md)
	}
	acts, _ := w.svc.store.Activities(ctx, 10)
	found := false
	for _, a := range acts {
		if a.Action == "mail.records.set" && a.Target == "bobzone.org" {
			found = strings.Contains(a.Detail, "mx, spf, dkim, srv-submissions") && !strings.Contains(a.Detail, "DKIM1")
		}
	}
	if !found {
		t.Fatalf("activity %+v", acts)
	}
	r, _ := w.svc.MailRecords(ctx, bob, "bobzone.org")
	for _, x := range r.Records {
		if x.Status != RecordOK {
			t.Fatalf("after applying %+v", x)
		}
	}

	// Again: nothing to do.
	res, err = w.svc.SetUpMailRecords(ctx, bob, "bobzone.org", false)
	if err != nil || !res.Applied || b.writes() != 4 {
		t.Fatalf("%+v %v", res, err)
	}
	for _, c := range res.Changes {
		if c.Action != ChangeKeep {
			t.Fatalf("second run %+v", c)
		}
	}

	// A domain without any records needs no confirmation.
	res, err = w.svc.SetUpMailRecords(ctx, bob, "fresh.org", false)
	if err != nil || !res.Applied {
		t.Fatalf("fresh %+v %v", res, err)
	}
	for _, c := range res.Changes {
		if c.Action != ChangeCreate || c.NeedsConfirm {
			t.Fatalf("fresh %+v", c)
		}
	}
	if v := b.get("fresh.org", "_dmarc", "TXT"); !slices.Equal(v, []string{defaultDMARC}) {
		t.Fatalf("DMARC %v", v)
	}
	if v := b.get("fresh.org", "@", "TXT"); !slices.Equal(v, []string{defaultSPF}) {
		t.Fatalf("SPF %v", v)
	}
}

func TestSetUpMailRecordsSkips(t *testing.T) {
	w := newRecordsWorld(t)
	ctx := context.Background()
	b := w.bobDNS
	b.put("bobzone.org", "@", "TXT", "v=spf1 a ~all", "v=spf1 mx -all", "other")
	b.put("bobzone.org", "_dmarc", "CNAME", "dmarc.example.net.")
	res, err := w.svc.SetUpMailRecords(ctx, bob, "bobzone.org", true)
	if err != nil || !res.Applied {
		t.Fatalf("%+v %v", res, err)
	}
	for _, c := range res.Changes {
		switch c.Kind {
		case MailRecordSPF:
			if c.Action != ChangeSkip || len(c.Old) != 2 || !strings.Contains(c.Note, "several") {
				t.Fatalf("spf %+v", c)
			}
		case MailRecordDMARC:
			if c.Action != ChangeSkip || !strings.Contains(c.Note, "CNAME") {
				t.Fatalf("dmarc %+v", c)
			}
		}
	}
	if v := b.get("bobzone.org", "@", "TXT"); len(v) != 3 {
		t.Fatalf("TXT changed: %v", v)
	}

	// A refused write is reported without the token and noted on the
	// connection, which then cannot apply.
	w2 := newRecordsWorld(t)
	w2.bobDNS.mu.Lock()
	w2.bobDNS.fail = fmt.Errorf("%w: token %s", dns.ErrForbidden, bobToken)
	w2.bobDNS.mu.Unlock()
	_, err = w2.svc.SetUpMailRecords(ctx, bob, "fresh.org", true)
	wantKind(t, err, Conflict)
	if strings.Contains(err.Error(), bobToken) {
		t.Fatalf("token in error: %v", err)
	}
	c, _ := w2.svc.store.DNSConnection(ctx, w2.bobConn)
	if c.CanWrite == nil || *c.CanWrite {
		t.Fatalf("refused write not noted: %+v", c)
	}
	r, _ := w2.svc.MailRecords(ctx, bob, "fresh.org")
	if r.CanApply || !strings.Contains(r.ApplyNote, "may only read") {
		t.Fatalf("after a refused write %+v", r)
	}
}

func TestSetUpMailRecordsPermissions(t *testing.T) {
	w := newRecordsWorld(t)
	ctx := context.Background()
	try := func(a Actor, domain string, want Kind) {
		t.Helper()
		_, err := w.svc.SetUpMailRecords(ctx, a, domain, true)
		if want == ok {
			if err != nil {
				t.Fatalf("%s %s: %v", a.Name, domain, err)
			}
			return
		}
		wantKind(t, err, want)
	}
	try(bobRead, "bobzone.org", Forbidden)
	try(bobLimited, "bobzone.org", Forbidden)
	try(nobody, "bobzone.org", Unauthenticated)
	try(System, "bobzone.org", Forbidden)
	// Admins see bob's domain, but never write into bob's connection.
	try(admin, "bobzone.org", Conflict)
	try(rootWriteTok, "bobzone.org", Conflict)
	try(rootRead, "bobzone.org", Forbidden)
	// The admin's own domain is not in the admin's connection either.
	try(admin, "rootsite.example.com", Conflict)
	// bob cannot see root's mail domain.
	try(bob, "rootsite.example.com", NotFound)
	if w.bobDNS.writes() != 0 || w.rootDNS.writes() != 0 {
		t.Fatal("something was written")
	}
	try(bob, "bobzone.org", ok)
	if w.bobDNS.writes() == 0 {
		t.Fatal("nothing written")
	}
	// A duplicate connection for the zone makes it unclear which to use.
	if _, err := w.svc.CreateDNSConnection(ctx, bob, "hetzner", "bob-second", bobToken); err != nil {
		t.Fatal(err)
	}
	try(bob, "fresh.org", Conflict)
	if r, _ := w.svc.MailRecords(ctx, bob, "fresh.org"); r.CanApply || !strings.Contains(r.ApplyNote, "more than one") {
		t.Fatalf("%+v", r)
	}
}
