package mailsetup

import (
	"strings"
	"testing"
)

func TestProfileHelpers(t *testing.T) {
	if got := ProfileFileName(`a+b/c"d@example.com`); got != "a-b-c-d@example.com.mobileconfig" {
		t.Fatal(got)
	}
	u := profileUUID("account", "a@example.com", "mail.example.net")
	if len(u) != 36 || u[14] != '5' || u != profileUUID("account", "a@example.com", "mail.example.net") ||
		u == profileUUID("profile", "a@example.com", "mail.example.net") {
		t.Fatal(u)
	}
	b := AppleProfile("a&b@example.com", ClientSettings{Server: "m<x>"})
	if strings.Contains(string(b), "a&b") || strings.Contains(string(b), "m<x>") {
		t.Fatalf("not escaped: %s", b)
	}
	if s := Settings("mail.example.net"); s.IMAPPort != 993 || s.SMTPPort != 465 || s.SMTPStartTLS != 587 || !s.UserNameIsEmail {
		t.Fatalf("%+v", s)
	}
}

func TestProfileWithCalendars(t *testing.T) {
	plain := string(AppleProfile("a@example.com", Settings("mail.example.net")))
	if strings.Contains(plain, "caldav") {
		t.Fatal("calendars without DAV")
	}
	c := Settings("mail.example.net").WithDAV()
	if c.DAVURL != "https://mail.example.net/dav/" {
		t.Fatal(c.DAVURL)
	}
	b := string(AppleProfile("a+b@example.com", c))
	for _, want := range []string{"com.apple.caldav.account", "com.apple.carddav.account", "<key>CalDAVHostName</key><string>mail.example.net</string>",
		"<key>CardDAVPrincipalURL</key><string>https://mail.example.net/dav/principals/a+b@example.com/</string>", "<key>CalDAVPort</key><integer>443</integer>"} {
		if !strings.Contains(b, want) {
			t.Fatalf("profile lacks %s:\n%s", want, b)
		}
	}
	if strings.Contains(b, "Password</key><string>") {
		t.Fatal("password in profile")
	}
}
