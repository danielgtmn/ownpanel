package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
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

func joinFlags(f []string) string { return strings.Join(f, " ") }

func splitFlags(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, " ")
}

// Message is the index entry of one message in a folder.
type Message struct {
	UID          uint32
	Size         int64
	InternalDate time.Time
	Flags        []string

	storageID string
	file      string
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
	var u Usage
	err := s.rdb.QueryRowContext(ctx, `SELECT used_bytes, messages FROM mailboxes WHERE storage_id = ?`, id).Scan(&u.Bytes, &u.Messages)
	if errors.Is(err, sql.ErrNoRows) {
		return Usage{}, nil
	}
	return u, err
}

// StorageIDs returns every storage id that has data.
func (s *Store) StorageIDs(ctx context.Context) ([]string, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT storage_id FROM mailboxes ORDER BY storage_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

const messageCols = `uid, size, internal_date, flags, file`

func scanMessage(sc interface{ Scan(...any) error }, id string) (Message, error) {
	var m Message
	var date int64
	var flags string
	if err := sc.Scan(&m.UID, &m.Size, &date, &flags, &m.file); err != nil {
		return m, err
	}
	m.InternalDate = time.Unix(date, 0)
	m.Flags = splitFlags(flags)
	m.storageID = id
	return m, nil
}

// Select returns a folder and all its messages ordered by UID, read in one
// transaction so that both are consistent (IMAP SELECT).
func (s *Store) Select(ctx context.Context, id, name string) (Folder, []Message, error) {
	var f Folder
	name, err := NormalizeFolderName(name)
	if err != nil {
		return f, nil, err
	}
	if err := s.ensure(ctx, id); err != nil {
		return f, nil, err
	}
	tx, err := s.rdb.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return f, nil, err
	}
	defer tx.Rollback()
	var fid int64
	err = tx.QueryRow(`SELECT id, name, special_use, uid_validity, uid_next, subscribed
		FROM folders WHERE storage_id = ? AND name = ?`, id, name).
		Scan(&fid, &f.Name, &f.SpecialUse, &f.UIDValidity, &f.UIDNext, &f.Subscribed)
	if errors.Is(err, sql.ErrNoRows) {
		return f, nil, ErrNoFolder
	}
	if err != nil {
		return f, nil, err
	}
	rows, err := tx.Query(`SELECT `+messageCols+` FROM messages WHERE folder_id = ? ORDER BY uid`, fid)
	if err != nil {
		return f, nil, err
	}
	defer rows.Close()
	msgs := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows, id)
		if err != nil {
			return f, nil, err
		}
		msgs = append(msgs, m)
	}
	return f, msgs, rows.Err()
}

// Messages returns the messages with the given UIDs (those that exist),
// ordered by UID.
func (s *Store) Messages(ctx context.Context, id, folder string, uids []uint32) ([]Message, error) {
	folder, err := NormalizeFolderName(folder)
	if err != nil {
		return nil, err
	}
	if err := s.ensure(ctx, id); err != nil {
		return nil, err
	}
	var fid int64
	err = s.rdb.QueryRowContext(ctx, `SELECT id FROM folders WHERE storage_id = ? AND name = ?`, id, folder).Scan(&fid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoFolder
	}
	if err != nil {
		return nil, err
	}
	out := []Message{}
	for _, u := range sortedUnique(uids) {
		m, err := scanMessage(s.rdb.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages WHERE folder_id = ? AND uid = ?`, fid, u), id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
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
}

// AppendResult tells where a message was stored.
type AppendResult struct {
	UIDValidity uint32
	UID         uint32
	Size        int64
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
	err = s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		f, err := folderByName(tx, id, folder)
		if err != nil {
			return err
		}
		if quota > 0 {
			var used int64
			if err := tx.QueryRow(`SELECT used_bytes FROM mailboxes WHERE storage_id = ?`, id).Scan(&used); err != nil {
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
		if _, err := tx.Exec(`INSERT INTO messages (folder_id, storage_id, uid, file, size, internal_date, flags) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			f.id, id, f.uidNext, name, size, date.Unix(), joinFlags(flags)); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE folders SET uid_next = uid_next + 1 WHERE id = ?`, f.id); err != nil {
			return err
		}
		if err := addUsage(tx, id, size, 1); err != nil {
			return err
		}
		res = AppendResult{UIDValidity: f.uidValidity, UID: f.uidNext, Size: size}
		ch.add(id, Change{Kind: Appended, Folder: folder, UIDs: []uint32{f.uidNext}})
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

func addUsage(tx *sql.Tx, id string, bytes int64, messages int) error {
	_, err := tx.Exec(`UPDATE mailboxes SET used_bytes = used_bytes + ?, messages = messages + ? WHERE storage_id = ?`, bytes, messages, id)
	return err
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
	err = s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		f, err := folderByName(tx, id, folder)
		if err != nil {
			return err
		}
		var changed []uint32
		for _, u := range sortedUnique(uids) {
			m, err := scanMessage(tx.QueryRow(`SELECT `+messageCols+` FROM messages WHERE folder_id = ? AND uid = ?`, f.id, u), id)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
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
			if joinFlags(next) != joinFlags(m.Flags) {
				if _, err := tx.Exec(`UPDATE messages SET flags = ? WHERE folder_id = ? AND uid = ?`, joinFlags(next), f.id, u); err != nil {
					return err
				}
				changed = append(changed, u)
			}
			m.Flags = slices.Clone(next)
			out = append(out, m)
		}
		if len(changed) > 0 {
			ch.add(id, Change{Kind: FlagsChanged, Folder: folder, UIDs: changed})
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
	err = s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		src, err := folderByName(tx, id, from)
		if err != nil {
			return err
		}
		dst, err := folderByName(tx, id, to)
		if err != nil {
			return err
		}
		res.UIDValidity = dst.uidValidity
		if move {
			if src.id == dst.id {
				return ErrInvalidName
			}
			existing, err := existingUIDs(tx, src.id, uids)
			if err != nil {
				return err
			}
			res.Source = existing
			if res.Dest, err = moveRows(tx, src.id, dst.id, existing); err != nil {
				return err
			}
			if len(existing) > 0 {
				ch.add(id, Change{Kind: Expunged, Folder: from, UIDs: existing})
				ch.add(id, Change{Kind: Appended, Folder: to, UIDs: res.Dest})
			}
			return nil
		}
		next := dst.uidNext
		var added int64
		for _, u := range sortedUnique(uids) {
			m, err := scanMessage(tx.QueryRow(`SELECT `+messageCols+` FROM messages WHERE folder_id = ? AND uid = ?`, src.id, u), id)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO messages (folder_id, storage_id, uid, file, size, internal_date, flags) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				dst.id, id, next, m.file, m.Size, m.InternalDate.Unix(), joinFlags(m.Flags)); err != nil {
				return err
			}
			res.Source = append(res.Source, u)
			res.Dest = append(res.Dest, next)
			added += m.Size
			next++
		}
		if len(res.Dest) == 0 {
			return nil
		}
		if quota > 0 {
			var used int64
			if err := tx.QueryRow(`SELECT used_bytes FROM mailboxes WHERE storage_id = ?`, id).Scan(&used); err != nil {
				return err
			}
			if used+added > quota {
				return ErrOverQuota
			}
		}
		if _, err := tx.Exec(`UPDATE folders SET uid_next = ? WHERE id = ?`, next, dst.id); err != nil {
			return err
		}
		if err := addUsage(tx, id, added, len(res.Dest)); err != nil {
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

func existingUIDs(tx *sql.Tx, folderID int64, uids []uint32) ([]uint32, error) {
	var out []uint32
	for _, u := range sortedUnique(uids) {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM messages WHERE folder_id = ? AND uid = ?`, folderID, u).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, u)
		}
	}
	return out, nil
}

// moveRows moves existing messages (uids sorted) to another folder, giving
// them new UIDs there in the same order.
func moveRows(tx *sql.Tx, srcID, dstID int64, uids []uint32) ([]uint32, error) {
	var next uint32
	if err := tx.QueryRow(`SELECT uid_next FROM folders WHERE id = ?`, dstID).Scan(&next); err != nil {
		return nil, err
	}
	out := make([]uint32, 0, len(uids))
	for _, u := range uids {
		if _, err := tx.Exec(`UPDATE messages SET folder_id = ?, uid = ? WHERE folder_id = ? AND uid = ?`, dstID, next, srcID, u); err != nil {
			return nil, err
		}
		out = append(out, next)
		next++
	}
	_, err := tx.Exec(`UPDATE folders SET uid_next = ? WHERE id = ?`, next, dstID)
	return out, err
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
	err = s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		f, err := folderByName(tx, id, folder)
		if err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT uid FROM messages WHERE folder_id = ? AND instr(' ' || flags || ' ', ' \Deleted ') > 0 ORDER BY uid`, f.id)
		if err != nil {
			return err
		}
		var del []uint32
		for rows.Next() {
			var u uint32
			if err := rows.Scan(&u); err != nil {
				rows.Close()
				return err
			}
			if uids == nil || slices.Contains(uids, u) {
				del = append(del, u)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if removed, err = s.removeMessages(tx, ch, id, f.id, del); err != nil {
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
func (s *Store) removeMessages(tx *sql.Tx, ch *changes, id string, folderID int64, uids []uint32) ([]uint32, error) {
	var removed []uint32
	var bytes int64
	trashed := false
	for _, u := range uids {
		var file string
		var size int64
		err := tx.QueryRow(`DELETE FROM messages WHERE folder_id = ? AND uid = ? RETURNING file, size`, folderID, u).Scan(&file, &size)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		removed = append(removed, u)
		bytes += size
		var refs int
		if err := tx.QueryRow(`SELECT count(*) FROM messages WHERE storage_id = ? AND file = ?`, id, file).Scan(&refs); err != nil {
			return nil, err
		}
		if refs > 0 {
			continue
		}
		from := filepath.Join(s.mailboxDir(id), file)
		to := filepath.Join(s.dir, trashDir, id+"."+file)
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
		if err := addUsage(tx, id, -bytes, -len(removed)); err != nil {
			return nil, err
		}
	}
	return removed, nil
}
