package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
)

func davEvent(uid string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20250101T000000Z\r\nDTSTART:20250101T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
}

func putEvent(t *testing.T, s *Store, c DAVCollection, name, uid string) DAVObject {
	t.Helper()
	data := davEvent(uid)
	info, _, err := davobj.ParseCalendar(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := s.PutDAVObject(context.Background(), c, name, data, info, PutCondition{})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func defaultCalendar(t *testing.T, s *Store, id string) DAVCollection {
	t.Helper()
	ctx := context.Background()
	if err := s.EnsureDefaultDAVCollections(ctx, id); err != nil {
		t.Fatal(err)
	}
	c, err := s.DAVCollection(ctx, id, davobj.Calendar, DefaultCalendar)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDAVDeletionsPruned(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	s, err := Open(t.TempDir(), Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := defaultCalendar(t, s, idA)
	putEvent(t, s, c, "a.ics", "a")
	putEvent(t, s, c, "b.ics", "b")
	c, _ = s.DAVCollection(ctx, idA, davobj.Calendar, DefaultCalendar)
	tok := c.SyncToken()
	if err := s.DeleteDAVObject(ctx, c, "a.ics", PutCondition{}); err != nil {
		t.Fatal(err)
	}
	c, _ = s.DAVCollection(ctx, idA, davobj.Calendar, DefaultCalendar)
	changed, deleted, err := s.DAVChanges(ctx, c, tok)
	if err != nil || len(changed) != 0 || len(deleted) != 1 || deleted[0] != "a.ics" {
		t.Fatalf("%v %v %v", changed, deleted, err)
	}
	// After the retention the deletion is forgotten and the old token
	// refused; a fresh one works.
	now = now.Add(DAVDeletionRetention + time.Hour)
	if err := s.PruneDAVDeletions(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ = s.DAVCollection(ctx, idA, davobj.Calendar, DefaultCalendar)
	if _, _, err := s.DAVChanges(ctx, c, tok); !errors.Is(err, ErrInvalidSyncToken) {
		t.Fatalf("old token: %v", err)
	}
	if _, _, err := s.DAVChanges(ctx, c, c.SyncToken()); err != nil {
		t.Fatal(err)
	}
}

func TestDAVReconcileAndDelete(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir, 0)
	c := defaultCalendar(t, s, idA)
	a := putEvent(t, s, c, "a.ics", "a")
	putEvent(t, s, c, "b.ics", "b")
	collDir := filepath.Join(dir, davDir, idA, strconv.FormatInt(c.ID, 10))
	// A file of a lost row (crash before commit, or a backup taken while
	// writing), a damaged file, and a row whose file is gone.
	if err := os.WriteFile(filepath.Join(collDir, "1.0123456789abcdef"), davEvent("lost"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collDir, "2.0123456789abcdef"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(collDir, a.file)); err != nil {
		t.Fatal(err)
	}
	epoch := c.Epoch
	s.Close()
	s = open(t, dir, 0)
	rep, err := s.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.DAVMissing != 1 || rep.DAVIndexed != 1 || rep.DAVStray != 1 || !rep.NewDAVEpoch {
		t.Fatalf("%+v", rep)
	}
	c, _ = s.DAVCollection(ctx, idA, davobj.Calendar, DefaultCalendar)
	if c.Epoch == epoch {
		t.Fatal("epoch kept")
	}
	objs, err := s.DAVObjects(ctx, c, 0, 0)
	if err != nil || len(objs) != 2 {
		t.Fatalf("%+v %v", objs, err)
	}
	u, _ := s.Usage(ctx, idA)
	if u.Bytes != int64(len(davEvent("b"))+len(davEvent("lost"))+2*DAVObjectOverhead) {
		t.Fatalf("usage %+v", u)
	}
	if _, err := os.Stat(filepath.Join(collDir, "2.0123456789abcdef")); !os.IsNotExist(err) {
		t.Fatal("junk kept")
	}
	// Deleting the mailbox removes calendars and contacts too.
	if err := s.DeleteMailbox(ctx, idA); err != nil {
		t.Fatal(err)
	}
	if cs, err := s.DAVCollections(ctx, idA, ""); err != nil || len(cs) != 0 {
		t.Fatalf("%v %v", cs, err)
	}
	if _, err := os.Stat(filepath.Join(dir, davDir, idA)); !os.IsNotExist(err) {
		t.Fatal("files kept")
	}
}

func TestDAVLimits(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir(), 0)
	for i := 0; i < MaxDAVCollections; i++ {
		if _, err := s.CreateDAVCollection(ctx, idA, DAVCollection{Kind: davobj.Calendar, Name: "c" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateDAVCollection(ctx, idA, DAVCollection{Kind: davobj.Calendar, Name: "one-more"}); !errors.Is(err, ErrTooManyCollections) {
		t.Fatal(err)
	}
	if _, err := s.CreateDAVCollection(ctx, idB, DAVCollection{Kind: davobj.Calendar, Name: "../x"}); !errors.Is(err, ErrInvalidDAVName) {
		t.Fatal(err)
	}
}
