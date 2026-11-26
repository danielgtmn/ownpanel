package store

import (
	"context"

	"github.com/danielgtmn/ablage"
)

// Forwarding by filters (G2): mailboxes listed in mailbox_no_forwarding
// may not forward mail.

// MailboxesNoForwarding returns the ids of mailboxes that may not forward.
func (s *Store) MailboxesNoForwarding(ctx context.Context) (map[string]bool, error) {
	rows, err := s.r.noForwarding.Find(ctx, ablage.Query{})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.MailboxID] = true
	}
	return out, nil
}

// MailboxMayForward tells whether a mailbox's filters may forward mail.
func (s *Store) MailboxMayForward(ctx context.Context, id string) (bool, error) {
	n, err := s.r.noForwarding.Count(ctx, ablage.Eq("mailbox_id", id))
	return n == 0, err
}

// SetMailboxMayForward allows or forbids forwarding for a mailbox.
func (s *Store) SetMailboxMayForward(ctx context.Context, id string, allowed bool) error {
	if allowed {
		_, err := s.r.noForwarding.DeleteWhere(ctx, ablage.Eq("mailbox_id", id))
		return err
	}
	return s.tx(ctx, func(r repos) error {
		ok, err := r.mailboxes.Exists(ctx, id)
		if err != nil {
			return err
		}
		if id == "" || !ok {
			return errMailboxNotFound
		}
		return ignoreDup(r.noForwarding.Create(ctx, &noForwardingRow{MailboxID: id}))
	})
}
