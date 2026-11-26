package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/danielgtmn/ablage"
)

// ---- organisations ----

// Organisation groups mail domains. Domains lists its domains, sorted;
// DomainOwners has the owner of each.
type Organisation struct {
	ID           string
	OwnerID      string
	Owner        string
	Name         string
	CreatedAt    time.Time
	Domains      []string
	DomainOwners map[string]string
}

var errOrgNotFound = notFoundf("organisation not found")

// compareNoCase orders like SQLite's COLLATE NOCASE: ASCII letters without
// case, everything else by bytes.
func compareNoCase(a, b string) int {
	fold := func(c byte) byte {
		if 'A' <= c && c <= 'Z' {
			return c + 'a' - 'A'
		}
		return c
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if x, y := fold(a[i]), fold(b[i]); x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return len(a) - len(b)
}

// organisationsOf completes rows with owner names and their domains (one
// query each).
func organisationsOf(ctx context.Context, r repos, rows []organisationRow) ([]Organisation, error) {
	ids := make([]string, len(rows))
	owners := make([]string, len(rows))
	for i, o := range rows {
		ids[i], owners[i] = o.ID, o.OwnerID
	}
	names, err := userNames(ctx, r, owners)
	if err != nil {
		return nil, err
	}
	out := make([]Organisation, len(rows))
	idx := map[string]int{}
	for i, o := range rows {
		out[i] = Organisation{ID: o.ID, OwnerID: o.OwnerID, Owner: names[o.OwnerID], Name: o.Name, CreatedAt: o.CreatedAt,
			Domains: []string{}, DomainOwners: map[string]string{}}
		idx[o.ID] = i
	}
	domains, err := r.mailDomains.Find(ctx, ablage.Query{Where: ablage.In("organisation_id", ids...)})
	if err != nil {
		return nil, err
	}
	for _, d := range domains {
		i := idx[*d.OrganisationID]
		out[i].Domains = append(out[i].Domains, d.ID)
		out[i].DomainOwners[d.ID] = d.OwnerID
	}
	return out, nil
}

// Organisations returns every organisation with its domains, sorted by name.
func (s *Store) Organisations(ctx context.Context) ([]Organisation, error) {
	rows, err := s.r.orgs.Find(ctx, ablage.Query{})
	if err != nil {
		return nil, err
	}
	// Names without case (as COLLATE NOCASE did), then id: sorted in Go.
	slices.SortStableFunc(rows, func(a, b organisationRow) int {
		if c := compareNoCase(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return organisationsOf(ctx, s.r, rows)
}

func getOrganisation(ctx context.Context, r repos, id string) (Organisation, error) {
	row, err := r.orgs.Get(ctx, id)
	if err != nil {
		return Organisation{}, missing(err, errOrgNotFound)
	}
	list, err := organisationsOf(ctx, r, []organisationRow{row})
	if err != nil {
		return Organisation{}, err
	}
	return list[0], nil
}

// Organisation returns one organisation with its domains.
func (s *Store) Organisation(ctx context.Context, id string) (Organisation, error) {
	return getOrganisation(ctx, s.r, id)
}

// CreateOrganisation stores a new organisation. It refuses an owner who has
// limit organisations already (limit <= 0: no limit).
func (s *Store) CreateOrganisation(ctx context.Context, id, ownerID, name string, limit int) (Organisation, error) {
	err := s.tx(ctx, func(r repos) error {
		if limit > 0 {
			n, err := r.orgs.Count(ctx, ablage.Eq("owner_id", ownerID))
			if err != nil {
				return err
			}
			if n >= int64(limit) {
				return conflictf("at most %d organisations are possible per person", limit)
			}
		}
		err := r.orgs.Create(ctx, &organisationRow{ID: id, OwnerID: ownerID, Name: name, CreatedAt: s.now()})
		if errors.Is(err, ablage.ErrReference) {
			return errUserNotFound
		}
		return dup(err, conflictf("the organisation id is already used; try again"))
	})
	if err != nil {
		return Organisation{}, err
	}
	return s.Organisation(ctx, id)
}

// RenameOrganisation changes an organisation's name.
func (s *Store) RenameOrganisation(ctx context.Context, id, name string) error {
	return missing(s.r.orgs.Patch(ctx, id, ablage.Row{"name": name}), errOrgNotFound)
}

// DeleteOrganisation removes an organisation; its domains then stand alone.
func (s *Store) DeleteOrganisation(ctx context.Context, id string) error {
	return missing(s.r.orgs.Delete(ctx, id), errOrgNotFound)
}

// ErrMixedOwners: the organisation has, or would get, domains of
// different owners (only admins may group those).
var ErrMixedOwners = errors.New("the organisation holds domains of different owners")

// GroupMailDomain puts a domain into organisation orgID ("": the domain
// stands alone again). In the same transaction it checks whether the
// organisation then holds domains of different owners (its owner counts
// too): mixed reports it, and without allowMixed that is refused with
// ErrMixedOwners. before is the organisation as it was (zero for "").
func (s *Store) GroupMailDomain(ctx context.Context, domain, orgID string, allowMixed bool) (before Organisation, mixed bool, err error) {
	err = s.tx(ctx, func(r repos) error {
		d, err := r.mailDomains.Get(ctx, domain)
		if err != nil {
			return missing(err, errMailDomainNotFound)
		}
		if orgID != "" {
			before, err = getOrganisation(ctx, r, orgID)
			if err != nil {
				return err
			}
			mixed = before.OwnerID != d.OwnerID
			for _, other := range before.Domains {
				mixed = mixed || (before.DomainOwners[other] != d.OwnerID && other != domain)
			}
			if mixed && !allowMixed {
				return ErrMixedOwners
			}
		}
		return r.mailDomains.Patch(ctx, domain, ablage.Row{"organisation_id": optional(orgID)})
	})
	if err != nil {
		return Organisation{}, false, err
	}
	return before, mixed, nil
}

// ---- app passwords ----

// AppPassword is an app password of a mailbox: never the token, only its
// MAC.
type AppPassword struct {
	ID          string
	MailboxID   string
	Name        string
	MAC         string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	LastUsedNet string
}

var errAppPasswordNotFound = notFoundf("app password not found")

func appPasswordOf(a appPasswordRow) AppPassword {
	return AppPassword{ID: a.ID, MailboxID: a.MailboxID, Name: a.Name, MAC: a.MAC, CreatedAt: a.CreatedAt,
		LastUsedAt: a.LastUsedAt, LastUsedNet: a.LastUsedNet}
}

// AppPasswords returns the app passwords of a mailbox ("": of every
// mailbox), oldest first.
func (s *Store) AppPasswords(ctx context.Context, mailboxID string) ([]AppPassword, error) {
	where := ablage.All
	if mailboxID != "" {
		where = ablage.Eq("mailbox_id", mailboxID)
	}
	rows, err := s.r.appPasswords.Find(ctx, ablage.Query{Where: where, OrderBy: byCreation})
	if err != nil {
		return nil, err
	}
	out := make([]AppPassword, len(rows))
	for i, a := range rows {
		out[i] = appPasswordOf(a)
	}
	return out, nil
}

// CreateAppPassword stores an app password. It refuses a mailbox that has
// limit app passwords already.
func (s *Store) CreateAppPassword(ctx context.Context, a AppPassword, limit int) (AppPassword, error) {
	var out AppPassword
	err := s.tx(ctx, func(r repos) error {
		var err error
		out, err = insertAppPassword(ctx, r, a, s.now(), limit)
		return err
	})
	return out, err
}

func insertAppPassword(ctx context.Context, r repos, a AppPassword, now time.Time, limit int) (AppPassword, error) {
	if limit > 0 {
		n, err := r.appPasswords.Count(ctx, ablage.Eq("mailbox_id", a.MailboxID))
		if err != nil {
			return AppPassword{}, err
		}
		if n >= int64(limit) {
			return AppPassword{}, conflictf("a mailbox can have at most %d app passwords", limit)
		}
	}
	created := now
	if !a.CreatedAt.IsZero() {
		created = seconds(a.CreatedAt)
	}
	row := appPasswordRow{ID: a.ID, MailboxID: a.MailboxID, Name: a.Name, MAC: a.MAC, CreatedAt: created}
	if row.ID == "" {
		return AppPassword{}, invalidf("an app password needs an id")
	}
	if err := r.appPasswords.Create(ctx, &row); err != nil {
		if errors.Is(err, ablage.ErrReference) {
			return AppPassword{}, errMailboxNotFound
		}
		return AppPassword{}, dup(err, conflictf("the app password id is already used; try again"))
	}
	return appPasswordOf(row), nil
}

// DeleteAppPassword removes an app password of a mailbox and returns it.
func (s *Store) DeleteAppPassword(ctx context.Context, mailboxID, id string) (AppPassword, error) {
	var a AppPassword
	err := s.tx(ctx, func(r repos) error {
		row, err := r.appPasswords.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("id", id), ablage.Eq("mailbox_id", mailboxID))})
		if err != nil {
			return missing(err, errAppPasswordNotFound)
		}
		a = appPasswordOf(row)
		return r.appPasswords.Delete(ctx, id)
	})
	return a, err
}

// TouchAppPassword records the last use of an app password of the mailbox
// with storageID (a later one only). Unknown ids are ignored.
func (s *Store) TouchAppPassword(ctx context.Context, storageID, id string, at time.Time, network string) error {
	return s.tx(ctx, func(r repos) error {
		m, err := r.mailboxes.First(ctx, ablage.Query{Where: ablage.Eq("storage_id", storageID)})
		if errors.Is(err, ablage.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		at := seconds(at)
		_, err = r.appPasswords.UpdateWhere(ctx, ablage.And(
			ablage.Eq("id", id), ablage.Eq("mailbox_id", m.ID),
			ablage.Or(ablage.Null("last_used_at"), ablage.Lte("last_used_at", at)),
		), ablage.Row{"last_used_at": at, "last_used_net": network})
		return err
	})
}

// MailboxChange is a change a mailbox made to its own account in the mail
// service, as reported in an event. Nil fields are not part of it.
type MailboxChange struct {
	Revision          int64
	At                time.Time
	PasswordHash      *string
	DisplayName       *string
	AppPasswordsOnly  *bool
	AddAppPassword    *AppPassword // MailboxID is filled in
	RevokeAppPassword string
	// Grants replaces who a personal mailbox is shared with.
	Grants *[]GrantByStorage
}

// MailboxChangeResult tells what ApplyMailboxChange did.
type MailboxChangeResult struct {
	// Applied is false when the mailbox is unknown or the revision was
	// recorded already.
	Applied bool
	Mailbox Mailbox
	// Overridden is set when a field was not taken because the panel had
	// changed it at or after the time of the change.
	Overridden bool
	// Revoked is the app password that was removed (RevokeAppPassword).
	Revoked *AppPassword
	// TooManyAppPasswords is set when AddAppPassword was not recorded
	// because the mailbox has the most app passwords it may have (one was
	// created in the panel at the same time). The revision is recorded
	// all the same, so the mail service drops its copy.
	TooManyAppPasswords bool
}

// ApplyMailboxChange records a change of the mailbox with storageID if its
// revision is newer than the mailbox's. The password, display name and
// AppPasswordsOnly are each taken only when the panel did not set that
// field itself at or after ch.At (seconds); app passwords are always
// added or removed, but an added one is not recorded when the mailbox has
// maxAppPasswords already (<= 0: no limit).
// The mailbox's revision becomes ch.Revision either way.
func (s *Store) ApplyMailboxChange(ctx context.Context, storageID string, ch MailboxChange, maxAppPasswords int) (MailboxChangeResult, error) {
	var res MailboxChangeResult
	err := s.tx(ctx, func(r repos) error {
		m, err := findMailbox(ctx, r, ablage.Eq("storage_id", storageID))
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		if ch.Revision <= m.Revision {
			return nil
		}
		// Each field on its own: the panel's change of one field never
		// discards the mailbox's change of another.
		taken := func(admin time.Time) bool {
			if admin.IsZero() || ch.At.Unix() > admin.Unix() {
				return true
			}
			res.Overridden = true
			return false
		}
		// A shared mailbox has no password and no app passwords: a change
		// the mailbox made while it was personal, reported late, is ignored.
		shared := m.Kind == "shared"
		if shared && (ch.PasswordHash != nil || ch.AppPasswordsOnly != nil || ch.AddAppPassword != nil) {
			res.Overridden = true
			ch.PasswordHash, ch.AppPasswordsOnly, ch.AddAppPassword = nil, nil, nil
		}
		if ch.PasswordHash != nil && taken(m.AdminPasswordAt) {
			m.PasswordHash = *ch.PasswordHash
		}
		if ch.DisplayName != nil && taken(m.AdminDisplayNameAt) {
			m.DisplayName = *ch.DisplayName
		}
		if ch.AppPasswordsOnly != nil && taken(m.AdminAppPasswordsOnlyAt) {
			m.AppPasswordsOnly = *ch.AppPasswordsOnly
		}
		if err := r.mailboxes.Patch(ctx, m.ID, ablage.Row{"password_hash": m.PasswordHash, "display_name": m.DisplayName,
			"app_passwords_only": m.AppPasswordsOnly, "revision": ch.Revision}); err != nil {
			return err
		}
		m.Revision = ch.Revision
		if ch.AddAppPassword != nil {
			a := *ch.AddAppPassword
			a.MailboxID = m.ID
			n, err := r.appPasswords.Count(ctx, ablage.Eq("mailbox_id", m.ID))
			if err != nil {
				return err
			}
			if maxAppPasswords > 0 && n >= int64(maxAppPasswords) {
				res.TooManyAppPasswords = true
			} else if _, err := insertAppPassword(ctx, r, a, s.now(), 0); err != nil && !errors.Is(err, ErrConflict) {
				return err
			}
		}
		if ch.Grants != nil {
			// Only a personal mailbox delegates itself: the grants of a
			// shared one are the panel's.
			// The panel's change at or after the mailbox's wins (a removal
			// of access must not come back with a late event).
			if m.Kind == "shared" {
				res.Overridden = true
			} else if taken(m.AdminGrantsAt) {
				if err := replaceGrants(ctx, r, m.ID, *ch.Grants, s.now()); err != nil {
					return err
				}
			}
		}
		if ch.RevokeAppPassword != "" {
			row, err := r.appPasswords.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("id", ch.RevokeAppPassword), ablage.Eq("mailbox_id", m.ID))})
			switch {
			case err == nil:
				if err := r.appPasswords.Delete(ctx, row.ID); err != nil {
					return err
				}
				a := appPasswordOf(row)
				res.Revoked = &a
			case !errors.Is(err, ablage.ErrNotFound):
				return err
			}
		}
		res.Applied, res.Mailbox = true, m
		return nil
	})
	return res, err
}
