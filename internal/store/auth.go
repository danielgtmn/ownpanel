package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Token is a personal access token. Only the hash of its secret is kept.
type Token struct {
	ID     int64
	UserID int64
	Name   string
	Hash   string
	Access string // "read" or "write"
	// Sites limits the token to these sites; nil means every site the user
	// may use.
	Sites      []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	// ExpiresAt is when the token stops working; nil means never.
	ExpiresAt *time.Time
}

const tokenCols = `id, user_id, name, hash, access, sites, created_at, last_used_at, expires_at`

func scanToken(row interface{ Scan(...any) error }) (Token, error) {
	var t Token
	var sites sql.NullString
	var created int64
	var used, expires sql.NullInt64
	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Hash, &t.Access, &sites, &created, &used, &expires); err != nil {
		if noRows(err) {
			return Token{}, notFoundf("access token not found")
		}
		return Token{}, err
	}
	if sites.Valid {
		t.Sites = []string{}
		if err := json.Unmarshal([]byte(sites.String), &t.Sites); err != nil {
			return Token{}, err
		}
	}
	t.CreatedAt = unix(created)
	if used.Valid {
		at := unix(used.Int64)
		t.LastUsedAt = &at
	}
	if expires.Valid {
		at := unix(expires.Int64)
		t.ExpiresAt = &at
	}
	return t, nil
}

func sitesJSON(sites []string) (sql.NullString, error) {
	if sites == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(sites)
	return sql.NullString{String: string(b), Valid: true}, err
}

// CreateToken stores a token and returns it with id and creation time.
func (s *Store) CreateToken(ctx context.Context, t Token) (Token, error) {
	sites, err := sitesJSON(t.Sites)
	if err != nil {
		return Token{}, err
	}
	created := s.now()
	var expires sql.NullInt64
	if t.ExpiresAt != nil {
		expires = sql.NullInt64{Int64: t.ExpiresAt.Unix(), Valid: true}
		at := unix(expires.Int64)
		t.ExpiresAt = &at
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO tokens (user_id, name, hash, access, sites, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.UserID, t.Name, t.Hash, t.Access, sites, created, expires)
	if err != nil {
		return Token{}, err
	}
	t.ID, err = res.LastInsertId()
	t.CreatedAt = unix(created)
	return t, err
}

// Tokens lists a user's tokens, newest first.
func (s *Store) Tokens(ctx context.Context, userID int64) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TokenByHash returns a token and its user.
func (s *Store) TokenByHash(ctx context.Context, hash string) (Token, User, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE hash = ?`, hash))
	if err != nil {
		return Token{}, User{}, err
	}
	u, err := s.User(ctx, t.UserID)
	return t, u, err
}

// DeleteToken removes a token of the given user.
func (s *Store) DeleteToken(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("access token not found")
	}
	return nil
}

// TouchToken records a use of a token, at most once per minute so reads do
// not turn into a write each.
func (s *Store) TouchToken(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at <= ?)`,
		now.Unix(), id, now.Add(-time.Minute).Unix())
	return err
}
