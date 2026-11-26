package dav

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// TestHideFromDirectory guards the hook for G3: the directory looks up a
// bool field HideFromDirectory on aliases and mailboxes by name. When
// config.Alias or config.Mailbox has such a field, setting it must keep
// the entry out of the directory; this fails if the field is renamed in
// a way the lookup misses, or ignored.
func TestHideFromDirectory(t *testing.T) {
	type withField struct{ HideFromDirectory bool }
	if !hidden(&withField{true}) || hidden(&withField{false}) || hidden(&struct{ HideFromDirectory string }{"yes"}) {
		t.Fatal("hidden() does not read the field")
	}
	e := newEnv(t)
	checked := 0
	set := func(v reflect.Value) bool {
		f := v.FieldByName("HideFromDirectory")
		if !f.IsValid() || f.Kind() != reflect.Bool {
			return false
		}
		f.SetBool(true)
		return true
	}
	if set(reflect.ValueOf(&e.cfg.Aliases[1]).Elem()) { // team@example.org
		checked++
	}
	if set(reflect.ValueOf(&e.cfg.Mailboxes[2]).Elem()) { // carol@example.de
		checked++
	}
	if checked == 0 {
		t.Skip("config has no HideFromDirectory field yet (comes with G3)")
	}
	if err := e.srv.Load(context.Background(), e.cfg); err != nil {
		t.Fatal(err)
	}
	r := e.as("REPORT", "/dav/addressbooks/alice@example.org/directory/", map[string]string{"Depth": "1"},
		`<card:addressbook-query xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav"><d:prop><card:address-data/></d:prop></card:addressbook-query>`)
	expect(t, r, http.StatusMultiStatus, "alice@example.org")
	if v := reflect.ValueOf(e.cfg.Aliases[1]).FieldByName("HideFromDirectory"); v.IsValid() && strings.Contains(r.body, aliasAll) {
		t.Error("hidden alias listed")
	}
	if v := reflect.ValueOf(e.cfg.Mailboxes[2]).FieldByName("HideFromDirectory"); v.IsValid() && strings.Contains(r.body, carol) {
		t.Error("hidden mailbox listed")
	}
}
