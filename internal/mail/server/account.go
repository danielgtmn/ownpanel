package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
	"github.com/danielgtmn/ownpanel/internal/passwd"
)

// Changes a mailbox makes to its own account here (the self-service area,
// built on top of ApplyAccountChange) are applied at once, kept as an
// overlay over the pushed configuration and reported to the panel as an
// event with a revision; see directory.Overlay and Decision 6 in
// wiki/Groupware-Architecture.md.

// Errors of ApplyAccountChange.
var (
	ErrUnknownMailbox = errors.New("no such mailbox")
	// ErrInvalidChange: the change is malformed (callers check input
	// first and give their own message).
	ErrInvalidChange = errors.New("invalid account change")
	// ErrTooManyAppPasswords: the mailbox has config.MaxAppPasswords.
	ErrTooManyAppPasswords = errors.New("too many app passwords")
	// ErrNoAppPasswordKey: the panel has not pushed an app password key.
	ErrNoAppPasswordKey = errors.New("app passwords are not set up")
)

func loadOverlays(st *storage.Store) (map[string]*directory.Overlay, error) {
	raw, err := st.AccountOverlays(context.Background())
	if err != nil {
		return nil, err
	}
	out := make(map[string]*directory.Overlay, len(raw))
	for id, b := range raw {
		o := &directory.Overlay{}
		if err := json.Unmarshal(b, o); err != nil {
			return nil, fmt.Errorf("mailbox %s: %w", id, err)
		}
		out[id] = o
	}
	return out, nil
}

// pruneOverlays drops the overlay parts cfg includes (by each mailbox's
// Revision and the fields the panel set itself, see Overlay.PruneFor) and
// the overlays of mailboxes cfg no longer has. Caller holds
// loadMu.
func (s *Server) pruneOverlays(ctx context.Context, cfg *config.Config) error {
	if len(s.overlays) == 0 {
		return nil
	}
	pushed := make(map[string]config.Mailbox, len(cfg.Mailboxes))
	for _, m := range cfg.Mailboxes {
		pushed[m.StorageID] = m
	}
	next := maps.Clone(s.overlays)
	changes := map[string][]byte{}
	for id, o := range s.overlays {
		m, ok := pushed[id]
		if !ok {
			delete(next, id)
			changes[id] = nil
			continue
		}
		cp := cloneOverlay(o)
		cp.PruneFor(m)
		if cp.Empty() {
			delete(next, id)
			changes[id] = nil
			continue
		}
		b, err := json.Marshal(cp)
		if err != nil {
			return err
		}
		if old, _ := json.Marshal(o); string(old) == string(b) {
			continue
		}
		next[id] = cp
		changes[id] = b
	}
	if len(changes) == 0 {
		return nil
	}
	if err := s.store.PutAccountOverlays(ctx, changes); err != nil {
		return err
	}
	s.overlays = next
	return nil
}

func cloneOverlay(o *directory.Overlay) *directory.Overlay {
	b, err := json.Marshal(o)
	if err != nil {
		panic(err)
	}
	cp := &directory.Overlay{}
	if err := json.Unmarshal(b, cp); err != nil {
		panic(err)
	}
	return cp
}

// ApplyAccountChange applies a change a mailbox made to its own account:
// exactly one of PasswordHash, DisplayName, AppPasswordsOnly, AppPassword
// (created) or AppPasswordID (revoked) is set; Revision is assigned here.
// The change takes effect at once (a revoked app password and, with
// AppPasswordsOnly, the main password stop working in open sessions too),
// survives a restart, and is reported to the panel as an event. It returns
// the change with its revision.
func (s *Server) ApplyAccountChange(ctx context.Context, storageID string, ch config.AccountChange) (config.AccountChange, error) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	rt := s.state.Load()
	var pushed *config.Mailbox
	for i := range rt.pushed.Mailboxes {
		if rt.pushed.Mailboxes[i].StorageID == storageID {
			pushed = &rt.pushed.Mailboxes[i]
		}
	}
	cur, ok := rt.dir.MailboxByStorageID(storageID)
	if pushed == nil || !ok {
		return ch, ErrUnknownMailbox
	}
	kind, detail, err := checkChange(rt, cur, ch)
	if err != nil {
		return ch, err
	}
	o := &directory.Overlay{}
	if old := s.overlays[storageID]; old != nil {
		o = cloneOverlay(old)
	}
	ch.Revision = o.NextRevision(pushed.Revision)
	ch.Network = ""
	// The overlay keeps the event's time: the panel compares the same time
	// with when it set the field itself (Overlay.PruneFor).
	now := time.Now()
	o.Apply(ch, now)
	data, err := json.Marshal(o)
	if err != nil {
		return ch, err
	}
	ev := config.Event{At: now, Kind: kind, StorageID: storageID, Address: cur.Address, Detail: detail, Account: &ch}
	if err := s.store.SaveAccountChange(ctx, storageID, data, ev); err != nil {
		return ch, err
	}
	next := maps.Clone(s.overlays)
	if next == nil {
		next = map[string]*directory.Overlay{}
	}
	next[storageID] = o
	s.overlays = next
	if err := s.apply(rt.pushed); err != nil {
		return ch, err
	}
	// Sessions check themselves against the new directory.
	s.syncListeners()
	return ch, nil
}

// checkChange validates a change against the mailbox as it is now and
// returns the event kind and a detail without secrets.
func checkChange(rt *runtime, cur *config.Mailbox, ch config.AccountChange) (kind, detail string, err error) {
	n := 0
	if ch.PasswordHash != "" {
		n++
		kind = config.EventPasswordChanged
		if !passwd.WellFormed(ch.PasswordHash) {
			return "", "", ErrInvalidChange
		}
	}
	if ch.DisplayName != nil {
		n++
		kind = config.EventDisplayNameChanged
		if !config.ValidDisplayName(*ch.DisplayName) {
			return "", "", ErrInvalidChange
		}
		detail = "removed"
		if *ch.DisplayName != "" {
			detail = "now " + *ch.DisplayName
		}
	}
	if ch.AppPasswordsOnly != nil {
		n++
		kind = config.EventAppPasswordsOnly
		detail = "off"
		if *ch.AppPasswordsOnly {
			detail = "on"
		}
	}
	if ap := ch.AppPassword; ap != nil {
		n++
		kind = config.EventAppPasswordCreated
		if rt.pushed.AppPasswordKey == "" {
			return "", "", ErrNoAppPasswordKey
		}
		if !config.ValidAppPasswordID(ap.ID) || ap.Name == "" || !config.ValidDisplayName(ap.Name) || len(ap.MAC) != 64 || ap.Created.IsZero() {
			return "", "", ErrInvalidChange
		}
		if len(cur.AppPasswords) >= config.MaxAppPasswords {
			return "", "", ErrTooManyAppPasswords
		}
		for _, m := range rt.dir.Config().Mailboxes {
			for _, other := range m.AppPasswords {
				if other.ID == ap.ID {
					return "", "", ErrInvalidChange
				}
			}
		}
		detail = ap.Name
	}
	if ch.AppPasswordID != "" {
		n++
		kind = config.EventAppPasswordRevoked
		found := false
		for _, ap := range cur.AppPasswords {
			if ap.ID == ch.AppPasswordID {
				found, detail = true, ap.Name
			}
		}
		if !found {
			return "", "", ErrInvalidChange
		}
	}
	if ch.Grants != nil {
		n++
		kind = config.EventGrantsChanged
		if cur.Shared() || config.CheckGrants(*cur, *ch.Grants) != nil {
			return "", "", ErrInvalidChange
		}
		cfg := rt.dir.Config()
		orgs := map[string]string{}
		for _, d := range cfg.Domains {
			orgs[d.Name] = cfg.OrganisationOf(d.Name)
		}
		byStorage := map[string]*config.Mailbox{}
		for i := range cfg.Mailboxes {
			byStorage[cfg.Mailboxes[i].StorageID] = &cfg.Mailboxes[i]
		}
		var parts []string
		for _, g := range *ch.Grants {
			if config.GrantProblem(*cur, g, byStorage, orgs) != "" {
				return "", "", ErrInvalidChange
			}
			r, _ := config.ParseRights(g.Rights)
			parts = append(parts, byStorage[g.StorageID].Address+" ("+strings.Join(r.Names(), ", ")+")")
		}
		detail = "shared with nobody"
		if len(parts) > 0 {
			detail = "shared with " + strings.Join(parts, "; ")
		}
	}
	if n != 1 || ch.Network != "" {
		return "", "", ErrInvalidChange
	}
	return kind, detail, nil
}
