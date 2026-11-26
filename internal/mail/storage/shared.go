package storage

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Shared mailboxes (G3 in wiki/Groupware-Architecture.md): the read state
// of each reader, copies between mailboxes, and copies of sent mail kept
// once.

// maxMessageIDBytes bounds the Message-ID kept for de-duplication.
const maxMessageIDBytes = 998

// messageIDOf returns the Message-ID of the message in file (from its
// header, read up to 1 MiB), or "" if it has none.
func messageIDOf(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	h, err := textproto.NewReader(bufio.NewReader(io.LimitReader(f, 1<<20))).ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return ""
	}
	id := strings.TrimSpace(h.Get("Message-Id"))
	if len(id) > maxMessageIDBytes {
		return ""
	}
	return id
}

// FolderStatusAs is FolderStatus for a reader of a shared mailbox: Unseen
// counts what that reader has not seen. "" is FolderStatus.
func (s *Store) FolderStatusAs(ctx context.Context, id, name, reader string) (FolderStatus, error) {
	st, err := s.FolderStatus(ctx, id, name)
	if err != nil || reader == "" {
		return st, err
	}
	err = s.rdb.QueryRowContext(ctx, `SELECT coalesce(sum(NOT EXISTS (SELECT 1 FROM shared_seen ss WHERE ss.message_id = m.id AND ss.reader = ?)), 0)
		FROM messages m JOIN folders f ON f.id = m.folder_id WHERE f.storage_id = ? AND f.name = ?`, reader, id, st.Name).Scan(&st.Unseen)
	return st, err
}

// CrossOptions describe a copy between two mailboxes.
type CrossOptions struct {
	// Move removes the messages from the source.
	Move bool
	// SrcReader and DstReader are the reader (a storage id) when the source
	// or the destination is a shared mailbox read by someone else: \Seen of
	// the copies is the reader's own read state there.
	SrcReader, DstReader string
}

// CopyAcross copies (or moves) messages from a folder of one mailbox into a
// folder of another, with their flags and internal date. The copies count
// against the destination's quota. Message files are linked, not copied,
// where the file system allows it (they never change).
func (s *Store) CopyAcross(ctx context.Context, srcID, from string, uids []uint32, dstID, to string, opts CrossOptions) (CopyResult, error) {
	var res CopyResult
	if srcID == dstID {
		return res, ErrInvalidName
	}
	from, err := NormalizeFolderName(from)
	if err != nil {
		return res, err
	}
	to, err = NormalizeFolderName(to)
	if err != nil {
		return res, err
	}
	if err := s.ensure(ctx, srcID); err != nil {
		return res, err
	}
	if err := s.ensure(ctx, dstID); err != nil {
		return res, err
	}
	quota := s.quota(dstID)
	err = s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		src, err := folderByName(tx, srcID, from)
		if err != nil {
			return err
		}
		dst, err := folderByName(tx, dstID, to)
		if err != nil {
			return err
		}
		res.UIDValidity = dst.uidValidity
		next := dst.uidNext
		var added int64
		var linked []string
		for _, u := range sortedUnique(uids) {
			m, err := scanMessage(tx.QueryRow(`SELECT `+messageCols+` FROM messages WHERE folder_id = ? AND uid = ?`, opts.SrcReader, src.id, u), srcID, opts.SrcReader)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			name, err := s.link(ch, srcID, m.file, dstID)
			if err != nil {
				return err
			}
			linked = append(linked, name)
			flags, seen := m.Flags, false
			if opts.DstReader != "" {
				seen = slices.Contains(flags, FlagSeen)
				flags = withSeen(flags, false)
			}
			ins, err := tx.Exec(`INSERT INTO messages (folder_id, storage_id, uid, file, size, internal_date, flags) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				dst.id, dstID, next, name, m.Size, m.InternalDate.Unix(), joinFlags(flags))
			if err != nil {
				return err
			}
			if seen {
				row, err := ins.LastInsertId()
				if err != nil {
					return err
				}
				if _, err := tx.Exec(`INSERT INTO shared_seen (message_id, reader) VALUES (?, ?)`, row, opts.DstReader); err != nil {
					return err
				}
			}
			res.Source = append(res.Source, u)
			res.Dest = append(res.Dest, next)
			added += m.Size
			next++
		}
		if len(res.Dest) == 0 {
			return nil
		}
		if len(linked) > 0 {
			if err := syncDir(s.mailboxDir(dstID)); err != nil {
				return err
			}
		}
		if quota > 0 {
			var used int64
			if err := tx.QueryRow(`SELECT used_bytes FROM mailboxes WHERE storage_id = ?`, dstID).Scan(&used); err != nil {
				return err
			}
			if used+added > quota {
				return ErrOverQuota
			}
		}
		if _, err := tx.Exec(`UPDATE folders SET uid_next = ? WHERE id = ?`, next, dst.id); err != nil {
			return err
		}
		if err := addUsage(tx, dstID, added, len(res.Dest)); err != nil {
			return err
		}
		ch.add(dstID, Change{Kind: Appended, Folder: to, UIDs: res.Dest})
		if opts.Move {
			removed, err := s.removeMessages(tx, ch, srcID, src.id, res.Source)
			if err != nil {
				return err
			}
			if len(removed) > 0 {
				ch.add(srcID, Change{Kind: Expunged, Folder: from, UIDs: removed})
			}
		}
		return nil
	})
	if err != nil {
		return CopyResult{}, err
	}
	return res, nil
}

// link gives the message file of mailbox srcID a new name in the directory
// of dstID: a hard link, or a copy where linking is not possible. Callers
// hold the write lock; the caller fsyncs the directory.
func (s *Store) link(ch *changes, srcID, file, dstID string) (string, error) {
	if !fileNameRe.MatchString(file) {
		return "", ErrNoMessage
	}
	dir := s.mailboxDir(dstID)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", err
		}
		if err := syncDir(filepath.Dir(dir)); err != nil {
			return "", err
		}
	}
	src := filepath.Join(s.mailboxDir(srcID), file)
	name := fmt.Sprintf("%d.%s", s.now().UnixNano(), randHex(8))
	dst := filepath.Join(dir, name)
	if err := os.Link(src, dst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoMessage
		}
		// Copy instead (another file system, no hard links).
		in, err := os.Open(src)
		if err != nil {
			return "", err
		}
		defer in.Close()
		tmp, _, err := s.spool(in, 0)
		if err != nil {
			return "", err
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			return "", err
		}
	}
	ch.placed = append(ch.placed, dst)
	return name, nil
}
