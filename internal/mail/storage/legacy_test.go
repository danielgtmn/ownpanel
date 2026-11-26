package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
)

// The legacy fixture is built with legacyMigrations, the old schema's SQL
// exactly as it was released, so it is the final old schema.

// legacyT is the time of the rows of the legacy fixture (Unix seconds).
const legacyT = 1700000000

// Files of the legacy fixture.
const (
	legacyMsg1  = "1700000000.0000000000000001"
	legacyMsg2  = "1700000000.0000000000000002"
	legacyMsg3  = "1700000000.0000000000000003"
	legacyMsg4  = "1700000000.0000000000000004"
	legacyQueue = "1700000000.00000000000000aa"
	legacyDAV   = "1700000000.00000000000000bb"
)

// legacyRows fills every table of the last schema before ablage, with the
// edge cases the conversion has to keep: flags with keywords, 0 meaning
// "never", JSON, blobs, unicode, an event id handed out and deleted again.
var legacyRows = fmt.Sprintf(`
INSERT INTO meta (key, value) VALUES ('uid_validity', 1700000100), ('dav_epoch', 1700000200);
INSERT INTO mailboxes (storage_id, created, used_bytes, messages) VALUES ('%[1]s', %[3]d, 999, 3), ('%[2]s', %[3]d, 4, 1);
INSERT INTO folders (id, storage_id, name, special_use, uid_validity, uid_next, subscribed) VALUES
	(5, '%[1]s', 'INBOX', '', 1700000050, 4, 1),
	(9, '%[1]s', 'Sent', '\Sent', 1700000051, 1, 0),
	(12, '%[1]s', 'Ünïcødé/Ordner', '', 1700000052, 3, 0),
	(20, '%[2]s', 'INBOX', '', 1700000060, 2, 1);
INSERT INTO messages (id, folder_id, storage_id, uid, file, size, internal_date, flags, msgid) VALUES
	(100, 5, '%[1]s', 1, '%[4]s', 5, %[3]d, '', ''),
	(101, 5, '%[1]s', 3, '%[5]s', 7, %[3]d + 1, '$Forwarded \Answered \Seen custom', '<m@x>'),
	(102, 12, '%[1]s', 2, '%[6]s', 3, %[3]d + 2, '\Deleted \Draft \Flagged', ''),
	(103, 20, '%[2]s', 1, '%[7]s', 4, %[3]d + 3, '\Seen', '');
INSERT INTO shared_seen (message_id, reader) VALUES (100, '%[2]s');
INSERT INTO bindings (storage_id, address, bound) VALUES ('%[1]s', 'a@example.com', %[3]d);
INSERT INTO blocks (storage_id, blocked, reason, at) VALUES ('%[1]s', 1, 'spam', %[3]d + 5);
INSERT INTO events (at, kind, storage_id, address, detail, account) VALUES
	(%[3]d, 'blocked', '%[1]s', 'a@example.com', 'x', ''),
	(%[3]d + 1, 'mailbox.app-password-used', '%[1]s', 'a@example.com', 'y', '{"app_password_id":"p1","network":"10.0.0.0/24"}'),
	(%[3]d + 2, 'blocked', '%[1]s', '', 'z', '');
DELETE FROM events WHERE id = 3;
INSERT INTO queue (id, file, sender, storage_id, created, size) VALUES (7, '%[8]s', 'bounce@x', '%[1]s', %[3]d, 10);
INSERT INTO queue_rcpts (id, message_id, address, domain, state, attempts, next_try, last_error) VALUES
	(30, 7, 'b@y', 'y', 0, 0, %[3]d, ''),
	(31, 7, 'a@z', 'z', 2, 3, %[3]d + 60, 'fail');
INSERT INTO send_log (key, value, at) VALUES ('send:%[1]s', '', %[3]d), ('net:%[1]s', '10.0.0.0/24', %[3]d);
INSERT INTO greylist (key, first, passed, net) VALUES ('k1', %[3]d, 0, 'net1'), ('k2', %[3]d, %[3]d + 400, 'net1');
INSERT INTO imports (id, storage_id, kind, state, error, host, port, username, file_name, file_size, received, data, created, started, finished) VALUES
	(7, '%[1]s', 'imap', 'done', '', 'imap.x', 993, 'u', '', 0, 0, '{"choices":{},"copied":3}', %[3]d, %[3]d + 1, %[3]d + 2),
	(8, '%[1]s', 'file', 'uploading', '', '', 0, '', 'x.mbox', 100, 50, '{}', %[3]d + 3, 0, 0);
INSERT INTO import_failures (id, import_id, folder, date, subject, reason) VALUES
	(40, 7, 'INBOX', %[3]d, 'Betreff ü', 'too big'),
	(41, 7, 'Sent', 0, '', 'bad');
INSERT INTO import_seen (storage_id, folder, key) VALUES ('%[1]s', 'INBOX', X'00ff10');
INSERT INTO import_watermarks (storage_id, host, username, folder, uid_validity, last_uid) VALUES ('%[1]s', 'imap.x', 'u', 'INBOX', 42, 17);
INSERT INTO account_overlays (storage_id, data) VALUES ('%[1]s', '{"name": "Ä"}');
INSERT INTO account_sessions (id_hash, storage_id, address, credential, created, last_seen) VALUES (X'0102', '%[1]s', 'a@example.com', 'cred', %[3]d, %[3]d + 9);
INSERT INTO dav_collections (id, storage_id, kind, name, display_name, color, description, components, seq, pruned_seq, created) VALUES
	(3, '%[1]s', 'calendar', 'calendar', 'Kalender', '#ff0000', '', 'VEVENT', 5, 1, %[3]d);
INSERT INTO dav_objects (id, collection_id, storage_id, name, uid, component, size, start_at, end_at, file, seq, modified) VALUES
	(60, 3, '%[1]s', 'e1.ics', 'e1', 'VEVENT', %[10]d, 1735722000, 1735722000, '%[9]s', 5, %[3]d);
INSERT INTO dav_deletions (collection_id, name, seq, at) VALUES (3, 'gone.ics', 4, %[3]d);
INSERT INTO sieve_scripts (storage_id, name, content, active, updated) VALUES ('%[1]s', 'main', 'keep;', 1, %[3]d), ('%[1]s', 'other', 'stop;ü', 0, %[3]d);
INSERT INTO sieve_replies (storage_id, handle, sender, until) VALUES ('%[1]s', 'h', 's@x', %[3]d + 1000);
INSERT INTO sieve_duplicates (storage_id, id, until) VALUES ('%[1]s', 'dupid', %[3]d + 1000);
`, idA, idB, legacyT, legacyMsg1, legacyMsg2, legacyMsg3, legacyMsg4, legacyQueue, legacyDAV, len(davEvent("e1")))

// legacyIndex writes an index as the code before ablage did: the old
// migrations up to version (all if 0), then rows, plus the files the
// fixture's rows name.
func legacyIndex(t *testing.T, dir string, version int, rows string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, dbName)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if version == 0 {
		version = len(legacyMigrations)
	}
	for i := range version {
		if _, err := db.Exec(legacyMigrations[i]); err != nil {
			t.Fatalf("migration %d: %v", i+1, err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(rows); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct{ path, body string }{
		{filepath.Join(messagesDir, idA, legacyMsg1), "hello"},
		{filepath.Join(messagesDir, idA, legacyMsg2), "Message-Id: <m@x>\r\n\r\nx"},
		{filepath.Join(messagesDir, idA, legacyMsg3), "del"},
		{filepath.Join(messagesDir, idB, legacyMsg4), "bbbb"},
		{filepath.Join(queueDir, legacyQueue), "queued msg"},
		{filepath.Join(davDir, idA, "3", legacyDAV), string(davEvent("e1"))},
	} {
		p := filepath.Join(dir, f.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func backups(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, dbName+".pre-ablage-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func userVersion(t *testing.T, file string) int {
	t.Helper()
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func schemaOf(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.rdb.Query(`SELECT type, name, coalesce(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, q string
		if err := rows.Scan(&typ, &name, &q); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %s %s\n", typ, name, q)
	}
	return b.String()
}

// TestLegacyMigration converts an index written by the code before ablage
// and checks every value through the store, then that nothing changes for
// clients: UIDs, UIDVALIDITY, DAV ETags and sync tokens, the event cursor.
func TestLegacyMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	legacyIndex(t, dir, 0, legacyRows)
	s := open(t, dir, 0)

	// The backup is the old index, readable by the owner only.
	bs := backups(t, dir)
	if len(bs) != 1 {
		t.Fatalf("backups %v", bs)
	}
	if fi, err := os.Stat(bs[0]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup %v %v", fi.Mode(), err)
	}
	if v := userVersion(t, bs[0]); v != len(legacyMigrations) {
		t.Fatalf("backup version %d", v)
	}
	if v := userVersion(t, filepath.Join(dir, dbName)); v != ablageVersion {
		t.Fatalf("version %d", v)
	}
	var bad int
	if err := s.rdb.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("foreign key check %d %v", bad, err)
	}
	if strings.Contains(schemaOf(t, s), "legacy_") {
		t.Fatal("legacy tables left")
	}

	// Folders and messages.
	folders, err := s.ListFolders(ctx, idA)
	if err != nil {
		t.Fatal(err)
	}
	want := []Folder{
		{Name: Inbox, UIDValidity: 1700000050, UIDNext: 4, Subscribed: true},
		{Name: "Sent", SpecialUse: SpecialSent, UIDValidity: 1700000051, UIDNext: 1},
		{Name: "Ünïcødé/Ordner", UIDValidity: 1700000052, UIDNext: 3},
	}
	if !slices.Equal(folders, want) {
		t.Fatalf("folders %+v", folders)
	}
	if u, _ := s.Usage(ctx, idA); u != (Usage{Bytes: 999, Messages: 3}) {
		t.Fatalf("usage %+v", u)
	}
	_, msgs, err := s.Select(ctx, idA, Inbox)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("inbox %v %v", msgs, err)
	}
	if m := msgs[0]; m.UID != 1 || m.Size != 5 || m.InternalDate.Unix() != legacyT || len(m.Flags) != 0 || read(t, s, m) != "hello" {
		t.Fatalf("message 1 %+v", m)
	}
	if m := msgs[1]; m.UID != 3 || m.InternalDate.Unix() != legacyT+1 ||
		!slices.Equal(m.Flags, []string{"$Forwarded", FlagAnswered, FlagSeen, "custom"}) {
		t.Fatalf("message 3 %+v", m)
	}
	_, msgs, _ = s.Select(ctx, idA, "Ünïcødé/Ordner")
	if len(msgs) != 1 || !slices.Equal(msgs[0].Flags, []string{FlagDeleted, FlagDraft, FlagFlagged}) {
		t.Fatalf("flags %+v", msgs)
	}
	// The read state of the other reader.
	_, msgs, _ = s.SelectAs(ctx, idA, Inbox, idB)
	if !msgs[0].HasFlag(FlagSeen) || msgs[1].HasFlag(FlagSeen) {
		t.Fatalf("read state %+v", msgs)
	}
	st, err := s.FolderStatus(ctx, idA, Inbox)
	if err != nil || st.Messages != 2 || st.Unseen != 1 || st.Size != 12 || st.Deleted != 0 {
		t.Fatalf("status %+v %v", st, err)
	}
	if st, _ := s.FolderStatusAs(ctx, idA, Inbox, idB); st.Unseen != 1 {
		t.Fatalf("status as reader %+v", st)
	}
	if st, _ := s.FolderStatus(ctx, idA, "Ünïcødé/Ordner"); st.Messages != 1 || st.Unseen != 1 || st.Deleted != 1 || st.Size != 3 {
		t.Fatalf("status %+v", st)
	}
	// The Message-ID was kept: a second copy is stored once.
	dup, err := s.Append(ctx, idA, Inbox, strings.NewReader("Message-Id: <m@x>\r\n\r\nx"), AppendOptions{Dedupe: true})
	if err != nil || !dup.Duplicate || dup.UID != 3 {
		t.Fatalf("dedupe %+v %v", dup, err)
	}
	if r := appendMsg(t, s, idA, Inbox, "new"); r.UID != 4 || r.UIDValidity != 1700000050 {
		t.Fatalf("append %+v", r)
	}

	// Service state.
	ev, err := s.Events(ctx, 0)
	if err != nil || len(ev) != 2 || ev[0].ID != 1 || ev[1].ID != 2 || ev[0].At.Unix() != legacyT ||
		ev[1].Account == nil || ev[1].Account.AppPasswordID != "p1" {
		t.Fatalf("events %+v %v", ev, err)
	}
	if err := s.AddEvent(ctx, config.Event{Kind: config.EventBlocked, StorageID: idA}); err != nil {
		t.Fatal(err)
	}
	// Event 3 was handed out (and deleted) before: the next one is 4.
	if ev, _ := s.Events(ctx, 2); len(ev) != 1 || ev[0].ID != 4 {
		t.Fatalf("new event %+v", ev)
	}
	if uses, _ := s.AppPasswordUses(ctx, idA); uses["p1"].Network != "10.0.0.0/24" || uses["p1"].At.Unix() != legacyT+1 {
		t.Fatalf("uses %+v", uses)
	}
	var conflict *BindingConflictError
	if err := s.Bind(ctx, map[string]string{idA: "other@example.com"}); !errors.As(err, &conflict) || conflict.Bound != "a@example.com" {
		t.Fatalf("bind %v", err)
	}
	if b, _ := s.Blocked(ctx, idA); !b.Blocked || b.Reason != "spam" || b.At.Unix() != legacyT+5 {
		t.Fatalf("block %+v", b)
	}
	q, err := s.QueueMessage(ctx, "7")
	if err != nil || q.File != legacyQueue || q.Sender != "bounce@x" || q.Size != 10 || q.Created.Unix() != legacyT || len(q.Rcpts) != 2 {
		t.Fatalf("queue %+v %v", q, err)
	}
	if r := q.Rcpts; r[0].ID != "30" || r[0].Address != "b@y" || r[0].NextTry.Unix() != legacyT ||
		r[1].Address != "a@z" || r[1].State != RcptFailed || r[1].Attempts != 3 || r[1].LastError != "fail" || r[1].NextTry.Unix() != legacyT+60 {
		t.Fatalf("rcpts %+v", r)
	}
	if n, _ := s.QueueLength(ctx); n != 1 {
		t.Fatalf("queue length %d", n)
	}
	if n, _ := s.CountMarks(ctx, KeySends+idA, time.Unix(legacyT-1, 0)); n != 1 {
		t.Fatalf("send log %d", n)
	}
	if n, _ := s.CountMarks(ctx, KeySends+idA, time.Unix(legacyT, 0)); n != 0 {
		t.Fatalf("send log after %d", n)
	}
	if g, err := s.r.greylist.Get(ctx, "k1"); err != nil || g.Passed != nil || g.First.Unix() != legacyT || g.Net != "net1" {
		t.Fatalf("greylist k1 %+v %v", g, err)
	}
	if g, _ := s.r.greylist.Get(ctx, "k2"); g.Passed == nil || g.Passed.Unix() != legacyT+400 {
		t.Fatalf("greylist k2 %+v", g)
	}
	j, err := s.Import(ctx, idA, "7")
	if err != nil || j.Kind != "imap" || j.Port != 993 || j.Copied != 3 || j.Created.Unix() != legacyT ||
		j.Started.Unix() != legacyT+1 || j.Finished.Unix() != legacyT+2 {
		t.Fatalf("import 7 %+v %v", j, err)
	}
	if j, _ := s.Import(ctx, idA, "8"); !j.Started.IsZero() || !j.Finished.IsZero() || j.Received != 50 || j.FileName != "x.mbox" {
		t.Fatalf("import 8 %+v", j)
	}
	if js, _ := s.Imports(ctx, idA); len(js) != 2 || js[0].ID != "8" {
		t.Fatalf("imports %+v", js)
	}
	if f, _ := s.ImportFailures(ctx, "7"); len(f) != 2 || f[0].Subject != "Betreff ü" || f[0].Date.Unix() != legacyT || f[1].Folder != "Sent" || !f[1].Date.IsZero() {
		t.Fatalf("failures %+v", f)
	}
	if ok, _ := s.ImportSeen(ctx, idA, "INBOX", []byte{0, 0xff, 0x10}); !ok {
		t.Fatal("import key lost")
	}
	if wm, _ := s.ImportWatermark(ctx, idA, "imap.x", "u", "INBOX"); wm.UIDValidity != 42 || wm.LastUID != 17 {
		t.Fatalf("watermark %+v", wm)
	}
	if o, _ := s.AccountOverlays(ctx); string(o[idA]) != `{"name": "Ä"}` {
		t.Fatalf("overlay %q", o[idA])
	}
	if a, ok, _ := s.AccountSessionByHash(ctx, []byte{1, 2}); !ok || a.Credential != "cred" || a.Created.Unix() != legacyT || a.LastSeen.Unix() != legacyT+9 {
		t.Fatalf("session %+v", a)
	}
	if name, content, ok, _ := s.ActiveSieveScript(ctx, idA); !ok || name != "main" || content != "keep;" {
		t.Fatalf("active script %q", name)
	}
	if sc, _ := s.SieveScripts(ctx, idA); len(sc) != 2 || sc[1].Size != len("stop;ü") || sc[1].Active || sc[0].Updated.Unix() != legacyT {
		t.Fatalf("scripts %+v", sc)
	}
	if ok, _ := s.SieveDuplicateSeen(ctx, idA, "dupid", time.Unix(legacyT, 0)); !ok {
		t.Fatal("duplicate lost")
	}
	if ok, _ := s.SieveReplied(ctx, idA, "h", "s@x", time.Hour, time.Unix(legacyT, 0)); ok {
		t.Fatal("reply forgotten")
	}

	// Calendars: the same id, ETags and sync tokens as before.
	c, err := s.DAVCollection(ctx, idA, davobj.Calendar, DefaultCalendar)
	if err != nil || c.ID != "3" || c.Epoch != 1700000200 || c.Seq != 5 || c.PrunedSeq != 1 || !slices.Equal(c.Components, []string{"VEVENT"}) || c.DisplayName != "Kalender" {
		t.Fatalf("collection %+v %v", c, err)
	}
	oldToken := func(seq int) string { return fmt.Sprintf("urn:ownpanel:sync:%x:%x:%x", 1700000200, 3, seq) }
	if c.SyncToken() != oldToken(5) {
		t.Fatalf("token %s", c.SyncToken())
	}
	o, err := s.DAVObject(ctx, c, "e1.ics")
	if err != nil || o.ETag() != fmt.Sprintf("%x-%x-%x", 1700000200, 3, 5) {
		t.Fatalf("object %+v %v", o, err)
	}
	if data, err := s.ReadDAVObject(o); err != nil || !bytes.Equal(data, davEvent("e1")) {
		t.Fatalf("read %v", err)
	}
	changed, deleted, err := s.DAVChanges(ctx, c, oldToken(3))
	if err != nil || len(changed) != 1 || !slices.Equal(deleted, []string{"gone.ics"}) {
		t.Fatalf("changes %v %v %v", changed, deleted, err)
	}
	if _, _, err := s.DAVChanges(ctx, c, oldToken(0)); !errors.Is(err, ErrInvalidSyncToken) {
		t.Fatalf("pruned token %v", err)
	}
	if cs, _ := s.DAVCollections(ctx, idA, ""); len(cs) != 1 || cs[0].Objects != 1 || cs[0].Bytes != int64(len(davEvent("e1"))) {
		t.Fatalf("collections %+v", cs)
	}

	// The start-up check finds index and files in line: no new
	// UIDVALIDITY, no new DAV epoch.
	rep, err := s.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep != (ReconcileReport{}) {
		t.Fatalf("reconcile %+v", rep)
	}
	if f, _ := s.ListFolders(ctx, idA); f[0].UIDValidity != 1700000050 {
		t.Fatalf("UIDVALIDITY changed: %+v", f[0])
	}
	if c2, _ := s.DAVCollection(ctx, idA, davobj.Calendar, DefaultCalendar); c2.Epoch != c.Epoch {
		t.Fatal("DAV epoch changed")
	}

	// Opening again changes nothing.
	schema := schemaOf(t, s)
	s.Close()
	s = open(t, dir, 0)
	if got := schemaOf(t, s); got != schema {
		t.Fatalf("schema changed on a second open:\n%s\n%s", schema, got)
	}
	if bs := backups(t, dir); len(bs) != 1 {
		t.Fatalf("backups after the second open %v", bs)
	}
	if _, msgs, _ := s.Select(ctx, idA, Inbox); len(msgs) != 3 {
		t.Fatalf("messages after reopening %d", len(msgs))
	}
}

// TestLegacyMigrationFailure: a conversion that fails half-way leaves the
// old index as it was (and the backup); the next start converts it.
func TestLegacyMigrationFailure(t *testing.T) {
	dir := t.TempDir()
	legacyIndex(t, dir, 0, legacyRows)
	boom := errors.New("boom")
	legacyCopied = func(table string) error {
		if table == "dav_objects" {
			return boom
		}
		return nil
	}
	defer func() { legacyCopied = nil }()
	if _, err := Open(dir, Options{}); !errors.Is(err, boom) {
		t.Fatalf("open %v", err)
	}
	file := filepath.Join(dir, dbName)
	if v := userVersion(t, file); v != len(legacyMigrations) {
		t.Fatalf("version %d", v)
	}
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM messages WHERE flags != ''`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("old messages %d %v", n, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name LIKE 'legacy_%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("legacy tables %d %v", n, err)
	}
	db.Close()
	if bs := backups(t, dir); len(bs) != 1 {
		t.Fatalf("backups %v", bs)
	}

	legacyCopied = nil
	s := open(t, dir, 0)
	if _, msgs, err := s.Select(context.Background(), idA, Inbox); err != nil || len(msgs) != 2 {
		t.Fatalf("after retry %v %v", msgs, err)
	}
	if bs := backups(t, dir); len(bs) != 2 {
		t.Fatalf("backups after retry %v", bs)
	}
}

// TestLegacyMigrationOldVersion: an index of an older version gets the
// old migrations it lacks first.
func TestLegacyMigrationOldVersion(t *testing.T) {
	dir := t.TempDir()
	legacyIndex(t, dir, 1, fmt.Sprintf(`
INSERT INTO mailboxes (storage_id, created, used_bytes, messages) VALUES ('%[1]s', 1, 5, 1);
INSERT INTO folders (id, storage_id, name, uid_validity, uid_next, subscribed) VALUES (1, '%[1]s', 'INBOX', 77, 2, 1);
INSERT INTO messages (id, folder_id, storage_id, uid, file, size, internal_date, flags) VALUES (1, 1, '%[1]s', 1, '%[2]s', 5, 1, '\Flagged');`,
		idA, legacyMsg1))
	s := open(t, dir, 0)
	f, msgs, err := s.Select(context.Background(), idA, Inbox)
	if err != nil || f.UIDValidity != 77 || len(msgs) != 1 || !slices.Equal(msgs[0].Flags, []string{FlagFlagged}) {
		t.Fatalf("%+v %+v %v", f, msgs, err)
	}
}

// TestFreshIndexNoLegacy: a new index is created by ablage, without a
// backup.
func TestFreshIndexNoLegacy(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, 0)
	appendMsg(t, s, idA, Inbox, "x")
	if bs := backups(t, dir); len(bs) != 0 {
		t.Fatalf("backups %v", bs)
	}
	if v := userVersion(t, filepath.Join(dir, dbName)); v != ablageVersion {
		t.Fatalf("version %d", v)
	}
}
