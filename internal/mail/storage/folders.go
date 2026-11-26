package storage

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtmn/ablage"
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
func (s *Store) ensureMailbox(ctx context.Context, tx *repos, id string) error {
	err := tx.mailboxes.Create(ctx, &mailboxRow{ID: id, Created: sec(s.now())})
	if errors.Is(err, ablage.ErrDuplicate) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, f := range defaultFolders {
		if _, err := s.createFolder(ctx, tx, id, f.name, f.use, true); err != nil {
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
	ok, err := s.r.mailboxes.Exists(ctx, id)
	if err != nil || ok {
		return err
	}
	return s.write(ctx, func(tx *repos, _ *changes) error { return s.ensureMailbox(ctx, tx, id) })
}

// EnsureMailbox creates the mailbox's default folders if it has none yet.
// Every other method does this itself; IMAP may call it at sign-in.
func (s *Store) EnsureMailbox(ctx context.Context, id string) error { return s.ensure(ctx, id) }

func (s *Store) createFolder(ctx context.Context, tx *repos, id, name, use string, subscribed bool) (folderRec, error) {
	f := folderRec{StorageID: id, Name: name, SpecialUse: use, UIDNext: 1, Subscribed: subscribed}
	if ok, err := tx.folders.Count(ctx, ablage.And(ablage.Eq("storage_id", id), ablage.Eq("name", name))); err != nil {
		return f, err
	} else if ok > 0 {
		return f, ErrFolderExists
	}
	v, err := s.nextUIDValidity(ctx, tx)
	if err != nil {
		return f, err
	}
	f.UIDValidity = int64(v)
	err = tx.folders.Create(ctx, &f)
	if errors.Is(err, ablage.ErrDuplicate) {
		return f, ErrFolderExists
	}
	return f, err
}

// folderByName returns a folder of a mailbox; ErrNoFolder if there is none.
func folderByName(ctx context.Context, tx *repos, id, name string) (folderRec, error) {
	f, err := tx.folders.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("storage_id", id), ablage.Eq("name", name))})
	if errors.Is(err, ablage.ErrNotFound) {
		return f, ErrNoFolder
	}
	return f, err
}

func (f folderRec) folder() Folder {
	return Folder{Name: f.Name, SpecialUse: f.SpecialUse, UIDValidity: uint32(f.UIDValidity), UIDNext: uint32(f.UIDNext), Subscribed: f.Subscribed}
}

// ListFolders returns all folders of a mailbox, sorted by name, INBOX
// first.
func (s *Store) ListFolders(ctx context.Context, id string) ([]Folder, error) {
	if err := s.ensure(ctx, id); err != nil {
		return nil, err
	}
	list, err := s.r.folders.Find(ctx, ablage.Query{Where: ablage.Eq("storage_id", id), OrderBy: []ablage.Order{ablage.Asc("name")}})
	if err != nil {
		return nil, err
	}
	var out []Folder
	for _, f := range list {
		if f.Name == Inbox {
			out = append([]Folder{f.folder()}, out...)
		} else {
			out = append(out, f.folder())
		}
	}
	return out, nil
}

// FolderStatus returns a folder with its counts.
func (s *Store) FolderStatus(ctx context.Context, id, name string) (FolderStatus, error) {
	return s.FolderStatusAs(ctx, id, name, "")
}

// FolderStatusAs is FolderStatus for a reader of a shared mailbox: Unseen
// counts what that reader has not seen. "" is FolderStatus.
func (s *Store) FolderStatusAs(ctx context.Context, id, name, reader string) (FolderStatus, error) {
	var st FolderStatus
	name, err := NormalizeFolderName(name)
	if err != nil {
		return st, err
	}
	if err := s.ensure(ctx, id); err != nil {
		return st, err
	}
	err = s.read(ctx, func(tx *repos) error {
		f, err := folderByName(ctx, tx, id, name)
		if err != nil {
			return err
		}
		st.Folder = f.folder()
		// One row per combination of \Seen and \Deleted.
		rows, err := tx.messages.Aggregate(ctx, ablage.Aggregation{
			Where:   ablage.Eq("folder_id", f.ID),
			GroupBy: []string{"seen", "deleted"},
			Values:  []ablage.Agg{ablage.CountAll("n"), ablage.Sum("size", "bytes")},
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			n := int(r["n"].(int64))
			st.Messages += n
			st.Size += r["bytes"].(int64)
			if r["seen"] == false {
				st.Unseen += n
			}
			if r["deleted"] == true {
				st.Deleted += n
			}
		}
		if reader == "" {
			return nil
		}
		// The reader's own read state: everything but what they have seen.
		ids, err := messageIDs(ctx, tx, f.ID)
		if err != nil {
			return err
		}
		seen, err := tx.sharedSeen.Count(ctx, ablage.And(ablage.Eq("reader", reader), ablage.In("message_id", ids...)))
		if err != nil {
			return err
		}
		st.Unseen = st.Messages - int(seen)
		return nil
	})
	return st, err
}

// messageIDs returns the row ids of a folder's messages.
func messageIDs(ctx context.Context, tx *repos, folderID string) ([]string, error) {
	list, err := tx.messages.Find(ctx, ablage.Query{Where: ablage.Eq("folder_id", folderID)})
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(list))
	for i, m := range list {
		ids[i] = m.ID
	}
	return ids, nil
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
	return s.write(ctx, func(tx *repos, ch *changes) error {
		if _, err := s.createFolder(ctx, tx, id, name, "", false); err != nil {
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
	return s.write(ctx, func(tx *repos, _ *changes) error {
		n, err := tx.folders.UpdateWhere(ctx, ablage.And(ablage.Eq("storage_id", id), ablage.Eq("name", name)), ablage.Row{"subscribed": subscribed})
		if err != nil {
			return err
		}
		if n == 0 {
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
	return s.write(ctx, func(tx *repos, ch *changes) error {
		src, err := folderByName(ctx, tx, id, from)
		if err != nil {
			return err
		}
		if _, err := folderByName(ctx, tx, id, to); err == nil {
			return ErrFolderExists
		} else if !errors.Is(err, ErrNoFolder) {
			return err
		}
		if from == Inbox {
			dst, err := s.createFolder(ctx, tx, id, to, "", false)
			if err != nil {
				return err
			}
			uids, err := uidsOf(ctx, tx, src.ID)
			if err != nil {
				return err
			}
			if _, err := moveRows(ctx, tx, src.ID, dst.ID, uids); err != nil {
				return err
			}
			ch.add(id, Change{Kind: Expunged, Folder: Inbox, UIDs: uids})
			ch.add(id, Change{Kind: FoldersChanged, Folder: to})
			return nil
		}
		// Folders below "to" may exist even though "to" does not (parents
		// are not required), so every single rename checks for a collision.
		list, err := tx.folders.Find(ctx, ablage.Query{Where: ablage.And(ablage.Eq("storage_id", id),
			ablage.Or(ablage.Eq("name", from), ablage.Prefix("name", from+Separator)))})
		if err != nil {
			return err
		}
		for _, f := range list {
			nn := to + strings.TrimPrefix(f.Name, from)
			if len(nn) > 255 {
				return ErrInvalidName
			}
			if n, err := tx.folders.Count(ctx, ablage.And(ablage.Eq("storage_id", id), ablage.Eq("name", nn))); err != nil {
				return err
			} else if n > 0 {
				return ErrFolderExists
			}
			if err := tx.folders.Patch(ctx, f.ID, ablage.Row{"name": nn}); err != nil {
				return err
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
	return s.write(ctx, func(tx *repos, ch *changes) error {
		f, err := folderByName(ctx, tx, id, name)
		if err != nil {
			return err
		}
		uids, err := uidsOf(ctx, tx, f.ID)
		if err != nil {
			return err
		}
		if _, err := s.removeMessages(ctx, tx, ch, id, f.ID, uids); err != nil {
			return err
		}
		if err := tx.folders.Delete(ctx, f.ID); err != nil {
			return err
		}
		ch.add(id, Change{Kind: FoldersChanged, Folder: name})
		return nil
	})
}

// uidsOf returns the UIDs of a folder, ascending.
func uidsOf(ctx context.Context, tx *repos, folderID string) ([]uint32, error) {
	list, err := tx.messages.Find(ctx, ablage.Query{Where: ablage.Eq("folder_id", folderID), OrderBy: []ablage.Order{ablage.Asc("uid")}})
	if err != nil {
		return nil, err
	}
	out := make([]uint32, len(list))
	for i, m := range list {
		out[i] = uint32(m.UID)
	}
	return out, nil
}
