package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

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

// CreateAccountSession stores a new session.
func (s *Store) CreateAccountSession(ctx context.Context, idHash []byte, a AccountSession) error {
	if err := checkID(a.StorageID); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		if _, err := tx.Exec(`INSERT INTO account_sessions (id_hash, storage_id, address, credential, created, last_seen)
			VALUES (?, ?, ?, ?, ?, ?)`, idHash, a.StorageID, a.Address, a.Credential, a.Created.Unix(), a.LastSeen.Unix()); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM account_sessions WHERE storage_id = ? AND id_hash NOT IN
			(SELECT id_hash FROM account_sessions WHERE storage_id = ? ORDER BY created DESC, rowid DESC LIMIT ?)`,
			a.StorageID, a.StorageID, MaxAccountSessions)
		return err
	})
}

// AccountSessionByHash returns the session with the id hash; ok is false
// if there is none.
func (s *Store) AccountSessionByHash(ctx context.Context, idHash []byte) (a AccountSession, ok bool, err error) {
	var created, seen int64
	err = s.rdb.QueryRowContext(ctx, `SELECT storage_id, address, credential, created, last_seen FROM account_sessions WHERE id_hash = ?`,
		idHash).Scan(&a.StorageID, &a.Address, &a.Credential, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	a.Created, a.LastSeen = time.Unix(created, 0), time.Unix(seen, 0)
	return a, true, nil
}

// TouchAccountSession records the session's last request.
func (s *Store) TouchAccountSession(ctx context.Context, idHash []byte, at time.Time) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		_, err := tx.Exec(`UPDATE account_sessions SET last_seen = ? WHERE id_hash = ?`, at.Unix(), idHash)
		return err
	})
}

// SetAccountSessionCredential gives a session a new credential
// fingerprint (after the mailbox changed its own password in it).
func (s *Store) SetAccountSessionCredential(ctx context.Context, idHash []byte, credential string) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		_, err := tx.Exec(`UPDATE account_sessions SET credential = ? WHERE id_hash = ?`, credential, idHash)
		return err
	})
}

// DeleteAccountSession ends a session.
func (s *Store) DeleteAccountSession(ctx context.Context, idHash []byte) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		_, err := tx.Exec(`DELETE FROM account_sessions WHERE id_hash = ?`, idHash)
		return err
	})
}

// DeleteAccountSessions ends every session of a mailbox except the one
// with the id hash except (nil: all).
func (s *Store) DeleteAccountSessions(ctx context.Context, storageID string, except []byte) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		_, err := tx.Exec(`DELETE FROM account_sessions WHERE storage_id = ? AND id_hash != ?`, storageID, append([]byte{}, except...))
		return err
	})
}

// PruneAccountSessions deletes the sessions last seen before idleBefore or
// created before createdBefore, and those keep says no to (a mailbox that
// is gone or has another password). It returns how many it deleted.
func (s *Store) PruneAccountSessions(ctx context.Context, idleBefore, createdBefore time.Time, keep func(AccountSession) bool) (int, error) {
	n := 0
	err := s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		res, err := tx.Exec(`DELETE FROM account_sessions WHERE last_seen < ? OR created < ?`, idleBefore.Unix(), createdBefore.Unix())
		if err != nil {
			return err
		}
		d, _ := res.RowsAffected()
		n += int(d)
		rows, err := tx.Query(`SELECT id_hash, storage_id, address, credential FROM account_sessions`)
		if err != nil {
			return err
		}
		var drop [][]byte
		for rows.Next() {
			var h []byte
			var a AccountSession
			if err := rows.Scan(&h, &a.StorageID, &a.Address, &a.Credential); err != nil {
				rows.Close()
				return err
			}
			if !keep(a) {
				drop = append(drop, h)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, h := range drop {
			if _, err := tx.Exec(`DELETE FROM account_sessions WHERE id_hash = ?`, h); err != nil {
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
	rows, err := s.rdb.QueryContext(ctx, `SELECT at, account FROM events WHERE storage_id = ? AND kind = ? ORDER BY id DESC LIMIT ?`,
		storageID, config.EventAppPasswordUsed, maxUseEvents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AppPasswordUse{}
	for rows.Next() {
		var at int64
		var account string
		if err := rows.Scan(&at, &account); err != nil {
			return nil, err
		}
		var ch config.AccountChange
		if json.Unmarshal([]byte(account), &ch) != nil || ch.AppPasswordID == "" {
			continue
		}
		if _, seen := out[ch.AppPasswordID]; !seen {
			out[ch.AppPasswordID] = AppPasswordUse{At: time.Unix(at, 0).UTC(), Network: ch.Network}
		}
	}
	return out, rows.Err()
}
