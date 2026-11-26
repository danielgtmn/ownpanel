package build

import (
	"fmt"
	"strings"
	"testing"
)

func TestSettingsClean(t *testing.T) {
	ok := Settings{Command: "npm ci\nnpm run build \n\n", Output: "./dist/", Image: "node-22",
		Env: []EnvVar{{Name: "PUBLIC_URL", Value: "https://example.org"}, {Name: "API_TOKEN", Value: "s3cret", Secret: true}}}
	got, err := ok.Clean(Images)
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "npm ci\nnpm run build" || got.Output != "dist" || len(got.Env) != 2 {
		t.Errorf("%#v", got)
	}
	if _, err := (Settings{Output: "."}).Clean(Images); err != nil {
		t.Errorf("no build: %v", err)
	}

	long := strings.Repeat("a", MaxEnvValueBytes)
	tests := []struct {
		name string
		s    Settings
		want string
	}{
		{"command too long", Settings{Command: strings.Repeat("x", MaxCommandBytes+1), Output: ".", Image: "node-22"}, "longer than"},
		{"command not text", Settings{Command: "a\xffb", Output: ".", Image: "node-22"}, "not valid text"},
		{"command with zero byte", Settings{Command: "a\x00b", Output: ".", Image: "node-22"}, "not allowed"},
		{"command with escape", Settings{Command: "echo \x1b[2J", Output: ".", Image: "node-22"}, "not allowed"},
		{"command with carriage return", Settings{Command: "a\rb", Output: ".", Image: "node-22"}, "not allowed"},
		{"command with line separator", Settings{Command: "a\u2028b", Output: ".", Image: "node-22"}, "not allowed"},
		{"command without image", Settings{Command: "make", Output: "."}, "choose the environment"},
		{"unknown image", Settings{Command: "make", Output: ".", Image: "ruby-3"}, "not available"},
		{"image by reference", Settings{Command: "make", Output: ".", Image: Images[0].Ref}, "not available"},
		{"no output", Settings{Output: " "}, "missing"},
		{"absolute output", Settings{Output: "/var/www"}, "absolute"},
		{"output leaves", Settings{Output: "../x"}, "leave"},
		{"output with .. inside", Settings{Output: "a/../b"}, "leave"},
		{"output with backslash", Settings{Output: `a\b`}, "not a valid path"},
		{"output with newline", Settings{Output: "a\nb"}, "not a valid path"},
		{"output too long", Settings{Output: strings.Repeat("a", MaxOutputPathBytes+1)}, "not a valid path"},
		{"lower-case name", Settings{Output: ".", Env: []EnvVar{{Name: "path", Value: "x"}}}, "not valid"},
		{"name with digit first", Settings{Output: ".", Env: []EnvVar{{Name: "1A", Value: "x"}}}, "not valid"},
		{"name with dash", Settings{Output: ".", Env: []EnvVar{{Name: "A-B", Value: "x"}}}, "not valid"},
		{"empty name", Settings{Output: ".", Env: []EnvVar{{Name: "", Value: "x"}}}, "not valid"},
		{"long name", Settings{Output: ".", Env: []EnvVar{{Name: strings.Repeat("A", MaxEnvNameBytes+1), Value: "x"}}}, "not valid"},
		{"PATH", Settings{Output: ".", Env: []EnvVar{{Name: "PATH", Value: "/tmp"}}}, "PATH"},
		{"LD_PRELOAD", Settings{Output: ".", Env: []EnvVar{{Name: "LD_PRELOAD", Value: "/x.so"}}}, "LD_PRELOAD"},
		{"GCONV_PATH", Settings{Output: ".", Env: []EnvVar{{Name: "GCONV_PATH", Value: "/x"}}}, "GCONV_PATH"},
		{"BASH_ENV", Settings{Output: ".", Env: []EnvVar{{Name: "BASH_ENV", Value: "/x"}}}, "BASH_ENV"},
		{"HOME", Settings{Output: ".", Env: []EnvVar{{Name: "HOME", Value: "/x"}}}, "HOME"},
		{"OWNPANEL_", Settings{Output: ".", Env: []EnvVar{{Name: "OWNPANEL_SITE", Value: "x"}}}, "OWNPANEL_SITE"},
		{"twice", Settings{Output: ".", Env: []EnvVar{{Name: "A", Value: "x"}, {Name: "A", Value: "y", Secret: true}}}, "twice"},
		{"value too long", Settings{Output: ".", Env: []EnvVar{{Name: "A", Value: long + "a"}}}, "longer than"},
		{"value with zero byte", Settings{Output: ".", Env: []EnvVar{{Name: "A", Value: "a\x00"}}}, "not valid text"},
		{"short secret", Settings{Output: ".", Env: []EnvVar{{Name: "A", Value: "abc", Secret: true}}}, "at least"},
		{"too many", Settings{Output: ".", Env: manyVars(MaxEnvVars+1, "x")}, "more than"},
		{"too large together", Settings{Output: ".", Env: manyVars(MaxEnvBytes/MaxEnvValueBytes+1, long)}, "together"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.s.Clean(Images)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want an error with %q", err, tt.want)
			}
			for _, v := range tt.s.Env {
				if len(v.Value) >= 3 && strings.Contains(err.Error(), v.Value) {
					t.Errorf("the error shows a value: %v", err)
				}
			}
		})
	}
}

func manyVars(n int, value string) []EnvVar {
	out := make([]EnvVar, n)
	for i := range out {
		out[i] = EnvVar{Name: fmt.Sprintf("V%d", i), Value: value}
	}
	return out
}

// Everything Settings allow, the worker accepts.
func TestSettingsSpec(t *testing.T) {
	s, err := Settings{Command: "npm ci", Output: "dist/", Image: "node-24",
		Env: []EnvVar{{Name: "A", Value: "1"}, {Name: "TOKEN", Value: "secret", Secret: true}}}.Clean(Images)
	if err != nil {
		t.Fatal(err)
	}
	spec := s.Spec(7, "blog", Limits{})
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if spec.Env["A"] != "1" || spec.Secrets["TOKEN"] != "secret" || len(spec.Env) != 1 || len(spec.Secrets) != 1 || spec.Output != "dist" {
		t.Errorf("%+v %v %v", spec, spec.Env, spec.Secrets)
	}
	for name := range reservedEnv {
		if EnvNameAllowed(name) {
			t.Errorf("%s allowed by the settings but refused by the worker", name)
		}
	}
	if !EnvNameAllowed("NODE_OPTIONS") || !EnvNameAllowed("_X") || EnvNameAllowed("LD_LIBRARY_PATH") {
		t.Errorf("deny-list")
	}
}

func TestSettingsStringHidesValues(t *testing.T) {
	s := Settings{Command: "echo CMDTEXT", Output: ".", Image: "node-24", Env: []EnvVar{{Name: "TOKEN", Value: "VALUETEXT", Secret: true}}}
	for _, str := range []string{s.String(), fmt.Sprintf("%v %+v %#v %v", s, s, s, s.Env[0])} {
		if strings.Contains(str, "CMDTEXT") || strings.Contains(str, "VALUETEXT") || !strings.Contains(str, "TOKEN") {
			t.Errorf("%s", str)
		}
	}
	if got := s.SecretValues(); len(got) != 1 || got[0] != "VALUETEXT" {
		t.Errorf("%v", got)
	}
}

func TestMaskSecrets(t *testing.T) {
	secrets := []string{"secret-one", "secret-one-longer", "abc", ""}
	tests := map[string]string{
		"":                                 "",
		"nothing here":                     "nothing here",
		"token=secret-one!":                "token=***!",
		"secret-one-longer and secret-one": "*** and ***",
		"abc is too short to be hidden":    "abc is too short to be hidden",
		"secret-onesecret-one":             "******",
		"ends with secret-on":              "ends with secret-on",
		"ümlaut secret-one ümlaut":         "ümlaut *** ümlaut",
	}
	for in, want := range tests {
		if got := MaskSecrets(in, secrets); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if got := MaskSecrets("abcd", nil); got != "abcd" {
		t.Errorf("%q", got)
	}
}
