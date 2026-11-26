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

	"github.com/danielgtmn/ownpanel/internal/store"
	"github.com/danielgtmn/panzerschrank"
)

// DBFile is the database's name inside the data directory.
const DBFile = "ownpanel.db"

// dirs are copied as they are (site releases, Caddy's certificates, the
// mail host's certificate and ACME account). CaddyDir may come from
// elsewhere (Sources.CaddyStorage); it is stored under this name.
var dirs = []string{"sites", CaddyDir, "mail-tls"}

// CaddyDir is the folder of the archive that holds Caddy's certificate
// storage.
const CaddyDir = "caddy"

// MailDir is the folder of the archive that holds the mail service's data.
const MailDir = "mail"

// mailDB is the mail service's index inside its data directory.
const mailDB = "mail.db"

// RestoredMarker is the file the mail service looks for at start to learn
// that its data was restored (see internal/mail/storage).
const RestoredMarker = "restored"

// files are copied as they are if they exist: the key for the secrets in
// the database (without it, DNS connections cannot be used after a restore).
var files = []string{panzerschrank.KeyFile}

// DBSource writes a consistent copy of the database (see WriteDB) to w.
type DBSource func(w io.Writer) error

// sqliteHeader starts every SQLite database file.
const sqliteHeader = "SQLite format 3\x00"

// Create writes a tar.gz of dataDir to out (mode 0600, must not exist). It
// is safe while the daemon runs: the database is copied with VACUUM INTO.
func Create(dataDir, out string) error { return CreateWithMail(dataDir, out, nil) }

// CreateWithMail is Create plus the mail service's data: mail writes it as
// a tar stream (WriteMail), which is checked and stored under MailDir. nil
// means no mail.
func CreateWithMail(dataDir, out string, mail MailSource) error {
	return CreateFrom(dataDir, out, Sources{Mail: mail})
}

// Sources says where the parts of a backup come from that are not simply
// read from the data directory.
type Sources struct {
	// DB writes the database copy (see WriteDB); nil copies it here. The data
	// directory belongs to the panel's user: run as root, ownpaneld lets a
	// child running as that user copy the database, so root never opens it
	// and never leaves files of its own there.
	DB DBSource
	// Mail writes the mail service's data (see WriteMail); nil means none.
	Mail MailSource
	// CaddyStorage is Caddy's certificate storage; "" means CaddyDir in the
	// data directory.
	CaddyStorage string
}

// CreateFrom writes a backup of dataDir to out like Create, with the parts
// given in src.
func CreateFrom(dataDir, out string, src Sources) (err error) {
	tmp, err := os.MkdirTemp("", "ownpanel-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dbCopy := filepath.Join(tmp, DBFile)
	db := src.DB
	if db == nil {
		db = func(w io.Writer) error { return WriteDB(w, dataDir) }
	}
	if err := receiveDB(db, dbCopy); err != nil {
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
		root := filepath.Join(dataDir, dir)
		if dir == CaddyDir && src.CaddyStorage != "" {
			root = src.CaddyStorage
		}
		if err := addTree(tw, root, dir); err != nil {
			return err
		}
	}
	if src.Mail != nil {
		if err := embedMail(tw, src.Mail); err != nil {
			return fmt.Errorf("mail: %w", err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// WriteDB writes a consistent copy of dataDir's database to w (VACUUM INTO
// a temporary file, then the file). Call it as the owner of dataDir.
func WriteDB(w io.Writer, dataDir string) error {
	tmp, err := os.MkdirTemp("", "ownpanel-db-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dbCopy := filepath.Join(tmp, DBFile)
	if err := store.VacuumInto(filepath.Join(dataDir, DBFile), dbCopy); err != nil {
		return err
	}
	f, err := os.Open(dbCopy)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// receiveDB stores what db writes in file (0600) and checks that it is an
// SQLite database.
func receiveDB(db DBSource, file string) (err error) {
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	if err := db(f); err != nil {
		return err
	}
	head := make([]byte, len(sqliteHeader))
	if _, err := f.ReadAt(head, 0); err != nil || string(head) != sqliteHeader {
		return errors.New("the copy is not an SQLite database")
	}
	return nil
}

func addFile(tw *tar.Writer, path, name string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return addEntry(tw, path, name, fi)
}

// addTree adds the directory root recursively under the name dir; a missing
// root is skipped.
func addTree(tw *tar.Writer, root, dir string) error {
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.Join(dir, rel)
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
