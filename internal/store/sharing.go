package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/danielgtmn/ablage"
)

// Shared mailboxes, delegation and distribution groups (G3).

// MailboxGrant gives the personal mailbox GranteeID rights on MailboxID.
type MailboxGrant struct {
	MailboxID      string
	MailboxAddress string
	MailboxKind    string
	MailboxDomain  string
	GranteeID      string
	GranteeStorage string
	GranteeAddress string
	GranteeDomain  string
	GranteeName    string // display name
	// Rights are names of config.Rights.
	Rights    []string
	CreatedAt time.Time
}

// MailboxGrants returns grants, ordered by mailbox (in the order the
// mailboxes were created) and grantee address: all of them (both ids ""),
// those on mailboxID, or those held by granteeID.
func (s *Store) MailboxGrants(ctx context.Context, mailboxID, granteeID string) ([]MailboxGrant, error) {
	where := ablage.All
	switch {
	case mailboxID != "":
		where = ablage.Eq("mailbox_id", mailboxID)
	case granteeID != "":
		where = ablage.Eq("grantee_id", granteeID)
	}
	rows, err := s.r.grants.Find(ctx, ablage.Query{Where: where})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, g := range rows {
		ids = append(ids, g.MailboxID, g.GranteeID)
	}
	boxes := map[string]mailboxRow{}
	if len(ids) > 0 {
		list, err := s.r.mailboxes.GetMany(ctx, unique(ids)...)
		if err != nil {
			return nil, err
		}
		for _, m := range list {
			boxes[m.ID] = m
		}
	}
	type sorted struct {
		g       MailboxGrant
		mailbox mailboxRow
		grantee mailboxRow
	}
	list := make([]sorted, 0, len(rows))
	for _, g := range rows {
		m, e := boxes[g.MailboxID], boxes[g.GranteeID]
		list = append(list, sorted{MailboxGrant{
			MailboxID: g.MailboxID, MailboxAddress: m.LocalPart + "@" + m.Domain, MailboxKind: m.Kind, MailboxDomain: m.Domain,
			GranteeID: g.GranteeID, GranteeStorage: e.StorageID, GranteeAddress: e.LocalPart + "@" + e.Domain,
			GranteeDomain: e.Domain, GranteeName: e.DisplayName, Rights: strings.Fields(g.Rights), CreatedAt: g.CreatedAt,
		}, m, e})
	}
	slices.SortFunc(list, func(a, b sorted) int {
		return cmp.Or(
			a.mailbox.CreatedAt.Compare(b.mailbox.CreatedAt), strings.Compare(a.mailbox.ID, b.mailbox.ID),
			strings.Compare(a.grantee.Domain, b.grantee.Domain), strings.Compare(a.grantee.LocalPart, b.grantee.LocalPart),
		)
	})
	out := make([]MailboxGrant, len(list))
	for i, x := range list {
		out[i] = x.g
	}
	return out, nil
}

// MailboxByAddress returns the mailbox local@domain.
func (s *Store) MailboxByAddress(ctx context.Context, domain, local string) (Mailbox, error) {
	return findMailbox(ctx, s.r, ablage.And(ablage.Eq("domain", domain), ablage.Eq("local_part", local)))
}

// SetMailboxGrant gives grantee rights on a mailbox, or replaces its
// rights; it returns whether a grant existed before. At most limit grants
// per mailbox (<= 0: no limit).
func (s *Store) SetMailboxGrant(ctx context.Context, mailboxID, granteeID string, rights []string, limit int) (bool, error) {
	if mailboxID == granteeID {
		return false, invalidf("a mailbox cannot be shared with itself")
	}
	existed := false
	err := s.tx(ctx, func(r repos) error {
		n, err := r.grants.Count(ctx, ablage.And(ablage.Eq("mailbox_id", mailboxID), ablage.Eq("grantee_id", granteeID)))
		if err != nil {
			return err
		}
		existed = n > 0
		if !existed && limit > 0 {
			n, err := r.grants.Count(ctx, ablage.Eq("mailbox_id", mailboxID))
			if err != nil {
				return err
			}
			if n >= int64(limit) {
				return conflictf("a mailbox can be shared with at most %d mailboxes", limit)
			}
		}
		now := s.now()
		if existed {
			if _, err := r.grants.UpdateWhere(ctx, ablage.And(ablage.Eq("mailbox_id", mailboxID), ablage.Eq("grantee_id", granteeID)),
				ablage.Row{"rights": strings.Join(rights, " ")}); err != nil {
				return err
			}
		} else if err := r.grants.Create(ctx, &grantRow{MailboxID: mailboxID, GranteeID: granteeID,
			Rights: strings.Join(rights, " "), CreatedAt: now}); err != nil {
			if errors.Is(err, ablage.ErrReference) {
				return errMailboxNotFound
			}
			return err
		}
		return grantsChangedByPanel(ctx, r, now, ablage.Eq("id", mailboxID))
	})
	return existed, err
}

// grantsChangedByPanel records that the panel changed the grants of the
// mailboxes matching where (admin_grants_at).
func grantsChangedByPanel(ctx context.Context, r repos, now time.Time, where ablage.Cond) error {
	_, err := r.mailboxes.UpdateWhere(ctx, where, ablage.Row{"admin_grants_at": now})
	return err
}

// DeleteMailboxGrant removes a grant.
func (s *Store) DeleteMailboxGrant(ctx context.Context, mailboxID, granteeID string) error {
	return s.tx(ctx, func(r repos) error {
		n, err := r.grants.DeleteWhere(ctx, ablage.And(ablage.Eq("mailbox_id", mailboxID), ablage.Eq("grantee_id", granteeID)))
		if err != nil {
			return err
		}
		if n == 0 {
			return notFoundf("this mailbox has no access")
		}
		return grantsChangedByPanel(ctx, r, s.now(), ablage.Eq("id", mailboxID))
	})
}

// MakeMailboxShared turns a personal mailbox into a shared one: its
// password, app passwords and "only app passwords" go, and so do the
// grants it holds on other mailboxes (only personal mailboxes may hold
// grants). The time counts as the panel setting the password.
func (s *Store) MakeMailboxShared(ctx context.Context, id string) error {
	now := s.now()
	return s.tx(ctx, func(r repos) error {
		if err := patchMailbox(ctx, r, id, ablage.Row{"kind": "shared", "password_hash": "", "app_passwords_only": false,
			"admin_password_at": now, "admin_app_passwords_only_at": now}); err != nil {
			return err
		}
		// The mailboxes whose grants change get the panel's time first.
		held, err := r.grants.Find(ctx, ablage.Query{Where: ablage.Eq("grantee_id", id)})
		if err != nil {
			return err
		}
		on := make([]string, len(held))
		for i, g := range held {
			on[i] = g.MailboxID
		}
		if err := grantsChangedByPanel(ctx, r, now, ablage.In("id", on...)); err != nil {
			return err
		}
		if _, err := r.appPasswords.DeleteWhere(ctx, ablage.Eq("mailbox_id", id)); err != nil {
			return err
		}
		_, err = r.grants.DeleteWhere(ctx, ablage.Eq("grantee_id", id))
		return err
	})
}

// MakeMailboxPersonal turns a shared mailbox into a personal one with the
// password hash; it no longer keeps copies of sent mail. Its grants stay:
// the person now shares their own mailbox with them.
func (s *Store) MakeMailboxPersonal(ctx context.Context, id, hash string) error {
	return s.updateMailbox(ctx, id, ablage.Row{"kind": "personal", "password_hash": hash, "keep_sent_copy": false, "admin_password_at": s.now()})
}

// SetMailboxKeepSentCopy switches copies of sent mail of a mailbox.
func (s *Store) SetMailboxKeepSentCopy(ctx context.Context, id string, on bool) error {
	return s.updateMailbox(ctx, id, ablage.Row{"keep_sent_copy": on})
}

// replaceGrants replaces all grants on a mailbox with those to the
// mailboxes with the given storage ids (unknown ones are skipped).
func replaceGrants(ctx context.Context, r repos, mailboxID string, grants []GrantByStorage, now time.Time) error {
	if _, err := r.grants.DeleteWhere(ctx, ablage.Eq("mailbox_id", mailboxID)); err != nil {
		return err
	}
	storage := make([]string, len(grants))
	for i, g := range grants {
		storage[i] = g.StorageID
	}
	boxes, err := r.mailboxes.Find(ctx, ablage.Query{Where: ablage.In("storage_id", storage...)})
	if err != nil {
		return err
	}
	byStorage := map[string]string{}
	for _, m := range boxes {
		byStorage[m.StorageID] = m.ID
	}
	for _, g := range grants {
		grantee, ok := byStorage[g.StorageID]
		if !ok || grantee == mailboxID {
			continue
		}
		err := r.grants.Create(ctx, &grantRow{MailboxID: mailboxID, GranteeID: grantee, Rights: strings.Join(g.Rights, " "), CreatedAt: now})
		if err := ignoreDup(err); err != nil {
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
