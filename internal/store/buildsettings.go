package store

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtmn/ablage"
)

// SiteBuildSettings is how a site is built (#114). Variables is sealed by
// the panel; the store never sees the values in the clear.
type SiteBuildSettings struct {
	Site      string
	Command   string
	Output    string
	Image     string
	Variables []byte
	UpdatedBy string // user name, "" if the user was removed
	UpdatedAt time.Time
}

var errNoBuildSettings = notFoundf("the site has no build settings")

// SiteBuildSettings returns a site's build settings; ErrNotFound if it has
// none.
func (s *Store) SiteBuildSettings(ctx context.Context, site string) (SiteBuildSettings, error) {
	row, err := s.r.buildSets.First(ctx, ablage.Query{Where: ablage.Eq("site_name", site)})
	if err != nil {
		return SiteBuildSettings{}, missing(err, errNoBuildSettings)
	}
	names, err := userNames(ctx, s.r, []string{deref(row.UpdatedBy)})
	if err != nil {
		return SiteBuildSettings{}, err
	}
	return SiteBuildSettings{Site: row.SiteName, Command: row.Command, Output: row.Output, Image: row.Image,
		Variables: row.Variables, UpdatedBy: names[deref(row.UpdatedBy)], UpdatedAt: row.UpdatedAt}, nil
}

// PutSiteBuildSettings stores a site's build settings, replacing those it
// had. by is the id of the user who changes them ("" for none).
func (s *Store) PutSiteBuildSettings(ctx context.Context, b SiteBuildSettings, by string) (SiteBuildSettings, error) {
	row := siteBuildSettingsRow{SiteName: b.Site, Command: b.Command, Output: b.Output, Image: b.Image,
		Variables: b.Variables, UpdatedBy: optional(by), UpdatedAt: s.now()}
	err := s.tx(ctx, func(r repos) error {
		if ok, err := r.sites.Exists(ctx, b.Site); err != nil {
			return err
		} else if !ok {
			return errSiteNotFound
		}
		have, err := r.buildSets.First(ctx, ablage.Query{Where: ablage.Eq("site_name", b.Site)})
		switch {
		case errors.Is(err, ablage.ErrNotFound):
			err = r.buildSets.Create(ctx, &row)
		case err != nil:
			return err
		default:
			row.ID = have.ID
			err = r.buildSets.Replace(ctx, &row)
		}
		if errors.Is(err, ablage.ErrReference) {
			return notFoundf("the user is gone")
		}
		return err
	})
	if err != nil {
		return SiteBuildSettings{}, err
	}
	return s.SiteBuildSettings(ctx, b.Site)
}
