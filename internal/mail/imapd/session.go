package imapd

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// session is one IMAP connection. The library calls its methods from one
// goroutine at a time (IDLE runs on another goroutine while the command
// goroutine waits), so only the fields shared with the storage callback
// and the server need the lock.
type session struct {
	srv  *Server
	conn *imapserver.Conn
	ip   netip.Addr
	// ctx marks storage changes as this session's (storage.WithOrigin).
	ctx  context.Context
	wake chan struct{}

	// watch holds a storage watch per mailbox the session reads (its own
	// and those shared with it).
	watch watches

	// mu guards the fields below and selected.queue. It is never held
	// while calling the store: the storage callback takes it under the
	// store's write lock.
	mu     sync.Mutex
	box    signin.Grant
	authed bool
	gone   bool // the mailbox's data was deleted
	sel    *selected
	ended  bool
}

var _ imapserver.SessionIMAP4rev2 = (*session)(nil)
var _ imapserver.SessionAppendLimit = (*session)(nil)

func (s *session) mailbox() (signin.Grant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.box, s.authed
}

func (s *session) id() string { return s.box.StorageID }

// end closes the connection of a session whose sign-in is no longer
// valid.
func (s *session) end() {
	s.mu.Lock()
	ended := s.ended
	s.ended = true
	s.mu.Unlock()
	if !ended {
		s.conn.Bye("Sign-in no longer valid")
	}
}

var errEnded = &imap.Error{Type: imap.StatusResponseTypeNo, Text: "Sign-in no longer valid"}

// check ends the session if its mailbox was removed or its password
// changed since sign-in, and checks the rights on a selected folder of
// another mailbox.
func (s *session) check() error {
	s.mu.Lock()
	ok := s.authed && !s.gone && !s.ended
	box := s.box
	s.mu.Unlock()
	dir := s.srv.opts.Directory()
	if ok && signin.Valid(dir, box) {
		return s.checkSelected(dir)
	}
	s.end()
	return errEnded
}

func (s *session) Close() error {
	s.srv.remove(s)
	s.stopWatches()
	s.mu.Lock()
	s.sel = nil
	s.mu.Unlock()
	return nil
}

func (s *session) Login(username, password string) error {
	m, err := s.srv.opts.SignIn.Check(s.srv.opts.Directory(), s.ip, username, password)
	if errors.Is(err, signin.ErrThrottled) {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeUnavailable, Text: "Too many failed sign-ins, try again later"}
	}
	if err != nil {
		return imapserver.ErrAuthFailed
	}
	if err := s.srv.opts.Store.EnsureMailbox(s.ctx, m.StorageID); err != nil {
		return s.fail(err)
	}
	s.mu.Lock()
	s.box, s.authed = m, true
	s.mu.Unlock()
	s.syncWatches(s.srv.opts.Directory())
	return nil
}

// onChange returns the storage callback for mailbox sid: it runs under the
// store's write lock and only queues.
func (s *session) onChange(sid string) func(storage.Change) {
	return func(c storage.Change) {
		s.mu.Lock()
		switch c.Kind {
		case storage.MailboxDeleted:
			if sid == s.box.StorageID {
				s.gone = true
			}
		case storage.Appended, storage.Expunged, storage.FlagsChanged:
			// Another reader's own read state is no change for this one.
			if c.Kind == storage.FlagsChanged && c.Reader != "" && c.Reader != s.box.StorageID {
				break
			}
			// A session reports its own flag changes itself (STORE, FETCH
			// setting \Seen); appends and expunges always go through the
			// queue so that sequence numbers are only ever changed here.
			if s.sel != nil && s.sel.t.sid == sid && s.sel.t.folder == c.Folder && !(c.Kind == storage.FlagsChanged && c.Origin == s) {
				s.sel.queue = append(s.sel.queue, c)
			}
		}
		s.mu.Unlock()
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// fail maps storage errors to IMAP answers.
func (s *session) fail(err error) error {
	var ie *imap.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ie):
		return err
	case errors.Is(err, storage.ErrNoFolder):
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNonExistent, Text: "No such folder"}
	case errors.Is(err, storage.ErrFolderExists):
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAlreadyExists, Text: "Folder already exists"}
	case errors.Is(err, storage.ErrInvalidName):
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeCannot, Text: "Invalid folder name"}
	case errors.Is(err, storage.ErrInbox):
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeCannot, Text: "INBOX cannot be deleted"}
	case errors.Is(err, storage.ErrInvalidFlag):
		return &imap.Error{Type: imap.StatusResponseTypeBad, Code: imap.ResponseCodeClientBug, Text: "Invalid flag"}
	case errors.Is(err, storage.ErrOverQuota):
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeOverQuota, Text: "Mailbox is full"}
	case errors.Is(err, storage.ErrTooLarge):
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeTooBig, Text: "Message is too large"}
	}
	s.srv.log.Error("imap: storage", "err", err)
	return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeServerBug, Text: "Internal error, try again later"}
}

func errTryCreate(err error) error {
	if errors.Is(err, storage.ErrNoFolder) || err == errNoSuchFolder {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeTryCreate, Text: "No such folder"}
	}
	return err
}

var errReadOnly = &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeCannot, Text: "Folder is open read-only"}

// --- Authenticated state ---

func (s *session) AppendLimit() uint32 {
	n := s.srv.opts.Directory().Limits().MaxMessageBytes
	return uint32(min(n, 1<<32-1))
}

func (s *session) Namespace() (*imap.NamespaceData, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	return &imap.NamespaceData{
		Personal: []imap.NamespaceDescriptor{{Delim: delim}},
		Shared:   []imap.NamespaceDescriptor{{Prefix: sharedRoot + string(delim), Delim: delim}},
	}, nil
}

const delim = '/'

func (s *session) Create(name string, options *imap.CreateOptions) error {
	if err := s.check(); err != nil {
		return err
	}
	if options != nil && len(options.SpecialUse) > 0 {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeCannot, Text: "Special-use folders cannot be created"}
	}
	// A trailing delimiter only says that children are planned.
	name = strings.TrimRight(name, string(delim))
	t, err := s.resolve(s.srv.opts.Directory(), name)
	if err != nil {
		if _, _, _, ok := sharedName(name); ok {
			return errReserved
		}
		return err
	}
	if err := t.need(config.Write); err != nil {
		return err
	}
	return s.fail(s.srv.opts.Store.CreateFolder(s.ctx, t.sid, t.folder))
}

func (s *session) Delete(name string) error {
	if err := s.check(); err != nil {
		return err
	}
	t, err := s.resolve(s.srv.opts.Directory(), name)
	if err != nil {
		return err
	}
	if err := t.need(config.Write); err != nil {
		return err
	}
	return s.fail(s.srv.opts.Store.DeleteFolder(s.ctx, t.sid, t.folder))
}

func (s *session) Rename(from, to string, _ *imap.RenameOptions) error {
	if err := s.check(); err != nil {
		return err
	}
	to = strings.TrimRight(to, string(delim))
	dir := s.srv.opts.Directory()
	src, err := s.resolve(dir, from)
	if err != nil {
		return err
	}
	dst, err := s.resolve(dir, to)
	if err != nil {
		if _, _, _, ok := sharedName(to); ok && !src.shared {
			return errReserved
		}
		return err
	}
	if src.sid != dst.sid {
		return errAcrossMailboxes
	}
	if err := src.need(config.Write); err != nil {
		return err
	}
	return s.fail(s.srv.opts.Store.RenameFolder(s.ctx, src.sid, src.folder, dst.folder))
}

func (s *session) Subscribe(name string) error {
	return s.subscribe(name, true)
}

func (s *session) Unsubscribe(name string) error {
	return s.subscribe(name, false)
}

// subscribe changes the subscription of an own folder. Folders of other
// mailboxes are always subscribed (iOS and Outlook show only those):
// changing that is accepted and has no effect.
func (s *session) subscribe(name string, on bool) error {
	if err := s.check(); err != nil {
		return err
	}
	t, err := s.resolve(s.srv.opts.Directory(), name)
	if t.shared && (err == nil || err == errNoSelect) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.fail(s.srv.opts.Store.SetSubscribed(s.ctx, t.sid, t.folder, on))
}

func (s *session) Status(name string, options *imap.StatusOptions) (*imap.StatusData, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	t, err := s.resolve(s.srv.opts.Directory(), name)
	if err != nil {
		return nil, err
	}
	st, err := s.srv.opts.Store.FolderStatusAs(s.ctx, t.sid, t.folder, t.reader)
	if err != nil {
		return nil, s.fail(err)
	}
	return s.statusData(t.name, st, options), nil
}

func (s *session) statusData(name string, st storage.FolderStatus, options *imap.StatusOptions) *imap.StatusData {
	d := &imap.StatusData{Mailbox: name}
	u32 := func(n int) *uint32 { v := uint32(n); return &v }
	if options.NumMessages {
		d.NumMessages = u32(st.Messages)
	}
	if options.NumRecent {
		d.NumRecent = u32(0)
	}
	if options.UIDNext {
		d.UIDNext = imap.UID(st.UIDNext)
	}
	if options.UIDValidity {
		d.UIDValidity = st.UIDValidity
	}
	if options.NumUnseen {
		d.NumUnseen = u32(st.Unseen)
	}
	if options.NumDeleted {
		d.NumDeleted = u32(st.Deleted)
	}
	if options.Size {
		size := st.Size
		d.Size = &size
	}
	if options.AppendLimit {
		l := s.AppendLimit()
		d.AppendLimit = &l
	}
	return d
}

func (s *session) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	if err := s.check(); err != nil {
		return err
	}
	if len(patterns) == 0 {
		// LIST "" "": the hierarchy delimiter.
		return w.WriteList(&imap.ListData{Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}, Delim: delim})
	}
	dir := s.srv.opts.Directory()
	folders, err := s.srv.opts.Store.ListFolders(s.ctx, s.id())
	if err != nil {
		return s.fail(err)
	}
	byName, err := s.sharedFolders(dir)
	if err != nil {
		return s.fail(err)
	}
	for _, f := range folders {
		byName[f.Name] = listEntry{folder: f, t: target{sid: s.id(), folder: f.Name, name: f.Name, rights: config.AllRights}}
	}
	// Parents that do not exist themselves (the hierarchy is only a naming
	// convention) are listed as \Noselect when a pattern stops at their
	// level.
	implied := map[string]bool{}
	for _, f := range folders {
		parts := strings.Split(f.Name, string(delim))
		for i := 1; i < len(parts); i++ {
			p := strings.Join(parts[:i], string(delim))
			if _, ok := byName[p]; !ok {
				implied[p] = true
			}
		}
	}
	hasChildren := func(name string) bool {
		for n := range byName {
			if strings.HasPrefix(n, name+string(delim)) {
				return true
			}
		}
		return false
	}
	subscribedChild := func(name string) bool {
		for n, e := range byName {
			if e.folder.Subscribed && strings.HasPrefix(n, name+string(delim)) {
				return true
			}
		}
		return false
	}
	match := func(name string) bool {
		for _, p := range patterns {
			if imapserver.MatchList(name, delim, ref, p) {
				return true
			}
		}
		return false
	}
	names := make([]string, 0, len(byName)+len(implied))
	for n := range byName {
		names = append(names, n)
	}
	for n := range implied {
		names = append(names, n)
	}
	slices.SortFunc(names, func(a, b string) int {
		switch {
		case a == storage.Inbox:
			return -1
		case b == storage.Inbox:
			return 1
		}
		return strings.Compare(a, b)
	})
	for _, name := range names {
		if !match(name) {
			continue
		}
		e, exists := byName[name]
		f := e.folder
		data := imap.ListData{Mailbox: name, Delim: delim}
		if e.noSelect {
			// The parents of other mailboxes' folders are always listed, so
			// programs show them as a tree.
			if options.SelectSpecialUse || options.SelectSubscribed && !options.SelectRecursiveMatch {
				continue
			}
			data.Attrs = append(data.Attrs, imap.MailboxAttrNoSelect, imap.MailboxAttrHasChildren)
			if options.SelectRecursiveMatch {
				data.ChildInfo = &imap.ListDataChildInfo{Subscribed: true}
			}
			if err := w.WriteList(&data); err != nil {
				return err
			}
			continue
		}
		if !exists {
			// Only where a "%" pattern stops: with "*" the children are
			// listed themselves.
			descendantMatches := false
			for n := range byName {
				if strings.HasPrefix(n, name+string(delim)) && match(n) {
					descendantMatches = true
					break
				}
			}
			if descendantMatches && !options.SelectSubscribed {
				continue
			}
			if options.SelectSubscribed && !(options.SelectRecursiveMatch && subscribedChild(name)) {
				continue
			}
			if options.SelectSpecialUse {
				continue
			}
			data.Attrs = append(data.Attrs, imap.MailboxAttrNoSelect, imap.MailboxAttrHasChildren)
			if options.SelectRecursiveMatch {
				data.ChildInfo = &imap.ListDataChildInfo{Subscribed: true}
			}
			if err := w.WriteList(&data); err != nil {
				return err
			}
			continue
		}
		if options.SelectSpecialUse && f.SpecialUse == "" {
			continue
		}
		if options.SelectSubscribed && !f.Subscribed {
			if !(options.SelectRecursiveMatch && subscribedChild(name)) {
				continue
			}
			data.ChildInfo = &imap.ListDataChildInfo{Subscribed: true}
		}
		if f.Subscribed && (options.SelectSubscribed || options.ReturnSubscribed) {
			data.Attrs = append(data.Attrs, imap.MailboxAttrSubscribed)
		}
		if hasChildren(name) {
			data.Attrs = append(data.Attrs, imap.MailboxAttrHasChildren)
		} else {
			data.Attrs = append(data.Attrs, imap.MailboxAttrHasNoChildren)
		}
		// Special-use attributes are always sent: several clients only
		// look at plain LIST answers (RFC 6154 allows it).
		if f.SpecialUse != "" {
			data.Attrs = append(data.Attrs, imap.MailboxAttr(f.SpecialUse))
		}
		if options.ReturnStatus != nil {
			st, err := s.srv.opts.Store.FolderStatusAs(s.ctx, e.t.sid, e.t.folder, e.t.reader)
			if err == nil {
				data.Status = s.statusData(name, st, options.ReturnStatus)
			} else if !errors.Is(err, storage.ErrNoFolder) {
				return s.fail(err)
			}
		}
		if err := w.WriteList(&data); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) Append(name string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	flags, err := storeFlags(options.Flags)
	if err != nil {
		return nil, s.fail(err)
	}
	if err := s.checkDisk(); err != nil {
		return nil, err
	}
	dir := s.srv.opts.Directory()
	t, err := s.resolve(dir, name)
	if err != nil {
		return nil, errTryCreate(err)
	}
	if err := t.need(config.Write); err != nil {
		return nil, err
	}
	res, err := s.srv.opts.Store.Append(s.ctx, t.sid, t.folder, r, storage.AppendOptions{
		Flags:   flags,
		Date:    options.Time,
		MaxSize: dir.Limits().MaxMessageBytes,
		Reader:  t.reader,
		// The copy the program saves of mail sent for a mailbox that keeps
		// copies itself is stored once (by Message-ID).
		Dedupe: t.keepSent && s.isSent(t),
	})
	if err != nil {
		return nil, s.fail(errTryCreate(err))
	}
	return &imap.AppendData{UID: imap.UID(res.UID), UIDValidity: res.UIDValidity}, nil
}

// errDiskFull: the server's disk is nearly full; the client may try again
// later.
var errDiskFull = &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeUnavailable,
	Text: "The server is short of storage, try again later"}

// checkDisk refuses to store messages below the free disk space SMTP and
// submission keep.
func (s *session) checkDisk() error {
	free, err := s.srv.opts.FreeBytes()
	if err != nil {
		return s.fail(err)
	}
	if free < s.srv.opts.MinFreeBytes {
		return errDiskFull
	}
	return nil
}

// storeFlags converts IMAP flags for the storage: \Recent is not stored
// (and cannot be set by clients), everything else is checked there.
func storeFlags(flags []imap.Flag) ([]string, error) {
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		if strings.EqualFold(string(f), `\Recent`) {
			continue
		}
		out = append(out, string(f))
	}
	return storage.NormalizeFlags(out)
}

func imapFlags(flags []string) []imap.Flag {
	out := make([]imap.Flag, len(flags))
	for i, f := range flags {
		out[i] = imap.Flag(f)
	}
	return out
}

func (s *session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	for {
		if err := s.Poll(w, true); err != nil {
			return err
		}
		select {
		case <-stop:
			return nil
		case <-s.wake:
		}
	}
}
