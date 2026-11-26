package deliver

import (
	"bufio"
	"bytes"
	"io"
	"mime"
	"strings"
	"sync"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"

	"github.com/danielgtmn/ownpanel/internal/mail/header"
)

// maxBodyBytes bounds what the body test reads of a message (decoded text
// of all parts together), maxHeaderBytes the header section read.
const (
	maxBodyBytes   = 1 << 20
	maxHeaderBytes = 1 << 20
	maxBodyParts   = 200
)

// parsed is the message as filters see it: read once, shared by every
// recipient mailbox.
type parsed struct {
	open func() (io.Reader, error)
	size int64

	once   sync.Once
	header map[string][]string // lowercase name → raw values, in order

	bodyOnce sync.Once
	text     []part // decoded leaf parts
	raw      string // undecoded body
}

type part struct {
	ctype string // lowercase type/subtype
	text  string
}

func (p *parsed) headers() map[string][]string {
	p.once.Do(func() {
		p.header = map[string][]string{}
		r, err := p.open()
		if err != nil {
			return
		}
		// The same fields port 25 saw (package header): a field name other
		// parsers refuse does not hide the fields after it.
		fields, _, _, _ := header.Read(r, maxHeaderBytes)
		for _, f := range fields {
			k := strings.ToLower(f.Name)
			p.header[k] = append(p.header[k], f.Value())
		}
	})
	return p.header
}

// Header implements sieve.Message.
func (p *parsed) Header(name string) []string { return p.headers()[name] }

// Size implements sieve.Message.
func (p *parsed) Size() int64 { return p.size }

// Body implements sieve.Message.
func (p *parsed) Body(transform string, types []string) []string {
	p.bodyOnce.Do(p.readBody)
	if transform == "raw" {
		return []string{p.raw}
	}
	var out []string
	for _, pt := range p.text {
		if transform == "text" {
			if strings.HasPrefix(pt.ctype, "text/") {
				out = append(out, pt.text)
			}
			continue
		}
		for _, t := range types {
			main, _, _ := strings.Cut(pt.ctype, "/")
			if t == "" || t == pt.ctype || !strings.Contains(t, "/") && t == main {
				out = append(out, pt.text)
				break
			}
		}
	}
	return out
}

func (p *parsed) readBody() {
	r, err := p.open()
	if err != nil {
		return
	}
	br := bufio.NewReader(r)
	// The raw body: after the header section.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(br, maxHeaderBytes+maxBodyBytes)); err != nil {
		return
	}
	all := buf.Bytes()
	if i := bytes.Index(all, []byte("\r\n\r\n")); i >= 0 {
		p.raw = string(truncate(all[i+4:], maxBodyBytes))
	} else if i := bytes.Index(all, []byte("\n\n")); i >= 0 {
		p.raw = string(truncate(all[i+2:], maxBodyBytes))
	}
	e, err := message.Read(bytes.NewReader(all))
	if e == nil {
		return
	}
	_ = err // unknown charsets still give an entity
	budget := maxBodyBytes
	parts := 0
	var walk func(e *message.Entity, depth int)
	walk = func(e *message.Entity, depth int) {
		if budget <= 0 || parts >= maxBodyParts || depth > 5 {
			return
		}
		if mr := e.MultipartReader(); mr != nil {
			for {
				child, err := mr.NextPart()
				if err != nil {
					break
				}
				walk(child, depth+1)
			}
			return
		}
		parts++
		ct, _, _ := e.Header.ContentType()
		if ct == "" {
			ct = "text/plain"
		}
		b, _ := io.ReadAll(io.LimitReader(e.Body, int64(budget)))
		budget -= len(b)
		p.text = append(p.text, part{ctype: strings.ToLower(ct), text: string(b)})
	}
	walk(e, 0)
}

func truncate(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

// charsetReader decodes header words of other charsets.
func charsetReader(cs string, input io.Reader) (io.Reader, error) { return charset.Reader(cs, input) }

// decodeHeader decodes encoded words for display (subjects of replies).
func decodeHeader(v string) string {
	d := mime.WordDecoder{CharsetReader: charsetReader}
	if s, err := d.DecodeHeader(v); err == nil {
		return s
	}
	return v
}
