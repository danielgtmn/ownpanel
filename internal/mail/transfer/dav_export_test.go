package transfer

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// TestExportFoldersNamedLikeCalendars: mail folders called "calendars" or
// "contacts" come back from our own export like any other, next to the
// calendars and address books in the zip.
func TestExportFoldersNamedLikeCalendars(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st := e.store
	for _, f := range []string{"calendars", "calendars/2024", "contacts"} {
		must(t, st.CreateFolder(ctx, idAlice, f))
		_, err := st.Append(ctx, idAlice, f, strings.NewReader("Subject: "+f+"\r\n\r\nx\r\n"), storage.AppendOptions{Date: time.Now()})
		must(t, err)
	}
	must(t, st.EnsureDefaultDAVCollections(ctx, idAlice))
	cal, err := st.DAVCollection(ctx, idAlice, davobj.Calendar, storage.DefaultCalendar)
	must(t, err)
	ev := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:ev1\r\nDTSTAMP:20240101T000000Z\r\nDTSTART:20240101T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	info, _, err := davobj.ParseCalendar(ev, nil)
	must(t, err)
	_, _, err = st.PutDAVObject(ctx, cal, "ev1.ics", ev, info, storage.PutCondition{})
	must(t, err)

	var zipBuf bytes.Buffer
	must(t, Export(ctx, st, idAlice, &zipBuf, time.Now()))
	j := e.importFile(idBob, "alice-export.zip", zipBuf.Bytes(), config.ImportChoices{})
	if j.State != config.ImportDone || j.Copied != 3 || j.Check.SkippedEntries != 0 {
		t.Fatalf("import %+v %+v", j, j.Check)
	}
	for _, f := range []string{"calendars", "calendars/2024", "contacts"} {
		_, msgs, err := st.Select(ctx, idBob, f)
		if err != nil || len(msgs) != 1 {
			t.Fatalf("%s: %d messages, %v", f, len(msgs), err)
		}
	}
}
