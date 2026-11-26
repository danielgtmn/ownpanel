// Package store keeps the panel's data: users, access tokens, sites with
// their domains, redirects, DNS connections, mail domains with mailboxes and
// aliases, GitHub connections, settings and the activity log. It knows
// nothing about permissions; internal/panel decides who may do what.
//
// The data lives in typed ablage repositories (github.com/danielgtmn/ablage):
// on SQLite in ownpanel.db (Open), or in memory for tests (New with a
// memstore). There are no joins: related rows are loaded in one batch per
// relation and merged in Go. Ids are strings; rows from before ablage keep
// their former number as text ("42").
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/danielgtmn/ablage"
	"github.com/danielgtmn/ablage/sqlstore"

	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, no cgo)
)

// Store is the panel's database.
type Store struct {
	// db is the SQLite database behind st; nil for a store from New.
	db *sql.DB
	st ablage.Store
	r  repos
	// Now is the clock for created_at and similar fields.
	Now func() time.Time
	// NewUserID makes the id of a new user (ablage.NewID); tests set it
	// to get the short ids users from before ablage have.
	NewUserID func() string
}

// dsn are the SQLite settings of every connection. Opening the database is
// one of the few places outside ablage: the pragmas belong to the
// connection, not to a repository.
//
//   - foreign_keys(1): ablage relies on the database for references.
//   - busy_timeout, journal_mode(WAL), synchronous(NORMAL): as before ablage.
//   - _txlock=immediate: every transaction takes the write lock when it
//     begins, so a transaction that reads and then writes (count, then
//     insert) never fails upgrading its lock, and such sequences are
//     serialized.
const dsn = "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"

// Open opens (or creates) the database at file and brings its schema up to
// date. A database from before ablage is converted once (see legacy.go);
// a copy of it is kept next to it as <file>.pre-ablage-<time>.
func Open(file string) (*Store, error) {
	// Create the file ourselves so it never exists with wider permissions.
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(file, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", file+dsn)
	if err != nil {
		return nil, err
	}
	// One connection, as before ablage. Everything goes through one ablage
	// store, sesam's sign-in tables included (see Ablage); only the
	// one-time steps in legacy.go and authref.go run SQL of their own, and
	// they finish before the store is used. With one connection every
	// write and every transaction is serialized inside the process and
	// none of them can fail with "database is locked"; the price is that
	// reads wait for a running transaction, which is short everywhere
	// (sesam hashes passwords outside its transactions). The rule that
	// comes with it: inside a transaction only its own store is used (the
	// tx store, repo.With(tx)), never the outer one, or the call waits for
	// the one connection forever. The memory store used in tests holds one
	// lock per transaction and fails the same way, so tests catch a
	// violation.
	db.SetMaxOpenConns(1)
	ss, err := sqlstore.New(db, sqlstore.SQLite)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, st: ss, Now: time.Now, NewUserID: ablage.NewID}
	if s.r, err = newRepos(ss); err != nil {
		db.Close()
		return nil, err
	}
	ctx := context.Background()
	converted, err := convertLegacy(ctx, db, ss, file)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("convert %s: %w", file, err)
	}
	if converted != "" {
		slog.Info("converted the panel database; the former one is kept", "copy", converted)
	}
	if err := s.init(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", file, err)
	}
	linked, err := linkAuthUsers(ctx, db, ss, file)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("link the users to their sign-in in %s: %w", file, err)
	}
	if linked != "" {
		slog.Info("linked the panel users to their sign-in; the former database is kept", "copy", linked)
	}
	return s, nil
}

// New returns a store on st, e.g. memstore.New() in tests. It declares the
// tables and migrates st.
func New(st ablage.Store) (*Store, error) {
	s := &Store{st: st, Now: time.Now, NewUserID: ablage.NewID}
	var err error
	if s.r, err = newRepos(st); err != nil {
		return nil, err
	}
	if err := s.init(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// init creates missing tables and indexes and records the storage format.
func (s *Store) init(ctx context.Context) error {
	if err := s.st.Migrate(ctx); err != nil {
		return err
	}
	ok, err := s.r.format.Exists(ctx, formatAblage)
	if err != nil || ok {
		return err
	}
	return ignoreDup(s.r.format.Create(ctx, &formatRow{ID: formatAblage}))
}

// Close closes the database.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Ablage is the ablage store the panel's data lives in, for sesam: its
// sign-in tables (auth_*) are declared and migrated with the panel's own
// (signin.go), so sesam is set up on this store. The SQLite database has
// one connection (see Open): inside a transaction use only the
// transaction's store.
func (s *Store) Ablage() ablage.Store { return s.st }

// tx runs fn in one transaction with the repositories bound to it.
func (s *Store) tx(ctx context.Context, fn func(r repos) error) error {
	return s.st.Tx(ctx, func(tx ablage.Store) error { return fn(s.r.with(tx)) })
}

// now is the time for created_at and similar fields. They keep whole
// seconds, as before ablage.
func (s *Store) now() time.Time { return unix(s.Now().Unix()) }

// VacuumInto writes a consistent copy of the database at file to dest
// (which must not exist). It is safe while the daemon is running. Backups
// are outside ablage: VACUUM INTO is SQLite's own consistent copy.
func VacuumInto(file, dest string) error {
	if _, err := os.Stat(file); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", file+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`VACUUM INTO ?`, dest); err != nil {
		return err
	}
	return os.Chmod(dest, 0o600)
}

func unix(t int64) time.Time { return time.Unix(t, 0).UTC() }

// seconds drops the fraction of a time, as the store keeps whole seconds.
func seconds(t time.Time) time.Time { return unix(t.Unix()) }

func secondsPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := seconds(*t)
	return &v
}

// dup turns ablage's ErrDuplicate into the store's own error.
func dup(err error, mine error) error {
	if errors.Is(err, ablage.ErrDuplicate) {
		return mine
	}
	return err
}

// missing turns ablage's ErrNotFound into the store's own error.
func missing(err error, mine error) error {
	if errors.Is(err, ablage.ErrNotFound) {
		return mine
	}
	return err
}

func ignoreDup(err error) error {
	if errors.Is(err, ablage.ErrDuplicate) {
		return nil
	}
	return err
}

func ptr[T any](v T) *T { return &v }

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// optional is nil for "", else a pointer to v.
func optional(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// unique returns the distinct non-empty values of ids.
func unique(ids []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
