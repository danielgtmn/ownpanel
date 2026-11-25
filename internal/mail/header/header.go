// Package header splits the header section of a message into fields the
// way port 25 reads it: tolerant of field names other parsers refuse
// (bytes ≥ 0x80, characters such as "(" before the colon), so that
// everything that reads a message (the checks, the filters) sees the same
// fields.
package header

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// ErrTooLarge: the header section is longer than the limit given to Read.
var ErrTooLarge = errors.New("header too large")

// Field is one header field as received (with continuation lines and the
// line endings).
type Field struct {
	Name string // as written
	Raw  []byte
}

// Value is the unfolded value of the field.
func (f Field) Value() string {
	v := string(f.Raw)
	if i := strings.IndexByte(v, ':'); i >= 0 {
		v = v[i+1:]
	}
	v = strings.NewReplacer("\r\n", "", "\n", "").Replace(v)
	return strings.TrimSpace(v)
}

// Read reads the header fields at the start of r and returns them with the
// offset of the body and the empty line that separated them (as received;
// "\r\n" when there was none). A line without a colon, or with blanks
// before it, ends the header, like most parsers do; it counts as body then.
// A header section longer than max bytes is ErrTooLarge.
func Read(r io.Reader, max int64) ([]Field, int64, []byte, error) {
	crlf := []byte("\r\n")
	br := bufio.NewReader(io.LimitReader(r, max+1))
	var fields []Field
	var off int64
	for {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				return fields, off, crlf, nil
			}
			return nil, 0, nil, err
		}
		if off+int64(len(line)) > max {
			return nil, 0, nil, ErrTooLarge
		}
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) == 0 {
			return fields, off + int64(len(line)), line, nil
		}
		if (line[0] == ' ' || line[0] == '\t') && len(fields) > 0 {
			last := &fields[len(fields)-1]
			last.Raw = append(last.Raw, line...)
			off += int64(len(line))
			continue
		}
		i := bytes.IndexByte(trimmed, ':')
		if i <= 0 || bytes.ContainsAny(trimmed[:i], " \t") {
			return fields, off, crlf, nil
		}
		fields = append(fields, Field{Name: string(trimmed[:i]), Raw: append([]byte(nil), line...)})
		off += int64(len(line))
		if err != nil {
			return fields, off, crlf, nil
		}
	}
}
