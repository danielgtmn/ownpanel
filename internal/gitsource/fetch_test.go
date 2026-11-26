package gitsource

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/danielgtmn/ownpanel/internal/gitsource/gitsourcetest"
)

func TestMain(m *testing.M) {
	allowPrivate = true
	os.Exit(m.Run())
}

func newKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type tarEntry struct {
	typ  byte
	mode int64
	body string
	link string
}

func readTree(t *testing.T, f Fetched) map[string]tarEntry {
	t.Helper()
	defer f.Tree.Close()
	gz, err := gzip.NewReader(f.Tree)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]tarEntry{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" {
			t.Errorf("%s has an owner", h.Name)
		}
		out[h.Name] = tarEntry{typ: h.Typeflag, mode: h.Mode, body: string(body), link: h.Linkname}
	}
	return out
}

func sshWorld(t *testing.T, files map[string]string) (*gitsourcetest.SSH, *gitsourcetest.Repo, Remote) {
	t.Helper()
	root := t.TempDir()
	repo := gitsourcetest.NewRepo(t, root, "acme/site.git", files)
	srv := gitsourcetest.NewSSH(t, root)
	key := newKey(t)
	srv.Authorize(key.PublicKey())
	a, err := ParseAddress(srv.Address("acme/site.git"))
	if err != nil {
		t.Fatal(err)
	}
	return srv, repo, Remote{Address: a, Key: key, HostKey: srv.HostKey.PublicKey()}
}

var siteFiles = map[string]string{
	"index.html":         "<h1>hello</h1>",
	"assets/app.js":      "console.log(1)",
	"assets/deep/x.css":  "body{}",
	"build.sh":           "#!/bin/sh\necho build",
	"docs/readme.md":     "->../index.html",
	"current":            "->assets",
	"assets/deep/up.css": "->../app.js",
}

func TestFetchOverSSH(t *testing.T) {
	srv, repo, remote := sshWorld(t, siteFiles)
	ctx := context.Background()

	// The host key as scanning shows it.
	k, err := ScanHostKey(ctx, remote.Address)
	if err != nil || ssh.FingerprintSHA256(k) != ssh.FingerprintSHA256(srv.HostKey.PublicKey()) {
		t.Fatalf("scan: %v %v", k, err)
	}

	tip, err := BranchTip(ctx, remote, "main")
	if err != nil || tip != repo.Head("main") {
		t.Fatalf("tip %q, %v", tip, err)
	}
	f, err := Fetch(ctx, remote, "main", tip, t.TempDir(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if f.Commit != tip {
		t.Fatalf("commit %s", f.Commit)
	}
	tree := readTree(t, f)
	want := map[string]tarEntry{
		"index.html":         {typ: tar.TypeReg, mode: 0o644, body: "<h1>hello</h1>"},
		"assets/":            {typ: tar.TypeDir, mode: 0o755},
		"assets/app.js":      {typ: tar.TypeReg, mode: 0o644, body: "console.log(1)"},
		"assets/deep/":       {typ: tar.TypeDir, mode: 0o755},
		"assets/deep/x.css":  {typ: tar.TypeReg, mode: 0o644, body: "body{}"},
		"assets/deep/up.css": {typ: tar.TypeSymlink, mode: 0o777, link: "../app.js"},
		"build.sh":           {typ: tar.TypeReg, mode: 0o755, body: "#!/bin/sh\necho build"},
		"docs/":              {typ: tar.TypeDir, mode: 0o755},
		"docs/readme.md":     {typ: tar.TypeSymlink, mode: 0o777, link: "../index.html"},
		"current":            {typ: tar.TypeSymlink, mode: 0o777, link: "assets"},
	}
	if len(tree) != len(want) {
		t.Errorf("entries: %v", tree)
	}
	for name, w := range want {
		if tree[name] != w {
			t.Errorf("%s: got %+v, want %+v", name, tree[name], w)
		}
	}
	// The temporary file is gone once read.
	if _, err := os.Stat(f.Tree.(*tempFile).Name()); !os.IsNotExist(err) {
		t.Errorf("temporary file left: %v", err)
	}
	for _, c := range srv.Commands {
		if c != "git-upload-pack '/acme/site.git'" {
			t.Errorf("command %q", c)
		}
	}
	for _, u := range srv.Users {
		if u != "git" {
			t.Errorf("user %q", u)
		}
	}

	// A newer push: the old commit is not fetched any more.
	newer := repo.Commit("second", map[string]string{"index.html": "<h1>new</h1>"})
	_, err = Fetch(ctx, remote, "main", tip, t.TempDir(), Limits{})
	var moved *BranchMovedError
	if !errors.As(err, &moved) || moved.Have != newer {
		t.Fatalf("moved: %v", err)
	}
	f, err = Fetch(ctx, remote, "main", "", t.TempDir(), Limits{})
	if err != nil || f.Commit != newer || readTree(t, f)["index.html"].body != "<h1>new</h1>" {
		t.Fatalf("newer: %v", err)
	}

	_, err = BranchTip(ctx, remote, "nope")
	var missing *BranchMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("missing branch: %v", err)
	}

	// The scp-like form reaches the same repository (relative path).
	a, err := ParseAddress("git@" + srv.Addr[:strings.LastIndexByte(srv.Addr, ':')] + ":acme/site.git")
	if err != nil {
		t.Fatal(err)
	}
	a.Port = remote.Address.Port
	if _, err := BranchTip(ctx, Remote{Address: a, Key: remote.Key, HostKey: remote.HostKey}, "main"); err != nil {
		t.Fatalf("short form: %v", err)
	}
}

func TestFetchRefusesChangedHostKey(t *testing.T) {
	srv, _, remote := sshWorld(t, siteFiles)
	ctx := context.Background()
	srv.Users = nil
	other := newKey(t)
	srv.SetHostKey(other)
	_, err := Fetch(ctx, remote, "main", "", t.TempDir(), Limits{})
	var changed *HostKeyChangedError
	if !errors.As(err, &changed) || changed.Presented == nil || ssh.FingerprintSHA256(changed.Presented) != ssh.FingerprintSHA256(other.PublicKey()) {
		t.Fatalf("changed key: %v", err)
	}
	if !strings.Contains(err.Error(), ssh.FingerprintSHA256(other.PublicKey())) {
		t.Fatalf("message: %v", err)
	}
	if len(srv.Users) != 0 {
		t.Fatalf("signed in although the host key was wrong: %v", srv.Users)
	}
	// A key of another type than the pinned one.
	_, rsaPriv := rsaKey(t)
	srv.SetHostKey(rsaPriv)
	_, err = BranchTip(ctx, remote, "main")
	if !errors.As(err, &changed) {
		t.Fatalf("other type: %v", err)
	}
	// Without a pinned key nothing is fetched.
	remote.HostKey = nil
	if _, err := BranchTip(ctx, remote, "main"); err == nil {
		t.Fatal("fetched without a pinned key")
	}
}

func TestFetchNeedsTheDeployKey(t *testing.T) {
	srv, _, remote := sshWorld(t, siteFiles)
	srv.Revoke(remote.Key.PublicKey())
	_, err := BranchTip(context.Background(), remote, "main")
	var auth *AuthError
	if !errors.As(err, &auth) {
		t.Fatalf("got %v", err)
	}
	// A repository that does not exist: the server's message is shown.
	srv.Authorize(remote.Key.PublicKey())
	remote.Address.Path = "/acme/nothing.git"
	_, err = BranchTip(context.Background(), remote, "main")
	var se *ServerError
	if !errors.As(err, &se) || !strings.Contains(se.Msg, "Repository not found") {
		t.Fatalf("got %v", err)
	}
}

func TestFetchOverHTTPS(t *testing.T) {
	root := t.TempDir()
	repo := gitsourcetest.NewRepo(t, root, "acme/site.git", siteFiles)
	srv := gitsourcetest.NewHTTPS(t, root)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	UseRoots(pool)
	defer UseRoots(nil)
	a, err := ParseAddress(srv.Address("acme/site.git"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := Fetch(context.Background(), Remote{Address: a}, "main", repo.Head("main"), t.TempDir(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if tree := readTree(t, f); tree["assets/deep/x.css"].body != "body{}" || tree["build.sh"].mode != 0o755 {
		t.Fatalf("tree: %v", tree)
	}
	a.Path = "/acme/none.git"
	_, err = BranchTip(context.Background(), Remote{Address: a}, "main")
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 404 {
		t.Fatalf("missing: %v", err)
	}
}

func TestFetchRefusesLinksOutOfTheTree(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"absolute":            {"a": "->/etc/passwd"},
		"up":                  {"a": "->../outside"},
		"up from a folder":    {"x/a": "->../../outside"},
		"through a link":      {"up": "->x/..", "x/y": "y", "b": "->up/.."},
		"through a link loop": {"l1": "->l2", "l2": "->l1"},
		"link to a link up":   {"sub/up": "->..", "x": "->sub/up/.."},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, remote := sshWorld(t, files)
			_, err := Fetch(context.Background(), remote, "main", "", t.TempDir(), Limits{})
			var te *TreeError
			if !errors.As(err, &te) || !strings.Contains(te.Msg, "outside the repository") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestFetchLimits(t *testing.T) {
	files := map[string]string{}
	for i := range 30 {
		files[fmt.Sprintf("dir/f%02d.txt", i)] = strings.Repeat(fmt.Sprintf("line %d of a file\n", i), 200)
	}
	files["big.bin"] = strings.Repeat("x", 300_000)
	_, _, remote := sshWorld(t, files)
	ctx := context.Background()
	for name, c := range map[string]struct {
		lim  Limits
		want string
	}{
		"files":    {Limits{Files: 10}, "more than 10 files"},
		"bytes":    {Limits{FilesBytes: 100_000}, "larger than"},
		"object":   {Limits{ObjectBytes: 100_000}, "larger than"},
		"transfer": {Limits{TransferBytes: 1000}, "to download"},
		"objects":  {Limits{Objects: 5}, "more than 5 objects"},
		"depth":    {Limits{Depth: 0, PathLength: 5}, "longer than 5"},
	} {
		_, err := Fetch(ctx, remote, "main", "", t.TempDir(), c.lim)
		var le *LimitError
		if !errors.As(err, &le) || !strings.Contains(le.Msg, c.want) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	// Within the limits, every file comes out intact (the pack has
	// deltas between the similar files).
	f, err := Fetch(ctx, remote, "main", "", t.TempDir(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	tree := readTree(t, f)
	for name, body := range files {
		if tree[name].body != body {
			t.Errorf("%s differs", name)
		}
	}
}

func TestFetchOnlyPublicAddresses(t *testing.T) {
	_, _, remote := sshWorld(t, siteFiles)
	allowPrivate = false
	defer func() { allowPrivate = true }()
	_, err := BranchTip(context.Background(), remote, "main")
	var ce *ConnectError
	if !errors.As(err, &ce) || !errors.Is(err, errNotPublic) || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("got %v", err)
	}
	if _, err := ScanHostKey(context.Background(), remote.Address); !errors.Is(err, errNotPublic) {
		t.Fatalf("scan: %v", err)
	}
}

func TestApplyDelta(t *testing.T) {
	base := []byte("hello world, hello git")
	// result = "hello git" + "!" : copy 13..22, insert "!"
	delta := []byte{22, 10, 0x80 | 0x01 | 0x10, 13, 9, 1, '!'}
	out, err := applyDelta(base, delta, 100)
	if err != nil || string(out) != "hello git!" {
		t.Fatalf("%q %v", out, err)
	}
	for _, bad := range [][]byte{
		{21, 10},                            // wrong base size
		{22, 10, 0x80 | 0x01 | 0x10, 20, 9}, // copy beyond the base
		{22, 3, 5, 'a', 'b'},                // insert beyond the data
		{22, 2, 0},                          // reserved instruction
		{22, 5, 1, 'a'},                     // result too short
	} {
		if _, err := applyDelta(base, bad, 100); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if _, err := applyDelta(base, []byte{22, 0x80, 0x80, 0x01}, 1000); err == nil {
		t.Error("accepted a result larger than the limit")
	}
}

func rsaKey(t *testing.T) (ssh.PublicKey, ssh.Signer) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s.PublicKey(), s
}

func TestReadUntilPackIsBounded(t *testing.T) {
	var b bytes.Buffer
	for range 5000 {
		writePkt(&b, "shallow "+strings.Repeat("a", 40)+"\n")
	}
	writePkt(&b, "NAK\n")
	if err := readUntilPack(newPktReader(&b)); err == nil {
		t.Fatal("read endless lines before the pack")
	}
	b.Reset()
	writePkt(&b, "shallow "+strings.Repeat("a", 40)+"\n")
	writeFlush(&b)
	writePkt(&b, "NAK\n")
	if err := readUntilPack(newPktReader(&b)); err != nil {
		t.Fatal(err)
	}
}
