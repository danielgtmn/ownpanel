package build

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Settings are the build settings of one site as people enter them (#114):
// what to run, in which image, and which folder holds the result. They are
// stricter than a Spec, which is what the worker accepts: a Spec is built
// from Settings (Settings.Spec) after Settings.Clean.
type Settings struct {
	// Command is run by the image's /bin/sh in the source tree, one line
	// after another, stopping at the first failing one. Empty means the
	// site needs no build: the output folder is published as it is.
	Command string `json:"command"`
	// Output is the folder with the result, relative to the source tree
	// ("dist", "public/site", "." for the tree itself).
	Output string `json:"output"`
	// Image is the Name of one of Images. Needed when Command is set.
	Image string `json:"image,omitempty"`
	// Env are the environment variables of the build, in the order people
	// entered them.
	Env []EnvVar `json:"env,omitempty"`
}

// EnvVar is one environment variable of a build. The value of a secret one
// is sealed in the database, never shown again and blanked out in the
// build's log (MaskSecrets).
type EnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

// String and GoString keep the command and the values out of logs and
// error messages that print Settings.
func (s Settings) String() string {
	names := make([]string, len(s.Env))
	for i, v := range s.Env {
		names[i] = v.Name
	}
	return fmt.Sprintf("build settings (image %s, output %s, variables %s)", s.Image, s.Output, strings.Join(names, ","))
}

// GoString is String.
func (s Settings) GoString() string { return "build.Settings{" + s.String() + "}" }

// String shows the name only.
func (v EnvVar) String() string { return v.Name }

// GoString is String.
func (v EnvVar) GoString() string { return "build.EnvVar{" + v.Name + "}" }

// MaxOutputPathBytes bounds the output folder of Settings.
const MaxOutputPathBytes = 255

// deniedEnvPrefixes and deniedEnv are the variable names Settings refuse
// on top of those the worker refuses itself (reservedEnv, LD_*).
//
// The container is what keeps a build away from the server, and the build
// command is the site's own code anyway, so this list is not what protects
// the server. It keeps three promises the settings make:
//
//   - The image's own tools run the command. Variables in the build's
//     environment are in place before /bin/sh starts, so PATH, the dynamic
//     linker's variables (LD_*, GCONV_PATH) and the shell's start-up
//     variables (ENV, BASH_ENV, SHELLOPTS, IFS, PS4) would change what runs
//     before the first line of the command, invisibly in the settings and
//     in the log. A command that wants another PATH can say so in its own
//     text, where people see it.
//   - What the build service sets stays what it set: HOME, PWD, TMPDIR.
//   - OWNPANEL_* is the panel's: it tells builds about themselves
//     (OWNPANEL_BUILD today, more with #113), and a site's variable must
//     not pass itself off as one of those.
//
// Proxy variables (HTTP_PROXY and friends), NODE_OPTIONS and certificate
// paths stay allowed: they only change what the site's own build does with
// its own network access, which it can do from its command as well.
var (
	deniedEnvPrefixes = []string{"LD_", "OWNPANEL_"}
	deniedEnv         = map[string]bool{"PATH": true, "GCONV_PATH": true, "CDPATH": true}
)

// EnvNameAllowed tells whether name can be the name of a build's variable:
// upper-case letters, digits and _, not starting with a digit, and not one
// the build service or the panel keeps for itself.
func EnvNameAllowed(name string) bool {
	return validEnvName(name) && !deniedEnvName(name)
}

func validEnvName(name string) bool {
	if name == "" || len(name) > MaxEnvNameBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func deniedEnvName(name string) bool {
	if reservedEnv[name] || deniedEnv[name] {
		return true
	}
	for _, p := range deniedEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Clean checks s against the list of images and returns it in its plain
// form: the command with trailing white space removed, the output folder
// without "./" and a trailing "/". The messages name what is wrong without
// repeating values.
func (s Settings) Clean(images []Image) (Settings, error) {
	out := s
	out.Command = strings.TrimRightFunc(s.Command, unicode.IsSpace)
	if err := checkCommand(out.Command); err != nil {
		return Settings{}, err
	}
	output, err := cleanSettingsOutput(s.Output)
	if err != nil {
		return Settings{}, err
	}
	out.Output = output
	switch {
	case out.Image == "" && out.Command != "":
		return Settings{}, errors.New("choose the environment the build runs in")
	case out.Image != "":
		if _, ok := FindImage(images, out.Image); !ok {
			return Settings{}, errors.New("the chosen build environment is not available on this server")
		}
	}
	if err := checkEnv(s.Env); err != nil {
		return Settings{}, err
	}
	out.Env = append([]EnvVar(nil), s.Env...)
	return out, nil
}

// Validate is Clean against Images, without the result.
func (s Settings) Validate() error {
	_, err := s.Clean(Images)
	return err
}

// NeedsBuild tells whether a command has to run before the output folder
// can be published.
func (s Settings) NeedsBuild() bool { return strings.TrimSpace(s.Command) != "" }

func checkCommand(cmd string) error {
	switch {
	case len(cmd) > MaxCommandBytes:
		return fmt.Errorf("the build command is longer than %d KB", MaxCommandBytes>>10)
	case !utf8.ValidString(cmd):
		return errors.New("the build command is not valid text")
	}
	for _, r := range cmd {
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return errors.New("the build command contains characters that are not allowed (only text, tabs and line breaks)")
		}
	}
	return nil
}

// cleanSettingsOutput is CleanOutput, but stricter: no ".." anywhere (even
// where it would stay inside the tree), no empty names, no control
// characters, at most MaxOutputPathBytes.
func cleanSettingsOutput(output string) (string, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return "", errors.New("the output folder is missing")
	}
	if len(output) > MaxOutputPathBytes || !utf8.ValidString(output) {
		return "", errors.New("the output folder is not a valid path")
	}
	for _, r := range output {
		if unicode.IsControl(r) || r == '\\' {
			return "", errors.New("the output folder is not a valid path")
		}
	}
	if strings.HasPrefix(output, "/") {
		return "", errors.New("the output folder must be a folder inside the site's files, not an absolute path")
	}
	trimmed := strings.TrimSuffix(output, "/")
	for part := range strings.SplitSeq(trimmed, "/") {
		if part == ".." {
			return "", errors.New("the output folder must not leave the site's files")
		}
	}
	clean, err := CleanOutput(trimmed)
	if err != nil {
		return "", err
	}
	return clean, nil
}

func checkEnv(vars []EnvVar) error {
	if len(vars) > MaxEnvVars {
		return fmt.Errorf("more than %d environment variables", MaxEnvVars)
	}
	seen := make(map[string]bool, len(vars))
	total := 0
	for _, v := range vars {
		switch {
		case !validEnvName(v.Name):
			return fmt.Errorf("the name of an environment variable is not valid (capital letters, digits and _, not starting with a digit, at most %d characters)", MaxEnvNameBytes)
		case deniedEnvName(v.Name):
			return fmt.Errorf("the environment variable %s is set by the build environment and cannot be changed", v.Name)
		case seen[v.Name]:
			return fmt.Errorf("the environment variable %s is given twice", v.Name)
		case len(v.Value) > MaxEnvValueBytes:
			return fmt.Errorf("the value of the environment variable %s is longer than %d KB", v.Name, MaxEnvValueBytes>>10)
		case !utf8.ValidString(v.Value) || strings.ContainsRune(v.Value, 0):
			return fmt.Errorf("the value of the environment variable %s is not valid text", v.Name)
		case v.Secret && len(v.Value) < MinSecretMask:
			return fmt.Errorf("the secret value of %s must be at least %d characters long, so it can be hidden in the build log", v.Name, MinSecretMask)
		}
		seen[v.Name] = true
		total += len(v.Name) + len(v.Value)
	}
	if total > MaxEnvBytes {
		return fmt.Errorf("the environment variables are larger than %d KB together", MaxEnvBytes>>10)
	}
	return nil
}

// Spec turns cleaned settings (Clean) into what the worker is asked to
// run as build id of site.
func (s Settings) Spec(id uint64, site string, limits Limits) Spec {
	spec := Spec{ID: id, Site: site, Image: s.Image, Command: s.Command, Output: s.Output, Limits: limits}
	for _, v := range s.Env {
		m := &spec.Env
		if v.Secret {
			m = &spec.Secrets
		}
		if *m == nil {
			*m = map[string]string{}
		}
		(*m)[v.Name] = v.Value
	}
	return spec
}

// SecretValues returns the values of the secret variables.
func (s Settings) SecretValues() []string {
	var out []string
	for _, v := range s.Env {
		if v.Secret {
			out = append(out, v.Value)
		}
	}
	return out
}

// MaskSecrets replaces every secret value (MinSecretMask characters and
// longer) in line with "***". Where two values overlap, the longer one
// wins. The build worker masks the log as it streams (the same rule); this
// is for single lines the panel shows or stores itself, such as the end of
// a log in a build's status.
func MaskSecrets(line string, secrets []string) string {
	var vals []string
	for _, s := range secrets {
		if len(s) >= MinSecretMask {
			vals = append(vals, s)
		}
	}
	if len(vals) == 0 || line == "" {
		return line
	}
	// Longest first, so a value that begins with a shorter one is hidden
	// as a whole.
	slices.SortStableFunc(vals, func(a, b string) int { return len(b) - len(a) })
	var b strings.Builder
	start := 0
	for i := 0; i < len(line); {
		matched := false
		for _, v := range vals {
			if strings.HasPrefix(line[i:], v) {
				b.WriteString(line[start:i])
				b.WriteString("***")
				i += len(v)
				start = i
				matched = true
				break
			}
		}
		if !matched {
			i++
		}
	}
	if start == 0 {
		return line
	}
	b.WriteString(line[start:])
	return b.String()
}
