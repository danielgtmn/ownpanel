package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/danielgtmn/ablage"
)

// Site is one hosted static site.
type Site struct {
	Name    string
	Domains []string
	OwnerID string // "" while nobody owns it (imported before the first admin)
	Owner   string // the owner's user name, "" if none
}

var errSiteNotFound = notFoundf("site not found")

// Sites returns all sites sorted by name.
func (s *Store) Sites(ctx context.Context) ([]Site, error) {
	return querySites(ctx, s.r, ablage.All)
}

// Site returns one site.
func (s *Store) Site(ctx context.Context, name string) (Site, error) {
	return siteIn(ctx, s.r, name)
}

func siteIn(ctx context.Context, r repos, name string) (Site, error) {
	list, err := querySites(ctx, r, ablage.Eq("id", name))
	if err != nil {
		return Site{}, err
	}
	if len(list) == 0 {
		return Site{}, errSiteNotFound
	}
	return list[0], nil
}

// querySites lists the sites matching where, with their domains and owner
// names (one query each).
func querySites(ctx context.Context, r repos, where ablage.Cond) ([]Site, error) {
	rows, err := r.sites.Find(ctx, ablage.Query{Where: where})
	if err != nil {
		return nil, err
	}
	out := make([]Site, len(rows))
	index := map[string]int{}
	names := make([]string, len(rows))
	owners := make([]string, 0, len(rows))
	for i, row := range rows {
		out[i] = Site{Name: row.ID, Domains: []string{}, OwnerID: deref(row.OwnerID)}
		index[row.ID] = i
		names[i] = row.ID
		owners = append(owners, out[i].OwnerID)
	}
	if len(rows) == 0 {
		return out, nil
	}
	users, err := userNames(ctx, r, owners)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Owner = users[out[i].OwnerID]
	}
	domains, err := r.siteDomains.Find(ctx, ablage.Query{Where: ablage.In("site_name", names...)})
	if err != nil {
		return nil, err
	}
	for _, d := range domains {
		i := index[d.SiteName]
		out[i].Domains = append(out[i].Domains, d.ID)
	}
	return out, nil
}

// CreateSite adds a site owned by ownerID ("" for nobody). Domains are
// normalized and must not belong to another site.
func (s *Store) CreateSite(ctx context.Context, name string, domains []string, ownerID string) (Site, error) {
	if err := ValidateName(name); err != nil {
		return Site{}, err
	}
	clean, err := NormalizeDomains(domains)
	if err != nil {
		return Site{}, err
	}
	var site Site
	err = s.tx(ctx, func(r repos) error {
		if err := r.sites.Create(ctx, &siteRow{ID: name, OwnerID: optional(ownerID), CreatedAt: s.now()}); err != nil {
			if errors.Is(err, ablage.ErrReference) {
				return errUserNotFound
			}
			return dup(err, conflictf("the site name %q is already taken", name))
		}
		if err := replaceDomains(ctx, r, name, clean); err != nil {
			return err
		}
		site, err = siteIn(ctx, r, name)
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
	err = s.tx(ctx, func(r repos) error {
		if _, err := siteIn(ctx, r, name); err != nil {
			return err
		}
		if err := replaceDomains(ctx, r, name, clean); err != nil {
			return err
		}
		site, err = siteIn(ctx, r, name)
		return err
	})
	return site, err
}

func replaceDomains(ctx context.Context, r repos, name string, domains []string) error {
	taken, err := r.siteDomains.Find(ctx, ablage.Query{Where: ablage.And(ablage.In("id", domains...), ablage.Ne("site_name", name))})
	if err != nil {
		return err
	}
	if len(taken) > 0 {
		// The first in the list, as the domains are sorted.
		return &DomainTakenError{Domain: taken[0].ID, Site: taken[0].SiteName}
	}
	if err := domainRedirects(ctx, r, domains); err != nil {
		return err
	}
	if _, err := r.siteDomains.DeleteWhere(ctx, ablage.Eq("site_name", name)); err != nil {
		return err
	}
	for _, d := range domains {
		if err := r.siteDomains.Create(ctx, &siteDomainRow{ID: d, SiteName: name}); err != nil {
			return err
		}
	}
	return dropStrayPathRedirects(ctx, r, name, domains)
}

// DeleteSite removes a site and its domains, and drops it from the site
// lists of access tokens, so a later site with the same name is not covered
// by an old token.
func (s *Store) DeleteSite(ctx context.Context, name string) error {
	return s.tx(ctx, func(r repos) error {
		if err := r.sites.Delete(ctx, name); err != nil {
			return missing(err, errSiteNotFound)
		}
		return dropSiteFromTokens(ctx, r, name)
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
	n, err := s.r.sites.Count(ctx, ablage.All)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}
	err = s.tx(ctx, func(r repos) error {
		var owner *string
		admin, err := r.users.First(ctx, ablage.Query{Where: ablage.Eq("role", RoleAdmin), OrderBy: []ablage.Order{ablage.Asc("created_at")}})
		switch {
		case err == nil:
			owner = &admin.ID
		case !errors.Is(err, ablage.ErrNotFound):
			return err
		}
		for _, site := range list {
			if err := ValidateName(site.Name); err != nil {
				return err
			}
			clean, err := NormalizeDomains(site.Domains)
			if err != nil {
				return err
			}
			if err := r.sites.Create(ctx, &siteRow{ID: site.Name, OwnerID: owner, CreatedAt: s.now()}); err != nil {
				return err
			}
			if err := replaceDomains(ctx, r, site.Name, clean); err != nil {
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
