package managesieve

import (
	"bufio"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/mail/sieve"
)

// Limits of the command parser. Everything a command can hold is bounded:
// a line by the reader's buffer, a literal by maxLiteral, the arguments by
// maxArgs, so one command never takes more than about
// maxArgs*(maxLine+maxLiteral) bytes of memory.
const (
	// maxLine bounds one line of a command (without literals); it is the
	// size of the session's read buffer.
	maxLine = 8 << 10
	// maxArgs is the most arguments any command takes (PUTSCRIPT,
	// RENAMESCRIPT, HAVESPACE, AUTHENTICATE take two).
	maxArgs = 3
	// maxLiteral is the largest literal kept: a script.
	maxLiteral = sieve.MaxScriptBytes
	// maxSkip is the largest literal read and dropped (answered with
	// QUOTA/MAXSIZE); a larger one ends the connection instead.
	maxSkip = 1 << 20
	// maxNumber: number = 1*DIGIT, < 4294967296 (RFC 5804 4).
	maxNumber = 1<<32 - 1
)

// Fatal parser errors: the connection cannot continue.
var (
	errLineTooLong    = errors.New("line too long")
	errLiteralTooLong = errors.New("literal too long")
)

// syntaxError is a command the parser could not read. The line it was on
// is consumed, so the session can answer NO and go on; a literal announced
// later on that line is not read, and its bytes then arrive as commands of
// their own (and fail), as with any client that does not follow the
// protocol.
type syntaxError struct{ msg string }

func (e *syntaxError) Error() string { return e.msg }

func syntax(msg string) error { return &syntaxError{msg} }

// arg is one argument: a string (quoted or literal) or a number.
type arg struct {
	str   string
	num   uint32
	isNum bool
	// tooBig: a literal over maxLiteral that was skipped; str is empty.
	tooBig bool
}

// command is one parsed command line.
type command struct {
	name string // upper case; "" for an empty line
	args []arg
}

// tooBig tells whether a literal of the command was skipped.
func (c *command) tooBig() bool {
	for _, a := range c.args {
		if a.tooBig {
			return true
		}
	}
	return false
}

// parser reads commands from r. r's buffer size bounds the line length
// (ReadSlice fails with bufio.ErrBufferFull beyond it).
type parser struct {
	r    *bufio.Reader
	line []byte
	pos  int
}

// readLine reads the next line without its line end into p.line.
func (p *parser) readLine() error {
	b, err := p.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return errLineTooLong
	}
	if err != nil {
		if err == io.EOF && len(b) > 0 {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	b = b[:len(b)-1]
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	// ReadSlice's buffer is reused by the next read.
	p.line = append(p.line[:0], b...)
	p.pos = 0
	return nil
}

// readCommand reads one command: its name and arguments, following
// literals over several lines.
func (p *parser) readCommand() (command, error) {
	if err := p.readLine(); err != nil {
		return command{}, err
	}
	start := p.pos
	for p.pos < len(p.line) && isAlpha(p.line[p.pos]) {
		p.pos++
	}
	name := strings.ToUpper(string(p.line[start:p.pos]))
	if name == "" && len(p.line) > 0 {
		return command{}, syntax("Expected a command")
	}
	if len(name) > 32 {
		return command{}, syntax("Unknown command")
	}
	args, err := p.readArgs(false)
	return command{name: name, args: args}, err
}

// readResponse reads a client's SASL response line: one string, or "*"
// to abort (RFC 5804 2.1). A bare word is accepted as the string too.
func (p *parser) readResponse() (s string, abort bool, err error) {
	if err := p.readLine(); err != nil {
		return "", false, err
	}
	if len(p.line) > 0 && p.line[0] != '"' && p.line[0] != '{' {
		w := strings.TrimSpace(string(p.line))
		return w, w == "*", nil
	}
	args, err := p.readArgs(true)
	if err != nil {
		return "", false, err
	}
	if len(args) != 1 || args[0].isNum || args[0].tooBig {
		return "", false, syntax("Expected one string")
	}
	return args[0].str, args[0].str == "*", nil
}

// readArgs reads the arguments up to the end of the command. first means
// the line starts with an argument (no space before it).
func (p *parser) readArgs(first bool) ([]arg, error) {
	var args []arg
	for {
		if p.pos == len(p.line) {
			return args, nil
		}
		if !first {
			if p.line[p.pos] != ' ' {
				return nil, syntax("Expected a space")
			}
			for p.pos < len(p.line) && p.line[p.pos] == ' ' {
				p.pos++
			}
			if p.pos == len(p.line) {
				return args, nil // trailing spaces
			}
		}
		first = false
		if len(args) == maxArgs {
			// The rest of this line is consumed already; a literal on it
			// was not read, its bytes arrive as the next "command".
			return nil, syntax("Too many arguments")
		}
		a, err := p.readArg()
		if err != nil {
			return nil, err
		}
		args = append(args, a)
	}
}

func (p *parser) readArg() (arg, error) {
	switch c := p.line[p.pos]; {
	case c == '"':
		return p.quoted()
	case c == '{':
		return p.literal()
	case c >= '0' && c <= '9':
		start := p.pos
		for p.pos < len(p.line) && p.line[p.pos] >= '0' && p.line[p.pos] <= '9' {
			p.pos++
		}
		n, err := strconv.ParseUint(string(p.line[start:p.pos]), 10, 64)
		if err != nil || n > maxNumber {
			return arg{}, syntax("Number too large")
		}
		return arg{num: uint32(n), isNum: true}, nil
	}
	return arg{}, syntax("Expected a string or a number")
}

// quoted reads a quoted string; only \" and \\ are escapes, and CR, LF
// and NUL cannot occur (RFC 5804 4).
func (p *parser) quoted() (arg, error) {
	p.pos++ // "
	var b strings.Builder
	for p.pos < len(p.line) {
		c := p.line[p.pos]
		p.pos++
		switch c {
		case '"':
			s := b.String()
			if !utf8.ValidString(s) {
				return arg{}, syntax("String is not valid UTF-8")
			}
			return arg{str: s}, nil
		case '\\':
			if p.pos == len(p.line) || (p.line[p.pos] != '"' && p.line[p.pos] != '\\') {
				return arg{}, syntax("Invalid escape in string")
			}
			b.WriteByte(p.line[p.pos])
			p.pos++
		case 0, '\r':
			return arg{}, syntax("Invalid character in string")
		default:
			b.WriteByte(c)
		}
	}
	return arg{}, syntax("Unterminated string")
}

// literal reads {n+} or {n} at the end of a line, the n octets after it
// and the next line, where the command goes on. No continuation is sent
// for {n}: ManageSieve clients send literals without waiting.
func (p *parser) literal() (arg, error) {
	p.pos++ // {
	start := p.pos
	for p.pos < len(p.line) && p.line[p.pos] >= '0' && p.line[p.pos] <= '9' {
		p.pos++
	}
	digits := p.line[start:p.pos]
	if len(digits) == 0 || len(digits) > 10 {
		return arg{}, syntax("Invalid literal")
	}
	if p.pos < len(p.line) && p.line[p.pos] == '+' {
		p.pos++
	}
	if p.pos != len(p.line)-1 || p.line[p.pos] != '}' {
		return arg{}, syntax("Invalid literal")
	}
	n, err := strconv.ParseUint(string(digits), 10, 64)
	if err != nil || n > maxNumber {
		return arg{}, syntax("Invalid literal")
	}
	var a arg
	switch {
	case n > maxSkip:
		return arg{}, errLiteralTooLong
	case n > maxLiteral:
		if _, err := io.CopyN(io.Discard, p.r, int64(n)); err != nil {
			return arg{}, eof(err)
		}
		a.tooBig = true
	default:
		buf := make([]byte, n)
		if _, err := io.ReadFull(p.r, buf); err != nil {
			return arg{}, eof(err)
		}
		if !utf8.Valid(buf) {
			// Read the rest of the command before refusing it.
			if err := p.readLine(); err != nil {
				return arg{}, err
			}
			return arg{}, syntax("String is not valid UTF-8")
		}
		a.str = string(buf)
	}
	if err := p.readLine(); err != nil {
		return arg{}, err
	}
	return a, nil
}

func eof(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

func isAlpha(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

// quote returns s as a ManageSieve string: quoted when it is short and
// plain, a literal otherwise.
func quote(s string) string {
	if len(s) <= 1024 && !strings.ContainsAny(s, "\r\n\x00") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return literal(s)
}

// literal returns s as a literal ({n} CRLF s).
func literal(s string) string {
	return "{" + strconv.Itoa(len(s)) + "}\r\n" + s
}
