package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// ReconcileReport tells what the start-up check found.
type ReconcileReport struct {
	// MissingFiles are index rows whose file was gone (dropped).
	MissingFiles int
	// IndexedFiles are files without a row, added to INBOX.
	IndexedFiles int
	// NewUIDValidity is set when every folder got a new UIDVALIDITY.
	NewUIDValidity bool
	// Restored is set when the restore marker was found.
	Restored bool
	// QueueMissing are queued messages whose file was gone (dropped);
	// QueueStray are queue files without a row (removed: never accepted).
	QueueMissing int
	QueueStray   int
	// DAVMissing are calendar and contact rows whose file was gone
	// (dropped); DAVIndexed files without a row indexed again; DAVStray
	// files without a row that could not be (removed). NewDAVEpoch is set
	// when every sync token, ETag and ctag changed.
	DAVMissing  int
	DAVIndexed  int
	DAVStray    int
	NewDAVEpoch bool
}

// Reconcile brings index and files in line; call it once at start, before
// serving. Rows whose file is missing are dropped, files without a row are
// indexed into INBOX. If anything differed, or the marker file "restored"
// exists in the data directory (left there after restoring a backup), every
// folder gets a new UIDVALIDITY, so mail clients drop their caches; the
// marker is removed afterwards.
func (s *Store) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var rep ReconcileReport
	// Leftovers of interrupted appends and deletions.
	for _, d := range []string{tmpDir, trashDir} {
		if err := clearDir(filepath.Join(s.dir, d)); err != nil {
			return rep, err
		}
	}
	marker := filepath.Join(s.dir, RestoredFile)
	if _, err := os.Stat(marker); err == nil {
		rep.Restored = true
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	err := s.writeLocked(ctx, func(tx *sql.Tx, _ *changes) error {
		// Rows without a file.
		type row struct {
			id   int64
			sid  string
			file string
			size int64
		}
		rows, err := tx.Query(`SELECT id, storage_id, file, size FROM messages`)
		if err != nil {
			return err
		}
		var missing []row
		known := map[string]bool{}
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.sid, &r.file, &r.size); err != nil {
				rows.Close()
				return err
			}
			known[r.sid+"/"+r.file] = true
			if _, err := os.Stat(filepath.Join(s.mailboxDir(r.sid), r.file)); errors.Is(err, os.ErrNotExist) {
				missing = append(missing, r)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, r := range missing {
			if _, err := tx.Exec(`DELETE FROM messages WHERE id = ?`, r.id); err != nil {
				return err
			}
		}
		rep.MissingFiles = len(missing)

		// Files without a row.
		dirs, err := os.ReadDir(filepath.Join(s.dir, messagesDir))
		if err != nil {
			return err
		}
		for _, d := range dirs {
			if !d.IsDir() || !config.ValidStorageID(d.Name()) {
				continue
			}
			sid := d.Name()
			files, err := os.ReadDir(s.mailboxDir(sid))
			if err != nil {
				return err
			}
			for _, f := range files {
				if !f.Type().IsRegular() || !fileNameRe.MatchString(f.Name()) || known[sid+"/"+f.Name()] {
					continue
				}
				info, err := f.Info()
				if err != nil {
					return err
				}
				if err := s.ensureMailbox(tx, sid); err != nil {
					return err
				}
				inbox, err := folderByName(tx, sid, Inbox)
				if errors.Is(err, ErrNoFolder) {
					id, cerr := s.createFolder(tx, sid, Inbox, "", true)
					if cerr != nil {
						return cerr
					}
					inbox = folderRow{id: id, uidNext: 1}
				} else if err != nil {
					return err
				}
				if _, err := tx.Exec(`INSERT INTO messages (folder_id, storage_id, uid, file, size, internal_date, flags) VALUES (?, ?, ?, ?, ?, ?, '')`,
					inbox.id, sid, inbox.uidNext, f.Name(), info.Size(), info.ModTime().Unix()); err != nil {
					return err
				}
				if _, err := tx.Exec(`UPDATE folders SET uid_next = uid_next + 1 WHERE id = ?`, inbox.id); err != nil {
					return err
				}
				rep.IndexedFiles++
			}
		}

		dav, newEpoch, err := s.reconcileDAV(tx, rep.Restored)
		if err != nil {
			return err
		}
		rep.DAVMissing, rep.DAVIndexed, rep.DAVStray, rep.NewDAVEpoch = dav.missing, dav.indexed, dav.stray, newEpoch

		// Usage counters from the rows, so they can never drift. Calendars
		// and contacts count towards the quota too.
		if _, err := tx.Exec(`UPDATE mailboxes SET
			used_bytes = (SELECT coalesce(sum(size), 0) FROM messages WHERE messages.storage_id = mailboxes.storage_id) +
				(SELECT coalesce(sum(size), 0) + `+strconv.Itoa(DAVObjectOverhead)+` * count(*) FROM dav_objects WHERE dav_objects.storage_id = mailboxes.storage_id) +
				(SELECT `+strconv.Itoa(DAVDeletionOverhead)+` * count(*) FROM dav_deletions d JOIN dav_collections c ON c.id = d.collection_id WHERE c.storage_id = mailboxes.storage_id),
			messages = (SELECT count(*) FROM messages WHERE messages.storage_id = mailboxes.storage_id)`); err != nil {
			return err
		}

		if rep.MissingFiles > 0 || rep.IndexedFiles > 0 || rep.Restored {
			ids, err := folderIDs(tx)
			if err != nil {
				return err
			}
			for _, id := range ids {
				v, err := s.nextUIDValidity(tx)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(`UPDATE folders SET uid_validity = ? WHERE id = ?`, v, id); err != nil {
					return err
				}
			}
			rep.NewUIDValidity = true
		}
		var qerr error
		rep.QueueMissing, rep.QueueStray, qerr = s.reconcileQueue(tx)
		return qerr
	})
	if err != nil {
		return rep, err
	}
	if rep.Restored {
		if err := os.Remove(marker); err != nil {
			return rep, err
		}
		if err := syncDir(s.dir); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func folderIDs(tx *sql.Tx) ([]int64, error) {
	rows, err := tx.Query(`SELECT id FROM folders ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// DeleteMailbox removes all data of a mailbox: messages, folders, binding
// and block state. It is not an error if there was none.
func (s *Store) DeleteMailbox(ctx context.Context, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	dir := s.mailboxDir(id)
	gone := filepath.Join(s.dir, trashDir, id+"-"+randHex(8))
	err := s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		// Move the directory away first: if the commit fails it is moved
		// back; if the process dies, the trash is emptied at start and the
		// rows without files are dropped.
		if err := os.Rename(dir, gone); err == nil {
			ch.trashed = append(ch.trashed, rename{dir, gone})
			if err := syncDir(filepath.Dir(dir)); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, q := range []string{
			`DELETE FROM messages WHERE storage_id = ?`,
			`DELETE FROM folders WHERE storage_id = ?`,
			`DELETE FROM mailboxes WHERE storage_id = ?`,
			`DELETE FROM bindings WHERE storage_id = ?`,
			`DELETE FROM blocks WHERE storage_id = ?`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM send_log WHERE key IN (?, ?, ?, ?)`, mailboxSendKeys(id)...); err != nil {
			return err
		}
		if err := s.deleteDAV(tx, ch, id); err != nil {
			return err
		}
		ch.add(id, Change{Kind: MailboxDeleted})
		return nil
	})
	return err
}
