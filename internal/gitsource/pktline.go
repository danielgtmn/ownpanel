package gitsource

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Git's pkt-line framing: four hex digits of length (including themselves)
// and the data; "0000" is a flush.

const (
	maxPktData = 65516
	// Limits on what the server announces before the pack.
	maxRefs        = 100_000
	maxAdvertBytes = 32 << 20
	// checkAdvertBytes bounds the advertisement when only branches are
	// asked for (checks); fetching allows maxAdvertBytes.
	checkAdvertBytes = 8 << 20
	maxServerTexts   = 4 << 10
	// What a server may send between the request and the pack (a depth
	// of 1 for one commit needs a single shallow line).
	maxLinesBeforePack = 1000
	maxBytesBeforePack = 256 << 10
	sidebandData       = 1
	sidebandMessage    = 2
	sidebandError      = 3
)

var errFlush = errors.New("flush")

// ProtocolError is a server answer that is not Git's protocol.
type ProtocolError struct{ Msg string }

func (e *ProtocolError) Error() string {
	return "the server's answer is not what a Git server sends: " + e.Msg
}

func protocolf(format string, args ...any) error {
	return &ProtocolError{Msg: fmt.Sprintf(format, args...)}
}

// ServerError is an error message the server sent. Msg is untrusted text,
// shortened and stripped of control characters.
type ServerError struct{ Msg string }

func (e *ServerError) Error() string { return "the server answered: " + e.Msg }

// serverText makes a server's text fit for showing: one line, printable
// ASCII and common letters, at most 200 characters.
func serverText(b []byte) string {
	s := strings.ToValidUTF8(string(b), "?")
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || r >= 0x80 && r < 0xa0 || r >= 0x200b && r <= 0x200f || r >= 0x202a && r <= 0x202e ||
			r >= 0x2066 && r <= 0x2069 || r == 0xfeff {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}

type pktReader struct {
	r   *bufio.Reader
	buf [maxPktData + 4]byte
}

func newPktReader(r io.Reader) *pktReader {
	if br, ok := r.(*bufio.Reader); ok {
		return &pktReader{r: br}
	}
	return &pktReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// next returns the data of the next packet (valid until the next call),
// or errFlush for a flush packet.
func (p *pktReader) next() ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(p.r, head[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	n, err := strconv.ParseUint(string(head[:]), 16, 16)
	if err != nil {
		return nil, protocolf("bad packet length")
	}
	switch {
	case n == 0:
		return nil, errFlush
	case n == 1 || n == 2:
		// delim and response-end packets of protocol v2; not expected.
		return nil, protocolf("unexpected packet")
	case n < 4 || n > maxPktData+4:
		return nil, protocolf("bad packet length")
	}
	data := p.buf[:n-4]
	if _, err := io.ReadFull(p.r, data); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err // a limit, a closed connection
	}
	return data, nil
}

func writePkt(w *bytes.Buffer, s string) {
	fmt.Fprintf(w, "%04x%s", len(s)+4, s)
}

func writeFlush(w *bytes.Buffer) { w.WriteString("0000") }

// advert is what a server announces before the fetch (protocol v0), as
// far as the caller asked for it: refs holds only the branches kept.
type advert struct {
	refs map[string]string // name → object id
	caps map[string]string // capability → value ("" if none)
	// truncated: there were more branches than were kept.
	truncated bool
}

func validOID(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// readAdvert reads the ref advertisement up to its flush, at most
// maxBytes of it. Only branches are kept: the one named branch, or (branch
// "") the first MaxBranches; everything else a server announces (tags,
// other refs, more branches) is read and dropped, so what is kept in
// memory stays small whatever the server sends.
func readAdvert(p *pktReader, branch string, maxBytes int) (advert, error) {
	ad := advert{refs: map[string]string{}, caps: map[string]string{}}
	var want []byte
	if branch != "" {
		want = []byte("refs/heads/" + branch)
	}
	heads := []byte("refs/heads/")
	total, seen := 0, 0
	first := true
	for {
		line, err := p.next()
		if errors.Is(err, errFlush) {
			if first {
				// An empty answer: an empty repository announces at least a
				// capabilities line, so this is "no repository here".
				return ad, &ServerError{Msg: "the server did not announce any branches (is the repository empty or the address wrong?)"}
			}
			return ad, nil
		}
		if err != nil {
			return ad, err
		}
		total += len(line) + 4
		if seen++; total > maxBytes || seen > maxRefs {
			return ad, limitf("the server announces too many branches and tags")
		}
		if bytes.HasPrefix(line, []byte("ERR ")) {
			return ad, &ServerError{Msg: serverText(line[4:])}
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		if first {
			first = false
			if bytes.HasPrefix(line, []byte("version ")) {
				// A server that answers in a newer protocol although none
				// was asked for.
				if string(line) != "version 1" {
					return ad, protocolf("unexpected protocol version")
				}
				first = true
				continue
			}
			nul := bytes.IndexByte(line, 0)
			if nul < 0 {
				return ad, protocolf("no capabilities")
			}
			for _, c := range strings.Fields(string(line[nul+1:])) {
				k, v, _ := strings.Cut(c, "=")
				if k == "symref" && !strings.HasPrefix(v, "HEAD:") {
					continue // only HEAD's matters here
				}
				if len(ad.caps) < 64 {
					ad.caps[k] = v
				}
			}
			line = line[:nul]
		}
		sp := bytes.IndexByte(line, ' ')
		if sp != 40 || len(line) == 41 || !validOID(string(line[:40])) {
			if bytes.HasPrefix(line, []byte("shallow ")) {
				continue
			}
			return ad, protocolf("bad ref line")
		}
		name := line[41:]
		switch {
		case want != nil:
			if bytes.Equal(name, want) {
				ad.refs[string(name)] = string(line[:40])
			}
		case bytes.HasPrefix(name, heads):
			if len(ad.refs) >= MaxBranches {
				ad.truncated = true
				continue
			}
			ad.refs[string(name)] = string(line[:40])
		}
	}
}

// fetchRequest is what the client sends to ask for want, at depth 1.
func fetchRequest(ad advert, want string) []byte {
	var caps []string
	for _, c := range []string{"side-band-64k", "ofs-delta", "shallow", "no-progress"} {
		if _, ok := ad.caps[c]; ok {
			caps = append(caps, c)
		}
	}
	if _, ok := ad.caps["side-band-64k"]; !ok {
		if _, ok := ad.caps["side-band"]; ok {
			caps = append(caps, "side-band")
		}
	}
	caps = append(caps, "agent=git/ownpanel")
	var b bytes.Buffer
	writePkt(&b, "want "+want+" "+strings.Join(caps, " ")+"\n")
	if _, ok := ad.caps["shallow"]; ok {
		writePkt(&b, "deepen 1\n")
	}
	writeFlush(&b)
	writePkt(&b, "done\n")
	return b.Bytes()
}

// readUntilPack reads the server's answer to fetchRequest up to the pack:
// shallow lines (and their flush), then NAK or ACK.
func readUntilPack(p *pktReader) error {
	total := 0
	for range maxLinesBeforePack {
		line, err := p.next()
		if errors.Is(err, errFlush) {
			continue
		}
		if err != nil {
			return err
		}
		if total += len(line) + 4; total > maxBytesBeforePack {
			break
		}
		s := strings.TrimSuffix(string(line), "\n")
		switch {
		case s == "NAK" || strings.HasPrefix(s, "ACK "):
			return nil
		case strings.HasPrefix(s, "shallow ") || strings.HasPrefix(s, "unshallow "):
		case strings.HasPrefix(s, "ERR "):
			return &ServerError{Msg: serverText(line[4:])}
		default:
			return protocolf("unexpected line before the pack")
		}
	}
	return protocolf("too many lines before the pack")
}

// sideband reads the pack out of side-band packets.
type sideband struct {
	p    *pktReader
	cur  []byte
	done bool
}

func (s *sideband) Read(b []byte) (int, error) {
	for len(s.cur) == 0 {
		if s.done {
			return 0, io.EOF
		}
		data, err := s.p.next()
		if errors.Is(err, errFlush) {
			s.done = true
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		if len(data) == 0 {
			continue
		}
		switch data[0] {
		case sidebandData:
			s.cur = data[1:]
		case sidebandMessage:
			// Progress and notices; asked not to send progress.
		case sidebandError:
			return 0, &ServerError{Msg: serverText(data[1:])}
		default:
			return 0, protocolf("unknown side band")
		}
	}
	n := copy(b, s.cur)
	s.cur = s.cur[n:]
	return n, nil
}

// packStream returns the reader the pack comes out of, after the answer up
// to NAK was read.
func packStream(p *pktReader, ad advert) io.Reader {
	_, sb64 := ad.caps["side-band-64k"]
	_, sb := ad.caps["side-band"]
	if sb64 || sb {
		return &sideband{p: p}
	}
	return p.r
}
