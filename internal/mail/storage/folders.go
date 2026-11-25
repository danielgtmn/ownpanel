package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Separator is the hierarchy delimiter of folder names.
const Separator = "/"

// Inbox is the name of the folder new mail goes to.
const Inbox = "INBOX"

// Special-use attributes (RFC 6154).
const (
	SpecialSent    = `\Sent`
	SpecialDrafts  = `\Drafts`
	SpecialJunk    = `\Junk`
	SpecialTrash   = `\Trash`
	SpecialArchive = `\Archive`
)

// defaultFolders are created the first time a mailbox is used.
var defaultFolders = []struct{ name, use string }{
	{Inbox, ""},
	{"Sent", SpecialSent},
	{"Drafts", SpecialDrafts},
	{"Junk", SpecialJunk},
	{"Trash", SpecialTrash},
	{"Archive", SpecialArchive},
}

// Folder is one folder of a mailbox.
type Folder struct {
	Name        string
	SpecialUse  string // "" or one of the Special* constants
	UIDValidity uint32
	UIDNext     uint32
	Subscribed  bool
}

// FolderStatus is a folder with its counts.
type FolderStatus struct {
	Folder
	Messages int
	Unseen   int
	Deleted  int // messages flagged \Deleted
	Size     int64
}

// NormalizeFolderName checks a folder name and returns its canonical form:
// "INBOX" (as the first level) in any case becomes "INBOX".
func NormalizeFolderName(name string) (string, error) {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) ||
		strings.HasPrefix(name, Separator) || strings.HasSuffix(name, Separator) ||
		strings.Contains(name, Separator+Separator) {
		return "", ErrInvalidName
	}
	for _, r := range name {
		// Wildcards of LIST and control characters would make names
		// ambiguous for clients.
		if unicode.IsControl(r) || r == '*' || r == '%' {
			return "", ErrInvalidName
		}
	}
	first, rest, hasRest := strings.Cut(name, Separator)
	if strings.EqualFold(first, Inbox) {
		if hasRest {
			return Inbox + Separator + rest, nil
		}
		return Inbox, nil
	}
	return name, nil
}

// ensureMailbox creates the mailbox row and its default folders if they do
// not exist yet.
func (s *Store) ensureMailbox(tx *sql.Tx, id string) error {
	res, err := tx.Exec(`INSERT INTO mailboxes (storage_id, created) VALUES (?, ?) ON CONFLICT DO NOTHING`, id, s.now().Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	for _, f := range defaultFolders {
		if _, err := s.createFolder(tx, id, f.name, f.use, true); err != nil {
			return err
		}
	}
	return nil
}

// ensure makes sure the mailbox exists, without taking the write lock when
// it already does.
func (s *Store) ensure(ctx context.Context, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	var n int
	if err := s.rdb.QueryRowContext(ctx, `SELECT count(*) FROM mailboxes WHERE storage_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error { return s.ensureMailbox(tx, id) })
}

// EnsureMailbox creates the mailbox's default folders if it has none yet.
// Every other method does this itself; IMAP may call it at sign-in.
func (s *Store) EnsureMailbox(ctx context.Context, id string) error { return s.ensure(ctx, id) }

func (s *Store) createFolder(tx *sql.Tx, id, name, use string, subscribed bool) (int64, error) {
	v, err := s.nextUIDValidity(tx)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO folders (storage_id, name, special_use, uid_validity, subscribed) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`, id, name, use, v, subscribed)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrFolderExists
	}
	return res.LastInsertId()
}

type folderRow struct {
	id          int64
	uidValidity uint32
	uidNext     uint32
}

func folderByName(tx *sql.Tx, id, name string) (folderRow, error) {
	var f folderRow
	err := tx.QueryRow(`SELECT id, uid_validity, uid_next FROM folders WHERE storage_id = ? AND name = ?`, id, name).
		Scan(&f.id, &f.uidValidity, &f.uidNext)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNoFolder
	}
	return f, err
}

// ListFolders returns all folders of a mailbox, sorted by name.
func (s *Store) ListFolders(ctx context.Context, id string) ([]Folder, error) {
	if err := s.ensure(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.rdb.QueryContext(ctx, `SELECT name, special_use, uid_validity, uid_next, subscribed
		FROM folders WHERE storage_id = ? ORDER BY name = 'INBOX' DESC, name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.Name, &f.SpecialUse, &f.UIDValidity, &f.UIDNext, &f.Subscribed); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FolderStatus returns a folder with its counts.
func (s *Store) FolderStatus(ctx context.Context, id, name string) (FolderStatus, error) {
	var st FolderStatus
	name, err := NormalizeFolderName(name)
	if err != nil {
		return st, err
	}
	if err := s.ensure(ctx, id); err != nil {
		return st, err
	}
	var fid int64
	err = s.rdb.QueryRowContext(ctx, `SELECT id, name, special_use, uid_validity, uid_next, subscribed
		FROM folders WHERE storage_id = ? AND name = ?`, id, name).
		Scan(&fid, &st.Name, &st.SpecialUse, &st.UIDValidity, &st.UIDNext, &st.Subscribed)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNoFolder
	}
	if err != nil {
		return st, err
	}
	err = s.rdb.QueryRowContext(ctx, `SELECT count(*),
		coalesce(sum(instr(' ' || flags || ' ', ' \Seen ') = 0), 0),
		coalesce(sum(instr(' ' || flags || ' ', ' \Deleted ') > 0), 0),
		coalesce(sum(size), 0)
		FROM messages WHERE folder_id = ?`, fid).Scan(&st.Messages, &st.Unseen, &st.Deleted, &st.Size)
	return st, err
}

// CreateFolder creates a folder. Parents are not created: the hierarchy is
// only a naming convention.
func (s *Store) CreateFolder(ctx context.Context, id, name string) error {
	name, err := NormalizeFolderName(name)
	if err != nil {
		return err
	}
	if err := s.ensure(ctx, id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		if _, err := s.createFolder(tx, id, name, "", false); err != nil {
			return err
		}
		ch.add(id, Change{Kind: FoldersChanged, Folder: name})
		return nil
	})
}

// SetSubscribed subscribes to or unsubscribes from a folder.
func (s *Store) SetSubscribed(ctx context.Context, id, name string, subscribed bool) error {
	name, err := NormalizeFolderName(name)
	if err != nil {
		return err
	}
	if err := s.ensure(ctx, id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		res, err := tx.Exec(`UPDATE folders SET subscribed = ? WHERE storage_id = ? AND name = ?`, subscribed, id, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoFolder
		}
		return nil
	})
}

// RenameFolder renames a folder and the folders below it. Renaming INBOX
// moves its messages into a new folder and leaves INBOX empty (RFC 9051).
func (s *Store) RenameFolder(ctx context.Context, id, from, to string) error {
	from, err := NormalizeFolderName(from)
	if err != nil {
		return err
	}
	to, err = NormalizeFolderName(to)
	if err != nil {
		return err
	}
	if from == to || strings.HasPrefix(to, from+Separator) {
		return ErrInvalidName
	}
	if err := s.ensure(ctx, id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		src, err := folderByName(tx, id, from)
		if err != nil {
			return err
		}
		if _, err := folderByName(tx, id, to); err == nil {
			return ErrFolderExists
		} else if !errors.Is(err, ErrNoFolder) {
			return err
		}
		if from == Inbox {
			dst, err := s.createFolder(tx, id, to, "", false)
			if err != nil {
				return err
			}
			uids, err := uidsOf(tx, src.id)
			if err != nil {
				return err
			}
			if _, err := moveRows(tx, src.id, dst, uids); err != nil {
				return err
			}
			ch.add(id, Change{Kind: Expunged, Folder: Inbox, UIDs: uids})
			ch.add(id, Change{Kind: FoldersChanged, Folder: to})
			return nil
		}
		// Folders below "to" may exist even though "to" does not (parents
		// are not required), so every single rename checks for a collision.
		rows, err := tx.Query(`SELECT name FROM folders WHERE storage_id = ? AND (name = ? OR substr(name, 1, ?) = ?)`,
			id, from, len(from)+1, from+Separator)
		if err != nil {
			return err
		}
		var names []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return err
			}
			names = append(names, n)
		}
		rows.Close()
		for _, n := range names {
			nn := to + strings.TrimPrefix(n, from)
			if len(nn) > 255 {
				return ErrInvalidName
			}
			res, err := tx.Exec(`UPDATE folders SET name = ? WHERE storage_id = ? AND name = ?
				AND NOT EXISTS (SELECT 1 FROM folders WHERE storage_id = ? AND name = ?)`, nn, id, n, id, nn)
			if err != nil {
				return err
			}
			if c, _ := res.RowsAffected(); c == 0 {
				return ErrFolderExists
			}
		}
		ch.add(id, Change{Kind: FoldersChanged, Folder: from})
		return nil
	})
}

// DeleteFolder deletes a folder and its messages. Folders below it stay.
func (s *Store) DeleteFolder(ctx context.Context, id, name string) error {
	name, err := NormalizeFolderName(name)
	if err != nil {
		return err
	}
	if name == Inbox {
		return ErrInbox
	}
	if err := s.ensure(ctx, id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		f, err := folderByName(tx, id, name)
		if err != nil {
			return err
		}
		uids, err := uidsOf(tx, f.id)
		if err != nil {
			return err
		}
		if _, err := s.removeMessages(tx, ch, id, f.id, uids); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM folders WHERE id = ?`, f.id); err != nil {
			return err
		}
		ch.add(id, Change{Kind: FoldersChanged, Folder: name})
		return nil
	})
}

func uidsOf(tx *sql.Tx, folderID int64) ([]uint32, error) {
	rows, err := tx.Query(`SELECT uid FROM messages WHERE folder_id = ? ORDER BY uid`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uint32
	for rows.Next() {
		var u uint32
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
