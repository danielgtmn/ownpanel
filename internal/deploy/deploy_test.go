package deploy

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name, body, link string
	typ              byte
}

func archive(t *testing.T, entries ...entry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if e.typ == 0 {
			e.typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.body)), Linkname: e.link}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return bytes.NewReader(buf.Bytes())
}

func served(t *testing.T, m *Manager, site, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(m.Root(site), file))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDeployAndRollback(t *testing.T) {
	m := &Manager{Base: t.TempDir()}
	v1, err := m.Deploy(t.Context(), "a", archive(t, entry{name: "./index.html", body: "v1"}, entry{name: "css/app.css", body: "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := served(t, m, "a", "index.html"); got != "v1" {
		t.Fatalf("got %q", got)
	}
	if got := served(t, m, "a", "css/app.css"); got != "x" {
		t.Fatalf("got %q", got)
	}
	v2, err := m.Deploy(t.Context(), "a", archive(t, entry{name: "index.html", body: "v2"}))
	if err != nil {
		t.Fatal(err)
	}
	if m.Current("a") != v2 || served(t, m, "a", "index.html") != "v2" {
		t.Fatal("v2 not active")
	}
	if id, err := m.Rollback("a", ""); err != nil || id != v1 {
		t.Fatalf("rollback: %q %v", id, err)
	}
	if served(t, m, "a", "index.html") != "v1" {
		t.Fatal("v1 not active after rollback")
	}
	if _, err := m.Rollback("a", ""); err == nil {
		t.Fatal("rollback past the first release should fail")
	}
	if _, err := m.Rollback("a", "nope"); err == nil {
		t.Fatal("rollback to unknown release should fail")
	}
}

func TestDeployRejectsUnsafeArchives(t *testing.T) {
	cases := map[string]entry{
		"parent traversal": {name: "../escape.txt", body: "x"},
		"absolute path":    {name: "/etc/escape.txt", body: "x"},
		"symlink":          {name: "link", typ: tar.TypeSymlink, link: "/etc/passwd"},
		"hard link":        {name: "link", typ: tar.TypeLink, link: "index.html"},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			m := &Manager{Base: filepath.Join(base, "sites")}
			if _, err := m.Deploy(t.Context(), "a", archive(t, entry{name: "index.html", body: "ok"}, e)); err == nil {
				t.Fatal("expected error")
			}
			if m.Current("a") != "" || len(m.Releases("a")) != 0 {
				t.Fatal("failed deployment left a release behind")
			}
			if _, err := os.Stat(filepath.Join(base, "escape.txt")); err == nil {
				t.Fatal("file escaped the site directory")
			}
		})
	}
}

func TestPruneKeepsActiveRelease(t *testing.T) {
	m := &Manager{Base: t.TempDir()}
	for i := 0; i < KeepReleases+3; i++ {
		if _, err := m.Deploy(t.Context(), "a", archive(t, entry{name: "index.html", body: "x"})); err != nil {
			t.Fatal(err)
		}
	}
	rel := m.Releases("a")
	if len(rel) != KeepReleases {
		t.Fatalf("kept %d releases, want %d", len(rel), KeepReleases)
	}
	if rel[len(rel)-1] != m.Current("a") {
		t.Fatal("newest release is not active")
	}
}

// zipArchive builds a zip. Entries with a mode set get it as Unix mode bits.
func zipArchive(t *testing.T, entries ...zipEntry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

type zipEntry struct {
	name, body string
	mode       os.FileMode
}

func releasesDirEntries(t *testing.T, m *Manager, site string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(m.Base, site, "releases"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestDeployZip(t *testing.T) {
	m := &Manager{Base: t.TempDir()}
	id, err := m.Deploy(t.Context(), "a", zipArchive(t,
		zipEntry{name: "index.html", body: "hello"},
		zipEntry{name: "css/", mode: os.ModeDir | 0o755},
		zipEntry{name: "css/deep/app.css", body: "x"},
		zipEntry{name: "img/logo.svg", body: "<svg/>"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if m.Current("a") != id {
		t.Fatal("release not active")
	}
	for file, want := range map[string]string{"index.html": "hello", "css/deep/app.css": "x", "img/logo.svg": "<svg/>"} {
		if got := served(t, m, "a", file); got != want {
			t.Fatalf("%s: got %q", file, got)
		}
	}
	info, err := os.Stat(filepath.Join(m.Root("a"), "index.html"))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("file mode %v %v", info, err)
	}
	info, err = os.Stat(filepath.Join(m.Root("a"), "css"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("dir mode %v %v", info, err)
	}
	if got := releasesDirEntries(t, m, "a"); len(got) != 1 || got[0] != id {
		t.Fatalf("leftovers in releases: %v", got)
	}
}

func TestDeployZipRejectsUnsafeArchives(t *testing.T) {
	cases := map[string]zipEntry{
		"parent traversal": {name: "../escape.txt", body: "x"},
		"nested traversal": {name: "a/../../escape.txt", body: "x"},
		"absolute path":    {name: "/etc/escape.txt", body: "x"},
		"backslash":        {name: "..\\escape.txt", body: "x"},
		"symlink":          {name: "link", body: "/etc/passwd", mode: os.ModeSymlink | 0o777},
		"device":           {name: "dev", mode: os.ModeDevice | 0o644},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			m := &Manager{Base: filepath.Join(base, "sites")}
			_, err := m.Deploy(t.Context(), "a", zipArchive(t, zipEntry{name: "index.html", body: "ok"}, e))
			if err == nil {
				t.Fatal("expected error")
			}
			if m.Current("a") != "" || len(releasesDirEntries(t, m, "a")) != 0 {
				t.Fatal("failed deployment left files behind")
			}
			if _, err := os.Stat(filepath.Join(base, "escape.txt")); err == nil {
				t.Fatal("file escaped the site directory")
			}
		})
	}
}

func TestDeployZipLimitsUseActualBytes(t *testing.T) {
	old := MaxUnpackedBytes
	MaxUnpackedBytes = 1000
	t.Cleanup(func() { MaxUnpackedBytes = old })

	big := strings.Repeat("a", 5000) // compresses to a few bytes
	honest := zipArchive(t, zipEntry{name: "big.txt", body: big})

	// Same archive, but the central directory claims the file is tiny.
	raw := mustRead(t, honest)
	i := bytes.Index(raw, []byte("PK\x01\x02"))
	if i < 0 {
		t.Fatal("no central directory")
	}
	raw[i+24], raw[i+25], raw[i+26], raw[i+27] = 10, 0, 0, 0
	lying := bytes.NewReader(raw)

	for name, a := range map[string]*bytes.Reader{"honest header": honest, "lying header": lying} {
		t.Run(name, func(t *testing.T) {
			m := &Manager{Base: t.TempDir()}
			if _, err := m.Deploy(t.Context(), "a", a); err == nil {
				t.Fatal("expected error")
			}
			if m.Current("a") != "" || len(releasesDirEntries(t, m, "a")) != 0 {
				t.Fatal("failed deployment left files behind")
			}
		})
	}
}

func mustRead(t *testing.T, r *bytes.Reader) []byte {
	t.Helper()
	b := make([]byte, r.Len())
	if _, err := r.ReadAt(b, 0); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDeployTooManyEntries(t *testing.T) {
	old := MaxFiles
	MaxFiles = 2
	t.Cleanup(func() { MaxFiles = old })
	m := &Manager{Base: t.TempDir()}
	if _, err := m.Deploy(t.Context(), "a", zipArchive(t, zipEntry{name: "a", body: "1"}, zipEntry{name: "b", body: "1"}, zipEntry{name: "c", body: "1"})); err == nil {
		t.Fatal("zip: expected error")
	}
	if _, err := m.Deploy(t.Context(), "a", archive(t, entry{name: "a"}, entry{name: "b"}, entry{name: "c"})); err == nil {
		t.Fatal("tar: expected error")
	}
	if len(releasesDirEntries(t, m, "a")) != 0 {
		t.Fatal("left files behind")
	}
}

func TestDeploySkipsMacJunk(t *testing.T) {
	m := &Manager{Base: t.TempDir()}
	_, err := m.Deploy(t.Context(), "z", zipArchive(t,
		zipEntry{name: "index.html", body: "z"},
		zipEntry{name: "__MACOSX/", mode: os.ModeDir | 0o755},
		zipEntry{name: "__MACOSX/._index.html", body: "junk"},
		zipEntry{name: ".DS_Store", body: "junk"},
		zipEntry{name: "css/.DS_Store", body: "junk"},
		zipEntry{name: "css/._app.css", body: "junk"},
		zipEntry{name: "css/app.css", body: "x"},
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Deploy(t.Context(), "t", archive(t,
		entry{name: "index.html", body: "t"},
		entry{name: "__MACOSX/._index.html", body: "junk"},
		entry{name: ".DS_Store", body: "junk"},
		entry{name: "._x", body: "junk"},
		entry{name: "__MACOSX/link", typ: tar.TypeSymlink, link: "/etc/passwd"},
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{"z", "t"} {
		var names []string
		_ = filepath.WalkDir(m.Root(site), func(p string, d os.DirEntry, err error) error {
			if err == nil && p != m.Root(site) {
				rel, _ := filepath.Rel(m.Root(site), p)
				names = append(names, rel)
			}
			return nil
		})
		for _, n := range names {
			if strings.Contains(n, "__MACOSX") || strings.Contains(n, ".DS_Store") || strings.Contains(n, "._") {
				t.Fatalf("%s: junk was extracted: %v", site, names)
			}
		}
		if served(t, m, site, "index.html") != site {
			t.Fatalf("%s: index.html missing", site)
		}
	}
	if served(t, m, "z", "css/app.css") != "x" {
		t.Fatal("css/app.css missing")
	}
}

func TestDeployUnwrapsSingleFolder(t *testing.T) {
	cases := map[string]func(*testing.T) *bytes.Reader{
		"zip": func(t *testing.T) *bytes.Reader {
			return zipArchive(t,
				zipEntry{name: "mysite/", mode: os.ModeDir | 0o755},
				zipEntry{name: "mysite/index.html", body: "hi"},
				zipEntry{name: "mysite/mysite/inner.txt", body: "in"},
				zipEntry{name: "__MACOSX/mysite/._index.html", body: "junk"},
			)
		},
		"tar.gz": func(t *testing.T) *bytes.Reader {
			return archive(t,
				entry{name: "mysite/", typ: tar.TypeDir},
				entry{name: "mysite/index.html", body: "hi"},
				entry{name: "mysite/mysite/inner.txt", body: "in"},
			)
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			m := &Manager{Base: t.TempDir()}
			if _, err := m.Deploy(t.Context(), "a", mk(t)); err != nil {
				t.Fatal(err)
			}
			if served(t, m, "a", "index.html") != "hi" || served(t, m, "a", "mysite/inner.txt") != "in" {
				t.Fatal("folder was not unwrapped")
			}
			if len(releasesDirEntries(t, m, "a")) != 1 {
				t.Fatal("leftovers in releases")
			}
		})
	}

	// Two top-level entries: nothing is unwrapped.
	m := &Manager{Base: t.TempDir()}
	if _, err := m.Deploy(t.Context(), "a", zipArchive(t, zipEntry{name: "d/index.html", body: "x"}, zipEntry{name: "robots.txt", body: "y"})); err != nil {
		t.Fatal(err)
	}
	if served(t, m, "a", "d/index.html") != "x" {
		t.Fatal("folder was unwrapped although there is a second entry")
	}
}

func TestDeployRejectsNonArchivesAndEmptyArchives(t *testing.T) {
	cases := map[string]struct {
		in      *bytes.Reader
		wantErr string
	}{
		"not an archive": {bytes.NewReader([]byte("not an archive")), "neither a .zip nor a .tar.gz"},
		"too short":      {bytes.NewReader([]byte("P")), "neither a .zip nor a .tar.gz"},
		"empty upload":   {bytes.NewReader(nil), "neither a .zip nor a .tar.gz"},
		"empty zip":      {zipArchive(t), "no files"},
		"empty tar.gz":   {archive(t), "no files"},
		"only folders":   {zipArchive(t, zipEntry{name: "a/b/", mode: os.ModeDir | 0o755}), "no files"},
		"only junk":      {zipArchive(t, zipEntry{name: ".DS_Store", body: "x"}), "no files"},
		"empty folder":   {archive(t, entry{name: "site/", typ: tar.TypeDir}), "no files"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := &Manager{Base: t.TempDir()}
			if _, err := m.Deploy(t.Context(), "a", archive(t, entry{name: "index.html", body: "live"})); err != nil {
				t.Fatal(err)
			}
			live := m.Current("a")
			_, err := m.Deploy(t.Context(), "a", c.in)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, c.wantErr)
			}
			if m.Current("a") != live || served(t, m, "a", "index.html") != "live" {
				t.Fatal("live release was touched")
			}
			if got := releasesDirEntries(t, m, "a"); len(got) != 1 || got[0] != live {
				t.Fatalf("leftovers in releases: %v", got)
			}
		})
	}
}

func TestDeployLeavesNoTempFiles(t *testing.T) {
	m := &Manager{Base: t.TempDir()}
	id, err := m.Deploy(t.Context(), "a", zipArchive(t, zipEntry{name: "index.html", body: "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := releasesDirEntries(t, m, "a"); len(got) != 1 || got[0] != id {
		t.Fatalf("after success: %v", got)
	}
	if _, err := m.Deploy(t.Context(), "a", zipArchive(t, zipEntry{name: "../x", body: "x"})); err == nil {
		t.Fatal("expected error")
	}
	if got := releasesDirEntries(t, m, "a"); len(got) != 1 || got[0] != id {
		t.Fatalf("after failure: %v", got)
	}
}

// Ignored entries (macOS junk) in a tar are decompressed all the same, so
// their content counts toward the unpacked size. (Ignored zip entries are
// never opened.)
func TestDeployCountsIgnoredContent(t *testing.T) {
	old := MaxUnpackedBytes
	MaxUnpackedBytes = 1000
	t.Cleanup(func() { MaxUnpackedBytes = old })
	junk := strings.Repeat("a", 5000) // compresses to a few bytes

	for name, a := range map[string]*bytes.Reader{
		"junk name": archive(t,
			entry{name: "index.html", body: "x"},
			entry{name: "._index.html", body: junk},
		),
		"junk folder": archive(t,
			entry{name: "index.html", body: "x"},
			entry{name: "__MACOSX/big", body: junk},
		),
		"archive root": archive(t,
			entry{name: "index.html", body: "x"},
			entry{name: ".", body: junk},
		),
	} {
		t.Run(name, func(t *testing.T) {
			m := &Manager{Base: t.TempDir()}
			_, err := m.Deploy(t.Context(), "a", a)
			if !errors.Is(err, ErrBadArchive) || !strings.Contains(err.Error(), "larger than") {
				t.Fatalf("got %v, want the size limit", err)
			}
			if m.Current("a") != "" || len(releasesDirEntries(t, m, "a")) != 0 {
				t.Fatal("failed deployment left files behind")
			}
		})
	}
}

func TestDeployCountsIgnoredEntries(t *testing.T) {
	old := MaxFiles
	MaxFiles = 3
	t.Cleanup(func() { MaxFiles = old })
	m := &Manager{Base: t.TempDir()}
	_, err := m.Deploy(t.Context(), "a", archive(t,
		entry{name: "index.html", body: "x"},
		entry{name: "._a"}, entry{name: "._b"}, entry{name: "._c"},
	))
	if err == nil || !strings.Contains(err.Error(), "more than 3 entries") {
		t.Fatalf("got %v, want the entry limit", err)
	}
}

// Headers are not entry content, but they are decompressed too: extended
// headers must not be a way around the limits.
func TestDeployLimitsTarHeaders(t *testing.T) {
	oldBytes, oldFiles := MaxUnpackedBytes, MaxFiles
	MaxUnpackedBytes, MaxFiles = 1000, 2
	t.Cleanup(func() { MaxUnpackedBytes, MaxFiles = oldBytes, oldFiles })

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name: "index.html", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1, Format: tar.FormatPAX,
		PAXRecords: map[string]string{"comment": strings.Repeat("a", 512<<10)},
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()

	m := &Manager{Base: t.TempDir()}
	_, err := m.Deploy(t.Context(), "a", bytes.NewReader(buf.Bytes()))
	if !errors.Is(err, ErrBadArchive) || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("got %v, want the size limit", err)
	}
}

// zipEnd returns the offset of the end of central directory record of a zip
// written without a comment.
func zipEnd(t *testing.T, raw []byte) int {
	t.Helper()
	i := len(raw) - 22
	if binary.LittleEndian.Uint32(raw[i:]) != 0x06054b50 {
		t.Fatal("no end of central directory record")
	}
	return i
}

// toZip64 rewrites the end of a zip into the zip64 form, declaring records
// entries.
func toZip64(t *testing.T, raw []byte, records uint64) []byte {
	t.Helper()
	i := zipEnd(t, raw)
	dirSize := uint64(binary.LittleEndian.Uint32(raw[i+12:]))
	dirOffset := uint64(binary.LittleEndian.Uint32(raw[i+16:]))
	out := append([]byte{}, raw[:i]...)
	end64 := uint64(len(out))
	le := binary.LittleEndian
	out = le.AppendUint32(out, 0x06064b50)
	out = le.AppendUint64(out, 44) // size of the rest of this record
	out = le.AppendUint16(out, 45)
	out = le.AppendUint16(out, 45)
	out = le.AppendUint32(out, 0)
	out = le.AppendUint32(out, 0)
	out = le.AppendUint64(out, records)
	out = le.AppendUint64(out, records)
	out = le.AppendUint64(out, dirSize)
	out = le.AppendUint64(out, dirOffset)
	out = le.AppendUint32(out, 0x07064b50)
	out = le.AppendUint32(out, 0)
	out = le.AppendUint64(out, end64)
	out = le.AppendUint32(out, 1)
	out = le.AppendUint32(out, 0x06054b50)
	out = le.AppendUint16(out, 0)
	out = le.AppendUint16(out, 0)
	out = le.AppendUint16(out, 0xffff)
	out = le.AppendUint16(out, 0xffff)
	out = le.AppendUint32(out, 0xffffffff)
	out = le.AppendUint32(out, 0xffffffff)
	out = le.AppendUint16(out, 0)
	return out
}

// The central directory is checked before archive/zip reads it into memory.
// Each case would fail differently (or not at all) if the records were
// parsed first, so the error shows the check ran before.
func TestDeployZipDirectoryCheckedFirst(t *testing.T) {
	old := MaxFiles
	MaxFiles = 10
	t.Cleanup(func() { MaxFiles = old })

	var many []zipEntry
	for i := range 20 {
		many = append(many, zipEntry{name: fmt.Sprintf("f%d", i), body: "x"})
	}
	// More records than the end record admits to: archive/zip compares the
	// count only modulo 65536 and would report a damaged file here.
	understated := mustRead(t, zipArchive(t, many...))
	i := zipEnd(t, understated)
	understated[i+8], understated[i+10] = 4, 4

	// A zip64 end record declaring billions of records for a single one:
	// archive/zip would report a damaged file.
	overstated := toZip64(t, mustRead(t, zipArchive(t, zipEntry{name: "index.html", body: "x"})), 1<<32)

	for name, raw := range map[string][]byte{"understated count": understated, "zip64 declared count": overstated} {
		t.Run(name, func(t *testing.T) {
			m := &Manager{Base: t.TempDir()}
			_, err := m.Deploy(t.Context(), "a", bytes.NewReader(raw))
			if !errors.Is(err, ErrBadArchive) || !strings.Contains(err.Error(), "more than 10 entries") {
				t.Fatalf("got %v, want the entry limit", err)
			}
		})
	}
}

func TestDeployZip64(t *testing.T) {
	raw := toZip64(t, mustRead(t, zipArchive(t,
		zipEntry{name: "index.html", body: "hi"},
		zipEntry{name: "css/app.css", body: "x"},
	)), 2)
	m := &Manager{Base: t.TempDir()}
	if _, err := m.Deploy(t.Context(), "a", bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if served(t, m, "a", "index.html") != "hi" {
		t.Fatal("index.html missing")
	}
}

func TestDeployStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m := &Manager{Base: t.TempDir()}
	_, err := m.Deploy(ctx, "a", archive(t, entry{name: "index.html", body: "x"}))
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrBadArchive) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if m.Current("a") != "" || len(releasesDirEntries(t, m, "a")) != 0 {
		t.Fatal("cancelled deployment left files behind")
	}
}
