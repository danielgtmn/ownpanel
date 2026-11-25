package storage

import (
	"context"
	"os"
)

// ChangeKind says what changed.
type ChangeKind int

const (
	// Appended: messages with UIDs were added to Folder.
	Appended ChangeKind = iota + 1
	// FlagsChanged: the flags of UIDs in Folder changed.
	FlagsChanged
	// Expunged: UIDs were removed from Folder.
	Expunged
	// FoldersChanged: folders were created, renamed or deleted.
	FoldersChanged
	// MailboxDeleted: all data of the mailbox is gone.
	MailboxDeleted
)

// Change is one committed change of a mailbox, for IMAP IDLE and for
// sessions that have a folder selected.
type Change struct {
	Kind   ChangeKind
	Folder string
	UIDs   []uint32
	// Origin is the value passed to WithOrigin in the context of the call
	// that made the change (nil if none), so that a session can tell its
	// own changes from those of others.
	Origin any
}

type originKey struct{}

// WithOrigin marks the changes made with ctx as coming from origin (see
// Change.Origin).
func WithOrigin(ctx context.Context, origin any) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}

// Watch calls fn for every committed change of the mailbox until the
// returned cancel function is called. fn is called in commit order while
// the store's write lock is held: it must return quickly and must not call
// the store (hand the change to a channel or set a flag).
func (s *Store) Watch(storageID string, fn func(Change)) (cancel func()) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	s.watchSeq++
	n := s.watchSeq
	if s.watchers[storageID] == nil {
		s.watchers[storageID] = map[int]func(Change){}
	}
	s.watchers[storageID][n] = fn
	return func() {
		s.watchMu.Lock()
		defer s.watchMu.Unlock()
		delete(s.watchers[storageID], n)
		if len(s.watchers[storageID]) == 0 {
			delete(s.watchers, storageID)
		}
	}
}

func (s *Store) notify(storageID string, c Change) {
	s.watchMu.Lock()
	fns := make([]func(Change), 0, len(s.watchers[storageID]))
	for _, fn := range s.watchers[storageID] {
		fns = append(fns, fn)
	}
	s.watchMu.Unlock()
	for _, fn := range fns {
		fn(c)
	}
}

// changes collects what a write transaction did besides the database: the
// notifications to send after commit, and file operations to finish after
// commit or to undo on rollback.
type changes struct {
	list []struct {
		storageID string
		Change
	}
	// trashed files were moved out of the messages directory; removed after
	// commit, moved back on rollback.
	trashed []rename
	// placed files were moved into the messages directory; removed on
	// rollback.
	placed []string
}

type rename struct{ from, to string }

func (ch *changes) add(storageID string, c Change) {
	ch.list = append(ch.list, struct {
		storageID string
		Change
	}{storageID, c})
}

func (ch *changes) rollback() {
	for _, r := range ch.trashed {
		os.Rename(r.to, r.from)
	}
	for _, p := range ch.placed {
		os.Remove(p)
	}
}

func (ch *changes) commit() {
	for _, r := range ch.trashed {
		os.RemoveAll(r.to)
	}
}
