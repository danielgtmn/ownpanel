package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// MailDomain is a domain with e-mail. It belongs to OwnerID.
type MailDomain struct {
	Domain      string
	OwnerID     int64
	Owner       string // the owner's user name
	Active      bool
	ActivatedAt *time.Time
	CheckedAt   *time.Time
	CheckError  string
	CreatedAt   time.Time
	Mailboxes   int // filled by MailDomains and MailDomain
	Aliases     int
	// OrganisationID is "" when the domain is its own organisation.
	OrganisationID      string
	OrganisationName    string
	OrganisationOwnerID int64
}

// DKIMKey is one DKIM key of a mail domain. PrivateKey is sealed by the
// caller (internal/secrets); the store never sees the plain key.
type DKIMKey struct {
	Domain     string
	Selector   string
	PrivateKey []byte
	PublicKey  string // base64 DER, for the DNS record
	State      string // DKIMActive, DKIMPending or DKIMRetired
	CreatedAt  time.Time
}

// DKIM key states.
const (
	DKIMActive  = "active"
	DKIMPending = "pending"
	DKIMRetired = "retired"
)

// Mailbox is a mailbox of a mail domain. OwnerID is the domain's owner.
type Mailbox struct {
	ID           int64
	StorageID    string
	Domain       string
	LocalPart    string
	PasswordHash string
	QuotaBytes   int64
	SendPerHour  int
	SendPerDay   int
	CreatedAt    time.Time
	OwnerID      int64
	Owner        string
	// DisplayName may be empty. AppPasswordsOnly: mail programs sign in
	// only with app passwords.
	DisplayName      string
	AppPasswordsOnly bool
	// Revision is the highest revision of the mailbox's own changes in the
	// mail service recorded here. Admin*At are when the panel last set the
	// password, the display name or AppPasswordsOnly itself (zero: never).
	Revision                int64
	AdminPasswordAt         time.Time
	AdminDisplayNameAt      time.Time
	AdminAppPasswordsOnlyAt time.Time
}

// MailAlias delivers mail for LocalPart@Domain to Targets. LocalPart "*" is
// the catch-all. OwnerID is the domain's owner.
type MailAlias struct {
	ID        int64
	Domain    string
	LocalPart string
	Targets   []string
	CreatedAt time.Time
	OwnerID   int64
	Owner     string
}

// ---- domains ----

const mailDomainCols = `d.domain, d.owner_id, coalesce(u.name, ''), d.active, d.activated_at, d.checked_at, d.check_error, d.created_at,
	(SELECT count(*) FROM mailboxes m WHERE m.domain = d.domain), (SELECT count(*) FROM mail_aliases a WHERE a.domain = d.domain),
	coalesce(o.id, ''), coalesce(o.name, ''), coalesce(o.owner_id, 0)`

const mailDomainFrom = ` FROM mail_domains d LEFT JOIN users u ON u.id = d.owner_id LEFT JOIN organisations o ON o.id = d.organisation_id`

func optionalTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := unix(v.Int64)
	return &t
}

func scanMailDomain(row interface{ Scan(...any) error }) (MailDomain, error) {
	var d MailDomain
	var activated, checked sql.NullInt64
	var created int64
	err := row.Scan(&d.Domain, &d.OwnerID, &d.Owner, &d.Active, &activated, &checked, &d.CheckError, &created, &d.Mailboxes, &d.Aliases,
		&d.OrganisationID, &d.OrganisationName, &d.OrganisationOwnerID)
	if err != nil {
		if noRows(err) {
			return MailDomain{}, notFoundf("mail domain not found")
		}
		return MailDomain{}, err
	}
	d.ActivatedAt, d.CheckedAt, d.CreatedAt = optionalTime(activated), optionalTime(checked), unix(created)
	return d, nil
}

// MailDomains returns every mail domain, sorted by name.
func (s *Store) MailDomains(ctx context.Context) ([]MailDomain, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mailDomainCols+mailDomainFrom+` ORDER BY d.domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MailDomain{}
	for rows.Next() {
		d, err := scanMailDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MailDomain returns one mail domain.
func (s *Store) MailDomain(ctx context.Context, domain string) (MailDomain, error) {
	return scanMailDomain(s.db.QueryRowContext(ctx, `SELECT `+mailDomainCols+mailDomainFrom+` WHERE d.domain = ?`, domain))
}

// CreateMailDomain stores a new, inactive mail domain with its first DKIM
// key. It refuses a domain that has e-mail already, and an owner who has
// limit mail domains already (limit <= 0: no limit).
func (s *Store) CreateMailDomain(ctx context.Context, domain string, ownerID int64, key DKIMKey, limit int) (MailDomain, error) {
	var out MailDomain
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if limit > 0 {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM mail_domains WHERE owner_id = ?`, ownerID).Scan(&n); err != nil {
				return err
			}
			if n >= limit {
				return conflictf("at most %d mail domains are possible per person", limit)
			}
		}
		now := s.now()
		_, err := tx.Exec(`INSERT INTO mail_domains (domain, owner_id, created_at) VALUES (?, ?, ?)`, domain, ownerID, now)
		if isUnique(err) {
			return conflictf("e-mail is already enabled for %s", domain)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO mail_dkim_keys (domain, selector, private_key, public_key, state, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			domain, key.Selector, key.PrivateKey, key.PublicKey, key.State, now); err != nil {
			return err
		}
		out, err = scanMailDomain(tx.QueryRow(`SELECT `+mailDomainCols+mailDomainFrom+` WHERE d.domain = ?`, domain))
		return err
	})
	return out, err
}

// RecordMailDomainCheck stores the outcome of looking at a domain's MX
// record. active true activates the domain (for good); false leaves an
// active domain active.
func (s *Store) RecordMailDomainCheck(ctx context.Context, domain string, at time.Time, active bool, problem string) error {
	var res sql.Result
	var err error
	if active {
		res, err = s.db.ExecContext(ctx, `UPDATE mail_domains SET checked_at = ?, check_error = '', active = 1,
			activated_at = coalesce(activated_at, ?) WHERE domain = ?`, at.Unix(), at.Unix(), domain)
	} else {
		res, err = s.db.ExecContext(ctx, `UPDATE mail_domains SET checked_at = ?, check_error = ? WHERE domain = ?`, at.Unix(), problem, domain)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("mail domain not found")
	}
	return nil
}

// DeleteMailDomain removes a mail domain with its keys, mailboxes and
// aliases. The storage ids of its mailboxes are kept as pending deletes and
// returned.
func (s *Store) DeleteMailDomain(ctx context.Context, domain string) ([]string, error) {
	var ids []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT storage_id FROM mailboxes WHERE domain = ? ORDER BY id`, domain)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			if err := addPendingDelete(tx, id, s.now()); err != nil {
				return err
			}
		}
		res, err := tx.Exec(`DELETE FROM mail_domains WHERE domain = ?`, domain)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFoundf("mail domain not found")
		}
		return nil
	})
	return ids, err
}

// DKIMKeys returns the keys of a domain, oldest first.
func (s *Store) DKIMKeys(ctx context.Context, domain string) ([]DKIMKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain, selector, private_key, public_key, state, created_at
		FROM mail_dkim_keys WHERE domain = ? ORDER BY id`, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DKIMKey{}
	for rows.Next() {
		var k DKIMKey
		var created int64
		if err := rows.Scan(&k.Domain, &k.Selector, &k.PrivateKey, &k.PublicKey, &k.State, &created); err != nil {
			return nil, err
		}
		k.CreatedAt = unix(created)
		out = append(out, k)
	}
	return out, rows.Err()
}

// ---- mailboxes ----

const mailboxCols = `m.id, m.storage_id, m.domain, m.local_part, m.password_hash, m.quota_bytes, m.send_per_hour, m.send_per_day,
	m.created_at, d.owner_id, coalesce(u.name, ''), m.display_name, m.app_passwords_only, m.revision,
	m.admin_password_at, m.admin_display_name_at, m.admin_app_passwords_only_at`

const mailboxFrom = ` FROM mailboxes m JOIN mail_domains d ON d.domain = m.domain LEFT JOIN users u ON u.id = d.owner_id`

func scanMailbox(row interface{ Scan(...any) error }) (Mailbox, error) {
	var m Mailbox
	var created, adminPassword, adminDisplayName, adminAppPasswordsOnly int64
	err := row.Scan(&m.ID, &m.StorageID, &m.Domain, &m.LocalPart, &m.PasswordHash, &m.QuotaBytes, &m.SendPerHour, &m.SendPerDay,
		&created, &m.OwnerID, &m.Owner, &m.DisplayName, &m.AppPasswordsOnly, &m.Revision,
		&adminPassword, &adminDisplayName, &adminAppPasswordsOnly)
	if err != nil {
		if noRows(err) {
			return Mailbox{}, notFoundf("mailbox not found")
		}
		return Mailbox{}, err
	}
	m.CreatedAt = unix(created)
	for _, f := range []struct {
		v   int64
		dst *time.Time
	}{{adminPassword, &m.AdminPasswordAt}, {adminDisplayName, &m.AdminDisplayNameAt}, {adminAppPasswordsOnly, &m.AdminAppPasswordsOnlyAt}} {
		if f.v > 0 {
			*f.dst = unix(f.v)
		}
	}
	return m, nil
}

// Mailboxes returns the mailboxes of domain ("": of all domains) in the
// order they were created.
func (s *Store) Mailboxes(ctx context.Context, domain string) ([]Mailbox, error) {
	q := `SELECT ` + mailboxCols + mailboxFrom
	var args []any
	if domain != "" {
		q += ` WHERE m.domain = ?`
		args = append(args, domain)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY m.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mailbox{}
	for rows.Next() {
		m, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Mailbox returns one mailbox.
func (s *Store) Mailbox(ctx context.Context, id int64) (Mailbox, error) {
	return scanMailbox(s.db.QueryRowContext(ctx, `SELECT `+mailboxCols+mailboxFrom+` WHERE m.id = ?`, id))
}

// CreateMailbox stores a new mailbox. It refuses a local part that is
// taken by a mailbox or an alias of the domain, and a domain owner who has
// limit mailboxes already (limit <= 0: no limit).
func (s *Store) CreateMailbox(ctx context.Context, m Mailbox, limit int) (Mailbox, error) {
	var out Mailbox
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var owner int64
		if err := tx.QueryRow(`SELECT owner_id FROM mail_domains WHERE domain = ?`, m.Domain).Scan(&owner); err != nil {
			if noRows(err) {
				return notFoundf("mail domain not found")
			}
			return err
		}
		if err := aliasFree(tx, m.Domain, m.LocalPart, 0); err != nil {
			return err
		}
		if limit > 0 {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM mailboxes m JOIN mail_domains d ON d.domain = m.domain WHERE d.owner_id = ?`, owner).Scan(&n); err != nil {
				return err
			}
			if n >= limit {
				return conflictf("at most %d mailboxes are possible per person", limit)
			}
		}
		res, err := tx.Exec(`INSERT INTO mailboxes (storage_id, domain, local_part, password_hash, quota_bytes, send_per_hour, send_per_day, created_at, display_name)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.StorageID, m.Domain, m.LocalPart, m.PasswordHash, m.QuotaBytes, m.SendPerHour, m.SendPerDay, s.now(), m.DisplayName)
		if isUnique(err) {
			var n int
			if e := tx.QueryRow(`SELECT count(*) FROM mailboxes WHERE domain = ? AND local_part = ?`, m.Domain, m.LocalPart).Scan(&n); e == nil && n > 0 {
				return conflictf("the mailbox %s@%s already exists", m.LocalPart, m.Domain)
			}
			return conflictf("the storage id is already used; try again")
		}
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = scanMailbox(tx.QueryRow(`SELECT `+mailboxCols+mailboxFrom+` WHERE m.id = ?`, id))
		return err
	})
	return out, err
}

// SetMailboxPassword replaces a mailbox's password hash (a change by the
// panel: it records AdminPasswordAt).
func (s *Store) SetMailboxPassword(ctx context.Context, id int64, hash string) error {
	return s.updateMailbox(ctx, `UPDATE mailboxes SET password_hash = ?, admin_password_at = ? WHERE id = ?`, hash, s.now(), id)
}

// SetMailboxAccount replaces a mailbox's display name and AppPasswordsOnly;
// nil fields stay as they are. A change by the panel: it records the
// admin time of each field it writes, and only of those.
func (s *Store) SetMailboxAccount(ctx context.Context, id int64, displayName *string, appPasswordsOnly *bool) error {
	now := s.now()
	q := `UPDATE mailboxes SET id = id`
	var args []any
	if displayName != nil {
		q += `, display_name = ?, admin_display_name_at = ?`
		args = append(args, *displayName, now)
	}
	if appPasswordsOnly != nil {
		q += `, app_passwords_only = ?, admin_app_passwords_only_at = ?`
		args = append(args, *appPasswordsOnly, now)
	}
	return s.updateMailbox(ctx, q+` WHERE id = ?`, append(args, id)...)
}

// SetMailboxLimits replaces a mailbox's quota and send limits.
func (s *Store) SetMailboxLimits(ctx context.Context, id int64, quota int64, perHour, perDay int) error {
	return s.updateMailbox(ctx, `UPDATE mailboxes SET quota_bytes = ?, send_per_hour = ?, send_per_day = ? WHERE id = ?`, quota, perHour, perDay, id)
}

func (s *Store) updateMailbox(ctx context.Context, q string, args ...any) error {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("mailbox not found")
	}
	return nil
}

// DeleteMailbox removes a mailbox and keeps its storage id as a pending
// delete, which it returns.
func (s *Store) DeleteMailbox(ctx context.Context, id int64) (string, error) {
	var storageID string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT storage_id FROM mailboxes WHERE id = ?`, id).Scan(&storageID); err != nil {
			if noRows(err) {
				return notFoundf("mailbox not found")
			}
			return err
		}
		if err := addPendingDelete(tx, storageID, s.now()); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM mailboxes WHERE id = ?`, id)
		return err
	})
	return storageID, err
}

func addPendingDelete(tx *sql.Tx, storageID string, now int64) error {
	_, err := tx.Exec(`INSERT INTO mail_pending_deletes (storage_id, created_at) VALUES (?, ?) ON CONFLICT DO NOTHING`, storageID, now)
	return err
}

// PendingMailDeletes returns the storage ids whose data still has to be
// deleted in the mail service, oldest first.
func (s *Store) PendingMailDeletes(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT storage_id FROM mail_pending_deletes ORDER BY created_at, storage_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DonePendingMailDelete forgets a pending delete the service confirmed.
func (s *Store) DonePendingMailDelete(ctx context.Context, storageID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mail_pending_deletes WHERE storage_id = ?`, storageID)
	return err
}

// ---- aliases ----

const aliasCols = `a.id, a.domain, a.local_part, a.targets, a.created_at, d.owner_id, coalesce(u.name, '')`

const aliasFrom = ` FROM mail_aliases a JOIN mail_domains d ON d.domain = a.domain LEFT JOIN users u ON u.id = d.owner_id`

func scanAlias(row interface{ Scan(...any) error }) (MailAlias, error) {
	var a MailAlias
	var targets string
	var created int64
	if err := row.Scan(&a.ID, &a.Domain, &a.LocalPart, &targets, &created, &a.OwnerID, &a.Owner); err != nil {
		if noRows(err) {
			return MailAlias{}, notFoundf("alias not found")
		}
		return MailAlias{}, err
	}
	if err := json.Unmarshal([]byte(targets), &a.Targets); err != nil {
		return MailAlias{}, err
	}
	a.CreatedAt = unix(created)
	return a, nil
}

// MailAliases returns the aliases of domain ("": of all domains), sorted by
// domain and local part.
func (s *Store) MailAliases(ctx context.Context, domain string) ([]MailAlias, error) {
	q := `SELECT ` + aliasCols + aliasFrom
	var args []any
	if domain != "" {
		q += ` WHERE a.domain = ?`
		args = append(args, domain)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY a.domain, a.local_part`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MailAlias{}
	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MailAlias returns one alias.
func (s *Store) MailAlias(ctx context.Context, id int64) (MailAlias, error) {
	return scanAlias(s.db.QueryRowContext(ctx, `SELECT `+aliasCols+aliasFrom+` WHERE a.id = ?`, id))
}

// aliasFree refuses a local part that is a mailbox of the domain, or an
// alias other than except.
func aliasFree(tx *sql.Tx, domain, local string, except int64) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM mail_aliases WHERE domain = ? AND local_part = ? AND id != ?`, domain, local, except).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return conflictf("%s@%s is already an alias", local, domain)
	}
	return nil
}

func mailboxFree(tx *sql.Tx, domain, local string) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM mailboxes WHERE domain = ? AND local_part = ?`, domain, local).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return conflictf("%s@%s is a mailbox; an address is either a mailbox or an alias", local, domain)
	}
	return nil
}

// CreateMailAlias stores a new alias. The local part must be neither a
// mailbox nor an alias of the domain yet.
func (s *Store) CreateMailAlias(ctx context.Context, a MailAlias) (MailAlias, error) {
	targets, err := json.Marshal(a.Targets)
	if err != nil {
		return MailAlias{}, err
	}
	var out MailAlias
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM mail_domains WHERE domain = ?`, a.Domain).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return notFoundf("mail domain not found")
		}
		if err := mailboxFree(tx, a.Domain, a.LocalPart); err != nil {
			return err
		}
		if err := aliasFree(tx, a.Domain, a.LocalPart, 0); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO mail_aliases (domain, local_part, targets, created_at) VALUES (?, ?, ?, ?)`,
			a.Domain, a.LocalPart, string(targets), s.now())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = scanAlias(tx.QueryRow(`SELECT `+aliasCols+aliasFrom+` WHERE a.id = ?`, id))
		return err
	})
	return out, err
}

// UpdateMailAlias replaces the local part and targets of an alias (its
// domain stays), with the checks of CreateMailAlias.
func (s *Store) UpdateMailAlias(ctx context.Context, a MailAlias) (MailAlias, error) {
	targets, err := json.Marshal(a.Targets)
	if err != nil {
		return MailAlias{}, err
	}
	var out MailAlias
	err = s.tx(ctx, func(tx *sql.Tx) error {
		cur, err := scanAlias(tx.QueryRow(`SELECT `+aliasCols+aliasFrom+` WHERE a.id = ?`, a.ID))
		if err != nil {
			return err
		}
		if err := mailboxFree(tx, cur.Domain, a.LocalPart); err != nil {
			return err
		}
		if err := aliasFree(tx, cur.Domain, a.LocalPart, a.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE mail_aliases SET local_part = ?, targets = ? WHERE id = ?`, a.LocalPart, string(targets), a.ID); err != nil {
			return err
		}
		out, err = scanAlias(tx.QueryRow(`SELECT `+aliasCols+aliasFrom+` WHERE a.id = ?`, a.ID))
		return err
	})
	return out, err
}

// DeleteMailAlias removes an alias.
func (s *Store) DeleteMailAlias(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mail_aliases WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("alias not found")
	}
	return nil
}

func ownsNoMailDomains(tx *sql.Tx, userID int64, name string) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM mail_domains WHERE owner_id = ?`, userID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return conflictf("%s still owns mail domains; delete them first", name)
	}
	return nil
}
