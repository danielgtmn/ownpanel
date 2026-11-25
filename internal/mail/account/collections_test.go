package account

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCollectionsImportExport(t *testing.T) {
	e := newEnv(t)
	cookie := e.signIn(address, password)
	rec := e.do(req{method: "GET", path: "/account/api/collections", cookie: cookie})
	want(t, rec, 200, `"name":"calendar"`)
	var list struct {
		Collections []collectionView `json:"collections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Collections) != 2 {
		t.Fatalf("%s %v", rec.Body, err)
	}
	me, _ := e.me(cookie)
	if me.Client.DAVURL != "https://"+host+"/dav/" {
		t.Fatalf("client %+v", me.Client)
	}

	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:e1\r\nDTSTAMP:20240101T000000Z\r\nDTSTART:20240101T090000Z\r\nSUMMARY:Hello\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	imp := func(path, body string) string {
		return e.do(req{method: "POST", path: path, cookie: cookie, body: body, header: map[string]string{"Content-Type": "text/calendar"}}).Body.String()
	}
	if got := imp("/account/api/collections/calendar/calendar/import", ics); !strings.Contains(got, `"added":1`) {
		t.Fatal(got)
	}
	if got := imp("/account/api/collections/calendar/calendar/import", ics); !strings.Contains(got, `"replaced":1`) {
		t.Fatal(got)
	}
	want(t, e.do(req{method: "POST", path: "/account/api/collections/calendar/calendar/import", cookie: cookie, body: "nonsense"}), 400, "can't be read as a calendar")
	want(t, e.do(req{method: "POST", path: "/account/api/collections/addressbook/contacts/import", cookie: cookie, body: "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Zed\r\nEND:VCARD\r\n"}), 200, `"added":1`)
	want(t, e.do(req{method: "POST", path: "/account/api/collections/calendar/nope/import", cookie: cookie, body: ics}), 404, "")
	// Without the header that marks the app's own requests it is refused.
	want(t, e.do(req{method: "POST", path: "/account/api/collections/calendar/calendar/import", cookie: cookie, body: ics, header: map[string]string{CSRFHeader: ""}}), 403, "")
	want(t, e.do(req{method: "POST", path: "/account/api/collections/calendar/calendar/import", body: ics}), 401, "")

	rec = e.do(req{method: "GET", path: "/account/api/collections/calendar/calendar/export", cookie: cookie})
	want(t, rec, 200, "SUMMARY:Hello")
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="Calendar.ics"`) {
		t.Fatal(cd)
	}
	rec = e.do(req{method: "GET", path: "/account/api/collections/addressbook/contacts/export", cookie: cookie})
	want(t, rec, 200, "FN:Zed")

	// The setup for Apple devices sets up calendars and contacts too.
	rec = e.do(req{method: "GET", path: "/account/api/apple-setup", cookie: cookie})
	want(t, rec, 200, "com.apple.caldav.account")
}
