// Package storage keeps the mail of every mailbox: an SQLite index (mail.db)
// plus one immutable file per message under messages/<storage_id>/. Folders
// have IMAP semantics (UIDVALIDITY, UIDNEXT, per-message UIDs and flags), so
// the IMAP server can work on it directly. Everything is keyed by storage_id,
// never by address: a reused address or a restored panel database can never
// reach another mailbox's mail.
//
// The same database holds the service's own state that must survive a
// config push: storage_id bindings, sending blocks and events for the panel.
//
// Message files contain the raw message exactly as stored (RFC 5322, as
// received). Parsed data (envelope, body structure) can later be cached in
// new columns of the messages table without changing the files.
package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"

	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, no cgo)
)

// Errors callers map to protocol answers.
var (
	ErrNoFolder     = errors.New("no such folder")
	ErrFolderExists = errors.New("folder already exists")
	ErrNoMessage    = errors.New("no such message")
	ErrInvalidName  = errors.New("invalid folder name")
	ErrInbox        = errors.New("INBOX cannot be deleted")
	ErrInvalidFlag  = errors.New("invalid flag")
	ErrOverQuota    = errors.New("mailbox is over its quota")
	ErrTooLarge     = errors.New("message is too large")
	ErrInvalidID    = errors.New("invalid storage id")
)

// Options configure a Store.
type Options struct {
	// Quota returns the quota in bytes of a mailbox (0: no limit). It is
	// called on every append and copy, so it should be cheap. Nil: no limit.
	Quota func(storageID string) int64
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
}

// Store is the mail storage of the service.
type Store struct {
	dir  string
	wdb  *sql.DB // the single writer connection
	rdb  *sql.DB // read pool
	opts Options

	// wmu serializes writes, file renames that belong to them and the
	// change notifications, so watchers see changes in commit order.
	wmu sync.Mutex

	watchMu  sync.Mutex
	watchers map[string]map[int]func(Change)
	watchSeq int
}

// File and directory names inside the data directory.
const (
	dbName       = "mail.db"
	messagesDir  = "messages"
	tmpDir       = "tmp"
	trashDir     = "trash"
	RestoredFile = "restored"
)

var fileNameRe = regexp.MustCompile(`^[0-9]{1,20}\.[0-9a-f]{16}$`)

// Open opens (or creates) the storage in dir. Call Reconcile once before
// serving.
func Open(dir string, opts Options) (*Store, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	for _, d := range []string{dir, filepath.Join(dir, messagesDir), filepath.Join(dir, tmpDir), filepath.Join(dir, trashDir), filepath.Join(dir, queueDir)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	file := filepath.Join(dir, dbName)
	// Create the file ourselves so it never exists with wider permissions.
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	dsn := file + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
	wdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer connection: writes never wait on each other inside SQLite.
	wdb.SetMaxOpenConns(1)
	if err := migrate(wdb); err != nil {
		wdb.Close()
		return nil, fmt.Errorf("migrate %s: %w", file, err)
	}
	rdb, err := sql.Open("sqlite", dsn+"&_pragma=query_only(1)")
	if err != nil {
		wdb.Close()
		return nil, err
	}
	rdb.SetMaxOpenConns(8)
	return &Store{dir: dir, wdb: wdb, rdb: rdb, opts: opts, watchers: map[string]map[int]func(Change){}}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	err := s.rdb.Close()
	if werr := s.wdb.Close(); err == nil {
		err = werr
	}
	return err
}

// Dir is the data directory.
func (s *Store) Dir() string { return s.dir }

// TempDir is a directory on the same file system as the messages, for
// spooling.
func (s *Store) TempDir() string { return filepath.Join(s.dir, tmpDir) }

// migrations are applied in order; the index is PRAGMA user_version. Never
// edit one that has been released, add a new one.
var migrations = []string{
	`
CREATE TABLE meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
CREATE TABLE mailboxes (
	storage_id TEXT PRIMARY KEY,
	created    INTEGER NOT NULL,
	used_bytes INTEGER NOT NULL DEFAULT 0,
	messages   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE folders (
	id           INTEGER PRIMARY KEY,
	storage_id   TEXT NOT NULL REFERENCES mailboxes(storage_id) ON DELETE CASCADE,
	name         TEXT NOT NULL,
	special_use  TEXT NOT NULL DEFAULT '',
	uid_validity INTEGER NOT NULL,
	uid_next     INTEGER NOT NULL DEFAULT 1,
	subscribed   INTEGER NOT NULL DEFAULT 0,
	UNIQUE (storage_id, name)
);
CREATE TABLE messages (
	id            INTEGER PRIMARY KEY,
	folder_id     INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
	storage_id    TEXT NOT NULL,
	uid           INTEGER NOT NULL,
	file          TEXT NOT NULL,
	size          INTEGER NOT NULL,
	internal_date INTEGER NOT NULL,
	flags         TEXT NOT NULL DEFAULT '',
	UNIQUE (folder_id, uid)
);
CREATE INDEX messages_file ON messages(storage_id, file);
CREATE TABLE bindings (
	storage_id TEXT PRIMARY KEY,
	address    TEXT NOT NULL,
	bound      INTEGER NOT NULL
);
CREATE TABLE blocks (
	storage_id TEXT PRIMARY KEY,
	blocked    INTEGER NOT NULL,
	reason     TEXT NOT NULL,
	at         INTEGER NOT NULL
);
CREATE TABLE events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	at         INTEGER NOT NULL,
	kind       TEXT NOT NULL,
	storage_id TEXT NOT NULL DEFAULT '',
	address    TEXT NOT NULL DEFAULT '',
	detail     TEXT NOT NULL
);
CREATE INDEX events_at ON events(at);
`,
	// 2: outbound queue and the send log of the submission limits.
	`
CREATE TABLE queue (
	id         INTEGER PRIMARY KEY,
	file       TEXT NOT NULL UNIQUE,
	sender     TEXT NOT NULL,
	storage_id TEXT NOT NULL DEFAULT '',
	created    INTEGER NOT NULL,
	size       INTEGER NOT NULL
);
CREATE TABLE queue_rcpts (
	id         INTEGER PRIMARY KEY,
	message_id INTEGER NOT NULL REFERENCES queue(id) ON DELETE CASCADE,
	address    TEXT NOT NULL,
	domain     TEXT NOT NULL,
	state      INTEGER NOT NULL DEFAULT 0,
	attempts   INTEGER NOT NULL DEFAULT 0,
	next_try   INTEGER NOT NULL,
	last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX queue_rcpts_due ON queue_rcpts(state, next_try);
CREATE INDEX queue_rcpts_message ON queue_rcpts(message_id);
CREATE TABLE send_log (
	key   TEXT NOT NULL,
	value TEXT NOT NULL DEFAULT '',
	at    INTEGER NOT NULL
);
CREATE INDEX send_log_key ON send_log(key, at);
`,
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// write runs fn in a transaction on the writer connection while holding
// wmu, then delivers the changes fn collected, still in commit order.
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx, ch *changes) error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.writeLocked(ctx, fn)
}

func (s *Store) writeLocked(ctx context.Context, fn func(tx *sql.Tx, ch *changes) error) error {
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var ch changes
	if err := fn(tx, &ch); err != nil {
		tx.Rollback()
		ch.rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		ch.rollback()
		return err
	}
	ch.commit()
	origin := ctx.Value(originKey{})
	for _, c := range ch.list {
		c.Change.Origin = origin
		s.notify(c.storageID, c.Change)
	}
	return nil
}

func (s *Store) now() time.Time { return s.opts.Now() }

func (s *Store) quota(storageID string) int64 {
	if s.opts.Quota == nil {
		return 0
	}
	return s.opts.Quota(storageID)
}

func checkID(id string) error {
	if !config.ValidStorageID(id) {
		return ErrInvalidID
	}
	return nil
}

func (s *Store) mailboxDir(id string) string { return filepath.Join(s.dir, messagesDir, id) }

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// syncDir fsyncs a directory so that renames and removals in it survive a
// crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// nextUIDValidity returns a UIDVALIDITY that was never handed out before:
// larger than the last one and at least the current time in seconds, so a
// restored (older) database still yields new values.
func (s *Store) nextUIDValidity(tx *sql.Tx) (uint32, error) {
	var last int64
	err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'uid_validity'`).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	v := max(last+1, s.now().Unix())
	if v > 0xffffffff {
		return 0, errors.New("UIDVALIDITY exhausted")
	}
	if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('uid_validity', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, v); err != nil {
		return 0, err
	}
	return uint32(v), nil
}
