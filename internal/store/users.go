package store

import (
	"context"
	"database/sql"
	"time"
)

// Roles.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// User is a person who can sign in.
type User struct {
	ID           int64
	Name         string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
}

const userCols = `id, name, password_hash, role, created_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created int64
	if err := row.Scan(&u.ID, &u.Name, &u.PasswordHash, &u.Role, &created); err != nil {
		if noRows(err) {
			return User{}, notFoundf("user not found")
		}
		return User{}, err
	}
	u.CreatedAt = unix(created)
	return u, nil
}

// CountUsers returns how many users exist.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser adds a user. When firstOnly is set it fails with ErrConflict if
// any user exists already, atomically. A new admin takes over sites that
// have no owner (imported before any user existed).
func (s *Store) CreateUser(ctx context.Context, name, hash, role string, firstOnly bool) (User, error) {
	var u User
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if firstOnly {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return conflictf("the panel is already set up")
			}
		}
		res, err := tx.Exec(`INSERT INTO users (name, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
			name, hash, role, s.now())
		if isUnique(err) {
			return conflictf("the user name %q is already taken", name)
		}
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if role == RoleAdmin {
			if _, err := tx.Exec(`UPDATE sites SET owner_id = ? WHERE owner_id IS NULL`, id); err != nil {
				return err
			}
		}
		u, err = scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
		return err
	})
	return u, err
}

// User returns one user by id.
func (s *Store) User(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// UserByName returns one user by name.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE name = ?`, name))
}

// Users lists all users by name.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetRole changes a user's role. The last admin cannot be demoted.
func (s *Store) SetRole(ctx context.Context, id int64, role string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
		if err != nil {
			return err
		}
		if u.Role == RoleAdmin && role != RoleAdmin {
			if err := ensureOtherAdmin(tx, id, "the last admin cannot be made a member"); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, id)
		return err
	})
}

// SetPassword replaces a user's password hash and ends all of the user's
// sessions except keepSession (a session id, or "" for none).
func (s *Store) SetPassword(ctx context.Context, id int64, hash, keepSession string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFoundf("user not found")
		}
		_, err = tx.Exec(`DELETE FROM sessions WHERE user_id = ? AND id != ?`, id, keepSession)
		return err
	})
}

// DeleteUser removes a user with their sessions, tokens and DNS
// connections. It refuses while
// the user owns sites, and for the last admin.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
		if err != nil {
			return err
		}
		if u.Role == RoleAdmin {
			if err := ensureOtherAdmin(tx, id, "the last admin cannot be deleted"); err != nil {
				return err
			}
		}
		var sites int
		if err := tx.QueryRow(`SELECT count(*) FROM sites WHERE owner_id = ?`, id).Scan(&sites); err != nil {
			return err
		}
		if sites > 0 {
			return conflictf("%s still owns sites; delete them first", u.Name)
		}
		_, err = tx.Exec(`DELETE FROM users WHERE id = ?`, id)
		return err
	})
}

func ensureOtherAdmin(tx *sql.Tx, id int64, msg string) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM users WHERE role = ? AND id != ?`, RoleAdmin, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return conflictf("%s", msg)
	}
	return nil
}
