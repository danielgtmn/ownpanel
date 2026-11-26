package transfer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

// mboxReader splits an mbox file into messages. A message starts with a
// "From " line that carries a date (or the first line of the file); the
// empty line before it belongs to the format, not the message. In mboxrd
// files (our export) one ">" is removed from lines matching ^>+From ;
// other files are read as mboxo, where ">From " stays as it is.
//
// Lines are read in pieces of at most mboxPiece bytes, so a file with one
// huge line (a small zip can unpack to one) never sits in memory whole.
// Only the first piece of a line can start a message or be unquoted.
type mboxReader struct {
	ctx   context.Context
	br    *bufio.Reader
	rd    bool
	next  []byte // "From " line of the next message; nil at the end
	cur   *mboxMessage
	err   error
	mid   bool // the last piece did not end its line
	reads int
}

// mboxPiece is the most of one line the reader holds at a time.
const mboxPiece = 64 << 10

var errNotMbox = errors.New("not an mbox file")

func newMboxReader(ctx context.Context, r io.Reader, rd bool) (*mboxReader, error) {
	m := &mboxReader{ctx: ctx, br: bufio.NewReaderSize(r, mboxPiece), rd: rd}
	line, err := m.piece()
	if !bytes.HasPrefix(line, []byte("From ")) {
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return nil, errNotMbox
	}
	m.next = line
	// A first line too long for a date: the rest of it is not mail.
	for m.mid {
		if _, err := m.piece(); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
	}
	return m, nil
}

// piece reads the next line, or the next piece of a long one (m.mid
// tells which), into a new slice. It checks for cancellation now and
// then, so stopping a job does not wait for the end of a large file.
func (m *mboxReader) piece() ([]byte, error) {
	if m.reads++; m.reads%256 == 0 {
		if err := m.ctx.Err(); err != nil {
			return nil, err
		}
	}
	b, err := m.br.ReadSlice('\n')
	m.mid = errors.Is(err, bufio.ErrBufferFull)
	if m.mid {
		err = nil
	}
	return append([]byte(nil), b...), err
}

// next returns the next message, or nil at the end. The previous
// message is read to its end first.
func (m *mboxReader) nextMessage() (*mboxMessage, error) {
	if m.cur != nil {
		if _, err := io.Copy(io.Discard, m.cur); err != nil {
			return nil, err
		}
		m.cur = nil
	}
	if m.err != nil {
		return nil, m.err
	}
	if m.next == nil {
		return nil, nil
	}
	msg := &mboxMessage{m: m, from: fromLineDate(string(m.next))}
	m.next = nil
	m.cur = msg
	return msg, nil
}

// mboxMessage reads one message of an mbox file.
type mboxMessage struct {
	m       *mboxReader
	from    time.Time // date of the "From " line (zero if none)
	pending []byte
	held    []byte // an empty line that may precede the next "From "
	done    bool
}

func (msg *mboxMessage) Read(b []byte) (int, error) {
	for len(msg.pending) == 0 {
		if msg.done {
			return 0, io.EOF
		}
		if err := msg.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(b, msg.pending)
	msg.pending = msg.pending[n:]
	return n, nil
}

// fill reads the next line (or piece of a long line) into pending.
func (msg *mboxMessage) fill() error {
	m := msg.m
	start := !m.mid // the piece begins a line
	line, err := m.piece()
	if len(line) == 0 {
		msg.done = true
		if err != nil && !errors.Is(err, io.EOF) {
			m.err = err
			return err
		}
		// An empty line at the end of the file ends the format, too.
		msg.held = nil
		return nil
	}
	whole := start && !m.mid
	if whole && bytes.HasPrefix(line, []byte("From ")) && !fromLineDate(string(line)).IsZero() {
		// The next message; the empty line before it is dropped.
		m.next = line
		msg.done = true
		msg.held = nil
		return nil
	}
	if start && m.rd && bytes.HasPrefix(line, []byte(">")) && bytes.HasPrefix(bytes.TrimLeft(line, ">"), []byte("From ")) {
		line = line[1:]
	}
	out := msg.held
	msg.held = nil
	if whole && len(bytes.TrimRight(line, "\r\n")) == 0 && err == nil {
		msg.held = line
	} else {
		out = append(out, line...)
	}
	msg.pending = out
	if err != nil {
		msg.done = true
		if !errors.Is(err, io.EOF) {
			m.err = err
			return err
		}
	}
	return nil
}

// fromLineLayouts are the date forms seen in "From " lines (after the
// sender), with runs of spaces collapsed.
var fromLineLayouts = []string{
	"Mon Jan 2 15:04:05 2006",
	"Mon Jan 2 15:04:05 -0700 2006",
	"Mon Jan 2 15:04:05 MST 2006",
	"Mon Jan 2 15:04 2006",
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon Jan 2 2006 15:04:05",
}

// maxFromLine is the longest "From " line whose date is looked for; real
// ones are well under 200 bytes. Longer lines are message text.
const maxFromLine = 512

// fromLineFields bounds the field counts tried for the date: the layouts
// in fromLineLayouts have at most 6 fields, the rest of a line (such as
// uucp's "remote from host") is ignored.
const fromLineFields = 8

// fromLineDate parses the date of a "From sender date" line; asctime
// dates are UTC. Zero if there is none. It takes time linear in the
// length of the line, which comes from an uploaded file.
func fromLineDate(line string) time.Time {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "From ") || len(line) > maxFromLine {
		return time.Time{}
	}
	f := strings.Fields(line[5:])
	if len(f) < 2 {
		return time.Time{}
	}
	rest := f[1:]
	for n := min(len(rest), fromLineFields); n >= 4; n-- {
		s := strings.Join(rest[:n], " ")
		for _, l := range fromLineLayouts {
			if t, err := time.Parse(l, s); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}

// fromLine formats the "From " line of our export (asctime, UTC).
func fromLine(t time.Time) string {
	return "From MAILER-DAEMON " + t.UTC().Format("Mon Jan _2 15:04:05 2006") + "\r\n"
}
