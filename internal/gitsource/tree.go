package gitsource

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
func walk(s *packStore, root *object, lim Limits) ([]entry, error) {
	var out []entry
	var total int64
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
			if mode == modeGitlink {
				continue // a submodule: never fetched
			}
			o, ok := s.get(id)
			if !ok {
				return nil, protocolf("the server did not send all of the commit's files")
			}
			if len(out) >= lim.Files {
				return nil, limitf("the repository has more than %d files and folders", lim.Files)
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
	return nil
}

// checkLinks makes sure no link points out of the tree: absolute targets,
// and targets that leave the tree when resolved through the tree's own
// folders and links, are refused. Targets that do not exist are fine.
func checkLinks(s *packStore, entries []entry) (map[string]string, error) {
	kinds := make(map[string]int, len(entries))
	targets := map[string]string{}
	for _, e := range entries {
		kinds[e.path] = e.mode
		if e.mode == modeLink {
			if e.object.size > 4096 {
				return nil, treef("the link %q is too long", e.path)
			}
			data, err := s.data(e.object)
			if err != nil {
				return nil, err
			}
			t := string(data)
			if t == "" || strings.ContainsRune(t, 0) {
				return nil, treef("the link %q is empty or contains a zero byte", e.path)
			}
			targets[e.path] = t
		}
	}
	for p, t := range targets {
		if strings.HasPrefix(t, "/") {
			return nil, treef("the link %q points to %q, outside the repository", p, t)
		}
		if !insideTree(path.Dir(p), t, targets, 0) {
			return nil, treef("the link %q points to %q, outside the repository", p, t)
		}
	}
	return targets, nil
}

// insideTree resolves target relative to dir ("." for the root) through
// the tree's links and reports whether it stays inside.
func insideTree(dir, target string, links map[string]string, hops int) bool {
	if hops > 40 {
		return false // a loop; refuse rather than guess
	}
	var cur []string
	if dir != "." && dir != "" {
		cur = strings.Split(dir, "/")
	}
	parts := strings.Split(target, "/")
	for i, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(cur) == 0 {
				return false
			}
			cur = cur[:len(cur)-1]
			continue
		}
		cur = append(cur, part)
		joined := strings.Join(cur, "/")
		if t, ok := links[joined]; ok {
			if strings.HasPrefix(t, "/") {
				return false
			}
			// Resolve the link, then go on from where it leads.
			rest := strings.Join(parts[i+1:], "/")
			next := t
			if rest != "" {
				next = t + "/" + rest
			}
			return insideTree(path.Dir(joined), next, links, hops+1)
		}
	}
	return true
}

// writeTar writes the entries as a tar.gz (names relative to the tree,
// folders 0755, files 0644 or 0755, links as links, no owners, the
// commit's time unknown so every time is the same fixed one).
func writeTar(s *packStore, entries []entry, links map[string]string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	when := time.Unix(0, 0).UTC()
	for _, e := range entries {
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
