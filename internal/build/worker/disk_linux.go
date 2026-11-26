//go:build linux

package worker

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// QuotaDisk is the build disk as set up by deploy/ownpanel-build-setup: an
// ext4 file system of its own, mounted with nosuid, nodev and project
// quotas. The worker may give its own folders a project (the kernel lets
// the owner do that from outside a container); only root can set the
// quota of a project, and nothing inside a container can change the
// project of a file.
type QuotaDisk struct{}

const ext4Magic = 0xEF53

// fsxattr is struct fsxattr of <linux/fs.h>.
type fsxattr struct {
	xflags     uint32
	extsize    uint32
	nextents   uint32
	projid     uint32
	cowextsize uint32
	pad        [8]byte
}

const (
	fsIOCGetXattr      = 0x801c581f // _IOR('X', 31, struct fsxattr)
	fsIOCSetXattr      = 0x401c5820 // _IOW('X', 32, struct fsxattr)
	fsXflagProjInherit = 0x00000200
)

func xattrIoctl(dir string, req uintptr, x *fsxattr) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(unsafe.Pointer(x))); errno != 0 {
		return &os.PathError{Op: "ioctl", Path: dir, Err: errno}
	}
	return nil
}

// Check implements Disk.
func (QuotaDisk) Check(dir string) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return err
	}
	if st.Type != ext4Magic {
		return fmt.Errorf("%s is not the build disk (no ext4 file system is mounted there)", dir)
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		// id parent major:minor root mountpoint options [optional...] - fstype source superoptions
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || unescapeMount(fields[4]) != dir {
			continue
		}
		sep := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+3 >= len(fields) {
			continue
		}
		mountOpts := strings.Split(fields[5], ",")
		superOpts := strings.Split(fields[sep+3], ",")
		for _, want := range []string{"nosuid", "nodev"} {
			if !contains(mountOpts, want) {
				return fmt.Errorf("the build disk is not mounted with %s", want)
			}
		}
		if !contains(superOpts, "prjquota") {
			return errors.New("the build disk is not mounted with project quotas (prjquota)")
		}
		return nil
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%s is not a mount point: the build disk is not mounted", dir)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// unescapeMount undoes the octal escapes of /proc/self/mountinfo.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			var v int
			if _, err := fmt.Sscanf(s[i+1:i+4], "%03o", &v); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// SetProject implements Disk.
func (QuotaDisk) SetProject(dir string, id uint32) error {
	var x fsxattr
	if err := xattrIoctl(dir, fsIOCGetXattr, &x); err != nil {
		return err
	}
	x.projid = id
	x.xflags |= fsXflagProjInherit
	return xattrIoctl(dir, fsIOCSetXattr, &x)
}

// Project implements Disk.
func (QuotaDisk) Project(dir string) (uint32, error) {
	var x fsxattr
	if err := xattrIoctl(dir, fsIOCGetXattr, &x); err != nil {
		return 0, err
	}
	if x.xflags&fsXflagProjInherit == 0 {
		return 0, nil
	}
	return x.projid, nil
}

// Usage implements Disk: for a folder with a project, ext4 reports the
// project's quota and use as the size of the file system.
func (QuotaDisk) Usage(dir string) (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return Usage{}, err
	}
	bs := int64(st.Bsize)
	return Usage{
		TotalBytes: int64(st.Blocks) * bs,
		FreeBytes:  int64(st.Bavail) * bs,
		Files:      int64(st.Files),
		FreeFiles:  int64(st.Ffree),
	}, nil
}

// FilterGuard checks the packet filter that keeps builds away from this
// server (deploy/ownpanel-build-setup). It cannot read the filter (that
// needs root), so it tries it: the filter applies to this user too, and
// answers a datagram to loopback with "administratively prohibited",
// which reaches the socket as EHOSTUNREACH. Without the filter the kernel
// answers "port unreachable" (ECONNREFUSED), or nothing.
type FilterGuard struct{}

// Protected implements Guard.
func (FilterGuard) Protected() error {
	c, err := net.Dial("udp4", "127.0.0.1:9")
	if err != nil {
		return fmt.Errorf("testing the packet filter: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, err := c.Write([]byte{0}); err != nil && errors.Is(err, syscall.EHOSTUNREACH) {
		return nil
	}
	_, err = c.Read(make([]byte, 1))
	if errors.Is(err, syscall.EHOSTUNREACH) {
		return nil
	}
	return errors.New("the packet filter for builds is not active: a test packet to this server was not refused (is ownpanel-build-setup running, and was the firewall flushed?)")
}
