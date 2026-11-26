package worker

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// envScript is the content of env.sh: one export per variable, the value
// in single quotes (in which the shell interprets nothing; a quote in the
// value ends the quotes, adds an escaped quote and opens them again). Names
// were validated (build.Spec.Validate), so they are plain identifiers.
func envScript(spec build.Spec) []byte {
	names := make([]string, 0, len(spec.Env)+len(spec.Secrets))
	values := make(map[string]string, len(spec.Env)+len(spec.Secrets))
	for _, vars := range []map[string]string{spec.Env, spec.Secrets} {
		for k, v := range vars {
			names = append(names, k)
			values[k] = v
		}
	}
	sort.Strings(names)
	var b bytes.Buffer
	b.WriteString("# Written by the ownpanel build service.\n")
	for _, k := range names {
		b.WriteString("export ")
		b.WriteString(k)
		b.WriteString("='")
		b.WriteString(strings.ReplaceAll(values[k], "'", `'\''`))
		b.WriteString("'\n")
	}
	return b.Bytes()
}

// writeControl writes the start script, the variables and the command of a
// build into dir. Only the worker's user can read them (the container's
// root is that user).
func writeControl(dir string, spec build.Spec) error {
	files := []struct {
		name string
		data []byte
	}{
		{controlInit, []byte(initScript)},
		{controlEnv, envScript(spec)},
		{controlCommand, []byte(spec.Command + "\n")},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// masker blanks out secret values in what passes through it. A value can
// be split over two writes, so the end of the data that could be the start
// of a value is held back until the next write (or Flush).
type masker struct {
	w       io.Writer
	secrets [][]byte // longest first
	first   [256]bool
	buf     []byte
}

const maskText = "***"

func newMasker(w io.Writer, secrets map[string]string) *masker {
	m := &masker{w: w}
	for _, v := range secrets {
		if len(v) >= build.MinSecretMask {
			m.secrets = append(m.secrets, []byte(v))
			m.first[v[0]] = true
		}
	}
	slices.SortFunc(m.secrets, func(a, b []byte) int { return len(b) - len(a) })
	return m
}

func (m *masker) Write(p []byte) (int, error) {
	if len(m.secrets) == 0 {
		return m.w.Write(p)
	}
	m.buf = append(m.buf, p...)
	if err := m.emit(false); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush writes out what was held back.
func (m *masker) Flush() error {
	if len(m.secrets) == 0 {
		return nil
	}
	return m.emit(true)
}

// emit writes buf up to the first place where a secret could begin but is
// cut off by the end of buf (all of it when final).
func (m *masker) emit(final bool) error {
	var out bytes.Buffer
	i, start := 0, 0
scan:
	for i < len(m.buf) {
		if !m.first[m.buf[i]] {
			i++
			continue
		}
		rest := m.buf[i:]
		// First: could what is held here still grow into a secret (also a
		// longer one that begins with a shorter one)? Then wait for more.
		if !final {
			for _, s := range m.secrets {
				if len(rest) < len(s) && bytes.HasPrefix(s, rest) {
					break scan
				}
			}
		}
		for _, s := range m.secrets { // longest first
			if bytes.HasPrefix(rest, s) {
				out.Write(m.buf[start:i])
				out.WriteString(maskText)
				i += len(s)
				start = i
				continue scan
			}
		}
		i++
	}
	out.Write(m.buf[start:i])
	m.buf = append(m.buf[:0], m.buf[i:]...)
	if out.Len() == 0 {
		return nil
	}
	_, err := m.w.Write(out.Bytes())
	return err
}

// logFile keeps what a build prints, up to a limit, and tells followers
// when more arrived.
type logFile struct {
	mu        sync.Mutex
	f         *os.File
	limit     int64
	size      int64
	truncated bool
	closed    bool
	changed   chan struct{}
	failed    error
}

const truncatedNote = "\n[ownpanel: the log was cut off here because it reached its size limit]\n"

func newLogFile(path string, limit int64) (*logFile, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	return &logFile{f: f, limit: limit, changed: make(chan struct{})}, nil
}

// Write never fails towards the build: what does not fit is dropped.
func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.truncated || l.failed != nil {
		return len(p), nil
	}
	room := l.limit - l.size
	chunk := p
	if int64(len(chunk)) > room {
		chunk = chunk[:room]
	}
	n, err := l.f.Write(chunk)
	l.size += int64(n)
	if err != nil {
		l.failed = err
	} else if len(chunk) < len(p) {
		l.truncated = true
		n, _ := l.f.Write([]byte(truncatedNote))
		l.size += int64(n)
	}
	l.notify()
	return len(p), nil
}

// Note adds a line of the worker's own to the log (beyond the limit).
func (l *logFile) Note(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	n, _ := l.f.Write([]byte("\n[ownpanel: " + text + "]\n"))
	l.size += int64(n)
	l.notify()
}

func (l *logFile) notify() {
	close(l.changed)
	l.changed = make(chan struct{})
}

// Close ends the log: followers stop after reading what is there.
func (l *logFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	err := l.f.Close()
	l.notify()
	return err
}

// state returns the size, whether output was dropped, whether the log has
// ended, and a channel that is closed on the next change.
func (l *logFile) state() (size int64, truncated, closed bool, changed <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.size, l.truncated, l.closed, l.changed
}
