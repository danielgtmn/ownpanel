package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
)

// Site is one hosted static site.
type Site struct {
	Name    string
	Domains []string
	OwnerID int64  // 0 while nobody owns it (imported before the first admin)
	Owner   string // the owner's user name, "" if none
}

// Sites returns all sites sorted by name.
func (s *Store) Sites(ctx context.Context) ([]Site, error) {
	return querySites(ctx, s.db, "")
}

// Site returns one site.
func (s *Store) Site(ctx context.Context, name string) (Site, error) {
	return siteIn(ctx, s.db, name)
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func siteIn(ctx context.Context, q querier, name string) (Site, error) {
	list, err := querySites(ctx, q, name)
	if err != nil {
		return Site{}, err
	}
	if len(list) == 0 {
		return Site{}, notFoundf("site not found")
	}
	return list[0], nil
}

// querySites lists all sites, or only the named one if name is not empty.
func querySites(ctx context.Context, q querier, name string) ([]Site, error) {
	rows, err := q.QueryContext(ctx, `SELECT s.name, coalesce(s.owner_id, 0), coalesce(u.name, '')
		FROM sites s LEFT JOIN users u ON u.id = s.owner_id
		WHERE ? = '' OR s.name = ? ORDER BY s.name`, name, name)
	if err != nil {
		return nil, err
	}
	out := []Site{}
	index := map[string]int{}
	for rows.Next() {
		var site Site
		if err := rows.Scan(&site.Name, &site.OwnerID, &site.Owner); err != nil {
			rows.Close()
			return nil, err
		}
		site.Domains = []string{}
		index[site.Name] = len(out)
		out = append(out, site)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, `SELECT site_name, domain FROM site_domains
		WHERE ? = '' OR site_name = ? ORDER BY domain`, name, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var site, domain string
		if err := rows.Scan(&site, &domain); err != nil {
			return nil, err
		}
		if i, ok := index[site]; ok {
			out[i].Domains = append(out[i].Domains, domain)
		}
	}
	return out, rows.Err()
}

// CreateSite adds a site owned by ownerID (0 for nobody). Domains are
// normalized and must not belong to another site.
func (s *Store) CreateSite(ctx context.Context, name string, domains []string, ownerID int64) (Site, error) {
	if err := ValidateName(name); err != nil {
		return Site{}, err
	}
	clean, err := NormalizeDomains(domains)
	if err != nil {
		return Site{}, err
	}
	var site Site
	err = s.tx(ctx, func(tx *sql.Tx) error {
		owner := sql.NullInt64{Int64: ownerID, Valid: ownerID != 0}
		_, err := tx.Exec(`INSERT INTO sites (name, owner_id, created_at) VALUES (?, ?, ?)`, name, owner, s.now())
		if isUnique(err) {
			return conflictf("the site name %q is already taken", name)
		}
		if err != nil {
			return err
		}
		if err := replaceDomains(tx, name, clean); err != nil {
			return err
		}
		site, err = siteIn(ctx, tx, name)
		return err
	})
	return site, err
}

// SetDomains replaces the domains of an existing site.
func (s *Store) SetDomains(ctx context.Context, name string, domains []string) (Site, error) {
	clean, err := NormalizeDomains(domains)
	if err != nil {
		return Site{}, err
	}
	var site Site
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := siteIn(ctx, tx, name); err != nil {
			return err
		}
		if err := replaceDomains(tx, name, clean); err != nil {
			return err
		}
		site, err = siteIn(ctx, tx, name)
		return err
	})
	return site, err
}

func replaceDomains(tx *sql.Tx, name string, domains []string) error {
	for _, d := range domains {
		var other string
		err := tx.QueryRow(`SELECT site_name FROM site_domains WHERE domain = ? AND site_name != ?`, d, name).Scan(&other)
		if err == nil {
			return &DomainTakenError{Domain: d, Site: other}
		}
		if !noRows(err) {
			return err
		}
		if err := domainRedirects(tx, d); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM site_domains WHERE site_name = ?`, name); err != nil {
		return err
	}
	for _, d := range domains {
		if _, err := tx.Exec(`INSERT INTO site_domains (domain, site_name) VALUES (?, ?)`, d, name); err != nil {
			return err
		}
	}
	return dropStrayPathRedirects(tx, name)
}

// DeleteSite removes a site and its domains, and drops it from the site
// lists of access tokens, so a later site with the same name is not covered
// by an old token.
func (s *Store) DeleteSite(ctx context.Context, name string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM sites WHERE name = ?`, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFoundf("site not found")
		}
		rows, err := tx.Query(`SELECT id, sites FROM tokens WHERE sites IS NOT NULL`)
		if err != nil {
			return err
		}
		updates := map[int64]string{}
		for rows.Next() {
			var id int64
			var raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			var list []string
			if err := json.Unmarshal([]byte(raw), &list); err != nil {
				rows.Close()
				return err
			}
			if i := slices.Index(list, name); i >= 0 {
				b, _ := json.Marshal(slices.Delete(list, i, i+1))
				updates[id] = string(b)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for id, list := range updates {
			if _, err := tx.Exec(`UPDATE tokens SET sites = ? WHERE id = ?`, list, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ImportStateFile moves the sites of the former JSON state file into the
// database, once: only if the database has no sites yet. The file is renamed
// to <file>.imported afterwards. Imported sites belong to the first admin, or
// to nobody until an admin exists. It reports how many sites were imported.
func (s *Store) ImportStateFile(ctx context.Context, file string) (int, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var list []struct {
		Name    string   `json:"name"`
		Domains []string `json:"domains"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return 0, fmt.Errorf("parse %s: %w", file, err)
	}
	existing, err := s.Sites(ctx)
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		return 0, nil
	}
	var admin int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE role = ? ORDER BY id LIMIT 1`, RoleAdmin).Scan(&admin)
	if err != nil && !noRows(err) {
		return 0, err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		for _, site := range list {
			if err := ValidateName(site.Name); err != nil {
				return err
			}
			clean, err := NormalizeDomains(site.Domains)
			if err != nil {
				return err
			}
			owner := sql.NullInt64{Int64: admin, Valid: admin != 0}
			if _, err := tx.Exec(`INSERT INTO sites (name, owner_id, created_at) VALUES (?, ?, ?)`, site.Name, owner, s.now()); err != nil {
				return err
			}
			if err := replaceDomains(tx, site.Name, clean); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("import %s: %w", file, err)
	}
	return len(list), os.Rename(file, file+".imported")
}
