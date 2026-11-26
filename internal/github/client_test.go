package github_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/github/githubtest"
)

func newFake(t *testing.T) (*githubtest.Server, *github.Client) {
	t.Helper()
	srv := githubtest.New()
	t.Cleanup(srv.Close)
	c, err := github.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return srv, c
}

func manifest() githubtest.Manifest {
	var m githubtest.Manifest
	m.Name = "ownpanel test"
	m.URL = "https://panel.example.com/"
	m.HookAttributes.URL = "https://panel.example.com/api/github/webhook"
	m.DefaultPermissions = map[string]string{"contents": "read", "metadata": "read", "statuses": "write"}
	return m
}

func TestNewChecksTheAddress(t *testing.T) {
	for _, bad := range []string{
		"http://github.example.com", "ftp://github.com", "https://user@github.com", "https://github.com/x",
		"https://github.com?x=1", "github.com", "https://",
	} {
		if _, err := github.New(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	c, err := github.New("")
	if err != nil || c.WebURL() != "https://github.com" {
		t.Fatalf("default: %v %v", c, err)
	}
	if _, err := github.New("http://127.0.0.1:9101"); err != nil {
		t.Errorf("loopback http refused: %v", err)
	}
	got := c.ManifestFormURL("acme", "s t")
	if got != "https://github.com/organizations/acme/settings/apps/new?state=s+t" {
		t.Errorf("org form = %s", got)
	}
	if got := c.ManifestFormURL("", "x"); got != "https://github.com/settings/apps/new?state=x" {
		t.Errorf("personal form = %s", got)
	}
	if got := c.InstallURL("my-app", "x"); got != "https://github.com/apps/my-app/installations/new?state=x" {
		t.Errorf("install = %s", got)
	}
	if got := c.AppSettingsURL(github.Account{Login: "acme", Type: github.AccountOrganization}, "my-app"); got != "https://github.com/organizations/acme/settings/apps/my-app" {
		t.Errorf("org settings = %s", got)
	}
}

func TestManifestToRepositories(t *testing.T) {
	srv, c := newFake(t)
	ctx := context.Background()
	repo := srv.AddRepo("acme", "site", true)
	srv.AddRepo("acme", "other", false)
	srv.AddRepo("someone-else", "secret", true)

	code, err := srv.CreateCode(manifest(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := c.ConvertManifest(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Owner != (github.Account{Login: "acme", Type: github.AccountOrganization}) || creds.Slug != "ownpanel-test" ||
		creds.WebhookSecret == "" || creds.ClientSecret == "" || !strings.HasPrefix(string(creds.PrivateKey), "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("credentials: %+v", creds.App)
	}
	if _, err := c.ConvertManifest(ctx, code); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("second conversion: %v", err)
	}

	app, err := c.AsApp(creds.ClientID, creds.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	instID := srv.AddInstallation(creds.ID, "acme", "Organization", nil)
	inst, err := app.Installation(ctx, instID)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Account.Login != "acme" || inst.RepositorySelection != github.SelectionAll || inst.AppID != creds.ID {
		t.Fatalf("installation: %+v", inst)
	}
	jwts := srv.JWTs()
	if len(jwts) != 1 || jwts[0].Issuer != creds.ClientID || jwts[0].Expires.Sub(jwts[0].IssuedAt) > 10*time.Minute {
		t.Fatalf("JWT: %+v", jwts)
	}
	list, err := app.Installations(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("installations: %v %v", list, err)
	}

	tok, err := app.InstallationToken(ctx, instID, github.TokenRequest{
		RepositoryIDs: []int64{repo}, Permissions: map[string]string{"contents": "read", "metadata": "read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	issued := srv.Tokens()
	if len(issued) != 1 || !slices.Equal(issued[0].RepositoryIDs, []int64{repo}) || issued[0].Permissions["contents"] != "read" {
		t.Fatalf("token: %+v", issued)
	}
	repos, truncated, err := c.InstallationRepositories(ctx, tok)
	if err != nil || truncated || len(repos) != 1 || repos[0].FullName != "acme/site" || !repos[0].Private || repos[0].DefaultBranch != "main" {
		t.Fatalf("repositories: %+v %v %v", repos, truncated, err)
	}

	// The App may not ask for more than it was given.
	if _, err := app.InstallationToken(ctx, instID, github.TokenRequest{Permissions: map[string]string{"contents": "write"}}); !errors.Is(err, github.ErrInvalid) {
		t.Fatalf("write token: %v", err)
	}
}

func TestForeignInstallationIsNotFound(t *testing.T) {
	srv, c := newFake(t)
	ctx := context.Background()
	mine, _ := srv.CreateCode(manifest(), "")
	other, _ := srv.CreateCode(manifest(), "")
	a, err := c.ConvertManifest(ctx, mine)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.ConvertManifest(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	foreign := srv.AddInstallation(b.ID, "octo-admin", "User", nil)
	app, _ := c.AsApp(a.ClientID, a.PrivateKey)
	if _, err := app.Installation(ctx, foreign); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("foreign installation: %v", err)
	}
	if _, err := app.InstallationToken(ctx, foreign, github.TokenRequest{}); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("token for foreign installation: %v", err)
	}
	srv.DeleteApp(a.ID)
	if _, err := app.Installations(ctx); !errors.Is(err, github.ErrUnauthorized) {
		t.Fatalf("deleted App: %v", err)
	}
}

func TestRepositoriesArePagedAndCapped(t *testing.T) {
	srv := githubtest.New()
	t.Cleanup(srv.Close)
	ctx := context.Background()
	for i := range 250 {
		srv.AddRepo("acme", fmt.Sprintf("r%03d", i), false)
	}
	code, _ := srv.CreateCode(manifest(), "acme")
	for _, tc := range []struct {
		pages     int
		want      int
		truncated bool
	}{{30, 250, false}, {2, 200, true}} {
		c, _ := github.New(srv.URL, github.WithMaxPages(tc.pages))
		if code != "" {
			creds, err := c.ConvertManifest(ctx, code)
			if err != nil {
				t.Fatal(err)
			}
			code = ""
			srv.AddInstallation(creds.ID, "acme", "Organization", nil)
		}
		ids := srv.Apps()
		app, _ := srv.App(ids[0])
		key := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(app.Key)})
		auth, err := c.AsApp(app.ClientID, key)
		if err != nil {
			t.Fatal(err)
		}
		inst := srv.Installations(app.ID)[0]
		tok, err := auth.InstallationToken(ctx, inst.ID, github.TokenRequest{Permissions: map[string]string{"metadata": "read"}})
		if err != nil {
			t.Fatal(err)
		}
		repos, truncated, err := c.InstallationRepositories(ctx, tok)
		if err != nil || len(repos) != tc.want || truncated != tc.truncated {
			t.Fatalf("pages %d: %d repos, truncated %v, %v", tc.pages, len(repos), truncated, err)
		}
	}
}

func TestUntrustedAnswers(t *testing.T) {
	ctx := context.Background()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	goodPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	smallPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(small)}))
	app := func(over map[string]string) string {
		f := map[string]string{
			"id": "1", "slug": `"my-app"`, "name": `"My App"`, "client_id": `"Iv1.abc"`,
			"owner": `{"login":"acme","type":"Organization"}`, "html_url": `"%s/apps/my-app"`,
			"client_secret": `"cs"`, "webhook_secret": `"ws"`, "pem": fmt.Sprintf("%q", goodPEM),
		}
		for k, v := range over {
			f[k] = v
		}
		parts := []string{}
		for k, v := range f {
			parts = append(parts, fmt.Sprintf("%q:%s", k, v))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	cases := map[string]struct {
		status int
		body   string
		want   error
	}{
		"ok":                {201, app(nil), nil},
		"other host":        {201, app(map[string]string{"html_url": `"https://evil.example/apps/my-app"`}), github.ErrBadAnswer},
		"bad slug":          {201, app(map[string]string{"slug": `"../x"`}), github.ErrBadAnswer},
		"short key":         {201, app(map[string]string{"pem": fmt.Sprintf("%q", smallPEM)}), github.ErrBadAnswer},
		"no webhook secret": {201, app(map[string]string{"webhook_secret": "null"}), github.ErrBadAnswer},
		"bad owner":         {201, app(map[string]string{"owner": `{"login":"a b","type":"User"}`}), github.ErrBadAnswer},
		"trailing data":     {201, app(nil) + "{}", github.ErrBadAnswer},
		"not json":          {201, "<html>", github.ErrBadAnswer},
		"too large":         {201, `{"x":"` + strings.Repeat("a", 5<<20) + `"}`, github.ErrBadAnswer},
		"rate limited":      {429, `{"message":"slow down"}`, github.ErrRateLimited},
		"server error":      {502, ``, github.ErrUnavailable},
		"redirect":          {302, ``, github.ErrBadAnswer},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 302 {
					w.Header().Set("Location", "https://evil.example/")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, strings.ReplaceAll(tc.body, "%s", srv.URL))
			}))
			defer srv.Close()
			c, _ := github.New(srv.URL)
			_, err := c.ConvertManifest(ctx, "abc123")
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestErrorsNeverContainTheCode(t *testing.T) {
	// Nothing listens here: the transport error would name the URL.
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c, _ := github.New(srv.URL)
	const code = "secretcode1234567890"
	_, err := c.ConvertManifest(context.Background(), code)
	if err == nil || strings.Contains(err.Error(), code) || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("error = %v", err)
	}
	if _, err := c.ConvertManifest(context.Background(), "../../x"); !errors.Is(err, github.ErrInvalid) {
		t.Fatalf("bad code: %v", err)
	}
}

func TestTokenIsRedacted(t *testing.T) {
	tok := github.NewToken("ghs_secretvalue", time.Now())
	var b strings.Builder
	slog.New(slog.NewTextHandler(&b, nil)).Info("x", "token", tok)
	for _, s := range []string{fmt.Sprint(tok), fmt.Sprintf("%v %+v %#v %s", tok, tok, tok, tok), b.String()} {
		if strings.Contains(s, "ghs_secretvalue") {
			t.Fatalf("token printed: %s", s)
		}
	}
	if tok.Value() != "ghs_secretvalue" {
		t.Fatal("value lost")
	}
}

func TestParsePrivateKeyPKCS8(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	if _, err := github.ParsePrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		t.Fatal(err)
	}
	if _, err := github.ParsePrivateKey([]byte("nonsense")); err == nil {
		t.Fatal("nonsense accepted")
	}
}
