package directory

import (
	"slices"

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
// cannot undo it.
type Overlay struct {
	// Revision is the highest revision of any change in the overlay.
	Revision         int64                `json:"revision"`
	PasswordHash     *Stamped[string]     `json:"password_hash,omitempty"`
	DisplayName      *Stamped[string]     `json:"display_name,omitempty"`
	AppPasswordsOnly *Stamped[bool]       `json:"app_passwords_only,omitempty"`
	Added            []StampedAppPassword `json:"added,omitempty"`
	Revoked          []Stamped[string]    `json:"revoked,omitempty"`
}

// Stamped is a value with the revision of the change that set it.
type Stamped[T any] struct {
	Value    T     `json:"value"`
	Revision int64 `json:"revision"`
}

// StampedAppPassword is an app password created in the mail service.
type StampedAppPassword struct {
	config.AppPassword
	Revision int64 `json:"revision"`
}

// Empty tells whether the overlay holds no change.
func (o *Overlay) Empty() bool {
	return o.PasswordHash == nil && o.DisplayName == nil && o.AppPasswordsOnly == nil && len(o.Added) == 0 && len(o.Revoked) == 0
}

// NextRevision is the revision of the next change of a mailbox whose pushed
// configuration has revision pushed.
func (o *Overlay) NextRevision(pushed int64) int64 { return max(o.Revision, pushed) + 1 }

// Apply records a change with its revision (ch.Revision must be
// NextRevision). Only the fields of the change are touched.
func (o *Overlay) Apply(ch config.AccountChange) {
	r := ch.Revision
	o.Revision = max(o.Revision, r)
	if ch.PasswordHash != "" {
		o.PasswordHash = &Stamped[string]{ch.PasswordHash, r}
	}
	if ch.DisplayName != nil {
		o.DisplayName = &Stamped[string]{*ch.DisplayName, r}
	}
	if ch.AppPasswordsOnly != nil {
		o.AppPasswordsOnly = &Stamped[bool]{*ch.AppPasswordsOnly, r}
	}
	if ch.AppPassword != nil {
		o.Added = append(o.Added, StampedAppPassword{*ch.AppPassword, r})
	}
	if ch.AppPasswordID != "" {
		id := ch.AppPasswordID
		// Created here and not pushed yet: forget it altogether.
		o.Added = slices.DeleteFunc(o.Added, func(a StampedAppPassword) bool { return a.ID == id })
		o.Revoked = append(o.Revoked, Stamped[string]{id, r})
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
	o.Added = slices.DeleteFunc(o.Added, func(a StampedAppPassword) bool { return a.Revision <= pushed })
	o.Revoked = slices.DeleteFunc(o.Revoked, func(r Stamped[string]) bool { return r.Revision <= pushed })
}

// MergeInto applies the overlay to m (the mailbox as pushed).
func (o *Overlay) MergeInto(m *config.Mailbox) {
	if o.PasswordHash != nil {
		m.PasswordHash = o.PasswordHash.Value
	}
	if o.DisplayName != nil {
		m.DisplayName = o.DisplayName.Value
	}
	if o.AppPasswordsOnly != nil {
		m.AppPasswordsOnly = o.AppPasswordsOnly.Value
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
