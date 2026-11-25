package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Filter scripts (Sieve) of a mailbox and what their runs remember (G2):
// out-of-office replies sent and ids the duplicate test saw. Scripts are
// stored as written; checking them is the caller's job.

// Errors of script changes.
var (
	ErrNoScript      = errors.New("no such script")
	ErrScriptExists  = errors.New("a script of that name exists")
	ErrScriptActive  = errors.New("the active script cannot be deleted")
	ErrTooManyScript = errors.New("too many scripts")
)

// SieveScript describes one script.
type SieveScript struct {
	Name    string
	Active  bool
	Size    int
	Updated time.Time
}

// SieveScripts lists a mailbox's scripts by name.
func (s *Store) SieveScripts(ctx context.Context, id string) ([]SieveScript, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT name, active, length(CAST(content AS BLOB)), updated FROM sieve_scripts WHERE storage_id = ? ORDER BY name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SieveScript
	for rows.Next() {
		var sc SieveScript
		var at int64
		if err := rows.Scan(&sc.Name, &sc.Active, &sc.Size, &at); err != nil {
			return nil, err
		}
		sc.Updated = time.Unix(at, 0)
		out = append(out, sc)
	}
	return out, rows.Err()
}

// SieveScript returns one script; ErrNoScript when there is none.
func (s *Store) SieveScript(ctx context.Context, id, name string) (content string, active bool, err error) {
	err = s.rdb.QueryRowContext(ctx, `SELECT content, active FROM sieve_scripts WHERE storage_id = ? AND name = ?`, id, name).Scan(&content, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrNoScript
	}
	return content, active, err
}

// ActiveSieveScript returns the active script; ok is false when none is.
func (s *Store) ActiveSieveScript(ctx context.Context, id string) (name, content string, ok bool, err error) {
	err = s.rdb.QueryRowContext(ctx, `SELECT name, content FROM sieve_scripts WHERE storage_id = ? AND active = 1`, id).Scan(&name, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return name, content, err == nil, err
}

// PutSieveScript stores a script (replacing one of the same name). A new
// script beyond max scripts is ErrTooManyScript. activate also makes it
// the active one.
func (s *Store) PutSieveScript(ctx context.Context, id, name, content string, max int, activate bool) error {
	if err := checkID(id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		var n, exists int
		if err := tx.QueryRow(`SELECT count(*), coalesce(sum(name = ?), 0) FROM sieve_scripts WHERE storage_id = ?`, name, id).Scan(&n, &exists); err != nil {
			return err
		}
		if exists == 0 && n >= max {
			return ErrTooManyScript
		}
		if activate {
			if _, err := tx.Exec(`UPDATE sieve_scripts SET active = 0 WHERE storage_id = ? AND active = 1 AND name != ?`, id, name); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`INSERT INTO sieve_scripts (storage_id, name, content, active, updated) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (storage_id, name) DO UPDATE SET content = excluded.content, updated = excluded.updated,
			active = CASE WHEN excluded.active = 1 THEN 1 ELSE sieve_scripts.active END`,
			id, name, content, activate, s.now().Unix())
		return err
	})
}

// SetActiveSieveScript makes name the active script; "" makes none active.
func (s *Store) SetActiveSieveScript(ctx context.Context, id, name string) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		if name != "" {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM sieve_scripts WHERE storage_id = ? AND name = ?`, id, name).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrNoScript
			}
		}
		if _, err := tx.Exec(`UPDATE sieve_scripts SET active = 0 WHERE storage_id = ? AND active = 1`, id); err != nil {
			return err
		}
		if name == "" {
			return nil
		}
		_, err := tx.Exec(`UPDATE sieve_scripts SET active = 1 WHERE storage_id = ? AND name = ?`, id, name)
		return err
	})
}

// DeleteSieveScript removes a script that is not active.
func (s *Store) DeleteSieveScript(ctx context.Context, id, name string) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		var active bool
		err := tx.QueryRow(`SELECT active FROM sieve_scripts WHERE storage_id = ? AND name = ?`, id, name).Scan(&active)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoScript
		}
		if err != nil {
			return err
		}
		if active {
			return ErrScriptActive
		}
		_, err = tx.Exec(`DELETE FROM sieve_scripts WHERE storage_id = ? AND name = ?`, id, name)
		return err
	})
}

// RenameSieveScript renames a script; it stays active if it was.
func (s *Store) RenameSieveScript(ctx context.Context, id, from, to string) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM sieve_scripts WHERE storage_id = ? AND name = ?`, id, to).Scan(&n); err != nil {
			return err
		}
		if n > 0 && from != to {
			return ErrScriptExists
		}
		res, err := tx.Exec(`UPDATE sieve_scripts SET name = ?, updated = ? WHERE storage_id = ? AND name = ?`, to, s.now().Unix(), id, from)
		if err != nil {
			return err
		}
		if k, _ := res.RowsAffected(); k == 0 {
			return ErrNoScript
		}
		return nil
	})
}

// SieveReplied records that sender gets an out-of-office reply of handle
// now, unless one was sent within period: it returns true when the reply
// may be sent (and is recorded), false when one was sent already.
func (s *Store) SieveReplied(ctx context.Context, id, handle, sender string, period time.Duration, now time.Time) (bool, error) {
	ok := false
	err := s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		var until int64
		err := tx.QueryRow(`SELECT until FROM sieve_replies WHERE storage_id = ? AND handle = ? AND sender = ?`, id, handle, sender).Scan(&until)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && until > now.Unix() {
			return nil
		}
		ok = true
		_, err = tx.Exec(`INSERT INTO sieve_replies (storage_id, handle, sender, until) VALUES (?, ?, ?, ?)
			ON CONFLICT (storage_id, handle, sender) DO UPDATE SET until = excluded.until`,
			id, handle, sender, now.Add(period).Unix())
		return err
	})
	return ok, err
}

// ForgetSieveReply takes back what SieveReplied recorded (the reply could
// not be sent after all).
func (s *Store) ForgetSieveReply(ctx context.Context, id, handle, sender string) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		_, err := tx.Exec(`DELETE FROM sieve_replies WHERE storage_id = ? AND handle = ? AND sender = ?`, id, handle, sender)
		return err
	})
}

// SieveDuplicate tells whether the duplicate test saw key within period
// and records it: a new key is kept for period; a seen one is moved
// forward only with last (RFC 7352 :last).
func (s *Store) SieveDuplicate(ctx context.Context, id, key string, period time.Duration, last bool, now time.Time) (bool, error) {
	seen := false
	err := s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		var until int64
		err := tx.QueryRow(`SELECT until FROM sieve_duplicates WHERE storage_id = ? AND id = ?`, id, key).Scan(&until)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		seen = err == nil && until > now.Unix()
		if seen && !last {
			return nil
		}
		_, err = tx.Exec(`INSERT INTO sieve_duplicates (storage_id, id, until) VALUES (?, ?, ?)
			ON CONFLICT (storage_id, id) DO UPDATE SET until = excluded.until`, id, key, now.Add(period).Unix())
		return err
	})
	return seen, err
}

// PruneSieve forgets replies and duplicate ids whose time has passed.
func (s *Store) PruneSieve(ctx context.Context, now time.Time) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		if _, err := tx.Exec(`DELETE FROM sieve_replies WHERE until <= ?`, now.Unix()); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM sieve_duplicates WHERE until <= ?`, now.Unix())
		return err
	})
}
