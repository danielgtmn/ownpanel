// Package backup writes a restorable archive of a data directory: a
// consistent copy of the database plus site files, certificates and the key
// that decrypts the secrets in the database. The archive therefore contains
// secrets and must be kept as safe as the server itself.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/danielgtmn/ownpanel/internal/secrets"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// DBFile is the database's name inside the data directory.
const DBFile = "ownpanel.db"

// dirs are copied as they are (site releases, Caddy's certificates, the
// mail host's certificate and ACME account).
var dirs = []string{"sites", "caddy", "mail-tls"}

// MailDir is the folder of the archive that holds the mail service's data.
const MailDir = "mail"

// mailDB is the mail service's index inside its data directory.
const mailDB = "mail.db"

// RestoredMarker is the file the mail service looks for at start to learn
// that its data was restored (see internal/mail/storage).
const RestoredMarker = "restored"

// files are copied as they are if they exist: the key for the secrets in
// the database (without it, DNS connections cannot be used after a restore).
var files = []string{secrets.KeyFile}

// Create writes a tar.gz of dataDir to out (mode 0600, must not exist). It
// is safe while the daemon runs: the database is copied with VACUUM INTO.
func Create(dataDir, out string) error { return CreateWithMail(dataDir, out, nil) }

// CreateWithMail is Create plus the mail service's data: mail writes it as
// a tar stream (WriteMail), which is checked and stored under MailDir. nil
// means no mail.
func CreateWithMail(dataDir, out string, mail MailSource) (err error) {
	tmp, err := os.MkdirTemp("", "ownpanel-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dbCopy := filepath.Join(tmp, DBFile)
	if err := store.VacuumInto(filepath.Join(dataDir, DBFile), dbCopy); err != nil {
		return fmt.Errorf("copy database: %w", err)
	}

	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(out)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := addFile(tw, dbCopy, DBFile); err != nil {
		return err
	}
	for _, name := range files {
		err := addFile(tw, filepath.Join(dataDir, name), name)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	for _, dir := range dirs {
		if err := addTree(tw, dataDir, dir); err != nil {
			return err
		}
	}
	if mail != nil {
		if err := embedMail(tw, mail); err != nil {
			return fmt.Errorf("mail: %w", err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func addFile(tw *tar.Writer, path, name string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return addEntry(tw, path, name, fi)
}

// addTree adds dataDir/dir recursively; a missing dir is skipped.
func addTree(tw *tar.Writer, dataDir, dir string) error {
	root := filepath.Join(dataDir, dir)
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return addEntry(tw, path, filepath.ToSlash(rel), fi)
	})
}

// addEntry writes directories, regular files and symlinks (the "current"
// release link); sockets and the like are skipped.
func addEntry(tw *tar.Writer, path, name string, fi fs.FileInfo) error {
	var link string
	switch {
	case fi.Mode().IsRegular(), fi.IsDir():
	case fi.Mode()&fs.ModeSymlink != 0:
		var err error
		if link, err = os.Readlink(path); err != nil {
			return err
		}
	default:
		return nil
	}
	hdr, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return err
	}
	hdr.Name = name
	if fi.IsDir() {
		hdr.Name += "/"
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.Copy(tw, src)
	return err
}
