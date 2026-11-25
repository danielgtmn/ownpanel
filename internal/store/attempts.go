package store

import (
	"context"
	"database/sql"
	"time"
)

// AddAttempt records an attempt for key at now unless key already has max
// attempts after now-window. Attempts at or before now-window (for any key)
// are removed first. It returns the id of the new attempt, or 0 and the time
// of the oldest attempt still in the window when key is blocked.
func (s *Store) AddAttempt(ctx context.Context, key string, now time.Time, window time.Duration, max int) (int64, time.Time, error) {
	var id int64
	var oldest time.Time
	err := s.tx(ctx, func(tx *sql.Tx) error {
		cutoff := now.Add(-window).Unix()
		if _, err := tx.Exec(`DELETE FROM login_attempts WHERE at <= ?`, cutoff); err != nil {
			return err
		}
		var n int
		var first sql.NullInt64
		if err := tx.QueryRow(`SELECT count(*), min(at) FROM login_attempts WHERE key = ?`, key).Scan(&n, &first); err != nil {
			return err
		}
		if n >= max {
			oldest = unix(first.Int64)
			return nil
		}
		res, err := tx.Exec(`INSERT INTO login_attempts (key, at) VALUES (?, ?)`, key, now.Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, oldest, err
}

// DeleteAttempt removes one attempt by id.
func (s *Store) DeleteAttempt(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE id = ?`, id)
	return err
}

// CountAttempts returns how many attempts are stored.
func (s *Store) CountAttempts(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM login_attempts`).Scan(&n)
	return n, err
}

// DeleteAttempts removes every attempt for key.
func (s *Store) DeleteAttempts(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE key = ?`, key)
	return err
}
