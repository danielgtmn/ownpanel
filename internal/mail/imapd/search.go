package imapd

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	gomessage "github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"

	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// maxScanBytes bounds how much of one message TEXT and BODY searches read;
// maxHeaderBytes how much of its header any search reads. A message whose
// header is longer matches no header criterion.
const (
	maxScanBytes   = 64 << 20
	maxHeaderBytes = 1 << 20
)

var errHeaderTooLong = errors.New("header too long")

// headerCap passes reads through, but while capped fails once more than n
// bytes were read: the header of a message must end within them.
type headerCap struct {
	r      io.Reader
	n      int64
	capped bool
}

func (h *headerCap) Read(p []byte) (int, error) {
	if !h.capped {
		return h.r.Read(p)
	}
	if h.n <= 0 {
		return 0, errHeaderTooLong
	}
	if int64(len(p)) > h.n {
		p = p[:h.n]
	}
	n, err := h.r.Read(p)
	h.n -= int64(n)
	return n, err
}

// readHeader reads the header of a message from r, at most maxHeaderBytes
// of it, and returns it with a reader of the body.
func readHeader(r io.Reader) (textproto.Header, io.Reader, error) {
	hc := &headerCap{r: r, n: maxHeaderBytes, capped: true}
	br := bufio.NewReader(hc)
	h, err := textproto.ReadHeader(br)
	hc.capped = false
	return h, br, err
}

func (s *session) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	sel := s.sel
	all := make([]entry, len(sel.uids))
	for i, u := range sel.uids {
		all[i] = entry{uint32(i + 1), u}
	}
	msgs, err := s.load(sel, all)
	if err != nil {
		return nil, s.fail(err)
	}
	ev := evaluator{s: s, sel: sel, max: uint32(len(sel.uids))}
	if len(sel.uids) > 0 {
		ev.lastUID = sel.uids[len(sel.uids)-1]
	}

	var data imap.SearchData
	var seqs imap.SeqSet
	var uids imap.UIDSet
	for _, e := range all {
		m, ok := msgs[e.uid]
		if !ok {
			continue
		}
		c := candidate{entry: e, m: m}
		if !ev.match(&c, criteria) {
			continue
		}
		uids.AddNum(imap.UID(e.uid))
		num := e.seq
		if kind == imapserver.NumKindSeq {
			seqs.AddNum(e.seq)
		} else {
			num = e.uid
		}
		if data.Min == 0 || num < data.Min {
			data.Min = num
		}
		if num > data.Max {
			data.Max = num
		}
		data.Count++
	}
	if kind == imapserver.NumKindSeq {
		data.All = seqs
	} else {
		data.All = uids
	}
	if options.ReturnSave {
		sel.searchRes = uids
	}
	return &data, nil
}

type evaluator struct {
	s       *session
	sel     *selected
	max     uint32
	lastUID uint32
}

// candidate is one message being matched; its header is read at most
// once.
type candidate struct {
	entry
	m          storage.Message
	header     *mail.Header
	headerRead bool
}

func (ev *evaluator) headerOf(c *candidate) *mail.Header {
	if c.headerRead {
		return c.header
	}
	c.headerRead = true
	f, err := ev.s.srv.opts.Store.Open(c.m)
	if err != nil {
		return nil
	}
	defer f.Close()
	h, _, err := readHeader(f)
	if err != nil {
		return nil
	}
	c.header = &mail.Header{Header: gomessage.Header{Header: h}}
	return c.header
}

func (ev *evaluator) match(c *candidate, cr *imap.SearchCriteria) bool {
	for _, set := range cr.SeqNum {
		if !seqContains(set, c.seq, ev.max) {
			return false
		}
	}
	for _, set := range cr.UID {
		if imap.IsSearchRes(set) {
			set = ev.sel.searchRes
		}
		if !uidContains(set, c.uid, ev.lastUID) {
			return false
		}
	}
	if !matchDate(c.m.InternalDate, cr.Since, cr.Before) {
		return false
	}
	for _, f := range cr.Flag {
		if !c.m.HasFlag(string(f)) {
			return false
		}
	}
	for _, f := range cr.NotFlag {
		if c.m.HasFlag(string(f)) {
			return false
		}
	}
	if cr.Larger != 0 && c.m.Size <= cr.Larger {
		return false
	}
	if cr.Smaller != 0 && c.m.Size >= cr.Smaller {
		return false
	}
	if len(cr.Header) > 0 || !cr.SentSince.IsZero() || !cr.SentBefore.IsZero() {
		h := ev.headerOf(c)
		if h == nil {
			return false
		}
		for _, hc := range cr.Header {
			if !matchHeaderFields(h.FieldsByKey(hc.Key), hc.Value) {
				return false
			}
		}
		if !cr.SentSince.IsZero() || !cr.SentBefore.IsZero() {
			// Without a usable Date header the internal date counts.
			t, err := h.Date()
			if err != nil || t.IsZero() {
				t = c.m.InternalDate
			}
			if !matchDate(t, cr.SentSince, cr.SentBefore) {
				return false
			}
		}
	}
	for _, text := range cr.Text {
		if !ev.scan(c, text, true) {
			return false
		}
	}
	for _, body := range cr.Body {
		if !ev.scan(c, body, false) {
			return false
		}
	}
	for i := range cr.Not {
		if ev.match(c, &cr.Not[i]) {
			return false
		}
	}
	for i := range cr.Or {
		if !ev.match(c, &cr.Or[i][0]) && !ev.match(c, &cr.Or[i][1]) {
			return false
		}
	}
	return true
}

// scan reads the message file and looks for pattern in text parts (and in
// header fields with includeHeader), without case.
func (ev *evaluator) scan(c *candidate, pattern string, includeHeader bool) bool {
	if pattern == "" {
		return true
	}
	f, err := ev.s.srv.opts.Store.Open(c.m)
	if err != nil {
		return false
	}
	defer f.Close()
	h, body, err := readHeader(io.LimitReader(f, maxScanBytes))
	if err != nil {
		return false
	}
	e, err := gomessage.New(gomessage.Header{Header: h}, body)
	if e == nil {
		return false
	}
	_ = err // unknown charsets still give a usable entity
	return matchEntity(e, strings.ToLower(pattern), includeHeader)
}

// matchDate compares dates only, without time zones (RFC 9051).
func matchDate(t, since, before time.Time) bool {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if !since.IsZero() && t.Before(since) {
		return false
	}
	if !before.IsZero() && !t.Before(before) {
		return false
	}
	return true
}

func matchHeaderFields(fields gomessage.HeaderFields, pattern string) bool {
	if pattern == "" {
		return fields.Len() > 0
	}
	pattern = strings.ToLower(pattern)
	for fields.Next() {
		v, _ := fields.Text()
		if strings.Contains(strings.ToLower(v), pattern) {
			return true
		}
	}
	return false
}

// matchEntity: pattern is lowercase.
func matchEntity(e *gomessage.Entity, pattern string, includeHeader bool) bool {
	if includeHeader && matchHeaderFields(e.Header.Fields(), pattern) {
		return true
	}
	if mr := e.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err != nil {
				return false
			}
			if matchEntity(part, pattern, includeHeader) {
				return true
			}
		}
	}
	t, _, _ := e.Header.ContentType()
	if t != "" && !strings.HasPrefix(t, "text/") && !strings.HasPrefix(t, "message/") {
		return false
	}
	buf, err := io.ReadAll(e.Body)
	if err != nil && len(buf) == 0 {
		return false
	}
	return bytes.Contains(bytes.ToLower(buf), []byte(pattern))
}
