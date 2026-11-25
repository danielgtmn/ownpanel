package directory

import (
	"slices"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// Overlay holds the changes a mailbox made to its own account in the mail
// service (self-service) that the panel's configuration may not include
// yet (Decision 6 in wiki/Groupware-Architecture.md).
//
// Every change gets a revision, one higher than any the mailbox had. The
// panel records the change from the event and from then on pushes the
// mailbox with Revision at least that high. Each part of the overlay keeps
// the revision of its last change and is dropped once a pushed
// configuration's Revision reaches it (Prune): from then on the pushed
// value is the truth, also when the panel decided otherwise (an admin
// changed the same field at the same time). Until then the overlay wins
// over what a push says, so a push built before the panel saw the change
// cannot undo it, with one exception: a field the panel set itself at or
// after the time of the mailbox's change (config.Mailbox.PasswordSetAt and
// the like) drops that part at once (PruneFor), so an admin's reset takes
// effect with the push even before the panel saw the mailbox's change. The
// panel decides the same way when it sees the change (the same times).
type Overlay struct {
	// Revision is the highest revision of any change in the overlay.
	Revision         int64                `json:"revision"`
	PasswordHash     *Stamped[string]     `json:"password_hash,omitempty"`
	DisplayName      *Stamped[string]     `json:"display_name,omitempty"`
	AppPasswordsOnly *Stamped[bool]       `json:"app_passwords_only,omitempty"`
	Added            []StampedAppPassword `json:"added,omitempty"`
	Revoked          []Stamped[string]    `json:"revoked,omitempty"`
	// Grants is the self-delegation of a personal mailbox (G3): the whole
	// list as the person set it last.
	Grants *Stamped[[]config.Grant] `json:"grants,omitempty"`
}

// Stamped is a value with the revision and time (Unix seconds) of the
// change that set it.
type Stamped[T any] struct {
	Value    T     `json:"value"`
	Revision int64 `json:"revision"`
	At       int64 `json:"at,omitempty"`
}

// StampedAppPassword is an app password created in the mail service.
type StampedAppPassword struct {
	config.AppPassword
	Revision int64 `json:"revision"`
}

// Empty tells whether the overlay holds no change.
func (o *Overlay) Empty() bool {
	return o.PasswordHash == nil && o.DisplayName == nil && o.AppPasswordsOnly == nil && len(o.Added) == 0 && len(o.Revoked) == 0 &&
		o.Grants == nil
}

// NextRevision is the revision of the next change of a mailbox whose pushed
// configuration has revision pushed.
func (o *Overlay) NextRevision(pushed int64) int64 { return max(o.Revision, pushed) + 1 }

// Apply records a change made at at (the time of its event) with its
// revision (ch.Revision must be NextRevision). Only the fields of the
// change are touched.
func (o *Overlay) Apply(ch config.AccountChange, at time.Time) {
	r, sec := ch.Revision, at.Unix()
	o.Revision = max(o.Revision, r)
	if ch.PasswordHash != "" {
		o.PasswordHash = &Stamped[string]{ch.PasswordHash, r, sec}
	}
	if ch.DisplayName != nil {
		o.DisplayName = &Stamped[string]{*ch.DisplayName, r, sec}
	}
	if ch.AppPasswordsOnly != nil {
		o.AppPasswordsOnly = &Stamped[bool]{*ch.AppPasswordsOnly, r, sec}
	}
	if ch.Grants != nil {
		o.Grants = &Stamped[[]config.Grant]{slices.Clone(*ch.Grants), r, sec}
	}
	if ch.AppPassword != nil {
		o.Added = append(o.Added, StampedAppPassword{*ch.AppPassword, r})
	}
	if ch.AppPasswordID != "" {
		id := ch.AppPasswordID
		// Created here and not pushed yet: forget it altogether.
		o.Added = slices.DeleteFunc(o.Added, func(a StampedAppPassword) bool { return a.ID == id })
		o.Revoked = append(o.Revoked, Stamped[string]{id, r, sec})
	}
}

// Prune drops the changes a configuration with revision pushed includes.
func (o *Overlay) Prune(pushed int64) {
	if o.PasswordHash != nil && o.PasswordHash.Revision <= pushed {
		o.PasswordHash = nil
	}
	if o.DisplayName != nil && o.DisplayName.Revision <= pushed {
		o.DisplayName = nil
	}
	if o.AppPasswordsOnly != nil && o.AppPasswordsOnly.Revision <= pushed {
		o.AppPasswordsOnly = nil
	}
	if o.Grants != nil && o.Grants.Revision <= pushed {
		o.Grants = nil
	}
	o.Added = slices.DeleteFunc(o.Added, func(a StampedAppPassword) bool { return a.Revision <= pushed })
	o.Revoked = slices.DeleteFunc(o.Revoked, func(r Stamped[string]) bool { return r.Revision <= pushed })
}

// PruneFor is Prune for the pushed mailbox m, and also drops the password,
// display name or switch the panel set itself at or after the time of the
// mailbox's change (m.PasswordSetAt and the like).
func (o *Overlay) PruneFor(m config.Mailbox) {
	o.Prune(m.Revision)
	setByPanel := func(at, set int64) bool { return set > 0 && at <= set }
	if o.PasswordHash != nil && setByPanel(o.PasswordHash.At, m.PasswordSetAt) {
		o.PasswordHash = nil
	}
	if o.DisplayName != nil && setByPanel(o.DisplayName.At, m.DisplayNameSetAt) {
		o.DisplayName = nil
	}
	if o.AppPasswordsOnly != nil && setByPanel(o.AppPasswordsOnly.At, m.AppPasswordsOnlySetAt) {
		o.AppPasswordsOnly = nil
	}
}

// MergeInto applies the overlay to m (the mailbox as pushed).
func (o *Overlay) MergeInto(m *config.Mailbox) {
	if m.Shared() {
		// A shared mailbox has no password: what it changed while it was
		// personal no longer applies (its sessions ended with the
		// conversion).
		if o.DisplayName != nil {
			m.DisplayName = o.DisplayName.Value
		}
		return
	}
	if o.PasswordHash != nil {
		m.PasswordHash = o.PasswordHash.Value
	}
	if o.DisplayName != nil {
		m.DisplayName = o.DisplayName.Value
	}
	if o.AppPasswordsOnly != nil {
		m.AppPasswordsOnly = o.AppPasswordsOnly.Value
	}
	// Only a personal mailbox delegates itself; the grants of a shared one
	// are the panel's.
	if o.Grants != nil && !m.Shared() {
		m.Grants = slices.Clone(o.Grants.Value)
	}
	if len(o.Added) == 0 && len(o.Revoked) == 0 {
		return
	}
	aps := slices.Clone(m.AppPasswords)
	aps = slices.DeleteFunc(aps, func(a config.AppPassword) bool {
		return slices.ContainsFunc(o.Revoked, func(r Stamped[string]) bool { return r.Value == a.ID })
	})
	for _, a := range o.Added {
		if !slices.ContainsFunc(aps, func(b config.AppPassword) bool { return b.ID == a.ID }) {
			aps = append(aps, a.AppPassword)
		}
	}
	m.AppPasswords = aps
}

// Merge returns a copy of cfg with the overlays (by storage id) applied.
// cfg itself is not changed.
func Merge(cfg *config.Config, overlays map[string]*Overlay) *config.Config {
	if len(overlays) == 0 {
		return cfg
	}
	out := *cfg
	out.Mailboxes = slices.Clone(cfg.Mailboxes)
	for i := range out.Mailboxes {
		if o := overlays[out.Mailboxes[i].StorageID]; o != nil {
			o.MergeInto(&out.Mailboxes[i])
		}
	}
	return &out
}
