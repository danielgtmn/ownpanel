// Package sieve is the mail filter language of the mail service (RFC 5228)
// with exactly the extensions wiki/Groupware-Architecture.md (G2) lists:
// fileinto, mailbox, envelope, imap4flags, body, copy, variables,
// relational, date, vacation, vacation-seconds, include (personal scripts
// only), duplicate, spamtest, spamtestplus and regex. reject and ereject are
// not offered.
//
// Scripts are parsed and checked once (Compile) and run per delivery
// (Script.Run) against an Env that gives access to the message, the
// envelope and the mailbox. A run never touches the message or the
// mailbox itself: it returns the actions to take, and the caller (package
// deliver) carries them out. Runs are bounded in steps and time, so a
// script cannot hold up delivery.
//
// The package has no dependency on the rest of the mail service.
package sieve

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits of scripts.
const (
	// MaxScriptBytes bounds the source of one script.
	MaxScriptBytes = 64 << 10
	// maxNesting bounds nested blocks and tests.
	maxNesting = 32
	// maxStringList bounds the strings of one list.
	maxStringList = 1024
)

// Error is a problem with a script, with the line it is on.
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

func errorAt(line int, format string, args ...any) *Error {
	return &Error{Line: line, Msg: fmt.Sprintf(format, args...)}
}

type tokenKind int

const (
	tEOF tokenKind = iota
	tIdent
	tTag
	tNumber
	tString
	tLBracket
	tRBracket
	tLParen
	tRParen
	tComma
	tSemicolon
	tLBrace
	tRBrace
)

func (k tokenKind) String() string {
	switch k {
	case tEOF:
		return "end of script"
	case tIdent:
		return "identifier"
	case tTag:
		return "tag"
	case tNumber:
		return "number"
	case tString:
		return "string"
	case tLBracket:
		return `"["`
	case tRBracket:
		return `"]"`
	case tLParen:
		return `"("`
	case tRParen:
		return `")"`
	case tComma:
		return `","`
	case tSemicolon:
		return `";"`
	case tLBrace:
		return `"{"`
	case tRBrace:
		return `"}"`
	}
	return "token"
}

type token struct {
	kind tokenKind
	text string // identifier or tag name (lowercase), or string value
	num  int64
	line int
}

type lexer struct {
	src  string
	pos  int
	line int
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentChar(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }

// next returns the next token.
func (l *lexer) next() (token, error) {
	for {
		if l.pos >= len(l.src) {
			return token{kind: tEOF, line: l.line}, nil
		}
		c := l.src[l.pos]
		switch {
		case c == '\n':
			l.line++
			l.pos++
		case c == ' ' || c == '\t' || c == '\r':
			l.pos++
		case c == '#':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		case c == '/' && strings.HasPrefix(l.src[l.pos:], "/*"):
			end := strings.Index(l.src[l.pos+2:], "*/")
			if end < 0 {
				return token{}, errorAt(l.line, "comment is not closed")
			}
			l.line += strings.Count(l.src[l.pos:l.pos+2+end], "\n")
			l.pos += 2 + end + 2
		default:
			return l.token()
		}
	}
}

func (l *lexer) token() (token, error) {
	c := l.src[l.pos]
	line := l.line
	single := map[byte]tokenKind{'[': tLBracket, ']': tRBracket, '(': tLParen, ')': tRParen,
		',': tComma, ';': tSemicolon, '{': tLBrace, '}': tRBrace}
	if k, ok := single[c]; ok {
		l.pos++
		return token{kind: k, line: line}, nil
	}
	switch {
	case c == ':':
		l.pos++
		start := l.pos
		if l.pos >= len(l.src) || !isIdentStart(l.src[l.pos]) {
			return token{}, errorAt(line, "a tag needs a name after the colon")
		}
		for l.pos < len(l.src) && isIdentChar(l.src[l.pos]) {
			l.pos++
		}
		return token{kind: tTag, text: strings.ToLower(l.src[start:l.pos]), line: line}, nil
	case c >= '0' && c <= '9':
		start := l.pos
		for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
			l.pos++
		}
		n, err := strconv.ParseInt(l.src[start:l.pos], 10, 64)
		if err != nil || n > 1<<40 {
			return token{}, errorAt(line, "number too large")
		}
		if l.pos < len(l.src) {
			mult := int64(1)
			switch l.src[l.pos] {
			case 'K', 'k':
				mult = 1 << 10
			case 'M', 'm':
				mult = 1 << 20
			case 'G', 'g':
				mult = 1 << 30
			}
			if mult > 1 {
				l.pos++
				n *= mult
			}
		}
		if n > 1<<50 {
			return token{}, errorAt(line, "number too large")
		}
		return token{kind: tNumber, num: n, line: line}, nil
	case c == '"':
		return l.quoted()
	case isIdentStart(c):
		start := l.pos
		for l.pos < len(l.src) && isIdentChar(l.src[l.pos]) {
			l.pos++
		}
		word := strings.ToLower(l.src[start:l.pos])
		if word == "text" && l.pos < len(l.src) && l.src[l.pos] == ':' {
			l.pos++
			return l.multiline(line)
		}
		return token{kind: tIdent, text: word, line: line}, nil
	}
	r, _ := utf8.DecodeRuneInString(l.src[l.pos:])
	return token{}, errorAt(line, "unexpected character %q", r)
}

// quoted reads a quoted string: \" and \\ are escapes, any other
// backslash is dropped (RFC 5228 2.4.2).
func (l *lexer) quoted() (token, error) {
	line := l.line
	l.pos++ // opening quote
	var b strings.Builder
	for {
		if l.pos >= len(l.src) {
			return token{}, errorAt(line, "string is not closed")
		}
		c := l.src[l.pos]
		switch c {
		case '"':
			l.pos++
			return token{kind: tString, text: b.String(), line: line}, nil
		case '\\':
			if l.pos+1 >= len(l.src) {
				return token{}, errorAt(line, "string is not closed")
			}
			l.pos++
			c = l.src[l.pos]
			if c == '\n' {
				l.line++
			}
			b.WriteByte(c)
			l.pos++
		default:
			if c == '\n' {
				l.line++
			}
			b.WriteByte(c)
			l.pos++
		}
	}
}

// multiline reads the rest of "text:": optional blanks and a comment to
// the end of the line, then lines up to one that is just ".". A leading
// ".." stands for ".".
func (l *lexer) multiline(line int) (token, error) {
	for l.pos < len(l.src) && (l.src[l.pos] == ' ' || l.src[l.pos] == '\t') {
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '#' {
		for l.pos < len(l.src) && l.src[l.pos] != '\n' {
			l.pos++
		}
	}
	if l.pos < len(l.src) && l.src[l.pos] == '\r' {
		l.pos++
	}
	if l.pos >= len(l.src) || l.src[l.pos] != '\n' {
		return token{}, errorAt(line, `"text:" must end its line`)
	}
	l.pos++
	l.line++
	var b strings.Builder
	for {
		if l.pos >= len(l.src) {
			return token{}, errorAt(line, `multi-line text is not closed with a line "."`)
		}
		end := strings.IndexByte(l.src[l.pos:], '\n')
		var ln string
		if end < 0 {
			ln = l.src[l.pos:]
			l.pos = len(l.src)
		} else {
			ln = l.src[l.pos : l.pos+end]
			l.pos += end + 1
			l.line++
		}
		ln = strings.TrimSuffix(ln, "\r")
		if ln == "." {
			return token{kind: tString, text: b.String(), line: line}, nil
		}
		if strings.HasPrefix(ln, "..") {
			ln = ln[1:]
		}
		b.WriteString(ln)
		b.WriteString("\r\n")
	}
}

// Syntax tree.

type argKind int

const (
	argTag argKind = iota
	argNumber
	argStrings
)

type arg struct {
	kind argKind
	tag  string
	num  int64
	strs []string
	list bool // written as [ … ]
	line int
}

type node struct {
	name  string
	args  []arg
	tests []*node
	block []*node
	// hasBlock: a block followed (also an empty one).
	hasBlock bool
	// testList: the tests were written as "( … )".
	testList bool
	line     int
}

type parser struct {
	lx   lexer
	tok  token
	err  error
	deep int
}

func (p *parser) advance() {
	if p.err != nil {
		return
	}
	p.tok, p.err = p.lx.next()
}

func (p *parser) fail(format string, args ...any) {
	if p.err == nil {
		p.err = errorAt(p.tok.line, format, args...)
	}
}

func (p *parser) expect(k tokenKind) {
	if p.err != nil {
		return
	}
	if p.tok.kind != k {
		p.fail("expected %s, found %s", k, p.tok.kind)
		return
	}
	p.advance()
}

// parse turns the source into commands.
func parse(src string) ([]*node, error) {
	if len(src) > MaxScriptBytes {
		return nil, &Error{Msg: fmt.Sprintf("the script is larger than %d KiB", MaxScriptBytes>>10)}
	}
	if !utf8.ValidString(src) {
		return nil, &Error{Msg: "the script is not valid UTF-8"}
	}
	p := &parser{lx: lexer{src: src, line: 1}}
	p.advance()
	var cmds []*node
	for p.err == nil && p.tok.kind != tEOF {
		cmds = append(cmds, p.command())
	}
	if p.err != nil {
		return nil, p.err
	}
	return cmds, nil
}

func (p *parser) enter() bool {
	p.deep++
	if p.deep > maxNesting {
		p.fail("nested too deeply")
		return false
	}
	return true
}

func (p *parser) command() *node {
	if !p.enter() {
		return &node{}
	}
	defer func() { p.deep-- }()
	n := &node{line: p.tok.line}
	if p.tok.kind != tIdent {
		p.fail("expected a command, found %s", p.tok.kind)
		return n
	}
	n.name = p.tok.text
	p.advance()
	p.arguments(n)
	if p.err != nil {
		return n
	}
	switch p.tok.kind {
	case tSemicolon:
		p.advance()
	case tLBrace:
		p.advance()
		n.hasBlock = true
		for p.err == nil && p.tok.kind != tRBrace {
			if p.tok.kind == tEOF {
				p.fail(`block is not closed with "}"`)
				break
			}
			n.block = append(n.block, p.command())
		}
		p.expect(tRBrace)
	default:
		p.fail(`expected ";" or a block after %s, found %s`, n.name, p.tok.kind)
	}
	return n
}

// arguments reads arguments and then a test or test list.
func (p *parser) arguments(n *node) {
	for p.err == nil {
		switch p.tok.kind {
		case tTag:
			n.args = append(n.args, arg{kind: argTag, tag: p.tok.text, line: p.tok.line})
			p.advance()
		case tNumber:
			n.args = append(n.args, arg{kind: argNumber, num: p.tok.num, line: p.tok.line})
			p.advance()
		case tString:
			n.args = append(n.args, arg{kind: argStrings, strs: []string{p.tok.text}, line: p.tok.line})
			p.advance()
		case tLBracket:
			a := arg{kind: argStrings, list: true, line: p.tok.line}
			p.advance()
			for p.err == nil {
				if p.tok.kind != tString {
					p.fail("expected a string in the list, found %s", p.tok.kind)
					return
				}
				if len(a.strs) >= maxStringList {
					p.fail("too many strings in one list")
					return
				}
				a.strs = append(a.strs, p.tok.text)
				p.advance()
				if p.tok.kind == tComma {
					p.advance()
					continue
				}
				p.expect(tRBracket)
				break
			}
			n.args = append(n.args, a)
		case tIdent:
			n.tests = []*node{p.test()}
			return
		case tLParen:
			p.advance()
			for p.err == nil {
				n.tests = append(n.tests, p.test())
				if p.tok.kind == tComma {
					p.advance()
					continue
				}
				p.expect(tRParen)
				break
			}
			n.testList = true
			return
		default:
			return
		}
	}
}

func (p *parser) test() *node {
	if !p.enter() {
		return &node{}
	}
	defer func() { p.deep-- }()
	n := &node{line: p.tok.line}
	if p.tok.kind != tIdent {
		p.fail("expected a test, found %s", p.tok.kind)
		return n
	}
	n.name = p.tok.text
	p.advance()
	p.arguments(n)
	return n
}
