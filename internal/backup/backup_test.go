package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/store"
	"github.com/danielgtmn/panzerschrank"
)

func TestCreate(t *testing.T) {
	data := t.TempDir()
	st, err := store.Open(filepath.Join(data, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close() // stays open: backups run next to the daemon
	if _, err := st.CreateSite(context.Background(), "blog", []string{"blog.example.com"}, 0); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(data, "sites", "blog", "releases", "r1")
	os.MkdirAll(rel, 0o755)
	os.WriteFile(filepath.Join(rel, "index.html"), []byte("hi"), 0o644)
	os.Symlink(filepath.Join("releases", "r1"), filepath.Join(data, "sites", "blog", "current"))
	os.MkdirAll(filepath.Join(data, "caddy", "certificates"), 0o700)
	os.WriteFile(filepath.Join(data, "caddy", "certificates", "x.crt"), []byte("cert"), 0o600)
	os.WriteFile(filepath.Join(data, "setup-code"), []byte("secret"), 0o600)
	key, err := panzerschrank.LoadOrCreateKey(filepath.Join(data, panzerschrank.KeyFile))
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := Create(data, out); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o600 {
		t.Fatalf("archive mode %v", fi.Mode().Perm())
	}
	if err := Create(data, out); err == nil {
		t.Fatal("an existing archive must not be overwritten")
	}

	restore := t.TempDir()
	f, _ := os.Open(out)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[hdr.Name] = true
		dst := filepath.Join(restore, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(dst, 0o755)
		case tar.TypeSymlink:
			os.Symlink(hdr.Linkname, dst)
		case tar.TypeReg:
			b, _ := io.ReadAll(tr)
			os.WriteFile(dst, b, 0o600)
		}
	}
	for _, want := range []string{DBFile, panzerschrank.KeyFile, "sites/blog/current", "sites/blog/releases/r1/index.html", "caddy/certificates/x.crt"} {
		if !seen[want] {
			t.Errorf("archive misses %s (has %v)", want, seen)
		}
	}
	if seen["setup-code"] {
		t.Error("the setup code must not be backed up")
	}
	if b, err := os.ReadFile(filepath.Join(restore, "sites", "blog", "current", "index.html")); err != nil || string(b) != "hi" {
		t.Fatalf("release link: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(restore, panzerschrank.KeyFile)); err != nil || !bytes.Equal(b, key) {
		t.Fatalf("secret key not restored: %v", err)
	}
	restored, err := store.Open(filepath.Join(restore, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if site, err := restored.Site(context.Background(), "blog"); err != nil || site.Domains[0] != "blog.example.com" {
		t.Fatalf("restored database: %+v %v", site, err)
	}
}
