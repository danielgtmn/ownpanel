package store

import "context"

// Forwarding by filters (G2): mailboxes listed in mailbox_no_forwarding
// may not forward mail.

// MailboxesNoForwarding returns the ids of mailboxes that may not forward.
func (s *Store) MailboxesNoForwarding(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mailbox_id FROM mailbox_no_forwarding`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// MailboxMayForward tells whether a mailbox's filters may forward mail.
func (s *Store) MailboxMayForward(ctx context.Context, id int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_no_forwarding WHERE mailbox_id = ?`, id).Scan(&n)
	return n == 0, err
}

// SetMailboxMayForward allows or forbids forwarding for a mailbox.
func (s *Store) SetMailboxMayForward(ctx context.Context, id int64, allowed bool) error {
	if allowed {
		_, err := s.db.ExecContext(ctx, `DELETE FROM mailbox_no_forwarding WHERE mailbox_id = ?`, id)
		return err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM mailboxes WHERE id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return notFoundf("mailbox not found")
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO mailbox_no_forwarding (mailbox_id) VALUES (?)`, id)
	return err
}
