package panel

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

func boolp(b bool) *bool           { return &b }
func floatp(f float64) *float64    { return &f }
func zonesp(z ...string) *[]string { return &z }

const dqsZone = "abcdef0123456789abcdef0123.zen.dq.spamhaus.net"

func TestSpamCheckSettingsArePushed(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()
	// Defaults: nothing set, the service uses its defaults.
	if f := w.mail.last().Filter; f.Blocklists != nil || f.JunkScore != 0 || f.Greylisting {
		t.Fatalf("default filter %+v", f)
	}
	v, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{
		MailBlocklists: zonesp("bl.example.org", strings.ToUpper(dqsZone)+"."), MailJunkScore: floatp(8), MailGreylisting: boolp(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.MailBlocklistsDefault || !slices.Equal(v.MailBlocklists, []string{"bl.example.org", "••••.zen.dq.spamhaus.net"}) || v.MailJunkScore != 8 || !v.MailGreylisting {
		t.Fatalf("view %+v", v)
	}
	f := w.mail.last().Filter
	if !slices.Equal(f.Blocklists, []string{"bl.example.org", dqsZone}) || f.JunkScore != 8 || !f.Greylisting {
		t.Fatalf("pushed %+v", f)
	}
	// The key is stored sealed and stays out of the activity log.
	m, _ := svc.store.Settings(ctx)
	if strings.Contains(m[keyMailBlocklists], "abcdef") || m[keyMailJunkScore] != "8" || m[keyMailGreylisting] != "true" {
		t.Fatalf("stored %q", m)
	}
	acts, _ := svc.store.Activities(ctx, 10)
	if acts[0].Action != "settings.update" || strings.Contains(acts[0].Detail, "abcdef") || !strings.Contains(acts[0].Detail, "••••.zen.dq.spamhaus.net") {
		t.Fatalf("activity %+v", acts[0])
	}

	// Sending the mask back keeps the key; a reload reads it again.
	loads := w.mail.loadCount()
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklists: &v.MailBlocklists}); err != nil {
		t.Fatal(err)
	}
	// The same key in full is no change either.
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklists: zonesp("bl.example.org", dqsZone)}); err != nil {
		t.Fatal(err)
	}
	if w.mail.loadCount() != loads {
		t.Fatal("unchanged blocklists were pushed again")
	}
	if err := svc.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if f, _ := svc.mailFilter(); !slices.Equal(f.Blocklists, []string{"bl.example.org", dqsZone}) || f.JunkScore != 8 {
		t.Fatalf("after reload %+v", f)
	}
	// Reordered with the mask, then a mask without a stored key.
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklists: zonesp("••••.zen.dq.spamhaus.net")}); err != nil {
		t.Fatal(err)
	}
	if f := w.mail.last().Filter; !slices.Equal(f.Blocklists, []string{dqsZone}) {
		t.Fatalf("mask kept %+v", f)
	}
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklists: zonesp("••••.sbl.dq.spamhaus.net")}); KindOf(err) != Invalid {
		t.Fatalf("unknown mask: %v", err)
	}
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklists: zonesp("••••.zen.dq.spamhaus.net", dqsZone)}); KindOf(err) != Invalid {
		t.Fatalf("mask and same key: %v", err)
	}

	// Off, then the default again; false alone keeps the zones in effect.
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklists: zonesp()}); err != nil {
		t.Fatal(err)
	}
	if f := w.mail.last().Filter; f.Blocklists == nil || len(f.Blocklists) != 0 {
		t.Fatalf("off %+v", f)
	}
	if err := svc.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := svc.Settings(ctx, admin); v.MailBlocklistsDefault || len(v.MailBlocklists) != 0 {
		t.Fatalf("off after reload %+v", v)
	}
	v, err = svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklistsDefault: boolp(true), MailJunkScore: floatp(5), MailGreylisting: boolp(false)})
	if err != nil || !v.MailBlocklistsDefault || v.MailJunkScore != 5 {
		t.Fatalf("default %+v %v", v, err)
	}
	if f := w.mail.last().Filter; f.Blocklists != nil || f.JunkScore != 0 || f.Greylisting {
		t.Fatalf("default pushed %+v", f)
	}
	if v, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklistsDefault: boolp(false)}); err != nil || v.MailBlocklistsDefault || !slices.Equal(v.MailBlocklists, config.DefaultBlocklists) {
		t.Fatalf("own copy of the default %+v %v", v, err)
	}
	if m, _ := svc.store.Settings(ctx); m[keyMailJunkScore] != "" || m[keyMailGreylisting] != "" {
		t.Fatalf("defaults stored %q", m)
	}
}

func TestSpamCheckSettingsValidation(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()
	many := make([]string, config.MaxBlocklists+1)
	for i := range many {
		many[i] = string(rune('a'+i)) + ".example"
	}
	for _, u := range []SettingsUpdate{
		{MailBlocklists: zonesp("bl.example.org", "BL.example.org")},
		{MailBlocklists: zonesp("no_underscores.example")},
		{MailBlocklists: zonesp("localhost")},
		{MailBlocklists: zonesp("127.0.0.1 zen.spamhaus.org")},
		{MailBlocklists: &many},
		{MailBlocklists: zonesp("a.example"), MailBlocklistsDefault: boolp(true)},
		{MailJunkScore: floatp(0)},
		{MailJunkScore: floatp(50.5)},
	} {
		if _, err := svc.UpdateSettings(ctx, admin, u); KindOf(err) != Invalid {
			t.Errorf("%+v: %v", u, err)
		}
	}
	// A secret zone in an error message is masked.
	_, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklists: zonesp(dqsZone, dqsZone)})
	if KindOf(err) != Invalid || strings.Contains(err.Error(), "abcdef") {
		t.Fatalf("twice: %v", err)
	}
	// Admins only; read-only tokens may read.
	for _, a := range []Actor{bob, rootRead, rootLimited} {
		if _, err := svc.UpdateSettings(ctx, a, SettingsUpdate{MailGreylisting: boolp(true)}); err == nil {
			t.Errorf("%+v changed the settings", a)
		}
	}
	if _, err := svc.Settings(ctx, rootRead); err != nil {
		t.Fatal(err)
	}
	// Without a secret key, a zone with a key cannot be stored.
	svc.secrets = nil
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklists: zonesp(dqsZone)}); KindOf(err) != Conflict {
		t.Fatalf("no box: %v", err)
	}
}

func TestBlocklistRefusalsInHealthAndActivity(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklists: zonesp("zen.spamhaus.org", dqsZone)}); err != nil {
		t.Fatal(err)
	}
	h, err := svc.MailHealth(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if c := healthCheck(t, h, HealthBlocklists); c.State != HealthOK || len(c.Found) != 0 || len(c.Expected) != 2 {
		t.Fatalf("no refusals %+v", c)
	}
	w.mail.set(func(f *fakeMail) {
		f.events = []config.Event{
			{ID: 1, At: w.now.Add(-time.Hour), Kind: config.EventBlocklistRefused,
				Detail: "the blocklist " + dqsZone + " refused to answer this server's queries (usually because the server uses a public DNS resolver); incoming mail is not checked against it"},
			{ID: 2, Kind: config.EventNotForwarded, Address: "sales@bobsite.example.com", Detail: "a message to sales@bobsite.example.com was not forwarded to x@example.net: junk"},
		}
	})
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	acts, _ := svc.store.Activities(ctx, 10)
	for _, a := range acts {
		if strings.Contains(a.Target+a.Detail, "abcdef") {
			t.Fatalf("key in the activity log: %+v", a)
		}
	}
	if acts[1].Action != "mail.blocklist.refused" || acts[1].Target != "••••.zen.dq.spamhaus.net" || acts[0].Action != "mail.delivery.not-forwarded" {
		t.Fatalf("activity %+v", acts[:2])
	}
	h, _ = svc.MailHealth(ctx, admin)
	c := healthCheck(t, h, HealthBlocklists)
	if c.State != HealthWarning || !slices.Equal(c.Found, []string{"••••.zen.dq.spamhaus.net"}) || !strings.Contains(c.Detail, "own resolver") {
		t.Fatalf("refused %+v", c)
	}
	// Gone after 7 days, and not shown for a zone no longer used.
	w.now = w.now.Add(7*24*time.Hour - 2*time.Hour)
	if h, _ := svc.MailHealth(ctx, admin); healthCheck(t, h, HealthBlocklists).State != HealthWarning {
		t.Fatal("still within 7 days")
	}
	w.now = w.now.Add(2 * time.Hour)
	if h, _ := svc.MailHealth(ctx, admin); healthCheck(t, h, HealthBlocklists).State != HealthOK {
		t.Fatal("older than 7 days")
	}
	w.now = w.now.Add(-7 * 24 * time.Hour)
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklistsDefault: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if h, _ := svc.MailHealth(ctx, admin); healthCheck(t, h, HealthBlocklists).State != HealthOK {
		t.Fatal("zone no longer used")
	}
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailBlocklists: zonesp()}); err != nil {
		t.Fatal(err)
	}
	if c := healthCheck(t, must(svc.MailHealth(ctx, admin)), HealthBlocklists); c.State != HealthOK || !strings.Contains(c.Detail, "turned off") {
		t.Fatalf("off %+v", c)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestMaskSecretZones(t *testing.T) {
	for in, want := range map[string]string{
		"the blocklist KEY123.zen.dq.spamhaus.net refused": "the blocklist ••••.zen.dq.spamhaus.net refused",
		"zen.spamhaus.org refused":                         "zen.spamhaus.org refused",
		"a.sbl.dq.spamhaus.net and b.xbl.dq.spamhaus.net":  "••••.sbl.dq.spamhaus.net and ••••.xbl.dq.spamhaus.net",
	} {
		if got := maskSecretZones(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	if maskZone("dq.spamhaus.net") != "dq.spamhaus.net" || maskZone("k.zen.dq.spamhaus.net") != "••••.zen.dq.spamhaus.net" {
		t.Fatal("maskZone")
	}
}
