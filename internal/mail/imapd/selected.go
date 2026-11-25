package imapd

import (
	"slices"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// selected is a session's view of its selected folder.
type selected struct {
	// t is the folder: the session's own or one of another mailbox.
	t target
	// readOnly: nothing may change (EXAMINE, or no write right); examine:
	// the client asked for it, so not even its own read state changes.
	readOnly bool
	examine  bool
	// uids is the folder as the client knows it: sequence number i+1 is
	// uids[i]. Only the session's goroutine changes it, and outside poll
	// the client has been told about every entry (EXISTS).
	uids []uint32
	// queue holds changes from the storage not applied to uids yet
	// (guarded by session.mu).
	queue []storage.Change
	// searchRes is the result saved by SEARCH RETURN (SAVE) (RFC 5182).
	searchRes imap.UIDSet
}

// entry is a message of the view.
type entry struct {
	seq uint32
	uid uint32
}

var (
	permanentFlags = []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDeleted, imap.FlagDraft}
	allFlags       = permanentFlags
)

func (s *session) Select(name string, options *imap.SelectOptions) (*imap.SelectData, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	t, err := s.resolve(s.srv.opts.Directory(), name)
	if err != nil {
		return nil, err
	}
	// Watch first, then read: a change committed in between is both in
	// the queue and in the snapshot, and applying the queue is idempotent.
	// (The watches of every readable mailbox are always there.)
	readOnly := options.ReadOnly || !t.rights.Has(config.Write)
	sel := &selected{t: t, readOnly: readOnly, examine: options.ReadOnly}
	s.mu.Lock()
	s.sel = sel
	s.mu.Unlock()
	f, msgs, err := s.srv.opts.Store.SelectAs(s.ctx, t.sid, t.folder, t.reader)
	if err != nil {
		s.mu.Lock()
		s.sel = nil
		s.mu.Unlock()
		return nil, s.fail(err)
	}
	sel.uids = make([]uint32, len(msgs))
	var firstUnseen uint32
	for i, m := range msgs {
		sel.uids[i] = m.UID
		if firstUnseen == 0 && !m.HasFlag(storage.FlagSeen) {
			firstUnseen = uint32(i + 1)
		}
	}
	perm := append(slices.Clone(permanentFlags), imap.FlagWildcard)
	switch {
	case options.ReadOnly:
		perm = nil
	case readOnly && t.reader != "":
		// Read only, but the own read state is kept.
		perm = []imap.Flag{imap.FlagSeen}
	case readOnly:
		perm = nil
	}
	return &imap.SelectData{
		ReadOnly:          readOnly && !options.ReadOnly,
		Flags:             allFlags,
		PermanentFlags:    perm,
		NumMessages:       uint32(len(msgs)),
		FirstUnseenSeqNum: firstUnseen,
		UIDNext:           imap.UID(f.UIDNext),
		UIDValidity:       f.UIDValidity,
	}, nil
}

func (s *session) Unselect() error {
	s.mu.Lock()
	s.sel = nil
	s.mu.Unlock()
	return nil
}

// Poll applies queued changes to the view and tells the client: EXISTS
// for new messages, EXPUNGE (only when allowed; otherwise they wait),
// FETCH FLAGS for flags changed by others.
func (s *session) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	if err := s.check(); err != nil {
		return err
	}
	// IDLE polls from another goroutine that may outlive the connection
	// for a moment, so the selection is read under the lock.
	s.mu.Lock()
	sel := s.sel
	if sel == nil {
		s.mu.Unlock()
		return nil
	}
	queue := sel.queue
	sel.queue = nil
	s.mu.Unlock()
	if len(queue) == 0 {
		return nil
	}
	announced := len(sel.uids)
	var deferred []storage.Change
	flagged := map[uint32]bool{}
	for _, c := range queue {
		switch c.Kind {
		case storage.Appended:
			for _, u := range c.UIDs {
				// UIDs only grow; anything not above the last one is
				// already in the view or was expunged before the select.
				if len(sel.uids) == 0 || u > sel.uids[len(sel.uids)-1] {
					sel.uids = append(sel.uids, u)
				}
			}
		case storage.Expunged:
			if !allowExpunge {
				deferred = append(deferred, c)
				continue
			}
			for _, u := range c.UIDs {
				i, ok := slices.BinarySearch(sel.uids, u)
				if !ok {
					continue
				}
				// The client must know a message before it hears that it
				// is gone.
				if len(sel.uids) != announced {
					if err := w.WriteNumMessages(uint32(len(sel.uids))); err != nil {
						return err
					}
					announced = len(sel.uids)
				}
				if err := w.WriteExpunge(uint32(i + 1)); err != nil {
					return err
				}
				sel.uids = slices.Delete(sel.uids, i, i+1)
				announced--
			}
		case storage.FlagsChanged:
			for _, u := range c.UIDs {
				flagged[u] = true
			}
		}
	}
	if len(deferred) > 0 {
		s.mu.Lock()
		sel.queue = append(deferred, sel.queue...)
		s.mu.Unlock()
	}
	if len(sel.uids) != announced {
		if err := w.WriteNumMessages(uint32(len(sel.uids))); err != nil {
			return err
		}
	}
	if len(flagged) == 0 {
		return nil
	}
	var entries []entry
	for i, u := range sel.uids {
		if flagged[u] {
			entries = append(entries, entry{uint32(i + 1), u})
		}
	}
	msgs, err := s.load(sel, entries)
	if err != nil {
		return s.fail(err)
	}
	for _, e := range entries {
		if m, ok := msgs[e.uid]; ok {
			if err := w.WriteMessageFlags(e.seq, imap.UID(e.uid), imapFlags(m.Flags)); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolve returns the messages of the view a sequence or UID set names,
// in sequence order. "*" is the last message; "$" the saved search.
func (sel *selected) resolve(numSet imap.NumSet) []entry {
	var out []entry
	if imap.IsSearchRes(numSet) {
		numSet = sel.searchRes
	}
	switch set := numSet.(type) {
	case imap.SeqSet:
		n := uint32(len(sel.uids))
		for i, u := range sel.uids {
			if seqContains(set, uint32(i+1), n) {
				out = append(out, entry{uint32(i + 1), u})
			}
		}
	case imap.UIDSet:
		var last uint32
		if len(sel.uids) > 0 {
			last = sel.uids[len(sel.uids)-1]
		}
		for i, u := range sel.uids {
			if uidContains(set, u, last) {
				out = append(out, entry{uint32(i + 1), u})
			}
		}
	}
	return out
}

// seqContains tells whether set contains n, "*" (0) being max.
func seqContains(set imap.SeqSet, n, max uint32) bool {
	for _, r := range set {
		if inRange(r.Start, r.Stop, n, max) {
			return true
		}
	}
	return false
}

func uidContains(set imap.UIDSet, u, max uint32) bool {
	for _, r := range set {
		if inRange(uint32(r.Start), uint32(r.Stop), u, max) {
			return true
		}
	}
	return false
}

func inRange(start, stop, n, max uint32) bool {
	if start == 0 {
		start = max
	}
	if stop == 0 {
		stop = max
	}
	if start > stop {
		start, stop = stop, start
	}
	return n >= start && n <= stop
}

// load reads the index entries of messages; those expunged meanwhile are
// missing from the result.
func (s *session) load(sel *selected, entries []entry) (map[uint32]storage.Message, error) {
	out := make(map[uint32]storage.Message, len(entries))
	if len(entries) == 0 {
		return out, nil
	}
	var msgs []storage.Message
	var err error
	if len(entries) > 32 {
		// One query for the whole folder beats one per message.
		_, msgs, err = s.srv.opts.Store.SelectAs(s.ctx, sel.t.sid, sel.t.folder, sel.t.reader)
	} else {
		uids := make([]uint32, len(entries))
		for i, e := range entries {
			uids[i] = e.uid
		}
		msgs, err = s.srv.opts.Store.MessagesAs(s.ctx, sel.t.sid, sel.t.folder, uids, sel.t.reader)
	}
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		out[m.UID] = m
	}
	return out, nil
}

func uidsOf(entries []entry) []uint32 {
	out := make([]uint32, len(entries))
	for i, e := range entries {
		out[i] = e.uid
	}
	return out
}

func (s *session) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, _ *imap.StoreOptions) error {
	if err := s.check(); err != nil {
		return err
	}
	sel := s.sel
	fl, err := storeFlags(flags.Flags)
	if err != nil {
		return s.fail(err)
	}
	if err := sel.mayStore(flags.Op, fl); err != nil {
		return err
	}
	var op storage.FlagOp
	switch flags.Op {
	case imap.StoreFlagsSet:
		op = storage.FlagsReplace
	case imap.StoreFlagsAdd:
		op = storage.FlagsAdd
	case imap.StoreFlagsDel:
		op = storage.FlagsRemove
	}
	entries := sel.resolve(numSet)
	if len(entries) == 0 {
		return nil
	}
	msgs, err := s.srv.opts.Store.SetFlagsAs(s.ctx, sel.t.sid, sel.t.folder, uidsOf(entries), op, fl, sel.t.reader)
	if err != nil {
		return s.fail(err)
	}
	if flags.Silent {
		return nil
	}
	byUID := make(map[uint32]storage.Message, len(msgs))
	for _, m := range msgs {
		byUID[m.UID] = m
	}
	for _, e := range entries {
		m, ok := byUID[e.uid]
		if !ok {
			continue
		}
		rw := w.CreateMessage(e.seq)
		rw.WriteUID(imap.UID(e.uid))
		rw.WriteFlags(imapFlags(m.Flags))
		if err := rw.Close(); err != nil {
			return err
		}
	}
	return nil
}

func copyData(res storage.CopyResult) *imap.CopyData {
	if len(res.Source) == 0 {
		return nil
	}
	d := &imap.CopyData{UIDValidity: res.UIDValidity}
	for i := range res.Source {
		d.SourceUIDs.AddNum(imap.UID(res.Source[i]))
		d.DestUIDs.AddNum(imap.UID(res.Dest[i]))
	}
	return d
}

func (s *session) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	sel := s.sel
	dst, err := s.resolve(s.srv.opts.Directory(), dest)
	if err != nil {
		return nil, errTryCreate(err)
	}
	// Copying out needs only read (the selection); copying in needs write.
	if err := dst.need(config.Write); err != nil {
		return nil, err
	}
	entries := sel.resolve(numSet)
	if len(entries) == 0 {
		return nil, nil
	}
	// A copy is a new message file; like APPEND it is refused for now
	// when the disk is nearly full.
	if err := s.checkDisk(); err != nil {
		return nil, err
	}
	res, err := s.copyTo(sel, entries, dst, false)
	if err != nil {
		return nil, s.fail(errTryCreate(err))
	}
	return copyData(res), nil
}

// copyTo copies or moves messages of the selection into dst, within the
// mailbox or into another one (quota of the destination).
func (s *session) copyTo(sel *selected, entries []entry, dst target, move bool) (storage.CopyResult, error) {
	st := s.srv.opts.Store
	if dst.sid == sel.t.sid {
		if move {
			return st.Move(s.ctx, sel.t.sid, sel.t.folder, uidsOf(entries), dst.folder)
		}
		return st.Copy(s.ctx, sel.t.sid, sel.t.folder, uidsOf(entries), dst.folder)
	}
	return st.CopyAcross(s.ctx, sel.t.sid, sel.t.folder, uidsOf(entries), dst.sid, dst.folder,
		storage.CrossOptions{Move: move, SrcReader: sel.t.reader, DstReader: dst.reader})
}

// mayStore tells whether STORE with op and flags is allowed: not after
// EXAMINE; with read only, nothing but the reader's own \Seen in a shared
// mailbox.
func (sel *selected) mayStore(op imap.StoreFlagsOp, flags []string) error {
	if sel.examine {
		return errReadOnly
	}
	if !sel.readOnly {
		return nil
	}
	if sel.t.reader != "" && op != imap.StoreFlagsSet && len(flags) == 1 && flags[0] == storage.FlagSeen {
		return nil
	}
	if sel.t.shared {
		return errNoPerm
	}
	return errReadOnly
}

// mayChange refuses changes of a read-only selection.
func (sel *selected) mayChange() error {
	switch {
	case sel.examine:
		return errReadOnly
	case sel.readOnly && sel.t.shared:
		return errNoPerm
	case sel.readOnly:
		return errReadOnly
	}
	return nil
}

func (s *session) Move(w *imapserver.MoveWriter, numSet imap.NumSet, dest string) error {
	if err := s.check(); err != nil {
		return err
	}
	sel := s.sel
	if err := sel.mayChange(); err != nil {
		return err
	}
	dst, err := s.resolve(s.srv.opts.Directory(), dest)
	if err != nil {
		return errTryCreate(err)
	}
	if err := dst.need(config.Write); err != nil {
		return err
	}
	entries := sel.resolve(numSet)
	if len(entries) == 0 {
		return nil
	}
	// Moving writes to the storage too (new UIDs, index entries).
	if err := s.checkDisk(); err != nil {
		return err
	}
	res, err := s.copyTo(sel, entries, dst, true)
	if err != nil {
		return s.fail(errTryCreate(err))
	}
	if err := w.WriteCopyData(copyData(res)); err != nil {
		return err
	}
	// The queue has the same expunges; poll skips UIDs no longer in the
	// view.
	for _, u := range res.Source {
		i, ok := slices.BinarySearch(sel.uids, u)
		if !ok {
			continue
		}
		if err := w.WriteExpunge(uint32(i + 1)); err != nil {
			return err
		}
		sel.uids = slices.Delete(sel.uids, i, i+1)
	}
	return nil
}

// Expunge removes messages flagged \Deleted; the EXPUNGE responses are
// written by the poll after the command (none after CLOSE, which
// unselects first).
func (s *session) Expunge(_ *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	if err := s.check(); err != nil {
		return err
	}
	sel := s.sel
	if err := sel.mayChange(); err != nil {
		return err
	}
	var only []uint32
	if uids != nil {
		only = uidsOf(sel.resolve(*uids))
		if len(only) == 0 {
			return nil
		}
	}
	_, err := s.srv.opts.Store.Expunge(s.ctx, sel.t.sid, sel.t.folder, only)
	return s.fail(err)
}
