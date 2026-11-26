package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"syscall"

	"github.com/danielgtmn/ownpanel/internal/backup"
)

// backupMailCommand is the internal command that writes the mail data as a
// tar stream to stdout (backup.WriteMail). The backup runs it as the owner of
// the mail data, so root never reads a directory the Internet-facing mail
// service controls.
const backupMailCommand = "backup-mail-data"

// backupDBCommand is the internal command that writes a copy of the panel's
// database to stdout (backup.WriteDB). The backup runs it as the owner of the
// data directory, so root never opens the database there and never leaves
// files of its own (-wal, -shm) behind that ownpaneld could not open.
const backupDBCommand = "backup-db"

func runBackupMail(args []string) error {
	fs := flag.NewFlagSet(backupMailCommand, flag.ExitOnError)
	dir := fs.String("mail-data", "", "data directory of the mail service")
	fs.Parse(args)
	if *dir == "" {
		return errors.New("needs -mail-data")
	}
	return backup.WriteMail(os.Stdout, *dir)
}

func runBackupDB(args []string) error {
	fs := flag.NewFlagSet(backupDBCommand, flag.ExitOnError)
	dir := fs.String("data", "", "data directory of the daemon")
	fs.Parse(args)
	if *dir == "" {
		return errors.New("needs -data")
	}
	return backup.WriteDB(os.Stdout, *dir)
}

// mailSource returns how the backup gets the mail data in dir: nil if there
// is none; as a child process running as the directory's owner when we run
// as root and someone else owns it; else directly.
func mailSource(dir string) (backup.MailSource, error) {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("-mail-data %s is not a directory", dir)
	}
	run, err := asOwner(fi, "reading the mail data as its owner", backupMailCommand, "-mail-data", dir)
	if err != nil || run != nil {
		return backup.MailSource(run), err
	}
	return func(w io.Writer) error { return backup.WriteMail(w, dir) }, nil
}

// dbSource returns how the backup copies the database in dataDir: as a child
// process running as the directory's owner when we run as root and someone
// else owns it; nil (copied here) otherwise.
func dbSource(dataDir string) (backup.DBSource, error) {
	fi, err := os.Lstat(dataDir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("-data %s is not a directory", dataDir)
	}
	run, err := asOwner(fi, "copying the database as the owner of the data directory", backupDBCommand, "-data", dataDir)
	return backup.DBSource(run), err
}

// asOwner returns a function that runs ownpaneld with args as the owner of
// fi, its standard output going to w; nil when we are not root or root owns
// fi, so the caller does the work itself.
func asOwner(fi fs.FileInfo, what string, args ...string) (func(w io.Writer) error, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() != 0 || st.Uid == 0 {
		return nil, nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return func(w io.Writer) error {
		cmd := exec.Command(self, args...)
		cmd.Dir = "/"
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "TMPDIR=" + os.TempDir()}
		cmd.Stdout, cmd.Stderr = w, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid, NoSetGroups: false, Groups: []uint32{}}}
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		return nil
	}, nil
}
