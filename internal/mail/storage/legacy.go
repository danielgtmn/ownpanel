package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// The index before ablage: its migrations ran by PRAGMA user_version (1 to
// 10). They stay here, unchanged, so that an index of any older version is
// brought to the last one before it is converted (migrateLegacy). Never
// edit them.

var legacyMigrations = []string{
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
	// 3: greylisting triplets.
	`
CREATE TABLE greylist (
	key    TEXT PRIMARY KEY,
	first  INTEGER NOT NULL,
	passed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX greylist_first ON greylist(first);
`,
	// 4: the network a greylisting triplet counts against.
	`
ALTER TABLE greylist ADD COLUMN net TEXT NOT NULL DEFAULT '';
CREATE INDEX greylist_net ON greylist(net, passed);
`,
	// 5: import jobs, the keys of imported messages, failures and IMAP
	// watermarks; all go with the mailbox.
	`
CREATE TABLE imports (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	storage_id TEXT NOT NULL REFERENCES mailboxes(storage_id) ON DELETE CASCADE,
	kind       TEXT NOT NULL,
	state      TEXT NOT NULL,
	error      TEXT NOT NULL DEFAULT '',
	host       TEXT NOT NULL DEFAULT '',
	port       INTEGER NOT NULL DEFAULT 0,
	username   TEXT NOT NULL DEFAULT '',
	file_name  TEXT NOT NULL DEFAULT '',
	file_size  INTEGER NOT NULL DEFAULT 0,
	received   INTEGER NOT NULL DEFAULT 0,
	data       TEXT NOT NULL DEFAULT '{}',
	created    INTEGER NOT NULL,
	started    INTEGER NOT NULL DEFAULT 0,
	finished   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX imports_mailbox ON imports(storage_id, id);
CREATE INDEX imports_state ON imports(state);
CREATE TABLE import_failures (
	id         INTEGER PRIMARY KEY,
	import_id  INTEGER NOT NULL REFERENCES imports(id) ON DELETE CASCADE,
	folder     TEXT NOT NULL,
	date       INTEGER NOT NULL DEFAULT 0,
	subject    TEXT NOT NULL DEFAULT '',
	reason     TEXT NOT NULL
);
CREATE INDEX import_failures_import ON import_failures(import_id, id);
CREATE TABLE import_seen (
	storage_id TEXT NOT NULL REFERENCES mailboxes(storage_id) ON DELETE CASCADE,
	folder     TEXT NOT NULL,
	key        BLOB NOT NULL,
	PRIMARY KEY (storage_id, folder, key)
) WITHOUT ROWID;
CREATE INDEX import_seen_key ON import_seen(storage_id, key);
CREATE TABLE import_watermarks (
	storage_id   TEXT NOT NULL REFERENCES mailboxes(storage_id) ON DELETE CASCADE,
	host         TEXT NOT NULL,
	username     TEXT NOT NULL,
	folder       TEXT NOT NULL,
	uid_validity INTEGER NOT NULL,
	last_uid     INTEGER NOT NULL,
	PRIMARY KEY (storage_id, host, username, folder)
);
`,
	// Mailbox accounts (G1): account events carry their change as JSON
	// (config.AccountChange); account_overlays keeps the changes a mailbox
	// made itself until the panel's configuration includes them
	// (directory.Overlay as JSON). Not bound to the mailboxes table: a
	// mailbox may change its account before it stored any mail.
	`
ALTER TABLE events ADD COLUMN account TEXT NOT NULL DEFAULT '';
CREATE TABLE account_overlays (
	storage_id TEXT PRIMARY KEY,
	data       TEXT NOT NULL
);
`,
	// 7: sessions of the self-service area (G1). Only a hash of the
	// session id is stored; credential is a fingerprint of the password
	// hash at sign-in, so a new password ends the session.
	`
CREATE TABLE account_sessions (
	id_hash    BLOB PRIMARY KEY,
	storage_id TEXT NOT NULL,
	address    TEXT NOT NULL,
	credential TEXT NOT NULL,
	created    INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL
);
CREATE INDEX account_sessions_mailbox ON account_sessions(storage_id, created);
`,
	// 8: shared mailboxes (G3). shared_seen is the read state of each
	// reader (a storage id) in a shared mailbox, by message row: it moves
	// with the message and goes with it. msgid is the Message-ID of copies
	// of sent mail kept in a shared mailbox, so one saved by the mail
	// program and one kept by the server are stored once.
	`
CREATE TABLE shared_seen (
	message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
	reader     TEXT    NOT NULL,
	PRIMARY KEY (message_id, reader)
) WITHOUT ROWID;
CREATE INDEX shared_seen_reader ON shared_seen(reader);
ALTER TABLE messages ADD COLUMN msgid TEXT NOT NULL DEFAULT '';
CREATE INDEX messages_msgid ON messages(folder_id, msgid) WHERE msgid != '';
`,
	// 9: calendars and address books (G4a, dav.go). IF NOT EXISTS keeps the
	// migration harmless if it ends up numbered after another phase's.
	legacyDAVSchema,
	// 10: filters and out-of-office (G2): the mailbox's Sieve scripts (at
	// most one active), who got an out-of-office reply until when, and the
	// ids the "duplicate" test has seen.
	`
CREATE TABLE sieve_scripts (
	storage_id TEXT NOT NULL,
	name       TEXT NOT NULL,
	content    TEXT NOT NULL,
	active     INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
	updated    INTEGER NOT NULL,
	PRIMARY KEY (storage_id, name)
);
CREATE UNIQUE INDEX sieve_scripts_active ON sieve_scripts(storage_id) WHERE active = 1;
CREATE TABLE sieve_replies (
	storage_id TEXT NOT NULL,
	handle     TEXT NOT NULL,
	sender     TEXT NOT NULL,
	until      INTEGER NOT NULL,
	PRIMARY KEY (storage_id, handle, sender)
);
CREATE INDEX sieve_replies_until ON sieve_replies(until);
CREATE TABLE sieve_duplicates (
	storage_id TEXT NOT NULL,
	id         TEXT NOT NULL,
	until      INTEGER NOT NULL,
	PRIMARY KEY (storage_id, id)
);
CREATE INDEX sieve_duplicates_until ON sieve_duplicates(until);
`,
}

const legacyDAVSchema = `
CREATE TABLE IF NOT EXISTS dav_collections (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	storage_id   TEXT NOT NULL,
	kind         TEXT NOT NULL,
	name         TEXT NOT NULL,
	display_name TEXT NOT NULL DEFAULT '',
	color        TEXT NOT NULL DEFAULT '',
	description  TEXT NOT NULL DEFAULT '',
	components   TEXT NOT NULL DEFAULT '',
	seq          INTEGER NOT NULL DEFAULT 0,
	pruned_seq   INTEGER NOT NULL DEFAULT 0,
	created      INTEGER NOT NULL,
	UNIQUE (storage_id, kind, name)
);
CREATE TABLE IF NOT EXISTS dav_objects (
	id            INTEGER PRIMARY KEY,
	collection_id INTEGER NOT NULL REFERENCES dav_collections(id) ON DELETE CASCADE,
	storage_id    TEXT NOT NULL,
	name          TEXT NOT NULL,
	uid           TEXT NOT NULL,
	component     TEXT NOT NULL,
	size          INTEGER NOT NULL,
	start_at      INTEGER NOT NULL,
	end_at        INTEGER NOT NULL,
	file          TEXT NOT NULL,
	seq           INTEGER NOT NULL,
	modified      INTEGER NOT NULL,
	UNIQUE (collection_id, name),
	UNIQUE (collection_id, uid)
);
CREATE INDEX IF NOT EXISTS dav_objects_seq ON dav_objects(collection_id, seq);
CREATE INDEX IF NOT EXISTS dav_objects_mailbox ON dav_objects(storage_id);
CREATE TABLE IF NOT EXISTS dav_deletions (
	collection_id INTEGER NOT NULL REFERENCES dav_collections(id) ON DELETE CASCADE,
	name          TEXT NOT NULL,
	seq           INTEGER NOT NULL,
	at            INTEGER NOT NULL,
	PRIMARY KEY (collection_id, name)
);
CREATE INDEX IF NOT EXISTS dav_deletions_at ON dav_deletions(at);
`

// ablageVersion is PRAGMA user_version of an index kept with ablage; the
// versions before it (1 to len(legacyMigrations)) are the old schema.
const ablageVersion = 100

// migrate brings the index to the current schema: a new or ablage index
// through ablage's Migrate, an old one through migrateLegacy first.
func (s *Store) migrate(ctx context.Context, file string) error {
	var v int
	if err := s.wdb.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > 0 && v < ablageVersion {
		if err := applyLegacyMigrations(s.wdb, v); err != nil {
			return err
		}
		if err := s.migrateLegacy(ctx, file); err != nil {
			return err
		}
	}
	if err := s.ws.Migrate(ctx); err != nil {
		return err
	}
	if v < ablageVersion {
		_, err := s.wdb.ExecContext(ctx, `PRAGMA user_version = `+strconv.Itoa(ablageVersion))
		return err
	}
	return nil
}

// applyLegacyMigrations runs the old migrations after version v, each in
// its own transaction, as the code before ablage did.
func applyLegacyMigrations(db *sql.DB, v int) error {
	for i := v; i < len(legacyMigrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(legacyMigrations[i]); err != nil {
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

// legacyCopy copies one old table into its new one.
type legacyCopy struct {
	table string
	// cols are the new table's columns, sel what fills them (same order),
	// selected from legacy_<table>.
	cols, sel string
}

// zeroTime is how ablage stores the zero time.Time on SQLite.
var zeroTime = strconv.FormatInt(time.Time{}.UnixMicro(), 10)

// micros converts Unix seconds to ablage's time (microseconds).
func micros(col string) string { return col + " * 1000000" }

// microsOrZero converts Unix seconds where 0 means "never" to ablage's
// time, the zero time for 0.
func microsOrZero(col string) string {
	return "CASE WHEN " + col + " = 0 THEN " + zeroTime + " ELSE " + col + " * 1000000 END"
}

// newID is a fresh id for rows of tables that had no id column of their
// own: 26 random hex characters.
const newID = `lower(hex(randomblob(13)))`

// hasFlag tells whether the old space-separated flags hold f.
func hasFlag(f string) string { return `instr(' ' || flags || ' ', ' ` + f + ` ') > 0` }

// flagWords is the old flags as a JSON array of strings: keywords cannot
// hold '"' or '\', only system flags start with '\' (doubled here).
const flagWords = `('["' || replace(replace(flags, '\', '\\'), ' ', '","') || '"]')`

// keywordsJSON is the keywords among the old flags as a JSON array, or
// null when there are none (what ablage stores for a nil slice).
var keywordsJSON = `CASE WHEN (SELECT count(*) FROM json_each(` + flagWords + `) WHERE value <> '' AND substr(value, 1, 1) <> '\') = 0
	THEN 'null' ELSE (SELECT json_group_array(value) FROM json_each(` + flagWords + `) WHERE value <> '' AND substr(value, 1, 1) <> '\') END`

// legacyCopies are all tables, parents before children.
var legacyCopies = []legacyCopy{
	{"meta", "id, value", "key, value"},
	{"mailboxes", "id, created, used_bytes, messages", "storage_id, " + micros("created") + ", used_bytes, messages"},
	{"folders", "id, storage_id, name, special_use, uid_validity, uid_next, subscribed",
		"CAST(id AS TEXT), storage_id, name, special_use, uid_validity, uid_next, subscribed != 0"},
	{"messages", "id, folder_id, storage_id, uid, file, size, internal_date, seen, answered, flagged, deleted, draft, keywords, msgid",
		"CAST(id AS TEXT), CAST(folder_id AS TEXT), storage_id, uid, file, size, " + micros("internal_date") + ", " +
			hasFlag(FlagSeen) + ", " + hasFlag(FlagAnswered) + ", " + hasFlag(FlagFlagged) + ", " + hasFlag(FlagDeleted) + ", " + hasFlag(FlagDraft) + ", " +
			keywordsJSON + ", msgid"},
	{"shared_seen", "id, message_id, reader", newID + ", CAST(message_id AS TEXT), reader"},
	{"bindings", "id, address, bound", "storage_id, address, " + micros("bound")},
	{"blocks", "id, blocked, reason, at", "storage_id, blocked != 0, reason, " + micros("at")},
	{"events", "id, seq, at, kind, storage_id, address, detail, account",
		"CAST(id AS TEXT), id, " + micros("at") + ", kind, storage_id, address, detail, account"},
	{"queue", "id, file, sender, storage_id, created, size", "CAST(id AS TEXT), file, sender, storage_id, " + micros("created") + ", size"},
	{"queue_rcpts", "id, message_id, pos, address, domain, state, attempts, next_try, last_error",
		"CAST(id AS TEXT), CAST(message_id AS TEXT), id, address, domain, state, attempts, " + micros("next_try") + ", last_error"},
	{"send_log", "id, key, value, at", newID + ", key, value, " + micros("at")},
	{"greylist", "id, net, first, passed", "key, net, " + micros("first") + ", CASE WHEN passed = 0 THEN NULL ELSE passed * 1000000 END"},
	{"imports", "id, storage_id, kind, state, error, host, port, username, file_name, file_size, received, data, created, started, finished",
		"CAST(id AS TEXT), storage_id, kind, state, error, host, port, username, file_name, file_size, received, data, " +
			micros("created") + ", " + microsOrZero("started") + ", " + microsOrZero("finished")},
	{"import_failures", "id, import_id, n, folder, date, subject, reason",
		"CAST(id AS TEXT), CAST(import_id AS TEXT), id, folder, " + microsOrZero("date") + ", subject, reason"},
	{"import_seen", "id, storage_id, folder, key", newID + ", storage_id, folder, key"},
	{"import_watermarks", "id, storage_id, host, username, folder, uid_validity, last_uid",
		newID + ", storage_id, host, username, folder, uid_validity, last_uid"},
	{"account_overlays", "id, data", "storage_id, data"},
	{"account_sessions", "id, storage_id, address, credential, created, last_seen",
		"lower(hex(id_hash)), storage_id, address, credential, " + micros("created") + ", " + micros("last_seen")},
	{"dav_collections", "id, storage_id, kind, name, display_name, color, description, components, seq, pruned_seq, created",
		"CAST(id AS TEXT), storage_id, kind, name, display_name, color, description, components, seq, pruned_seq, " + micros("created")},
	{"dav_objects", "id, collection_id, storage_id, name, uid, component, size, start_at, end_at, file, seq, modified",
		"CAST(id AS TEXT), CAST(collection_id AS TEXT), storage_id, name, uid, component, size, start_at, end_at, file, seq, " + micros("modified")},
	{"dav_deletions", "id, collection_id, name, seq, at", newID + ", CAST(collection_id AS TEXT), name, seq, " + micros("at")},
	{"sieve_scripts", "id, storage_id, name, content, size, active_for, updated",
		newID + ", storage_id, name, content, length(CAST(content AS BLOB)), CASE WHEN active = 1 THEN storage_id END, " + micros("updated")},
	{"sieve_replies", "id, storage_id, handle, sender, until", newID + ", storage_id, handle, sender, " + micros("until")},
	{"sieve_duplicates", "id, storage_id, key, until", newID + ", storage_id, id, " + micros("until")},
}

// migrateLegacy converts an index of the last old schema to ablage's
// tables, once: it backs the file up next to it (mail.db.pre-ablage-<UTC
// time>), then in one transaction renames the old tables, creates the new
// ones, copies every row with SQL, compares the row counts, drops the old
// tables and sets user_version to ablageVersion (the marker). If anything
// fails, nothing changed and the backup stays. This and the old migrations
// are the only SQL outside ablage besides opening the database: they read
// the old schema.
//
// What changes: ids become text with the same digits (folders, messages,
// events, queue, imports, calendars and address books keep theirs, so
// ETags, sync tokens, file paths and the panel's import ids stay valid);
// the flags string becomes bool fields plus keywords; Unix seconds become
// ablage times; the event cursor is the old id (seq) and goes on from the
// highest id ever handed out. UIDs, UIDVALIDITY and the DAV epoch are
// copied as they are, so mail and DAV clients see no change.
func (s *Store) migrateLegacy(ctx context.Context, file string) error {
	// The backup is created empty with mode 0600 first: VACUUM INTO
	// writes into an empty file, keeping its mode. An earlier attempt in
	// the same second (it failed) left one already: count on.
	base := file + ".pre-ablage-" + s.now().UTC().Format("20060102T150405Z")
	backup := base
	for n := 2; ; n++ {
		f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			f.Close()
			break
		}
		if !errors.Is(err, os.ErrExist) || n > 100 {
			return fmt.Errorf("backup: %w", err)
		}
		backup = base + "-" + strconv.Itoa(n)
	}
	if _, err := s.wdb.ExecContext(ctx, `VACUUM INTO ?`, backup); err != nil {
		os.Remove(backup)
		return fmt.Errorf("backup: %w", err)
	}
	ddl, err := s.ws.DDL()
	if err != nil {
		return err
	}
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) error {
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("%w: %s", err, firstLine(q))
		}
		return nil
	}
	if err := exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
		return err
	}
	var lastEvent int64
	if err := tx.QueryRowContext(ctx, `SELECT max(coalesce((SELECT max(seq) FROM sqlite_sequence WHERE name = 'events'), 0),
		coalesce((SELECT max(id) FROM events), 0))`).Scan(&lastEvent); err != nil {
		return err
	}
	for _, c := range legacyCopies {
		if err := exec(`ALTER TABLE ` + c.table + ` RENAME TO legacy_` + c.table); err != nil {
			return err
		}
	}
	for _, q := range ddl {
		if err := exec(q); err != nil {
			return err
		}
	}
	for _, c := range legacyCopies {
		if err := exec(`INSERT INTO ` + c.table + ` (` + c.cols + `) SELECT ` + c.sel + ` FROM legacy_` + c.table); err != nil {
			return err
		}
		var before, after int64
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM legacy_`+c.table+`), (SELECT count(*) FROM `+c.table+`)`).Scan(&before, &after); err != nil {
			return err
		}
		if before != after {
			return fmt.Errorf("%s: %d rows copied of %d", c.table, after, before)
		}
		if legacyCopied != nil {
			if err := legacyCopied(c.table); err != nil {
				return err
			}
		}
	}
	for i := len(legacyCopies) - 1; i >= 0; i-- {
		if err := exec(`DROP TABLE legacy_` + legacyCopies[i].table); err != nil {
			return err
		}
	}
	if err := exec(`INSERT INTO meta (id, value) VALUES (?, ?)`, metaEventSeq, lastEvent); err != nil {
		return err
	}
	if err := exec(`PRAGMA user_version = ` + strconv.Itoa(ablageVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// legacyCopied is called after each table is copied (tests: a failure
// half-way).
var legacyCopied func(table string) error

func firstLine(q string) string {
	q, _, _ = strings.Cut(strings.TrimSpace(q), "\n")
	return q
}
