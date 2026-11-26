package transfer

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"mime"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// maxHeaderBytes bounds the header of an imported message, like delivery.
const maxHeaderBytes = 1 << 20

// maxKeywords bounds the keywords kept per message.
const maxKeywords = 50

var (
	errTooLarge      = errors.New("message is larger than the message size limit")
	errHeaderTooLong = errors.New("message header is too large")
)

// prepared is what reading a message found out.
type prepared struct {
	key      []byte // SHA-256 of the message without mbox metadata fields
	size     int64  // bytes written
	received time.Time
	date     time.Time // Date field
	subject  string
	// From mbox metadata fields (file sources only).
	flags   []string
	deleted bool
	labels  []string // X-Gmail-Labels
}

type prepOptions struct {
	// host is our mail host name: Authentication-Results fields that
	// claim to come from it are removed, as on delivery.
	host string
	// meta reads the mbox metadata fields (Status, X-Status, X-Keywords,
	// X-Mozilla-*, X-Gmail-Labels) into flags and labels and leaves them
	// out of the stored message. Without it they are kept (but never
	// counted in the key).
	meta bool
	// max is the message size limit (0: none).
	max int64
}

// isMetaField tells whether a field is mbox metadata, not part of the
// message.
func isMetaField(name string) bool {
	n := strings.ToLower(name)
	return n == "status" || n == "x-status" || n == "x-keywords" || n == "x-gmail-labels" ||
		strings.HasPrefix(n, "x-mozilla-")
}

// prepare copies a message from r to w with CRLF line endings, without
// Authentication-Results of our host (and mbox metadata when opts.meta),
// and computes its key.
func prepare(r io.Reader, w io.Writer, opts prepOptions) (*prepared, error) {
	p := &prepared{}
	h := sha256.New()
	cw := &countWriter{w: w, max: opts.max}
	out := io.MultiWriter(cw, h)
	br := bufio.NewReaderSize(r, 64<<10)

	// Header.
	var fields [][]byte
	var headerLen int
	var bodyStart []byte // a line that ended the header without being empty
	sawEmpty := false
	for {
		line, err := readLineMax(br, maxHeaderBytes-headerLen)
		if errors.Is(err, errHeaderTooLong) {
			return nil, err
		}
		if len(line) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		headerLen += len(line)
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) == 0 {
			sawEmpty = true
			break
		}
		if (line[0] == ' ' || line[0] == '\t') && len(fields) > 0 {
			fields[len(fields)-1] = append(fields[len(fields)-1], crlf(line)...)
		} else if i := bytes.IndexByte(trimmed, ':'); i <= 0 || bytes.ContainsAny(trimmed[:i], " \t") {
			bodyStart = line
			break
		} else {
			fields = append(fields, crlf(line))
		}
		if err != nil {
			break
		}
	}
	seen := map[string]bool{}
	for _, f := range fields {
		name, value := splitField(f)
		lname := strings.ToLower(name)
		if lname == "authentication-results" && opts.host != "" && auth.AuthServID(value) == strings.ToLower(opts.host) {
			continue
		}
		if isMetaField(name) {
			if opts.meta && !seen[lname] {
				// Only the first of each counts: our export puts its own
				// at the top.
				seen[lname] = true
				p.readMeta(lname, value)
			}
			if !opts.meta {
				if _, err := cw.Write(f); err != nil {
					return nil, err
				}
			}
			continue
		}
		switch lname {
		case "received":
			if p.received.IsZero() {
				if i := strings.LastIndexByte(value, ';'); i >= 0 {
					if t, err := mail.ParseDate(strings.TrimSpace(value[i+1:])); err == nil {
						p.received = t
					}
				}
			}
		case "date":
			if p.date.IsZero() {
				if t, err := mail.ParseDate(value); err == nil {
					p.date = t
				}
			}
		case "subject":
			if p.subject == "" {
				p.subject = decodeSubject(value)
			}
		}
		if _, err := out.Write(f); err != nil {
			return nil, err
		}
	}
	if sawEmpty {
		if _, err := out.Write([]byte("\r\n")); err != nil {
			return nil, err
		}
	} else if bodyStart != nil {
		if _, err := out.Write(crlf(bodyStart)); err != nil {
			return nil, err
		}
	}

	// Body.
	lw := &lineEndingWriter{w: out}
	if _, err := io.Copy(lw, br); err != nil {
		return nil, err
	}
	p.key = h.Sum(nil)
	p.size = cw.n
	p.flags = cleanFlags(p.flags)
	return p, nil
}

// readLineMax reads a line like ReadBytes('\n'), but gives up with
// errHeaderTooLong as soon as it holds more than max bytes, so an
// endless line is never buffered whole.
func readLineMax(br *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		b, err := br.ReadSlice('\n')
		if len(line)+len(b) > max {
			return nil, errHeaderTooLong
		}
		line = append(line, b...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

// splitField returns the name and the unfolded value of a raw field.
func splitField(f []byte) (string, string) {
	i := bytes.IndexByte(f, ':')
	if i < 0 {
		return "", ""
	}
	v := strings.NewReplacer("\r\n", "", "\n", "").Replace(string(f[i+1:]))
	return string(f[:i]), strings.TrimSpace(v)
}

// crlf returns line with a CRLF ending (a lone LF, a lone CR or none
// become CRLF).
func crlf(line []byte) []byte {
	t := bytes.TrimRight(line, "\r\n")
	out := make([]byte, 0, len(t)+2)
	out = append(out, t...)
	return append(out, '\r', '\n')
}

func (p *prepared) readMeta(name, value string) {
	switch name {
	case "status":
		if strings.ContainsRune(value, 'R') {
			p.flags = append(p.flags, storage.FlagSeen)
		}
	case "x-status":
		for _, c := range value {
			switch c {
			case 'A':
				p.flags = append(p.flags, storage.FlagAnswered)
			case 'F':
				p.flags = append(p.flags, storage.FlagFlagged)
			case 'T':
				p.flags = append(p.flags, storage.FlagDraft)
			case 'D':
				p.deleted = true
			}
		}
	case "x-keywords", "x-mozilla-keys":
		p.flags = append(p.flags, strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })...)
	case "x-mozilla-status":
		v, err := strconv.ParseUint(strings.TrimSpace(value), 16, 32)
		if err != nil {
			return
		}
		if v&0x0001 != 0 {
			p.flags = append(p.flags, storage.FlagSeen)
		}
		if v&0x0002 != 0 {
			p.flags = append(p.flags, storage.FlagAnswered)
		}
		if v&0x0004 != 0 {
			p.flags = append(p.flags, storage.FlagFlagged)
		}
		if v&0x0008 != 0 {
			p.deleted = true
		}
		if v&0x1000 != 0 {
			p.flags = append(p.flags, "$Forwarded")
		}
	case "x-gmail-labels":
		p.labels = splitLabels(value)
	}
}

// splitLabels splits X-Gmail-Labels: comma-separated, labels with a comma
// in double quotes.
func splitLabels(v string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, r := range v {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// cleanFlags keeps the system flags we store (\Seen, \Answered,
// \Flagged, \Draft) and valid keywords (at most maxKeywords); others are
// dropped, not the message.
func cleanFlags(in []string) []string {
	var out []string
	keywords := 0
	for _, f := range in {
		n, err := storage.NormalizeFlags([]string{f})
		if err != nil || len(n) != 1 {
			continue
		}
		f = n[0]
		if strings.HasPrefix(f, `\`) {
			if f == storage.FlagDeleted {
				continue
			}
		} else {
			if keywords >= maxKeywords {
				continue
			}
			keywords++
		}
		out = append(out, f)
	}
	n, _ := storage.NormalizeFlags(out)
	return n
}

var wordDecoder = &mime.WordDecoder{}

// decodeSubject decodes encoded words and shortens the subject for the
// list of failures.
func decodeSubject(v string) string {
	if d, err := wordDecoder.DecodeHeader(v); err == nil {
		v = d
	}
	return shorten(v, 80)
}

// shorten cuts s to at most n runes (adding "…"), on one line, valid
// UTF-8.
func shorten(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// countWriter counts bytes and refuses more than max.
type countWriter struct {
	w   io.Writer
	n   int64
	max int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	if c.max > 0 && c.n+int64(len(b)) > c.max {
		return 0, errTooLarge
	}
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

// lineEndingWriter turns lone LF into CRLF.
type lineEndingWriter struct {
	w      io.Writer
	lastCR bool
	buf    []byte
}

func (l *lineEndingWriter) Write(b []byte) (int, error) {
	l.buf = l.buf[:0]
	for _, c := range b {
		if c == '\n' && !l.lastCR {
			l.buf = append(l.buf, '\r')
		}
		l.buf = append(l.buf, c)
		l.lastCR = c == '\r'
	}
	if _, err := l.w.Write(l.buf); err != nil {
		return 0, err
	}
	return len(b), nil
}
