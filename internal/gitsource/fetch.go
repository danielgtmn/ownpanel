package gitsource

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Time limits.
const (
	// ScanTimeout bounds fetching a server's host key.
	ScanTimeout = 15 * time.Second
	// CheckTimeout bounds asking a server for its branches.
	CheckTimeout = 45 * time.Second
	// FetchTimeout bounds a whole fetch, download and packing included.
	FetchTimeout = 10 * time.Minute
)

// Limits bound one fetch. DefaultLimits are used where a field is 0.
type Limits struct {
	// TransferBytes: bytes read from the server (the pack and what comes
	// before it).
	TransferBytes int64
	// StoreBytes: all objects unpacked, in the temporary file.
	StoreBytes int64
	// ObjectBytes: one object (a file, a folder listing, a commit).
	ObjectBytes int64
	// Objects in the pack.
	Objects int
	// Files and folders in the commit, their bytes together, how deep
	// folders nest and how long a path may be.
	Files      int
	FilesBytes int64
	Depth      int
	PathLength int
	// TreeBytes: folder listings read while walking the commit, counted
	// every time a listing is named (a folder can be named many times).
	TreeBytes int64
	// LinkParts: parts of one link target; LinkWork: steps of resolving
	// every link through the tree's own links.
	LinkParts int
	LinkWork  int64
}

// DefaultLimits match what the build service accepts as a source tree
// (internal/build: 1 GB, 100,000 entries).
var DefaultLimits = Limits{
	TransferBytes: 1 << 30,
	StoreBytes:    3 << 29, // 1.5 GB
	ObjectBytes:   100 << 20,
	Objects:       300_000,
	Files:         100_000,
	FilesBytes:    1 << 30,
	Depth:         64,
	PathLength:    4096,
	TreeBytes:     128 << 20,
	LinkParts:     256,
	LinkWork:      64 << 20,
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits
	if l.TransferBytes > 0 {
		d.TransferBytes = l.TransferBytes
	}
	if l.StoreBytes > 0 {
		d.StoreBytes = l.StoreBytes
	}
	if l.ObjectBytes > 0 {
		d.ObjectBytes = l.ObjectBytes
	}
	if l.Objects > 0 {
		d.Objects = l.Objects
	}
	if l.Files > 0 {
		d.Files = l.Files
	}
	if l.FilesBytes > 0 {
		d.FilesBytes = l.FilesBytes
	}
	if l.Depth > 0 {
		d.Depth = l.Depth
	}
	if l.PathLength > 0 {
		d.PathLength = l.PathLength
	}
	if l.TreeBytes > 0 {
		d.TreeBytes = l.TreeBytes
	}
	if l.LinkParts > 0 {
		d.LinkParts = l.LinkParts
	}
	if l.LinkWork > 0 {
		d.LinkWork = l.LinkWork
	}
	return d
}

// Remote is a repository and how to reach it.
type Remote struct {
	Address Address
	// Key is the deploy key (SSH only).
	Key ssh.Signer
	// HostKey is the pinned key of the SSH server.
	HostKey ssh.PublicKey
}

// BranchMissingError: the repository has no such branch.
type BranchMissingError struct{ Branch string }

func (e *BranchMissingError) Error() string {
	return fmt.Sprintf("the repository has no branch %q", e.Branch)
}

// BranchMovedError: the branch points at another commit than the one
// asked for (a newer push, or an old notification sent again). Only the
// commit a branch points at now is ever fetched.
type BranchMovedError struct {
	Branch, Want, Have string
}

func (e *BranchMovedError) Error() string {
	return fmt.Sprintf("the branch %s now points at commit %.7s, not %.7s", e.Branch, e.Have, e.Want)
}

// conversation is an open connection that has read the advertisement.
type conversation struct {
	ad    advert
	fetch func(req []byte) (pack io.Reader, done func(), err error)
	close func()
}

// openLimits bound a conversation: the advertisement's bytes, and every
// raw byte read from the server after it (side-band progress and empty
// packets included).
type openLimits struct {
	branch      string // "" for every branch (up to MaxBranches)
	advertBytes int
	transfer    int64
}

// rawLimit counts the raw bytes read from a server and refuses more than
// max.
type rawLimit struct {
	r   io.Reader
	n   int64
	max int64
}

func (l *rawLimit) Read(b []byte) (int, error) {
	if l.n >= l.max {
		return 0, limitf("the repository is larger than %s to download", byteSize(l.max))
	}
	if int64(len(b)) > l.max-l.n {
		b = b[:l.max-l.n]
	}
	n, err := l.r.Read(b)
	l.n += int64(n)
	return n, err
}

func open(ctx context.Context, r Remote, ol openLimits) (*conversation, error) {
	a := r.Address
	switch a.Scheme {
	case SchemeSSH:
		if r.Key == nil {
			return nil, errors.New("no deploy key")
		}
		s, err := openSSH(ctx, a, r.Key, r.HostKey)
		if err != nil {
			return nil, err
		}
		raw := &rawLimit{r: s.stdout, max: int64(ol.advertBytes) + 64<<10}
		p := newPktReader(raw)
		ad, err := readAdvert(p, ol.branch, ol.advertBytes)
		if err != nil {
			s.Close()
			return nil, s.failure(err)
		}
		return &conversation{
			ad: ad,
			fetch: func(req []byte) (io.Reader, func(), error) {
				if _, err := s.stdin.Write(req); err != nil {
					return nil, nil, s.failure(err)
				}
				// From here on everything the server sends counts against
				// the transfer limit.
				raw.max = raw.n + ol.transfer
				if err := readUntilPack(p); err != nil {
					return nil, nil, s.failure(err)
				}
				return &failingReader{r: packStream(p, ad), fail: s.failure}, func() {}, nil
			},
			close: func() {
				s.stdin.Close()
				s.Close()
			},
		}, nil
	case SchemeHTTPS:
		c := httpClient()
		ad, err := httpAdvert(ctx, c, a, ol)
		if err != nil {
			c.CloseIdleConnections()
			return nil, err
		}
		return &conversation{
			ad: ad,
			fetch: func(req []byte) (io.Reader, func(), error) {
				body, pack, err := httpPack(ctx, c, a, req, ad, ol.transfer)
				if err != nil {
					return nil, nil, err
				}
				return pack, func() { body.Close() }, nil
			},
			close: c.CloseIdleConnections,
		}, nil
	}
	return nil, errors.New("unknown kind of address")
}

type failingReader struct {
	r    io.Reader
	fail func(error) error
}

func (f *failingReader) Read(b []byte) (int, error) {
	n, err := f.r.Read(b)
	if err != nil && err != io.EOF {
		err = f.fail(err)
	}
	return n, err
}

// BranchTip returns the commit a branch points at now. For SSH it needs
// the deploy key, so it also tells whether the key was added.
func BranchTip(ctx context.Context, r Remote, branch string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	c, err := open(ctx, r, openLimits{branch: branch, advertBytes: checkAdvertBytes})
	if err != nil {
		return "", timeoutErr(ctx, r.Address, err)
	}
	defer c.close()
	tip, ok := c.ad.refs["refs/heads/"+branch]
	if !ok {
		return "", &BranchMissingError{Branch: branch}
	}
	return tip, nil
}

// Fetched is a fetched commit. Tree is a tar.gz of its files, with names
// relative to the repository's root (strip 0); closing it removes the
// temporary file it is read from.
type Fetched struct {
	Commit string
	Tree   io.ReadCloser
	Size   int64
}

// Fetch fetches the commit the branch points at, and checks that it is
// want when want is not "". dir is where temporary files go ("" for the
// system's).
func Fetch(ctx context.Context, r Remote, branch, want, dir string, lim Limits) (Fetched, error) {
	lim = lim.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	c, err := open(ctx, r, openLimits{branch: branch, advertBytes: maxAdvertBytes, transfer: lim.TransferBytes})
	if err != nil {
		return Fetched{}, timeoutErr(ctx, r.Address, err)
	}
	defer c.close()
	tip, ok := c.ad.refs["refs/heads/"+branch]
	if !ok {
		return Fetched{}, &BranchMissingError{Branch: branch}
	}
	if want != "" && want != tip {
		return Fetched{}, &BranchMovedError{Branch: branch, Want: want, Have: tip}
	}
	pack, done, err := c.fetch(fetchRequest(c.ad, tip))
	if err != nil {
		return Fetched{}, timeoutErr(ctx, r.Address, err)
	}
	store, err := readPack(pack, dir, lim)
	done()
	if err != nil {
		return Fetched{}, timeoutErr(ctx, r.Address, err)
	}
	defer store.close()
	root, err := commitTree(store, tip)
	if err != nil {
		return Fetched{}, err
	}
	entries, err := walk(ctx, store, root, lim)
	if err != nil {
		return Fetched{}, timeoutErr(ctx, r.Address, err)
	}
	links, err := checkLinks(ctx, store, entries, lim)
	if err != nil {
		return Fetched{}, timeoutErr(ctx, r.Address, err)
	}
	out, err := os.CreateTemp(dir, "source-*.tar.gz")
	if err != nil {
		return Fetched{}, err
	}
	fail := func(err error) (Fetched, error) {
		out.Close()
		os.Remove(out.Name())
		return Fetched{}, err
	}
	if err := writeTar(ctx, store, entries, links, out); err != nil {
		return fail(timeoutErr(ctx, r.Address, err))
	}
	size, err := out.Seek(0, io.SeekCurrent)
	if err != nil {
		return fail(err)
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return Fetched{Commit: tip, Tree: &tempFile{File: out}, Size: size}, nil
}

type tempFile struct{ *os.File }

func (t *tempFile) Close() error {
	err := t.File.Close()
	os.Remove(t.Name())
	return err
}

func timeoutErr(ctx context.Context, a Address, err error) error {
	if ctx.Err() != nil && !strings.Contains(err.Error(), "did not answer in time") {
		var le *LimitError
		if !errors.As(err, &le) {
			return &ConnectError{Server: a.ServerName(), Err: context.DeadlineExceeded}
		}
	}
	return err
}

func newBufReader(r io.Reader) *bufio.Reader { return bufio.NewReaderSize(r, 64<<10) }

// MaxBranches is how many branch names Branches returns at most.
const MaxBranches = 1000

// Branches is what a repository announces: its branches (sorted, at most
// MaxBranches), the default branch if the server names it, and whether
// there were more.
type Branches struct {
	Names     []string
	Default   string
	Truncated bool
}

// ListBranches asks a repository for its branches. For SSH it needs the
// deploy key, so it also tells whether the key was added.
func ListBranches(ctx context.Context, r Remote) (Branches, error) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	c, err := open(ctx, r, openLimits{advertBytes: checkAdvertBytes})
	if err != nil {
		return Branches{}, timeoutErr(ctx, r.Address, err)
	}
	defer c.close()
	out := Branches{Truncated: c.ad.truncated}
	for name := range c.ad.refs {
		if b, ok := strings.CutPrefix(name, "refs/heads/"); ok {
			out.Names = append(out.Names, b)
		}
	}
	slices.Sort(out.Names)
	// symref=HEAD:refs/heads/main (readAdvert keeps only HEAD's).
	if v, ok := c.ad.caps["symref"]; ok {
		if b, ok := strings.CutPrefix(v, "HEAD:refs/heads/"); ok {
			out.Default = b
		}
	}
	return out, nil
}
