package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ---- organisations ----

// Organisation groups mail domains. Domains lists its domains, sorted;
// DomainOwners has the owner of each.
type Organisation struct {
	ID           string
	OwnerID      int64
	Owner        string
	Name         string
	CreatedAt    time.Time
	Domains      []string
	DomainOwners map[string]int64
}

// Organisations returns every organisation with its domains, sorted by name.
func (s *Store) Organisations(ctx context.Context) ([]Organisation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.id, o.owner_id, coalesce(u.name, ''), o.name, o.created_at
		FROM organisations o LEFT JOIN users u ON u.id = o.owner_id ORDER BY o.name COLLATE NOCASE, o.id`)
	if err != nil {
		return nil, err
	}
	out := []Organisation{}
	idx := map[string]int{}
	for rows.Next() {
		var o Organisation
		var created int64
		if err := rows.Scan(&o.ID, &o.OwnerID, &o.Owner, &o.Name, &created); err != nil {
			rows.Close()
			return nil, err
		}
		o.CreatedAt, o.Domains, o.DomainOwners = unix(created), []string{}, map[string]int64{}
		idx[o.ID] = len(out)
		out = append(out, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	drows, err := s.db.QueryContext(ctx, `SELECT organisation_id, domain, owner_id FROM mail_domains WHERE organisation_id IS NOT NULL ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer drows.Close()
	for drows.Next() {
		var id, d string
		var owner int64
		if err := drows.Scan(&id, &d, &owner); err != nil {
			return nil, err
		}
		if i, ok := idx[id]; ok {
			out[i].Domains = append(out[i].Domains, d)
			out[i].DomainOwners[d] = owner
		}
	}
	return out, drows.Err()
}

// Organisation returns one organisation with its domains.
func (s *Store) Organisation(ctx context.Context, id string) (Organisation, error) {
	all, err := s.Organisations(ctx)
	if err != nil {
		return Organisation{}, err
	}
	for _, o := range all {
		if o.ID == id {
			return o, nil
		}
	}
	return Organisation{}, notFoundf("organisation not found")
}

// CreateOrganisation stores a new organisation. It refuses an owner who has
// limit organisations already (limit <= 0: no limit).
func (s *Store) CreateOrganisation(ctx context.Context, id string, ownerID int64, name string, limit int) (Organisation, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if limit > 0 {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM organisations WHERE owner_id = ?`, ownerID).Scan(&n); err != nil {
				return err
			}
			if n >= limit {
				return conflictf("at most %d organisations are possible per person", limit)
			}
		}
		_, err := tx.Exec(`INSERT INTO organisations (id, owner_id, name, created_at) VALUES (?, ?, ?, ?)`, id, ownerID, name, s.now())
		if isUnique(err) {
			return conflictf("the organisation id is already used; try again")
		}
		return err
	})
	if err != nil {
		return Organisation{}, err
	}
	return s.Organisation(ctx, id)
}

// RenameOrganisation changes an organisation's name.
func (s *Store) RenameOrganisation(ctx context.Context, id, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE organisations SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("organisation not found")
	}
	return nil
}

// DeleteOrganisation removes an organisation; its domains then stand alone.
func (s *Store) DeleteOrganisation(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM organisations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("organisation not found")
	}
	return nil
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
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var owner int64
		if err := tx.QueryRow(`SELECT owner_id FROM mail_domains WHERE domain = ?`, domain).Scan(&owner); err != nil {
			if noRows(err) {
				return notFoundf("mail domain not found")
			}
			return err
		}
		var v any
		if orgID != "" {
			v = orgID
			var created int64
			err := tx.QueryRow(`SELECT o.id, o.owner_id, coalesce(u.name, ''), o.name, o.created_at
				FROM organisations o LEFT JOIN users u ON u.id = o.owner_id WHERE o.id = ?`, orgID).
				Scan(&before.ID, &before.OwnerID, &before.Owner, &before.Name, &created)
			if noRows(err) {
				return notFoundf("organisation not found")
			}
			if err != nil {
				return err
			}
			before.CreatedAt, before.Domains, before.DomainOwners = unix(created), []string{}, map[string]int64{}
			rows, err := tx.Query(`SELECT domain, owner_id FROM mail_domains WHERE organisation_id = ? ORDER BY domain`, orgID)
			if err != nil {
				return err
			}
			defer rows.Close()
			mixed = before.OwnerID != owner
			for rows.Next() {
				var d string
				var o int64
				if err := rows.Scan(&d, &o); err != nil {
					return err
				}
				before.Domains = append(before.Domains, d)
				before.DomainOwners[d] = o
				mixed = mixed || (o != owner && d != domain)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if mixed && !allowMixed {
				return ErrMixedOwners
			}
		}
		_, err := tx.Exec(`UPDATE mail_domains SET organisation_id = ? WHERE domain = ?`, v, domain)
		return err
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
	MailboxID   int64
	Name        string
	MAC         string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	LastUsedNet string
}

const appPasswordCols = `id, mailbox_id, name, mac, created_at, last_used_at, last_used_net`

func scanAppPassword(row interface{ Scan(...any) error }) (AppPassword, error) {
	var a AppPassword
	var created int64
	var used sql.NullInt64
	if err := row.Scan(&a.ID, &a.MailboxID, &a.Name, &a.MAC, &created, &used, &a.LastUsedNet); err != nil {
		if noRows(err) {
			return AppPassword{}, notFoundf("app password not found")
		}
		return AppPassword{}, err
	}
	a.CreatedAt, a.LastUsedAt = unix(created), optionalTime(used)
	return a, nil
}

// AppPasswords returns the app passwords of a mailbox (0: of every
// mailbox), oldest first.
func (s *Store) AppPasswords(ctx context.Context, mailboxID int64) ([]AppPassword, error) {
	q := `SELECT ` + appPasswordCols + ` FROM mailbox_app_passwords`
	var args []any
	if mailboxID != 0 {
		q += ` WHERE mailbox_id = ?`
		args = append(args, mailboxID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppPassword{}
	for rows.Next() {
		a, err := scanAppPassword(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateAppPassword stores an app password. It refuses a mailbox that has
// limit app passwords already.
func (s *Store) CreateAppPassword(ctx context.Context, a AppPassword, limit int) (AppPassword, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		return insertAppPassword(tx, a, s.now(), limit)
	})
	if err != nil {
		return AppPassword{}, err
	}
	return scanAppPassword(s.db.QueryRowContext(ctx, `SELECT `+appPasswordCols+` FROM mailbox_app_passwords WHERE id = ?`, a.ID))
}

func insertAppPassword(tx *sql.Tx, a AppPassword, now int64, limit int) error {
	if limit > 0 {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM mailbox_app_passwords WHERE mailbox_id = ?`, a.MailboxID).Scan(&n); err != nil {
			return err
		}
		if n >= limit {
			return conflictf("a mailbox can have at most %d app passwords", limit)
		}
	}
	created := now
	if !a.CreatedAt.IsZero() {
		created = a.CreatedAt.Unix()
	}
	_, err := tx.Exec(`INSERT INTO mailbox_app_passwords (id, mailbox_id, name, mac, created_at) VALUES (?, ?, ?, ?, ?)`,
		a.ID, a.MailboxID, a.Name, a.MAC, created)
	if isUnique(err) {
		return conflictf("the app password id is already used; try again")
	}
	return err
}

// DeleteAppPassword removes an app password of a mailbox and returns it.
func (s *Store) DeleteAppPassword(ctx context.Context, mailboxID int64, id string) (AppPassword, error) {
	var a AppPassword
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		a, err = scanAppPassword(tx.QueryRow(`SELECT `+appPasswordCols+` FROM mailbox_app_passwords WHERE id = ? AND mailbox_id = ?`, id, mailboxID))
		if err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM mailbox_app_passwords WHERE id = ?`, id)
		return err
	})
	return a, err
}

// TouchAppPassword records the last use of an app password of the mailbox
// with storageID (a later one only). Unknown ids are ignored.
func (s *Store) TouchAppPassword(ctx context.Context, storageID, id string, at time.Time, network string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mailbox_app_passwords SET last_used_at = ?, last_used_net = ?
		WHERE id = ? AND mailbox_id = (SELECT id FROM mailboxes WHERE storage_id = ?) AND coalesce(last_used_at, 0) <= ?`,
		at.Unix(), network, id, storageID, at.Unix())
	return err
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
	var r MailboxChangeResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
		m, err := scanMailbox(tx.QueryRow(`SELECT `+mailboxCols+mailboxFrom+` WHERE m.storage_id = ?`, storageID))
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
			r.Overridden = true
			return false
		}
		// A shared mailbox has no password and no app passwords: a change
		// the mailbox made while it was personal, reported late, is ignored.
		shared := m.Kind == "shared"
		if shared && (ch.PasswordHash != nil || ch.AppPasswordsOnly != nil || ch.AddAppPassword != nil) {
			r.Overridden = true
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
		if _, err := tx.Exec(`UPDATE mailboxes SET password_hash = ?, display_name = ?, app_passwords_only = ?, revision = ? WHERE id = ?`,
			m.PasswordHash, m.DisplayName, m.AppPasswordsOnly, ch.Revision, m.ID); err != nil {
			return err
		}
		m.Revision = ch.Revision
		if ch.AddAppPassword != nil {
			a := *ch.AddAppPassword
			a.MailboxID = m.ID
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM mailbox_app_passwords WHERE mailbox_id = ?`, m.ID).Scan(&n); err != nil {
				return err
			}
			if maxAppPasswords > 0 && n >= maxAppPasswords {
				r.TooManyAppPasswords = true
			} else if err := insertAppPassword(tx, a, s.now(), 0); err != nil && !errors.Is(err, ErrConflict) {
				return err
			}
		}
		if ch.Grants != nil {
			// Only a personal mailbox delegates itself: the grants of a
			// shared one are the panel's.
			if m.Kind == "shared" {
				r.Overridden = true
			} else if err := replaceGrants(tx, m.ID, *ch.Grants, s.now()); err != nil {
				return err
			}
		}
		if ch.RevokeAppPassword != "" {
			a, err := scanAppPassword(tx.QueryRow(`SELECT `+appPasswordCols+` FROM mailbox_app_passwords WHERE id = ? AND mailbox_id = ?`, ch.RevokeAppPassword, m.ID))
			switch {
			case err == nil:
				if _, err := tx.Exec(`DELETE FROM mailbox_app_passwords WHERE id = ?`, a.ID); err != nil {
					return err
				}
				r.Revoked = &a
			case !errors.Is(err, ErrNotFound):
				return err
			}
		}
		r.Applied, r.Mailbox = true, m
		return nil
	})
	return r, err
}
