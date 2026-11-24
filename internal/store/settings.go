package store

import (
	"context"
	"database/sql"
	"maps"
	"slices"
)

// Settings returns every stored setting by key.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// PutSettings stores several settings at once, atomically. An empty value
// removes the key.
func (s *Store) PutSettings(ctx context.Context, values map[string]string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, k := range slices.Sorted(maps.Keys(values)) {
			var err error
			if v := values[k]; v == "" {
				_, err = tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, k)
			} else {
				_, err = tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
					ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, v)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}
