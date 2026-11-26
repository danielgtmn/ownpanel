package backup

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// MailSource writes the mail service's data as an uncompressed tar stream
// with every entry under MailDir (see WriteMail).
type MailSource func(w io.Writer) error

// The mail data belongs to the mail service's user, and the mail service
// faces the Internet: if it were taken over, everything in its directory
// would be under an attacker's control. The backup runs as root, so root
// never reads that directory itself. ownpaneld runs WriteMail as the
// directory's owner (a child process, see cmd/ownpaneld) and only takes the
// tar stream, keeping nothing but plain files and folders below MailDir.
//
// Limits on what the stream may contain, so a hostile stream cannot fill the
// disk with names or headers alone.
const (
	maxMailEntries = 10_000_000
	maxMailName    = 512
)

// WriteMail writes the mail data in dir to w as a tar stream: the index
// first (a consistent copy), then the restore marker, then every other file
// except the live index and the scratch folders. Every message the copied
// index lists therefore has its file in the archive; files of messages that
// arrive meanwhile are indexed again on restore. Links and other special
// files are skipped. Call it as the owner of dir.
func WriteMail(w io.Writer, dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	tmp, err := os.MkdirTemp("", "ownpanel-mail-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	tw := tar.NewWriter(w)
	if err := writeDir(tw, MailDir, fi); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(dir, mailDB)); err == nil {
		indexCopy := filepath.Join(tmp, mailDB)
		if err := store.VacuumInto(filepath.Join(dir, mailDB), indexCopy); err != nil {
			return fmt.Errorf("copy mail index: %w", err)
		}
		if err := writeFile(tw, indexCopy, MailDir+"/"+mailDB); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Unpacked, the marker makes the mail service give every folder a new
	// UIDVALIDITY, so mail programs drop what they cached from newer mail.
	if err := tw.WriteHeader(&tar.Header{Name: MailDir + "/" + RestoredMarker, Mode: 0o600, Typeflag: tar.TypeReg, ModTime: time.Now()}); err != nil {
		return err
	}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A message expunged while we walk is simply not in the backup.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case rel == mailDB || strings.HasPrefix(rel, mailDB+"-") || rel == RestoredMarker:
			return nil
		case strings.HasPrefix(rel, mailDB+".pre-ablage-"):
			// The copy the service kept of an index before its conversion
			// (internal/mail/storage): the archive has the index itself.
			return nil
		case rel == "tmp" || rel == "trash":
			return fs.SkipDir
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			return writeDir(tw, MailDir+"/"+rel, info)
		case info.Mode().IsRegular():
			err := writeFile(tw, p, MailDir+"/"+rel)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		return nil // links, sockets and the like
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

func writeDir(tw *tar.Writer, name string, fi fs.FileInfo) error {
	return tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: int64(fi.Mode().Perm()), ModTime: fi.ModTime()})
}

// writeFile copies one regular file. It is opened without following a link
// and checked again after opening, so a file swapped for something else
// meanwhile is not read.
func writeFile(tw *tar.Writer, p, name string) error {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil
		}
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: int64(fi.Mode().Perm()), Size: fi.Size(), ModTime: fi.ModTime()}); err != nil {
		return err
	}
	// A file that grows while it is copied: the header promised fi.Size().
	_, err = io.Copy(tw, io.LimitReader(f, fi.Size()))
	return err
}

// embedMail runs mail and copies its stream into tw, keeping only plain
// files and folders whose names lie below MailDir. Owners are not kept:
// the restore sets them.
func embedMail(tw *tar.Writer, mail MailSource) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := mail(pw)
		pw.CloseWithError(err)
		done <- err
	}()
	err := copyMail(tw, pr)
	if err != nil {
		pr.CloseWithError(err)
	}
	if merr := <-done; err == nil && merr != nil {
		err = merr
	}
	return err
}

func copyMail(tw *tar.Writer, r io.Reader) error {
	tr := tar.NewReader(r)
	for n := 0; ; n++ {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if n >= maxMailEntries {
			return errors.New("too many entries")
		}
		name, ok := mailName(hdr.Name)
		if !ok {
			return fmt.Errorf("unexpected entry %q", hdr.Name)
		}
		out := &tar.Header{Name: name, Mode: hdr.Mode & 0o777, ModTime: hdr.ModTime}
		switch hdr.Typeflag {
		case tar.TypeDir:
			out.Typeflag = tar.TypeDir
			out.Name += "/"
			if err := tw.WriteHeader(out); err != nil {
				return err
			}
		case tar.TypeReg:
			out.Typeflag, out.Size = tar.TypeReg, hdr.Size
			if err := tw.WriteHeader(out); err != nil {
				return err
			}
			if _, err := io.Copy(tw, tr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected entry type of %q", hdr.Name)
		}
	}
}

// mailName returns the clean form of an entry name if it is MailDir or lies
// below it.
func mailName(name string) (string, bool) {
	if len(name) > maxMailName || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") {
		return "", false
	}
	clean := path.Clean(strings.TrimSuffix(name, "/"))
	if clean != MailDir && !strings.HasPrefix(clean, MailDir+"/") {
		return "", false
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." || part == "." || part == "" {
			return "", false
		}
	}
	return clean, true
}
