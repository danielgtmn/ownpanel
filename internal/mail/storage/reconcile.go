package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/danielgtmn/ablage"

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
	err := s.writeLocked(ctx, func(tx *repos, _ *changes) error {
		// Rows without a file.
		msgs, err := tx.messages.Find(ctx, ablage.Query{})
		if err != nil {
			return err
		}
		var missing []string
		known := map[string]bool{}
		for _, m := range msgs {
			known[m.StorageID+"/"+m.File] = true
			if _, err := os.Stat(filepath.Join(s.mailboxDir(m.StorageID), m.File)); errors.Is(err, os.ErrNotExist) {
				missing = append(missing, m.ID)
			}
		}
		if len(missing) > 0 {
			if _, err := tx.messages.DeleteWhere(ctx, ablage.In("id", missing...)); err != nil {
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
				if err := s.ensureMailbox(ctx, tx, sid); err != nil {
					return err
				}
				inbox, err := folderByName(ctx, tx, sid, Inbox)
				if errors.Is(err, ErrNoFolder) {
					inbox, err = s.createFolder(ctx, tx, sid, Inbox, "", true)
				}
				if err != nil {
					return err
				}
				if err := tx.messages.Create(ctx, &messageRec{FolderID: inbox.ID, StorageID: sid, UID: inbox.UIDNext, File: f.Name(),
					Size: info.Size(), InternalDate: sec(info.ModTime())}); err != nil {
					return err
				}
				if err := tx.folders.Patch(ctx, inbox.ID, ablage.Row{"uid_next": ablage.Incr(1)}); err != nil {
					return err
				}
				rep.IndexedFiles++
			}
		}

		dav, newEpoch, err := s.reconcileDAV(ctx, tx, rep.Restored)
		if err != nil {
			return err
		}
		rep.DAVMissing, rep.DAVIndexed, rep.DAVStray, rep.NewDAVEpoch = dav.missing, dav.indexed, dav.stray, newEpoch

		if err := recountUsage(ctx, tx); err != nil {
			return err
		}

		if rep.MissingFiles > 0 || rep.IndexedFiles > 0 || rep.Restored {
			folders, err := tx.folders.Find(ctx, ablage.Query{})
			if err != nil {
				return err
			}
			for _, f := range folders {
				v, err := s.nextUIDValidity(ctx, tx)
				if err != nil {
					return err
				}
				if err := tx.folders.Patch(ctx, f.ID, ablage.Row{"uid_validity": int64(v)}); err != nil {
					return err
				}
			}
			rep.NewUIDValidity = true
		}
		var qerr error
		rep.QueueMissing, rep.QueueStray, qerr = s.reconcileQueue(ctx, tx)
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

// recountUsage sets the usage counters of every mailbox from the rows, so
// they can never drift. Calendars and contacts count towards the quota
// too.
func recountUsage(ctx context.Context, tx *repos) error {
	boxes, err := tx.mailboxes.Find(ctx, ablage.Query{})
	if err != nil {
		return err
	}
	type usage struct{ bytes, messages int64 }
	use := map[string]*usage{}
	for _, b := range boxes {
		use[b.ID] = &usage{}
	}
	add := func(sid string, bytes, messages int64) {
		if u := use[sid]; u != nil {
			u.bytes += bytes
			u.messages += messages
		}
	}
	msgs, err := tx.messages.Aggregate(ctx, ablage.Aggregation{GroupBy: []string{"storage_id"}, Values: []ablage.Agg{ablage.CountAll("n"), ablage.Sum("size", "bytes")}})
	if err != nil {
		return err
	}
	for _, r := range msgs {
		add(r["storage_id"].(string), r["bytes"].(int64), r["n"].(int64))
	}
	objs, err := tx.davObjects.Aggregate(ctx, ablage.Aggregation{GroupBy: []string{"storage_id"}, Values: []ablage.Agg{ablage.CountAll("n"), ablage.Sum("size", "bytes")}})
	if err != nil {
		return err
	}
	for _, r := range objs {
		add(r["storage_id"].(string), r["bytes"].(int64)+DAVObjectOverhead*r["n"].(int64), 0)
	}
	dels, err := tx.davDeletions.Aggregate(ctx, ablage.Aggregation{GroupBy: []string{"collection_id"}, Values: []ablage.Agg{ablage.CountAll("n")}})
	if err != nil {
		return err
	}
	if len(dels) > 0 {
		colls, err := tx.davCollections.Find(ctx, ablage.Query{})
		if err != nil {
			return err
		}
		owner := map[string]string{}
		for _, c := range colls {
			owner[c.ID] = c.StorageID
		}
		for _, r := range dels {
			add(owner[r["collection_id"].(string)], DAVDeletionOverhead*r["n"].(int64), 0)
		}
	}
	for _, b := range boxes {
		u := use[b.ID]
		if u.bytes == b.UsedBytes && u.messages == b.Messages {
			continue
		}
		if err := tx.mailboxes.Patch(ctx, b.ID, ablage.Row{"used_bytes": u.bytes, "messages": u.messages}); err != nil {
			return err
		}
	}
	return nil
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
	err := s.write(ctx, func(tx *repos, ch *changes) error {
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
		// Folders with their messages and read states, and the mailbox's
		// imports, also go with the mailbox row (cascade).
		if _, err := tx.messages.DeleteWhere(ctx, ablage.Eq("storage_id", id)); err != nil {
			return err
		}
		for _, del := range []func() (int64, error){
			func() (int64, error) { return tx.folders.DeleteWhere(ctx, ablage.Eq("storage_id", id)) },
			func() (int64, error) { return tx.mailboxes.DeleteWhere(ctx, ablage.Eq("id", id)) },
			func() (int64, error) { return tx.bindings.DeleteWhere(ctx, ablage.Eq("id", id)) },
			func() (int64, error) { return tx.blocks.DeleteWhere(ctx, ablage.Eq("id", id)) },
			func() (int64, error) { return tx.sharedSeen.DeleteWhere(ctx, ablage.Eq("reader", id)) },
			func() (int64, error) { return tx.sieveScripts.DeleteWhere(ctx, ablage.Eq("storage_id", id)) },
			func() (int64, error) { return tx.sieveReplies.DeleteWhere(ctx, ablage.Eq("storage_id", id)) },
			func() (int64, error) { return tx.sieveDuplicates.DeleteWhere(ctx, ablage.Eq("storage_id", id)) },
			func() (int64, error) { return tx.sendLog.DeleteWhere(ctx, ablage.In("key", mailboxSendKeys(id)...)) },
		} {
			if _, err := del(); err != nil {
				return err
			}
		}
		if err := s.deleteDAV(ctx, tx, ch, id); err != nil {
			return err
		}
		ch.add(id, Change{Kind: MailboxDeleted})
		return nil
	})
	return err
}
