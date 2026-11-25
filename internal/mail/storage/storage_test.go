package storage

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

const (
	idA = "0123456789abcdef0123456789abcdef"
	idB = "fedcba9876543210fedcba9876543210"
)

func open(t *testing.T, dir string, quota int64) *Store {
	t.Helper()
	s, err := Open(dir, Options{Quota: func(string) int64 { return quota }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func appendMsg(t *testing.T, s *Store, id, folder, body string, flags ...string) AppendResult {
	t.Helper()
	r, err := s.Append(context.Background(), id, folder, strings.NewReader(body), AppendOptions{Flags: flags})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func read(t *testing.T, s *Store, m Message) string {
	t.Helper()
	f, err := s.Open(m)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return string(b)
}

func TestDefaultFoldersAndAppend(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir(), 0)
	folders, err := s.ListFolders(ctx, idA)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range folders {
		names = append(names, f.Name+f.SpecialUse)
		if !f.Subscribed || f.UIDNext != 1 || f.UIDValidity == 0 {
			t.Errorf("folder %+v", f)
		}
	}
	want := []string{"INBOX", `Archive\Archive`, `Drafts\Drafts`, `Junk\Junk`, `Sent\Sent`, `Trash\Trash`}
	if !slices.Equal(names, want) {
		t.Fatalf("folders %v, want %v", names, want)
	}

	r1 := appendMsg(t, s, idA, "inbox", "Subject: one\r\n\r\nhello\r\n")
	r2 := appendMsg(t, s, idA, "INBOX", "Subject: two\r\n\r\nworld\r\n", `\seen`, "$Label1")
	if r1.UID != 1 || r2.UID != 2 || r1.UIDValidity != r2.UIDValidity {
		t.Fatalf("results %+v %+v", r1, r2)
	}
	f, msgs, err := s.Select(ctx, idA, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if f.UIDNext != 3 || len(msgs) != 2 {
		t.Fatalf("select %+v %d", f, len(msgs))
	}
	if read(t, s, msgs[1]) != "Subject: two\r\n\r\nworld\r\n" {
		t.Fatal("content differs")
	}
	if !slices.Equal(msgs[1].Flags, []string{"$Label1", `\Seen`}) {
		t.Fatalf("flags %v", msgs[1].Flags)
	}
	st, err := s.FolderStatus(ctx, idA, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if st.Messages != 2 || st.Unseen != 1 || st.Size != r1.Size+r2.Size {
		t.Fatalf("status %+v", st)
	}
	u, _ := s.Usage(ctx, idA)
	if u.Messages != 2 || u.Bytes != r1.Size+r2.Size {
		t.Fatalf("usage %+v", u)
	}
	if _, err := s.Append(ctx, idA, "Nope", strings.NewReader("x"), AppendOptions{}); !errors.Is(err, ErrNoFolder) {
		t.Fatalf("append to missing folder: %v", err)
	}
	if _, err := s.Append(ctx, idA, "INBOX", strings.NewReader("x"), AppendOptions{Flags: []string{`\Recent`}}); !errors.Is(err, ErrInvalidFlag) {
		t.Fatalf("\\Recent: %v", err)
	}
	if _, err := s.Append(ctx, idA, "INBOX", strings.NewReader("123456"), AppendOptions{MaxSize: 5}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	if _, err := s.ListFolders(ctx, "../etc"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("bad id: %v", err)
	}
	// Nothing left behind in tmp.
	if e, _ := os.ReadDir(s.TempDir()); len(e) != 0 {
		t.Fatalf("tmp not empty: %v", e)
	}
}

func TestFlags(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir(), 0)
	appendMsg(t, s, idA, "INBOX", "a")
	appendMsg(t, s, idA, "INBOX", "b")
	var got []Change
	var mu sync.Mutex
	cancel := s.Watch(idA, func(c Change) { mu.Lock(); got = append(got, c); mu.Unlock() })
	defer cancel()

	ms, err := s.SetFlags(ctx, idA, "INBOX", []uint32{1, 2, 9}, FlagsAdd, []string{`\Flagged`, `\SEEN`})
	if err != nil || len(ms) != 2 || !slices.Equal(ms[0].Flags, []string{`\Flagged`, `\Seen`}) {
		t.Fatalf("add: %v %+v", err, ms)
	}
	ms, _ = s.SetFlags(ctx, idA, "INBOX", []uint32{1}, FlagsRemove, []string{`\Flagged`})
	if !slices.Equal(ms[0].Flags, []string{`\Seen`}) {
		t.Fatalf("remove: %v", ms[0].Flags)
	}
	ms, _ = s.SetFlags(ctx, idA, "INBOX", []uint32{2}, FlagsReplace, []string{`\Answered`, "Work"})
	if !slices.Equal(ms[0].Flags, []string{"Work", `\Answered`}) || !ms[0].HasFlag(`\answered`) {
		t.Fatalf("replace: %v", ms[0].Flags)
	}
	if _, err := s.SetFlags(ctx, idA, "INBOX", []uint32{1}, FlagsAdd, []string{"bad flag"}); !errors.Is(err, ErrInvalidFlag) {
		t.Fatalf("invalid keyword: %v", err)
	}
	st, _ := s.FolderStatus(ctx, idA, "INBOX")
	if st.Unseen != 1 {
		t.Fatalf("unseen %d", st.Unseen)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 || got[0].Kind != FlagsChanged || !slices.Equal(got[0].UIDs, []uint32{1, 2}) {
		t.Fatalf("changes %+v", got)
	}
}

func TestCopyMoveExpunge(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir, 0)
	appendMsg(t, s, idA, "INBOX", "one")
	appendMsg(t, s, idA, "INBOX", "two", `\Seen`)
	appendMsg(t, s, idA, "INBOX", "three")

	cp, err := s.Copy(ctx, idA, "INBOX", []uint32{2, 3, 7}, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cp.Source, []uint32{2, 3}) || !slices.Equal(cp.Dest, []uint32{1, 2}) {
		t.Fatalf("copy %+v", cp)
	}
	u, _ := s.Usage(ctx, idA)
	if u.Messages != 5 || u.Bytes != 3+3+5+3+5 {
		t.Fatalf("usage after copy %+v", u)
	}
	mv, err := s.Move(ctx, idA, "INBOX", []uint32{1}, "Trash")
	if err != nil || !slices.Equal(mv.Dest, []uint32{1}) {
		t.Fatalf("move %v %+v", err, mv)
	}
	_, inbox, _ := s.Select(ctx, idA, "INBOX")
	if len(inbox) != 2 || inbox[0].UID != 2 {
		t.Fatalf("inbox after move %+v", inbox)
	}
	_, arch, _ := s.Select(ctx, idA, "Archive")
	if read(t, s, arch[0]) != "two" || !arch[0].HasFlag(`\Seen`) {
		t.Fatal("copy lost content or flags")
	}

	// Expunge the original of a copied message: the shared file must stay.
	s.SetFlags(ctx, idA, "INBOX", []uint32{2, 3}, FlagsAdd, []string{`\Deleted`})
	gone, err := s.Expunge(ctx, idA, "INBOX", []uint32{2})
	if err != nil || !slices.Equal(gone, []uint32{2}) {
		t.Fatalf("uid expunge %v %v", err, gone)
	}
	if read(t, s, arch[0]) != "two" {
		t.Fatal("shared file removed")
	}
	gone, _ = s.Expunge(ctx, idA, "INBOX", nil)
	if !slices.Equal(gone, []uint32{3}) {
		t.Fatalf("expunge %v", gone)
	}
	// Remove the copies too: now the files go.
	s.SetFlags(ctx, idA, "Archive", []uint32{1, 2}, FlagsAdd, []string{`\Deleted`})
	s.Expunge(ctx, idA, "Archive", nil)
	files, _ := os.ReadDir(filepath.Join(dir, messagesDir, idA))
	if len(files) != 1 { // "one" in Trash
		t.Fatalf("files left %d", len(files))
	}
	if e, _ := os.ReadDir(filepath.Join(dir, trashDir)); len(e) != 0 {
		t.Fatal("trash not emptied")
	}
	u, _ = s.Usage(ctx, idA)
	if u.Messages != 1 || u.Bytes != 3 {
		t.Fatalf("usage %+v", u)
	}
}

func TestFolderOperations(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir(), 0)
	if err := s.CreateFolder(ctx, idA, "Work"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFolder(ctx, idA, "Work/2026"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFolder(ctx, idA, "work"); err != nil {
		t.Fatal(err) // names other than INBOX are case-sensitive
	}
	if err := s.CreateFolder(ctx, idA, "Work"); !errors.Is(err, ErrFolderExists) {
		t.Fatalf("duplicate: %v", err)
	}
	for _, bad := range []string{"", "/a", "a/", "a//b", "a*", "x\x01"} {
		if err := s.CreateFolder(ctx, idA, bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	appendMsg(t, s, idA, "Work/2026", "m")
	if err := s.RenameFolder(ctx, idA, "Work", "Jobs"); err != nil {
		t.Fatal(err)
	}
	_, msgs, err := s.Select(ctx, idA, "Jobs/2026")
	if err != nil || len(msgs) != 1 {
		t.Fatalf("renamed child: %v %d", err, len(msgs))
	}
	if err := s.SetSubscribed(ctx, idA, "Jobs", true); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFolder(ctx, idA, "Jobs/2026"); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.Usage(ctx, idA); u.Messages != 0 {
		t.Fatalf("usage after delete %+v", u)
	}
	if err := s.DeleteFolder(ctx, idA, "inbox"); !errors.Is(err, ErrInbox) {
		t.Fatalf("delete inbox: %v", err)
	}
	// Renaming INBOX moves the messages and leaves INBOX empty.
	appendMsg(t, s, idA, "INBOX", "x")
	if err := s.RenameFolder(ctx, idA, "INBOX", "Old"); err != nil {
		t.Fatal(err)
	}
	_, in, _ := s.Select(ctx, idA, "INBOX")
	_, old, _ := s.Select(ctx, idA, "Old")
	if len(in) != 0 || len(old) != 1 {
		t.Fatalf("inbox rename: %d %d", len(in), len(old))
	}
}

func TestQuota(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir(), 10)
	appendMsg(t, s, idA, "INBOX", "12345678")
	if _, err := s.Append(ctx, idA, "INBOX", strings.NewReader("123"), AppendOptions{}); !errors.Is(err, ErrOverQuota) {
		t.Fatalf("over quota: %v", err)
	}
	if _, err := s.Copy(ctx, idA, "INBOX", []uint32{1}, "Archive"); !errors.Is(err, ErrOverQuota) {
		t.Fatalf("copy over quota: %v", err)
	}
	appendMsg(t, s, idA, "INBOX", "12")
	if _, err := s.Append(ctx, idA, "INBOX", strings.NewReader("1"), AppendOptions{}); !errors.Is(err, ErrOverQuota) {
		t.Fatalf("full: %v", err)
	}
	// Moving does not use more space.
	if _, err := s.Move(ctx, idA, "INBOX", []uint32{1}, "Archive"); err != nil {
		t.Fatal(err)
	}
	if e, _ := os.ReadDir(s.TempDir()); len(e) != 0 {
		t.Fatal("tmp not cleaned after refusal")
	}
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir, 0)
	appendMsg(t, s, idA, "INBOX", "keep")
	appendMsg(t, s, idA, "INBOX", "lose")
	f0, _ := s.FolderStatus(ctx, idA, "Sent")
	s.Close()

	// Clean start: nothing changes.
	s = open(t, dir, 0)
	rep, err := s.Reconcile(ctx)
	if err != nil || rep != (ReconcileReport{}) {
		t.Fatalf("clean reconcile: %v %+v", err, rep)
	}
	_, msgs, _ := s.Select(ctx, idA, "INBOX")
	s.Close()

	// Remove one file, add an unknown one for another mailbox.
	os.Remove(filepath.Join(dir, messagesDir, idA, msgs[1].file))
	os.MkdirAll(filepath.Join(dir, messagesDir, idB), 0o700)
	os.WriteFile(filepath.Join(dir, messagesDir, idB, "123.0011223344556677"), []byte("found"), 0o600)
	os.WriteFile(filepath.Join(dir, messagesDir, idB, "junk.txt"), []byte("ignored"), 0o600)
	os.WriteFile(filepath.Join(dir, tmpDir, "append-1"), []byte("partial"), 0o600)

	s = open(t, dir, 0)
	rep, err = s.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.MissingFiles != 1 || rep.IndexedFiles != 1 || !rep.NewUIDValidity {
		t.Fatalf("report %+v", rep)
	}
	f1, _ := s.FolderStatus(ctx, idA, "Sent")
	if f1.UIDValidity == f0.UIDValidity {
		t.Fatal("UIDVALIDITY unchanged")
	}
	_, a, _ := s.Select(ctx, idA, "INBOX")
	_, b, _ := s.Select(ctx, idB, "INBOX")
	if len(a) != 1 || len(b) != 1 || read(t, s, b[0]) != "found" {
		t.Fatalf("after reconcile %d %d", len(a), len(b))
	}
	if u, _ := s.Usage(ctx, idA); u.Messages != 1 || u.Bytes != 4 {
		t.Fatalf("usage %+v", u)
	}
	if e, _ := os.ReadDir(filepath.Join(dir, tmpDir)); len(e) != 0 {
		t.Fatal("tmp not cleared")
	}
	ids, _ := s.StorageIDs(ctx)
	if !slices.Equal(ids, []string{idA, idB}) {
		t.Fatalf("ids %v", ids)
	}
	s.Close()

	// Restore marker: new UIDVALIDITY even without differences.
	os.WriteFile(filepath.Join(dir, RestoredFile), nil, 0o600)
	s = open(t, dir, 0)
	rep, err = s.Reconcile(ctx)
	if err != nil || !rep.Restored || !rep.NewUIDValidity {
		t.Fatalf("restored: %v %+v", err, rep)
	}
	f2, _ := s.FolderStatus(ctx, idA, "Sent")
	if f2.UIDValidity == f1.UIDValidity {
		t.Fatal("UIDVALIDITY unchanged after restore")
	}
	if _, err := os.Stat(filepath.Join(dir, RestoredFile)); !os.IsNotExist(err) {
		t.Fatal("marker not removed")
	}
}

func TestUIDValidityMonotonic(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	s, err := Open(t.TempDir(), Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fs, _ := s.ListFolders(ctx, idA)
	seen := map[uint32]bool{}
	for _, f := range fs {
		if seen[f.UIDValidity] {
			t.Fatalf("UIDVALIDITY %d used twice", f.UIDValidity)
		}
		seen[f.UIDValidity] = true
	}
	// Deleting and recreating a folder gives a new UIDVALIDITY.
	s.DeleteFolder(ctx, idA, "Junk")
	s.CreateFolder(ctx, idA, "Junk")
	st, _ := s.FolderStatus(ctx, idA, "Junk")
	if seen[st.UIDValidity] {
		t.Fatal("recreated folder reuses UIDVALIDITY")
	}
}

// TestRollbackKeepsFiles: a failing write after files were moved must put
// them back.
func TestRollbackKeepsFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir, 0)
	appendMsg(t, s, idA, "Junk", "spam")
	s.SetFlags(ctx, idA, "Junk", []uint32{1}, FlagsAdd, []string{`\Deleted`})
	boom := errors.New("boom")
	err := s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		if _, err := s.removeMessages(tx, ch, idA, mustFolder(t, s, idA, "Junk"), []uint32{1}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	_, msgs, _ := s.Select(ctx, idA, "Junk")
	if len(msgs) != 1 || read(t, s, msgs[0]) != "spam" {
		t.Fatal("message lost on rollback")
	}
}

func mustFolder(t *testing.T, s *Store, id, name string) int64 {
	var fid int64
	if err := s.rdb.QueryRow(`SELECT id FROM folders WHERE storage_id = ? AND name = ?`, id, name).Scan(&fid); err != nil {
		t.Fatal(err)
	}
	return fid
}

func TestDeleteMailbox(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir, 0)
	appendMsg(t, s, idA, "INBOX", "x")
	appendMsg(t, s, idB, "INBOX", "y")
	s.Bind(ctx, map[string]string{idA: "a@example.com"})
	s.SetBlocked(ctx, idA, "test")
	deleted := make(chan Change, 1)
	defer s.Watch(idA, func(c Change) { deleted <- c })()
	if err := s.DeleteMailbox(ctx, idA); err != nil {
		t.Fatal(err)
	}
	if c := <-deleted; c.Kind != MailboxDeleted {
		t.Fatalf("change %+v", c)
	}
	if _, err := os.Stat(filepath.Join(dir, messagesDir, idA)); !os.IsNotExist(err) {
		t.Fatal("files remain")
	}
	ids, _ := s.StorageIDs(ctx)
	if !slices.Equal(ids, []string{idB}) {
		t.Fatalf("ids %v", ids)
	}
	if b, _ := s.Blocks(ctx); len(b) != 0 {
		t.Fatal("block remains")
	}
	// Binding is gone, so the id could be bound anew (it never is reused).
	if err := s.Bind(ctx, map[string]string{idA: "other@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMailbox(ctx, idA); err != nil {
		t.Fatal("delete without data:", err)
	}
}

func TestBindings(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir(), 0)
	if err := s.Bind(ctx, map[string]string{idA: "a@example.com", idB: "b@example.com"}); err != nil {
		t.Fatal(err)
	}
	// No data yet: rebinding is harmless.
	if err := s.Bind(ctx, map[string]string{idA: "a2@example.com"}); err != nil {
		t.Fatal(err)
	}
	appendMsg(t, s, idA, "INBOX", "x")
	var conflict *BindingConflictError
	if err := s.Bind(ctx, map[string]string{idA: "b@example.com"}); !errors.As(err, &conflict) {
		t.Fatalf("conflict: %v", err)
	}
	if err := s.Bind(ctx, map[string]string{idA: "a2@example.com"}); err != nil {
		t.Fatal(err)
	}
}

func TestBlocksAndEvents(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	s, err := Open(t.TempDir(), Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetBlocked(ctx, idA, "hourly limit")
	b, _ := s.Blocks(ctx)
	if !b[idA].Blocked || b[idA].Reason != "hourly limit" || !b[idA].At.Equal(now) {
		t.Fatalf("blocks %+v", b)
	}
	if ok, _ := s.Unblock(ctx, idA); !ok {
		t.Fatal("unblock")
	}
	if ok, _ := s.Unblock(ctx, idA); ok {
		t.Fatal("unblock twice")
	}
	for i := 0; i < MaxEvents+10; i++ {
		at := now
		if i < 5 {
			at = now.Add(-31 * 24 * time.Hour)
		}
		s.AddEvent(ctx, config.Event{At: at, Kind: config.EventBlocked, StorageID: idA, Detail: "x"})
	}
	ev, _ := s.Events(ctx, 0)
	if len(ev) != MaxEvents || ev[0].ID != 1 {
		t.Fatalf("events %d first %d", len(ev), ev[0].ID)
	}
	ev, _ = s.Events(ctx, int64(MaxEvents))
	if len(ev) != 10 {
		t.Fatalf("next page %d", len(ev))
	}
	s.PruneEvents(ctx)
	ev, _ = s.Events(ctx, 0)
	if len(ev) != MaxEvents || ev[0].ID != 6 {
		t.Fatalf("after prune %d first %d", len(ev), ev[0].ID)
	}
}

func TestConcurrentAppends(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir(), 0)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Append(ctx, idA, "INBOX", strings.NewReader("m"), AppendOptions{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	_, msgs, _ := s.Select(ctx, idA, "INBOX")
	if len(msgs) != 20 || msgs[19].UID != 20 {
		t.Fatalf("%d messages", len(msgs))
	}
}

func TestGreylist(t *testing.T) {
	s := open(t, t.TempDir(), 0)
	ctx := context.Background()
	t0 := time.Unix(1_800_000_000, 0)
	g := Greylist{Delay: 5 * time.Minute, Retry: 24 * time.Hour, Valid: 30 * 24 * time.Hour, PerNetwork: 2, Max: 4}
	check := func(key, net string, at time.Time) bool {
		t.Helper()
		ok, err := s.GreylistCheck(ctx, key, net, at, g)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	count := func() int {
		var n int
		s.rdb.QueryRow(`SELECT count(*) FROM greylist`).Scan(&n)
		return n
	}
	if check("a", "n1", t0) || check("b", "n1", t0) {
		t.Fatal("first attempts pass")
	}
	// The third waiting triplet of n1 is refused and not recorded.
	if check("c", "n1", t0) || count() != 2 {
		t.Fatalf("per network: %d rows", count())
	}
	if check("d", "n2", t0) || check("e", "n3", t0) || count() != 4 {
		t.Fatalf("other networks: %d rows", count())
	}
	// Overall bound.
	if check("f", "n4", t0) || count() != 4 {
		t.Fatalf("max: %d rows", count())
	}

	// Seen again before the delay, or passing again soon after the last
	// pass: no write (the writer is held, so a write would block).
	s.wmu.Lock()
	done := make(chan bool)
	go func() { done <- check("a", "n1", t0.Add(time.Minute)) }()
	select {
	case ok := <-done:
		if ok {
			t.Error("passed before the delay")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting triplet wrote")
	}
	s.wmu.Unlock()
	if !check("a", "n1", t0.Add(6*time.Minute)) {
		t.Fatal("after the delay")
	}
	s.wmu.Lock()
	go func() { done <- check("a", "n1", t0.Add(7*time.Minute)) }()
	select {
	case ok := <-done:
		if !ok {
			t.Error("known triplet refused")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("known triplet wrote")
	}
	s.wmu.Unlock()

	// After a day the triplets that never passed are gone; the passed one
	// stays until it was not seen for Valid.
	if err := s.PruneGreylist(ctx, t0.Add(25*time.Hour), g); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatalf("after a day: %d rows", count())
	}
	if err := s.PruneGreylist(ctx, t0.Add(31*24*time.Hour), g); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Fatalf("after Valid: %d rows", count())
	}
}
