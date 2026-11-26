package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/api"
	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/store"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type fakeWeb struct{}

func (fakeWeb) Load(context.Context, any) error { return nil }

// fakeResolver knows a few names; every other name does not exist.
type fakeResolver map[string]string

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ip, ok := f[host]; ok {
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

type env struct {
	svc      *panel.Service
	srv      *httptest.Server
	sitesDir string
	admin    panel.Actor // signed-in admin, unrestricted
	write    string      // write token, all sites
	read     string      // read token
}

func newEnv(t *testing.T, panelDomain string, opts ...Option) *env {
	t.Helper()
	return newEnvWith(t, panelDomain, nil, opts...)
}

// newEnvWith is newEnv with a chance to change the panel's configuration.
func newEnvWith(t *testing.T, panelDomain string, change func(*panel.Config), opts ...Option) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sitesDir := filepath.Join(dir, "sites")
	cfg := panel.Config{
		Store: st, Deploys: &deploy.Manager{Base: sitesDir}, Web: fakeWeb{},
		Caddy:         caddy.Options{TLSMode: caddy.TLSOff, PanelDomain: panelDomain, PanelUpstream: "127.0.0.1:8600"},
		SetupCodeFile: filepath.Join(dir, "setup-code"),
		ServerIPs:     []netip.Addr{netip.MustParseAddr("192.0.2.10")},
		Resolver:      fakeResolver{"here.example.com": "192.0.2.10", "away.example.com": "198.51.100.7"},
	}
	if change != nil {
		change(&cfg)
	}
	svc, err := panel.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := svc.EnsureSetupCode(ctx); err != nil {
		t.Fatal(err)
	}
	code, _ := os.ReadFile(filepath.Join(dir, "setup-code"))
	_, session, err := svc.Setup(ctx, string(code), panel.Credentials{Name: "root", Password: "root-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := svc.SessionActor(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{svc: svc, sitesDir: sitesDir, admin: admin}
	e.write = e.token(t, "write", "all-write", nil)
	e.read = e.token(t, "read", "reader", nil)

	mux := http.NewServeMux()
	mux.Handle("/api/", (&api.Server{Panel: svc}).Handler())
	mux.Handle(Path, Handler(svc, "test", opts...))
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) token(t *testing.T, access, name string, sites []string) string {
	t.Helper()
	tok, err := e.svc.CreateToken(context.Background(), e.admin, name, access, sites, 0)
	if err != nil {
		t.Fatal(err)
	}
	return tok.Secret
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (e *env) connect(t *testing.T, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:             e.srv.URL + Path,
		HTTPClient:           &http.Client{Transport: bearer{token}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call runs a tool and returns its result; a failed call (not a tool error)
// fails the test.
func call_(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func text(res *sdk.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func ok(t *testing.T, res *sdk.CallToolResult, into any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", text(res))
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatal(err)
	}
}

func refused(t *testing.T, res *sdk.CallToolResult, want string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected a tool error containing %q, got success", want)
	}
	if res.StructuredContent != nil {
		t.Fatalf("a tool error must not carry structured output: %v", res.StructuredContent)
	}
	if !strings.Contains(strings.ToLower(text(res)), strings.ToLower(want)) {
		t.Fatalf("tool error %q does not mention %q", text(res), want)
	}
}

func index(body string) map[string]any {
	return map[string]any{"files": []any{map[string]any{"path": "index.html", "content": body}}}
}

func with(m map[string]any, k string, v any) map[string]any {
	m[k] = v
	return m
}

func readFile(t *testing.T, e *env, site, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.sitesDir, site, "current", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAuthentication(t *testing.T) {
	e := newEnv(t, "")
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	do := func(headers map[string]string, cookie string) *http.Response {
		req, _ := http.NewRequest("POST", e.srv.URL+Path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: api.SessionCookie, Value: cookie})
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	cases := []struct {
		name    string
		headers map[string]string
		cookie  string
		want    string
	}{
		{"no token", nil, "", `Bearer realm="ownpanel"`},
		{"bad token", map[string]string{"Authorization": "Bearer op_nope"}, "", `Bearer realm="ownpanel", error="invalid_token"`},
		{"no op_ prefix", map[string]string{"Authorization": "Bearer nope"}, "", `Bearer realm="ownpanel", error="invalid_token"`},
		{"basic scheme", map[string]string{"Authorization": "Basic abc"}, "", `Bearer realm="ownpanel"`},
	}
	for _, c := range cases {
		resp := do(c.headers, c.cookie)
		if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") != c.want {
			t.Errorf("%s: status %d, WWW-Authenticate %q", c.name, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
	}
	// A session cookie alone is never enough.
	sess := loginCookie(t, e)
	if resp := do(nil, sess); resp.StatusCode != 401 {
		t.Errorf("cookie only: status %d", resp.StatusCode)
	}
	if resp := do(map[string]string{"Authorization": "Bearer " + e.read}, ""); resp.StatusCode != 200 {
		t.Errorf("valid token: status %d", resp.StatusCode)
	}
	// Cross-site browser requests are refused even with a token.
	if resp := do(map[string]string{"Authorization": "Bearer " + e.read, "Origin": "https://evil.example"}, ""); resp.StatusCode != 403 {
		t.Errorf("foreign origin: status %d", resp.StatusCode)
	}
}

func loginCookie(t *testing.T, e *env) string {
	t.Helper()
	_, secret, err := e.svc.Login(context.Background(), panel.Credentials{Name: "root", Password: "root-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestToolsAndAnnotations(t *testing.T) {
	e := newEnv(t, "")
	cs := e.connect(t, e.read)
	if init := cs.InitializeResult(); init.ServerInfo.Name != "ownpanel" || !strings.Contains(init.Instructions, "static web sites") {
		t.Fatalf("initialize: %+v", init)
	}
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type want struct{ readOnly, destructive, idempotent bool }
	wants := map[string]want{
		"list_sites":                   {readOnly: true, idempotent: true},
		"get_site":                     {readOnly: true, idempotent: true},
		"create_site":                  {},
		"set_site_domains":             {destructive: true, idempotent: true},
		"delete_site":                  {destructive: true},
		"make_version_live":            {idempotent: true},
		"publish_files":                {},
		"build_site":                   {},
		"create_upload_link":           {},
		"check_site_domains":           {readOnly: true, idempotent: true},
		"list_redirects":               {readOnly: true, idempotent: true},
		"set_redirect":                 {destructive: true},
		"delete_redirect":              {destructive: true},
		"get_mail_status":              {readOnly: true, idempotent: true},
		"list_mail_domains":            {readOnly: true, idempotent: true},
		"get_mail_records":             {readOnly: true, idempotent: true},
		"set_up_mail_records":          {destructive: true},
		"get_mail_health":              {readOnly: true, idempotent: true},
		"set_up_mail_host_spf":         {idempotent: true},
		"enable_mail_domain":           {},
		"check_mail_domain":            {idempotent: true},
		"delete_mail_domain":           {destructive: true},
		"list_mailboxes":               {readOnly: true, idempotent: true},
		"set_mailbox_limits":           {idempotent: true},
		"unblock_mailbox":              {idempotent: true},
		"delete_mailbox":               {destructive: true},
		"list_mail_aliases":            {readOnly: true, idempotent: true},
		"set_mail_alias":               {destructive: true},
		"delete_mail_alias":            {destructive: true},
		"list_mail_imports":            {readOnly: true, idempotent: true},
		"cancel_mail_import":           {destructive: true},
		"list_mail_organisations":      {readOnly: true, idempotent: true},
		"set_mail_organisation":        {},
		"delete_mail_organisation":     {destructive: true},
		"set_mail_domain_organisation": {idempotent: true},
		"set_mailbox_display_name":     {idempotent: true},
		"set_mailbox_kind":             {destructive: true, idempotent: true},
		"set_mailbox_access":           {idempotent: true},
		"remove_mailbox_access":        {destructive: true, idempotent: true},
		"get_mailbox_out_of_office":    {readOnly: true, idempotent: true},
		"set_mailbox_out_of_office":    {idempotent: true},
		"clear_mailbox_out_of_office":  {destructive: true, idempotent: true},
		"list_dns_domains":             {readOnly: true, idempotent: true},
		"manage_dns_domain":            {idempotent: true},
		"stop_managing_dns_domain":     {},
		"list_dns_record_templates":    {readOnly: true, idempotent: true},
		"apply_dns_record_template":    {destructive: true, idempotent: true},
		"list_domains":                 {readOnly: true, idempotent: true},
		"add_domain":                   {idempotent: true},
		"remove_domain":                {},
		"get_github_status":            {readOnly: true, idempotent: true},
		"list_github_repositories":     {readOnly: true, idempotent: true},
		"list_dns_connections":         {readOnly: true, idempotent: true},
		"list_dns_records":             {readOnly: true, idempotent: true},
		"set_dns_record":               {destructive: true, idempotent: true},
		"delete_dns_record":            {destructive: true, idempotent: true},
		"check_domain":                 {readOnly: true, idempotent: true},
		"point_domain_at_server":       {destructive: true, idempotent: true},
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
		w, known := wants[tool.Name]
		if !known {
			t.Errorf("unexpected tool %s", tool.Name)
			continue
		}
		a := tool.Annotations
		if a == nil || a.ReadOnlyHint != w.readOnly || a.IdempotentHint != w.idempotent {
			t.Errorf("%s: annotations %+v", tool.Name, a)
			continue
		}
		if !w.readOnly && (a.DestructiveHint == nil || *a.DestructiveHint != w.destructive) {
			t.Errorf("%s: destructive hint %v, want %v", tool.Name, a.DestructiveHint, w.destructive)
		}
		if len(tool.Description) < 60 {
			t.Errorf("%s: description too short", tool.Name)
		}
	}
	for name := range wants {
		if !got[name] {
			t.Errorf("missing tool %s", name)
		}
	}
}

func TestSiteLifecycle(t *testing.T) {
	e := newEnv(t, "")
	cs := e.connect(t, e.write)
	var out siteOutput
	ok(t, call_(t, cs, "create_site", map[string]any{"name": "blog", "domains": []string{"Blog.Example.com"}}), &out)
	if out.Site.Name != "blog" || !slices.Equal(out.Site.Domains, []string{"blog.example.com"}) || out.Site.Owner != "root" || len(out.Site.Versions) != 0 {
		t.Fatalf("create_site: %+v", out)
	}
	refused(t, call_(t, cs, "create_site", map[string]any{"name": "blog", "domains": []string{}}), "already taken")
	refused(t, call_(t, cs, "create_site", map[string]any{"name": "Bad Name", "domains": []string{}}), "invalid site name")

	pub := func(body string) siteOutput {
		var o siteOutput
		ok(t, call_(t, cs, "publish_files", with(index(body), "site", "blog")), &o)
		return o
	}
	v1 := pub("<h1>one</h1>")
	if len(v1.Site.Versions) != 1 || !v1.Site.Versions[0].Live || v1.Site.LiveVersion != v1.Site.Versions[0].ID {
		t.Fatalf("after first publish: %+v", v1.Site)
	}
	if !strings.HasSuffix(v1.Site.Versions[0].CreatedAt, "Z") || !strings.Contains(v1.Site.Versions[0].CreatedAt, "T") {
		t.Fatalf("created_at not RFC 3339: %q", v1.Site.Versions[0].CreatedAt)
	}
	if got := readFile(t, e, "blog", "index.html"); got != "<h1>one</h1>" {
		t.Fatalf("served file %q", got)
	}

	// Several files, binary content and sub-folders.
	png := []byte{0x89, 'P', 'N', 'G', 0, 1, 2}
	var v2 siteOutput
	ok(t, call_(t, cs, "publish_files", map[string]any{"site": "blog", "files": []any{
		map[string]any{"path": "index.html", "content": "<h1>two</h1>", "encoding": "utf8"},
		map[string]any{"path": "css/site.css", "content": "body{}"},
		map[string]any{"path": "img/a.png", "content": base64.StdEncoding.EncodeToString(png), "encoding": "base64"},
	}}), &v2)
	if len(v2.Site.Versions) != 2 || v2.Site.Versions[0].ID != v2.Site.LiveVersion || v2.Site.Versions[1].ID != v1.Site.LiveVersion {
		t.Fatalf("versions must be newest first: %+v", v2.Site.Versions)
	}
	if got := readFile(t, e, "blog", "img/a.png"); got != string(png) {
		t.Fatalf("binary file corrupted: %q", got)
	}
	if readFile(t, e, "blog", "css/site.css") != "body{}" {
		t.Fatal("css missing")
	}

	// Going back without a version, then forward by id; repeating changes nothing.
	var back siteOutput
	ok(t, call_(t, cs, "make_version_live", map[string]any{"site": "blog"}), &back)
	if back.Site.LiveVersion != v1.Site.LiveVersion || readFile(t, e, "blog", "index.html") != "<h1>one</h1>" {
		t.Fatalf("rollback: %+v", back.Site)
	}
	for i := 0; i < 2; i++ {
		ok(t, call_(t, cs, "make_version_live", map[string]any{"site": "blog", "version": v2.Site.LiveVersion}), &back)
		if back.Site.LiveVersion != v2.Site.LiveVersion {
			t.Fatalf("make live by id: %+v", back.Site)
		}
	}
	refused(t, call_(t, cs, "make_version_live", map[string]any{"site": "blog", "version": "nope"}), "no such")

	var got siteOutput
	ok(t, call_(t, cs, "get_site", map[string]any{"site": "blog"}), &got)
	if got.Site.LiveVersion != v2.Site.LiveVersion || len(got.Site.Versions) != 2 {
		t.Fatalf("get_site: %+v", got.Site)
	}
	var list sitesOutput
	ok(t, call_(t, cs, "list_sites", nil), &list)
	if len(list.Sites) != 1 || list.Sites[0].Name != "blog" {
		t.Fatalf("list_sites: %+v", list)
	}
	refused(t, call_(t, cs, "get_site", map[string]any{"site": "missing"}), "not found")

	// Domains: replaced as a whole, repeatable, and a missing site is not created.
	for i := 0; i < 2; i++ {
		ok(t, call_(t, cs, "set_site_domains", map[string]any{"name": "blog", "domains": []string{"b.example.com", "www.b.example.com"}}), &got)
		if !slices.Equal(got.Site.Domains, []string{"b.example.com", "www.b.example.com"}) {
			t.Fatalf("set_site_domains: %+v", got.Site)
		}
	}
	refused(t, call_(t, cs, "set_site_domains", map[string]any{"name": "ghost", "domains": []string{"g.example.com"}}), "not found")
	refused(t, call_(t, cs, "set_site_domains", map[string]any{"name": "blog", "domains": []string{"not a domain"}}), "invalid domain")

	// Delete needs the matching confirmation.
	refused(t, call_(t, cs, "delete_site", map[string]any{"name": "blog", "confirm_name": "other"}), "does not match")
	refused(t, call_(t, cs, "delete_site", map[string]any{"name": "blog", "confirm_name": ""}), "does not match")
	ok(t, call_(t, cs, "get_site", map[string]any{"site": "blog"}), &got) // still there
	var del deleteSiteOutput
	ok(t, call_(t, cs, "delete_site", map[string]any{"name": "blog", "confirm_name": "blog"}), &del)
	if del.Deleted != "blog" {
		t.Fatalf("delete_site: %+v", del)
	}
	refused(t, call_(t, cs, "get_site", map[string]any{"site": "blog"}), "not found")
	if _, err := os.Stat(filepath.Join(e.sitesDir, "blog")); !os.IsNotExist(err) {
		t.Fatalf("files not removed: %v", err)
	}
}

func TestPermissions(t *testing.T) {
	e := newEnv(t, "")
	w := e.connect(t, e.write)
	var out siteOutput
	ok(t, call_(t, w, "create_site", map[string]any{"name": "a", "domains": []string{"a.example.com"}}), &out)
	ok(t, call_(t, w, "create_site", map[string]any{"name": "b", "domains": []string{"b.example.com"}}), &out)

	t.Run("read-only token", func(t *testing.T) {
		r := e.connect(t, e.read)
		var list sitesOutput
		ok(t, call_(t, r, "list_sites", nil), &list)
		if len(list.Sites) != 2 {
			t.Fatalf("read token should list: %+v", list)
		}
		refused(t, call_(t, r, "create_site", map[string]any{"name": "c", "domains": []string{}}), "only read")
		refused(t, call_(t, r, "publish_files", with(index("x"), "site", "a")), "only read")
		refused(t, call_(t, r, "make_version_live", map[string]any{"site": "a"}), "only read")
		refused(t, call_(t, r, "set_site_domains", map[string]any{"name": "a", "domains": []string{}}), "only read")
		refused(t, call_(t, r, "create_upload_link", map[string]any{"site": "a"}), "only read")
		refused(t, call_(t, r, "delete_site", map[string]any{"name": "a", "confirm_name": "a"}), "only read")
	})

	t.Run("site-limited token", func(t *testing.T) {
		l := e.connect(t, e.token(t, "write", "only-a", []string{"a"}))
		var list sitesOutput
		ok(t, call_(t, l, "list_sites", nil), &list)
		if len(list.Sites) != 1 || list.Sites[0].Name != "a" {
			t.Fatalf("limited token should only see a: %+v", list)
		}
		refused(t, call_(t, l, "create_site", map[string]any{"name": "c", "domains": []string{}}), "limited to certain sites")
		refused(t, call_(t, l, "delete_site", map[string]any{"name": "a", "confirm_name": "a"}), "limited to certain sites")
		refused(t, call_(t, l, "get_site", map[string]any{"site": "b"}), "not found")
		refused(t, call_(t, l, "publish_files", with(index("x"), "site", "b")), "not found")
		refused(t, call_(t, l, "create_upload_link", map[string]any{"site": "b"}), "not found")
		ok(t, call_(t, l, "publish_files", with(index("<p>a</p>"), "site", "a")), &out)
		if readFile(t, e, "a", "index.html") != "<p>a</p>" {
			t.Fatal("limited token could not publish to its site")
		}
	})
}

func TestPublishValidation(t *testing.T) {
	e := newEnv(t, "")
	cs := e.connect(t, e.write)
	var out siteOutput
	ok(t, call_(t, cs, "create_site", map[string]any{"name": "s", "domains": []string{}}), &out)

	file := func(path, content, enc string) map[string]any {
		f := map[string]any{"path": path, "content": content}
		if enc != "" {
			f["encoding"] = enc
		}
		return f
	}
	publish := func(files ...any) *sdk.CallToolResult {
		return call_(t, cs, "publish_files", map[string]any{"site": "s", "files": files})
	}
	for _, p := range []string{"../evil.html", "a/../../evil", "/abs.html", "a//b.html", "./x.html", "a/", "", "back\\slash.html"} {
		refused(t, publish(file(p, "x", "")), "path")
	}
	refused(t, publish(), "empty")
	refused(t, publish(file("a.html", "x", ""), file("a.html", "y", "")), "twice")
	refused(t, publish(file("a", "x", ""), file("a/b.html", "y", "")), "both a file and a folder")
	refused(t, publish(file("a.html", "!!!notbase64", "base64")), "base64")
	refused(t, publish(file("a.html", "x", "hex")), "encoding")

	// Too large: 10 MB plus one byte, as text and as base64.
	big := strings.Repeat("a", 10<<20+1)
	refused(t, publish(file("big.html", big, "")), "10 MB")
	refused(t, publish(file("big.bin", base64.StdEncoding.EncodeToString([]byte(big)), "base64")), "10 MB")
	refused(t, publish(file("a.html", strings.Repeat("a", 6<<20), ""), file("b.html", strings.Repeat("b", 6<<20), "")), "10 MB")
	// Exactly at the limit is fine.
	okRes := publish(file("edge.html", strings.Repeat("a", 10<<20), ""))
	if okRes.IsError {
		t.Fatalf("10 MB should be accepted: %s", text(okRes))
	}

	many := make([]any, 2001)
	for i := range many {
		many[i] = file("f/"+strings.Repeat("x", 1)+"-"+itoa(i)+".txt", "x", "")
	}
	refused(t, publish(many...), "too many files")
	if res := publish(many[:2000]...); res.IsError {
		t.Fatalf("2000 files should be accepted: %s", text(res))
	}

	// Nothing from the refused calls was published: only the two good versions exist.
	var got siteOutput
	ok(t, call_(t, cs, "get_site", map[string]any{"site": "s"}), &got)
	if len(got.Site.Versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(got.Site.Versions))
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestUploadLink(t *testing.T) {
	e := newEnv(t, "")
	cs := e.connect(t, e.write)
	var out siteOutput
	ok(t, call_(t, cs, "create_site", map[string]any{"name": "big", "domains": []string{}}), &out)
	ok(t, call_(t, cs, "create_site", map[string]any{"name": "other", "domains": []string{}}), &out)

	var link uploadLinkOutput
	ok(t, call_(t, cs, "create_upload_link", map[string]any{"site": "big"}), &link)
	if !strings.HasPrefix(link.URL, e.srv.URL+"/api/uploads/") {
		t.Fatalf("link should use the request's own address: %q", link.URL)
	}
	if !strings.Contains(link.CurlCommand, "curl") || !strings.Contains(link.CurlCommand, link.URL) || link.ExpiresAt == "" {
		t.Fatalf("link output: %+v", link)
	}

	archive, err := buildArchive([]File{{Path: "index.html", Content: "<p>uploaded</p>"}})
	if err != nil {
		t.Fatal(err)
	}
	post := func(url string) int {
		resp, err := http.Post(url, "application/octet-stream", bytes.NewReader(archive))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(link.URL); code != 201 {
		t.Fatalf("first upload: %d", code)
	}
	if got := readFile(t, e, "big", "index.html"); got != "<p>uploaded</p>" {
		t.Fatalf("uploaded file %q", got)
	}
	if code := post(link.URL); code != 404 {
		t.Fatalf("second upload with the same link: %d", code)
	}
	if code := post(e.srv.URL + "/api/uploads/does-not-exist"); code != 404 {
		t.Fatalf("wrong id: %d", code)
	}
	// A link for a site the token cannot use is refused before any link exists.
	limited := e.connect(t, e.token(t, "write", "only-other", []string{"other"}))
	refused(t, call_(t, limited, "create_upload_link", map[string]any{"site": "big"}), "not found")

	// The activity log shows who published and through which token.
	acts, err := e.svc.Activities(context.Background(), e.admin, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range acts {
		if a.Action == "site.publish" && a.Target == "big" && a.Actor == "root via token all-write" {
			found = true
		}
	}
	if !found {
		t.Fatalf("publish by upload link not attributed to the token: %+v", acts)
	}
}

func TestUploadLinkBaseURL(t *testing.T) {
	e := newEnv(t, "panel.example.com", WithPanelDomain("panel.example.com"))
	cs := e.connect(t, e.write)
	var out siteOutput
	ok(t, call_(t, cs, "create_site", map[string]any{"name": "s", "domains": []string{}}), &out)
	var link uploadLinkOutput
	ok(t, call_(t, cs, "create_upload_link", map[string]any{"site": "s"}), &link)
	if !strings.HasPrefix(link.URL, "https://panel.example.com/api/uploads/") {
		t.Fatalf("with a panel domain the link must use it: %q", link.URL)
	}
}

// Through Caddy the request arrives from loopback with the panel domain as
// Host; the SDK's rebinding protection must let exactly that through.
func TestPanelDomainHost(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	send := func(e *env, host string) int {
		req, _ := http.NewRequest("POST", e.srv.URL+Path, strings.NewReader(body))
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+e.read)
		req.Header.Set("X-Forwarded-Proto", "https")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	configured := newEnv(t, "panel.example.com", WithPanelDomain("panel.example.com"))
	if code := send(configured, "panel.example.com"); code != 200 {
		t.Errorf("panel domain: %d", code)
	}
	if code := send(configured, "Panel.Example.com:443"); code != 200 {
		t.Errorf("panel domain with port and capitals: %d", code)
	}
	if code := send(configured, "127.0.0.1:8600"); code != 200 {
		t.Errorf("loopback host: %d", code)
	}
	if code := send(configured, "evil.example"); code != 403 {
		t.Errorf("other host must stay refused: %d", code)
	}
	without := newEnv(t, "")
	if code := send(without, "panel.example.com"); code != 403 {
		t.Errorf("without a configured panel domain the host is refused: %d", code)
	}
}

func TestCheckSiteDomains(t *testing.T) {
	e := newEnv(t, "")
	cs := e.connect(t, e.read)
	w := e.connect(t, e.write)
	var out siteOutput
	ok(t, call_(t, w, "create_site", map[string]any{"name": "s", "domains": []string{"here.example.com", "away.example.com", "new.example.com"}}), &out)
	var got domainsOutput
	ok(t, call_(t, cs, "check_site_domains", map[string]any{"site": "s"}), &got)
	states := map[string]string{}
	for _, d := range got.Domains {
		states[d.Domain] = d.State
	}
	want := map[string]string{"here.example.com": "points_here", "away.example.com": "points_elsewhere", "new.example.com": "no_record"}
	if !maps.Equal(states, want) {
		t.Fatalf("states %v, want %v", states, want)
	}
	refused(t, call_(t, cs, "check_site_domains", map[string]any{"site": "nope"}), "not found")
}

// A panel domain set later in the panel settings takes effect without a
// restart.
func TestPanelDomainFromSettings(t *testing.T) {
	var svc atomic.Pointer[panel.Service]
	e := newEnv(t, "", WithPanelDomainFunc(func() string {
		if p := svc.Load(); p != nil {
			return p.PanelDomain()
		}
		return ""
	}))
	svc.Store(e.svc)
	cs := e.connect(t, e.write)
	var out siteOutput
	ok(t, call_(t, cs, "create_site", map[string]any{"name": "s", "domains": []string{}}), &out)
	var link uploadLinkOutput
	ok(t, call_(t, cs, "create_upload_link", map[string]any{"site": "s"}), &link)
	if strings.HasPrefix(link.URL, "https://panel.example.com/") {
		t.Fatalf("no panel domain yet: %q", link.URL)
	}
	domain := "panel.example.com"
	if _, err := e.svc.UpdateSettings(context.Background(), e.admin, panel.SettingsUpdate{PanelDomain: &domain}); err != nil {
		t.Fatal(err)
	}
	ok(t, call_(t, cs, "create_upload_link", map[string]any{"site": "s"}), &link)
	if !strings.HasPrefix(link.URL, "https://panel.example.com/api/uploads/") {
		t.Fatalf("the panel domain from the settings must be used: %q", link.URL)
	}
}

func TestPublishWithoutMakingLive(t *testing.T) {
	e := newEnv(t, "")
	cs := e.connect(t, e.write)
	var out siteOutput
	ok(t, call_(t, cs, "create_site", map[string]any{"name": "blog", "domains": []string{"blog.example.com"}}), &out)
	var v1 publishOutput
	ok(t, call_(t, cs, "publish_files", with(index("one"), "site", "blog")), &v1)
	if !v1.Live || v1.Site.LiveVersion != v1.Version || v1.PreviewURL != "" {
		t.Fatalf("live publish: %+v", v1)
	}

	// Previews get addresses once an admin sets a domain for them.
	pd := "preview.example.net"
	if _, err := e.svc.UpdateSettings(context.Background(), e.admin, panel.SettingsUpdate{PreviewDomain: &pd}); err != nil {
		t.Fatal(err)
	}
	var v2 publishOutput
	ok(t, call_(t, cs, "publish_files", with(with(index("two"), "site", "blog"), "live", false)), &v2)
	if v2.Live || v2.Site.LiveVersion != v1.Version || v2.Version == v1.Version || !strings.Contains(v2.PreviewURL, "."+pd) {
		t.Fatalf("not live: %+v", v2)
	}
	if readFile(t, e, "blog", "index.html") != "one" {
		t.Fatal("the live version changed")
	}
	vs := v2.Site.Versions
	if len(vs) != 2 || vs[0].ID != v2.Version || vs[0].Live || !vs[0].NeverLive || vs[0].PreviewURL != v2.PreviewURL || !vs[1].Live || vs[1].PreviewURL == "" {
		t.Fatalf("versions: %+v", vs)
	}
	ok(t, call_(t, cs, "make_version_live", map[string]any{"site": "blog", "version": v2.Version}), &out)
	if readFile(t, e, "blog", "index.html") != "two" || out.Site.Versions[0].NeverLive {
		t.Fatalf("made live: %+v", out.Site)
	}

	// Upload links can leave the version as it is too.
	var link uploadLinkOutput
	ok(t, call_(t, cs, "create_upload_link", map[string]any{"site": "blog", "live": false}), &link)
	archive, err := buildArchive([]File{{Path: "index.html", Content: "three"}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(link.URL, "application/octet-stream", bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Release    string `json:"published_release"`
		Live       bool   `json:"published_live"`
		PreviewURL string `json:"preview_url"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != 201 || body.Live || body.Release == "" || !strings.Contains(body.PreviewURL, "."+pd) {
		t.Fatalf("upload: %d %+v", resp.StatusCode, body)
	}
	if readFile(t, e, "blog", "index.html") != "two" {
		t.Fatal("the upload made its version live")
	}
}
