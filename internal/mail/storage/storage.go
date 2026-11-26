// Package storage keeps the mail of every mailbox: an SQLite index (mail.db,
// kept with ablage repositories, see records.go)
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

	"github.com/danielgtmn/ablage"
	"github.com/danielgtmn/ablage/sqlstore"

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
	dir string
	// wdb is the single writer connection, rdb a pool of read-only
	// connections. Both are SQLite in WAL mode, so reads never wait for a
	// write. ws and rs are the ablage stores on them; w and r their
	// repositories.
	wdb, rdb *sql.DB
	ws, rs   *sqlstore.Store
	w, r     *repos
	opts     Options

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
// serving. An index written before ablage is converted first (see
// migrateLegacy), after a backup next to it.
func Open(dir string, opts Options) (*Store, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	for _, d := range []string{dir, filepath.Join(dir, messagesDir), filepath.Join(dir, tmpDir), filepath.Join(dir, trashDir), filepath.Join(dir, queueDir), filepath.Join(dir, importsDir), filepath.Join(dir, davDir)} {
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
	// Opening the database stays outside ablage: the DSN carries the
	// pragmas. Writes are serialized by wmu on one connection; the reads
	// use a pool of read-only connections that see the last commit (WAL)
	// without waiting for a write in progress. Read transactions stay
	// deferred: BEGIN IMMEDIATE would take the write lock.
	dsn := file + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
	wdb, err := sql.Open("sqlite", dsn+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	// One writer connection: writes never wait on each other inside SQLite.
	wdb.SetMaxOpenConns(1)
	s := &Store{dir: dir, wdb: wdb, opts: opts, watchers: map[string]map[int]func(Change){}}
	fail := func(err error) (*Store, error) {
		s.Close()
		return nil, err
	}
	if s.ws, err = sqlstore.New(wdb, sqlstore.SQLite); err != nil {
		return fail(err)
	}
	if s.w, err = newRepos(s.ws); err != nil {
		return fail(err)
	}
	if err := s.migrate(context.Background(), file); err != nil {
		return fail(fmt.Errorf("migrate %s: %w", file, err))
	}
	if s.rdb, err = sql.Open("sqlite", dsn+"&_pragma=query_only(1)"); err != nil {
		return fail(err)
	}
	s.rdb.SetMaxOpenConns(8)
	if s.rs, err = sqlstore.New(s.rdb, sqlstore.SQLite); err != nil {
		return fail(err)
	}
	if s.r, err = newRepos(s.rs); err != nil {
		return fail(err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	var err error
	if s.rdb != nil {
		err = s.rdb.Close()
	}
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

// write runs fn in a transaction on the writer connection while holding
// wmu, then delivers the changes fn collected, still in commit order.
func (s *Store) write(ctx context.Context, fn func(tx *repos, ch *changes) error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.writeLocked(ctx, fn)
}

func (s *Store) writeLocked(ctx context.Context, fn func(tx *repos, ch *changes) error) error {
	var ch changes
	err := s.ws.Tx(ctx, func(tx ablage.Store) error {
		return fn(s.w.with(tx), &ch)
	})
	if err != nil {
		// fn failed, or the commit did: either way nothing was written.
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

// read runs fn in a read transaction, for reads that must be consistent
// with each other.
func (s *Store) read(ctx context.Context, fn func(tx *repos) error) error {
	return s.rs.Tx(ctx, func(tx ablage.Store) error { return fn(s.r.with(tx)) })
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
func (s *Store) nextUIDValidity(ctx context.Context, tx *repos) (uint32, error) {
	last, err := metaValue(ctx, tx, metaUIDValidity)
	if err != nil {
		return 0, err
	}
	v := max(last+1, s.now().Unix())
	if v > 0xffffffff {
		return 0, errors.New("UIDVALIDITY exhausted")
	}
	if err := setMeta(ctx, tx, metaUIDValidity, v); err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// Counters in meta.
const (
	metaUIDValidity = "uid_validity"
	metaDAVEpoch    = "dav_epoch"
	// metaEventSeq is the last event sequence number handed out.
	metaEventSeq = "event_seq"
)

// metaValue reads a counter (0 if it was never set).
func metaValue(ctx context.Context, tx *repos, key string) (int64, error) {
	m, err := tx.meta.Get(ctx, key)
	if errors.Is(err, ablage.ErrNotFound) {
		return 0, nil
	}
	return m.Value, err
}

// hasMeta tells whether a counter was set.
func hasMeta(ctx context.Context, tx *repos, key string) (int64, bool, error) {
	m, err := tx.meta.Get(ctx, key)
	if errors.Is(err, ablage.ErrNotFound) {
		return 0, false, nil
	}
	return m.Value, err == nil, err
}

// setMeta sets a counter to v.
func setMeta(ctx context.Context, tx *repos, key string, v int64) error {
	return tx.meta.Upsert(ctx, &metaRow{ID: key, Value: v})
}

// nextMeta adds one to a counter and returns the new value.
func nextMeta(ctx context.Context, tx *repos, key string) (int64, error) {
	n, err := tx.meta.UpdateWhere(ctx, ablage.Eq("id", key), ablage.Row{"value": ablage.Incr(1)})
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 1, tx.meta.Create(ctx, &metaRow{ID: key, Value: 1})
	}
	return metaValue(ctx, tx, key)
}
