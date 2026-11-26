package worker

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// The source tree comes from a repository anyone may have written to, and
// the output is written by the build itself. Both are untrusted, in both
// directions:
//
//   - Unpacking the source (unpackSource) accepts plain files, folders and
//     links only. Every write goes through os.Root, so no entry, and no link
//     an earlier entry created, can make the worker write outside the
//     workspace. Owners, set-user-ID bits, devices and hard links are not
//     taken over: every file belongs to the worker's user with mode 0644 or
//     0755. Sizes are counted on the bytes actually read.
//   - Packing the output (packOutput) reads through os.Root as well, takes
//     plain files and folders only and refuses everything else, links
//     included, the same as an uploaded archive. It runs after the build's
//     last process is gone, so nothing changes under it.
//   - Deleting (removeTree, or the runtime's RemoveTree) never follows links.

// treeError is a problem with the source or the output itself, as opposed
// to a failure of the worker. Its text is meant for people.
type treeError struct{ msg string }

func (e *treeError) Error() string { return e.msg }

func treeErrorf(format string, args ...any) error {
	return &treeError{fmt.Sprintf(format, args...)}
}

func isTreeError(err error) bool {
	var te *treeError
	return errors.As(err, &te)
}

// What one tar entry may add to the stream besides its content (header,
// extended header with long names, padding), and the end of the archive.
const (
	tarEntryOverhead = 4 << 10
	tarTrailer       = 32 << 10
	maxLinkTarget    = 4096
	maxStrip         = 8
)

// limitedReader fails with err once more than left bytes were read.
type limitedReader struct {
	r    io.Reader
	left int64
	err  error
}

func (l *limitedReader) Read(p []byte) (int, error) {
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

// sourceLimits bound a source tree; variables so tests can lower them.
type sourceLimits struct {
	bytes int64
	files int
}

var defaultSourceLimits = sourceLimits{bytes: build.MaxSourceBytes, files: build.MaxSourceFiles}

// errNoRoom: the source tree does not fit into the disk space of a build.
var errNoRoom error = &treeError{"the source tree does not fit into the disk space of a build"}

// isDiskFull tells whether err says the disk space of the build (its
// project quota) or the build disk ran out.
func isDiskFull(err error) bool {
	return errors.Is(err, syscall.EDQUOT) || errors.Is(err, syscall.ENOSPC)
}

// unpackSource unpacks a tar stream (gzipped or not) into dir, which must
// exist and be empty. strip removes that many leading folders from every
// name (1 for the archives GitHub makes of a commit); entries with fewer
// are skipped. With dir "" nothing is written: the stream is only checked
// (names, types, sizes and counts), as when it arrives.
func unpackSource(ctx context.Context, dir string, stream io.Reader, strip int, lim sourceLimits) error {
	if strip < 0 || strip > maxStrip {
		return treeErrorf("strip must be between 0 and %d", maxStrip)
	}
	tooLarge := treeErrorf("the source tree is larger than %d MB", lim.bytes>>20)
	tooMany := treeErrorf("the source tree has more than %d files and folders", lim.files)

	br := bufio.NewReader(ctxReader{ctx, stream})
	var in io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return treeErrorf("the source tree could not be read: %v", err)
		}
		defer gz.Close()
		in = gz
	}
	limit := &limitedReader{r: in, left: lim.bytes + int64(lim.files)*tarEntryOverhead + tarTrailer, err: tooLarge}

	var root *os.Root
	if dir != "" {
		r, err := os.OpenRoot(dir)
		if err != nil {
			return err
		}
		defer r.Close()
		root = r
	}

	var total int64
	entries := 0
	tr := tar.NewReader(limit)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if errors.Is(err, tooLarge) {
			return tooLarge
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			return treeErrorf("the source tree could not be read: %v", err)
		}
		if entries++; entries > lim.files {
			return tooMany
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeXHeader {
			continue
		}
		name, ok, err := entryName(hdr.Name, strip)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if root != nil {
				err = root.MkdirAll(name, 0o755)
			}
		case tar.TypeReg:
			mode := os.FileMode(0o644)
			if hdr.Mode&0o100 != 0 {
				mode = 0o755
			}
			content := io.LimitReader(tr, lim.bytes-total+1)
			var n int64
			if root != nil {
				n, err = writeFile(root, name, mode, content)
			} else {
				n, err = io.Copy(io.Discard, content)
			}
			if total += n; total > lim.bytes {
				return tooLarge
			}
		case tar.TypeSymlink:
			if len(hdr.Linkname) == 0 || len(hdr.Linkname) > maxLinkTarget || strings.ContainsRune(hdr.Linkname, 0) {
				return treeErrorf("the link %q in the source tree has no valid target", hdr.Name)
			}
			if root == nil {
				break
			}
			if err = mkParent(root, name); err == nil {
				// The link is stored as it is and never followed by the
				// worker; inside the container it resolves inside the
				// container.
				err = root.Symlink(hdr.Linkname, name)
			}
		default:
			return treeErrorf("%q in the source tree is not a file, folder or link", hdr.Name)
		}
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if errors.Is(err, tooLarge) {
				return tooLarge
			}
			if isDiskFull(err) {
				return errNoRoom
			}
			// An entry that would leave the workspace (through "..", an
			// absolute path or a link), or one given twice.
			return treeErrorf("%q in the source tree cannot be unpacked: %v", hdr.Name, rootCause(err))
		}
	}
	if entries == 0 {
		return treeErrorf("the source tree is empty")
	}
	return nil
}

// entryName validates the name of an archive entry and removes strip
// leading folders. ok is false for entries to skip (the archive's own root,
// or less deep than strip).
func entryName(raw string, strip int) (name string, ok bool, err error) {
	if strings.ContainsRune(raw, 0) {
		return "", false, treeErrorf("a name in the source tree contains a zero byte")
	}
	name = path.Clean(raw)
	if name == "." {
		return "", false, nil
	}
	if path.IsAbs(raw) || !filepath.IsLocal(name) {
		return "", false, treeErrorf("%q in the source tree points outside of it", raw)
	}
	for range strip {
		_, rest, found := strings.Cut(name, "/")
		if !found {
			return "", false, nil
		}
		name = rest
	}
	return name, true, nil
}

func mkParent(root *os.Root, name string) error {
	if d := path.Dir(name); d != "." {
		return root.MkdirAll(d, 0o755)
	}
	return nil
}

func writeFile(root *os.Root, name string, mode os.FileMode, content io.Reader) (int64, error) {
	if err := mkParent(root, name); err != nil {
		return 0, err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// rootCause strips the path from an error of the file system, which would
// name the worker's own directories.
func rootCause(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err
	}
	return err
}

// outputLimits bound an output folder; variables so tests can lower them.
type outputLimits struct {
	bytes int64
	files int
}

var defaultOutputLimits = outputLimits{bytes: build.MaxOutputBytes, files: build.MaxOutputFiles}

// packOutput writes the folder output of the workspace as a tar.gz of
// plain files and folders to w, and returns its size unpacked and its
// number of entries. Anything else in it (a link, a device, a pipe) is an
// error, as it would be in an upload.
func packOutput(ctx context.Context, workspace, output string, w io.Writer, lim outputLimits) (size int64, files int, err error) {
	ws, err := os.OpenRoot(workspace)
	if err != nil {
		return 0, 0, err
	}
	defer ws.Close()
	out := ws
	if output != "." {
		// OpenRoot follows links only as long as they stay inside the
		// workspace: an output folder that is a link to /etc is refused.
		info, err := ws.Lstat(output)
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, treeErrorf("the build did not create the output folder %q", output)
		}
		if err != nil {
			return 0, 0, treeErrorf("the output folder %q cannot be read: %v", output, rootCause(err))
		}
		if !info.IsDir() {
			return 0, 0, treeErrorf("the output %q is not a folder", output)
		}
		if out, err = ws.OpenRoot(output); err != nil {
			return 0, 0, treeErrorf("the output folder %q cannot be read: %v", output, rootCause(err))
		}
		defer out.Close()
	}

	gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	hasFile := false
	walkErr := fs.WalkDir(out.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			return treeErrorf("%q in the output folder cannot be read: %v", name, rootCause(err))
		}
		if name == "." {
			return nil
		}
		if files++; files > lim.files {
			return treeErrorf("the output folder has more than %d files and folders", lim.files)
		}
		switch {
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755})
		case d.Type().IsRegular():
			f, err := out.Open(name)
			if err != nil {
				return treeErrorf("%q in the output folder cannot be read: %v", name, rootCause(err))
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() {
				return treeErrorf("%q in the output folder is not a plain file", name)
			}
			if size += info.Size(); size > lim.bytes {
				return treeErrorf("the output folder is larger than %d MB", lim.bytes>>20)
			}
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
				return err
			}
			if _, err := io.CopyN(tw, f, info.Size()); err != nil {
				return fmt.Errorf("pack %q: %w", name, err)
			}
			hasFile = true
			return nil
		default:
			what := "not a plain file or folder"
			if d.Type()&fs.ModeSymlink != 0 {
				what = "a link; links cannot be published"
			}
			return treeErrorf("%q in the output folder is %s", name, what)
		}
	})
	if walkErr != nil {
		return 0, 0, walkErr
	}
	if !hasFile {
		return 0, 0, treeErrorf("the output folder %q contains no files", output)
	}
	if err := tw.Close(); err != nil {
		return 0, 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, 0, err
	}
	return size, files, nil
}

// removeTree deletes dir and everything in it. Builds can leave folders
// without permissions behind; they are opened up first.
func removeTree(dir string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	var open func(p string, depth int)
	open = func(p string, depth int) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || depth > 4096 {
			return
		}
		_ = os.Chmod(p, 0o700)
		entries, _ := os.ReadDir(p)
		for _, e := range entries {
			if e.IsDir() {
				open(filepath.Join(p, e.Name()), depth+1)
			}
		}
	}
	open(dir, 0)
	return os.RemoveAll(dir)
}
