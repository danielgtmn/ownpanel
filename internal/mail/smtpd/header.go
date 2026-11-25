package smtpd

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/textproto"
	"strings"
)

// maxHeaderBytes bounds the header of an incoming message; a larger one
// is refused (no real mail has one).
const maxHeaderBytes = 1 << 20

var errHeaderTooLarge = errors.New("header too large")

// field is one header field as received (with continuation lines and the
// line endings).
type field struct {
	name string // as written
	raw  []byte
}

// value is the unfolded value of the field.
func (f field) value() string {
	v := string(f.raw)
	if i := strings.IndexByte(v, ':'); i >= 0 {
		v = v[i+1:]
	}
	v = strings.NewReplacer("\r\n", "", "\n", "").Replace(v)
	return strings.TrimSpace(v)
}

// readHeader reads the header fields at the start of r and returns them
// with the offset of the body and the empty line that separated them (as
// received; "\r\n" when there was none). Lines without a colon before the
// empty line end the header, like most parsers do; they count as body
// then.
func readHeader(r io.Reader) ([]field, int64, []byte, error) {
	crlf := []byte("\r\n")
	br := bufio.NewReader(io.LimitReader(r, maxHeaderBytes+1))
	var fields []field
	var off int64
	for {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				return fields, off, crlf, nil
			}
			return nil, 0, nil, err
		}
		if off+int64(len(line)) > maxHeaderBytes {
			return nil, 0, nil, errHeaderTooLarge
		}
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) == 0 {
			return fields, off + int64(len(line)), line, nil
		}
		if (line[0] == ' ' || line[0] == '\t') && len(fields) > 0 {
			last := &fields[len(fields)-1]
			last.raw = append(last.raw, line...)
			off += int64(len(line))
			continue
		}
		i := bytes.IndexByte(trimmed, ':')
		if i <= 0 || bytes.ContainsAny(trimmed[:i], " \t") {
			return fields, off, crlf, nil
		}
		fields = append(fields, field{name: string(trimmed[:i]), raw: append([]byte(nil), line...)})
		off += int64(len(line))
		if err != nil {
			return fields, off, crlf, nil
		}
	}
}

// mimeHeader returns the fields as a textproto.MIMEHeader.
func mimeHeader(fields []field) textproto.MIMEHeader {
	h := textproto.MIMEHeader{}
	for _, f := range fields {
		k := textproto.CanonicalMIMEHeaderKey(f.name)
		h[k] = append(h[k], f.value())
	}
	return h
}
