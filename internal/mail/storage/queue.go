package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
	ID   int64
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
	ID        int64
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
func (s *Store) Enqueue(ctx context.Context, r io.Reader, m QueuedMessage) (int64, error) {
	if len(m.Rcpts) == 0 {
		return 0, errors.New("no recipients")
	}
	if m.StorageID != "" {
		if err := checkID(m.StorageID); err != nil {
			return 0, err
		}
	}
	tmp, size, err := s.spool(r, 0)
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp) // no-op once renamed
	now := s.now()
	if m.Created.IsZero() {
		m.Created = now
	}
	var id int64
	err = s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		name := fmt.Sprintf("%d.%s", now.UnixNano(), randHex(8))
		dst := filepath.Join(s.QueueDir(), name)
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
		ch.placed = append(ch.placed, dst)
		if err := syncDir(s.QueueDir()); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO queue (file, sender, storage_id, created, size) VALUES (?, ?, ?, ?, ?)`,
			name, m.Sender, m.StorageID, m.Created.Unix(), size)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		for _, rc := range m.Rcpts {
			if _, err := tx.Exec(`INSERT INTO queue_rcpts (message_id, address, domain, next_try) VALUES (?, ?, ?, ?)`,
				id, rc.Address, rc.Domain, now.Unix()); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// QueueDue returns up to limit messages with recipients due at now; each
// carries only its pending recipients that are due.
func (s *Store) QueueDue(ctx context.Context, now time.Time, limit int) ([]QueuedMessage, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT q.id, q.file, q.sender, q.storage_id, q.created, q.size,
		r.id, r.address, r.domain, r.state, r.attempts, r.next_try, r.last_error
		FROM queue q JOIN queue_rcpts r ON r.message_id = q.id
		WHERE r.state = 0 AND r.next_try <= ?
		AND q.id IN (SELECT DISTINCT message_id FROM queue_rcpts WHERE state = 0 AND next_try <= ? ORDER BY message_id LIMIT ?)
		ORDER BY q.id, r.id`, now.Unix(), now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	return scanQueue(rows)
}

// QueueMessage returns a queued message with all its recipients.
func (s *Store) QueueMessage(ctx context.Context, id int64) (QueuedMessage, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT q.id, q.file, q.sender, q.storage_id, q.created, q.size,
		r.id, r.address, r.domain, r.state, r.attempts, r.next_try, r.last_error
		FROM queue q JOIN queue_rcpts r ON r.message_id = q.id WHERE q.id = ? ORDER BY r.id`, id)
	if err != nil {
		return QueuedMessage{}, err
	}
	ms, err := scanQueue(rows)
	if err != nil {
		return QueuedMessage{}, err
	}
	if len(ms) == 0 {
		return QueuedMessage{}, ErrNoMessage
	}
	return ms[0], nil
}

func scanQueue(rows *sql.Rows) ([]QueuedMessage, error) {
	defer rows.Close()
	var out []QueuedMessage
	for rows.Next() {
		var m QueuedMessage
		var r QueuedRcpt
		var created, next int64
		if err := rows.Scan(&m.ID, &m.File, &m.Sender, &m.StorageID, &created, &m.Size,
			&r.ID, &r.Address, &r.Domain, &r.State, &r.Attempts, &next, &r.LastError); err != nil {
			return nil, err
		}
		r.NextTry = time.Unix(next, 0)
		if n := len(out); n > 0 && out[n-1].ID == m.ID {
			out[n-1].Rcpts = append(out[n-1].Rcpts, r)
			continue
		}
		m.Created = time.Unix(created, 0)
		m.Rcpts = []QueuedRcpt{r}
		out = append(out, m)
	}
	return out, rows.Err()
}

// QueueNext is the earliest time a pending recipient is due; ok is false
// when nothing is pending.
func (s *Store) QueueNext(ctx context.Context) (t time.Time, ok bool, err error) {
	var next sql.NullInt64
	if err := s.rdb.QueryRowContext(ctx, `SELECT min(next_try) FROM queue_rcpts WHERE state = 0`).Scan(&next); err != nil {
		return time.Time{}, false, err
	}
	if !next.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(next.Int64, 0), true, nil
}

// QueueUpdate saves state, attempts, next try and last error of recipients
// (by ID).
func (s *Store) QueueUpdate(ctx context.Context, rcpts []QueuedRcpt) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		for _, r := range rcpts {
			if _, err := tx.Exec(`UPDATE queue_rcpts SET state = ?, attempts = ?, next_try = ?, last_error = ? WHERE id = ?`,
				r.State, r.Attempts, r.NextTry.Unix(), r.LastError, r.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// QueueRemoveIfDone deletes a message whose recipients are all delivered or
// failed. It reports whether it did.
func (s *Store) QueueRemoveIfDone(ctx context.Context, id int64) (bool, error) {
	var file string
	err := s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		var pending int
		if err := tx.QueryRow(`SELECT count(*) FROM queue_rcpts WHERE message_id = ? AND state = 0`, id).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return nil
		}
		if err := tx.QueryRow(`SELECT file FROM queue WHERE id = ?`, id).Scan(&file); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if _, err := tx.Exec(`DELETE FROM queue_rcpts WHERE message_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM queue WHERE id = ?`, id)
		return err
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
	var n int
	err := s.rdb.QueryRowContext(ctx, `SELECT count(DISTINCT message_id) FROM queue_rcpts WHERE state = 0`).Scan(&n)
	return n, err
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
func (s *Store) reconcileQueue(tx *sql.Tx) (missing, stray int, err error) {
	rows, err := tx.Query(`SELECT id, file FROM queue`)
	if err != nil {
		return 0, 0, err
	}
	known := map[string]bool{}
	var gone []int64
	for rows.Next() {
		var id int64
		var file string
		if err := rows.Scan(&id, &file); err != nil {
			rows.Close()
			return 0, 0, err
		}
		known[file] = true
		if _, err := os.Stat(filepath.Join(s.QueueDir(), file)); errors.Is(err, os.ErrNotExist) {
			gone = append(gone, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, id := range gone {
		if _, err := tx.Exec(`DELETE FROM queue_rcpts WHERE message_id = ?`, id); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(`DELETE FROM queue WHERE id = ?`, id); err != nil {
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
	err := s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		for i, l := range limits {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM send_log WHERE key = ? AND at > ?`, l.Key, l.Since.Unix()).Scan(&n); err != nil {
				return err
			}
			if n >= l.Max {
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
			if _, err := tx.Exec(`INSERT INTO send_log (key, at) VALUES (?, ?)`, l.Key, now.Unix()); err != nil {
				return err
			}
		}
		return nil
	})
	return reached, err
}

// MarkOnce records (key, value) at now unless the same pair was recorded
// after since. It returns whether it added one and how many entries with
// distinct values the key has after since.
func (s *Store) MarkOnce(ctx context.Context, key, value string, since, now time.Time) (added bool, distinct int, err error) {
	err = s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM send_log WHERE key = ? AND value = ? AND at > ?`, key, value, since.Unix()).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := tx.Exec(`INSERT INTO send_log (key, value, at) VALUES (?, ?, ?)`, key, value, now.Unix()); err != nil {
				return err
			}
			added = true
		}
		return tx.QueryRow(`SELECT count(DISTINCT value) FROM send_log WHERE key = ? AND at > ?`, key, since.Unix()).Scan(&distinct)
	})
	return added, distinct, err
}

// CountMarks is the number of entries of key after since.
func (s *Store) CountMarks(ctx context.Context, key string, since time.Time) (int, error) {
	var n int
	err := s.rdb.QueryRowContext(ctx, `SELECT count(*) FROM send_log WHERE key = ? AND at > ?`, key, since.Unix()).Scan(&n)
	return n, err
}

// PruneSendLog deletes send log entries older than before.
func (s *Store) PruneSendLog(ctx context.Context, before time.Time) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		_, err := tx.Exec(`DELETE FROM send_log WHERE at < ?`, before.Unix())
		return err
	})
}

// Blocked returns the block state of one mailbox.
func (s *Store) Blocked(ctx context.Context, id string) (Block, error) {
	var b Block
	var at int64
	err := s.rdb.QueryRowContext(ctx, `SELECT reason, at FROM blocks WHERE storage_id = ? AND blocked = 1`, id).Scan(&b.Reason, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Block{}, nil
	}
	if err != nil {
		return Block{}, err
	}
	b.Blocked = true
	b.At = time.Unix(at, 0).UTC()
	return b, nil
}

// mailboxSendKeys are the send log keys that belong to a mailbox.
func mailboxSendKeys(id string) []any {
	return []any{KeySends + id, KeyHourlyHits + id, KeyNetworks + id, KeyNotices + id}
}
