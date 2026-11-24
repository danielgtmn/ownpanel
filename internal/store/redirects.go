package store

import (
	"context"
	"database/sql"
	"time"
)

// Redirect is one redirect. SourcePath "" makes it a domain redirect (the
// whole domain); otherwise it is a path redirect of the site SiteName.
//
// A domain redirect belongs to the person who created it (OwnerID); a path
// redirect to the owner of its site, so OwnerID is ignored when one is saved.
type Redirect struct {
	ID           int64
	OwnerID      int64  // 0: nobody (path redirect of a site without owner)
	Owner        string // the owner's user name, "" if none
	SiteName     string // "" for domain redirects
	SourceDomain string
	SourcePath   string
	Target       string
	Code         int
	KeepPath     bool
	KeepQuery    bool
	CreatedAt    time.Time
}

const redirectCols = `r.id, coalesce(st.owner_id, r.owner_id, 0), coalesce(u.name, ''), coalesce(r.site_name, ''),
	r.source_domain, r.source_path, r.target, r.code, r.keep_path, r.keep_query, r.created_at`

const redirectFrom = ` FROM redirects r LEFT JOIN sites st ON st.name = r.site_name
	LEFT JOIN users u ON u.id = coalesce(st.owner_id, r.owner_id)`

func scanRedirect(row interface{ Scan(...any) error }) (Redirect, error) {
	var r Redirect
	var created int64
	err := row.Scan(&r.ID, &r.OwnerID, &r.Owner, &r.SiteName, &r.SourceDomain, &r.SourcePath,
		&r.Target, &r.Code, &r.KeepPath, &r.KeepQuery, &created)
	if err != nil {
		if noRows(err) {
			return Redirect{}, notFoundf("redirect not found")
		}
		return Redirect{}, err
	}
	r.CreatedAt = unix(created)
	return r, nil
}

// Redirects returns every redirect, sorted by source domain and path.
func (s *Store) Redirects(ctx context.Context) ([]Redirect, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+redirectCols+redirectFrom+` ORDER BY r.source_domain, r.source_path, r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Redirect{}
	for rows.Next() {
		r, err := scanRedirect(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Redirect returns one redirect.
func (s *Store) Redirect(ctx context.Context, id int64) (Redirect, error) {
	return scanRedirect(s.db.QueryRowContext(ctx, `SELECT `+redirectCols+redirectFrom+` WHERE r.id = ?`, id))
}

// CreateRedirect stores a new redirect. The caller validates and normalizes
// it; the store makes sure that a domain redirect's domain is no site's and
// that a path redirect's domain belongs to its site, and that the owner has
// fewer than limit redirects (limit <= 0: no limit).
func (s *Store) CreateRedirect(ctx context.Context, r Redirect, limit int) (Redirect, error) {
	var out Redirect
	err := s.tx(ctx, func(tx *sql.Tx) error {
		owner, err := checkRedirect(tx, &r)
		if err != nil {
			return err
		}
		if err := underLimit(tx, owner, limit); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO redirects (owner_id, site_name, source_domain, source_path, target, code, keep_path, keep_query, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nullID(r.OwnerID), nullString(r.SiteName), r.SourceDomain, r.SourcePath, r.Target, r.Code, r.KeepPath, r.KeepQuery, s.now())
		if isUnique(err) {
			return redirectTaken(r)
		}
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = scanRedirect(tx.QueryRow(`SELECT `+redirectCols+redirectFrom+` WHERE r.id = ?`, id))
		return err
	})
	return out, err
}

// UpdateRedirect replaces every field of an existing redirect but its id and
// creation time, with the same checks as CreateRedirect. A change of owner
// is checked against limit.
func (s *Store) UpdateRedirect(ctx context.Context, r Redirect, limit int) (Redirect, error) {
	var out Redirect
	err := s.tx(ctx, func(tx *sql.Tx) error {
		cur, err := scanRedirect(tx.QueryRow(`SELECT `+redirectCols+redirectFrom+` WHERE r.id = ?`, r.ID))
		if err != nil {
			return err
		}
		owner, err := checkRedirect(tx, &r)
		if err != nil {
			return err
		}
		if cur.OwnerID != owner {
			if err := underLimit(tx, owner, limit); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`UPDATE redirects SET owner_id = ?, site_name = ?, source_domain = ?, source_path = ?, target = ?,
			code = ?, keep_path = ?, keep_query = ? WHERE id = ?`,
			nullID(r.OwnerID), nullString(r.SiteName), r.SourceDomain, r.SourcePath, r.Target, r.Code, r.KeepPath, r.KeepQuery, r.ID)
		if isUnique(err) {
			return redirectTaken(r)
		}
		if err != nil {
			return err
		}
		out, err = scanRedirect(tx.QueryRow(`SELECT `+redirectCols+redirectFrom+` WHERE r.id = ?`, r.ID))
		return err
	})
	return out, err
}

// DeleteRedirect removes a redirect.
func (s *Store) DeleteRedirect(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM redirects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("redirect not found")
	}
	return nil
}

// checkRedirect checks r against the sites and returns whose it is. For a
// path redirect it clears r.OwnerID: the owner is the site's.
func checkRedirect(tx *sql.Tx, r *Redirect) (int64, error) {
	var site string
	var siteOwner int64
	err := tx.QueryRow(`SELECT d.site_name, coalesce(s.owner_id, 0) FROM site_domains d JOIN sites s ON s.name = d.site_name
		WHERE d.domain = ?`, r.SourceDomain).Scan(&site, &siteOwner)
	if err != nil && !noRows(err) {
		return 0, err
	}
	if r.SourcePath == "" {
		if err == nil {
			return 0, &DomainTakenError{Domain: r.SourceDomain, Site: site}
		}
		r.SiteName = ""
		return r.OwnerID, nil
	}
	if err != nil || site != r.SiteName {
		return 0, invalidf("the domain %s is not a domain of the site %s", r.SourceDomain, r.SiteName)
	}
	r.OwnerID = 0
	return siteOwner, nil
}

// underLimit: the owner has fewer than limit redirects (limit <= 0: no
// limit). Path redirects count for the owner of their site.
func underLimit(tx *sql.Tx, owner int64, limit int) error {
	if limit <= 0 {
		return nil
	}
	var n int
	err := tx.QueryRow(`SELECT count(*) FROM redirects r LEFT JOIN sites st ON st.name = r.site_name
		WHERE coalesce(st.owner_id, r.owner_id) IS ?`, nullID(owner)).Scan(&n)
	if err != nil {
		return err
	}
	if n >= limit {
		return conflictf("at most %d redirects are possible per person", limit)
	}
	return nil
}

func redirectTaken(r Redirect) error {
	if r.SourcePath == "" {
		return conflictf("the domain %s already redirects", r.SourceDomain)
	}
	return conflictf("there is already a redirect for %s%s", r.SourceDomain, r.SourcePath)
}

func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id != 0} }

func nullString(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }

// domainRedirects refuses a site domain that a domain redirect uses.
func domainRedirects(tx *sql.Tx, domain string) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM redirects WHERE source_domain = ? AND source_path = ''`, domain).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return invalidf("the domain %s redirects elsewhere; remove that redirect first", domain)
	}
	return nil
}

// dropStrayPathRedirects removes the path redirects of a site on domains it
// no longer has.
func dropStrayPathRedirects(tx *sql.Tx, site string) error {
	_, err := tx.Exec(`DELETE FROM redirects WHERE site_name = ?
		AND source_domain NOT IN (SELECT domain FROM site_domains WHERE site_name = ?)`, site, site)
	return err
}

func ownsNoDomainRedirects(tx *sql.Tx, userID int64, name string) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM redirects WHERE owner_id = ? AND site_name IS NULL`, userID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return conflictf("%s still owns domain redirects; delete them first", name)
	}
	return nil
}
