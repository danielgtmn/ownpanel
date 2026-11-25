package webui

import (
	"testing"
	"testing/fstest"
)

func TestBuildFollowsTheApp(t *testing.T) {
	if Build(nil) != "" || Build(fstest.MapFS{}) != "" {
		t.Fatal("no app, but a build")
	}
	a := Build(app)
	if len(a) != 16 || a != Build(app) {
		t.Fatalf("build %q", a)
	}
	other := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><script src=/assets/app-def.js></script>")}}
	if Build(other) == a {
		t.Fatal("a different app has the same build")
	}
}
