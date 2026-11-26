package mcp

import (
	"context"
	"strings"
	"testing"
)

// The tools of redirects.go and their annotations (see TestToolsAndAnnotations).
var _ = expectTools(map[string]toolWant{
	"list_redirects":  {readOnly: true, idempotent: true},
	"set_redirect":    {destructive: true},
	"delete_redirect": {destructive: true},
})

func TestRedirectTools(t *testing.T) {
	e := newEnv(t, "panel.example.com")
	ctx := context.Background()
	if _, err := e.svc.SaveSite(ctx, e.admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	cs := e.connect(t, e.write)

	var empty redirectsOutput
	ok(t, call_(t, cs, "list_redirects", nil), &empty)
	if empty.Redirects == nil || len(empty.Redirects) != 0 {
		t.Fatalf("empty list: %+v", empty)
	}

	var dom redirectOutput
	ok(t, call_(t, cs, "set_redirect", map[string]any{
		"source_domain": "www.blog.example.com", "target": "https://blog.example.com", "keep_path": true, "keep_query": true,
	}), &dom)
	r := dom.Redirect
	if r.ID == "" || r.Owner != "root" || r.Code != 301 || r.SourcePath != "" || !r.KeepPath || !r.KeepQuery || r.CreatedAt == "" {
		t.Fatalf("created %+v", r)
	}
	var path redirectOutput
	ok(t, call_(t, cs, "set_redirect", map[string]any{
		"source_domain": "blog.example.com", "source_path": "/old/*", "target": "/new", "code": 308, "keep_path": true,
	}), &path)

	// Update by id.
	var upd redirectOutput
	ok(t, call_(t, cs, "set_redirect", map[string]any{
		"id": r.ID, "source_domain": "www.blog.example.com", "target": "https://blog.example.com", "code": 302,
	}), &upd)
	if upd.Redirect.ID != r.ID || upd.Redirect.Code != 302 || upd.Redirect.KeepPath {
		t.Fatalf("updated %+v", upd.Redirect)
	}

	var all redirectsOutput
	ok(t, call_(t, cs, "list_redirects", nil), &all)
	if len(all.Redirects) != 2 || all.Redirects[0].SourcePath != "/old/*" {
		t.Fatalf("list %+v", all)
	}
	var site redirectsOutput
	ok(t, call_(t, cs, "list_redirects", map[string]any{"site": "blog"}), &site)
	if len(site.Redirects) != 1 || site.Redirects[0].ID != path.Redirect.ID {
		t.Fatalf("site list %+v", site)
	}

	// Refusals read like sentences.
	refused(t, call_(t, cs, "set_redirect", map[string]any{"source_domain": "panel.example.com", "target": "https://example.com"}), "address of the panel itself")
	refused(t, call_(t, cs, "set_redirect", map[string]any{"source_domain": "x.example.com", "target": "https://x.example.com/"}), "redirect again forever")
	refused(t, call_(t, cs, "set_redirect", map[string]any{"source_domain": "x.example.com", "target": "https://example.com", "code": 303}), "301, 302, 307 or 308")
	refused(t, call_(t, cs, "set_redirect", map[string]any{"id": "9999", "source_domain": "x.example.com", "target": "https://example.com"}), "not found")
	refused(t, call_(t, e.connect(t, e.read), "set_redirect", map[string]any{"source_domain": "x.example.com", "target": "https://example.com"}), "can only read")

	// delete_redirect needs the source repeated.
	refused(t, call_(t, cs, "delete_redirect", map[string]any{"id": path.Redirect.ID, "confirm_source": "blog.example.com"}), "does not match")
	refused(t, call_(t, cs, "delete_redirect", map[string]any{"id": "9999", "confirm_source": "x"}), "not found")
	var del deleteRedirectOutput
	ok(t, call_(t, cs, "delete_redirect", map[string]any{"id": path.Redirect.ID, "confirm_source": "blog.example.com/old/*"}), &del)
	if del.Deleted != "blog.example.com/old/*" {
		t.Fatalf("deleted %+v", del)
	}
	ok(t, call_(t, cs, "delete_redirect", map[string]any{"id": r.ID, "confirm_source": "www.blog.example.com"}), &del)
	ok(t, call_(t, cs, "list_redirects", nil), &all)
	if len(all.Redirects) != 0 {
		t.Fatalf("after delete %+v", all)
	}

	// A token limited to a site manages only its path redirects.
	limited := e.connect(t, e.token(t, "write", "blog-only", []string{"blog"}))
	refused(t, call_(t, limited, "set_redirect", map[string]any{"source_domain": "x.example.com", "target": "https://example.com"}), "domain redirects")
	var lp redirectOutput
	ok(t, call_(t, limited, "set_redirect", map[string]any{"source_domain": "blog.example.com", "source_path": "/a", "target": "/b"}), &lp)
	if !strings.HasPrefix(lp.Redirect.CreatedAt, "20") {
		t.Fatalf("created_at %q", lp.Redirect.CreatedAt)
	}
}
