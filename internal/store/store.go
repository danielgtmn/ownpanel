// Package store keeps the panel's data in SQLite: users, sessions, access
// tokens, sites with their domains, and the activity log. It knows nothing
// about permissions; internal/panel decides who may do what.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, no cgo)
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Store is the panel's database.
type Store struct {
	db *sql.DB
	// Now is the clock for created_at and similar columns.
	Now func() time.Time
}

// Open opens (or creates) the database at file and brings its schema up to
// date.
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
	db, err := sql.Open("sqlite", file+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	// One connection: every write is serialized and transactions never wait
	// on each other inside the process.
	db.SetMaxOpenConns(1)
	sub, _ := fs.Sub(migrationFiles, "migrations")
	if err := migrate(db, sub); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", file, err)
	}
	return &Store{db: db, Now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrate applies every *.sql file in fsys that is not yet recorded in
// schema_migrations, in file name order, each in its own transaction.
func migrate(db *sql.DB, fsys fs.FS) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, name := range names {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", path.Base(name), err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, name, time.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) now() int64 { return s.Now().Unix() }

// VacuumInto writes a consistent copy of the database at file to dest
// (which must not exist). It is safe while the daemon is running.
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

// isUnique reports a violated UNIQUE or PRIMARY KEY constraint. The driver
// only exposes it in the message.
func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func noRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
