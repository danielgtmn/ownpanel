package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// BindingConflictError is returned by Bind when a storage id with data was
// bound to another address before.
type BindingConflictError struct {
	StorageID string
	Bound     string
	Requested string
}

func (e *BindingConflictError) Error() string {
	return fmt.Sprintf("storage id %s belongs to %s, not %s", e.StorageID, e.Bound, e.Requested)
}

// Bind records which address each storage id belongs to. The first binding
// of a storage id is kept as long as the mailbox has data: binding it to
// another address is refused (a mix-up in the panel must never hand one
// person's mail to another address). Without data a binding may change.
// Either all bindings are applied or none.
func (s *Store) Bind(ctx context.Context, bindings map[string]string) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		for id, addr := range bindings {
			if err := checkID(id); err != nil {
				return err
			}
			var bound string
			err := tx.QueryRow(`SELECT address FROM bindings WHERE storage_id = ?`, id).Scan(&bound)
			if errors.Is(err, sql.ErrNoRows) {
				if _, err := tx.Exec(`INSERT INTO bindings (storage_id, address, bound) VALUES (?, ?, ?)`, id, addr, s.now().Unix()); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if bound == addr {
				continue
			}
			var hasData int
			if err := tx.QueryRow(`SELECT count(*) FROM mailboxes WHERE storage_id = ?`, id).Scan(&hasData); err != nil {
				return err
			}
			if hasData > 0 {
				return &BindingConflictError{StorageID: id, Bound: bound, Requested: addr}
			}
			if _, err := tx.Exec(`UPDATE bindings SET address = ?, bound = ? WHERE storage_id = ?`, addr, s.now().Unix(), id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Block is the sending block state of a mailbox.
type Block struct {
	Blocked bool
	Reason  string
	At      time.Time
}

// SetBlocked blocks a mailbox from sending (receiving goes on).
func (s *Store) SetBlocked(ctx context.Context, id, reason string) error {
	if err := checkID(id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		_, err := tx.Exec(`INSERT INTO blocks (storage_id, blocked, reason, at) VALUES (?, 1, ?, ?)
			ON CONFLICT (storage_id) DO UPDATE SET blocked = 1, reason = excluded.reason, at = excluded.at`,
			id, reason, s.now().Unix())
		return err
	})
}

// Unblock lifts a block and clears the send counters of the mailbox (sent
// messages, hourly limit hits, sign-in networks), so the reasons for the
// block do not block it again at once. It reports whether the mailbox was
// blocked.
func (s *Store) Unblock(ctx context.Context, id string) (bool, error) {
	if err := checkID(id); err != nil {
		return false, err
	}
	var was bool
	err := s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		res, err := tx.Exec(`DELETE FROM blocks WHERE storage_id = ? AND blocked = 1`, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		was = n > 0
		_, err = tx.Exec(`DELETE FROM send_log WHERE key IN (?, ?, ?, ?)`, mailboxSendKeys(id)...)
		return err
	})
	return was, err
}

// Blocks returns the block state of every blocked mailbox.
func (s *Store) Blocks(ctx context.Context) (map[string]Block, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT storage_id, reason, at FROM blocks WHERE blocked = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Block{}
	for rows.Next() {
		var id string
		var b Block
		var at int64
		if err := rows.Scan(&id, &b.Reason, &at); err != nil {
			return nil, err
		}
		b.Blocked = true
		b.At = time.Unix(at, 0).UTC()
		out[id] = b
	}
	return out, rows.Err()
}

// MaxEvents is the most Events returns at once.
const MaxEvents = 500

// EventRetention is how long events are kept.
const EventRetention = 30 * 24 * time.Hour

// AddEvent records an event for the panel. Detail must not contain secrets.
func (s *Store) AddEvent(ctx context.Context, e config.Event) error {
	at := e.At
	if at.IsZero() {
		at = s.now()
	}
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		return insertEvent(tx, e, at)
	})
}

func insertEvent(tx *sql.Tx, e config.Event, at time.Time) error {
	account := ""
	if e.Account != nil {
		b, err := json.Marshal(e.Account)
		if err != nil {
			return err
		}
		account = string(b)
	}
	_, err := tx.Exec(`INSERT INTO events (at, kind, storage_id, address, detail, account) VALUES (?, ?, ?, ?, ?, ?)`,
		at.Unix(), e.Kind, e.StorageID, e.Address, e.Detail, account)
	return err
}

// Events returns up to MaxEvents events with an id greater than after,
// oldest first.
func (s *Store) Events(ctx context.Context, after int64) ([]config.Event, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT id, at, kind, storage_id, address, detail, account FROM events
		WHERE id > ? ORDER BY id LIMIT ?`, after, MaxEvents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []config.Event{}
	for rows.Next() {
		var e config.Event
		var at int64
		var account string
		if err := rows.Scan(&e.ID, &at, &e.Kind, &e.StorageID, &e.Address, &e.Detail, &account); err != nil {
			return nil, err
		}
		if account != "" {
			e.Account = &config.AccountChange{}
			if err := json.Unmarshal([]byte(account), e.Account); err != nil {
				return nil, fmt.Errorf("event %d: %w", e.ID, err)
			}
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// AccountOverlays returns the stored account overlays (directory.Overlay
// as JSON) by storage id.
func (s *Store) AccountOverlays(ctx context.Context) (map[string][]byte, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT storage_id, data FROM account_overlays`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		out[id] = []byte(data)
	}
	return out, rows.Err()
}

// PutAccountOverlays stores or (nil data) deletes overlays, atomically.
func (s *Store) PutAccountOverlays(ctx context.Context, overlays map[string][]byte) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		return putOverlays(tx, overlays)
	})
}

func putOverlays(tx *sql.Tx, overlays map[string][]byte) error {
	for id, data := range overlays {
		if err := checkID(id); err != nil {
			return err
		}
		var err error
		if data == nil {
			_, err = tx.Exec(`DELETE FROM account_overlays WHERE storage_id = ?`, id)
		} else {
			_, err = tx.Exec(`INSERT INTO account_overlays (storage_id, data) VALUES (?, ?)
				ON CONFLICT (storage_id) DO UPDATE SET data = excluded.data`, id, string(data))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// SaveAccountChange stores a mailbox's overlay (data, nil deletes it) and
// records the event of the change, in one transaction: a change is never
// applied without being reported, nor reported without being kept.
func (s *Store) SaveAccountChange(ctx context.Context, storageID string, data []byte, e config.Event) error {
	at := e.At
	if at.IsZero() {
		at = s.now()
	}
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		if err := putOverlays(tx, map[string][]byte{storageID: data}); err != nil {
			return err
		}
		return insertEvent(tx, e, at)
	})
}

// PruneEvents deletes events older than EventRetention. The AUTOINCREMENT
// ids are never reused, so the panel's cursor stays valid.
func (s *Store) PruneEvents(ctx context.Context) error {
	cutoff := s.now().Add(-EventRetention).Unix()
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		_, err := tx.Exec(`DELETE FROM events WHERE at < ?`, cutoff)
		return err
	})
}
