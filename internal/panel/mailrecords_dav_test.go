package panel

import (
	"context"
	"slices"
	"testing"
)

// TestDAVRecords: with calendars and contacts on the mail host, the
// records that let their programs find the server come with the others.
func TestDAVRecords(t *testing.T) {
	w := newRecordsWorld(t)
	ctx := context.Background()
	w.svc.opts.MailWebSocket, w.svc.opts.PanelUpstream = "/run/ownpanel-mail-web/web.sock", "127.0.0.1:8600"
	if p := w.svc.selfServiceProblem(); p != "" {
		t.Fatal(p)
	}
	res, err := w.svc.SetUpMailRecords(ctx, bob, "fresh.org", false)
	if err != nil || !res.Applied {
		t.Fatalf("%+v %v", res, err)
	}
	b := w.bobDNS
	for _, name := range []string{"_caldavs._tcp", "_carddavs._tcp"} {
		if v := b.get("fresh.org", name, "SRV"); !slices.Equal(v, []string{"0 1 443 mail.host.test."}) {
			t.Fatalf("SRV %s %v", name, v)
		}
		if v := b.get("fresh.org", name, "TXT"); !slices.Equal(v, []string{"path=/dav/"}) {
			t.Fatalf("TXT %s %v", name, v)
		}
	}
	r, err := w.svc.MailRecords(ctx, bob, "fresh.org")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{MailRecordSRVCalDAV, MailRecordTXTCalDAV, MailRecordSRVCardDAV, MailRecordTXTCardDAV} {
		if x := recordOf(t, r, kind); x.Status != RecordOK || !x.Optional {
			t.Fatalf("%s %+v", kind, x)
		}
	}
	// Another path is replaced.
	b.put("fresh.org", "_caldavs._tcp", "TXT", "path=/caldav/")
	r, _ = w.svc.MailRecords(ctx, bob, "fresh.org")
	if x := recordOf(t, r, MailRecordTXTCalDAV); x.Status != RecordDifferent {
		t.Fatalf("%+v", x)
	}
	// Programs get the address too.
	if c := w.svc.clientSettings(testMailHost); c.DAVURL != "https://mail.host.test/dav/" {
		t.Fatal(c.DAVURL)
	}
}
