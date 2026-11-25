package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Greylist rules: a triplet seen for the first time is delayed; once its
// sender retries after Delay (and within Retry), it passes for Valid after
// its last pass.
type Greylist struct {
	Delay time.Duration
	Retry time.Duration
	Valid time.Duration
}

// GreylistCheck records an attempt of the triplet key at now and tells
// whether it may pass.
func (s *Store) GreylistCheck(ctx context.Context, key string, now time.Time, g Greylist) (bool, error) {
	pass := false
	err := s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		var first, passed int64
		err := tx.QueryRow(`SELECT first, passed FROM greylist WHERE key = ?`, key).Scan(&first, &passed)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.Exec(`INSERT INTO greylist (key, first) VALUES (?, ?)`, key, now.Unix())
			return err
		}
		if err != nil {
			return err
		}
		t := now.Unix()
		switch {
		case passed > 0 && t-passed <= int64(g.Valid/time.Second):
			pass = true
		case passed == 0 && t-first >= int64(g.Delay/time.Second) && t-first <= int64(g.Retry/time.Second):
			pass = true
		case passed > 0 || t-first > int64(g.Retry/time.Second):
			// Expired: start over.
			_, err = tx.Exec(`UPDATE greylist SET first = ?, passed = 0 WHERE key = ?`, t, key)
			return err
		default:
			return nil // still waiting
		}
		_, err = tx.Exec(`UPDATE greylist SET passed = ? WHERE key = ?`, t, key)
		return err
	})
	return pass, err
}

// PruneGreylist deletes triplets that neither passed nor were seen since
// before.
func (s *Store) PruneGreylist(ctx context.Context, before time.Time) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		_, err := tx.Exec(`DELETE FROM greylist WHERE max(first, passed) < ?`, before.Unix())
		return err
	})
}
