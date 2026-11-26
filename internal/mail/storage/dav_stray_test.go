package storage

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
)

// TestDAVStrayFiles: a file left by a write that crashed before its commit
// was never acknowledged and is removed, so the client's retry under its
// own name works. After a restore, files newer than the index are kept
// under the name the UID gives.
func TestDAVStrayFiles(t *testing.T) {
	ctx := context.Background()
	for _, restored := range []bool{false, true} {
		dir := t.TempDir()
		s := open(t, dir, 0)
		c := defaultCalendar(t, s, idA)
		collDir := filepath.Join(dir, davDir, idA, strconv.FormatInt(c.ID, 10))
		if err := os.MkdirAll(collDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(collDir, "1.0123456789abcdef"), davEvent("retry-me"), 0o600); err != nil {
			t.Fatal(err)
		}
		if restored {
			if err := os.WriteFile(filepath.Join(dir, RestoredFile), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		s.Close()
		s = open(t, dir, 0)
		rep, err := s.Reconcile(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c, _ = s.DAVCollection(ctx, idA, davobj.Calendar, DefaultCalendar)
		objs, _ := s.DAVObjects(ctx, c, 0, 0)
		if !restored {
			if rep.DAVStray != 1 || rep.DAVIndexed != 0 || len(objs) != 0 {
				t.Fatalf("crash: %+v %v", rep, objs)
			}
			// The client tries again under its own name.
			putEvent(t, s, c, "client-name.ics", "retry-me")
			continue
		}
		if rep.DAVIndexed != 1 || len(objs) != 1 || objs[0].Name != "retry-me.ics" {
			t.Fatalf("restore: %+v %+v", rep, objs)
		}
	}
}
