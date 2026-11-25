package store

import (
	"context"
	"database/sql"
	"time"
)

// Moving sign-in to the auth tables: panel users created before it have a
// password hash and sessions here and no auth user yet. The panel moves them
// once at start (internal/panel, moveUsersToAuth); these are the reads and
// the final step it needs.

// UnmovedUser is a user without an auth user yet, with the password hash
// still kept here.
type UnmovedUser struct {
	ID           int64
	Name         string
	PasswordHash string
}

// UnmovedUsers lists users that have no auth user yet.
func (s *Store) UnmovedUsers(ctx context.Context) ([]UnmovedUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, password_hash FROM users WHERE auth_user_id IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnmovedUser
	for rows.Next() {
		var u UnmovedUser
		if err := rows.Scan(&u.ID, &u.Name, &u.PasswordHash); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// OldSession is a session kept here before sign-in moved. ID is the hex
// SHA-256 of the cookie value, as in the auth tables.
type OldSession struct {
	ID        string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// OldSessions lists a user's sessions here that have not expired at now.
func (s *Store) OldSessions(ctx context.Context, userID int64, now time.Time) ([]OldSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at, expires_at FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY created_at`,
		userID, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OldSession
	for rows.Next() {
		var o OldSession
		var created, expires int64
		if err := rows.Scan(&o.ID, &created, &expires); err != nil {
			return nil, err
		}
		o.CreatedAt, o.ExpiresAt = unix(created), unix(expires)
		out = append(out, o)
	}
	return out, rows.Err()
}

// LinkAuthUser links a user to its auth user, forgets the password hash
// kept here and removes the user's sessions here (they were copied). It
// fails with ErrConflict when the user is linked already.
func (s *Store) LinkAuthUser(ctx context.Context, id int64, authUserID string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE users SET auth_user_id = ?, password_hash = '' WHERE id = ? AND auth_user_id IS NULL`, authUserID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return conflictf("the user is linked already")
		}
		_, err = tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, id)
		return err
	})
}

// AuthUserIDs returns the auth user ids that panel users are linked to.
func (s *Store) AuthUserIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT auth_user_id FROM users WHERE auth_user_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// DB is the database, for the sign-in tables (auth_*) that live next to
// the panel's own. The store allows one connection at a time: never use
// the store while a transaction on DB is open, or the other way round.
func (s *Store) DB() *sql.DB { return s.db }
