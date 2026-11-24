package hetzner

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxTXTChunk is the longest single character-string of a TXT record.
const maxTXTChunk = 255

// quoteTXT converts a plain TXT value to the wire format: the value in double
// quotes, `\` and `"` escaped with a backslash. A value longer than 255 bytes
// is split into several quoted sub-strings of at most 255 bytes (of the plain
// value, before escaping) separated by a space. Splits never cut a UTF-8
// character in half.
func quoteTXT(s string) string {
	if s == "" {
		return `""`
	}
	var b strings.Builder
	first := true
	for len(s) > 0 {
		n := len(s)
		if n > maxTXTChunk {
			n = maxTXTChunk
			for n > 0 && !utf8.RuneStart(s[n]) {
				n--
			}
			if n == 0 { // not valid UTF-8: cut at the limit
				n = maxTXTChunk
			}
		}
		if !first {
			b.WriteByte(' ')
		}
		first = false
		b.WriteByte('"')
		for i := 0; i < n; i++ {
			if c := s[i]; c == '\\' || c == '"' {
				b.WriteByte('\\')
			}
			b.WriteByte(s[i])
		}
		b.WriteByte('"')
		s = s[n:]
	}
	return b.String()
}

// unquoteTXT is the reverse of quoteTXT: it joins all quoted sub-strings into
// one plain value and resolves `\\`, `\"` and decimal `\DDD` escapes. A value
// that does not start with a double quote is returned unchanged.
func unquoteTXT(s string) (string, error) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, `"`) {
		return s, nil
	}
	var out []byte
	i := 0
	for i < len(t) {
		switch t[i] {
		case ' ', '\t':
			i++
		case '"':
			i++
			closed := false
			for i < len(t) {
				c := t[i]
				if c == '"' {
					closed = true
					i++
					break
				}
				if c == '\\' {
					if i+1 >= len(t) {
						return "", fmt.Errorf("invalid TXT value %q: dangling escape", s)
					}
					if d := t[i+1]; d >= '0' && d <= '9' {
						if i+3 >= len(t) || !isDigit(t[i+2]) || !isDigit(t[i+3]) {
							return "", fmt.Errorf("invalid TXT value %q: bad decimal escape", s)
						}
						v := int(d-'0')*100 + int(t[i+2]-'0')*10 + int(t[i+3]-'0')
						if v > 255 {
							return "", fmt.Errorf("invalid TXT value %q: bad decimal escape", s)
						}
						out = append(out, byte(v))
						i += 4
						continue
					}
					out = append(out, t[i+1])
					i += 2
					continue
				}
				out = append(out, c)
				i++
			}
			if !closed {
				return "", fmt.Errorf("invalid TXT value %q: missing closing quote", s)
			}
		default:
			return "", fmt.Errorf("invalid TXT value %q: text outside quotes", s)
		}
	}
	return string(out), nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
