package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/store"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type nopWeb struct{}

func (nopWeb) Load(context.Context, any) error { return nil }

// env is a real panel behind the API, set up with a first admin "root"
// whose session cookie is in cookie.
type env struct {
	t      *testing.T
	h      http.Handler
	svc    *panel.Service
	code   string // path of the setup code file
	cookie string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := newEmptyEnv(t)
	e.cookie = e.setup("root", "root-password-1")
	return e
}

func newEmptyEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := panel.New(panel.Config{
		Store:         st,
		Deploys:       &deploy.Manager{Base: filepath.Join(dir, "sites")},
		Web:           nopWeb{},
		Caddy:         caddy.Options{AdminListen: "127.0.0.1:2019", TLSMode: caddy.TLSOff, HTTPPort: 8080, HTTPSPort: 8443},
		SetupCodeFile: filepath.Join(dir, "setup-code"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureSetupCode(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, h: (&Server{Panel: svc}).Handler(), svc: svc, code: filepath.Join(dir, "setup-code")}
}

func (e *env) setupCode() string {
	b, _ := os.ReadFile(e.code)
	return strings.TrimSpace(string(b))
}

// setup performs the first-run setup and returns the session cookie.
func (e *env) setup(name, password string) string {
	e.t.Helper()
	body, _ := json.Marshal(map[string]string{"setup_code": e.setupCode(), "name": name, "password": password})
	rec := e.do(req{method: "POST", path: "/api/setup", body: string(body)})
	if rec.Code != 201 {
		e.t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	return sessionCookie(e.t, rec)
}

type req struct {
	method, path, body string
	cookie, token      string
	csrf               bool
	headers            map[string]string
	remote             string
	tls                bool
	host               string // Host header; "" means example.com
}

func (e *env) do(r req) *httptest.ResponseRecorder {
	e.t.Helper()
	hr := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	if r.cookie != "" {
		name := SessionCookie
		if r.tls || r.headers["X-Forwarded-Proto"] == "https" {
			name = SecureSessionCookie
		}
		hr.AddCookie(&http.Cookie{Name: name, Value: r.cookie})
	}
	if r.token != "" {
		hr.Header.Set("Authorization", "Bearer "+r.token)
	}
	if r.csrf {
		hr.Header.Set(CSRFHeader, "1")
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	if r.remote != "" {
		hr.RemoteAddr = r.remote
	}
	if r.host != "" {
		hr.Host = r.host
	}
	if r.tls {
		hr.TLS = &tls.ConnectionState{}
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, hr)
	return rec
}

// as sends a request with the admin's session and the CSRF header.
func (e *env) as(method, path, body string) *httptest.ResponseRecorder {
	return e.do(req{method: method, path: path, body: body, cookie: e.cookie, csrf: true})
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if (c.Name == SessionCookie || c.Name == SecureSessionCookie) && c.Value != "" {
			return c.Value
		}
	}
	t.Fatal("no session cookie set")
	return ""
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return v
}

func (e *env) createToken(access string, sites string) (int64, string) {
	e.t.Helper()
	rec := e.as("POST", "/api/tokens", `{"name":"t","access":"`+access+`","sites":`+sites+`}`)
	if rec.Code != 201 {
		e.t.Fatalf("token: %d %s", rec.Code, rec.Body)
	}
	tok := decode[struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}](e.t, rec)
	return tok.ID, tok.Token
}

func tarGz(t *testing.T, body string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "index.html", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	return buf.String()
}

func TestSiteRoutes(t *testing.T) {
	e := newEnv(t)
	steps := []struct {
		name, method, path, body string
		status                   int
		contains                 string
	}{
		{"empty list", "GET", "/api/sites", "", 200, "[]\n"},
		{"create", "PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`, 200,
			`{"name":"blog","domains":["blog.example.com"],"owner":"root","current_release":"","releases":[]}`},
		{"get", "GET", "/api/sites/blog", "", 200, `"owner":"root"`},
		{"list", "GET", "/api/sites", "", 200, `[{"name":"blog"`},
		{"publish", "POST", "/api/sites/blog/deployments", tarGz(t, "v1"), 201, `"current_release":"`},
		{"rollback without earlier release", "POST", "/api/sites/blog/rollback", "", 409, `{"error":"`},
		{"bad archive", "POST", "/api/sites/blog/deployments", "just text", 400, `{"error":"`},
		{"bad json", "PUT", "/api/sites/blog", "not json", 400, "Request body must be JSON"},
		{"bad domain", "PUT", "/api/sites/blog", `{"domains":["nodots"]}`, 400, `{"error":"Invalid domain`},
		{"delete", "DELETE", "/api/sites/blog", "", 204, ""},
		{"gone", "GET", "/api/sites/blog", "", 404, `{"error":"Site not found"}`},
	}
	for _, s := range steps {
		rec := e.as(s.method, s.path, s.body)
		if rec.Code != s.status || !strings.Contains(rec.Body.String(), s.contains) {
			t.Fatalf("%s: got %d %q, want %d containing %q", s.name, rec.Code, rec.Body, s.status, s.contains)
		}
		if s.status != 204 && rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s: content type %q", s.name, rec.Header().Get("Content-Type"))
		}
	}
}

func TestAuthenticationAndCSRF(t *testing.T) {
	e := newEnv(t)
	_, write := e.createToken("write", "null")
	_, read := e.createToken("read", "null")
	tests := []struct {
		name   string
		r      req
		status int
		msg    string
	}{
		{"no credentials", req{method: "GET", path: "/api/sites"}, 401, "Please sign in"},
		{"wrong token", req{method: "GET", path: "/api/sites", token: "op_wrong"}, 401, "The access token is not valid"},
		{"not a bearer", req{method: "GET", path: "/api/sites", headers: map[string]string{"Authorization": "Basic x"}}, 401, ""},
		{"stale cookie", req{method: "GET", path: "/api/sites", cookie: "nope"}, 401, ""},
		{"cookie read", req{method: "GET", path: "/api/sites", cookie: e.cookie}, 200, ""},
		{"cookie change without header", req{method: "PUT", path: "/api/sites/a", body: `{}`, cookie: e.cookie}, 403, "X-Ownpanel-Request"},
		{"cookie change with wrong header value", req{method: "PUT", path: "/api/sites/a", body: `{}`, cookie: e.cookie,
			headers: map[string]string{CSRFHeader: "yes"}}, 403, ""},
		{"cookie change with header", req{method: "PUT", path: "/api/sites/a", body: `{}`, cookie: e.cookie, csrf: true}, 200, ""},
		{"cookie change from another origin", req{method: "PUT", path: "/api/sites/b", body: `{}`, cookie: e.cookie, csrf: true,
			headers: map[string]string{"Origin": "https://evil.example"}}, 403, "other web sites"},
		{"cookie change from same origin", req{method: "PUT", path: "/api/sites/c", body: `{}`, cookie: e.cookie, csrf: true,
			headers: map[string]string{"Origin": "http://EXAMPLE.com"}}, 200, ""},
		{"login from another origin", req{method: "POST", path: "/api/login", body: `{"name":"root","password":"root-password-1"}`,
			headers: map[string]string{"Origin": "https://evil.example"}}, 403, ""},
		{"token change needs no header", req{method: "PUT", path: "/api/sites/d", body: `{}`, token: write}, 200, ""},
		{"token wins over cookie", req{method: "PUT", path: "/api/sites/e", body: `{}`, token: read, cookie: e.cookie, csrf: true}, 403, "only read"},
		{"read token cannot create", req{method: "PUT", path: "/api/sites/f", body: `{}`, token: read}, 403, ""},
		{"tokens via token", req{method: "GET", path: "/api/tokens", token: write}, 403, "access token"},
		{"password via token", req{method: "PUT", path: "/api/account/password", body: `{"current":"root-password-1","new":"root-password-2"}`, token: write}, 403, ""},
		{"users via write token", req{method: "GET", path: "/api/users", token: write}, 200, ""},
		{"users via read token", req{method: "GET", path: "/api/users", token: read}, 403, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.do(tc.r)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.msg) {
				t.Fatalf("got %d %q, want %d containing %q", rec.Code, rec.Body, tc.status, tc.msg)
			}
		})
	}
}

func TestSessionLifecycleOverHTTP(t *testing.T) {
	e := newEmptyEnv(t)
	rec := e.do(req{method: "GET", path: "/api/session"})
	if rec.Body.String() != `{"setup_required":true,"setup_complete":false,"user":null,"via":null}`+"\n" {
		t.Fatalf("before setup: %s", rec.Body)
	}
	rec = e.do(req{method: "POST", path: "/api/setup", body: `{"setup_code":"WRONG","name":"root","password":"root-password-1"}`})
	if rec.Code != 400 {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body)
	}
	e.cookie = e.setup("root", "root-password-1")
	rec = e.do(req{method: "POST", path: "/api/setup", body: `{"setup_code":"x","name":"x","password":"x"}`})
	if rec.Code != 409 {
		t.Fatalf("second setup: %d", rec.Code)
	}

	rec = e.do(req{method: "GET", path: "/api/session", cookie: e.cookie})
	s := decode[struct {
		Setup bool        `json:"setup_required"`
		User  *panel.User `json:"user"`
		Via   *string     `json:"via"`
	}](t, rec)
	if s.Setup || s.User == nil || s.User.Name != "root" || s.User.Role != "admin" || *s.Via != "session" {
		t.Fatalf("session: %s", rec.Body)
	}

	rec = e.do(req{method: "POST", path: "/api/login", body: `{"name":"root","password":"nope-nope-nope"}`})
	if rec.Code != 401 || rec.Body.String() != `{"error":"Wrong name or password."}`+"\n" {
		t.Fatalf("wrong login: %d %s", rec.Code, rec.Body)
	}
	rec = e.do(req{method: "POST", path: "/api/login", body: `{"name":"root","password":"root-password-1"}`, tls: true})
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	c := rec.Result().Cookies()[0]
	if c.Name != SecureSessionCookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 30*24*3600 {
		t.Fatalf("cookie attributes: %+v", c)
	}
	other := c.Value
	if rec := e.do(req{method: "GET", path: "/api/sites", cookie: other, tls: true}); rec.Code != 200 {
		t.Fatalf("secure cookie over https: %d", rec.Code)
	}

	rec = e.as("PUT", "/api/account/password", `{"current":"root-password-1","new":"root-password-2"}`)
	if rec.Code != 204 {
		t.Fatalf("password: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(req{method: "GET", path: "/api/sites", cookie: other}); rec.Code != 401 {
		t.Fatalf("other session must end: %d", rec.Code)
	}

	rec = e.do(req{method: "POST", path: "/api/logout", cookie: e.cookie})
	if rec.Code != 403 {
		t.Fatalf("logout by cookie needs the CSRF header: %d", rec.Code)
	}
	rec = e.as("POST", "/api/logout", "")
	if rec.Code != 204 || rec.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout: %d %+v", rec.Code, rec.Result().Cookies())
	}
	if rec := e.do(req{method: "GET", path: "/api/sites", cookie: e.cookie}); rec.Code != 401 {
		t.Fatalf("logged out session still works: %d", rec.Code)
	}
	if rec := e.do(req{method: "POST", path: "/api/logout"}); rec.Code != 204 {
		t.Fatalf("logout without session: %d", rec.Code)
	}
}

func TestSecureCookieBehindProxy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote string
		proto  string
		secure bool
	}{
		{"plain", "192.0.2.1:1", "", false},
		{"local proxy says https", "127.0.0.1:1", "https", true},
		{"remote client claims https", "192.0.2.1:1", "https", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEmptyEnv(t)
			body, _ := json.Marshal(map[string]string{"setup_code": e.setupCode(), "name": "root", "password": "root-password-1"})
			rec := e.do(req{method: "POST", path: "/api/setup", body: string(body), remote: tc.remote,
				headers: map[string]string{"X-Forwarded-Proto": tc.proto}})
			if rec.Code != 201 || rec.Result().Cookies()[0].Secure != tc.secure {
				t.Fatalf("%d secure=%v", rec.Code, rec.Result().Cookies()[0].Secure)
			}
		})
	}
}

func TestClientIP(t *testing.T) {
	for _, tc := range []struct{ remote, xff, want string }{
		{"192.0.2.1:5", "", "192.0.2.1"},
		{"192.0.2.1:5", "10.0.0.1", "192.0.2.1"},
		{"127.0.0.1:5", "10.0.0.1, 203.0.113.9", "203.0.113.9"},
		{"[::1]:5", "203.0.113.9", "203.0.113.9"},
		{"127.0.0.1:5", "", "127.0.0.1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := clientIP(r); got != tc.want {
			t.Errorf("%+v: got %s", tc, got)
		}
	}
}

func TestLoginThrottledOverHTTP(t *testing.T) {
	e := newEnv(t)
	bad := req{method: "POST", path: "/api/login", body: `{"name":"root","password":"wrong-password"}`}
	for i := 0; i < 5; i++ {
		if rec := e.do(bad); rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	rec := e.do(req{method: "POST", path: "/api/login", body: `{"name":"root","password":"root-password-1"}`})
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "Too many failed attempts") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	// Behind the local proxy the forwarded address counts.
	rec = e.do(req{method: "POST", path: "/api/login", body: `{"name":"root","password":"root-password-1"}`,
		remote: "127.0.0.1:9", headers: map[string]string{"X-Forwarded-For": "198.51.100.7"}})
	if rec.Code != 200 {
		t.Fatalf("other client: %d %s", rec.Code, rec.Body)
	}
}

func TestUserAndTokenRoutes(t *testing.T) {
	e := newEnv(t)
	rec := e.as("POST", "/api/users", `{"name":"Alice","password":"alice-password-1","role":"member"}`)
	if rec.Code != 201 {
		t.Fatalf("create user: %d %s", rec.Code, rec.Body)
	}
	alice := decode[panel.User](t, rec)
	if alice.Name != "alice" || alice.Role != "member" || alice.CreatedAt.IsZero() {
		t.Fatalf("%+v", alice)
	}
	if rec := e.as("GET", "/api/users", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"alice"`) {
		t.Fatalf("list users: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("PUT", "/api/users/2", `{"role":"admin"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"role":"admin"`) {
		t.Fatalf("promote: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("PUT", "/api/users/abc", `{}`); rec.Code != 404 {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := e.as("DELETE", "/api/users/2", ""); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("DELETE", "/api/users/1", ""); rec.Code != 409 {
		t.Fatalf("delete last admin: %d %s", rec.Code, rec.Body)
	}

	// Members are refused.
	e.as("POST", "/api/users", `{"name":"bob","password":"bob-password-1","role":"member"}`)
	rec = e.do(req{method: "POST", path: "/api/login", body: `{"name":"bob","password":"bob-password-1"}`})
	bob := sessionCookie(t, rec)
	if rec := e.do(req{method: "GET", path: "/api/users", cookie: bob}); rec.Code != 403 {
		t.Fatalf("member lists users: %d", rec.Code)
	}
	if rec := e.do(req{method: "GET", path: "/api/activity", cookie: bob}); rec.Code != 403 {
		t.Fatalf("member reads activity: %d", rec.Code)
	}
}

func TestTokenRoutes(t *testing.T) {
	e := newEnv(t)
	e.as("PUT", "/api/sites/blog", `{"domains":[]}`)
	rec := e.as("POST", "/api/tokens", `{"name":"ci","access":"read","sites":["blog"]}`)
	if rec.Code != 201 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	for _, k := range []string{"id", "name", "access", "sites", "created_at", "last_used_at", "token"} {
		if _, ok := created[k]; !ok {
			t.Errorf("field %s missing in %s", k, rec.Body)
		}
	}
	if !strings.HasPrefix(created["token"].(string), "op_") || created["last_used_at"] != nil {
		t.Fatalf("%s", rec.Body)
	}
	if rec := e.as("POST", "/api/tokens", `{"name":"ci","access":"write","sites":["nope"]}`); rec.Code != 400 {
		t.Fatalf("unknown site: %d", rec.Code)
	}
	rec = e.as("GET", "/api/tokens", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"token"`) || !strings.Contains(rec.Body.String(), `"sites":["blog"]`) {
		t.Fatalf("list must not contain secrets: %s", rec.Body)
	}
	if rec := e.do(req{method: "GET", path: "/api/sites/blog", token: created["token"].(string)}); rec.Code != 200 {
		t.Fatalf("use token: %d", rec.Code)
	}
	if rec := e.as("DELETE", "/api/tokens/1", ""); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("DELETE", "/api/tokens/1", ""); rec.Code != 404 {
		t.Fatalf("delete twice: %d", rec.Code)
	}
	if rec := e.do(req{method: "GET", path: "/api/sites", token: created["token"].(string)}); rec.Code != 401 {
		t.Fatalf("deleted token still works: %d", rec.Code)
	}
}

func TestActivityRoute(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"a", "b", "c"} {
		e.as("PUT", "/api/sites/"+n, `{}`)
	}
	rec := e.as("GET", "/api/activity?limit=2", "")
	list := decode[[]panel.Activity](t, rec)
	if rec.Code != 200 || len(list) != 2 || list[0].Target != "c" || list[0].Action != "site.create" || list[0].Actor != "root" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := e.as("GET", "/api/activity?limit=x", ""); rec.Code != 400 {
		t.Fatalf("bad limit: %d", rec.Code)
	}
	if rec := e.as("GET", "/api/activity?limit=1000", ""); rec.Code != 200 {
		t.Fatalf("large limit is clamped: %d", rec.Code)
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{"not found", &panel.Error{Kind: panel.NotFound, Msg: "site not found"}, 404, "Site not found"},
		{"invalid", &panel.Error{Kind: panel.Invalid, Msg: "invalid site name"}, 400, "Invalid site name"},
		{"conflict", &panel.Error{Kind: panel.Conflict, Msg: "no such release: x"}, 409, "No such release: x"},
		{"forbidden", &panel.Error{Kind: panel.Forbidden, Msg: "no"}, 403, "No"},
		{"unauthenticated", &panel.Error{Kind: panel.Unauthenticated, Msg: "please sign in"}, 401, "Please sign in"},
		{"throttled", &panel.Error{Kind: panel.Throttled, Msg: "too many"}, 429, "Too many"},
		{"not synced", &panel.Error{Kind: panel.NotSynced, Msg: "saved, but the web server could not be updated yet: down"}, 502,
			"Saved, but the web server could not be updated yet: down"},
		{"unavailable", &panel.Error{Kind: panel.Unavailable, Msg: "Hetzner could not be reached"}, 502, "Hetzner could not be reached"},
		{"internal hides details", &panel.Error{Kind: panel.Internal, Msg: "open /secret/path: denied"}, 500, "Something went wrong on the server"},
		{"unclassified hides details", errors.New("open /secret/path: denied"), 500, "Something went wrong on the server"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeFailure(rec, tc.err)
			wantBody := `{"error":"` + tc.want + `"}` + "\n"
			if rec.Code != tc.status || rec.Body.String() != wantBody {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tc.status, wantBody)
			}
		})
	}
}

func TestOversizedJSONBodyRejected(t *testing.T) {
	e := newEnv(t)
	big := `{"domains":["` + strings.Repeat("a", maxJSONBytes) + `.example.com"]}`
	if rec := e.as("PUT", "/api/sites/blog", big); rec.Code != 400 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec := e.as("GET", "/api/sites/blog", ""); rec.Code != 404 {
		t.Fatal("the site must not have been created")
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	e := newEnv(t)
	rec := e.do(req{method: "GET", path: "/api/x"})
	if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/json" ||
		rec.Body.String() != `{"error":"No such API endpoint"}`+"\n" {
		t.Fatalf("got %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

// Over HTTPS only the prefixed cookie counts; one under the old name is
// ignored and removed.
func TestOldSessionCookieOverHTTPS(t *testing.T) {
	e := newEnv(t) // e.cookie was set over plain HTTP, under the old name
	hr := httptest.NewRequest("GET", "/api/sites", nil)
	hr.TLS = &tls.ConnectionState{}
	hr.AddCookie(&http.Cookie{Name: SessionCookie, Value: e.cookie})
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, hr)
	if rec.Code != 401 {
		t.Fatalf("old cookie over https: %d", rec.Code)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != SessionCookie || cs[0].MaxAge != -1 {
		t.Fatalf("old cookie not removed: %+v", cs)
	}
	// Over plain HTTP the prefixed name is not read.
	hr = httptest.NewRequest("GET", "/api/sites", nil)
	hr.AddCookie(&http.Cookie{Name: SecureSessionCookie, Value: e.cookie})
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, hr)
	if rec.Code != 401 {
		t.Fatalf("prefixed cookie over http: %d", rec.Code)
	}
}

func TestSignOutEverywhere(t *testing.T) {
	e := newEnv(t)
	rec := e.do(req{method: "POST", path: "/api/login", body: `{"name":"root","password":"root-password-1"}`, tls: true})
	other := sessionCookie(t, rec)
	_, token := e.createToken("read", "null")

	if rec := e.do(req{method: "POST", path: "/api/account/logout-everywhere", cookie: e.cookie}); rec.Code != 403 {
		t.Fatalf("needs the CSRF header: %d", rec.Code)
	}
	if rec := e.do(req{method: "POST", path: "/api/account/logout-everywhere", token: token}); rec.Code != 403 {
		t.Fatalf("not with a token: %d %s", rec.Code, rec.Body)
	}
	rec = e.as("POST", "/api/account/logout-everywhere", "")
	if rec.Code != 204 || rec.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout everywhere: %d %+v", rec.Code, rec.Result().Cookies())
	}
	if rec := e.do(req{method: "GET", path: "/api/sites", cookie: e.cookie}); rec.Code != 401 {
		t.Fatalf("this session must end: %d", rec.Code)
	}
	if rec := e.do(req{method: "GET", path: "/api/sites", cookie: other, tls: true}); rec.Code != 401 {
		t.Fatalf("other session must end: %d", rec.Code)
	}
	if rec := e.do(req{method: "GET", path: "/api/sites", token: token}); rec.Code != 200 {
		t.Fatalf("access tokens keep working: %d", rec.Code)
	}
}

func TestTokenExpiryOverAPI(t *testing.T) {
	e := newEnv(t)
	rec := e.as("POST", "/api/tokens", `{"name":"ci","access":"read","sites":null,"expires_in_days":30}`)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode[struct {
		ExpiresAt *string `json:"expires_at"`
		Expired   bool    `json:"expired"`
	}](t, rec)
	if created.ExpiresAt == nil || created.Expired {
		t.Fatalf("expiry missing: %s", rec.Body)
	}
	rec = e.as("POST", "/api/tokens", `{"name":"ci","access":"read","sites":null}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"expires_at":null`) {
		t.Fatalf("never: %d %s", rec.Code, rec.Body)
	}
	for _, days := range []string{"-1", "3651", `"30"`} {
		rec = e.as("POST", "/api/tokens", `{"name":"ci","access":"read","sites":null,"expires_in_days":`+days+`}`)
		if rec.Code != 400 {
			t.Fatalf("days %s: %d %s", days, rec.Code, rec.Body)
		}
	}
}
