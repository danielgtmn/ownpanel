package transfer

import (
	"bufio"
	"bytes"
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
type mboxReader struct {
	br   *bufio.Reader
	rd   bool
	next []byte // "From " line of the next message; nil at the end
	cur  *mboxMessage
	err  error
}

var errNotMbox = errors.New("not an mbox file")

func newMboxReader(r io.Reader, rd bool) (*mboxReader, error) {
	m := &mboxReader{br: bufio.NewReaderSize(r, 64<<10), rd: rd}
	line, err := m.br.ReadBytes('\n')
	if !bytes.HasPrefix(line, []byte("From ")) {
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return nil, errNotMbox
	}
	m.next = line
	return m, nil
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

// fill reads the next line into pending.
func (msg *mboxMessage) fill() error {
	m := msg.m
	line, err := m.br.ReadBytes('\n')
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
	if bytes.HasPrefix(line, []byte("From ")) && !fromLineDate(string(line)).IsZero() {
		// The next message; the empty line before it is dropped.
		m.next = line
		msg.done = true
		msg.held = nil
		return nil
	}
	if m.rd && bytes.HasPrefix(line, []byte(">")) && bytes.HasPrefix(bytes.TrimLeft(line, ">"), []byte("From ")) {
		line = line[1:]
	}
	out := msg.held
	msg.held = nil
	if len(bytes.TrimRight(line, "\r\n")) == 0 && err == nil {
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

// fromLineDate parses the date of a "From sender date" line; asctime
// dates are UTC. Zero if there is none.
func fromLineDate(line string) time.Time {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "From ") {
		return time.Time{}
	}
	f := strings.Fields(line[5:])
	if len(f) < 2 {
		return time.Time{}
	}
	rest := f[1:]
	for n := len(rest); n >= 4; n-- {
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
