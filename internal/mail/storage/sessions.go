package storage

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/danielgtmn/ablage"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// Sessions of the self-service area (package account). They are keyed by a
// hash of the session id; the id itself is never stored.

// AccountSession is a signed-in mailbox in the self-service area.
type AccountSession struct {
	StorageID string
	Address   string
	// Credential is a fingerprint of the password hash at sign-in.
	Credential string
	Created    time.Time
	LastSeen   time.Time
}

// MaxAccountSessions bounds the sessions of one mailbox; signing in once
// more ends the oldest.
const MaxAccountSessions = 20

// sessionID is the row id of a session: its id hash in hex.
func sessionID(idHash []byte) string { return hex.EncodeToString(idHash) }

// CreateAccountSession stores a new session.
func (s *Store) CreateAccountSession(ctx context.Context, idHash []byte, a AccountSession) error {
	if err := checkID(a.StorageID); err != nil {
		return err
	}
	id := sessionID(idHash)
	return s.write(ctx, func(tx *repos, _ *changes) error {
		if err := tx.sessions.Create(ctx, &sessionRec{ID: id, StorageID: a.StorageID, Address: a.Address, Credential: a.Credential,
			Created: sec(a.Created), LastSeen: sec(a.LastSeen)}); err != nil {
			return err
		}
		// Keep the new one and the newest others up to the bound.
		others, err := tx.sessions.Find(ctx, ablage.Query{
			Where:   ablage.And(ablage.Eq("storage_id", a.StorageID), ablage.Ne("id", id)),
			OrderBy: []ablage.Order{ablage.Desc("created")},
			Offset:  MaxAccountSessions - 1,
		})
		if err != nil || len(others) == 0 {
			return err
		}
		drop := make([]string, len(others))
		for i, o := range others {
			drop[i] = o.ID
		}
		_, err = tx.sessions.DeleteWhere(ctx, ablage.In("id", drop...))
		return err
	})
}

// AccountSessionByHash returns the session with the id hash; ok is false
// if there is none.
func (s *Store) AccountSessionByHash(ctx context.Context, idHash []byte) (a AccountSession, ok bool, err error) {
	r, err := s.r.sessions.Get(ctx, sessionID(idHash))
	if errors.Is(err, ablage.ErrNotFound) {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	return AccountSession{StorageID: r.StorageID, Address: r.Address, Credential: r.Credential, Created: local(r.Created), LastSeen: local(r.LastSeen)}, true, nil
}

// TouchAccountSession records the session's last request.
func (s *Store) TouchAccountSession(ctx context.Context, idHash []byte, at time.Time) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		_, err := tx.sessions.UpdateWhere(ctx, ablage.Eq("id", sessionID(idHash)), ablage.Row{"last_seen": sec(at)})
		return err
	})
}

// SetAccountSessionCredential gives a session a new credential
// fingerprint (after the mailbox changed its own password in it).
func (s *Store) SetAccountSessionCredential(ctx context.Context, idHash []byte, credential string) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		_, err := tx.sessions.UpdateWhere(ctx, ablage.Eq("id", sessionID(idHash)), ablage.Row{"credential": credential})
		return err
	})
}

// DeleteAccountSession ends a session.
func (s *Store) DeleteAccountSession(ctx context.Context, idHash []byte) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		_, err := tx.sessions.DeleteWhere(ctx, ablage.Eq("id", sessionID(idHash)))
		return err
	})
}

// DeleteAccountSessions ends every session of a mailbox except the one
// with the id hash except (nil: all).
func (s *Store) DeleteAccountSessions(ctx context.Context, storageID string, except []byte) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		_, err := tx.sessions.DeleteWhere(ctx, ablage.And(ablage.Eq("storage_id", storageID), ablage.Ne("id", sessionID(except))))
		return err
	})
}

// PruneAccountSessions deletes the sessions last seen before idleBefore or
// created before createdBefore, and those keep says no to (a mailbox that
// is gone or has another password). It returns how many it deleted.
func (s *Store) PruneAccountSessions(ctx context.Context, idleBefore, createdBefore time.Time, keep func(AccountSession) bool) (int, error) {
	n := 0
	err := s.write(ctx, func(tx *repos, _ *changes) error {
		n = 0
		d, err := tx.sessions.DeleteWhere(ctx, ablage.Or(ablage.Lt("last_seen", sec(idleBefore)), ablage.Lt("created", sec(createdBefore))))
		if err != nil {
			return err
		}
		n += int(d)
		list, err := tx.sessions.Find(ctx, ablage.Query{})
		if err != nil {
			return err
		}
		var drop []string
		for _, r := range list {
			if !keep(AccountSession{StorageID: r.StorageID, Address: r.Address, Credential: r.Credential}) {
				drop = append(drop, r.ID)
			}
		}
		if len(drop) > 0 {
			if _, err := tx.sessions.DeleteWhere(ctx, ablage.In("id", drop...)); err != nil {
				return err
			}
		}
		n += len(drop)
		return nil
	})
	return n, err
}

// AppPasswordUse is the last reported use of an app password.
type AppPasswordUse struct {
	At      time.Time
	Network string
}

// maxUseEvents bounds how many use events AppPasswordUses reads.
const maxUseEvents = 5000

// AppPasswordUses returns the last use of each app password of a mailbox
// that is still in the events (EventRetention), by app password id.
func (s *Store) AppPasswordUses(ctx context.Context, storageID string) (map[string]AppPasswordUse, error) {
	list, err := s.r.events.Find(ctx, ablage.Query{
		Where:   ablage.And(ablage.Eq("storage_id", storageID), ablage.Eq("kind", config.EventAppPasswordUsed)),
		OrderBy: []ablage.Order{ablage.Desc("seq")},
		Limit:   maxUseEvents,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]AppPasswordUse{}
	for _, r := range list {
		var ch config.AccountChange
		if json.Unmarshal([]byte(r.Account), &ch) != nil || ch.AppPasswordID == "" {
			continue
		}
		if _, seen := out[ch.AppPasswordID]; !seen {
			out[ch.AppPasswordID] = AppPasswordUse{At: r.At.UTC(), Network: ch.Network}
		}
	}
	return out, nil
}
