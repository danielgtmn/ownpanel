package storage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/danielgtmn/ablage"
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
	err = s.write(ctx, func(tx *repos, ch *changes) error {
		src, err := folderByName(ctx, tx, srcID, from)
		if err != nil {
			return err
		}
		dst, err := folderByName(ctx, tx, dstID, to)
		if err != nil {
			return err
		}
		res.UIDValidity = uint32(dst.UIDValidity)
		msgs, err := messagesByUID(ctx, tx, srcID, src.ID, uids, opts.SrcReader)
		if err != nil {
			return err
		}
		next := dst.UIDNext
		var added int64
		var linked []string
		for _, m := range msgs {
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
			c := messageRec{FolderID: dst.ID, StorageID: dstID, UID: next, File: name, Size: m.Size, InternalDate: sec(m.InternalDate)}
			setFlags(&c, flags)
			if err := tx.messages.Create(ctx, &c); err != nil {
				return err
			}
			if seen {
				if err := tx.sharedSeen.Create(ctx, &sharedSeenRec{MessageID: c.ID, Reader: opts.DstReader}); err != nil {
					return err
				}
			}
			res.Source = append(res.Source, m.UID)
			res.Dest = append(res.Dest, uint32(next))
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
			used, err := usedBytes(ctx, tx, dstID)
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
		if err := addUsage(ctx, tx, dstID, added, len(res.Dest)); err != nil {
			return err
		}
		ch.add(dstID, Change{Kind: Appended, Folder: to, UIDs: res.Dest})
		if opts.Move {
			removed, err := s.removeMessages(ctx, tx, ch, srcID, src.ID, res.Source)
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
