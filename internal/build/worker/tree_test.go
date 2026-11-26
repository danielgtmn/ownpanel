package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func tarOf(t *testing.T, entries ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range entries {
		content := []byte(h.Linkname)
		if h.Typeflag == tar.TypeReg {
			h.Linkname = ""
			h.Size = int64(len(content))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write(content)
		}
	}
	tw.Close()
	return buf.Bytes()
}

func TestUnpackSource(t *testing.T) {
	ctx := context.Background()
	outside := t.TempDir()
	cases := map[string][]byte{
		"dot-dot":        tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "../x", Linkname: "x", Mode: 0o644}),
		"absolute":       tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "/x", Linkname: "x", Mode: 0o644}),
		"device":         tarOf(t, tar.Header{Typeflag: tar.TypeChar, Name: "mem", Mode: 0o644}),
		"hard link":      tarOf(t, tar.Header{Typeflag: tar.TypeLink, Name: "l", Linkname: "/etc/passwd"}),
		"through a link": tarOf(t, tar.Header{Typeflag: tar.TypeSymlink, Name: "out", Linkname: outside}, tar.Header{Typeflag: tar.TypeReg, Name: "out/x", Linkname: "x", Mode: 0o644}),
		"twice":          tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "a", Linkname: "1", Mode: 0o644}, tar.Header{Typeflag: tar.TypeReg, Name: "a", Linkname: "2", Mode: 0o644}),
		"empty":          tarOf(t),
	}
	for name, data := range cases {
		dir := t.TempDir()
		if err := unpackSource(ctx, dir, bytes.NewReader(data), 0, defaultSourceLimits); err == nil {
			t.Errorf("%s: unpacked", name)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("a file was written outside: %v", entries)
	}
	// Modes are the worker's: no set-user-ID bit, no foreign owner.
	dir := t.TempDir()
	data := tarOf(t,
		tar.Header{Typeflag: tar.TypeReg, Name: "top/suid", Linkname: "x", Mode: 0o4755, Uid: 0},
		tar.Header{Typeflag: tar.TypeReg, Name: "top/plain", Linkname: "y", Mode: 0o600},
		tar.Header{Typeflag: tar.TypeSymlink, Name: "top/link", Linkname: "plain"},
	)
	if err := unpackSource(ctx, dir, bytes.NewReader(data), 1, defaultSourceLimits); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "suid")); fi.Mode() != 0o755 {
		t.Errorf("suid has mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(dir, "plain")); fi.Mode() != 0o644 {
		t.Errorf("plain has mode %v", fi.Mode())
	}
	// Limits.
	big := tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "a", Linkname: string(make([]byte, 2048)), Mode: 0o644})
	if err := unpackSource(ctx, t.TempDir(), bytes.NewReader(big), 0, sourceLimits{bytes: 1024, files: 10}); err == nil {
		t.Errorf("a source over the size limit was unpacked")
	}
	many := tarOf(t, tar.Header{Typeflag: tar.TypeDir, Name: "a/", Mode: 0o755}, tar.Header{Typeflag: tar.TypeDir, Name: "b/", Mode: 0o755}, tar.Header{Typeflag: tar.TypeDir, Name: "c/", Mode: 0o755})
	if err := unpackSource(ctx, "", bytes.NewReader(many), 0, sourceLimits{bytes: 1024, files: 2}); err == nil {
		t.Errorf("a source over the file limit was accepted")
	}
}

func TestPackOutput(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "dist/sub"), 0o755)
	os.WriteFile(filepath.Join(ws, "dist/sub/a.html"), []byte("a"), 0o4755)
	var buf bytes.Buffer
	size, files, err := packOutput(ctx, ws, "dist", &buf, defaultOutputLimits)
	if err != nil || size != 1 || files != 2 {
		t.Fatalf("%d %d %v", size, files, err)
	}
	gz, _ := gzip.NewReader(&buf)
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if h.Typeflag == tar.TypeReg && h.Mode != 0o644 {
			t.Errorf("%s mode %o", h.Name, h.Mode)
		}
	}
	os.Symlink("/etc", filepath.Join(ws, "dist/etc"))
	if _, _, err := packOutput(ctx, ws, "dist", io.Discard, defaultOutputLimits); err == nil || !isTreeError(err) {
		t.Errorf("a link was packed: %v", err)
	}
	os.Symlink("/", filepath.Join(ws, "root"))
	if _, _, err := packOutput(ctx, ws, "root", io.Discard, defaultOutputLimits); err == nil {
		t.Errorf("an output folder that is a link out was packed")
	}
	if _, _, err := packOutput(ctx, ws, "missing", io.Discard, defaultOutputLimits); err == nil || !isTreeError(err) {
		t.Errorf("missing output: %v", err)
	}
}
