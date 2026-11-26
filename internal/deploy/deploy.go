// Package deploy manages the files of a site as immutable releases.
//
// Layout per site:
//
//	<base>/<site>/releases/<release-id>/   extracted archive, never modified
//	<base>/<site>/current                  symlink to the active release
//	<base>/<site>/pending/<release-id>     empty marker: published without
//	                                       ever having been made live
//
// The web server serves "current". Switching releases is one atomic rename of
// a symlink, so a deployment or rollback never exposes a half-written site
// and needs no web server reload.
package deploy

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// KeepReleases is how many of the newest releases are retained per site.
// The live release and the one Rollback would go back to are kept besides,
// so a burst of releases that are not made live cannot push them out.
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

// ctxReader stops reading once ctx is done, so a deployment whose request
// went away does not keep unpacking.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Manager owns the site directories below Base.
type Manager struct {
	Base string

	idMu      sync.Mutex
	lastStamp map[string]string // per site: timestamp of the last ID handed out
}

func (m *Manager) siteDir(site string) string     { return filepath.Join(m.Base, site) }
func (m *Manager) releasesDir(site string) string { return filepath.Join(m.Base, site, "releases") }
func (m *Manager) pendingDir(site string) string  { return filepath.Join(m.Base, site, "pending") }

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
// Unpacking stops when ctx is done (reading the upload already fails when
// the request goes away).
func (m *Manager) Deploy(ctx context.Context, site string, archive io.Reader) (string, error) {
	return m.add(ctx, site, archive, true)
}

// Add extracts an archive as a new release like Deploy, but leaves the live
// release as it is. The new release is marked as pending until it is made
// live with Rollback.
func (m *Manager) Add(ctx context.Context, site string, archive io.Reader) (string, error) {
	return m.add(ctx, site, archive, false)
}

func (m *Manager) add(ctx context.Context, site string, archive io.Reader, live bool) (string, error) {
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

	if err := extract(ctx, tmp, spool); err != nil {
		return "", err
	}
	if !live {
		// The marker exists before the release does, so a release is never
		// seen without it.
		if err := m.markPending(site, id); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, filepath.Join(relDir, id)); err != nil {
		_ = os.Remove(filepath.Join(m.pendingDir(site), id))
		return "", err
	}
	if live {
		if err := m.activate(site, id); err != nil {
			return "", err
		}
	}
	m.prune(site)
	return id, nil
}

func (m *Manager) markPending(site, id string) error {
	if err := os.MkdirAll(m.pendingDir(site), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(m.pendingDir(site), id), os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// Pending reports whether a release was published without being made live
// and has not been made live since.
func (m *Manager) Pending(site, id string) bool {
	_, err := os.Lstat(filepath.Join(m.pendingDir(site), id))
	return err == nil
}

// previous is the release Rollback goes back to without an id: the newest
// release older than the live one that is not pending (one that was live
// before, or older releases from before pending markers existed). "" if
// there is none.
func (m *Manager) previous(site string, releases []string) string {
	cur := m.Current(site)
	i := slices.Index(releases, cur)
	for i--; i >= 0; i-- {
		if !m.Pending(site, releases[i]) {
			return releases[i]
		}
	}
	return ""
}

// Rollback activates the given release, or the one before the current release
// when id is empty.
func (m *Manager) Rollback(site, id string) (string, error) {
	releases := m.Releases(site)
	if id == "" {
		if m.Current(site) != "" {
			id = m.previous(site, releases)
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
	if err := os.Rename(tmp, link); err != nil {
		return err
	}
	// Once live, a release is no longer pending: going back may return to it.
	if err := os.Remove(filepath.Join(m.pendingDir(site), id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// prune removes the oldest releases beyond KeepReleases. It never removes
// the live release nor the one Rollback would go back to, however many
// releases were added after them without being made live. Markers of
// releases that no longer exist are removed too.
func (m *Manager) prune(site string) {
	releases := m.Releases(site)
	keep := map[string]bool{m.Current(site): true, m.previous(site, releases): true}
	for i := 0; i < len(releases)-KeepReleases; i++ {
		if !keep[releases[i]] {
			_ = os.RemoveAll(filepath.Join(m.releasesDir(site), releases[i]))
		}
	}
	markers, _ := os.ReadDir(m.pendingDir(site))
	for _, e := range markers {
		if _, err := os.Lstat(filepath.Join(m.releasesDir(site), e.Name())); errors.Is(err, fs.ErrNotExist) {
			_ = os.Remove(filepath.Join(m.pendingDir(site), e.Name()))
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
// entry can escape dir. Ignored entries count toward the limits too: their
// content is read (and decompressed) just the same.
type unpacker struct {
	ctx   context.Context
	root  *os.Root
	total int64
	files int
}

func errTooManyEntries() error {
	return badArchive("archive has more than %d entries", MaxFiles)
}

func errTooLarge() error {
	return badArchive("archive is larger than %d MB when unpacked", MaxUnpackedBytes>>20)
}

// count counts one archive entry, whether it is unpacked or ignored.
func (u *unpacker) count() error {
	if u.files++; u.files > MaxFiles {
		return errTooManyEntries()
	}
	return nil
}

// entryName counts an archive entry, validates its name and returns the
// cleaned form. skip is true for entries that are silently ignored (macOS
// packaging junk, the archive's own root).
func (u *unpacker) entryName(raw string) (name string, skip bool, err error) {
	if err := u.count(); err != nil {
		return "", false, err
	}
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
	err = u.copyLimited(f, name, content)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("unpack %q: %w", name, cerr)
	}
	return err
}

// discard reads the content of an ignored entry, counting it like a written
// one: it has to be decompressed all the same.
func (u *unpacker) discard(name string, content io.Reader) error {
	return u.copyLimited(io.Discard, name, content)
}

// copyLimited copies content to w and counts it toward MaxUnpackedBytes,
// stopping as soon as the limit is exceeded.
func (u *unpacker) copyLimited(w io.Writer, name string, content io.Reader) error {
	r := ctxReader{u.ctx, io.LimitReader(archiveReader{content}, MaxUnpackedBytes-u.total+1)}
	n, err := io.Copy(w, r)
	if err != nil {
		return fmt.Errorf("unpack %q: %w", name, err)
	}
	if u.total += n; u.total > MaxUnpackedBytes {
		return errTooLarge()
	}
	return nil
}

func errLink(name string) error {
	return badArchive("archive entry %q is not a regular file or directory (links are not supported)", name)
}

// extract unpacks the spooled upload into dir. The format is detected from
// the content, never from a file name.
func extract(ctx context.Context, dir string, upload *os.File) error {
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
	u := &unpacker{ctx: ctx, root: root}

	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		err = u.untar(upload)
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		err = u.unzip(upload)
	default:
		return badArchive("the file is neither a .zip nor a .tar.gz archive")
	}
	if err != nil {
		// A cancelled request is not the archive's fault.
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return err
	}
	return finishRelease(dir)
}

// tarEntryOverhead is what one tar entry may add to the decompressed stream
// besides its content: its header, an extended header with long names, and
// padding to 512-byte blocks. tarTrailer covers the end of the archive.
const (
	tarEntryOverhead = 4 << 10
	tarTrailer       = 32 << 10
)

// streamLimit fails once more than left bytes have been read through it.
type streamLimit struct {
	r    io.Reader
	left int64
	err  error
}

func (l *streamLimit) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, l.err
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

func (u *unpacker) untar(upload io.Reader) error {
	gz, err := gzip.NewReader(ctxReader{u.ctx, upload})
	if err != nil {
		return badArchive("archive is not a gzipped tar file: %w", err)
	}
	defer gz.Close()

	// Entry contents are counted exactly below. This bounds the rest of the
	// decompressed stream (headers, extended headers, padding), which the tar
	// reader consumes on its own.
	limit := &streamLimit{
		r:    gz,
		left: MaxUnpackedBytes + int64(MaxFiles)*tarEntryOverhead + tarTrailer,
		err:  errTooLarge(),
	}
	tr := tar.NewReader(limit)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if errors.Is(err, limit.err) {
			return limit.err
		}
		if err != nil {
			return badArchive("read archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeXHeader {
			if err := u.count(); err != nil {
				return err
			}
			continue
		}
		name, skip, err := u.entryName(hdr.Name)
		if err != nil {
			return err
		}
		if skip {
			// Read the ignored content here, where it is counted, instead
			// of letting the next tr.Next skip over it unchecked.
			if err := u.discard(hdr.Name, tr); err != nil {
				return err
			}
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
	// zip.NewReader keeps every central directory record in memory, so the
	// number of records is checked first.
	if err := checkZipDirectory(upload, info.Size()); err != nil {
		return err
	}
	zr, err := zip.NewReader(upload, info.Size())
	// Names are checked per entry below, so an "insecure path" notice is moot.
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return badArchive("the .zip file is damaged or cannot be read: %w", err)
	}
	for _, zf := range zr.File {
		if err := u.ctx.Err(); err != nil {
			return err
		}
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

// Sizes from the zip format (APPNOTE.TXT).
const (
	zipEndLen         = 22 // end of central directory record, without comment
	zip64LocatorLen   = 20
	zip64EndLen       = 56
	zipDirHeaderLen   = 46 // central directory file header, without names
	zipEndSig         = 0x06054b50
	zip64LocatorSig   = 0x07064b50
	zip64EndSig       = 0x06064b50
	zipDirHeaderSig   = 0x02014b50
	zipEndSearchBytes = 65 * 1024
)

// checkZipDirectory refuses a zip whose central directory has more than
// MaxFiles records, before zip.NewReader allocates one entry per record (and
// preallocates for the count the archive declares). It locates the directory
// the way archive/zip does and walks the records with constant memory. Any
// structural problem is left to zip.NewReader to report.
func checkZipDirectory(r io.ReaderAt, size int64) error {
	tail := min(size, int64(zipEndSearchBytes))
	buf := make([]byte, tail)
	if _, err := r.ReadAt(buf, size-tail); err != nil && err != io.EOF {
		return nil
	}
	p := -1
	for i := len(buf) - zipEndLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) == zipEndSig {
			if i+zipEndLen+int(binary.LittleEndian.Uint16(buf[i+20:])) <= len(buf) {
				p = i
			}
			break
		}
	}
	if p < 0 {
		return nil
	}
	end := buf[p:]
	endOffset := size - tail + int64(p)
	records := uint64(binary.LittleEndian.Uint16(end[10:]))
	dirSize := uint64(binary.LittleEndian.Uint32(end[12:]))
	dirOffset := uint64(binary.LittleEndian.Uint32(end[16:]))

	if records == 0xffff || dirSize == 0xffffffff || dirOffset == 0xffffffff {
		var loc [zip64LocatorLen]byte
		if endOffset >= zip64LocatorLen {
			if _, err := r.ReadAt(loc[:], endOffset-zip64LocatorLen); err != nil {
				return nil
			}
		}
		if endOffset >= zip64LocatorLen &&
			binary.LittleEndian.Uint32(loc[0:]) == zip64LocatorSig &&
			binary.LittleEndian.Uint32(loc[4:]) == 0 &&
			binary.LittleEndian.Uint32(loc[16:]) == 1 {
			off := int64(binary.LittleEndian.Uint64(loc[8:]))
			var e [zip64EndLen]byte
			if _, err := r.ReadAt(e[:], off); err != nil || binary.LittleEndian.Uint32(e[0:]) != zip64EndSig {
				return nil
			}
			endOffset = off
			records = binary.LittleEndian.Uint64(e[32:])
			dirSize = binary.LittleEndian.Uint64(e[40:])
			dirOffset = binary.LittleEndian.Uint64(e[48:])
		}
	}
	if records > uint64(MaxFiles) {
		return errTooManyEntries()
	}
	if dirSize > 1<<63-1 || dirOffset > 1<<63-1 {
		return nil
	}
	// The count in the end record is only compared modulo 65536, so the
	// records themselves are counted. archive/zip starts reading either at
	// the declared offset or where the directory must start given its size.
	for _, start := range []int64{int64(dirOffset), endOffset - int64(dirSize)} {
		if start >= 0 && start < size && countZipRecords(r, start, size) > MaxFiles {
			return errTooManyEntries()
		}
	}
	return nil
}

// countZipRecords counts consecutive central directory records from start,
// stopping at the first one that is not complete or once MaxFiles is passed.
func countZipRecords(r io.ReaderAt, start, size int64) int {
	br := bufio.NewReader(io.NewSectionReader(r, start, size-start))
	var h [zipDirHeaderLen]byte
	n := 0
	for n <= MaxFiles {
		if _, err := io.ReadFull(br, h[:]); err != nil || binary.LittleEndian.Uint32(h[0:]) != zipDirHeaderSig {
			break
		}
		names := int(binary.LittleEndian.Uint16(h[28:])) + int(binary.LittleEndian.Uint16(h[30:])) + int(binary.LittleEndian.Uint16(h[32:]))
		if _, err := br.Discard(names); err != nil {
			break
		}
		n++
	}
	return n
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

// Sites lists the site names that have files below Base.
func (m *Manager) Sites() ([]string, error) {
	entries, err := os.ReadDir(m.Base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
