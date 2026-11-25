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
