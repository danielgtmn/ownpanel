package transfer

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// maxReconnects bounds how often a job connects again after the
// connection broke.
const maxReconnects = 3

// reconnectDelay is the pause before connecting again (tests shorten it).
var reconnectDelay = 10 * time.Second

// Bounds of what another server's answers may make the service hold.
var (
	// maxListed is the most LIST entries read (maxFolders are copied;
	// \Noselect entries and the rest of a namespace may come on top).
	maxListed = 10 * maxFolders
	// maxFolderMessages is the most messages of one folder one pass
	// looks at; a repeat goes on from the watermark.
	maxFolderMessages = 2_000_000
	// fetchBatch is how many messages' flags, dates and sizes are
	// fetched at a time.
	fetchBatch = 500
)

// Bytes per message the other server may send for a UID or for the
// flags, date and size of a message (generous: real ones are about 30
// and 150).
const (
	uidBytes  = 64
	metaBytes = 4 << 10
)

// errConnLost: the connection to the other server broke; the job
// connects again and goes on from the watermarks.
type errConnLost struct{ err error }

func (e *errConnLost) Error() string { return "the connection was lost: " + e.err.Error() }

func (s *session) logout() {
	s.lc.allow(controlBytes)
	done := make(chan struct{})
	go func() {
		s.c.Logout().Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	s.close()
}

// folders lists the folders of the account, with message counts and
// sizes when counts is set.
func (s *session) folders(counts bool) ([]sourceFolder, error) {
	s.lc.allow(controlBytes)
	caps := s.c.Caps()
	prefix := ""
	if caps.Has(imap.CapNamespace) || caps.Has(imap.CapIMAP4rev2) {
		ns, err := s.c.Namespace().Wait()
		if err != nil {
			if connectionLost(err) {
				return nil, err
			}
		} else if len(ns.Personal) > 0 {
			prefix = ns.Personal[0].Prefix
		}
	}
	s.lc.allow(controlBytes)
	cmd := s.c.List("", "*", nil)
	var list []*imap.ListData
	for l := cmd.Next(); l != nil; l = cmd.Next() {
		if len(list) >= maxListed {
			// Not read to its end: the session is given up.
			s.close()
			return nil, remoteErr("%s lists more than %d folders", s.host, maxListed)
		}
		list = append(list, l)
	}
	if err := cmd.Close(); err != nil {
		return nil, err
	}
	var out []sourceFolder
	for _, l := range list {
		f := sourceFolder{remote: l.Mailbox, levels: remoteLevels(l.Mailbox, l.Delim, prefix)}
		for _, a := range l.Attrs {
			f.attrs = append(f.attrs, string(a))
			if strings.EqualFold(string(a), string(imap.MailboxAttrNoSelect)) ||
				strings.EqualFold(string(a), string(imap.MailboxAttrNonExistent)) {
				f.noSelect = true
			}
		}
		if counts && !f.noSelect {
			if err := s.count(&f, caps); err != nil {
				if connectionLost(err) {
					return nil, err
				}
				f.noSelect = true // a folder that cannot be opened is left out
			}
		}
		out = append(out, f)
	}
	// INBOX first, then by name, so the order is stable.
	slices.SortStableFunc(out, func(a, b sourceFolder) int {
		ai, bi := strings.EqualFold(a.remote, storage.Inbox), strings.EqualFold(b.remote, storage.Inbox)
		switch {
		case ai && !bi:
			return -1
		case bi && !ai:
			return 1
		}
		return strings.Compare(a.remote, b.remote)
	})
	return out, nil
}

// count fills in messages and bytes of a folder: STATUS (SIZE) where the
// server has it, else the sizes of all messages.
func (s *session) count(f *sourceFolder, caps imap.CapSet) error {
	s.lc.allow(controlBytes)
	if caps.Has(imap.CapStatusSize) || caps.Has(imap.CapIMAP4rev2) {
		st, err := s.c.Status(f.remote, &imap.StatusOptions{NumMessages: true, Size: true}).Wait()
		if err != nil {
			return err
		}
		if st.NumMessages != nil {
			f.messages = int(*st.NumMessages)
		}
		if st.Size != nil {
			f.bytes = *st.Size
		}
		return nil
	}
	sel, err := s.c.Select(f.remote, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return err
	}
	f.messages = int(sel.NumMessages)
	if sel.NumMessages == 0 {
		return nil
	}
	// Summed as the answers come, not collected.
	s.lc.allow(controlBytes + int64(min(int(sel.NumMessages), maxFolderMessages))*metaBytes)
	cmd := s.c.Fetch(imap.SeqSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{RFC822Size: true})
	for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
		for item := msg.Next(); item != nil; item = msg.Next() {
			if sz, ok := item.(imapclient.FetchItemDataRFC822Size); ok {
				f.bytes += sz.Size
			}
		}
	}
	return cmd.Close()
}

// uidsFrom returns the UIDs from from on of the selected folder, in
// order, at most maxFolderMessages of them (more says whether there were
// more). Only the UIDs are kept, not the answers.
func (s *session) uidsFrom(from imap.UID, numMessages uint32) (uids []imap.UID, more bool, err error) {
	s.lc.allow(controlBytes + int64(min(int(numMessages), maxFolderMessages))*uidBytes)
	var set imap.UIDSet
	set.AddRange(from, 0)
	cmd := s.c.Fetch(set, &imap.FetchOptions{UID: true})
	for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
		for item := msg.Next(); item != nil; item = msg.Next() {
			u, ok := item.(imapclient.FetchItemDataUID)
			// "n:*" also returns the last message when it is below n.
			if !ok || u.UID < from {
				continue
			}
			if len(uids) >= maxFolderMessages {
				more = true
				continue
			}
			uids = append(uids, u.UID)
		}
	}
	if err := cmd.Close(); err != nil {
		return nil, false, err
	}
	slices.Sort(uids)
	return slices.Compact(uids), more, nil
}

func imapFlags(fs []imap.Flag) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, string(f))
	}
	return cleanFlags(out)
}

// runIMAP copies the chosen folders of another account, connecting again
// when the connection breaks.
func (r *runner) runIMAP(password string) error {
	if password == "" {
		return &Error{Msg: "enter the password again to continue"}
	}
	src := config.ImportSource{Host: r.j.Host, Port: r.j.Port, Username: r.j.Username, Password: password}
	for attempt := 0; ; attempt++ {
		err := r.imapPass(src)
		var lost *errConnLost
		if !errors.As(err, &lost) {
			return err
		}
		r.m.log.Warn("mail: import connection lost", "job", r.j.ID, "err", err)
		if attempt >= maxReconnects {
			return remoteErr("the connection to %s was lost too often", src.Host)
		}
		r.save(true)
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-time.After(reconnectDelay):
		}
	}
}

func (r *runner) imapPass(src config.ImportSource) error {
	s, err := r.m.connect(r.ctx, src)
	if err != nil {
		return err
	}
	defer s.logout()
	folders, err := s.folders(true)
	if err != nil {
		return r.imapErr(s, err, "listing the folders")
	}
	chosen, err := makePlan(folders, r.local).choose(r.j.Choices)
	if err != nil {
		return err
	}
	r.j.Messages = 0
	for _, f := range chosen {
		r.j.Messages += f.Messages
	}
	for _, f := range chosen {
		if err := r.copyIMAPFolder(s, f); err != nil {
			return err
		}
	}
	return nil
}

// imapErr classifies an error of the other server during a job.
func (r *runner) imapErr(s *session, err error, doing string) error {
	if r.ctx.Err() != nil {
		return r.ctx.Err()
	}
	if s.lc.tooMuch() {
		return remoteErr("%s on %s failed: %s", doing, s.host, errTooMuch)
	}
	if downloadLimited(err) {
		return &errPaused{msg: s.host + " limits downloads for now (" + s.clean(err) + "); continue later, e.g. tomorrow"}
	}
	if connectionLost(err) {
		return &errConnLost{err: errors.New(s.clean(err))}
	}
	return remoteErr("%s on %s failed: %s", doing, s.host, s.clean(err))
}

func (r *runner) copyIMAPFolder(s *session, f planned) error {
	st := r.m.opts.Store
	sid := r.j.StorageID
	r.beginFolder(f.Remote, f.Local)
	if err := r.ensureFolder(f.Local, f.SpecialUse, nil); err != nil {
		return err
	}
	s.lc.allow(controlBytes)
	sel, err := s.c.Select(f.Remote, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		e := r.imapErr(s, err, "opening "+f.Remote)
		var re *Error
		if errors.As(e, &re) {
			// One folder that cannot be opened does not end the job.
			if err := r.fail(f.Remote, nil, "the folder could not be opened: "+s.clean(err)); err != nil {
				return err
			}
			return r.endFolder()
		}
		return e
	}
	wm, err := st.ImportWatermark(r.ctx, sid, r.j.Host, r.j.Username, f.Remote)
	if err != nil {
		return err
	}
	from := imap.UID(1)
	if wm.UIDValidity == sel.UIDValidity {
		from = imap.UID(wm.LastUID) + 1
	}
	if sel.NumMessages == 0 {
		return r.endFolder()
	}
	uids, more, err := s.uidsFrom(from, sel.NumMessages)
	if err != nil {
		return r.imapErr(s, err, "reading "+f.Remote)
	}
	// Flags, dates and sizes in batches, so the answers held at a time
	// stay small however large the folder is.
	for len(uids) > 0 {
		batch := uids[:min(len(uids), fetchBatch)]
		uids = uids[len(batch):]
		bufs, err := s.metadata(batch)
		if err != nil {
			return r.imapErr(s, err, "reading "+f.Remote)
		}
		for _, b := range bufs {
			mark := &storage.Watermark{Host: r.j.Host, Username: r.j.Username, Folder: f.Remote, UIDValidity: sel.UIDValidity, LastUID: uint32(b.UID)}
			done, err := r.copyIMAPMessage(s, f, b, mark)
			if err != nil {
				return err
			}
			if !done {
				if err := st.RecordImported(context.WithoutCancel(r.ctx), sid, f.Local, nil, mark); err != nil {
					return err
				}
			}
		}
	}
	if more {
		// The rest comes with the next repeat, from the watermark.
		msg := "the folder has more than " + strconv.Itoa(maxFolderMessages) + " new messages; run the import again for the rest"
		if err := r.fail(f.Remote, nil, msg); err != nil {
			return err
		}
	}
	return r.endFolder()
}

// metadata fetches UID, flags, date and size of the given messages, in
// UID order.
func (s *session) metadata(batch []imap.UID) ([]*imapclient.FetchMessageBuffer, error) {
	s.lc.allow(controlBytes + int64(len(batch))*metaBytes)
	bufs, err := s.c.Fetch(imap.UIDSetNum(batch...), &imap.FetchOptions{UID: true, Flags: true, InternalDate: true, RFC822Size: true}).Collect()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(bufs, func(a, b *imapclient.FetchMessageBuffer) int { return int(a.UID) - int(b.UID) })
	return bufs, nil
}

// copyIMAPMessage copies one message; done tells whether the watermark
// was moved already.
func (r *runner) copyIMAPMessage(s *session, f planned, b *imapclient.FetchMessageBuffer, mark *storage.Watermark) (bool, error) {
	if slices.ContainsFunc(b.Flags, func(x imap.Flag) bool { return strings.EqualFold(string(x), string(imap.FlagDeleted)) }) {
		return false, r.ctx.Err()
	}
	if r.maxSize > 0 && b.RFC822Size > r.maxSize {
		return false, r.failRaw(f.Remote, "", b.InternalDate.UTC(), "larger than the message size limit")
	}
	if err := r.beforeWrite(b.RFC822Size); err != nil {
		return false, err
	}
	if r.maxSize > 0 {
		s.lc.allow(r.maxSize + bodySlack)
	} else {
		s.lc.allow(-1)
	}
	cmd := s.c.Fetch(imap.UIDSetNum(b.UID), &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
	})
	var res outcome = skipped
	var putErr error
	if msg := cmd.Next(); msg != nil {
		for item := msg.Next(); item != nil; item = msg.Next() {
			bs, ok := item.(imapclient.FetchItemDataBodySection)
			if !ok || bs.Literal == nil {
				continue
			}
			res, _, putErr = r.put(f.Local, f.role == roleAll, incoming{
				body: bs.Literal, flags: imapFlags(b.Flags), date: b.InternalDate, size: b.RFC822Size, wm: mark,
			}, f.Remote)
			io.Copy(io.Discard, bs.Literal)
			break
		}
		for msg.Next() != nil {
		}
	}
	closeErr := cmd.Close()
	if closeErr != nil {
		if putErr == nil || errors.Is(putErr, context.Canceled) {
			e := r.imapErr(s, closeErr, "reading a message of "+f.Remote)
			var re *Error
			if errors.As(e, &re) {
				// The server refused this one message: note it, go on.
				return false, r.failRaw(f.Remote, "", b.InternalDate.UTC(), "the other server did not hand it out: "+s.clean(closeErr))
			}
			return false, e
		}
	}
	if putErr != nil {
		return false, putErr
	}
	return res == copied, nil
}
