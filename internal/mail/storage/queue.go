package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/danielgtmn/ablage"
)

// The outbound queue: one row per message in queue, one per recipient in
// queue_rcpts, the message itself as a file under queue/. Enqueue answers
// only once the file and its directory are fsynced and the rows are
// committed, the same discipline as Append.
//
// The envelope sender is stored as it goes on the wire, so a later phase
// can put an SRS address there; StorageID says which local mailbox gets
// the bounce, independent of the envelope sender.

const queueDir = "queue"

var queueFileRe = regexp.MustCompile(`^[0-9]{1,20}\.[0-9a-f]{16}$`)

// RcptState is the delivery state of one recipient.
type RcptState int

const (
	RcptPending   RcptState = 0
	RcptDelivered RcptState = 1
	RcptFailed    RcptState = 2
)

// QueuedMessage is a message waiting in the outbound queue.
type QueuedMessage struct {
	ID   string
	File string
	// Sender is the envelope sender used on the wire ("" for the null
	// sender: never bounced).
	Sender string
	// StorageID is the local mailbox that receives bounces ("" for none).
	StorageID string
	Created   time.Time
	Size      int64
	Rcpts     []QueuedRcpt
}

// QueuedRcpt is one recipient of a queued message.
type QueuedRcpt struct {
	ID        string
	Address   string
	Domain    string
	State     RcptState
	Attempts  int
	NextTry   time.Time
	LastError string
}

// QueueDir is the directory of queued message files.
func (s *Store) QueueDir() string { return filepath.Join(s.dir, queueDir) }

// Enqueue stores a message for outbound delivery to m.Rcpts (Address and
// Domain are used; the rest is set here). It returns the message id.
func (s *Store) Enqueue(ctx context.Context, r io.Reader, m QueuedMessage) (string, error) {
	if len(m.Rcpts) == 0 {
		return "", errors.New("no recipients")
	}
	if m.StorageID != "" {
		if err := checkID(m.StorageID); err != nil {
			return "", err
		}
	}
	tmp, size, err := s.spool(r, 0)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp) // no-op once renamed
	now := s.now()
	if m.Created.IsZero() {
		m.Created = now
	}
	var id string
	err = s.write(ctx, func(tx *repos, ch *changes) error {
		name := fmt.Sprintf("%d.%s", now.UnixNano(), randHex(8))
		dst := filepath.Join(s.QueueDir(), name)
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
		ch.placed = append(ch.placed, dst)
		if err := syncDir(s.QueueDir()); err != nil {
			return err
		}
		q := queueRec{File: name, Sender: m.Sender, StorageID: m.StorageID, Created: sec(m.Created), Size: size}
		if err := tx.queue.Create(ctx, &q); err != nil {
			return err
		}
		id = q.ID
		for i, rc := range m.Rcpts {
			if err := tx.queueRcpts.Create(ctx, &queueRcptRec{MessageID: q.ID, Pos: int64(i), Address: rc.Address, Domain: rc.Domain, NextTry: sec(now)}); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// due is the condition of pending recipients due at now.
func due(now time.Time) ablage.Cond {
	return ablage.And(ablage.Eq("state", int64(RcptPending)), ablage.Lte("next_try", sec(now)))
}

// QueueDue returns up to limit messages with recipients due at now (the
// oldest first); each carries only its pending recipients that are due.
func (s *Store) QueueDue(ctx context.Context, now time.Time, limit int) ([]QueuedMessage, error) {
	var out []QueuedMessage
	err := s.read(ctx, func(tx *repos) error {
		rcpts, err := tx.queueRcpts.Find(ctx, ablage.Query{Where: due(now), OrderBy: []ablage.Order{ablage.Asc("pos")}})
		if err != nil || len(rcpts) == 0 {
			return err
		}
		ids := make([]string, len(rcpts))
		for i, r := range rcpts {
			ids[i] = r.MessageID
		}
		msgs, err := tx.queue.Find(ctx, ablage.Query{Where: ablage.In("id", ids...), OrderBy: []ablage.Order{ablage.Asc("created")}, Limit: limit})
		if err != nil {
			return err
		}
		out = joinRcpts(msgs, rcpts)
		return nil
	})
	return out, err
}

// joinRcpts gives each message its recipients among rcpts (in their
// order).
func joinRcpts(msgs []queueRec, rcpts []queueRcptRec) []QueuedMessage {
	by := map[string][]QueuedRcpt{}
	for _, r := range rcpts {
		by[r.MessageID] = append(by[r.MessageID], QueuedRcpt{ID: r.ID, Address: r.Address, Domain: r.Domain, State: RcptState(r.State),
			Attempts: int(r.Attempts), NextTry: local(r.NextTry), LastError: r.LastError})
	}
	out := make([]QueuedMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, QueuedMessage{ID: m.ID, File: m.File, Sender: m.Sender, StorageID: m.StorageID, Created: local(m.Created), Size: m.Size, Rcpts: by[m.ID]})
	}
	return out
}

// QueueMessage returns a queued message with all its recipients.
func (s *Store) QueueMessage(ctx context.Context, id string) (QueuedMessage, error) {
	var out []QueuedMessage
	err := s.read(ctx, func(tx *repos) error {
		m, err := tx.queue.Get(ctx, id)
		if errors.Is(err, ablage.ErrNotFound) {
			return ErrNoMessage
		}
		if err != nil {
			return err
		}
		rcpts, err := tx.queueRcpts.Find(ctx, ablage.Query{Where: ablage.Eq("message_id", id), OrderBy: []ablage.Order{ablage.Asc("pos")}})
		if err != nil {
			return err
		}
		if len(rcpts) == 0 {
			return ErrNoMessage
		}
		out = joinRcpts([]queueRec{m}, rcpts)
		return nil
	})
	if err != nil {
		return QueuedMessage{}, err
	}
	return out[0], nil
}

// QueueNext is the earliest time a pending recipient is due; ok is false
// when nothing is pending.
func (s *Store) QueueNext(ctx context.Context) (t time.Time, ok bool, err error) {
	rows, err := s.r.queueRcpts.Aggregate(ctx, ablage.Aggregation{Where: ablage.Eq("state", int64(RcptPending)), Values: []ablage.Agg{ablage.Min("next_try", "next")}})
	if err != nil {
		return time.Time{}, false, err
	}
	next, ok := rows[0]["next"].(time.Time)
	if !ok {
		return time.Time{}, false, nil
	}
	return local(next), true, nil
}

// QueueClaim takes the recipients (by ID) that are still pending and due
// at now for one delivery attempt: their next try moves to until, so no
// other attempt picks them up meanwhile (and they are tried again after
// until should the attempt never finish). It returns the IDs it claimed;
// only those may be delivered.
func (s *Store) QueueClaim(ctx context.Context, ids []string, now, until time.Time) ([]string, error) {
	var claimed []string
	err := s.write(ctx, func(tx *repos, _ *changes) error {
		claimed = claimed[:0]
		for _, id := range ids {
			n, err := tx.queueRcpts.UpdateWhere(ctx, ablage.And(ablage.Eq("id", id), due(now)), ablage.Row{"next_try": sec(until)})
			if err != nil {
				return err
			}
			if n == 1 {
				claimed = append(claimed, id)
			}
		}
		return nil
	})
	return claimed, err
}

// QueueUpdate saves state, attempts, next try and last error of recipients
// (by ID).
func (s *Store) QueueUpdate(ctx context.Context, rcpts []QueuedRcpt) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		for _, r := range rcpts {
			if _, err := tx.queueRcpts.UpdateWhere(ctx, ablage.Eq("id", r.ID), ablage.Row{
				"state": int64(r.State), "attempts": int64(r.Attempts), "next_try": sec(r.NextTry), "last_error": r.LastError,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// QueueRemoveIfDone deletes a message whose recipients are all delivered or
// failed. It reports whether it did.
func (s *Store) QueueRemoveIfDone(ctx context.Context, id string) (bool, error) {
	var file string
	err := s.write(ctx, func(tx *repos, _ *changes) error {
		pending, err := tx.queueRcpts.Count(ctx, ablage.And(ablage.Eq("message_id", id), ablage.Eq("state", int64(RcptPending))))
		if err != nil || pending > 0 {
			return err
		}
		m, err := tx.queue.Get(ctx, id)
		if errors.Is(err, ablage.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		file = m.File
		// The recipients go with it (cascade).
		return tx.queue.Delete(ctx, id)
	})
	if err != nil || file == "" {
		return false, err
	}
	// After the commit: a crash before this leaves a file without a row,
	// which ReconcileQueue removes.
	os.Remove(filepath.Join(s.QueueDir(), file))
	return true, nil
}

// QueueLength is the number of messages with recipients still pending.
func (s *Store) QueueLength(ctx context.Context) (int, error) {
	rows, err := s.r.queueRcpts.Aggregate(ctx, ablage.Aggregation{Where: ablage.Eq("state", int64(RcptPending)), GroupBy: []string{"message_id"}})
	return len(rows), err
}

// OpenQueued opens the file of a queued message.
func (s *Store) OpenQueued(m QueuedMessage) (*os.File, error) {
	if !queueFileRe.MatchString(m.File) {
		return nil, ErrNoMessage
	}
	return os.Open(filepath.Join(s.QueueDir(), m.File))
}

// reconcileQueue drops queue rows without a file and files without a row
// (a crash between writing the file and committing: never acknowledged).
func (s *Store) reconcileQueue(ctx context.Context, tx *repos) (missing, stray int, err error) {
	list, err := tx.queue.Find(ctx, ablage.Query{})
	if err != nil {
		return 0, 0, err
	}
	known := map[string]bool{}
	var gone []string
	for _, q := range list {
		known[q.File] = true
		if _, err := os.Stat(filepath.Join(s.QueueDir(), q.File)); errors.Is(err, os.ErrNotExist) {
			gone = append(gone, q.ID)
		}
	}
	if len(gone) > 0 {
		if _, err := tx.queue.DeleteWhere(ctx, ablage.In("id", gone...)); err != nil {
			return 0, 0, err
		}
	}
	entries, err := os.ReadDir(s.QueueDir())
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if known[e.Name()] {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") || e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(s.QueueDir(), e.Name())); err == nil {
			stray++
		}
	}
	return len(gone), stray, nil
}

// Send log keys. Entries are counted in sliding windows by the submission
// server; Unblock clears those of a mailbox.
const (
	// KeySends + storage id: one entry per accepted message.
	KeySends = "send:"
	// KeyDomainSends + domain: one entry per accepted message.
	KeyDomainSends = "domain:"
	// KeyHourlyHits + storage id: one entry per hour in which the mailbox
	// hit its hourly limit.
	KeyHourlyHits = "hit:"
	// KeyNetworks + storage id: one entry per network a mailbox signed in
	// from (value: the network), at most one per network and hour.
	KeyNetworks = "net:"
	// KeyNotices + storage id: limit events already recorded (value: the
	// kind of limit), so there is at most one per hour.
	KeyNotices = "notice:"
	// KeyBlocklistRefused + zone: a blocklist refused our queries (an
	// event is recorded at most once a day).
	KeyBlocklistRefused = "blrefused:"
)

// SendLimit is at most Max entries of Key since Since.
type SendLimit struct {
	Key   string
	Since time.Time
	Max   int
}

// ReserveSend checks the limits in order and, if none is reached, records
// one entry at now for every distinct key among them, in one transaction.
// It returns the index of the first limit reached, or -1.
func (s *Store) ReserveSend(ctx context.Context, limits []SendLimit, now time.Time) (int, error) {
	reached := -1
	err := s.write(ctx, func(tx *repos, _ *changes) error {
		for i, l := range limits {
			n, err := tx.sendLog.Count(ctx, ablage.And(ablage.Eq("key", l.Key), ablage.Gt("at", sec(l.Since))))
			if err != nil {
				return err
			}
			if n >= int64(l.Max) {
				reached = i
				return nil
			}
		}
		done := map[string]bool{}
		for _, l := range limits {
			if done[l.Key] {
				continue
			}
			done[l.Key] = true
			if err := tx.sendLog.Create(ctx, &sendLogRec{Key: l.Key, At: sec(now)}); err != nil {
				return err
			}
		}
		return nil
	})
	return reached, err
}

// ReleaseSend takes back what ReserveSend recorded at the same now for the
// same limits (one entry for every distinct key), for a message that was
// not accepted after all: its retry must not count twice.
func (s *Store) ReleaseSend(ctx context.Context, limits []SendLimit, now time.Time) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		done := map[string]bool{}
		for _, l := range limits {
			if done[l.Key] {
				continue
			}
			done[l.Key] = true
			e, err := tx.sendLog.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("key", l.Key), ablage.Eq("value", ""), ablage.Eq("at", sec(now)))})
			if errors.Is(err, ablage.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if err := tx.sendLog.Delete(ctx, e.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// MarkOnce records (key, value) at now unless the same pair was recorded
// after since. It returns whether it added one and how many entries with
// distinct values the key has after since.
func (s *Store) MarkOnce(ctx context.Context, key, value string, since, now time.Time) (added bool, distinct int, err error) {
	err = s.write(ctx, func(tx *repos, _ *changes) error {
		after := ablage.And(ablage.Eq("key", key), ablage.Gt("at", sec(since)))
		n, err := tx.sendLog.Count(ctx, ablage.And(after, ablage.Eq("value", value)))
		if err != nil {
			return err
		}
		if n == 0 {
			if err := tx.sendLog.Create(ctx, &sendLogRec{Key: key, Value: value, At: sec(now)}); err != nil {
				return err
			}
			added = true
		}
		rows, err := tx.sendLog.Aggregate(ctx, ablage.Aggregation{Where: after, GroupBy: []string{"value"}})
		distinct = len(rows)
		return err
	})
	return added, distinct, err
}

// CountMarks is the number of entries of key after since.
func (s *Store) CountMarks(ctx context.Context, key string, since time.Time) (int, error) {
	n, err := s.r.sendLog.Count(ctx, ablage.And(ablage.Eq("key", key), ablage.Gt("at", sec(since))))
	return int(n), err
}

// PruneSendLog deletes send log entries older than before.
func (s *Store) PruneSendLog(ctx context.Context, before time.Time) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		_, err := tx.sendLog.DeleteWhere(ctx, ablage.Lt("at", sec(before)))
		return err
	})
}

// Blocked returns the block state of one mailbox.
func (s *Store) Blocked(ctx context.Context, id string) (Block, error) {
	b, err := s.r.blocks.Get(ctx, id)
	if errors.Is(err, ablage.ErrNotFound) || err == nil && !b.Blocked {
		return Block{}, nil
	}
	if err != nil {
		return Block{}, err
	}
	return Block{Blocked: true, Reason: b.Reason, At: b.At.UTC()}, nil
}

// mailboxSendKeys are the send log keys that belong to a mailbox.
func mailboxSendKeys(id string) []string {
	return []string{KeySends + id, KeyHourlyHits + id, KeyNetworks + id, KeyNotices + id}
}
