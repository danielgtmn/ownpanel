package build

import (
	"strings"
	"testing"
)

func TestImagesArePinned(t *testing.T) {
	seen := map[string]bool{}
	for _, img := range Images {
		if !ValidImage(img) {
			t.Errorf("%+v is not a pinned image", img)
		}
		if seen[img.Name] {
			t.Errorf("%s twice", img.Name)
		}
		seen[img.Name] = true
	}
	for _, ref := range []string{"docker.io/library/node:22", "node@sha256:" + strings.Repeat("a", 64), "docker.io/library/node@sha256:abc"} {
		if ValidImage(Image{Name: "x", Ref: ref}) {
			t.Errorf("%s accepted", ref)
		}
	}
}

func TestCleanOutput(t *testing.T) {
	good := map[string]string{"dist": "dist", "./public/": "public", ".": ".", "a/../b": "b"}
	for in, want := range good {
		if got, err := CleanOutput(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "/dist", "../x", "a/../../x", "a\\b", "a\x00b"} {
		if _, err := CleanOutput(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestResolveLimits(t *testing.T) {
	l, err := Limits{}.Resolve(DefaultLimits, MaxLimits)
	if err != nil || l != DefaultLimits {
		t.Errorf("%+v %v", l, err)
	}
	l, _ = Limits{CPUMillis: 1 << 30, MemoryBytes: 1 << 50}.Resolve(DefaultLimits, MaxLimits)
	if l.CPUMillis != MaxLimits.CPUMillis || l.MemoryBytes != MaxLimits.MemoryBytes {
		t.Errorf("%+v", l)
	}
	for _, bad := range []Limits{{Seconds: -1}, {MemoryBytes: 1 << 20}, {Processes: 2}, {CPUMillis: 10}} {
		if _, err := bad.Resolve(DefaultLimits, MaxLimits); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestSpecValidateNamesNoValues(t *testing.T) {
	s := Spec{ID: 1, Site: "a", Image: "node-22", Command: "x", Output: ".",
		Secrets: map[string]string{"TOKEN": strings.Repeat("v", MaxEnvValueBytes+1)}}
	err := s.Validate()
	if err == nil || strings.Contains(err.Error(), "vvvv") || !strings.Contains(err.Error(), "TOKEN") {
		t.Errorf("%v", err)
	}
	s.Secrets = map[string]string{"TOKEN": "x"}
	s.Env = map[string]string{"TOKEN": "y"}
	if s.Validate() == nil {
		t.Errorf("a variable given twice was accepted")
	}
	if str := (Spec{ID: 1, Command: "SECRET"}).String(); strings.Contains(str, "SECRET") {
		t.Errorf("String shows the command")
	}
}
