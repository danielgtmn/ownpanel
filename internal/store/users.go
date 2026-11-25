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

// User is a person who can sign in. How they sign in (password, sessions,
// e-mail address) is kept in the auth_* tables under AuthUserID.
type User struct {
	ID         int64
	Name       string
	Role       string
	AuthUserID string // "" until the user was moved to the auth tables
	CreatedAt  time.Time
}

const userCols = `id, name, role, auth_user_id, created_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created int64
	var authID sql.NullString
	if err := row.Scan(&u.ID, &u.Name, &u.Role, &authID, &created); err != nil {
		if noRows(err) {
			return User{}, notFoundf("user not found")
		}
		return User{}, err
	}
	u.AuthUserID = authID.String
	u.CreatedAt = unix(created)
	return u, nil
}

// CountUsers returns how many users exist.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser adds a user linked to the auth user authUserID. When firstOnly is set it fails with ErrConflict if
// any user exists already, atomically. A new admin takes over sites that
// have no owner (imported before any user existed).
func (s *Store) CreateUser(ctx context.Context, name, authUserID, role string, firstOnly bool) (User, error) {
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
		// The password lives in the auth tables; password_hash stays empty.
		res, err := tx.Exec(`INSERT INTO users (name, password_hash, role, auth_user_id, created_at) VALUES (?, '', ?, ?, ?)`,
			name, role, nullString(authUserID), s.now())
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

// UserByAuthID returns the user linked to an auth user.
func (s *Store) UserByAuthID(ctx context.Context, authUserID string) (User, error) {
	if authUserID == "" {
		return User{}, notFoundf("user not found")
	}
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE auth_user_id = ?`, authUserID))
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

// DeleteUser removes a user with their sessions, tokens and DNS
// connections. It refuses while the user owns sites, domain redirects or
// mail domains, and for the last admin.
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
		if err := ownsNoDomainRedirects(tx, id, u.Name); err != nil {
			return err
		}
		if err := ownsNoMailDomains(tx, id, u.Name); err != nil {
			return err
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
