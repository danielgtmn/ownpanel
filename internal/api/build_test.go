package api

import "testing"

func TestEveryAnswerNamesTheWebAppBuild(t *testing.T) {
	e := newEnv(t)
	e.h = (&Server{Panel: e.svc, Build: "abc123"}).Handler()
	for _, r := range []req{
		{method: "GET", path: "/api/session"},
		{method: "GET", path: "/api/sites"}, // 401: the page still learns it was updated
		{method: "GET", path: "/api/sites", cookie: e.cookie},
		{method: "GET", path: "/api/nothing-here"},
	} {
		rec := e.do(r)
		if got := rec.Header().Get(BuildHeader); got != "abc123" {
			t.Fatalf("%s %s (%d): build %q", r.method, r.path, rec.Code, got)
		}
	}
	e.h = (&Server{Panel: e.svc}).Handler()
	if rec := e.do(req{method: "GET", path: "/api/session"}); rec.Header().Get(BuildHeader) != "" {
		t.Fatalf("no app built in, but %q", rec.Header().Get(BuildHeader))
	}
}
