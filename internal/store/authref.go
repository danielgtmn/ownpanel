package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/danielgtmn/ablage/sqlstore"
)

// users.auth_user_id references auth_users (sesam's users). A database
// converted to ablage before that (by an earlier build of the conversion in
// legacy.go) has the column without the reference, and ablage's Migrate
// never changes a column. SQLite cannot add a reference to a column either,
// so linkAuthUsers rebuilds the users table once, the way SQLite describes
// it (https://www.sqlite.org/lang_altertable.html#otheralter). Like the
// conversion it is one of the few places with SQL of its own.
//
//  1. copies the database to <file>.pre-auth-ref-<UTC time> (VACUUM INTO);
//  2. turns references off on the connection, so dropping the old users
//     table does not delete (cascade) the rows that reference users;
//  3. in one transaction: creates the new table as users_new (sqlstore
//     DDL), copies the rows, compares the row counts, drops users, renames
//     users_new to users (the other tables keep referencing "users"),
//     creates its indexes and checks every reference; anything failing
//     rolls it all back, the copy stays;
//  4. turns references on again.
//
// A link to a sign-in user that does not exist is cleared: the panel moves
// that user to the sign-in again at start, and an admin sets a new
// password. A database that has the reference already is left alone.

// authRefHook lets tests fail the rebuild after the rows were copied.
var authRefHook = func() error { return nil }

// linkAuthUsers adds the reference from users.auth_user_id to auth_users
// where it is missing. It returns the file name of the copy it made, or ""
// when there was nothing to do. The store must be migrated already (so
// auth_users exists).
func linkAuthUsers(ctx context.Context, db *sql.DB, ss *sqlstore.Store, file string) (string, error) {
	ok, err := hasAuthRef(ctx, db)
	if err != nil || ok {
		return "", err
	}
	ddl, err := ss.DDL()
	if err != nil {
		return "", err
	}
	var create string
	var indexes []string
	for _, stmt := range ddl {
		if ddlTable(stmt) != "users" {
			continue
		}
		if strings.HasPrefix(stmt, `CREATE TABLE "users" (`) {
			create = strings.Replace(stmt, `CREATE TABLE "users" (`, `CREATE TABLE "users_new" (`, 1)
		} else {
			indexes = append(indexes, stmt)
		}
	}
	if create == "" {
		return "", errors.New("the users table is not declared")
	}
	backup, err := keepCopy(file, "pre-auth-ref")
	if err != nil {
		return "", fmt.Errorf("copy the database first: %w", err)
	}
	// The pragma has no effect inside a transaction, and it belongs to the
	// connection: hold the (one) connection until it is on again.
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return "", err
	}
	err = rebuildUsers(ctx, conn, create, indexes)
	if _, onErr := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); onErr != nil {
		// Never hand out the connection without references: the pool
		// discards a connection that reports ErrBadConn.
		conn.Raw(func(any) error { return driver.ErrBadConn })
		return "", errors.Join(err, fmt.Errorf("turn references on again: %w", onErr))
	}
	if err != nil {
		return "", err
	}
	return backup, nil
}

// hasAuthRef tells whether users.auth_user_id references auth_users (or
// there is no users table to change).
func hasAuthRef(ctx context.Context, db *sql.DB) (bool, error) {
	have, err := tableNames(ctx, db)
	if err != nil || !have["users"] {
		return true, err
	}
	var n int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_list('users')
		WHERE "from" = 'auth_user_id' AND "table" = 'auth_users'`).Scan(&n)
	return n > 0, err
}

func rebuildUsers(ctx context.Context, conn *sql.Conn, create string, indexes []string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, create); err != nil {
		return fmt.Errorf("create the new users table: %w", err)
	}
	cols, err := columnNames(ctx, tx, "users")
	if err != nil {
		return err
	}
	newCols, err := columnNames(ctx, tx, "users_new")
	if err != nil {
		return err
	}
	if !slices.Equal(sorted(cols), sorted(newCols)) {
		return fmt.Errorf("the users table has the columns %v, expected %v", cols, newCols)
	}
	exprs := make([]string, len(cols))
	for i, c := range cols {
		exprs[i] = `"` + c + `"`
		if c == "auth_user_id" {
			exprs[i] = existingAuthUser
		}
	}
	q := fmt.Sprintf(`INSERT INTO users_new ("%s") SELECT %s FROM users`, strings.Join(cols, `", "`), strings.Join(exprs, ", "))
	if _, err := tx.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("copy the users: %w", err)
	}
	var before, after int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&before); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users_new`).Scan(&after); err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("copy the users: %d rows before, %d after", before, after)
	}
	if err := authRefHook(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE users`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE users_new RENAME TO users`); err != nil {
		return err
	}
	for _, stmt := range indexes {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("index the users: %w", err)
		}
	}
	// References were off: check them all before committing.
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	bad := rows.Next()
	rows.Close()
	if bad {
		return errors.New("references are broken after rebuilding the users table")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

func columnNames(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}
