package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/danielgtmn/ownpanel/internal/api"
	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/mcp"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/store"
	"github.com/danielgtmn/ownpanel/internal/webui"
)

type noWebServer struct{}

func (noWebServer) Load(context.Context, any) error { return nil }

func TestComposeKeepsAPIProtectedAndWebPublic(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc, err := panel.New(panel.Config{Store: st, Deploys: &deploy.Manager{Base: dir}, Web: noWebServer{},
		Caddy: caddy.Options{TLSMode: caddy.TLSOff}, SetupCodeFile: filepath.Join(dir, "setup-code")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureSetupCode(context.Background()); err != nil {
		t.Fatal(err)
	}
	setupCode, _ := os.ReadFile(filepath.Join(dir, "setup-code"))
	_, session, err := svc.Setup(context.Background(), string(setupCode), panel.Credentials{Name: "root", Password: "root-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := svc.SessionActor(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.CreateToken(context.Background(), actor, "t", "write", nil)
	if err != nil {
		t.Fatal(err)
	}
	a := &api.Server{Panel: svc}
	app := fstest.MapFS{"index.html": {Data: []byte("<title>app</title>")}}
	srv := httptest.NewServer(compose(a.Handler(), webui.Handler(app), mcp.Handler(svc, "test")))
	defer srv.Close()

	do := func(path, token string) (int, string, string) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var sb strings.Builder
		buf := make([]byte, 512)
		for {
			n, err := resp.Body.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return resp.StatusCode, resp.Header.Get("Content-Type"), sb.String()
	}

	if code, _, body := do("/", ""); code != 200 || !strings.Contains(body, "<title>app</title>") {
		t.Fatalf("/ without token: %d %q", code, body)
	}
	if code, _, body := do("/sites/blog", ""); code != 200 || !strings.Contains(body, "<title>app</title>") {
		t.Fatalf("deep link without token: %d %q", code, body)
	}
	if code, _, _ := do("/api/sites", ""); code != 401 {
		t.Fatalf("/api/sites without token: %d", code)
	}
	if code, _, _ := do("/api/sites", "wrong"); code != 401 {
		t.Fatalf("/api/sites with wrong token: %d", code)
	}
	if code, _, body := do("/api/sites", tok.Secret); code != 200 || body != "[]\n" {
		t.Fatalf("/api/sites with token: %d %q", code, body)
	}
	if code, ct, body := do("/mcp", ""); code != 401 || strings.Contains(body, "<title>") || ct != "application/json" {
		t.Fatalf("/mcp without token: %d %q %q", code, ct, body)
	}
	code, ct, body := do("/api/nope", tok.Secret)
	if code != 404 || ct != "application/json" || strings.Contains(body, "<title>") {
		t.Fatalf("unknown API path: %d %q %q", code, ct, body)
	}
}
