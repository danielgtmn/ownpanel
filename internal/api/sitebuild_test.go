package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/build"
	"github.com/danielgtmn/ownpanel/internal/build/buildtest"
	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/store"
)

func buildBody(t *testing.T, settings string, source []byte) (string, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if settings != "" {
		w, _ := mw.CreateFormField("settings")
		w.Write([]byte(settings))
	}
	if source != nil {
		w, _ := mw.CreateFormFile("source", "site.tar.gz")
		w.Write(source)
	}
	mw.Close()
	return mw.FormDataContentType(), &buf
}

func TestBuildSiteRoute(t *testing.T) {
	fake := buildtest.Start(t)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := panel.New(panel.Config{
		Store: st, Deploys: &deploy.Manager{Base: filepath.Join(dir, "sites")}, Web: nopWeb{},
		Caddy:         caddy.Options{AdminListen: "127.0.0.1:2019", TLSMode: caddy.TLSOff, HTTPPort: 8080, HTTPSPort: 8443},
		SetupCodeFile: filepath.Join(dir, "setup-code"),
		Builds:        fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.EnsureSetupCode(context.Background())
	e := &env{t: t, h: (&Server{Panel: svc}).Handler(), svc: svc, st: st, code: filepath.Join(dir, "setup-code")}
	e.cookie = e.setup("root", "root-password-1")
	if rec := e.as("PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`); rec.Code != 200 {
		t.Fatalf("site: %d %s", rec.Code, rec.Body)
	}
	post := func(cookie, contentType string, body *bytes.Buffer) *httptest.ResponseRecorder {
		hr := httptest.NewRequest("POST", "/api/sites/blog/builds", body)
		hr.Header.Set("Content-Type", contentType)
		hr.Header.Set(CSRFHeader, "1")
		hr.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, hr)
		return rec
	}
	src := buildtest.SiteArchive(map[string]string{"package.json": "{}"})
	settings := `{"command":"npm run build","output":"dist","image":"node-24","secrets":{"TOKEN":"s3cret-value"},"live":false}`

	ct, body := buildBody(t, settings, src)
	rec := post(e.cookie, ct, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("build: %d %s", rec.Code, rec.Body)
	}
	var res panel.BuildResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Status != "succeeded" || res.Publication == nil || res.Publication.Live || res.Publication.Release == "" {
		t.Fatalf("result: %s", rec.Body)
	}
	if specs := fake.Specs(); len(specs) != 1 || specs[0].Secrets["TOKEN"] != "s3cret-value" {
		t.Errorf("specs: %v", specs)
	}

	// A failed build answers 422 with its result.
	fake.Result = func(build.Spec) buildtest.Result {
		return buildtest.Result{State: build.StateFailed, Reason: build.ReasonExit, ExitCode: 1, Message: "the build command failed"}
	}
	ct, body = buildBody(t, settings, src)
	rec = post(e.cookie, ct, body)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"build_failed"`) ||
		!strings.Contains(rec.Body.String(), `"reason":"exit"`) {
		t.Fatalf("failed: %d %s", rec.Code, rec.Body)
	}

	// Bad requests; the secret never comes back.
	for _, c := range []struct {
		settings string
		source   []byte
		want     int
	}{
		{"", src, 400},
		{settings, nil, 400},
		{`{"command":"x","secrets":{"TOKEN":"s3cret-value"},"nope":1}`, src, 400},
		{`{"command":"x","output":"dist","image":"node-24","secrets":{"LD_PRELOAD":"s3cret-value"}}`, src, 400},
	} {
		ct, body := buildBody(t, c.settings, c.source)
		rec := post(e.cookie, ct, body)
		if rec.Code != c.want || strings.Contains(rec.Body.String(), "s3cret-value") {
			t.Errorf("%q: %d %s", c.settings, rec.Code, rec.Body)
		}
	}
	if rec := post(e.cookie, "application/gzip", bytes.NewBuffer(src)); rec.Code != 400 {
		t.Errorf("not multipart: %d", rec.Code)
	}

	// Members may not build from an upload.
	if rec := e.as("POST", "/api/users", `{"name":"bob","password":"bob-password-1","role":"member"}`); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	rec = e.do(req{method: "POST", path: "/api/login", body: `{"name":"bob","password":"bob-password-1"}`})
	if rec.Code != 200 {
		t.Fatalf("login bob: %d %s", rec.Code, rec.Body)
	}
	bob := sessionCookie(t, rec)
	ct, body = buildBody(t, settings, src)
	if rec := post(bob, ct, body); rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
		t.Errorf("member: %d %s", rec.Code, rec.Body)
	}
}
