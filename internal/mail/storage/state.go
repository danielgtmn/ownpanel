package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/danielgtmn/ablage"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// BindingConflictError is returned by Bind when a storage id with data was
// bound to another address before.
type BindingConflictError struct {
	StorageID string
	Bound     string
	Requested string
}

func (e *BindingConflictError) Error() string {
	return fmt.Sprintf("storage id %s belongs to %s, not %s", e.StorageID, e.Bound, e.Requested)
}

// Bind records which address each storage id belongs to. The first binding
// of a storage id is kept as long as the mailbox has data: binding it to
// another address is refused (a mix-up in the panel must never hand one
// person's mail to another address). Without data a binding may change.
// Either all bindings are applied or none.
func (s *Store) Bind(ctx context.Context, bindings map[string]string) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		for id, addr := range bindings {
			if err := checkID(id); err != nil {
				return err
			}
			b, err := tx.bindings.Get(ctx, id)
			if errors.Is(err, ablage.ErrNotFound) {
				if err := tx.bindings.Create(ctx, &bindingRec{ID: id, Address: addr, Bound: sec(s.now())}); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if b.Address == addr {
				continue
			}
			hasData, err := tx.mailboxes.Exists(ctx, id)
			if err != nil {
				return err
			}
			if hasData {
				return &BindingConflictError{StorageID: id, Bound: b.Address, Requested: addr}
			}
			if err := tx.bindings.Patch(ctx, id, ablage.Row{"address": addr, "bound": sec(s.now())}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Block is the sending block state of a mailbox.
type Block struct {
	Blocked bool
	Reason  string
	At      time.Time
}

// SetBlocked blocks a mailbox from sending (receiving goes on).
func (s *Store) SetBlocked(ctx context.Context, id, reason string) error {
	if err := checkID(id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *repos, _ *changes) error {
		return tx.blocks.Upsert(ctx, &blockRec{ID: id, Blocked: true, Reason: reason, At: sec(s.now())})
	})
}

// Unblock lifts a block and clears the send counters of the mailbox (sent
// messages, hourly limit hits, sign-in networks), so the reasons for the
// block do not block it again at once. It reports whether the mailbox was
// blocked.
func (s *Store) Unblock(ctx context.Context, id string) (bool, error) {
	if err := checkID(id); err != nil {
		return false, err
	}
	var was bool
	err := s.write(ctx, func(tx *repos, _ *changes) error {
		n, err := tx.blocks.DeleteWhere(ctx, ablage.And(ablage.Eq("id", id), ablage.Eq("blocked", true)))
		if err != nil {
			return err
		}
		was = n > 0
		_, err = tx.sendLog.DeleteWhere(ctx, ablage.In("key", mailboxSendKeys(id)...))
		return err
	})
	return was, err
}

// Blocks returns the block state of every blocked mailbox.
func (s *Store) Blocks(ctx context.Context) (map[string]Block, error) {
	list, err := s.r.blocks.Find(ctx, ablage.Query{Where: ablage.Eq("blocked", true)})
	if err != nil {
		return nil, err
	}
	out := map[string]Block{}
	for _, b := range list {
		out[b.ID] = Block{Blocked: true, Reason: b.Reason, At: b.At.UTC()}
	}
	return out, nil
}

// MaxEvents is the most Events returns at once.
const MaxEvents = 500

// EventRetention is how long events are kept.
const EventRetention = 30 * 24 * time.Hour

// AddEvent records an event for the panel. Detail must not contain secrets.
func (s *Store) AddEvent(ctx context.Context, e config.Event) error {
	at := e.At
	if at.IsZero() {
		at = s.now()
	}
	return s.write(ctx, func(tx *repos, _ *changes) error {
		return insertEvent(ctx, tx, e, at)
	})
}

// insertEvent records an event with the next sequence number, which is
// the panel's cursor (config.Event.ID): it only grows, also after pruning
// or a restart, as the AUTOINCREMENT id it replaces did.
func insertEvent(ctx context.Context, tx *repos, e config.Event, at time.Time) error {
	account := ""
	if e.Account != nil {
		b, err := json.Marshal(e.Account)
		if err != nil {
			return err
		}
		account = string(b)
	}
	seq, err := nextMeta(ctx, tx, metaEventSeq)
	if err != nil {
		return err
	}
	return tx.events.Create(ctx, &eventRec{Seq: seq, At: sec(at), Kind: e.Kind, StorageID: e.StorageID, Address: e.Address, Detail: e.Detail, Account: account})
}

// Events returns up to MaxEvents events with an id (sequence number)
// greater than after, oldest first.
func (s *Store) Events(ctx context.Context, after int64) ([]config.Event, error) {
	list, err := s.r.events.Find(ctx, ablage.Query{Where: ablage.Gt("seq", after), OrderBy: []ablage.Order{ablage.Asc("seq")}, Limit: MaxEvents})
	if err != nil {
		return nil, err
	}
	out := []config.Event{}
	for _, r := range list {
		e := config.Event{ID: r.Seq, At: r.At.UTC(), Kind: r.Kind, StorageID: r.StorageID, Address: r.Address, Detail: r.Detail}
		if r.Account != "" {
			e.Account = &config.AccountChange{}
			if err := json.Unmarshal([]byte(r.Account), e.Account); err != nil {
				return nil, fmt.Errorf("event %d: %w", e.ID, err)
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// AccountOverlays returns the stored account overlays (directory.Overlay
// as JSON) by storage id.
func (s *Store) AccountOverlays(ctx context.Context) (map[string][]byte, error) {
	list, err := s.r.overlays.Find(ctx, ablage.Query{})
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, o := range list {
		out[o.ID] = []byte(o.Data)
	}
	return out, nil
}

// PutAccountOverlays stores or (nil data) deletes overlays, atomically.
func (s *Store) PutAccountOverlays(ctx context.Context, overlays map[string][]byte) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		return putOverlays(ctx, tx, overlays)
	})
}

func putOverlays(ctx context.Context, tx *repos, overlays map[string][]byte) error {
	for id, data := range overlays {
		if err := checkID(id); err != nil {
			return err
		}
		var err error
		if data == nil {
			_, err = tx.overlays.DeleteWhere(ctx, ablage.Eq("id", id))
		} else {
			err = tx.overlays.Upsert(ctx, &overlayRec{ID: id, Data: string(data)})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// SaveAccountChange stores a mailbox's overlay (data, nil deletes it) and
// records the event of the change, in one transaction: a change is never
// applied without being reported, nor reported without being kept.
func (s *Store) SaveAccountChange(ctx context.Context, storageID string, data []byte, e config.Event) error {
	at := e.At
	if at.IsZero() {
		at = s.now()
	}
	return s.write(ctx, func(tx *repos, _ *changes) error {
		if err := putOverlays(ctx, tx, map[string][]byte{storageID: data}); err != nil {
			return err
		}
		return insertEvent(ctx, tx, e, at)
	})
}

// PruneEvents deletes events older than EventRetention. Sequence numbers
// are never reused, so the panel's cursor stays valid.
func (s *Store) PruneEvents(ctx context.Context) error {
	cutoff := sec(s.now().Add(-EventRetention))
	return s.write(ctx, func(tx *repos, _ *changes) error {
		_, err := tx.events.DeleteWhere(ctx, ablage.Lt("at", cutoff))
		return err
	})
}
