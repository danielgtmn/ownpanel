package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/danielgtmn/ablage/sqlstore"
)

// The one-time conversion of a database from before ablage. It is one of
// the few places with SQL of its own: it reads the old tables, which no
// repository describes.
//
// Before ablage, the schema came from the SQL files in legacy/, recorded in
// schema_migrations; integer ids, times as Unix seconds. The conversion:
//
//  1. applies the old files not applied yet (an install of an older
//     version), so it always starts from the last old schema;
//  2. copies the database to <file>.pre-ablage-<UTC time> (VACUUM INTO);
//  3. in one transaction: renames every old table to legacy_<name>, creates
//     the new tables (sqlstore DDL), copies the rows over with conversions,
//     compares the row counts, drops the old tables and schema_migrations
//     and records the format (store_format). Anything failing rolls it all
//     back; the copy stays.
//
// Conversions: ids become text ("42"), so links and sesam's link of a user
// (auth_user_id) keep working; times become microseconds; a 0 that meant
// "never" becomes the zero time; tables keyed by a name use it as the id
// (sites, site domains, settings, mail domains, pending deletes); tables
// with a composite key get the key as id ("<connection>/<zone>"). Rows that
// were listed in id order and may share a second (users, tokens, mailboxes,
// DKIM keys) keep that order through microseconds added to their time
// within the second; the store shows whole seconds. The activity log keeps
// its order in seq, the former id.
// login_attempts (unused since sign-in moved to sesam) is dropped.

//go:embed legacy/*.sql
var legacyFiles embed.FS

// legacyTables are the old tables, children before their parents (the
// order they are dropped in). Each new table has the same name.
var legacyTables = []string{
	"sessions", "tokens", "dns_zones", "dns_connections", "domains", "redirects", "site_domains", "sites",
	"mailbox_no_forwarding", "mailbox_grants", "mailbox_app_passwords", "mail_pending_deletes", "mail_aliases",
	"mailboxes", "mail_dkim_keys", "mail_domains", "organisations", "users", "activity", "settings",
}

// zeroTime is how ablage keeps the zero time.Time on SQLite.
var zeroTime = time.Time{}.UnixMicro()

// µs of a column of Unix seconds (NULL stays NULL).
func us(col string) string { return col + " * 1000000" }

// usNever converts seconds where 0 meant "never" to the zero time.
func usNever(col string) string {
	return fmt.Sprintf("CASE WHEN %s = 0 THEN %d ELSE %s * 1000000 END", col, zeroTime, col)
}

// usOrdered converts seconds and adds the rank by id within the second, so
// the order by time, then id stays the order by id it was.
func usOrdered(col string) string {
	return fmt.Sprintf("%s * 1000000 + (row_number() OVER (PARTITION BY %s ORDER BY id) - 1)", col, col)
}

func text(col string) string { return "CAST(" + col + " AS TEXT)" }

// legacyCopies are the INSERT … SELECT statements, one per new table:
// new columns and the expressions over legacy_<table> that fill them.
var legacyCopies = []struct {
	table string
	cols  []string
	exprs []string
}{
	{"users", []string{"id", "name", "password_hash", "role", "auth_user_id", "created_at"},
		[]string{text("id"), "name", "password_hash", "role", "auth_user_id", usOrdered("created_at")}},
	{"sessions", []string{"id", "user_id", "created_at", "expires_at"},
		[]string{"id", text("user_id"), us("created_at"), us("expires_at")}},
	{"tokens", []string{"id", "user_id", "name", "hash", "access", "sites", "created_at", "last_used_at", "expires_at"},
		[]string{text("id"), text("user_id"), "name", "hash", "access", "sites", usOrdered("created_at"), us("last_used_at"), us("expires_at")}},
	{"sites", []string{"id", "owner_id", "created_at"},
		[]string{"name", text("owner_id"), us("created_at")}},
	{"site_domains", []string{"id", "site_name"},
		[]string{"domain", "site_name"}},
	{"activity", []string{"id", "seq", "at", "actor", "action", "target", "detail"},
		[]string{text("id"), "id", us("at"), "actor", "action", "target", "detail"}},
	{"settings", []string{"id", "value"},
		[]string{"key", "value"}},
	{"dns_connections", []string{"id", "user_id", "provider", "name", "secret", "created_at", "last_checked_at", "last_error", "can_write", "zone_count"},
		[]string{text("id"), text("user_id"), "provider", "name", "secret", us("created_at"), us("last_checked_at"), "last_error", "can_write", "zone_count"}},
	{"dns_zones", []string{"id", "connection_id", "zone", "created_at"},
		[]string{text("connection_id") + " || '/' || zone", text("connection_id"), "zone", us("created_at")}},
	{"domains", []string{"id", "user_id", "domain", "created_at"},
		[]string{text("user_id") + " || '/' || domain", text("user_id"), "domain", us("created_at")}},
	{"redirects", []string{"id", "owner_id", "site_name", "source_domain", "source_path", "target", "code", "keep_path", "keep_query", "created_at"},
		[]string{text("id"), text("owner_id"), "site_name", "source_domain", "source_path", "target", "code", "keep_path", "keep_query", us("created_at")}},
	{"organisations", []string{"id", "owner_id", "name", "created_at"},
		[]string{"id", text("owner_id"), "name", us("created_at")}},
	{"mail_domains", []string{"id", "owner_id", "active", "activated_at", "checked_at", "check_error", "created_at", "organisation_id"},
		[]string{"domain", text("owner_id"), "active", us("activated_at"), us("checked_at"), "check_error", us("created_at"), "organisation_id"}},
	{"mail_dkim_keys", []string{"id", "domain", "selector", "private_key", "public_key", "state", "created_at", "active_domain"},
		[]string{text("id"), "domain", "selector", "private_key", "public_key", "state", usOrdered("created_at"), "CASE WHEN state = 'active' THEN domain END"}},
	{"mailboxes", []string{"id", "storage_id", "domain", "local_part", "password_hash", "quota_bytes", "send_per_hour", "send_per_day",
		"created_at", "display_name", "app_passwords_only", "revision", "admin_password_at", "admin_display_name_at",
		"admin_app_passwords_only_at", "kind", "keep_sent_copy", "admin_grants_at"},
		[]string{text("id"), "storage_id", "domain", "local_part", "password_hash", "quota_bytes", "send_per_hour", "send_per_day",
			usOrdered("created_at"), "display_name", "app_passwords_only", "revision", usNever("admin_password_at"), usNever("admin_display_name_at"),
			usNever("admin_app_passwords_only_at"), "kind", "keep_sent_copy", usNever("admin_grants_at")}},
	{"mail_aliases", []string{"id", "domain", "local_part", "targets", "created_at", "sender_policy", "hide_from_directory"},
		[]string{text("id"), "domain", "local_part", "targets", us("created_at"), "sender_policy", "hide_from_directory"}},
	{"mail_pending_deletes", []string{"id", "created_at"},
		[]string{"storage_id", us("created_at")}},
	{"mailbox_app_passwords", []string{"id", "mailbox_id", "name", "mac", "created_at", "last_used_at", "last_used_net"},
		[]string{"id", text("mailbox_id"), "name", "mac", us("created_at"), us("last_used_at"), "last_used_net"}},
	{"mailbox_grants", []string{"id", "mailbox_id", "grantee_id", "rights", "created_at"},
		[]string{text("mailbox_id") + " || '/' || " + text("grantee_id"), text("mailbox_id"), text("grantee_id"), "rights", us("created_at")}},
	{"mailbox_no_forwarding", []string{"id", "mailbox_id"},
		[]string{text("mailbox_id"), text("mailbox_id")}},
}

// legacyHook lets tests fail the conversion at a step ("copied": after
// the rows were copied, before the old tables are dropped).
var legacyHook = func(step string) error { return nil }

// convertLegacy converts a database from before ablage, once. It returns
// the file name of the copy it made, or "" when there was nothing to do.
// The repositories must be declared on ss already.
func convertLegacy(ctx context.Context, db *sql.DB, ss *sqlstore.Store, file string) (string, error) {
	legacy, err := tableExists(ctx, db, "schema_migrations")
	if err != nil || !legacy {
		return "", err
	}
	sub, _ := fs.Sub(legacyFiles, "legacy")
	if err := migrate(db, sub); err != nil {
		return "", fmt.Errorf("bring the former tables up to date: %w", err)
	}
	// A failed attempt leaves its copy; another one in the same second
	// gets a name of its own.
	backup := file + ".pre-ablage-" + time.Now().UTC().Format("20060102T150405Z")
	for i := 2; fileExists(backup); i++ {
		backup = fmt.Sprintf("%s.pre-ablage-%s-%d", file, time.Now().UTC().Format("20060102T150405Z"), i)
	}
	if err := VacuumInto(file, backup); err != nil {
		return "", fmt.Errorf("copy the database before converting it: %w", err)
	}
	ddl, err := ss.DDL()
	if err != nil {
		return "", err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := convertIn(ctx, tx, ddl); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return backup, nil
}

func convertIn(ctx context.Context, tx *sql.Tx, ddl []string) error {
	// References are checked at commit: the new tables are filled in any
	// order, and the old ones reference each other until they are dropped.
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return err
	}
	for _, t := range append(slices.Clone(legacyTables), "login_attempts") {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE "`+t+`" RENAME TO "legacy_`+t+`"`); err != nil {
			return fmt.Errorf("rename %s: %w", t, err)
		}
	}
	for _, stmt := range ddl {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create the new tables: %w", err)
		}
	}
	for _, c := range legacyCopies {
		q := fmt.Sprintf(`INSERT INTO "%s" (%s) SELECT %s FROM "legacy_%s"`, c.table,
			`"`+strings.Join(c.cols, `", "`)+`"`, strings.Join(c.exprs, ", "), c.table)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("copy %s: %w", c.table, err)
		}
		var before, after int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM "legacy_`+c.table+`"`).Scan(&before); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM "`+c.table+`"`).Scan(&after); err != nil {
			return err
		}
		if before != after {
			return fmt.Errorf("copy %s: %d rows before, %d after", c.table, before, after)
		}
	}
	if err := legacyHook("copied"); err != nil {
		return err
	}
	for _, t := range append([]string{"login_attempts"}, legacyTables...) {
		if _, err := tx.ExecContext(ctx, `DROP TABLE "legacy_`+t+`"`); err != nil {
			return fmt.Errorf("drop the former %s: %w", t, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE schema_migrations`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO store_format (id) VALUES (?)`, formatAblage)
	return err
}

func fileExists(name string) bool {
	_, err := os.Lstat(name)
	return err == nil
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0, err
}

// migrate applies every *.sql file in fsys that is not yet recorded in
// schema_migrations, in file name order, each in its own transaction. Only
// for databases from before ablage (convertLegacy).
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
