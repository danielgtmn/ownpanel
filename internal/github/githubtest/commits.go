package githubtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

// Commits: a repository's branches point at commits whose files the fake
// serves as GitHub does, as a tar.gz of the commit behind a redirect to a
// one-time download address (GET /repos/{owner}/{repo}/tarball/{sha}).
// Statuses posted to a commit are recorded.

// File is a file of a commit. Mode 0 means 0o644.
type File struct {
	Body string
	Mode int64
	// Link makes it a symbolic link to Link instead.
	Link string
}

// Status is a commit status the fake received.
type Status struct {
	RepoID      int64
	SHA         string
	State       string
	TargetURL   string
	Description string
	Context     string
	// Token is the installation token it was posted with.
	Token string
}

type commit struct {
	repoID int64
	files  map[string]File
}

// SetFiles records a new commit with files on a branch of a repository
// (adding the branch if needed) and returns its id.
func (s *Server) SetFiles(repoID int64, branch string, files map[string]string) string {
	fs := map[string]File{}
	for k, v := range files {
		fs[k] = File{Body: v}
	}
	return s.SetTree(repoID, branch, fs)
}

// SetTree is SetFiles with modes and links.
func (s *Server) SetTree(repoID int64, branch string, files map[string]File) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := sha1.New()
	fmt.Fprintf(h, "%d %s %d\n", repoID, branch, s.id())
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(h, "%s %d %s %s\n", n, files[n].Mode, files[n].Link, files[n].Body)
	}
	sha := hex.EncodeToString(h.Sum(nil))
	if s.commits == nil {
		s.commits = map[string]*commit{}
		s.heads = map[int64]map[string]string{}
	}
	s.commits[sha] = &commit{repoID: repoID, files: files}
	if s.heads[repoID] == nil {
		s.heads[repoID] = map[string]string{}
	}
	s.heads[repoID][branch] = sha
	for i := range s.repos {
		if s.repos[i].ID == repoID && !slices.Contains(s.repos[i].Branches, branch) {
			s.repos[i].Branches = append(s.repos[i].Branches, branch)
		}
	}
	return sha
}

// Head is the commit a branch points at ("" if none was set with SetFiles).
func (s *Server) Head(repoID int64, branch string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heads[repoID][branch]
}

// Statuses lists the commit statuses received, oldest first.
func (s *Server) Statuses() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.statuses)
}

// TarballDelay delays every download by d (to test time limits).
func (s *Server) TarballDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tarballDelay = d
}

// branchSHA is the commit of a branch: the one set with SetFiles, else a
// made-up one derived from its name. Call with s.mu held.
func (s *Server) branchSHA(repoID int64, name string) string {
	if sha := s.heads[repoID][name]; sha != "" {
		return sha
	}
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:20])
}

func (s *Server) tarball(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.repoOfToken(w, r)
	if rp == nil {
		return
	}
	sha := r.PathValue("ref")
	c, ok := s.commits[sha]
	// The commits of a pull request (also from a fork) can be fetched
	// from the repository it was opened against, as on GitHub.
	if !ok || c.repoID != rp.ID && s.pullSHAs[sha] != rp.ID {
		message(w, http.StatusNotFound, "Not Found")
		return
	}
	code := secret(16)
	if s.downloads == nil {
		s.downloads = map[string]string{}
	}
	s.downloads[code] = sha
	prefix := fmt.Sprintf("%s-%s-%s", rp.Owner, rp.Name, sha[:7])
	http.Redirect(w, r, s.webURL()+"/_codeload/"+prefix+".tar.gz?token="+code, http.StatusFound)
}

// codeload serves a download once, without any Authorization header (it
// refuses one, so a client that forwards the token fails).
func (s *Server) codeload(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "" {
		message(w, http.StatusBadRequest, "the download address takes no credentials")
		return
	}
	s.mu.Lock()
	sha, ok := s.downloads[r.URL.Query().Get("token")]
	delete(s.downloads, r.URL.Query().Get("token"))
	var c *commit
	if ok {
		c = s.commits[sha]
	}
	delay := s.tarballDelay
	s.mu.Unlock()
	if c == nil {
		http.NotFound(w, r)
		return
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	prefix := strings.TrimSuffix(r.PathValue("file"), ".tar.gz")
	w.Header().Set("Content-Type", "application/x-gzip")
	w.Write(TarGz(prefix, c.files))
}

// TarGz is a tar.gz of files, every name under prefix/ (none if "").
func TarGz(prefix string, files map[string]File) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	p := func(n string) string {
		if prefix == "" {
			return n
		}
		return prefix + "/" + n
	}
	if prefix != "" {
		tw.WriteHeader(&tar.Header{Name: prefix + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	}
	dirs := map[string]bool{}
	for _, n := range names {
		parts := strings.Split(n, "/")
		for i := 1; i < len(parts); i++ {
			d := strings.Join(parts[:i], "/")
			if !dirs[d] {
				dirs[d] = true
				tw.WriteHeader(&tar.Header{Name: p(d) + "/", Typeflag: tar.TypeDir, Mode: 0o755})
			}
		}
		f := files[n]
		if f.Link != "" {
			tw.WriteHeader(&tar.Header{Name: p(n), Typeflag: tar.TypeSymlink, Linkname: f.Link, Mode: 0o777})
			continue
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		tw.WriteHeader(&tar.Header{Name: p(n), Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(f.Body))})
		tw.Write([]byte(f.Body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func (s *Server) postStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	t, ok := s.tokens[raw]
	if !ok || !s.Now().Before(t.ExpiresAt) {
		message(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	var rp *Repo
	for k := range s.repos {
		if strings.EqualFold(s.repos[k].Owner, r.PathValue("owner")) && strings.EqualFold(s.repos[k].Name, r.PathValue("repo")) {
			rp = &s.repos[k]
		}
	}
	i := s.insts[t.InstallationID]
	if rp == nil || i == nil || !slices.ContainsFunc(s.reposOf(i), func(x Repo) bool { return x.ID == rp.ID }) ||
		t.RepositoryIDs != nil && !slices.Contains(t.RepositoryIDs, rp.ID) {
		message(w, http.StatusNotFound, "Not Found")
		return
	}
	if levels[t.Permissions["statuses"]] < 2 {
		message(w, http.StatusForbidden, "Resource not accessible by integration")
		return
	}
	var body struct {
		State       string `json:"state"`
		TargetURL   string `json:"target_url"`
		Description string `json:"description"`
		Context     string `json:"context"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		message(w, http.StatusBadRequest, "Problems parsing JSON")
		return
	}
	if !slices.Contains([]string{"pending", "success", "failure", "error"}, body.State) || len([]rune(body.Description)) > 140 {
		message(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	s.statuses = append(s.statuses, Status{RepoID: rp.ID, SHA: r.PathValue("sha"), State: body.State, TargetURL: body.TargetURL,
		Description: body.Description, Context: body.Context, Token: raw})
	writeJSON(w, http.StatusCreated, map[string]any{"state": body.State, "context": body.Context})
}

// devCommit (fakegithub only): POST /_dev/commit?repo=owner/name&branch=main
// with a tar.gz of a folder as the body records it as a new commit on the
// branch and answers its id. Push it with /_dev/push afterwards.
func (s *Server) devCommit(w http.ResponseWriter, r *http.Request) {
	owner, name, _ := strings.Cut(r.URL.Query().Get("repo"), "/")
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "main"
	}
	var repoID int64
	s.mu.Lock()
	for _, rp := range s.repos {
		if rp.Owner == owner && rp.Name == name {
			repoID = rp.ID
		}
	}
	s.mu.Unlock()
	if repoID == 0 {
		message(w, http.StatusNotFound, "no such repository")
		return
	}
	files, err := readTarGz(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		message(w, http.StatusBadRequest, err.Error())
		return
	}
	fmt.Fprintln(w, s.SetTree(repoID, branch, files))
}

func readTarGz(r io.Reader) (map[string]File, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	out := map[string]File{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		n := strings.TrimPrefix(h.Name, "./")
		if n == "" || strings.HasSuffix(n, "/") {
			continue
		}
		switch h.Typeflag {
		case tar.TypeReg:
			b, err := io.ReadAll(io.LimitReader(tr, 16<<20))
			if err != nil {
				return nil, err
			}
			out[n] = File{Body: string(b), Mode: h.Mode & 0o777}
		case tar.TypeSymlink:
			out[n] = File{Link: h.Linkname}
		}
	}
}
