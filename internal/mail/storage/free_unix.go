//go:build unix

package storage

import "syscall"

// FreeBytes is the space available to the service on the data directory's
// file system.
func (s *Store) FreeBytes() (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
