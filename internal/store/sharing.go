package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Shared mailboxes, delegation and distribution groups (G3).

// MailboxGrant gives the personal mailbox GranteeID rights on MailboxID.
type MailboxGrant struct {
	MailboxID      int64
	MailboxAddress string
	MailboxKind    string
	MailboxDomain  string
	GranteeID      int64
	GranteeStorage string
	GranteeAddress string
	GranteeDomain  string
	GranteeName    string // display name
	// Rights are names of config.Rights.
	Rights    []string
	CreatedAt time.Time
}

const grantCols = `g.mailbox_id, m.local_part || '@' || m.domain, m.kind, m.domain,
	g.grantee_id, e.storage_id, e.local_part || '@' || e.domain, e.domain, e.display_name, g.rights, g.created_at`

const grantFrom = ` FROM mailbox_grants g JOIN mailboxes m ON m.id = g.mailbox_id JOIN mailboxes e ON e.id = g.grantee_id`

func scanGrant(row interface{ Scan(...any) error }) (MailboxGrant, error) {
	var g MailboxGrant
	var rights string
	var created int64
	if err := row.Scan(&g.MailboxID, &g.MailboxAddress, &g.MailboxKind, &g.MailboxDomain, &g.GranteeID, &g.GranteeStorage,
		&g.GranteeAddress, &g.GranteeDomain, &g.GranteeName, &rights, &created); err != nil {
		return g, err
	}
	g.Rights = strings.Fields(rights)
	g.CreatedAt = unix(created)
	return g, nil
}

// MailboxGrants returns grants, ordered by mailbox and grantee address:
// all of them (both ids 0), those on mailboxID, or those held by granteeID.
func (s *Store) MailboxGrants(ctx context.Context, mailboxID, granteeID int64) ([]MailboxGrant, error) {
	q := `SELECT ` + grantCols + grantFrom
	var args []any
	switch {
	case mailboxID != 0:
		q += ` WHERE g.mailbox_id = ?`
		args = append(args, mailboxID)
	case granteeID != 0:
		q += ` WHERE g.grantee_id = ?`
		args = append(args, granteeID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY g.mailbox_id, e.domain, e.local_part`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MailboxGrant{}
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// MailboxByAddress returns the mailbox local@domain.
func (s *Store) MailboxByAddress(ctx context.Context, domain, local string) (Mailbox, error) {
	return scanMailbox(s.db.QueryRowContext(ctx, `SELECT `+mailboxCols+mailboxFrom+` WHERE m.domain = ? AND m.local_part = ?`, domain, local))
}

// SetMailboxGrant gives grantee rights on a mailbox, or replaces its
// rights; it returns whether a grant existed before. At most limit grants
// per mailbox (<= 0: no limit).
func (s *Store) SetMailboxGrant(ctx context.Context, mailboxID, granteeID int64, rights []string, limit int) (bool, error) {
	existed := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM mailbox_grants WHERE mailbox_id = ? AND grantee_id = ?`, mailboxID, granteeID).Scan(&n); err != nil {
			return err
		}
		existed = n > 0
		if !existed && limit > 0 {
			if err := tx.QueryRow(`SELECT count(*) FROM mailbox_grants WHERE mailbox_id = ?`, mailboxID).Scan(&n); err != nil {
				return err
			}
			if n >= limit {
				return conflictf("a mailbox can be shared with at most %d mailboxes", limit)
			}
		}
		_, err := tx.Exec(`INSERT INTO mailbox_grants (mailbox_id, grantee_id, rights, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (mailbox_id, grantee_id) DO UPDATE SET rights = excluded.rights`,
			mailboxID, granteeID, strings.Join(rights, " "), s.now())
		return err
	})
	return existed, err
}

// DeleteMailboxGrant removes a grant.
func (s *Store) DeleteMailboxGrant(ctx context.Context, mailboxID, granteeID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mailbox_grants WHERE mailbox_id = ? AND grantee_id = ?`, mailboxID, granteeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("this mailbox has no access")
	}
	return nil
}

// MakeMailboxShared turns a personal mailbox into a shared one: its
// password, app passwords and "only app passwords" go, and so do the
// grants it holds on other mailboxes (only personal mailboxes may hold
// grants). The time counts as the panel setting the password.
func (s *Store) MakeMailboxShared(ctx context.Context, id int64) error {
	now := s.now()
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE mailboxes SET kind = 'shared', password_hash = '', app_passwords_only = 0,
			admin_password_at = ?, admin_app_passwords_only_at = ? WHERE id = ?`, now, now, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFoundf("mailbox not found")
		}
		for _, q := range []string{
			`DELETE FROM mailbox_app_passwords WHERE mailbox_id = ?`,
			`DELETE FROM mailbox_grants WHERE grantee_id = ?`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// MakeMailboxPersonal turns a shared mailbox into a personal one with the
// password hash; it no longer keeps copies of sent mail. Its grants stay:
// the person now shares their own mailbox with them.
func (s *Store) MakeMailboxPersonal(ctx context.Context, id int64, hash string) error {
	return s.updateMailbox(ctx, `UPDATE mailboxes SET kind = 'personal', password_hash = ?, keep_sent_copy = 0, admin_password_at = ? WHERE id = ?`,
		hash, s.now(), id)
}

// SetMailboxKeepSentCopy switches copies of sent mail of a mailbox.
func (s *Store) SetMailboxKeepSentCopy(ctx context.Context, id int64, on bool) error {
	return s.updateMailbox(ctx, `UPDATE mailboxes SET keep_sent_copy = ? WHERE id = ?`, on, id)
}

// replaceGrants replaces all grants on a mailbox with those to the
// mailboxes with the given storage ids (unknown ones are skipped).
func replaceGrants(tx *sql.Tx, mailboxID int64, grants []GrantByStorage, now int64) error {
	if _, err := tx.Exec(`DELETE FROM mailbox_grants WHERE mailbox_id = ?`, mailboxID); err != nil {
		return err
	}
	for _, g := range grants {
		var grantee int64
		err := tx.QueryRow(`SELECT id FROM mailboxes WHERE storage_id = ?`, g.StorageID).Scan(&grantee)
		if noRows(err) || grantee == mailboxID {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO mailbox_grants (mailbox_id, grantee_id, rights, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT DO NOTHING`, mailboxID, grantee, strings.Join(g.Rights, " "), now); err != nil {
			return err
		}
	}
	return nil
}

// GrantByStorage is a grant as the mail service reports it.
type GrantByStorage struct {
	StorageID string
	Rights    []string
}
