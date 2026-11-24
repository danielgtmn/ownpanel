package store

import (
	"context"
	"time"
)

// Activity is one entry of the activity log.
type Activity struct {
	ID     int64
	At     time.Time
	Actor  string
	Action string
	Target string
	Detail string
}

// AddActivity appends an entry; At is set to now.
func (s *Store) AddActivity(ctx context.Context, a Activity) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO activity (at, actor, action, target, detail) VALUES (?, ?, ?, ?, ?)`,
		s.now(), a.Actor, a.Action, a.Target, a.Detail)
	return err
}

// Activities returns the newest entries first.
func (s *Store) Activities(ctx context.Context, limit int) ([]Activity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, actor, action, target, detail FROM activity ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		var at int64
		if err := rows.Scan(&a.ID, &at, &a.Actor, &a.Action, &a.Target, &a.Detail); err != nil {
			return nil, err
		}
		a.At = unix(at)
		out = append(out, a)
	}
	return out, rows.Err()
}
