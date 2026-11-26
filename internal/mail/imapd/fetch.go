package imapd

import (
	"bufio"
	"io"
	"os"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-message/textproto"

	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

func (s *session) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	if err := s.check(); err != nil {
		return err
	}
	sel := s.sel
	entries := sel.resolve(numSet)
	msgs, err := s.load(sel, entries)
	if err != nil {
		return s.fail(err)
	}

	// Reading a body without PEEK sets \Seen, in read-write mode only (and
	// in a shared mailbox the reader's own \Seen, unless after EXAMINE).
	markSeen := false
	for _, bs := range options.BodySection {
		markSeen = markSeen || !bs.Peek
	}
	for _, bs := range options.BinarySection {
		markSeen = markSeen || !bs.Peek
	}
	seen := map[uint32]bool{}
	if markSeen && (!sel.readOnly || sel.t.reader != "" && !sel.examine) {
		var unseen []uint32
		for _, e := range entries {
			if m, ok := msgs[e.uid]; ok && !m.HasFlag(storage.FlagSeen) {
				unseen = append(unseen, e.uid)
			}
		}
		if len(unseen) > 0 {
			updated, err := s.srv.opts.Store.SetFlagsAs(s.ctx, sel.t.sid, sel.t.folder, unseen, storage.FlagsAdd, []string{storage.FlagSeen}, sel.t.reader)
			if err != nil {
				return s.fail(err)
			}
			for _, m := range updated {
				msgs[m.UID] = m
				seen[m.UID] = true
			}
		}
	}

	for _, e := range entries {
		m, ok := msgs[e.uid]
		if !ok {
			continue // expunged meanwhile
		}
		if err := s.fetchOne(w, e.seq, m, options, seen[e.uid]); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) fetchOne(w *imapserver.FetchWriter, seq uint32, m storage.Message, options *imap.FetchOptions, seenSet bool) error {
	needFile := options.Envelope || options.BodyStructure != nil || len(options.BodySection) > 0 ||
		len(options.BinarySection) > 0 || len(options.BinarySectionSize) > 0
	var f *os.File
	if needFile {
		var err error
		if f, err = s.srv.opts.Store.Open(m); err != nil {
			// Expunged after the index was read: nothing to answer.
			return nil
		}
		defer f.Close()
	}
	// rewind returns the file from its start.
	rewind := func() (io.Reader, error) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		return bufio.NewReader(f), nil
	}

	// Prepare everything that can fail before the response starts.
	var envelope *imap.Envelope
	if options.Envelope {
		r, err := rewind()
		if err != nil {
			return err
		}
		h, err := textproto.ReadHeader(r.(*bufio.Reader))
		if err == nil {
			envelope = imapserver.ExtractEnvelope(h)
		} else {
			envelope = &imap.Envelope{}
		}
	}
	var bodyStructure imap.BodyStructure
	if options.BodyStructure != nil {
		r, err := rewind()
		if err != nil {
			return err
		}
		bodyStructure = imapserver.ExtractBodyStructure(r)
	}

	rw := w.CreateMessage(seq)
	if options.UID {
		rw.WriteUID(imap.UID(m.UID))
	}
	if options.Flags || seenSet {
		rw.WriteFlags(imapFlags(m.Flags))
	}
	if options.InternalDate {
		rw.WriteInternalDate(m.InternalDate)
	}
	if options.RFC822Size {
		rw.WriteRFC822Size(m.Size)
	}
	if options.Envelope {
		rw.WriteEnvelope(envelope)
	}
	if options.BodyStructure != nil {
		rw.WriteBodyStructure(bodyStructure)
	}
	for _, bs := range options.BodySection {
		if err := writeBodySection(rw, f, m.Size, bs, rewind); err != nil {
			rw.Close()
			return err
		}
	}
	for _, bs := range options.BinarySection {
		r, err := rewind()
		if err != nil {
			rw.Close()
			return err
		}
		buf := imapserver.ExtractBinarySection(r, bs)
		if err := writeLiteral(rw.WriteBinarySection(bs, int64(len(buf))), buf); err != nil {
			rw.Close()
			return err
		}
	}
	for _, bss := range options.BinarySectionSize {
		r, err := rewind()
		if err != nil {
			rw.Close()
			return err
		}
		rw.WriteBinarySectionSize(bss, imapserver.ExtractBinarySectionSize(r, bss))
	}
	return rw.Close()
}

// writeBodySection streams BODY[] (whole message, maybe partial) from the
// file; other sections are extracted in memory by the library.
func writeBodySection(rw *imapserver.FetchResponseWriter, f *os.File, size int64, bs *imap.FetchItemBodySection, rewind func() (io.Reader, error)) error {
	whole := len(bs.Part) == 0 && bs.Specifier == imap.PartSpecifierNone &&
		len(bs.HeaderFields) == 0 && len(bs.HeaderFieldsNot) == 0
	if whole {
		off, n := int64(0), size
		if p := bs.Partial; p != nil {
			off = min(p.Offset, size)
			n = size - off
			if p.Size >= 0 && p.Size < n {
				n = p.Size
			}
		}
		wc := rw.WriteBodySection(bs, n)
		_, err := io.CopyN(wc, io.NewSectionReader(f, off, n), n)
		if cerr := wc.Close(); err == nil {
			err = cerr
		}
		return err
	}
	r, err := rewind()
	if err != nil {
		return err
	}
	buf := imapserver.ExtractBodySection(r, bs)
	return writeLiteral(rw.WriteBodySection(bs, int64(len(buf))), buf)
}

func writeLiteral(wc io.WriteCloser, b []byte) error {
	_, err := wc.Write(b)
	if cerr := wc.Close(); err == nil {
		err = cerr
	}
	return err
}
