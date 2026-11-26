package transfer

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// upload sends data as an upload in parts and waits for the scan.
func (e *env) upload(sid, name string, data []byte) config.ImportJob {
	e.t.Helper()
	ctx := context.Background()
	j, err := e.m.CreateUpload(ctx, sid, config.UploadRequest{Name: name, Size: int64(len(data))})
	if err != nil {
		e.t.Fatal(err)
	}
	half := int64(len(data) / 2)
	for _, part := range [][2]int64{{0, half}, {half, int64(len(data))}} {
		if part[1] == part[0] {
			continue
		}
		got, err := e.m.WritePart(ctx, sid, j.ID, part[0], bytes.NewReader(data[part[0]:part[1]]), part[1]-part[0])
		if err != nil || got != part[1] {
			e.t.Fatalf("part %v: %d %v", part, got, err)
		}
	}
	return e.wait(sid, j.ID, config.ImportReady, config.ImportFailed)
}

// importFile uploads, checks and imports data with the default choices.
func (e *env) importFile(sid, name string, data []byte, ch config.ImportChoices) config.ImportJob {
	e.t.Helper()
	j := e.upload(sid, name, data)
	if j.State != config.ImportReady {
		e.t.Fatalf("scan: %s %s", j.State, j.Error)
	}
	if _, err := e.m.Resume(context.Background(), sid, j.ID, config.ImportResume{Choices: ch}); err != nil {
		e.t.Fatal(err)
	}
	return e.wait(sid, j.ID, config.ImportDone, config.ImportFailed)
}

type zipEntry struct {
	name    string
	data    string
	nonUTF8 bool
	store   bool
}

func makeZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, en := range entries {
		m := zip.Deflate
		if en.store {
			m = zip.Store
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: en.name, Method: m, NonUTF8: en.nonUTF8})
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, en.data)
	}
	must(t, zw.Close())
	return buf.Bytes()
}

func checkFolder(c *config.ImportCheck, remote string) (config.ImportFolder, bool) {
	for _, f := range c.Folders {
		if f.Remote == remote {
			return f, true
		}
	}
	return config.ImportFolder{}, false
}

func TestFileMbox(t *testing.T) {
	e := newEnv(t)
	data := "From alice@example.com Thu Jan  4 10:00:00 2024\n" +
		"Authentication-Results: mail.example.net; dkim=pass\nStatus: RO\nX-Status: A\nSubject: one\n\n" +
		">From the start\nbody\n\n" +
		"From - Fri Jan 05 11:00:00 2024\nX-Mozilla-Status: 0005\nSubject: two\n\nsecond\n"
	j := e.importFile(idAlice, "Inbox.mbox", []byte(data), config.ImportChoices{})
	if j.State != config.ImportDone || j.Copied != 2 {
		t.Fatalf("job %+v", j)
	}
	got := e.messages(idAlice)["INBOX"]
	if len(got) != 2 || got[0] != "Subject: one\r\n\r\n>From the start\r\nbody\r\n" || got[1] != "Subject: two\r\n\r\nsecond\r\n" {
		t.Fatalf("stored %q", got)
	}
	m0, m1 := e.message(idAlice, "INBOX", 0), e.message(idAlice, "INBOX", 1)
	if !slices.Equal(m0.Flags, []string{storage.FlagAnswered, storage.FlagSeen}) || !slices.Equal(m1.Flags, []string{storage.FlagFlagged, storage.FlagSeen}) {
		t.Fatalf("flags %v %v", m0.Flags, m1.Flags)
	}
	if !m0.InternalDate.Equal(time.Date(2024, 1, 4, 10, 0, 0, 0, time.UTC)) || !m1.InternalDate.Equal(time.Date(2024, 1, 5, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("dates %v %v", m0.InternalDate, m1.InternalDate)
	}
	// The same file again: nothing new.
	j = e.importFile(idAlice, "Inbox.mbox", []byte(data), config.ImportChoices{})
	if j.Copied != 0 || j.Skipped != 2 {
		t.Fatalf("repeat %+v", j)
	}
	if _, err := os.Stat(e.m.spoolPath(j.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("spool left after the job")
	}
}

func TestFileEML(t *testing.T) {
	e := newEnv(t)
	data := "Received: from a by b; Wed, 3 Jan 2024 12:00:00 +0100\nDate: Tue, 2 Jan 2024 12:00:00 +0000\nSubject: eml\n\nhi\n"
	j := e.importFile(idAlice, "message.eml", []byte(data), config.ImportChoices{})
	if j.Copied != 1 {
		t.Fatalf("%+v", j)
	}
	m := e.message(idAlice, "INBOX", 0)
	if !m.InternalDate.Equal(time.Date(2024, 1, 3, 11, 0, 0, 0, time.UTC)) || len(m.Flags) != 0 {
		t.Fatalf("%v %v", m.InternalDate, m.Flags)
	}
}

func TestFileThunderbirdZip(t *testing.T) {
	e := newEnv(t)
	mbox := func(s string) string { return "From - Fri Jan 05 11:00:00 2024\nSubject: " + s + "\n\nx\n" }
	data := makeZip(t,
		zipEntry{name: "Inbox", data: mbox("in")},
		zipEntry{name: "Inbox.msf", data: "// mork"},
		zipEntry{name: "Inbox.sbd/Work", data: mbox("work")},
		zipEntry{name: "Sent", data: mbox("sent")},
		zipEntry{name: "Trash", data: mbox("trash")},
		zipEntry{name: "notes", data: "not mail"},
	)
	j := e.upload(idAlice, "thunderbird.zip", data)
	if j.Check == nil || j.Check.SkippedEntries != 2 {
		t.Fatalf("check %+v", j.Check)
	}
	if f, _ := checkFolder(j.Check, "Trash"); f.Selected || f.Note != config.ImportNoteTrash {
		t.Fatalf("trash %+v", f)
	}
	if f, _ := checkFolder(j.Check, "Inbox/Work"); f.Local != "INBOX/Work" || f.Messages != 1 {
		t.Fatalf("work %+v", f)
	}
	_, err := e.m.Resume(context.Background(), idAlice, j.ID, config.ImportResume{Choices: config.ImportChoices{Folders: []string{"Nope"}}})
	if status(err) != http.StatusBadRequest {
		t.Fatalf("unknown folder: %v", err)
	}
	must(t, nil)
	if _, err := e.m.Resume(context.Background(), idAlice, j.ID, config.ImportResume{}); err != nil {
		t.Fatal(err)
	}
	j = e.wait(idAlice, j.ID, config.ImportDone)
	got := e.messages(idAlice)
	for folder, want := range map[string][]string{"INBOX": {"in"}, "INBOX/Work": {"work"}, "Sent": {"sent"}, "Trash": nil} {
		if s := subjects(got[folder]); !slices.Equal(s, want) {
			t.Errorf("%s: %v", folder, s)
		}
	}
}

func TestFileAppleZip(t *testing.T) {
	e := newEnv(t)
	mbox := func(s string) string { return "From x@y Fri Jan  5 11:00:00 2024\nSubject: " + s + "\n\nx\n" }
	data := makeZip(t,
		zipEntry{name: "Work.mbox/mbox", data: mbox("w")},
		zipEntry{name: "Work.mbox/Info.plist", data: "<plist/>"},
		zipEntry{name: "Work.mbox/Sub.mbox/mbox", data: mbox("s")},
		zipEntry{name: "__MACOSX/._Work.mbox", data: "x"},
	)
	j := e.importFile(idAlice, "apple.zip", data, config.ImportChoices{})
	if j.Copied != 2 {
		t.Fatalf("%+v", j)
	}
	got := e.messages(idAlice)
	if !slices.Equal(subjects(got["Work"]), []string{"w"}) || !slices.Equal(subjects(got["Work/Sub"]), []string{"s"}) {
		t.Fatalf("%v", got)
	}
}

func TestFileTakeout(t *testing.T) {
	e := newEnv(t)
	m := func(labels, subject string) string {
		return "From 1787095557347826@xxx Tue Jan 21 06:36:06 +0000 2020\r\nX-GM-THRID: 1\r\nX-Gmail-Labels: " + labels +
			"\r\nSubject: " + subject + "\r\n\r\nbody\r\n\r\n"
	}
	mboxData := m("Inbox,Unread", "unread in inbox") + m("Sent,Opened", "sent") + m("Spam,Unread", "spam") +
		m("Work,Starred,Opened", "work") + m("Opened,Category Updates", "archived")
	data := makeZip(t, zipEntry{name: "Takeout/Mail/All mail Including Spam and Trash.mbox", data: mboxData},
		zipEntry{name: "Takeout/archive_browser.html", data: "<html>"})
	j := e.upload(idAlice, "takeout-20240101.zip", data)
	if j.State != config.ImportReady {
		t.Fatalf("%s %s", j.State, j.Error)
	}
	for remote, sel := range map[string]bool{"INBOX": true, "Sent": true, "Junk": false, "Work": true, "Archive": true} {
		if f, ok := checkFolder(j.Check, remote); !ok || f.Selected != sel || f.Messages != 1 {
			t.Errorf("%s: %+v %v", remote, f, ok)
		}
	}
	if _, err := e.m.Resume(context.Background(), idAlice, j.ID, config.ImportResume{}); err != nil {
		t.Fatal(err)
	}
	j = e.wait(idAlice, j.ID, config.ImportDone)
	if j.Copied != 4 {
		t.Fatalf("%+v", j)
	}
	got := e.messages(idAlice)
	if len(got["Junk"]) != 0 || !slices.Equal(subjects(got["Archive"]), []string{"archived"}) {
		t.Fatalf("%v", got)
	}
	if in := e.message(idAlice, "INBOX", 0); len(in.Flags) != 0 {
		t.Fatalf("inbox flags %v", in.Flags)
	}
	if w := e.message(idAlice, "Work", 0); !slices.Equal(w.Flags, []string{storage.FlagFlagged, storage.FlagSeen}) ||
		!w.InternalDate.Equal(time.Date(2020, 1, 21, 6, 36, 6, 0, time.UTC)) {
		t.Fatalf("work %v %v", w.Flags, w.InternalDate)
	}
	if strings.Contains(got["Work"][0], "X-Gmail-Labels") || !strings.Contains(got["Work"][0], "X-GM-THRID") {
		t.Fatalf("stored %q", got["Work"][0])
	}
}

func TestZipLimits(t *testing.T) {
	dir := t.TempDir()
	write := func(data []byte) string {
		p := filepath.Join(dir, "up")
		must(t, os.WriteFile(p, data, 0o600))
		return p
	}
	eml := "Subject: x\r\n\r\nx\r\n"
	inner := makeZip(t, zipEntry{name: "a.eml", data: eml})
	data := makeZip(t,
		zipEntry{name: "../evil.eml", data: eml},
		zipEntry{name: "/abs.eml", data: eml},
		zipEntry{name: `back\slash.eml`, data: eml},
		zipEntry{name: "inner.zip", data: string(inner), store: true},
		zipEntry{name: "renamed.eml", data: string(inner), store: true},
		zipEntry{name: "newsletter.eml", data: "Subject: x\r\n\r\n" + strings.Repeat("0", 1<<20)},
		zipEntry{name: "Entw\x81rfe/x.eml", data: eml, nonUTF8: true},
		zipEntry{name: "ok/a.eml", data: eml},
		zipEntry{name: "OK/b.eml", data: eml},
	)
	src, err := openSource(write(data), "x.zip", int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer src.close()
	var folders []string
	n := 0
	err = src.each(context.Background(), func(fm fileMessage) error {
		n++
		if r := fm.entry.remote(); !slices.Contains(folders, r) {
			folders = append(folders, r)
		}
		io.Copy(io.Discard, fm.body)
		return nil
	})
	must(t, err)
	// Skipped: traversal ×2, backslash, nested zip by name and by content.
	// A message that compresses extremely well is mail like any other.
	if src.skipped != 5 || n != 4 || !slices.Equal(folders, []string{"INBOX", "Entwürfe", "ok"}) {
		t.Fatalf("skipped %d, %d messages in %q", src.skipped, n, folders)
	}

	// Too many entries.
	old := maxZipEntries
	maxZipEntries = 3
	defer func() { maxZipEntries = old }()
	many := makeZip(t, zipEntry{name: "1.eml"}, zipEntry{name: "2.eml"}, zipEntry{name: "3.eml"}, zipEntry{name: "4.eml"})
	if _, err := openSource(write(many), "x.zip", int64(len(many))); status(err) != http.StatusBadRequest {
		t.Fatalf("entries: %v", err)
	}
	maxZipEntries = old
	// More unpacked than maxUnpacked.
	oldUnpacked := maxUnpacked
	maxUnpacked = 5000
	big := makeZip(t, zipEntry{name: "a.eml", data: "Subject: x\r\n\r\n" + randomText(6000)})
	if _, err := openSource(write(big), "x.zip", int64(len(big))); status(err) != http.StatusBadRequest {
		t.Fatalf("unpacked size: %v", err)
	}
	maxUnpacked = oldUnpacked
	// Takeout as .tgz is refused with a hint.
	if _, err := openSource(write([]byte{0x1f, 0x8b, 8, 0, 0}), "takeout.tgz", 5); status(err) != http.StatusBadRequest ||
		!strings.Contains(err.Error(), ".zip") {
		t.Fatalf("tgz: %v", err)
	}
}

func TestUploadProtocol(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.m.CreateUpload(ctx, idAlice, config.UploadRequest{Name: "a.mbox", Size: config.MaxImportBytes + 1}); status(err) != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: %v", err)
	}
	if _, err := e.m.CreateUpload(ctx, idAlice, config.UploadRequest{Name: "../a", Size: 10}); status(err) != http.StatusBadRequest {
		t.Fatalf("name: %v", err)
	}
	e.free.Store(1<<30 + 5)
	if _, err := e.m.CreateUpload(ctx, idAlice, config.UploadRequest{Name: "a.mbox", Size: 10}); status(err) != http.StatusInsufficientStorage {
		t.Fatalf("space: %v", err)
	}
	e.free.Store(100 << 30)
	data := []byte("From x@y Fri Jan  5 11:00:00 2024\nSubject: a\n\nx\n")
	j, err := e.m.CreateUpload(ctx, idAlice, config.UploadRequest{Name: "a.mbox", Size: int64(len(data))})
	must(t, err)
	if _, err := e.m.CreateUpload(ctx, idAlice, config.UploadRequest{Name: "b.mbox", Size: 10}); status(err) != http.StatusConflict {
		t.Fatalf("second upload: %v", err)
	}
	if _, err := e.m.WritePart(ctx, idAlice, j.ID, 5, bytes.NewReader(data), 3); status(err) != http.StatusConflict {
		t.Fatalf("offset: %v", err)
	}
	if _, err := e.m.WritePart(ctx, idAlice, j.ID, 0, bytes.NewReader(data), int64(len(data))+1); status(err) != http.StatusBadRequest {
		t.Fatalf("beyond: %v", err)
	}
	// A broken connection keeps what arrived.
	broken := io.MultiReader(bytes.NewReader(data[:10]), errReader{})
	got, err := e.m.WritePart(ctx, idAlice, j.ID, 0, broken, int64(len(data)))
	if err == nil || got != 10 {
		t.Fatalf("broken: %d %v", got, err)
	}
	got, err = e.m.WritePart(ctx, idAlice, j.ID, 10, bytes.NewReader(data[10:]), int64(len(data)-10))
	if err != nil || got != int64(len(data)) {
		t.Fatalf("rest: %d %v", got, err)
	}
	j = e.wait(idAlice, j.ID, config.ImportReady)
	if f, _ := checkFolder(j.Check, "a"); f.Messages != 1 {
		t.Fatalf("check %+v", j.Check)
	}
	if _, err := e.m.WritePart(ctx, idAlice, j.ID, j.Received, bytes.NewReader(data), 1); status(err) != http.StatusConflict {
		t.Fatalf("after the end: %v", err)
	}
	// Cancelling a ready job removes its spool.
	must(t, e.m.Cancel(ctx, idAlice, j.ID))
	if _, err := os.Stat(e.m.spoolPath(j.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("spool kept")
	}
}

// randomText is text that does not compress well.
func randomText(n int) string {
	var b strings.Builder
	x := uint32(1)
	for b.Len() < n {
		x = x*1664525 + 1013904223
		b.WriteByte(byte('a' + (x>>24)%26))
	}
	return b.String()
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestFileJobResumesAfterRestart(t *testing.T) {
	e := newEnv(t)
	var mbox strings.Builder
	for i := range 5 {
		mbox.WriteString(sprintf("From x@y Fri Jan  5 11:00:0%d 2024\nSubject: n%d\n\nx\n", i, i))
	}
	j := e.upload(idAlice, "box.mbox", []byte(mbox.String()))
	ctx := context.Background()
	// As if the service stopped while the job ran, two messages in.
	j.State, j.Choices, j.Position = config.ImportRunning, config.ImportChoices{}, 2
	j.Copied = 2
	must(t, e.store.SaveImport(ctx, &j))
	sctx, cancel := context.WithTimeout(ctx, time.Second)
	e.m.Shutdown(sctx)
	cancel()
	// Leftovers: a spool without a job and a stray file.
	must(t, os.WriteFile(filepath.Join(e.store.ImportsDir(), "999.spool"), []byte("x"), 0o600))
	must(t, os.WriteFile(filepath.Join(e.store.ImportsDir(), "stray"), []byte("x"), 0o600))
	must(t, os.WriteFile(filepath.Join(e.store.ImportsDir(), "Not-An-ID.spool"), []byte("x"), 0o600))
	e.m = e.manager()
	j = e.wait(idAlice, j.ID, config.ImportDone)
	if s := subjects(e.messages(idAlice)["box"]); !slices.Equal(s, []string{"n2", "n3", "n4"}) || j.Copied != 5 {
		t.Fatalf("after restart %v %+v", s, j)
	}
	entries, _ := os.ReadDir(e.store.ImportsDir())
	if len(entries) != 0 {
		t.Fatalf("spools left: %v", entries)
	}
}

func TestPrune(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	e.m.opts.Now = func() time.Time { return now }
	j := e.importFile(idAlice, "m.eml", []byte("Subject: x\n\nx\n"), config.ImportChoices{})
	now = now.Add(config.ImportRetention + time.Hour)
	must(t, e.m.Prune(context.Background()))
	if _, err := e.m.Job(context.Background(), idAlice, j.ID); status(err) != http.StatusNotFound {
		t.Fatalf("kept: %v", err)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st := e.store
	must(t, st.CreateFolder(ctx, idAlice, "Projects"))
	must(t, st.CreateFolder(ctx, idAlice, "Projects/2024"))
	must(t, st.CreateFolder(ctx, idAlice, "Empty"))
	must(t, st.SetSubscribed(ctx, idAlice, "Projects", true))
	must(t, st.SetSubscribed(ctx, idAlice, "Empty", true))
	must(t, st.SetSubscribed(ctx, idAlice, "Junk", false))
	d1 := time.Date(2022, 3, 4, 5, 6, 7, 0, time.UTC)
	put := func(folder, body string, flags ...string) {
		t.Helper()
		_, err := st.Append(ctx, idAlice, folder, strings.NewReader(body), storage.AppendOptions{Flags: flags, Date: d1})
		must(t, err)
		d1 = d1.Add(time.Hour)
	}
	put("INBOX", "Subject: a\r\n\r\nFrom the top\r\n>From quoted\r\n", storage.FlagSeen, "$Forwarded", "Work")
	put("INBOX", "Subject: b\r\n\r\nno newline at the end")
	put("INBOX", "Subject: gone\r\n\r\nx\r\n", storage.FlagDeleted)
	put("Sent", "Subject: s\r\n\r\nx\r\n", storage.FlagSeen, storage.FlagAnswered)
	put("Drafts", "Subject: d\r\n\r\nx\r\n", storage.FlagDraft, storage.FlagFlagged)
	put("Projects/2024", "Subject: p\r\n\r\nx\r\n")
	// Calendars and contacts go into the zip too.
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
	zr, err := zip.NewReader(bytes.NewReader(zipBuf.Bytes()), int64(zipBuf.Len()))
	must(t, err)
	var davFiles []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "calendars/") || strings.HasPrefix(f.Name, "contacts/") {
			davFiles = append(davFiles, f.Name)
			if f.Name == "calendars/calendar.ics" {
				rc, _ := f.Open()
				b, _ := io.ReadAll(rc)
				rc.Close()
				if !strings.Contains(string(b), "UID:ev1") {
					t.Fatalf("calendar export %q", b)
				}
			}
		}
	}
	if strings.Join(davFiles, ",") != "contacts/contacts.vcf,calendars/calendar.ics" {
		t.Fatalf("calendar and contact files %v", davFiles)
	}

	// The check tells it is our own export (the interface then includes
	// Junk and Trash); another zip is not.
	for name, data := range map[string][]byte{
		"alice-export.zip": zipBuf.Bytes(),
		"other.zip":        makeZip(t, zipEntry{name: "Inbox", data: "From - Fri Jan 05 11:00:00 2024\nSubject: x\n\nx\n"}),
	} {
		j := e.upload(idBob, name, data)
		if j.Check == nil || j.Check.OwnExport != (name == "alice-export.zip") {
			t.Fatalf("%s: own export %+v", name, j.Check)
		}
		must(t, e.m.Cancel(ctx, idBob, j.ID))
	}

	j := e.importFile(idBob, "alice-export.zip", zipBuf.Bytes(), config.ImportChoices{IncludeJunk: true, IncludeTrash: true})
	if j.State != config.ImportDone || j.Copied != 5 || j.Failed != 0 || j.Check.SkippedEntries != 0 {
		t.Fatalf("import %+v", j)
	}
	fa, _ := st.ListFolders(ctx, idAlice)
	fb, _ := st.ListFolders(ctx, idBob)
	strip := func(fs []storage.Folder) []string {
		var out []string
		for _, f := range fs {
			out = append(out, sprintf("%s|%s|%v", f.Name, f.SpecialUse, f.Subscribed))
		}
		return out
	}
	if !slices.Equal(strip(fa), strip(fb)) {
		t.Fatalf("folders\n%v\n%v", strip(fa), strip(fb))
	}
	for _, f := range fa {
		_, ma, _ := st.Select(ctx, idAlice, f.Name)
		_, mb, _ := st.Select(ctx, idBob, f.Name)
		ma = slices.DeleteFunc(ma, func(m storage.Message) bool { return m.HasFlag(storage.FlagDeleted) })
		if len(ma) != len(mb) {
			t.Fatalf("%s: %d vs %d messages", f.Name, len(ma), len(mb))
		}
		for i := range ma {
			if !slices.Equal(ma[i].Flags, mb[i].Flags) || !ma[i].InternalDate.Equal(mb[i].InternalDate) {
				t.Errorf("%s %d: %v %v vs %v %v", f.Name, i, ma[i].Flags, ma[i].InternalDate, mb[i].Flags, mb[i].InternalDate)
			}
			if a, b := e.body(ma[i]), e.body(mb[i]); a != b && a+"\r\n" != b {
				t.Errorf("%s %d: body %q vs %q", f.Name, i, a, b)
			}
		}
	}
	// The same file again copies nothing.
	j = e.importFile(idBob, "alice-export.zip", zipBuf.Bytes(), config.ImportChoices{})
	if j.Copied != 0 || j.Skipped != 5 {
		t.Fatalf("again %+v", j)
	}
}

func (e *env) body(m storage.Message) string {
	r, err := e.store.Open(m)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestExportLimits(t *testing.T) {
	e := newEnv(t)
	release, err := e.m.BeginExport(idAlice)
	must(t, err)
	if _, err := e.m.BeginExport(idAlice); status(err) != http.StatusConflict {
		t.Fatalf("second export: %v", err)
	}
	release()
	if _, err := e.m.BeginExport(mailtestUnknown); status(err) != http.StatusNotFound {
		t.Fatalf("unknown: %v", err)
	}
}
