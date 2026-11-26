package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/danielgtmn/ablage"
)

// System flags. \Recent is not stored (IMAP4rev2 dropped it).
const (
	FlagSeen     = `\Seen`
	FlagAnswered = `\Answered`
	FlagFlagged  = `\Flagged`
	FlagDeleted  = `\Deleted`
	FlagDraft    = `\Draft`
)

var systemFlags = map[string]string{
	`\seen`:     FlagSeen,
	`\answered`: FlagAnswered,
	`\flagged`:  FlagFlagged,
	`\deleted`:  FlagDeleted,
	`\draft`:    FlagDraft,
}

// NormalizeFlags checks flags and returns them canonical (system flags in
// their usual spelling), without duplicates (keywords compared without
// case) and sorted.
func NormalizeFlags(flags []string) ([]string, error) {
	out := make([]string, 0, len(flags))
	seen := map[string]bool{}
	for _, f := range flags {
		if strings.HasPrefix(f, `\`) {
			c, ok := systemFlags[strings.ToLower(f)]
			if !ok {
				return nil, fmt.Errorf("%w: %q", ErrInvalidFlag, f)
			}
			f = c
		} else if !validKeyword(f) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidFlag, f)
		}
		k := strings.ToLower(f)
		if !seen[k] {
			seen[k] = true
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out, nil
}

// validKeyword: an IMAP atom of at most 100 characters.
func validKeyword(k string) bool {
	if k == "" || len(k) > 100 {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c <= 0x20 || c >= 0x7f || strings.IndexByte(`(){%*"\]`, c) >= 0 {
			return false
		}
	}
	return true
}

// flagsOf returns the flags of a stored message, sorted like
// NormalizeFlags sorts them.
func flagsOf(m messageRec) []string {
	out := make([]string, 0, len(m.Keywords)+5)
	for _, f := range []struct {
		on   bool
		flag string
	}{{m.Seen, FlagSeen}, {m.Answered, FlagAnswered}, {m.Flagged, FlagFlagged}, {m.Deleted, FlagDeleted}, {m.Draft, FlagDraft}} {
		if f.on {
			out = append(out, f.flag)
		}
	}
	out = append(out, m.Keywords...)
	slices.Sort(out)
	return out
}

// setFlags stores normalized flags in m: system flags as bools, the rest
// as keywords (nil when there are none).
func setFlags(m *messageRec, flags []string) {
	m.Seen, m.Answered, m.Flagged, m.Deleted, m.Draft = false, false, false, false, false
	m.Keywords = nil
	for _, f := range flags {
		switch f {
		case FlagSeen:
			m.Seen = true
		case FlagAnswered:
			m.Answered = true
		case FlagFlagged:
			m.Flagged = true
		case FlagDeleted:
			m.Deleted = true
		case FlagDraft:
			m.Draft = true
		default:
			m.Keywords = append(m.Keywords, f)
		}
	}
}

// flagRow is the update of the flag fields of a message.
func flagRow(m messageRec) ablage.Row {
	return ablage.Row{"seen": m.Seen, "answered": m.Answered, "flagged": m.Flagged, "deleted": m.Deleted, "draft": m.Draft, "keywords": m.Keywords}
}

// Message is the index entry of one message in a folder.
type Message struct {
	UID          uint32
	Size         int64
	InternalDate time.Time
	Flags        []string

	storageID string
	file      string
	// rowID is the index row; column are the flags as stored, seen whether
	// the reader the message was read for has seen it (shared mailboxes).
	rowID  string
	column []string
	seen   bool
}

// HasFlag tells whether the message has flag f (canonical spelling).
func (m Message) HasFlag(f string) bool {
	return slices.ContainsFunc(m.Flags, func(x string) bool { return strings.EqualFold(x, f) })
}

// Open opens the raw message. Message files never change, so the reader
// stays consistent even if the message is expunged meanwhile (the file is
// only unlinked).
func (s *Store) Open(m Message) (*os.File, error) {
	if checkID(m.storageID) != nil || !fileNameRe.MatchString(m.file) {
		return nil, ErrNoMessage
	}
	f, err := os.Open(filepath.Join(s.mailboxDir(m.storageID), m.file))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoMessage
	}
	return f, err
}

// Usage is how much a mailbox stores.
type Usage struct {
	Bytes    int64
	Messages int
}

// Usage returns what a mailbox stores (zero if it has no data).
func (s *Store) Usage(ctx context.Context, id string) (Usage, error) {
	m, err := s.r.mailboxes.Get(ctx, id)
	if errors.Is(err, ablage.ErrNotFound) {
		return Usage{}, nil
	}
	return Usage{Bytes: m.UsedBytes, Messages: int(m.Messages)}, err
}

// StorageIDs returns every storage id that has data.
func (s *Store) StorageIDs(ctx context.Context) ([]string, error) {
	list, err := s.r.mailboxes.Find(ctx, ablage.Query{})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range list {
		out = append(out, m.ID)
	}
	return out, nil
}

// loadMessages turns stored messages into Messages. With a reader (a
// shared mailbox, see SelectAs) \Seen is that reader's own read state, not
// the stored flag; it is read in one query for all of them.
func loadMessages(ctx context.Context, tx *repos, id, reader string, recs []messageRec) ([]Message, error) {
	seen := map[string]bool{}
	if reader != "" && len(recs) > 0 {
		ids := make([]string, len(recs))
		for i, m := range recs {
			ids[i] = m.ID
		}
		list, err := tx.sharedSeen.Find(ctx, ablage.Query{Where: ablage.And(ablage.Eq("reader", reader), ablage.In("message_id", ids...))})
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			seen[r.MessageID] = true
		}
	}
	out := make([]Message, len(recs))
	for i, r := range recs {
		m := Message{UID: uint32(r.UID), Size: r.Size, InternalDate: local(r.InternalDate),
			storageID: id, file: r.File, rowID: r.ID, column: flagsOf(r), seen: seen[r.ID]}
		m.Flags = m.column
		if reader != "" {
			m.Flags = withSeen(m.column, m.seen)
		}
		out[i] = m
	}
	return out, nil
}

// withSeen returns flags with \Seen set or not.
func withSeen(flags []string, seen bool) []string {
	out := slices.DeleteFunc(slices.Clone(flags), func(f string) bool { return f == FlagSeen })
	if seen {
		out, _ = NormalizeFlags(append(out, FlagSeen))
	}
	return out
}

// Select returns a folder and all its messages ordered by UID, read in one
// transaction so that both are consistent (IMAP SELECT).
func (s *Store) Select(ctx context.Context, id, name string) (Folder, []Message, error) {
	return s.SelectAs(ctx, id, name, "")
}

// SelectAs is Select for a reader (a storage id) of a shared mailbox: \Seen
// is the reader's own read state. "" is Select.
func (s *Store) SelectAs(ctx context.Context, id, name, reader string) (Folder, []Message, error) {
	var f Folder
	name, err := NormalizeFolderName(name)
	if err != nil {
		return f, nil, err
	}
	if err := s.ensure(ctx, id); err != nil {
		return f, nil, err
	}
	var msgs []Message
	err = s.read(ctx, func(tx *repos) error {
		fr, err := folderByName(ctx, tx, id, name)
		if err != nil {
			return err
		}
		f = fr.folder()
		recs, err := tx.messages.Find(ctx, ablage.Query{Where: ablage.Eq("folder_id", fr.ID), OrderBy: []ablage.Order{ablage.Asc("uid")}})
		if err != nil {
			return err
		}
		msgs, err = loadMessages(ctx, tx, id, reader, recs)
		return err
	})
	if err != nil {
		return Folder{}, nil, err
	}
	return f, msgs, nil
}

// Messages returns the messages with the given UIDs (those that exist),
// ordered by UID.
func (s *Store) Messages(ctx context.Context, id, folder string, uids []uint32) ([]Message, error) {
	return s.MessagesAs(ctx, id, folder, uids, "")
}

// MessagesAs is Messages for a reader of a shared mailbox (see SelectAs).
func (s *Store) MessagesAs(ctx context.Context, id, folder string, uids []uint32, reader string) ([]Message, error) {
	folder, err := NormalizeFolderName(folder)
	if err != nil {
		return nil, err
	}
	if err := s.ensure(ctx, id); err != nil {
		return nil, err
	}
	var out []Message
	err = s.read(ctx, func(tx *repos) error {
		f, err := folderByName(ctx, tx, id, folder)
		if err != nil {
			return err
		}
		out, err = messagesByUID(ctx, tx, id, f.ID, uids, reader)
		return err
	})
	return out, err
}

// messagesByUID reads the messages of a folder with the given UIDs (those
// that exist), ordered by UID.
func messagesByUID(ctx context.Context, tx *repos, id, folderID string, uids []uint32, reader string) ([]Message, error) {
	recs, err := tx.messages.Find(ctx, ablage.Query{
		Where:   ablage.And(ablage.Eq("folder_id", folderID), ablage.In("uid", sortedUnique(uids)...)),
		OrderBy: []ablage.Order{ablage.Asc("uid")},
	})
	if err != nil {
		return nil, err
	}
	return loadMessages(ctx, tx, id, reader, recs)
}

func sortedUnique(uids []uint32) []uint32 {
	u := slices.Clone(uids)
	slices.Sort(u)
	return slices.Compact(u)
}

// AppendOptions describe a message being added.
type AppendOptions struct {
	Flags []string
	// Date is the internal date; zero means now.
	Date time.Time
	// MaxSize refuses larger messages with ErrTooLarge; 0 means no limit.
	MaxSize int64
	// Reader (a storage id) appends to a shared mailbox: \Seen in Flags is
	// that reader's own read state.
	Reader string
	// Dedupe stores a message whose Message-ID is in the folder already
	// (from an earlier Dedupe append) only once: the result is then the
	// existing message with Duplicate set.
	Dedupe bool
}

// AppendResult tells where a message was stored.
type AppendResult struct {
	UIDValidity uint32
	UID         uint32
	Size        int64
	// Duplicate: the message was there already (AppendOptions.Dedupe).
	Duplicate bool
}

// Append stores a message in a folder. It returns only once the message
// file and the index are durable: the file is written to a temporary name
// and fsynced, renamed into place, the directory is fsynced, and then the
// index row is committed (synchronous=FULL).
func (s *Store) Append(ctx context.Context, id, folder string, r io.Reader, opts AppendOptions) (AppendResult, error) {
	var res AppendResult
	folder, err := NormalizeFolderName(folder)
	if err != nil {
		return res, err
	}
	flags, err := NormalizeFlags(opts.Flags)
	if err != nil {
		return res, err
	}
	if err := s.ensure(ctx, id); err != nil {
		return res, err
	}
	quota := s.quota(id)
	if quota > 0 {
		// Refuse before reading the message when already full.
		if u, err := s.Usage(ctx, id); err != nil {
			return res, err
		} else if u.Bytes >= quota {
			return res, ErrOverQuota
		}
	}
	tmp, size, err := s.spool(r, opts.MaxSize)
	if err != nil {
		return res, err
	}
	defer os.Remove(tmp) // no-op once renamed
	date := opts.Date
	if date.IsZero() {
		date = s.now()
	}
	msgid := ""
	if opts.Dedupe {
		msgid = messageIDOf(tmp)
	}
	seen := false
	if opts.Reader != "" && slices.Contains(flags, FlagSeen) {
		seen, flags = true, withSeen(flags, false)
	}
	err = s.write(ctx, func(tx *repos, ch *changes) error {
		f, err := folderByName(ctx, tx, id, folder)
		if err != nil {
			return err
		}
		if msgid != "" {
			dup, err := tx.messages.Find(ctx, ablage.Query{
				Where:   ablage.And(ablage.Eq("folder_id", f.ID), ablage.Eq("msgid", msgid)),
				OrderBy: []ablage.Order{ablage.Asc("uid")}, Limit: 1,
			})
			if err != nil {
				return err
			}
			if len(dup) > 0 {
				res = AppendResult{UIDValidity: uint32(f.UIDValidity), UID: uint32(dup[0].UID), Size: dup[0].Size, Duplicate: true}
				return nil
			}
		}
		if quota > 0 {
			used, err := usedBytes(ctx, tx, id)
			if err != nil {
				return err
			}
			if used+size > quota {
				return ErrOverQuota
			}
		}
		name, err := s.place(ch, id, tmp)
		if err != nil {
			return err
		}
		m := messageRec{FolderID: f.ID, StorageID: id, UID: f.UIDNext, File: name, Size: size, InternalDate: sec(date), MsgID: msgid}
		setFlags(&m, flags)
		if err := tx.messages.Create(ctx, &m); err != nil {
			return err
		}
		if seen {
			if err := tx.sharedSeen.Create(ctx, &sharedSeenRec{MessageID: m.ID, Reader: opts.Reader}); err != nil {
				return err
			}
		}
		if err := tx.folders.Patch(ctx, f.ID, ablage.Row{"uid_next": ablage.Incr(1)}); err != nil {
			return err
		}
		if err := addUsage(ctx, tx, id, size, 1); err != nil {
			return err
		}
		res = AppendResult{UIDValidity: uint32(f.UIDValidity), UID: uint32(f.UIDNext), Size: size}
		ch.add(id, Change{Kind: Appended, Folder: folder, UIDs: []uint32{uint32(f.UIDNext)}})
		return nil
	})
	return res, err
}

// spool writes r to a temporary file and fsyncs it.
func (s *Store) spool(r io.Reader, maxSize int64) (string, int64, error) {
	f, err := os.CreateTemp(s.TempDir(), "append-*")
	if err != nil {
		return "", 0, err
	}
	name := f.Name()
	fail := func(err error) (string, int64, error) {
		f.Close()
		os.Remove(name)
		return "", 0, err
	}
	src := r
	if maxSize > 0 {
		src = io.LimitReader(r, maxSize+1)
	}
	n, err := io.Copy(f, src)
	if err != nil {
		return fail(err)
	}
	if maxSize > 0 && n > maxSize {
		return fail(ErrTooLarge)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", 0, err
	}
	return name, n, nil
}

// place moves a spooled file into the mailbox directory under a new name
// and fsyncs the directory. Callers hold the write lock.
func (s *Store) place(ch *changes, id, tmp string) (string, error) {
	dir := s.mailboxDir(id)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", err
		}
		if err := syncDir(filepath.Dir(dir)); err != nil {
			return "", err
		}
	}
	name := fmt.Sprintf("%d.%s", s.now().UnixNano(), randHex(8))
	dst := filepath.Join(dir, name)
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	ch.placed = append(ch.placed, dst)
	if err := syncDir(dir); err != nil {
		return "", err
	}
	return name, nil
}

// addUsage adds to the usage counters of a mailbox.
func addUsage(ctx context.Context, tx *repos, id string, bytes int64, messages int) error {
	_, err := tx.mailboxes.UpdateWhere(ctx, ablage.Eq("id", id), ablage.Row{"used_bytes": ablage.Incr(bytes), "messages": ablage.Incr(int64(messages))})
	return err
}

// usedBytes is what a mailbox uses of its quota.
func usedBytes(ctx context.Context, tx *repos, id string) (int64, error) {
	m, err := tx.mailboxes.Get(ctx, id)
	return m.UsedBytes, err
}

// FlagOp is how SetFlags changes flags.
type FlagOp int

const (
	FlagsReplace FlagOp = iota
	FlagsAdd
	FlagsRemove
)

// SetFlags changes the flags of the messages with the given UIDs and
// returns them as they are afterwards (only those that exist).
func (s *Store) SetFlags(ctx context.Context, id, folder string, uids []uint32, op FlagOp, flags []string) ([]Message, error) {
	return s.SetFlagsAs(ctx, id, folder, uids, op, flags, "")
}

// SetFlagsAs is SetFlags for a reader of a shared mailbox: \Seen is the
// reader's own read state, the other flags are everyone's. A change of the
// read state alone is reported with Change.Reader set.
func (s *Store) SetFlagsAs(ctx context.Context, id, folder string, uids []uint32, op FlagOp, flags []string, reader string) ([]Message, error) {
	folder, err := NormalizeFolderName(folder)
	if err != nil {
		return nil, err
	}
	flags, err = NormalizeFlags(flags)
	if err != nil {
		return nil, err
	}
	if err := s.ensure(ctx, id); err != nil {
		return nil, err
	}
	var out []Message
	err = s.write(ctx, func(tx *repos, ch *changes) error {
		f, err := folderByName(ctx, tx, id, folder)
		if err != nil {
			return err
		}
		msgs, err := messagesByUID(ctx, tx, id, f.ID, uids, reader)
		if err != nil {
			return err
		}
		var changed, seenChanged []uint32
		for _, m := range msgs {
			var next []string
			switch op {
			case FlagsReplace:
				next = flags
			case FlagsAdd:
				next, _ = NormalizeFlags(append(slices.Clone(m.Flags), flags...))
			case FlagsRemove:
				next = slices.DeleteFunc(slices.Clone(m.Flags), func(x string) bool {
					return slices.ContainsFunc(flags, func(y string) bool { return strings.EqualFold(x, y) })
				})
			default:
				return errors.New("unknown flag operation")
			}
			column := next
			if reader != "" {
				// The stored \Seen is nobody's read state here: kept as it is.
				column = withSeen(next, slices.Contains(m.column, FlagSeen))
				if seen := slices.Contains(next, FlagSeen); seen != m.seen {
					if seen {
						err = tx.sharedSeen.Create(ctx, &sharedSeenRec{MessageID: m.rowID, Reader: reader})
					} else {
						_, err = tx.sharedSeen.DeleteWhere(ctx, ablage.And(ablage.Eq("message_id", m.rowID), ablage.Eq("reader", reader)))
					}
					if err != nil {
						return err
					}
					seenChanged = append(seenChanged, m.UID)
				}
			}
			if !slices.Equal(column, m.column) {
				var r messageRec
				setFlags(&r, column)
				if err := tx.messages.Patch(ctx, m.rowID, flagRow(r)); err != nil {
					return err
				}
				changed = append(changed, m.UID)
			}
			m.Flags = slices.Clone(next)
			out = append(out, m)
		}
		if len(changed) > 0 {
			ch.add(id, Change{Kind: FlagsChanged, Folder: folder, UIDs: changed})
		}
		seenChanged = slices.DeleteFunc(seenChanged, func(u uint32) bool { return slices.Contains(changed, u) })
		if len(seenChanged) > 0 {
			ch.add(id, Change{Kind: FlagsChanged, Folder: folder, UIDs: seenChanged, Reader: reader})
		}
		return nil
	})
	return out, err
}

// CopyResult maps source UIDs to the UIDs in the destination folder
// (UIDPLUS COPYUID / MOVE).
type CopyResult struct {
	UIDValidity uint32 // of the destination
	Source      []uint32
	Dest        []uint32
}

// Copy copies messages into another folder of the same mailbox, with their
// flags and internal date. Copies share the immutable file but count
// against the quota like separate messages.
func (s *Store) Copy(ctx context.Context, id, from string, uids []uint32, to string) (CopyResult, error) {
	return s.copyOrMove(ctx, id, from, uids, to, false)
}

// Move moves messages into another folder of the same mailbox (they get new
// UIDs there).
func (s *Store) Move(ctx context.Context, id, from string, uids []uint32, to string) (CopyResult, error) {
	return s.copyOrMove(ctx, id, from, uids, to, true)
}

func (s *Store) copyOrMove(ctx context.Context, id, from string, uids []uint32, to string, move bool) (CopyResult, error) {
	var res CopyResult
	from, err := NormalizeFolderName(from)
	if err != nil {
		return res, err
	}
	to, err = NormalizeFolderName(to)
	if err != nil {
		return res, err
	}
	if err := s.ensure(ctx, id); err != nil {
		return res, err
	}
	quota := s.quota(id)
	err = s.write(ctx, func(tx *repos, ch *changes) error {
		src, err := folderByName(ctx, tx, id, from)
		if err != nil {
			return err
		}
		dst, err := folderByName(ctx, tx, id, to)
		if err != nil {
			return err
		}
		res.UIDValidity = uint32(dst.UIDValidity)
		if move {
			if src.ID == dst.ID {
				return ErrInvalidName
			}
			existing, err := existingUIDs(ctx, tx, src.ID, uids)
			if err != nil {
				return err
			}
			res.Source = existing
			if res.Dest, err = moveRows(ctx, tx, src.ID, dst.ID, existing); err != nil {
				return err
			}
			if len(existing) > 0 {
				ch.add(id, Change{Kind: Expunged, Folder: from, UIDs: existing})
				ch.add(id, Change{Kind: Appended, Folder: to, UIDs: res.Dest})
			}
			return nil
		}
		recs, err := tx.messages.Find(ctx, ablage.Query{
			Where:   ablage.And(ablage.Eq("folder_id", src.ID), ablage.In("uid", sortedUnique(uids)...)),
			OrderBy: []ablage.Order{ablage.Asc("uid")},
		})
		if err != nil {
			return err
		}
		next := dst.UIDNext
		var added int64
		copies := map[string]string{} // source row → copy
		for _, m := range recs {
			c := m
			c.ID, c.FolderID, c.UID, c.MsgID = "", dst.ID, next, ""
			if err := tx.messages.Create(ctx, &c); err != nil {
				return err
			}
			copies[m.ID] = c.ID
			res.Source = append(res.Source, uint32(m.UID))
			res.Dest = append(res.Dest, uint32(next))
			added += m.Size
			next++
		}
		if len(res.Dest) == 0 {
			return nil
		}
		// In a shared mailbox every reader's read state goes with the copy.
		if err := copySeen(ctx, tx, copies); err != nil {
			return err
		}
		if quota > 0 {
			used, err := usedBytes(ctx, tx, id)
			if err != nil {
				return err
			}
			if used+added > quota {
				return ErrOverQuota
			}
		}
		if err := tx.folders.Patch(ctx, dst.ID, ablage.Row{"uid_next": next}); err != nil {
			return err
		}
		if err := addUsage(ctx, tx, id, added, len(res.Dest)); err != nil {
			return err
		}
		ch.add(id, Change{Kind: Appended, Folder: to, UIDs: res.Dest})
		return nil
	})
	if err != nil {
		return CopyResult{}, err
	}
	return res, nil
}

// copySeen gives copies of messages (source row → copy row) the read
// state of every reader of the source.
func copySeen(ctx context.Context, tx *repos, copies map[string]string) error {
	src := make([]string, 0, len(copies))
	for id := range copies {
		src = append(src, id)
	}
	list, err := tx.sharedSeen.Find(ctx, ablage.Query{Where: ablage.In("message_id", src...)})
	if err != nil {
		return err
	}
	for _, r := range list {
		if err := tx.sharedSeen.Create(ctx, &sharedSeenRec{MessageID: copies[r.MessageID], Reader: r.Reader}); err != nil {
			return err
		}
	}
	return nil
}

// existingUIDs returns those of uids that are in the folder, ascending.
func existingUIDs(ctx context.Context, tx *repos, folderID string, uids []uint32) ([]uint32, error) {
	recs, err := tx.messages.Find(ctx, ablage.Query{
		Where:   ablage.And(ablage.Eq("folder_id", folderID), ablage.In("uid", sortedUnique(uids)...)),
		OrderBy: []ablage.Order{ablage.Asc("uid")},
	})
	if err != nil {
		return nil, err
	}
	var out []uint32
	for _, m := range recs {
		out = append(out, uint32(m.UID))
	}
	return out, nil
}

// moveRows moves existing messages (uids sorted) to another folder, giving
// them new UIDs there in the same order.
func moveRows(ctx context.Context, tx *repos, srcID, dstID string, uids []uint32) ([]uint32, error) {
	dst, err := tx.folders.Get(ctx, dstID)
	if err != nil {
		return nil, err
	}
	recs, err := tx.messages.Find(ctx, ablage.Query{
		Where:   ablage.And(ablage.Eq("folder_id", srcID), ablage.In("uid", uids...)),
		OrderBy: []ablage.Order{ablage.Asc("uid")},
	})
	if err != nil {
		return nil, err
	}
	next := dst.UIDNext
	out := make([]uint32, 0, len(recs))
	for _, m := range recs {
		if err := tx.messages.Patch(ctx, m.ID, ablage.Row{"folder_id": dstID, "uid": next}); err != nil {
			return nil, err
		}
		out = append(out, uint32(next))
		next++
	}
	return out, tx.folders.Patch(ctx, dstID, ablage.Row{"uid_next": next})
}

// Expunge removes the messages flagged \Deleted, of the given UIDs only or
// of the whole folder if uids is nil (UID EXPUNGE / EXPUNGE). It returns
// the removed UIDs in ascending order.
func (s *Store) Expunge(ctx context.Context, id, folder string, uids []uint32) ([]uint32, error) {
	folder, err := NormalizeFolderName(folder)
	if err != nil {
		return nil, err
	}
	if err := s.ensure(ctx, id); err != nil {
		return nil, err
	}
	var removed []uint32
	err = s.write(ctx, func(tx *repos, ch *changes) error {
		f, err := folderByName(ctx, tx, id, folder)
		if err != nil {
			return err
		}
		where := ablage.And(ablage.Eq("folder_id", f.ID), ablage.Eq("deleted", true))
		if uids != nil {
			where = ablage.And(where, ablage.In("uid", uids...))
		}
		recs, err := tx.messages.Find(ctx, ablage.Query{Where: where, OrderBy: []ablage.Order{ablage.Asc("uid")}})
		if err != nil {
			return err
		}
		del := make([]uint32, len(recs))
		for i, m := range recs {
			del[i] = uint32(m.UID)
		}
		if removed, err = s.removeMessages(ctx, tx, ch, id, f.ID, del); err != nil {
			return err
		}
		if len(removed) > 0 {
			ch.add(id, Change{Kind: Expunged, Folder: folder, UIDs: removed})
		}
		return nil
	})
	return removed, err
}

// removeMessages deletes index rows and moves files no other row uses to
// the trash directory (removed after commit). The mailbox directory is
// fsynced before the commit, so a crash can never bring a deleted message
// back through reconciliation.
func (s *Store) removeMessages(ctx context.Context, tx *repos, ch *changes, id, folderID string, uids []uint32) ([]uint32, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	recs, err := tx.messages.Find(ctx, ablage.Query{
		Where:   ablage.And(ablage.Eq("folder_id", folderID), ablage.In("uid", uids...)),
		OrderBy: []ablage.Order{ablage.Asc("uid")},
	})
	if err != nil {
		return nil, err
	}
	var removed []uint32
	var bytes int64
	trashed := false
	for _, m := range recs {
		if err := tx.messages.Delete(ctx, m.ID); err != nil {
			return nil, err
		}
		removed = append(removed, uint32(m.UID))
		bytes += m.Size
		refs, err := tx.messages.Count(ctx, ablage.And(ablage.Eq("storage_id", id), ablage.Eq("file", m.File)))
		if err != nil {
			return nil, err
		}
		if refs > 0 {
			continue
		}
		from := filepath.Join(s.mailboxDir(id), m.File)
		to := filepath.Join(s.dir, trashDir, id+"."+m.File)
		if err := os.Rename(from, to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		} else if err == nil {
			ch.trashed = append(ch.trashed, rename{from, to})
			trashed = true
		}
	}
	if trashed {
		if err := syncDir(s.mailboxDir(id)); err != nil {
			return nil, err
		}
	}
	if len(removed) > 0 {
		if err := addUsage(ctx, tx, id, -bytes, -len(removed)); err != nil {
			return nil, err
		}
	}
	return removed, nil
}
