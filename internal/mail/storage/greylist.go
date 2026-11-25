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
	// PerNetwork bounds the triplets of one network that have not passed
	// yet, Max all triplets; 0 means no bound. A new triplet beyond either
	// is not recorded and does not pass (fail closed) until pruning makes
	// room again.
	PerNetwork int
	Max        int
}

// greylistRefresh: a passing triplet's last pass is written at most this
// often, so known senders do not cost a write per message.
const greylistRefresh = time.Hour

type greyAction int

const (
	greyNone    greyAction = iota // nothing to write
	greyPass                      // record a pass now
	greyRestart                   // expired: start over
)

func greyDecide(first, passed int64, now time.Time, g Greylist) (bool, greyAction) {
	t := now.Unix()
	switch {
	case passed > 0 && t-passed <= int64(g.Valid/time.Second):
		if t-passed >= int64(greylistRefresh/time.Second) {
			return true, greyPass
		}
		return true, greyNone
	case passed == 0 && t-first >= int64(g.Delay/time.Second) && t-first <= int64(g.Retry/time.Second):
		return true, greyPass
	case passed > 0 || t-first > int64(g.Retry/time.Second):
		return false, greyRestart
	default:
		return false, greyNone // still waiting
	}
}

// GreylistCheck records an attempt of the triplet key from network (the
// unit PerNetwork counts in) at now and tells whether it may pass. An
// attempt that changes nothing (still waiting, or passing again shortly
// after the last pass) does not write.
func (s *Store) GreylistCheck(ctx context.Context, key, network string, now time.Time, g Greylist) (bool, error) {
	var first, passed int64
	err := s.rdb.QueryRowContext(ctx, `SELECT first, passed FROM greylist WHERE key = ?`, key).Scan(&first, &passed)
	if err == nil {
		if pass, act := greyDecide(first, passed, now, g); act == greyNone {
			return pass, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	pass := false
	err = s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		err := tx.QueryRow(`SELECT first, passed FROM greylist WHERE key = ?`, key).Scan(&first, &passed)
		if errors.Is(err, sql.ErrNoRows) {
			if full, err := greyFull(tx, network, g); err != nil || full {
				return err
			}
			_, err = tx.Exec(`INSERT INTO greylist (key, net, first) VALUES (?, ?, ?)`, key, network, now.Unix())
			return err
		}
		if err != nil {
			return err
		}
		var act greyAction
		pass, act = greyDecide(first, passed, now, g)
		switch act {
		case greyPass:
			_, err = tx.Exec(`UPDATE greylist SET passed = ? WHERE key = ?`, now.Unix(), key)
		case greyRestart:
			_, err = tx.Exec(`UPDATE greylist SET first = ?, passed = 0 WHERE key = ?`, now.Unix(), key)
		}
		return err
	})
	return pass, err
}

// greyFull tells whether a new triplet of network would exceed g's bounds.
func greyFull(tx *sql.Tx, network string, g Greylist) (bool, error) {
	var n int
	if g.PerNetwork > 0 {
		if err := tx.QueryRow(`SELECT count(*) FROM greylist WHERE net = ? AND passed = 0`, network).Scan(&n); err != nil {
			return false, err
		}
		if n >= g.PerNetwork {
			return true, nil
		}
	}
	if g.Max > 0 {
		if err := tx.QueryRow(`SELECT count(*) FROM greylist`).Scan(&n); err != nil {
			return false, err
		}
		if n >= g.Max {
			return true, nil
		}
	}
	return false, nil
}

// PruneGreylist deletes triplets that never passed within g.Retry and
// passed ones whose last pass is older than g.Valid.
func (s *Store) PruneGreylist(ctx context.Context, now time.Time, g Greylist) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		_, err := tx.Exec(`DELETE FROM greylist WHERE (passed = 0 AND first < ?) OR (passed > 0 AND passed < ?)`,
			now.Add(-g.Retry).Unix(), now.Add(-g.Valid).Unix())
		return err
	})
}
