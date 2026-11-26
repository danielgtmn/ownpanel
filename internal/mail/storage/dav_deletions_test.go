package storage

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
)

// TestDAVDeletionsBoundedAndCounted: deletion records are capped per
// collection (older sync tokens are refused then), and objects and
// deletion records count towards the quota with a fixed overhead.
func TestDAVDeletionsBoundedAndCounted(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir(), 0)
	defer func(n int) { maxDAVDeletions = n }(maxDAVDeletions)
	maxDAVDeletions = 5
	c := defaultCalendar(t, s, idA)
	usage := func() int64 {
		u, err := s.Usage(ctx, idA)
		if err != nil {
			t.Fatal(err)
		}
		return u.Bytes
	}
	ev := int64(len(davEvent("e0")))
	putEvent(t, s, c, "e0.ics", "e0")
	if got := usage(); got != ev+DAVObjectOverhead {
		t.Fatalf("usage %d, want %d", got, ev+DAVObjectOverhead)
	}
	c, _ = s.DAVCollection(ctx, idA, davobj.Calendar, DefaultCalendar)
	first := c.SyncToken()
	for i := 1; i <= 8; i++ {
		name := "e" + strconv.Itoa(i) + ".ics"
		putEvent(t, s, c, name, "e"+strconv.Itoa(i))
		if err := s.DeleteDAVObject(ctx, c, name, PutCondition{}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.rdb.QueryRow(`SELECT count(*) FROM dav_deletions WHERE collection_id = ?`, c.ID).Scan(&n); err != nil || n != 5 {
		t.Fatalf("%d deletion records, %v", n, err)
	}
	if got := usage(); got != ev+DAVObjectOverhead+5*DAVDeletionOverhead {
		t.Fatalf("usage %d", got)
	}
	c, _ = s.DAVCollection(ctx, idA, davobj.Calendar, DefaultCalendar)
	if _, _, err := s.DAVChanges(ctx, c, first); !errors.Is(err, ErrInvalidSyncToken) {
		t.Fatalf("token older than the kept deletions: %v", err)
	}
	// The start-up check counts the same.
	if _, err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := usage(); got != ev+DAVObjectOverhead+5*DAVDeletionOverhead {
		t.Fatalf("usage after reconcile %d", got)
	}
	// Deleting the calendar gives everything back.
	if err := s.DeleteDAVCollection(ctx, idA, c.ID); err != nil {
		t.Fatal(err)
	}
	if got := usage(); got != 0 {
		t.Fatalf("usage after deleting the calendar %d", got)
	}
}
