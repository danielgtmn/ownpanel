package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/danielgtmn/ablage"
)

// Redirect is one redirect. SourcePath "" makes it a domain redirect (the
// whole domain); otherwise it is a path redirect of the site SiteName.
//
// A domain redirect belongs to the person who created it (OwnerID); a path
// redirect to the owner of its site, so OwnerID is ignored when one is saved.
type Redirect struct {
	ID           string
	OwnerID      string // "": nobody (path redirect of a site without owner)
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

var errRedirectNotFound = notFoundf("redirect not found")

// redirectsOf completes rows with the owners: the site's owner for a path
// redirect, the creator for a domain redirect (one query for the sites, one
// for the users).
func redirectsOf(ctx context.Context, r repos, rows []redirectRow) ([]Redirect, error) {
	var siteNames []string
	for _, row := range rows {
		if row.SiteName != nil {
			siteNames = append(siteNames, *row.SiteName)
		}
	}
	siteOwner := map[string]string{}
	if len(siteNames) > 0 {
		sites, err := r.sites.GetMany(ctx, unique(siteNames)...)
		if err != nil {
			return nil, err
		}
		for _, st := range sites {
			siteOwner[st.ID] = deref(st.OwnerID)
		}
	}
	out := make([]Redirect, len(rows))
	owners := make([]string, len(rows))
	for i, row := range rows {
		owner := deref(row.OwnerID)
		if row.SiteName != nil {
			// coalesce(site owner, own owner), as before ablage
			if o := siteOwner[*row.SiteName]; o != "" {
				owner = o
			}
		}
		owners[i] = owner
		out[i] = Redirect{ID: row.ID, OwnerID: owner, SiteName: deref(row.SiteName), SourceDomain: row.SourceDomain,
			SourcePath: row.SourcePath, Target: row.Target, Code: row.Code, KeepPath: row.KeepPath, KeepQuery: row.KeepQuery,
			CreatedAt: row.CreatedAt}
	}
	names, err := userNames(ctx, r, owners)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Owner = names[out[i].OwnerID]
	}
	return out, nil
}

func getRedirect(ctx context.Context, r repos, id string) (Redirect, error) {
	row, err := r.redirects.Get(ctx, id)
	if err != nil {
		return Redirect{}, missing(err, errRedirectNotFound)
	}
	list, err := redirectsOf(ctx, r, []redirectRow{row})
	if err != nil {
		return Redirect{}, err
	}
	return list[0], nil
}

// Redirects returns every redirect, sorted by source domain and path.
func (s *Store) Redirects(ctx context.Context) ([]Redirect, error) {
	rows, err := s.r.redirects.Find(ctx, ablage.Query{OrderBy: []ablage.Order{ablage.Asc("source_domain"), ablage.Asc("source_path")}})
	if err != nil {
		return nil, err
	}
	return redirectsOf(ctx, s.r, rows)
}

// Redirect returns one redirect.
func (s *Store) Redirect(ctx context.Context, id string) (Redirect, error) {
	return getRedirect(ctx, s.r, id)
}

func redirectRowOf(r Redirect) redirectRow {
	return redirectRow{ID: r.ID, OwnerID: optional(r.OwnerID), SiteName: optional(r.SiteName), SourceDomain: r.SourceDomain,
		SourcePath: r.SourcePath, Target: r.Target, Code: r.Code, KeepPath: r.KeepPath, KeepQuery: r.KeepQuery}
}

// CreateRedirect stores a new redirect. The caller validates and normalizes
// it; the store makes sure that a domain redirect's domain is no site's and
// that a path redirect's domain belongs to its site, and that the owner has
// fewer than limit redirects (limit <= 0: no limit).
func (s *Store) CreateRedirect(ctx context.Context, r Redirect, limit int) (Redirect, error) {
	var out Redirect
	err := s.tx(ctx, func(rp repos) error {
		owner, err := checkRedirect(ctx, rp, &r)
		if err != nil {
			return err
		}
		if err := underLimit(ctx, rp, owner, limit); err != nil {
			return err
		}
		row := redirectRowOf(r)
		row.ID, row.CreatedAt = "", s.now()
		if err := rp.redirects.Create(ctx, &row); err != nil {
			return redirectWriteErr(err, r)
		}
		out, err = getRedirect(ctx, rp, row.ID)
		return err
	})
	return out, err
}

// UpdateRedirect replaces every field of an existing redirect but its id and
// creation time, with the same checks as CreateRedirect. A change of owner
// is checked against limit.
func (s *Store) UpdateRedirect(ctx context.Context, r Redirect, limit int) (Redirect, error) {
	var out Redirect
	err := s.tx(ctx, func(rp repos) error {
		cur, err := getRedirect(ctx, rp, r.ID)
		if err != nil {
			return err
		}
		owner, err := checkRedirect(ctx, rp, &r)
		if err != nil {
			return err
		}
		if cur.OwnerID != owner {
			if err := underLimit(ctx, rp, owner, limit); err != nil {
				return err
			}
		}
		row := redirectRowOf(r)
		if err := rp.redirects.Patch(ctx, r.ID, ablage.Row{
			"owner_id": row.OwnerID, "site_name": row.SiteName, "source_domain": row.SourceDomain, "source_path": row.SourcePath,
			"target": row.Target, "code": row.Code, "keep_path": row.KeepPath, "keep_query": row.KeepQuery,
		}); err != nil {
			return redirectWriteErr(err, r)
		}
		out, err = getRedirect(ctx, rp, r.ID)
		return err
	})
	return out, err
}

func redirectWriteErr(err error, r Redirect) error {
	switch {
	case errors.Is(err, ablage.ErrDuplicate):
		return redirectTaken(r)
	case errors.Is(err, ablage.ErrReference):
		return errUserNotFound
	}
	return err
}

// DeleteRedirect removes a redirect.
func (s *Store) DeleteRedirect(ctx context.Context, id string) error {
	return missing(s.r.redirects.Delete(ctx, id), errRedirectNotFound)
}

// redirectCodes are the status codes a redirect may answer with.
var redirectCodes = []int{301, 302, 307, 308}

// checkRedirect checks r against the sites and returns whose it is. For a
// path redirect it clears r.OwnerID: the owner is the site's. It also keeps
// what the table checked before ablage: a known status code, and a site
// exactly for path redirects.
func checkRedirect(ctx context.Context, rp repos, r *Redirect) (string, error) {
	if !slices.Contains(redirectCodes, r.Code) {
		return "", invalidf("the status code must be 301, 302, 307 or 308, not %d", r.Code)
	}
	var site, siteOwner string
	found := false
	d, err := rp.siteDomains.Get(ctx, r.SourceDomain)
	switch {
	case err == nil:
		st, err := rp.sites.Get(ctx, d.SiteName)
		if err != nil {
			return "", err
		}
		site, siteOwner, found = st.ID, deref(st.OwnerID), true
	case !errors.Is(err, ablage.ErrNotFound):
		return "", err
	}
	if r.SourcePath == "" {
		if found {
			return "", &DomainTakenError{Domain: r.SourceDomain, Site: site}
		}
		r.SiteName = ""
		return r.OwnerID, nil
	}
	if !found || site != r.SiteName {
		return "", invalidf("the domain %s is not a domain of the site %s", r.SourceDomain, r.SiteName)
	}
	r.OwnerID = ""
	return siteOwner, nil
}

// underLimit: the owner has fewer than limit redirects (limit <= 0: no
// limit). Path redirects count for the owner of their site ("" counts the
// redirects without owner).
func underLimit(ctx context.Context, r repos, owner string, limit int) error {
	if limit <= 0 {
		return nil
	}
	n, err := countRedirectsOf(ctx, r, owner)
	if err != nil {
		return err
	}
	if n >= limit {
		return conflictf("at most %d redirects are possible per person", limit)
	}
	return nil
}

// countRedirectsOf counts the redirects whose owner is owner ("": none),
// as redirectsOf decides it: the site's owner for a path redirect, else
// the redirect's own. Candidates are loaded in one query, their sites in
// another.
func countRedirectsOf(ctx context.Context, r repos, owner string) (int, error) {
	own := ablage.Null("owner_id")
	if owner != "" {
		own = ablage.Eq("owner_id", owner)
	}
	sites, err := r.sites.Find(ctx, ablage.Query{Where: own})
	if err != nil {
		return 0, err
	}
	names := make([]string, len(sites))
	for i, st := range sites {
		names[i] = st.ID
	}
	rows, err := r.redirects.Find(ctx, ablage.Query{Where: ablage.Or(own, ablage.In("site_name", names...))})
	if err != nil {
		return 0, err
	}
	list, err := redirectsOf(ctx, r, rows)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range list {
		if x.OwnerID == owner {
			n++
		}
	}
	return n, nil
}

func redirectTaken(r Redirect) error {
	if r.SourcePath == "" {
		return conflictf("the domain %s already redirects", r.SourceDomain)
	}
	return conflictf("there is already a redirect for %s%s", r.SourceDomain, r.SourcePath)
}

// domainRedirects refuses site domains that a domain redirect uses.
func domainRedirects(ctx context.Context, r repos, domains []string) error {
	list, err := r.redirects.Find(ctx, ablage.Query{
		Where:   ablage.And(ablage.In("source_domain", domains...), ablage.Eq("source_path", "")),
		OrderBy: []ablage.Order{ablage.Asc("source_domain")},
		Limit:   1,
	})
	if err != nil {
		return err
	}
	if len(list) > 0 {
		return invalidf("the domain %s redirects elsewhere; remove that redirect first", list[0].SourceDomain)
	}
	return nil
}

// dropStrayPathRedirects removes the path redirects of a site on domains it
// no longer has (domains are the ones it has now).
func dropStrayPathRedirects(ctx context.Context, r repos, site string, domains []string) error {
	_, err := r.redirects.DeleteWhere(ctx, ablage.And(ablage.Eq("site_name", site), ablage.Not(ablage.In("source_domain", domains...))))
	return err
}

func ownsNoDomainRedirects(ctx context.Context, r repos, userID, name string) error {
	n, err := r.redirects.Count(ctx, ablage.And(ablage.Eq("owner_id", userID), ablage.Null("site_name")))
	if err != nil {
		return err
	}
	if n > 0 {
		return conflictf("%s still owns domain redirects; delete them first", name)
	}
	return nil
}
