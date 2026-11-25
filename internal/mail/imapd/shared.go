package imapd

// Shared mailboxes and delegation (G3 in wiki/Groupware-Architecture.md).
//
// A session can reach other mailboxes than its own: those it has a grant
// on with the read right. Their folders appear as
// "Shared/<address>/<folder>" (for example "Shared/info@example.com/INBOX")
// in a plain LIST, subscribed and without special-use attributes, so no
// program takes another mailbox's Sent or Trash for its own. NAMESPACE
// announces "Shared/" as the shared namespace.
//
// Rights are checked on every command against the current configuration
// (Directory.Rights), never cached: read gives LIST, STATUS, SELECT
// (READ-ONLY without write), FETCH, SEARCH and COPY out; write gives
// everything that changes the mailbox. Forbidden actions answer
// NO [NOPERM]. There is no ACL extension in v1 (go-imap's server
// dispatches a fixed set of commands).
//
// In a shared mailbox (kind shared) \Seen is each reader's own: it is kept
// per reader in the storage (SelectAs and friends), other flags are
// everyone's. A reader with read only may still set and clear its own
// \Seen. In a personal mailbox delegated to someone the read state is the
// mailbox's, as for its owner.

import (
	"strings"
	"sync"

	"github.com/emersion/go-imap/v2"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// sharedRoot is the first level of the folders of other mailboxes.
const sharedRoot = "Shared"

// target is a folder a client named, resolved to a mailbox and a folder in
// it.
type target struct {
	sid    string // storage id of the mailbox
	folder string // the folder's name in that mailbox (normalized)
	name   string // the folder's name as the client sees it
	shared bool   // another mailbox than the session's own
	addr   string // that mailbox's address (shared only)
	rights config.Rights
	// reader is the session's storage id when the mailbox keeps a read
	// state per reader (a shared mailbox), else "".
	reader string
	// keepSent: the mailbox keeps copies of sent mail (KeepSentCopy).
	keepSent bool
}

var (
	errNoPerm = &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNoPerm,
		Text: "You may not change this shared mailbox"}
	errNoSelect = &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeCannot,
		Text: "This name only groups other folders"}
	errNoSuchFolder    = &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNonExistent, Text: "No such folder"}
	errAcrossMailboxes = &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeCannot,
		Text: "Folders cannot be renamed into another mailbox"}
	errReserved = &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeCannot,
		Text: "Names under Shared/ with an address are kept for shared mailboxes"}
)

// sharedName splits "Shared/<address>[/<rest>]": ok when name is under
// sharedRoot and its second level is an address.
func sharedName(name string) (addr, rest string, hasRest, ok bool) {
	first, after, has := strings.Cut(name, "/")
	if !has || !strings.EqualFold(first, sharedRoot) {
		return "", "", false, false
	}
	addr, rest, hasRest = strings.Cut(after, "/")
	if !strings.Contains(addr, "@") {
		return "", "", false, false
	}
	return strings.ToLower(addr), rest, hasRest, true
}

// resolve maps a folder name to the mailbox and folder it means. Names of
// mailboxes the session may not read answer like folders that do not
// exist; "Shared/<address>" itself is not selectable (errNoSelect, with the
// target filled in).
func (s *session) resolve(dir *directory.Directory, name string) (target, error) {
	own := s.id()
	if addr, rest, hasRest, ok := sharedName(name); ok {
		m, found := dir.Mailbox(addr)
		if !found || m.StorageID == own {
			return target{}, errNoSuchFolder
		}
		r := dir.Rights(m.StorageID, own)
		if !r.Has(config.Read) {
			return target{}, errNoSuchFolder
		}
		t := target{sid: m.StorageID, shared: true, addr: addr, rights: r, keepSent: m.KeepSentCopy,
			name: sharedRoot + "/" + addr}
		if m.Shared() {
			t.reader = own
		}
		if !hasRest {
			return t, errNoSelect
		}
		folder, err := storage.NormalizeFolderName(rest)
		if err != nil {
			return target{}, s.fail(err)
		}
		t.folder, t.name = folder, t.name+"/"+folder
		return t, nil
	}
	folder, err := storage.NormalizeFolderName(name)
	if err != nil {
		return target{}, s.fail(err)
	}
	return target{sid: own, folder: folder, name: folder, rights: config.AllRights}, nil
}

// need refuses with NO [NOPERM] unless t has right r.
func (t target) need(r config.Rights) error {
	if !t.rights.Has(r) {
		return errNoPerm
	}
	return nil
}

// watches keeps one storage watch per mailbox the session reads: its own
// and every one it has the read right on.
type watches struct {
	mu     sync.Mutex
	cancel map[string]func()
	closed bool
}

// sync makes the watched mailboxes those of dir for the session.
func (s *session) syncWatches(dir *directory.Directory) {
	own := s.id()
	want := map[string]bool{own: true}
	for _, a := range dir.Access(own) {
		if a.Rights.Has(config.Read) {
			want[a.Mailbox.StorageID] = true
		}
	}
	w := &s.watch
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if w.cancel == nil {
		w.cancel = map[string]func(){}
	}
	for id, cancel := range w.cancel {
		if !want[id] {
			cancel()
			delete(w.cancel, id)
		}
	}
	for id := range want {
		if w.cancel[id] == nil {
			w.cancel[id] = s.srv.opts.Store.Watch(id, s.onChange(id))
		}
	}
}

func (s *session) stopWatches() {
	w := &s.watch
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	for _, cancel := range w.cancel {
		cancel()
	}
	w.cancel = nil
}

// reconfigure follows a new configuration: the watches, and a selected
// folder of another mailbox whose read right is gone ends the session
// (BYE): the client reconnects and no longer sees it.
func (s *session) reconfigure(dir *directory.Directory) {
	if _, ok := s.mailbox(); !ok {
		return
	}
	s.syncWatches(dir)
	s.mu.Lock()
	sel := s.sel
	s.mu.Unlock()
	if sel != nil && sel.t.shared && !dir.Rights(sel.t.sid, s.id()).Has(config.Read) {
		s.revoked()
	}
}

// revoked ends the session because the access to the selected shared
// folder was removed.
func (s *session) revoked() {
	s.mu.Lock()
	ended := s.ended
	s.ended = true
	s.mu.Unlock()
	if !ended {
		s.conn.Bye("Access to this shared mailbox was removed")
	}
}

var errRevoked = &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNoPerm,
	Text: "Access to this shared mailbox was removed"}

// checkSelected re-reads the rights on the selected folder: gone ends the
// session, write taken away makes the selection read-only.
func (s *session) checkSelected(dir *directory.Directory) error {
	sel := s.sel
	if sel == nil || !sel.t.shared {
		return nil
	}
	r := dir.Rights(sel.t.sid, s.id())
	if !r.Has(config.Read) {
		s.revoked()
		return errRevoked
	}
	sel.t.rights = r
	if !r.Has(config.Write) {
		sel.readOnly = true
	}
	return nil
}

// sharedFolders lists the folders of the mailboxes the session may read,
// with their names as the client sees them, subscribed and without
// special-use attributes; plus "Shared" and "Shared/<address>" as parents.
func (s *session) sharedFolders(dir *directory.Directory) (map[string]listEntry, error) {
	out := map[string]listEntry{}
	for _, a := range dir.Access(s.id()) {
		if !a.Rights.Has(config.Read) {
			continue
		}
		folders, err := s.srv.opts.Store.ListFolders(s.ctx, a.Mailbox.StorageID)
		if err != nil {
			return nil, err
		}
		base := target{sid: a.Mailbox.StorageID, shared: true, addr: a.Mailbox.Address, rights: a.Rights,
			keepSent: a.Mailbox.KeepSentCopy}
		if a.Mailbox.Shared() {
			base.reader = s.id()
		}
		root := sharedRoot + "/" + a.Mailbox.Address
		out[sharedRoot] = listEntry{folder: storage.Folder{Name: sharedRoot, Subscribed: true}, noSelect: true}
		out[root] = listEntry{folder: storage.Folder{Name: root, Subscribed: true}, noSelect: true}
		for _, f := range folders {
			t := base
			t.folder, t.name = f.Name, root+"/"+f.Name
			f.Name, f.SpecialUse, f.Subscribed = t.name, "", true
			out[t.name] = listEntry{folder: f, t: t}
		}
	}
	return out, nil
}

// listEntry is a folder LIST shows: one of the session's own, one of
// another mailbox (t.shared), or a parent of those (noSelect).
type listEntry struct {
	folder   storage.Folder
	t        target
	noSelect bool
}

// isSent tells whether t is the Sent folder of its mailbox.
func (s *session) isSent(t target) bool {
	st, err := s.srv.opts.Store.FolderStatus(s.ctx, t.sid, t.folder)
	return err == nil && st.SpecialUse == storage.SpecialSent
}
