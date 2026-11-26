package store

import (
	"context"
	"time"
)

// Domain is a domain a person entered by hand; its DNS is managed elsewhere.
type Domain struct {
	Domain    string
	CreatedAt time.Time
}

// AddDomain records a domain for a user. It reports whether the domain was
// new; adding it again changes nothing.
func (s *Store) AddDomain(ctx context.Context, userID int64, domain string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO domains (user_id, domain, created_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id, domain) DO NOTHING`, userID, domain, s.now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RemoveDomain forgets a domain of a user.
func (s *Store) RemoveDomain(ctx context.Context, userID int64, domain string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM domains WHERE user_id = ? AND domain = ?`, userID, domain)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("the domain %s is not in your panel", domain)
	}
	return nil
}

// Domains lists the domains of one user, ordered by name.
func (s *Store) Domains(ctx context.Context, userID int64) ([]Domain, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain, created_at FROM domains WHERE user_id = ? ORDER BY domain`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		var d Domain
		var created int64
		if err := rows.Scan(&d.Domain, &created); err != nil {
			return nil, err
		}
		d.CreatedAt = unix(created)
		out = append(out, d)
	}
	return out, rows.Err()
}
