package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
	"github.com/danielgtmn/ownpanel/internal/store"
)

const testStorageID = "0123456789abcdef0123456789abcdef"

// The archive holds the mail index, the message files and the restore
// marker, and an unpacked archive gives the mail service its messages back.
func TestCreateWithMail(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	st, err := store.Open(filepath.Join(data, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mailDir := t.TempDir()
	ms, err := storage.Open(mailDir, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	msg := "Subject: hello\r\n\r\nbody\r\n"
	if _, err := ms.Append(ctx, testStorageID, storage.Inbox, strings.NewReader(msg), storage.AppendOptions{}); err != nil {
		t.Fatal(err)
	}
	// Calendars and contacts live in the same directory and come along.
	if err := ms.EnsureDefaultDAVCollections(ctx, testStorageID); err != nil {
		t.Fatal(err)
	}
	cal, err := ms.DAVCollection(ctx, testStorageID, davobj.Calendar, storage.DefaultCalendar)
	if err != nil {
		t.Fatal(err)
	}
	ev := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:ev1\r\nDTSTAMP:20240101T000000Z\r\nDTSTART:20240101T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	info, _, err := davobj.ParseCalendar(ev, nil)
	if err != nil {
		t.Fatal(err)
	}
	obj, _, err := ms.PutDAVObject(ctx, cal, "ev1.ics", ev, info, storage.PutCondition{})
	if err != nil {
		t.Fatal(err)
	}
	// The service stays open while the backup runs.
	defer ms.Close()

	out := filepath.Join(t.TempDir(), "b.tar.gz")
	// A link the mail service could plant must not be followed.
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("root only"), 0o600)
	os.Symlink(secret, filepath.Join(mailDir, "messages", "planted"))
	// The copy of an index before its conversion to ablage stays out.
	os.WriteFile(filepath.Join(mailDir, "mail.db.pre-ablage-20260101T000000Z"), []byte("old"), 0o600)
	if err := CreateWithMail(data, out, func(w io.Writer) error { return WriteMail(w, mailDir) }); err != nil {
		t.Fatal(err)
	}

	restore := t.TempDir()
	names := unpack(t, out, restore)
	for _, want := range []string{"mail/", "mail/mail.db", "mail/restored"} {
		if !names[want] {
			t.Errorf("archive lacks %s; has %v", want, names)
		}
	}
	if names["mail/messages/planted"] {
		t.Error("a link in the mail data was copied")
	}
	for n := range names {
		if strings.HasPrefix(n, "mail/tmp") || strings.HasPrefix(n, "mail/trash") || strings.HasPrefix(n, "mail/mail.db-") || strings.HasPrefix(n, "mail/mail.db.pre-ablage-") {
			t.Errorf("archive should not contain %s", n)
		}
	}

	rs, err := storage.Open(filepath.Join(restore, MailDir), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	if _, err := rs.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	_, msgs, err := rs.Select(ctx, testStorageID, storage.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("restored INBOX has %d messages, want 1", len(msgs))
	}
	f, err := rs.Open(msgs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	if !strings.Contains(string(b), "body") {
		t.Fatalf("restored message is %q", b)
	}
	if _, err := os.Stat(filepath.Join(restore, MailDir, RestoredMarker)); err == nil {
		t.Error("the restore marker should be consumed by the start-up check")
	}
	rcal, err := rs.DAVCollection(ctx, testStorageID, davobj.Calendar, storage.DefaultCalendar)
	if err != nil {
		t.Fatal(err)
	}
	robj, err := rs.DAVObject(ctx, rcal, "ev1.ics")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := rs.ReadDAVObject(robj); err != nil || string(b) != string(ev) {
		t.Fatalf("restored event %q %v", b, err)
	}
	// A new epoch: programs sync everything again.
	if robj.ETag() == obj.ETag() {
		t.Error("ETag kept after a restore")
	}
}

func TestCreateWithoutMail(t *testing.T) {
	data := t.TempDir()
	st, err := store.Open(filepath.Join(data, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := CreateWithMail(data, out, nil); err != nil {
		t.Fatal(err)
	}
	for n := range unpack(t, out, t.TempDir()) {
		if strings.HasPrefix(n, "mail/") {
			t.Errorf("unexpected %s", n)
		}
	}
}

func unpack(t *testing.T, archive, dir string) map[string]bool {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names[hdr.Name] = true
		path := filepath.Join(dir, filepath.FromSlash(hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(path, 0o700)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(path), 0o700)
			w, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(w, tr); err != nil {
				t.Fatal(err)
			}
			w.Close()
		}
	}
}

// A mail stream may only put plain files and folders below mail/.
func TestEmbedMailRefusesForeignEntries(t *testing.T) {
	for name, hdr := range map[string]*tar.Header{
		"outside":   {Name: "sites/x", Typeflag: tar.TypeReg},
		"climbing":  {Name: "mail/../secret.key", Typeflag: tar.TypeReg},
		"absolute":  {Name: "/etc/passwd", Typeflag: tar.TypeReg},
		"link":      {Name: "mail/x", Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"},
		"hard link": {Name: "mail/x", Typeflag: tar.TypeLink, Linkname: "secret.key"},
		"device":    {Name: "mail/x", Typeflag: tar.TypeChar},
	} {
		t.Run(name, func(t *testing.T) {
			var outer bytes.Buffer
			tw := tar.NewWriter(&outer)
			err := embedMail(tw, func(w io.Writer) error {
				mw := tar.NewWriter(w)
				if err := mw.WriteHeader(hdr); err != nil {
					return err
				}
				return mw.Close()
			})
			if err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// Names are cleaned and modes reduced to permissions (no setuid).
	var outer bytes.Buffer
	tw := tar.NewWriter(&outer)
	err := embedMail(tw, func(w io.Writer) error {
		mw := tar.NewWriter(w)
		mw.WriteHeader(&tar.Header{Name: "mail/./a", Typeflag: tar.TypeReg, Mode: 0o4755, Size: 1})
		mw.Write([]byte("x"))
		return mw.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	tw.Close()
	hdr, err := tar.NewReader(&outer).Next()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Name != "mail/a" || hdr.Mode != 0o755 {
		t.Fatalf("got %q mode %o", hdr.Name, hdr.Mode)
	}
}
