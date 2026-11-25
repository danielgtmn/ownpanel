//go:build unix

package backup

import (
	"os"
	"syscall"
)

// giveBack gives the -wal and -shm files of an SQLite database the owner of
// the database file itself.
func giveBack(db string) error {
	fi, err := os.Stat(db)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		other, err := os.Stat(db + suffix)
		if err != nil {
			continue
		}
		if ost, ok := other.Sys().(*syscall.Stat_t); ok && (ost.Uid != st.Uid || ost.Gid != st.Gid) {
			if err := os.Chown(db+suffix, int(st.Uid), int(st.Gid)); err != nil {
				return err
			}
		}
	}
	return nil
}
