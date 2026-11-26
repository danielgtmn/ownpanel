package mcp

import (
	"reflect"
	"testing"
)

func TestDomainTools(t *testing.T) {
	e := newEnv(t, "")
	w := e.connect(t, e.write)
	var site siteOutput
	ok(t, call_(t, w, "create_site", map[string]any{"name": "a", "domains": []string{"a.elsewhere.org"}}), &site)

	refused(t, call_(t, w, "add_domain", map[string]any{"domain": "www.example.com"}), "not a domain you can register")
	var added ownDomainOutput
	for range 2 {
		ok(t, call_(t, w, "add_domain", map[string]any{"domain": "Elsewhere.ORG"}), &added)
	}
	if added.Domain.Domain != "elsewhere.org" || added.Domain.CreatedAt == "" ||
		!reflect.DeepEqual(added.Domain.UsedBy, []string{"the site a (a.elsewhere.org)"}) {
		t.Fatalf("added: %+v", added)
	}

	r := e.connect(t, e.read)
	var list ownDomainsOutput
	ok(t, call_(t, r, "list_domains", nil), &list)
	if len(list.Domains) != 1 || list.Domains[0].Domain != "elsewhere.org" {
		t.Fatalf("list: %+v", list)
	}
	refused(t, call_(t, r, "add_domain", map[string]any{"domain": "other.org"}), "write access")
	refused(t, call_(t, r, "remove_domain", map[string]any{"domain": "elsewhere.org"}), "write access")
	l := e.connect(t, e.token(t, "write", "only-a", []string{"a"}))
	refused(t, call_(t, l, "list_domains", nil), "limited to certain sites")

	var removed removedDomainOutput
	ok(t, call_(t, w, "remove_domain", map[string]any{"domain": "elsewhere.org"}), &removed)
	if removed.Removed != "elsewhere.org" || !reflect.DeepEqual(removed.StillUsedBy, []string{"the site a (a.elsewhere.org)"}) {
		t.Fatalf("removed: %+v", removed)
	}
	refused(t, call_(t, w, "remove_domain", map[string]any{"domain": "elsewhere.org"}), "not in your panel")
}
