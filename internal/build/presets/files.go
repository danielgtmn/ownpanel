package presets

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Size limits of the files that are read. Everything else is only looked
// up (does it exist as a plain file?), never opened.
const (
	maxPackageJSON = 512 << 10
	maxConfigFile  = 64 << 10
	maxVersionFile = 1 << 10
)

// tree reads the few files detection needs from the root of a source tree.
// Every file is untrusted: only plain files are read (a symbolic link is
// treated as missing, wherever it points), and only up to a limit.
type tree struct {
	fsys  fs.FS
	notes *[]string
}

var (
	errMissing = errors.New("missing")
	errTooBig  = errors.New("too large")
)

// isFile tells whether name is a plain file at the root of the tree.
func (t tree) isFile(name string) bool {
	fi, err := fs.Lstat(t.fsys, name)
	return err == nil && fi.Mode().IsRegular()
}

// isDir tells whether name is a folder (not a link to one).
func (t tree) isDir(name string) bool {
	fi, err := fs.Lstat(t.fsys, name)
	return err == nil && fi.IsDir()
}

// read returns the content of the plain file name, at most limit bytes. A
// link, a folder or a device is errMissing; a larger file errTooBig.
func (t tree) read(name string, limit int64) ([]byte, error) {
	fi, err := fs.Lstat(t.fsys, name)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errMissing
	}
	if fi.Size() > limit {
		return nil, errTooBig
	}
	f, err := t.fsys.Open(name)
	if err != nil {
		return nil, errMissing
	}
	defer f.Close()
	// The file may have been replaced since Lstat (a link to something
	// else, a device); check what was opened.
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, errMissing
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, errMissing
	}
	if int64(len(data)) > limit {
		return nil, errTooBig
	}
	return data, nil
}

// text reads a configuration file as text: missing, too large or not
// valid text is "" (with a note for the last two).
func (t tree) text(name string) string {
	data, err := t.read(name, maxConfigFile)
	switch {
	case errors.Is(err, errTooBig):
		t.note("The file %s is too large to be looked at; its settings were not taken into account.", name)
		return ""
	case err != nil:
		return ""
	case !utf8.Valid(data):
		t.note("The file %s is not readable text; its settings were not taken into account.", name)
		return ""
	}
	return string(data)
}

// firstFile returns the first of names that is a plain file.
func (t tree) firstFile(names ...string) string {
	for _, n := range names {
		if t.isFile(n) {
			return n
		}
	}
	return ""
}

func (t tree) note(format string, args ...any) {
	*t.notes = append(*t.notes, sprintf(format, args...))
}

// packageJSON is the part of package.json detection uses.
type packageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	PackageManager  string            `json:"packageManager"`
	Engines         json.RawMessage   `json:"engines"`
}

func (p *packageJSON) has(dep string) bool {
	if p == nil {
		return false
	}
	_, a := p.Dependencies[dep]
	_, b := p.DevDependencies[dep]
	return a || b
}

func (p *packageJSON) script(name string) bool {
	if p == nil {
		return false
	}
	return strings.TrimSpace(p.Scripts[name]) != ""
}

// nodeEngine returns engines.node, or "".
func (p *packageJSON) nodeEngine() string {
	if p == nil || len(p.Engines) == 0 {
		return ""
	}
	var e struct {
		Node string `json:"node"`
	}
	if json.Unmarshal(p.Engines, &e) != nil {
		return ""
	}
	return e.Node
}

// strictJSON decodes exactly one JSON value of valid UTF-8 into v.
func strictJSON(data []byte, v any) error {
	if !utf8.Valid(data) {
		return errors.New("not valid text")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("more than one value")
	}
	return nil
}

// readPackageJSON returns the parsed package.json, nil when there is none
// or it cannot be used (with a note); present tells whether a file of that
// name is there at all.
func (t tree) readPackageJSON() (pkg *packageJSON, present bool) {
	data, err := t.read("package.json", maxPackageJSON)
	switch {
	case errors.Is(err, errMissing):
		if _, lerr := fs.Lstat(t.fsys, "package.json"); lerr == nil {
			t.note("package.json is not a plain file and was not looked at.")
			return nil, true
		}
		return nil, false
	case errors.Is(err, errTooBig):
		t.note("package.json is larger than %d KB and was not looked at.", maxPackageJSON>>10)
		return nil, true
	}
	var p packageJSON
	if err := strictJSON(data, &p); err != nil {
		t.note("package.json could not be read (it is not valid); check it before building.")
		return nil, true
	}
	return &p, true
}

// codeLines returns the lines of a configuration file without those that
// are only a comment, so a commented-out setting does not count. The file
// is read as text and never run: a setting that is computed is not seen.
func codeLines(src string) []string {
	var out []string
	inBlock := false
	for line := range strings.SplitSeq(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if strings.Contains(trimmed, "*/") {
				inBlock = false
			}
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "//"), strings.HasPrefix(trimmed, "#"):
			continue
		case strings.HasPrefix(trimmed, "/*"):
			if !strings.Contains(trimmed, "*/") {
				inBlock = true
			}
			continue
		}
		out = append(out, line)
	}
	return out
}

// jsString matches a quoted string value of a key in a JS or TS
// configuration: key: 'value', key: "value" or key: `value` (without ${…}).
func jsString(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\s{,])` + regexp.QuoteMeta(key) + `\s*:\s*(?:'([^'\n]{1,255})'|"([^"\n]{1,255})"|` + "`([^`$\\n]{1,255})`" + `)`)
}

// findJSString returns the one value key has in src. When the key is
// missing, or appears with different values (in different branches of
// the configuration, say), it returns "" and whether it appeared at all.
func findJSString(src, key string) (value string, seen bool) {
	re := jsString(key)
	for _, line := range codeLines(src) {
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			v := m[1] + m[2] + m[3]
			if seen && v != value {
				return "", true
			}
			value, seen = v, true
		}
	}
	return value, seen
}

var (
	tomlTableRe = regexp.MustCompile(`^\s*\[`)
)

// findTOMLString returns a top-level string key of a TOML file (before the
// first table), matched line by line.
func findTOMLString(src, key string) string {
	re := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=\s*(?:"([^"\n]{1,255})"|'([^'\n]{1,255})')\s*(?:#.*)?$`)
	for line := range strings.SplitSeq(src, "\n") {
		if tomlTableRe.MatchString(line) {
			break
		}
		if m := re.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			return m[1] + m[2]
		}
	}
	return ""
}

// findYAMLString returns a top-level key of a YAML file (no indentation),
// plain or quoted, matched line by line.
func findYAMLString(src, key string) string {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(key) + `\s*:\s*(?:"([^"\n]{1,255})"|'([^'\n]{1,255})'|([^\s"'#][^\s#]{0,254}))\s*(?:#.*)?$`)
	for line := range strings.SplitSeq(src, "\n") {
		if m := re.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			return m[1] + m[2] + m[3]
		}
	}
	return ""
}

// hasTOMLKey and hasYAMLKey tell whether a top-level key is set at all.
func hasTOMLKey(src, key string) bool {
	re := regexp.MustCompile(`(?i)^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	for line := range strings.SplitSeq(src, "\n") {
		if tomlTableRe.MatchString(line) {
			break
		}
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

func hasYAMLKey(src, key string) bool {
	re := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(key) + `\s*:`)
	for line := range strings.SplitSeq(src, "\n") {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}
