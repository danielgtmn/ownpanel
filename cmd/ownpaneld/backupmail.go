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

func runBackupMail(args []string) error {
	fs := flag.NewFlagSet(backupMailCommand, flag.ExitOnError)
	dir := fs.String("mail-data", "", "data directory of the mail service")
	fs.Parse(args)
	if *dir == "" {
		return errors.New("needs -mail-data")
	}
	return backup.WriteMail(os.Stdout, *dir)
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
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() != 0 || st.Uid == 0 {
		return func(w io.Writer) error { return backup.WriteMail(w, dir) }, nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return func(w io.Writer) error {
		cmd := exec.Command(self, backupMailCommand, "-mail-data", dir)
		cmd.Dir = "/"
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "TMPDIR=" + os.TempDir()}
		cmd.Stdout, cmd.Stderr = w, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid, NoSetGroups: false, Groups: []uint32{}}}
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("reading the mail data as its owner: %w", err)
		}
		return nil
	}, nil
}
