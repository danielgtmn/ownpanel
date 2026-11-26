package panel

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner/hetznertest"
)

func buildTemplate(t *testing.T, id, zone string, params map[string]string) []templateRecord {
	t.Helper()
	tpl, err := dnsTemplate(id)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := tpl.build(zone, params)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func changeOf(t *testing.T, changes []DNSTemplateChange, name, typ string) DNSTemplateChange {
	t.Helper()
	for _, c := range changes {
		if c.Name == name && c.Type == typ {
			return c
		}
	}
	t.Fatalf("no change for %s %s in %+v", name, typ, changes)
	return DNSTemplateChange{}
}

func TestPlanTemplateFreshZone(t *testing.T) {
	recs := buildTemplate(t, "google-workspace", "example.com", nil)
	changes, writes := planTemplate(recs, nil)
	if len(changes) != 2 || len(writes) != 2 {
		t.Fatalf("%+v", changes)
	}
	mx := changeOf(t, changes, "@", "MX")
	if mx.Action != TemplateAdd || !reflect.DeepEqual(mx.New, []string{"1 smtp.google.com."}) || len(mx.Current) != 0 {
		t.Fatalf("%+v", mx)
	}
	spf := changeOf(t, changes, "@", "TXT")
	if spf.Action != TemplateAdd || !reflect.DeepEqual(spf.New, []string{"v=spf1 include:_spf.google.com ~all"}) {
		t.Fatalf("%+v", spf)
	}
	if writes[0].TTL != 0 || !reflect.DeepEqual(writes[1].Values, spf.New) {
		t.Fatalf("%+v %+v", writes[0], writes[1])
	}
}

func TestPlanTemplateKeepsOtherTXTValues(t *testing.T) {
	sets := []dns.RecordSet{{Name: "@", Type: "TXT", TTL: 600, Values: []string{"google-site-verification=abc", "something else"}}}
	recs := buildTemplate(t, "domain-verification", "example.com", map[string]string{"value": "ms=12345"})
	changes, writes := planTemplate(recs, sets)
	c := changes[0]
	if c.Action != TemplateChange || !reflect.DeepEqual(c.Current, sets[0].Values) ||
		!reflect.DeepEqual(c.New, []string{"google-site-verification=abc", "something else", "ms=12345"}) {
		t.Fatalf("%+v", c)
	}
	if writes[0].TTL != 600 || !reflect.DeepEqual(writes[0].Values, c.New) {
		t.Fatalf("%+v", writes[0])
	}

	// SPF without an existing SPF value: added next to the others.
	recs = buildTemplate(t, "microsoft-365", "example.com", map[string]string{"mx_host": "example-com.mail.protection.outlook.com"})
	changes, _ = planTemplate(recs, sets)
	spf := changeOf(t, changes, "@", "TXT")
	if spf.Action != TemplateChange || !reflect.DeepEqual(spf.New, []string{"google-site-verification=abc", "something else", "v=spf1 include:spf.protection.outlook.com -all"}) {
		t.Fatalf("%+v", spf)
	}
}

func TestPlanTemplateMergesExistingSPF(t *testing.T) {
	sets := []dns.RecordSet{{Name: "@", Type: "TXT", Values: []string{"v=spf1 mx include:_spf.example.net -all", "verify=1"}}}
	recs := buildTemplate(t, "google-workspace", "example.com", nil)
	changes, writes := planTemplate(recs, sets)
	spf := changeOf(t, changes, "@", "TXT")
	want := []string{"v=spf1 include:_spf.google.com mx include:_spf.example.net -all", "verify=1"}
	if spf.Action != TemplateChange || !reflect.DeepEqual(spf.New, want) || !strings.Contains(spf.Note, "stay allowed") {
		t.Fatalf("%+v", spf)
	}
	if !reflect.DeepEqual(writes[1].Values, want) {
		t.Fatalf("%+v", writes[1])
	}
	n := 0
	for _, v := range writes[1].Values {
		if isSPF(v) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d SPF values in %v", n, writes[1].Values)
	}

	// Already included: nothing to do.
	sets[0].Values = []string{"v=spf1 +include:_spf.google.com ~all"}
	changes, writes = planTemplate(recs, sets)
	if c := changeOf(t, changes, "@", "TXT"); c.Action != TemplateOK || writes[1] != nil {
		t.Fatalf("%+v", c)
	}

	// Two SPF values, an excluded provider, too many lookups: reported, never written.
	for _, vals := range [][]string{
		{"v=spf1 mx -all", "v=spf1 a -all"},
		{"v=spf1 -include:_spf.google.com mx -all"},
		{"v=spf1 include:a.example include:b.example include:c.example include:d.example include:e.example include:f.example include:g.example include:h.example include:i.example include:j.example -all"},
	} {
		sets[0].Values = vals
		changes, writes = planTemplate(recs, sets)
		c := changeOf(t, changes, "@", "TXT")
		if c.Action != TemplateConflict || writes[1] != nil || c.Note == "" || !reflect.DeepEqual(c.New, c.Current) {
			t.Fatalf("%v: %+v", vals, c)
		}
	}
}

func TestPlanTemplateReplacesMXVisibly(t *testing.T) {
	sets := []dns.RecordSet{{Name: "@", Type: "MX", TTL: 3600, Values: []string{"10 mx1.old.example.", "20 MX2.old.example"}}}
	recs := buildTemplate(t, "fastmail", "example.com", nil)
	changes, writes := planTemplate(recs, sets)
	mx := changeOf(t, changes, "@", "MX")
	if mx.Action != TemplateChange || !reflect.DeepEqual(mx.Current, []string{"10 mx1.old.example.", "20 mx2.old.example."}) ||
		!reflect.DeepEqual(mx.New, []string{"10 in1-smtp.messagingengine.com.", "20 in2-smtp.messagingengine.com."}) || mx.Note == "" {
		t.Fatalf("%+v", mx)
	}
	if writes[0].TTL != 3600 {
		t.Fatalf("%+v", writes[0])
	}
	dkim := changeOf(t, changes, "fm2._domainkey", "CNAME")
	if dkim.Action != TemplateAdd || !reflect.DeepEqual(dkim.New, []string{"fm2.example.com.dkim.fmhosted.com."}) {
		t.Fatalf("%+v", dkim)
	}
}

func TestPlanTemplateCNAMEConflicts(t *testing.T) {
	recs := buildTemplate(t, "microsoft-365", "example.com", map[string]string{"mx_host": "example-com.mail.protection.outlook.com"})
	// Other records where the template wants an alias.
	sets := []dns.RecordSet{{Name: "autodiscover", Type: "A", Values: []string{"198.51.100.1"}}}
	changes, writes := planTemplate(recs, sets)
	c := changeOf(t, changes, "autodiscover", "CNAME")
	if c.Action != TemplateConflict || writes[2] != nil || !strings.Contains(c.Note, "A") {
		t.Fatalf("%+v", c)
	}
	// An alias where the template wants other records.
	recs = buildTemplate(t, "domain-verification", "example.com", map[string]string{"value": "x=1", "name": "www"})
	sets = []dns.RecordSet{{Name: "www", Type: "CNAME", Values: []string{"example.com."}}}
	changes, writes = planTemplate(recs, sets)
	if changes[0].Action != TemplateConflict || writes[0] != nil || !reflect.DeepEqual(changes[0].Current, []string{"example.com."}) {
		t.Fatalf("%+v", changes[0])
	}
	// An alias elsewhere is replaced, visibly.
	recs = buildTemplate(t, "microsoft-365", "example.com", map[string]string{"mx_host": "example-com.mail.protection.outlook.com"})
	sets = []dns.RecordSet{{Name: "autodiscover", Type: "CNAME", Values: []string{"mail.example.com."}}}
	changes, _ = planTemplate(recs, sets)
	if c := changeOf(t, changes, "autodiscover", "CNAME"); c.Action != TemplateChange || !reflect.DeepEqual(c.Current, []string{"mail.example.com."}) {
		t.Fatalf("%+v", c)
	}
}

func TestTemplateParams(t *testing.T) {
	for _, tc := range []struct {
		id     string
		params map[string]string
	}{
		{"domain-verification", nil},
		{"domain-verification", map[string]string{"value": "a\nb"}},
		{"domain-verification", map[string]string{"value": "x", "name": "bad name"}},
		{"domain-verification", map[string]string{"value": "x", "name": "www.other.org."}},
		{"microsoft-365", nil},
		{"microsoft-365", map[string]string{"mx_host": "not a host"}},
	} {
		tpl, _ := dnsTemplate(tc.id)
		if _, err := tpl.build("example.com", tc.params); KindOf(err) != Invalid {
			t.Errorf("%s %v: %v", tc.id, tc.params, err)
		}
	}
	if _, err := dnsTemplate("nope"); KindOf(err) != NotFound {
		t.Fatal(err)
	}
}

func TestApplyDNSTemplate(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	srv := d.hz[bobToken]
	srv.AddRRSet("example.com", hetznertest.RRSet{Name: "@", Type: "TXT", Records: []string{`"keep-me=1"`, `"v=spf1 mx -all"`}})
	srv.AddRRSet("example.com", hetznertest.RRSet{Name: "@", Type: "MX", TTL: 3600, Records: []string{"10 mail.example.com."}})
	srv.ResetCalls()

	// The preview writes nothing, also for a read-only token.
	for _, a := range []Actor{bob, bobRead} {
		res, err := d.svc.ApplyDNSTemplate(ctx, a, d.bobConn, "example.com", "google-workspace", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.Applied || len(res.Changes) != 2 || res.Changes[0].Action != TemplateChange || res.Changes[1].Action != TemplateChange {
			t.Fatalf("%+v", res)
		}
	}
	if w := srv.Writes(); len(w) != 0 {
		t.Fatalf("preview wrote %v", w)
	}
	_, err := d.svc.ApplyDNSTemplate(ctx, bobRead, d.bobConn, "example.com", "google-workspace", nil, true)
	wantKind(t, err, Forbidden)

	res, err := d.svc.ApplyDNSTemplate(ctx, bob, d.bobConn, "example.com", "google-workspace", nil, true)
	if err != nil || !res.Applied {
		t.Fatalf("%+v %v", res, err)
	}
	mx, _ := srv.RRSet("example.com", "@", "MX")
	txt, _ := srv.RRSet("example.com", "@", "TXT")
	if !reflect.DeepEqual(mx.Records, []string{"1 smtp.google.com."}) || mx.TTL != 3600 {
		t.Fatalf("MX %+v", mx)
	}
	if !reflect.DeepEqual(txt.Records, []string{`"keep-me=1"`, `"v=spf1 include:_spf.google.com mx -all"`}) {
		t.Fatalf("TXT %+v", txt)
	}

	// A second apply has nothing to do and writes nothing.
	srv.ResetCalls()
	res, err = d.svc.ApplyDNSTemplate(ctx, bob, d.bobConn, "example.com", "google-workspace", nil, true)
	if err != nil || !res.Applied {
		t.Fatalf("%+v %v", res, err)
	}
	for _, c := range res.Changes {
		if c.Action != TemplateOK {
			t.Fatalf("%+v", c)
		}
	}
	if w := srv.Writes(); len(w) != 0 {
		t.Fatalf("second apply wrote %v", w)
	}

	// A conflict refuses the whole template.
	srv.AddRRSet("example.com", hetznertest.RRSet{Name: "autodiscover", Type: "A", Records: []string{"198.51.100.1"}})
	srv.ResetCalls()
	res, err = d.svc.ApplyDNSTemplate(ctx, bob, d.bobConn, "example.com", "microsoft-365", map[string]string{"mx_host": "example-com.mail.protection.outlook.com"}, true)
	wantKind(t, err, Conflict)
	if res.Applied || len(srv.Writes()) != 0 {
		t.Fatalf("%+v %v", res, srv.Writes())
	}

	// E-mail on this server is set up elsewhere.
	_, err = d.svc.ApplyDNSTemplate(ctx, bob, d.bobConn, "example.com", "own-mail", nil, false)
	wantKind(t, err, Invalid)
	_, err = d.svc.ApplyDNSTemplate(ctx, bob, d.bobConn, "example.com", "nope", nil, false)
	wantKind(t, err, NotFound)

	acts, _ := d.svc.store.Activities(ctx, 10)
	var seen []string
	for _, a := range acts {
		if strings.HasPrefix(a.Action, "dns.") {
			seen = append(seen, a.Action)
		}
	}
	if len(seen) < 3 || seen[0] != "dns.template.apply" || seen[1] != "dns.record.set" || seen[2] != "dns.record.set" {
		t.Fatalf("activity %v", seen)
	}
}

func TestDNSTemplatePermissions(t *testing.T) {
	ctx := context.Background()
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", admin}, {"admin write token", rootWriteTok}, {"member", bob}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody},
	}
	F, N, U := Forbidden, NotFound, Unauthenticated
	apply := func(own, confirm bool, zone string) func(d *dnsWorld, a Actor) error {
		return func(d *dnsWorld, a Actor) error {
			id := d.ownConn(a)
			if !own {
				id = d.otherConn(a)
			}
			_, err := d.svc.ApplyDNSTemplate(ctx, a, id, zone, "domain-verification", map[string]string{"value": "x=1"}, confirm)
			return err
		}
	}
	ops := []struct {
		name string
		run  func(d *dnsWorld, a Actor) error
		want []Kind
	}{
		{"list templates", func(d *dnsWorld, a Actor) error { _, err := d.svc.DNSTemplates(ctx, a); return err },
			[]Kind{ok, ok, ok, ok, F, ok, F, U}},
		{"preview own", apply(true, false, "example.com"), []Kind{ok, ok, ok, ok, F, ok, F, U}},
		{"apply own", apply(true, true, "example.com"), []Kind{ok, ok, ok, F, F, F, F, U}},
		{"preview other's", apply(false, false, "example.com"), []Kind{F, F, N, N, F, F, F, U}},
		{"apply other's", apply(false, true, "example.com"), []Kind{F, F, N, F, F, F, F, U}},
		{"preview unknown zone", apply(true, false, "nowhere.example"), []Kind{N, N, N, N, F, N, F, U}},
	}
	for _, op := range ops {
		for i, ac := range actors {
			t.Run(op.name+"/"+ac.name, func(t *testing.T) {
				d := newDNSWorld(t)
				err := op.run(d, ac.a)
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

func TestDNSTemplateReadOnlyZone(t *testing.T) {
	d := newDNSWorld(t)
	d.hz[bobToken].AddZone("secondary.example", "secondary")
	d.now = d.now.Add(time.Hour)
	_, err := d.svc.ApplyDNSTemplate(context.Background(), bob, d.bobConn, "secondary.example", "google-workspace", nil, false)
	wantKind(t, err, Conflict)
}
