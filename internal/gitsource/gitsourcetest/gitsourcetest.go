// Package gitsourcetest runs Git servers for tests: an SSH server and an
// HTTPS server that serve repositories in a folder with the real
// git-upload-pack. Only tests use it; it needs git installed.
package gitsourcetest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Git runs git in dir and returns its trimmed output.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Repo is a repository with a work tree that tests commit to.
type Repo struct {
	t   testing.TB
	Dir string
}

// NewRepo makes a repository named name (e.g. "acme/site.git") below
// root, with one commit of files on branch main. A file whose content
// starts with "->" becomes a link to the rest.
func NewRepo(t testing.TB, root, name string, files map[string]string) *Repo {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "init", "-q", "-b", "main")
	Git(t, dir, "config", "core.symlinks", "true")
	r := &Repo{t: t, Dir: dir}
	r.Commit("first", files)
	return r
}

// Commit writes files (replacing what is there) and commits them; it
// returns the commit's id.
func (r *Repo) Commit(message string, files map[string]string) string {
	r.t.Helper()
	for name, body := range files {
		p := filepath.Join(r.Dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		os.Remove(p)
		if target, ok := strings.CutPrefix(body, "->"); ok {
			if err := os.Symlink(target, p); err != nil {
				r.t.Fatal(err)
			}
			continue
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			r.t.Fatal(err)
		}
	}
	Git(r.t, r.Dir, "add", "-A")
	Git(r.t, r.Dir, "commit", "-q", "--allow-empty", "-m", message)
	return r.Head("main")
}

// Head is the commit a branch points at.
func (r *Repo) Head(branch string) string {
	r.t.Helper()
	return Git(r.t, r.Dir, "rev-parse", "refs/heads/"+branch)
}

// SSH is an SSH server that runs git-upload-pack for the repositories
// below Root, for clients with an authorized key.
type SSH struct {
	Root    string
	Addr    string // 127.0.0.1:port
	HostKey ssh.Signer
	ln      net.Listener

	mu         sync.Mutex
	authorized map[string]bool
	// Commands are the commands clients ran.
	Commands []string
	// Users are the users clients signed in as.
	Users []string
}

// NewSSH starts an SSH server for the repositories below root.
func NewSSH(t testing.TB, root string) *SSH {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hk, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &SSH{Root: root, Addr: ln.Addr().String(), HostKey: hk, ln: ln, authorized: map[string]bool{}}
	t.Cleanup(func() { ln.Close() })
	go s.serve()
	return s
}

// SetHostKey makes the server show another key from now on.
func (s *SSH) SetHostKey(k ssh.Signer) {
	s.mu.Lock()
	s.HostKey = k
	s.mu.Unlock()
}

// Authorize lets a key sign in.
func (s *SSH) Authorize(k ssh.PublicKey) {
	s.mu.Lock()
	s.authorized[string(k.Marshal())] = true
	s.mu.Unlock()
}

// Revoke stops a key from signing in.
func (s *SSH) Revoke(k ssh.PublicKey) {
	s.mu.Lock()
	delete(s.authorized, string(k.Marshal()))
	s.mu.Unlock()
}

// Address is the ssh:// address of a repository below Root.
func (s *SSH) Address(name string) string {
	return "ssh://git@" + s.Addr + "/" + name
}

func (s *SSH) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *SSH) handle(c net.Conn) {
	defer c.Close()
	s.mu.Lock()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.authorized[string(k.Marshal())] {
				s.Users = append(s.Users, meta.User())
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unknown key")
		},
	}
	cfg.AddHostKey(s.HostKey)
	s.mu.Unlock()
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.session(ch, chReqs)
	}
}

func (s *SSH) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		if len(req.Payload) < 4 {
			req.Reply(false, nil)
			return
		}
		n := binary.BigEndian.Uint32(req.Payload)
		cmd := string(req.Payload[4 : 4+n])
		s.mu.Lock()
		s.Commands = append(s.Commands, cmd)
		s.mu.Unlock()
		req.Reply(true, nil)
		status := s.run(ch, cmd)
		ch.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, uint32(status)))
		return
	}
}

func (s *SSH) run(ch ssh.Channel, cmd string) int {
	path, ok := strings.CutPrefix(cmd, "git-upload-pack '")
	if !ok || !strings.HasSuffix(path, "'") {
		fmt.Fprintf(ch.Stderr(), "unknown command\n")
		return 1
	}
	path = strings.TrimPrefix(strings.TrimSuffix(path, "'"), "/")
	dir := filepath.Join(s.Root, filepath.FromSlash(path))
	if !strings.HasPrefix(dir, s.Root+string(filepath.Separator)) {
		fmt.Fprintf(ch.Stderr(), "no\n")
		return 1
	}
	if _, err := os.Stat(dir); err != nil {
		fmt.Fprintf(ch.Stderr(), "ERROR: Repository not found.\n")
		return 1
	}
	c := exec.Command("git", "upload-pack", "--strict", gitDir(dir))
	c.Stdout, c.Stderr = ch, ch.Stderr()
	in, err := c.StdinPipe()
	if err != nil {
		return 1
	}
	if err := c.Start(); err != nil {
		return 1
	}
	// Not waited for: the client may keep its side open.
	go func() {
		io.Copy(in, ch)
		in.Close()
	}()
	if err := c.Wait(); err != nil {
		return 1
	}
	return 0
}

// HTTPS is an HTTPS server that serves the repositories below Root with
// Git's smart protocol.
type HTTPS struct {
	*httptest.Server
	Root string
}

// NewHTTPS starts an HTTPS server for the repositories below root.
func NewHTTPS(t testing.TB, root string) *HTTPS {
	t.Helper()
	h := &HTTPS{Root: root}
	h.Server = httptest.NewTLSServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.Close)
	return h
}

// Address is the https:// address of a repository below Root.
func (h *HTTPS) Address(name string) string {
	return h.URL + "/" + name
}

func (h *HTTPS) serve(w http.ResponseWriter, r *http.Request) {
	var repo string
	var advertise bool
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/info/refs") && r.URL.Query().Get("service") == "git-upload-pack":
		repo, advertise = strings.TrimSuffix(r.URL.Path, "/info/refs"), true
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-upload-pack"):
		repo = strings.TrimSuffix(r.URL.Path, "/git-upload-pack")
	default:
		http.NotFound(w, r)
		return
	}
	dir := filepath.Join(h.Root, filepath.FromSlash(strings.TrimPrefix(repo, "/")))
	if !strings.HasPrefix(dir, h.Root+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(dir); err != nil {
		http.NotFound(w, r)
		return
	}
	args := []string{"upload-pack", "--stateless-rpc", "--strict"}
	if advertise {
		args = append(args, "--advertise-refs")
	}
	c := exec.Command("git", append(args, gitDir(dir))...)
	c.Stdin = r.Body
	var out bytes.Buffer
	c.Stdout = &out
	if err := c.Run(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if advertise {
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		w.Write([]byte("001e# service=git-upload-pack\n0000"))
	} else {
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	}
	w.Write(out.Bytes())
}

// gitDir is the repository's .git folder when it has a work tree.
func gitDir(dir string) string {
	if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
		return filepath.Join(dir, ".git")
	}
	return dir
}
