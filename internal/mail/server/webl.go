package server

import (
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/web"
)

// listenUnix opens a Unix socket at path with mode 0660, replacing a stale
// socket left behind by a crash but never a live one or another file.
func listenUnix(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("%s is in use: is the mail service already running?", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// listenWeb opens the web socket (Options.WebSocket). Its directory is
// handed to Options.WebGroup (ownpanel-mail-web, see ownpanel-mail.service),
// not to the group that may use the admin socket, and the socket gets the
// directory's group: the web server, the only member, can connect to it
// and to nothing else of the mail service.
func (s *Server) listenWeb() error {
	path := s.opts.WebSocket
	if path == "" {
		return nil
	}
	if s.opts.WebGroup != "" {
		g, err := user.LookupGroup(s.opts.WebGroup)
		if err != nil {
			return fmt.Errorf("web socket: %w", err)
		}
		gid, err := strconv.Atoi(g.Gid)
		if err != nil {
			return fmt.Errorf("web socket: group %s: %w", s.opts.WebGroup, err)
		}
		// The service is a member of the group (SupplementaryGroups=), so
		// as the directory's owner it may hand it over.
		if err := os.Chown(filepath.Dir(path), -1, gid); err != nil {
			return fmt.Errorf("web socket: %w", err)
		}
	}
	ln, err := listenUnix(path)
	if err != nil {
		return fmt.Errorf("web socket: %w", err)
	}
	if err := matchDirGroup(path); err != nil {
		ln.Close()
		return fmt.Errorf("web socket: %w", err)
	}
	s.webLn = ln
	s.web = web.NewServer(web.Options{Account: s.opts.AccountHandler, DAV: s.opts.DAVHandler})
	return nil
}

// matchDirGroup gives the file at path the group of its directory (the
// process must be a member of that group).
func matchDirGroup(path string) error {
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	file, err := os.Lstat(path)
	if err != nil {
		return err
	}
	dst, ok1 := dir.Sys().(*syscall.Stat_t)
	fst, ok2 := file.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 || dst.Gid == fst.Gid {
		return nil
	}
	return os.Lchown(path, -1, int(dst.Gid))
}
