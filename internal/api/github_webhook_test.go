package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/github/githubtest"
	"github.com/danielgtmn/ownpanel/internal/panel"
)

func TestGitHubDeliveriesOverHTTP(t *testing.T) {
	e, gh := newGitHubEnv(t)
	inst := connectAcme(t, e)
	if rec := e.as("PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`); rec.Code != 200 {
		t.Fatalf("site: %d %s", rec.Code, rec.Body)
	}
	repoID := repoIDOf(t, e, inst)
	link := fmt.Sprintf(`{"installation_id":%d,"repository_id":%d,"branch":"main","publish_on_push":true}`, inst, repoID)
	if rec := e.as("PUT", "/api/sites/blog/source", link); rec.Code != 200 {
		t.Fatalf("link: %d %s", rec.Code, rec.Body)
	}
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	hook := srv.URL + panel.GitHubWebhookPath
	appID := appIDOf(t, e)

	const sha = "6113728f27ae82c7b1a177c8d03f9e96e0adf246"
	body := gh.PushPayload(githubtest.Push{RepoID: repoID, InstallationID: inst, Ref: "refs/heads/main", After: sha,
		Message: "Fix <b>the</b> header", AuthorName: "Ann", AuthorLogin: "ann", Pusher: "ann"})
	send := func(h http.Header, body []byte) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", hook, bytes.NewReader(body))
		req.Header = h
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// GitHub's delivery: no session, no token, no CSRF header.
	code, answer := send(gh.Headers(appID, "push", "d-1", body), body)
	if code != 200 || answer != `{"result":"handled","runs":1}`+"\n" {
		t.Fatalf("push: %d %s", code, answer)
	}
	if strings.Contains(answer, sha) || strings.Contains(answer, "acme") {
		t.Fatalf("the answer echoes the delivery: %s", answer)
	}
	code, answer = send(gh.Headers(appID, "push", "d-1", body), body)
	if code != 200 || !strings.Contains(answer, `"duplicate"`) {
		t.Fatalf("again: %d %s", code, answer)
	}

	// The run is readable like the site.
	rec := e.as("GET", "/api/sites/blog/runs", "")
	runs := decode[panel.SiteRuns](t, rec)
	if rec.Code != 200 || len(runs.Runs) != 1 || runs.Runs[0].Commit != sha || runs.Runs[0].Message != "Fix <b>the</b> header" ||
		runs.Runs[0].Status != "queued" || runs.Runs[0].Waiting == "" || runs.PublishingAvailable {
		t.Fatalf("runs: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `<b>`) {
		t.Fatalf("markup is not escaped in the answer: %s", rec.Body)
	}
	rec = e.as("GET", "/api/sites/blog/source", "")
	if st := decode[panel.SiteSourceState](t, rec); st.LastRun == nil || st.LastRun.ID != runs.Runs[0].ID {
		t.Fatalf("source: %s", rec.Body)
	}
	if rec := e.do(req{method: "GET", path: "/api/sites/blog/runs"}); rec.Code != 401 {
		t.Fatalf("runs without signing in: %d", rec.Code)
	}

	// Everything refused gets the same answer.
	rejected := `{"error":"The delivery was not accepted"}` + "\n"
	wrongSecret := gh.Headers(appID, "push", "d-2", body)
	wrongSecret.Set(github.HeaderSignature, github.Sign([]byte("guess"), body))
	noSig := gh.Headers(appID, "push", "d-3", body)
	noSig.Del(github.HeaderSignature)
	unknown := gh.Headers(appID, "push", "d-4", body)
	unknown.Set(github.HeaderTargetID, "987654")
	unknown.Set(github.HeaderSignature, github.Sign([]byte("guess"), body))
	form := gh.Headers(appID, "push", "d-5", body)
	form.Set("Content-Type", "application/x-www-form-urlencoded")
	malformed := []byte(`{"ref":`)
	for name, c := range map[string]struct {
		h    http.Header
		body []byte
	}{
		"wrong signature":   {wrongSecret, body},
		"missing signature": {noSig, body},
		"unknown app":       {unknown, body},
		"form encoded":      {form, body},
		"malformed JSON":    {gh.Headers(appID, "push", "d-6", malformed), malformed},
	} {
		if code, answer := send(c.h, c.body); code != 400 || answer != rejected {
			t.Errorf("%s: %d %s", name, code, answer)
		}
	}

	// Too large: refused before anything else.
	big := append([]byte(`{"x":"`), bytes.Repeat([]byte("a"), MaxDeliveryBytes)...)
	big = append(big, `"}`...)
	if code, _ := send(gh.Headers(appID, "push", "d-7", big), big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: %d", code)
	}
	// Also when the size is not announced.
	req, _ := http.NewRequest("POST", hook, io.MultiReader(bytes.NewReader(big)))
	req.Header = gh.Headers(appID, "push", "d-8", big)
	req.ContentLength = -1
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large, chunked: %d", resp.StatusCode)
	}

	if rec := e.as("GET", "/api/sites/blog/runs", ""); len(decode[panel.SiteRuns](t, rec).Runs) != 1 {
		t.Fatalf("refused deliveries created runs: %s", rec.Body)
	}
}

// Only POST on the exact path skips authentication; the other rules stay.
func TestGitHubDeliveryRouteIsNarrow(t *testing.T) {
	e := newEnv(t)
	for _, r := range []req{
		{method: "GET", path: panel.GitHubWebhookPath},
		{method: "PUT", path: panel.GitHubWebhookPath, body: `{}`},
		{method: "POST", path: panel.GitHubWebhookPath + "/x", body: `{}`},
	} {
		if rec := e.do(r); rec.Code != 404 && rec.Code != 405 {
			t.Errorf("%s %s: %d %s", r.method, r.path, rec.Code, rec.Body)
		}
	}
	// A signed-in browser still needs the CSRF header elsewhere, and other
	// sites are still refused.
	if rec := e.do(req{method: "POST", path: "/api/github/refresh", cookie: e.cookie}); rec.Code != 403 {
		t.Fatalf("no CSRF header: %d", rec.Code)
	}
	if rec := e.do(req{method: "POST", path: "/api/github/refresh", cookie: e.cookie, csrf: true,
		headers: map[string]string{"Origin": "https://evil.example"}}); rec.Code != 403 {
		t.Fatalf("other origin: %d", rec.Code)
	}
	// The delivery route ignores sessions and tokens entirely: it is the
	// signature that counts.
	rec := e.do(req{method: "POST", path: panel.GitHubWebhookPath, body: `{}`, cookie: e.cookie, csrf: true,
		headers: map[string]string{"Content-Type": "application/json"}})
	if rec.Code != 400 {
		t.Fatalf("signed in: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Security-Policy") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", rec.Header())
	}
}

func appIDOf(t *testing.T, e *env) int64 {
	t.Helper()
	gh, err := e.svc.GitHubConnections(context.Background(), rootActor(t, e))
	if err != nil || len(gh.Apps) == 0 {
		t.Fatalf("connections: %+v %v", gh, err)
	}
	return gh.Apps[0].AppID
}

func repoIDOf(t *testing.T, e *env, inst int64) int64 {
	t.Helper()
	list, err := e.svc.GitHubRepositories(context.Background(), rootActor(t, e), inst)
	if err != nil || len(list.Repositories) == 0 {
		t.Fatalf("repositories: %+v %v", list, err)
	}
	return list.Repositories[0].ID
}
