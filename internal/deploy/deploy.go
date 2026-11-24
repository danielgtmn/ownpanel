// Package deploy manages the files of a site as immutable releases.
//
// Layout per site:
//
//	<base>/<site>/releases/<release-id>/   extracted archive, never modified
//	<base>/<site>/current                  symlink to the active release
//
// The web server serves "current". Switching releases is one atomic rename of
// a symlink, so a deployment or rollback never exposes a half-written site
// and needs no web server reload.
package deploy

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// KeepReleases is how many releases are retained per site.
const KeepReleases = 5

// Limits for one release. Variables so tests can lower them.
var (
	// MaxUnpackedBytes caps the extracted size of one release.
	MaxUnpackedBytes int64 = 1 << 30
	// MaxFiles caps the number of entries in one release.
	MaxFiles = 100_000
)

var ErrNoRelease = errors.New("no such release")

// ErrBadArchive marks problems with the uploaded archive itself (unreadable,
// unsafe entries, too large, empty), as opposed to failures on our side.
var ErrBadArchive = errors.New("bad archive")

// archiveError keeps the human-readable message unchanged while still
// matching ErrBadArchive.
type archiveError struct{ err error }

func (e *archiveError) Error() string        { return e.err.Error() }
func (e *archiveError) Unwrap() error        { return e.err }
func (e *archiveError) Is(target error) bool { return target == ErrBadArchive }

func badArchive(format string, args ...any) error {
	return &archiveError{fmt.Errorf(format, args...)}
}

// archiveReader marks read errors of the upload as the user's problem (a
// truncated or corrupt stream, an oversized request body), so they are not
// confused with disk errors on the write side.
type archiveReader struct{ r io.Reader }

func (a archiveReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if err != nil && err != io.EOF {
		err = &archiveError{err}
	}
	return n, err
}

// Manager owns the site directories below Base.
type Manager struct {
	Base string

	idMu      sync.Mutex
	lastStamp map[string]string // per site: timestamp of the last ID handed out
}

func (m *Manager) siteDir(site string) string     { return filepath.Join(m.Base, site) }
func (m *Manager) releasesDir(site string) string { return filepath.Join(m.Base, site, "releases") }

// Root is the path the web server serves for a site.
func (m *Manager) Root(site string) string { return filepath.Join(m.Base, site, "current") }

// ReleaseDir is the directory of one release, whether it is active or not.
func (m *Manager) ReleaseDir(site, id string) string { return filepath.Join(m.releasesDir(site), id) }

// Current returns the active release ID, or "" if nothing is deployed.
func (m *Manager) Current(site string) string {
	target, err := os.Readlink(m.Root(site))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// Releases lists release IDs, oldest first.
func (m *Manager) Releases(site string) []string {
	entries, err := os.ReadDir(m.releasesDir(site))
	if err != nil {
		return []string{}
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Deploy extracts a .zip or .tar.gz archive as a new release and activates it.
// The archive root becomes the web root; a single top-level folder is unwrapped.
func (m *Manager) Deploy(site string, archive io.Reader) (string, error) {
	relDir := m.releasesDir(site)
	if err := os.MkdirAll(relDir, 0o755); err != nil {
		return "", err
	}
	// zip needs random access, so spool the upload to disk first. It lives in
	// the releases directory (same filesystem; Releases() ignores it).
	spool, err := os.CreateTemp(relDir, ".tmp-upload-")
	if err != nil {
		return "", err
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	if _, err := io.Copy(spool, archiveReader{archive}); err != nil {
		return "", fmt.Errorf("could not read the upload: %w", err)
	}

	id, err := m.nextReleaseID(site)
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(relDir, ".tmp-"+id)
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp) // no-op once renamed

	if err := extract(tmp, spool); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(relDir, id)); err != nil {
		return "", err
	}
	if err := m.activate(site, id); err != nil {
		return "", err
	}
	m.prune(site)
	return id, nil
}

// Rollback activates the given release, or the one before the current release
// when id is empty.
func (m *Manager) Rollback(site, id string) (string, error) {
	releases := m.Releases(site)
	if id == "" {
		cur := m.Current(site)
		for i, r := range releases {
			if r == cur && i > 0 {
				id = releases[i-1]
			}
		}
		if id == "" {
			return "", fmt.Errorf("%w: there is no earlier release to go back to", ErrNoRelease)
		}
	} else {
		found := false
		for _, r := range releases {
			found = found || r == id
		}
		if !found {
			return "", fmt.Errorf("%w: %q", ErrNoRelease, id)
		}
	}
	return id, m.activate(site, id)
}

// Remove deletes all files of a site.
func (m *Manager) Remove(site string) error {
	return os.RemoveAll(m.siteDir(site))
}

func (m *Manager) activate(site, id string) error {
	link := m.Root(site)
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("releases", id), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

// prune removes the oldest releases beyond KeepReleases, never the active one.
func (m *Manager) prune(site string) {
	releases := m.Releases(site)
	cur := m.Current(site)
	for i := 0; i < len(releases)-KeepReleases; i++ {
		if releases[i] != cur {
			_ = os.RemoveAll(filepath.Join(m.releasesDir(site), releases[i]))
		}
	}
}

// stampLen is the length of the timestamp at the start of a release ID.
const stampLen = len("20060102T150405.000Z")

// nextReleaseID returns an ID that sorts after every release of the site.
// "The version before" is defined by that order, so two releases must never
// share a timestamp: the random suffix would then decide which is newer.
func (m *Manager) nextReleaseID(site string) (string, error) {
	m.idMu.Lock()
	defer m.idMu.Unlock()
	last := m.lastStamp[site]
	if rel := m.Releases(site); len(rel) > 0 && len(rel[len(rel)-1]) >= stampLen && rel[len(rel)-1][:stampLen] > last {
		last = rel[len(rel)-1][:stampLen]
	}
	for {
		id, err := newReleaseID()
		if err != nil {
			return "", err
		}
		if id[:stampLen] > last {
			if m.lastStamp == nil {
				m.lastStamp = map[string]string{}
			}
			m.lastStamp[site] = id[:stampLen]
			return id, nil
		}
		time.Sleep(time.Millisecond)
	}
}

func newReleaseID() (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405.000Z") + "-" + hex.EncodeToString(b), nil
}

// Archives are untrusted input. Both formats go through the same unpacker:
// only regular files and directories are accepted, every entry name is
// checked, the limits are enforced on the bytes actually read (a header's
// declared size proves nothing), and all writes go through os.Root so no
// entry can escape dir.
type unpacker struct {
	root  *os.Root
	total int64
	files int
}

// entryName validates an archive entry name and returns its cleaned form.
// skip is true for entries that are silently ignored (macOS packaging junk,
// the archive's own root).
func (u *unpacker) entryName(raw string) (name string, skip bool, err error) {
	if strings.ContainsRune(raw, '\\') {
		return "", false, badArchive("archive entry %q has a backslash in its name; use forward slashes", raw)
	}
	name = path.Clean(raw)
	if name == "." {
		return "", true, nil
	}
	if !filepath.IsLocal(name) {
		return "", false, badArchive("archive entry %q points outside the site", raw)
	}
	if isJunk(name) {
		return "", true, nil
	}
	if u.files++; u.files > MaxFiles {
		return "", false, badArchive("archive has more than %d entries", MaxFiles)
	}
	return name, false, nil
}

// isJunk reports macOS packaging leftovers that are never part of a site.
func isJunk(name string) bool {
	segments := strings.Split(name, "/")
	for _, s := range segments {
		if s == "__MACOSX" {
			return true
		}
	}
	base := segments[len(segments)-1]
	return base == ".DS_Store" || strings.HasPrefix(base, "._")
}

func (u *unpacker) mkdir(name string) error {
	return u.root.MkdirAll(name, 0o755)
}

func (u *unpacker) writeFile(name string, content io.Reader) error {
	if d := path.Dir(name); d != "." {
		if err := u.root.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	f, err := u.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(archiveReader{content}, MaxUnpackedBytes-u.total+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("unpack %q: %w", name, err)
	}
	if u.total += n; u.total > MaxUnpackedBytes {
		return badArchive("archive is larger than %d MB when unpacked", MaxUnpackedBytes>>20)
	}
	return nil
}

func errLink(name string) error {
	return badArchive("archive entry %q is not a regular file or directory (links are not supported)", name)
}

// extract unpacks the spooled upload into dir. The format is detected from
// the content, never from a file name.
func extract(dir string, upload *os.File) error {
	var magic [4]byte
	n, _ := upload.ReadAt(magic[:], 0)
	head := magic[:n]
	if _, err := upload.Seek(0, io.SeekStart); err != nil {
		return err
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	u := &unpacker{root: root}

	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		err = u.untar(upload)
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		err = u.unzip(upload)
	default:
		return badArchive("the file is neither a .zip nor a .tar.gz archive")
	}
	if err != nil {
		return err
	}
	return finishRelease(dir)
}

func (u *unpacker) untar(upload io.Reader) error {
	gz, err := gzip.NewReader(upload)
	if err != nil {
		return badArchive("archive is not a gzipped tar file: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return badArchive("read archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeXHeader {
			continue
		}
		name, skip, err := u.entryName(hdr.Name)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = u.mkdir(name)
		case tar.TypeReg:
			err = u.writeFile(name, tr)
		default:
			err = errLink(hdr.Name)
		}
		if err != nil {
			return err
		}
	}
}

func (u *unpacker) unzip(upload *os.File) error {
	info, err := upload.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(upload, info.Size())
	// Names are checked per entry below, so an "insecure path" notice is moot.
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return badArchive("the .zip file is damaged or cannot be read: %w", err)
	}
	for _, zf := range zr.File {
		name, skip, err := u.entryName(zf.Name)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		mode := zf.Mode()
		switch {
		case mode.IsDir():
			err = u.mkdir(name)
		case mode.Type() != 0:
			err = errLink(zf.Name)
		case zf.Flags&0x1 != 0:
			err = badArchive("archive entry %q is password-protected, which is not supported", zf.Name)
		default:
			err = u.unzipFile(name, zf)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (u *unpacker) unzipFile(name string, zf *zip.File) error {
	rc, err := zf.Open()
	if err != nil {
		return badArchive("unpack %q: %w", zf.Name, err)
	}
	defer rc.Close()
	return u.writeFile(name, rc)
}

// finishRelease unwraps a single top-level folder (people often zip the
// folder instead of its contents) and rejects releases without any file.
// All moves happen inside dir, before the release is put in place.
func finishRelease(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 1 && entries[0].IsDir() {
		wrapper := filepath.Join(dir, entries[0].Name())
		moved := filepath.Join(dir, ".unwrap")
		if err := os.Rename(wrapper, moved); err != nil {
			return err
		}
		children, err := os.ReadDir(moved)
		if err != nil {
			return err
		}
		for _, c := range children {
			if err := os.Rename(filepath.Join(moved, c.Name()), filepath.Join(dir, c.Name())); err != nil {
				return err
			}
		}
		if err := os.Remove(moved); err != nil {
			return err
		}
	}

	hasFile := false
	err = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			hasFile = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !hasFile {
		return badArchive("archive contains no files")
	}
	return nil
}
