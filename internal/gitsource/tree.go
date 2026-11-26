package gitsource

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

// Writing a commit's files as tar.gz: names are checked the way Git checks
// them (no empty names, ".", "..", "/", NUL or .git in any case), the same
// name twice in a folder fails, and so do links that point out of the
// tree, also when followed through other links. Submodules (gitlinks) are
// left out: the panel never fetches other repositories.

// TreeError says why a commit's files cannot be used. Its text is meant
// for people.
type TreeError struct{ Msg string }

func (e *TreeError) Error() string { return e.Msg }

func treef(format string, args ...any) error {
	return &TreeError{Msg: strings.TrimSpace(fmt.Sprintf(format, args...))}
}

// Git's file modes.
const (
	modeTree    = 0o040000
	modeFile    = 0o100644
	modeExec    = 0o100755
	modeLink    = 0o120000
	modeGitlink = 0o160000
)

type entry struct {
	path   string
	mode   int
	object *object
}

// commitTree reads the tree id of a commit.
func commitTree(s *packStore, commit string) (*object, error) {
	c, ok := s.get(commit)
	if !ok || c.typ != objCommit {
		return nil, protocolf("the server did not send the commit")
	}
	if c.size > 1<<20 {
		return nil, protocolf("the commit is too large")
	}
	data, err := s.data(c)
	if err != nil {
		return nil, err
	}
	line, _, _ := bytes.Cut(data, []byte("\n"))
	id, ok := bytes.CutPrefix(line, []byte("tree "))
	if !ok || !validOID(string(id)) {
		return nil, protocolf("the commit has no tree")
	}
	t, ok := s.get(string(id))
	if !ok || t.typ != objTree {
		return nil, protocolf("the server did not send the commit's files")
	}
	return t, nil
}

// walk lists every entry below the root tree, folders before their
// contents, within the limits.
//
// Every tree read is charged to lim.TreeBytes, also when the same tree is
// named many times (a small pack can name one large tree from thousands
// of folders), and submodules count as entries like everything else.
func walk(ctx context.Context, s *packStore, root *object, lim Limits) ([]entry, error) {
	var out []entry
	var total, treeBytes int64
	entries := 0
	type frame struct {
		dir   string
		tree  *object
		depth int
	}
	stack := []frame{{tree: root}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if f.depth > lim.Depth {
			return nil, limitf("the repository's folders are nested deeper than %d levels", lim.Depth)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if treeBytes += f.tree.size; treeBytes > lim.TreeBytes {
			return nil, limitf("the repository's folder listings are larger than %s together", byteSize(lim.TreeBytes))
		}
		data, err := s.data(f.tree)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		var subdirs []frame
		for len(data) > 0 {
			sp := bytes.IndexByte(data, ' ')
			if sp <= 0 || sp > 6 {
				return nil, protocolf("bad tree")
			}
			mode, ok := parseMode(data[:sp])
			if !ok {
				return nil, protocolf("bad tree")
			}
			data = data[sp+1:]
			nul := bytes.IndexByte(data, 0)
			if nul < 0 || len(data) < nul+21 {
				return nil, protocolf("bad tree")
			}
			name := string(data[:nul])
			id := hex.EncodeToString(data[nul+1 : nul+21])
			data = data[nul+21:]
			if err := checkName(name); err != nil {
				return nil, err
			}
			full := name
			if f.dir != "" {
				full = f.dir + "/" + name
			}
			if len(full) > lim.PathLength {
				return nil, limitf("a path in the repository is longer than %d characters", lim.PathLength)
			}
			key := strings.ToLower(name)
			if seen[key] {
				return nil, treef("the folder %q contains %q twice (or in different upper and lower case)", f.dir+"/", name)
			}
			seen[key] = true
			if entries++; entries > lim.Files {
				return nil, limitf("the repository has more than %d files and folders", lim.Files)
			}
			if mode == modeGitlink {
				continue // a submodule: never fetched
			}
			o, ok := s.get(id)
			if !ok {
				return nil, protocolf("the server did not send all of the commit's files")
			}
			switch mode {
			case modeTree:
				if o.typ != objTree {
					return nil, protocolf("bad tree")
				}
				out = append(out, entry{path: full, mode: mode})
				subdirs = append(subdirs, frame{dir: full, tree: o, depth: f.depth + 1})
			case modeFile, modeExec, modeLink:
				if o.typ != objBlob {
					return nil, protocolf("bad tree")
				}
				total += o.size
				if total > lim.FilesBytes {
					return nil, limitf("the repository's files are larger than %s together", byteSize(lim.FilesBytes))
				}
				out = append(out, entry{path: full, mode: mode, object: o})
			default:
				return nil, protocolf("bad file mode")
			}
		}
		for i := len(subdirs) - 1; i >= 0; i-- {
			stack = append(stack, subdirs[i])
		}
	}
	return out, nil
}

func parseMode(b []byte) (int, bool) {
	m := 0
	for _, c := range b {
		if c < '0' || c > '7' {
			return 0, false
		}
		m = m*8 + int(c-'0')
	}
	switch m {
	case modeTree, modeFile, modeExec, modeLink, modeGitlink:
		return m, true
	case 0o100664, 0o100600, 0o100640: // old Git versions wrote these
		return modeFile, true
	}
	return 0, false
}

// checkName refuses names Git itself refuses in trees (fsck), and .git in
// any case.
func checkName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return treef("the repository contains an empty name, \".\" or \"..\"")
	case strings.ContainsAny(name, "/\x00"):
		return treef("the repository contains a name with \"/\" or a zero byte")
	case strings.EqualFold(strings.TrimRight(name, ". "), ".git"):
		return treef("the repository contains a file or folder named .git")
	case len(name) > 255:
		return treef("the repository contains a name longer than 255 bytes")
	}
	for i := 0; i < len(name); i++ {
		if name[i] < 0x20 || name[i] == 0x7f {
			return treef("the repository contains a name with a control character")
		}
	}
	return nil
}

// checkLinks makes sure no link points out of the tree: absolute targets,
// and targets that leave the tree when resolved through the tree's own
// folders and links, are refused. Targets that do not exist are fine.
//
// The work is bounded: a target has at most lim.LinkParts parts, the
// distinct targets together at most linkTargetBytes, and resolving all
// links at most lim.LinkWork steps (a step is one part, plus the bytes of
// the path looked up), however the links lead through each other.
func checkLinks(ctx context.Context, s *packStore, entries []entry, lim Limits) (map[string]string, error) {
	targets := map[string]string{}
	read := map[*object]string{} // a blob named by many links is read once
	var distinct int64
	for _, e := range entries {
		if e.mode != modeLink {
			continue
		}
		t, ok := read[e.object]
		if !ok {
			if e.object.size > 4096 {
				return nil, treef("the link %q is too long", e.path)
			}
			if distinct += e.object.size; distinct > linkTargetBytes {
				return nil, limitf("the repository's links are longer than %s together", byteSize(linkTargetBytes))
			}
			data, err := s.data(e.object)
			if err != nil {
				return nil, err
			}
			t = string(data)
			if t == "" || strings.ContainsRune(t, 0) {
				return nil, treef("the link %q is empty or contains a zero byte", e.path)
			}
			if strings.Count(t, "/") >= lim.LinkParts {
				return nil, limitf("the link %q has more than %d parts", e.path, lim.LinkParts)
			}
			read[e.object] = t
		}
		targets[e.path] = t
	}
	r := linkResolver{links: targets, budget: lim.LinkWork, ctx: ctx}
	// Links in the same folder with the same target resolve the same way.
	done := map[[2]string]bool{}
	for p, t := range targets {
		if strings.HasPrefix(t, "/") {
			return nil, treef("the link %q points to %q, outside the repository", p, t)
		}
		key := [2]string{path.Dir(p), t}
		inside, ok := done[key]
		if !ok {
			var err error
			if inside, err = r.inside(key[0], t); err != nil {
				return nil, err
			}
			done[key] = inside
		}
		if !inside {
			return nil, treef("the link %q points to %q, outside the repository", p, t)
		}
	}
	return targets, nil
}

// linkTargetBytes bounds the distinct link targets of a fetch.
const linkTargetBytes = 16 << 20

// maxLinkHops is how many links one resolution may follow (Linux's
// limit); more is treated as a loop and refused.
const maxLinkHops = 40

// linkResolver resolves link targets through the tree's own links, with a
// shared budget of work.
type linkResolver struct {
	links  map[string]string
	budget int64
	steps  int
	ctx    context.Context
}

var errLinkBudget = &LimitError{Msg: "the repository's links lead through each other too often to be checked"}

// inside resolves target relative to dir ("." for the root) and reports
// whether it stays inside the tree. The path walked so far is kept as a
// string and a stack of lengths, so each part costs one lookup of the
// current path; the parts still to walk are a stack, so following a link
// pushes its target's parts instead of joining strings.
func (r *linkResolver) inside(dir, target string) (bool, error) {
	cur := ""         // the path so far, without a leading "/"
	var lens []int    // len(cur) before each part was added
	var rest []string // parts still to walk, last first
	push := func(t string) {
		parts := strings.Split(t, "/")
		for i := len(parts) - 1; i >= 0; i-- {
			rest = append(rest, parts[i])
		}
	}
	setDir := func(d string) {
		cur, lens = "", lens[:0]
		if d == "." || d == "" {
			return
		}
		for _, part := range strings.Split(d, "/") {
			lens = append(lens, len(cur))
			if cur != "" {
				cur += "/"
			}
			cur += part
		}
	}
	setDir(dir)
	push(target)
	hops := 0
	for len(rest) > 0 {
		part := rest[len(rest)-1]
		rest = rest[:len(rest)-1]
		if r.steps++; r.steps%4096 == 0 {
			if err := r.ctx.Err(); err != nil {
				return false, err
			}
		}
		switch part {
		case "", ".":
			r.budget--
			continue
		case "..":
			r.budget--
			if len(lens) == 0 {
				return false, nil
			}
			cur, lens = cur[:lens[len(lens)-1]], lens[:len(lens)-1]
			continue
		}
		lens = append(lens, len(cur))
		if cur != "" {
			cur += "/"
		}
		cur += part
		if r.budget -= int64(1 + len(cur)); r.budget < 0 {
			return false, errLinkBudget
		}
		if t, ok := r.links[cur]; ok {
			if strings.HasPrefix(t, "/") {
				return false, nil
			}
			if hops++; hops > maxLinkHops {
				return false, nil // a loop, or too deep: refuse rather than guess
			}
			// Go on from the link's folder with its target.
			cur, lens = cur[:lens[len(lens)-1]], lens[:len(lens)-1]
			push(t)
		}
	}
	return true, nil
}

// writeTar writes the entries as a tar.gz (names relative to the tree,
// folders 0755, files 0644 or 0755, links as links, no owners, the
// commit's time unknown so every time is the same fixed one).
func writeTar(ctx context.Context, s *packStore, entries []entry, links map[string]string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	when := time.Unix(0, 0).UTC()
	for i, e := range entries {
		if i%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		h := &tar.Header{Name: e.path, ModTime: when, Format: tar.FormatPAX}
		switch e.mode {
		case modeTree:
			h.Typeflag, h.Name, h.Mode = tar.TypeDir, e.path+"/", 0o755
		case modeLink:
			h.Typeflag, h.Linkname, h.Mode = tar.TypeSymlink, links[e.path], 0o777
		case modeExec:
			h.Typeflag, h.Mode, h.Size = tar.TypeReg, 0o755, e.object.size
		default:
			h.Typeflag, h.Mode, h.Size = tar.TypeReg, 0o644, e.object.size
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, s.section(e.object)); err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
