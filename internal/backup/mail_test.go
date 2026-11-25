package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	// The service stays open while the backup runs.
	defer ms.Close()

	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := CreateWithMail(data, mailDir, out); err != nil {
		t.Fatal(err)
	}

	restore := t.TempDir()
	names := unpack(t, out, restore)
	for _, want := range []string{"mail/", "mail/mail.db", "mail/restored"} {
		if !names[want] {
			t.Errorf("archive lacks %s; has %v", want, names)
		}
	}
	for n := range names {
		if strings.HasPrefix(n, "mail/tmp") || strings.HasPrefix(n, "mail/trash") || strings.HasPrefix(n, "mail/mail.db-") {
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
}

func TestCreateWithoutMail(t *testing.T) {
	data := t.TempDir()
	st, err := store.Open(filepath.Join(data, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	// A mail directory that does not exist is skipped.
	if err := CreateWithMail(data, filepath.Join(t.TempDir(), "missing"), out); err != nil {
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
