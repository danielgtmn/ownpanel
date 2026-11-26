package githubtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Pull requests: a repository's pull requests with their head commit (from
// a branch of the same repository or of a fork), pull_request deliveries,
// and their comments (GET, POST and PATCH as on GitHub, with the
// pull_requests permission). Comments written with an installation token
// belong to the App's bot, "<slug>[bot]".

// Pull is a pull request of the fake.
type Pull struct {
	RepoID int64
	Number int
	// State is "open" or "closed"; Merged with "closed".
	State  string
	Merged bool
	Title  string
	User   string
	// BaseRef is the branch it is against; HeadRef the branch it comes
	// from, of HeadRepoID (0: the same repository).
	BaseRef    string
	HeadRef    string
	HeadRepoID int64
	HeadSHA    string
	UpdatedAt  time.Time
}

// Comment is a comment on a pull request.
type Comment struct {
	ID       int64
	RepoID   int64
	Number   int
	User     string
	UserType string
	AppID    int64
	Body     string
	// Edits counts how often it was changed.
	Edits int
}

// OpenPull opens a pull request against base of repoID from the head
// commit sha (a commit of SetFiles, of this repository or of a fork,
// headRepoID 0 meaning this one) and returns its number.
func (s *Server) OpenPull(repoID int64, base, headRef string, headRepoID int64, sha, title, user string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pulls == nil {
		s.pulls = map[int64]map[int]*Pull{}
		s.pullSHAs = map[string]int64{}
	}
	if s.pulls[repoID] == nil {
		s.pulls[repoID] = map[int]*Pull{}
	}
	n := len(s.pulls[repoID]) + 1
	s.pulls[repoID][n] = &Pull{RepoID: repoID, Number: n, State: "open", Title: title, User: user, BaseRef: base,
		HeadRef: headRef, HeadRepoID: headRepoID, HeadSHA: sha, UpdatedAt: s.Now()}
	s.pullSHAs[sha] = repoID
	return n
}

// PushToPull moves the head of a pull request to the commit sha.
func (s *Server) PushToPull(repoID int64, number int, sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pulls[repoID][number]; p != nil {
		p.HeadSHA, p.UpdatedAt = sha, s.Now()
		s.pullSHAs[sha] = repoID
	}
}

// ClosePull closes a pull request, merged or not.
func (s *Server) ClosePull(repoID int64, number int, merged bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pulls[repoID][number]; p != nil {
		p.State, p.Merged, p.UpdatedAt = "closed", merged, s.Now()
	}
}

// SetPrivate makes a repository private or public.
func (s *Server) SetPrivate(repoID int64, private bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.repos {
		if s.repos[i].ID == repoID {
			s.repos[i].Private = private
		}
	}
}

// Comments lists the comments of a pull request, oldest first.
func (s *Server) Comments(repoID int64, number int) []Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Comment
	for _, c := range s.comments {
		if c.RepoID == repoID && c.Number == number {
			out = append(out, *c)
		}
	}
	return out
}

// AddComment writes a comment as a person (login), as if on GitHub.
func (s *Server) AddComment(repoID int64, number int, login, body string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &Comment{ID: s.id(), RepoID: repoID, Number: number, User: login, UserType: "User", Body: body}
	s.comments = append(s.comments, c)
	return c.ID
}

// pullJSON is a pull request as GitHub describes it. Call with s.mu held.
func (s *Server) pullJSON(p *Pull) map[string]any {
	repo := func(id int64) map[string]any {
		for _, r := range s.repos {
			if r.ID == id {
				m := s.repoMap(r)
				m["fork"] = id != p.RepoID
				return m
			}
		}
		return nil
	}
	headID := p.HeadRepoID
	if headID == 0 {
		headID = p.RepoID
	}
	head, base := repo(headID), repo(p.RepoID)
	label := func(r map[string]any, ref string) string {
		if r == nil {
			return ref
		}
		return fmt.Sprint(r["owner"].(map[string]any)["login"]) + ":" + ref
	}
	m := map[string]any{
		"id": int64(p.Number) * 1000003, "node_id": "PR_" + fmt.Sprint(p.Number), "number": p.Number, "state": p.State,
		"locked": false, "title": p.Title, "body": "Please look at this.", "draft": false, "merged": p.Merged,
		"user":       map[string]any{"login": p.User, "type": "User"},
		"html_url":   fmt.Sprintf("%s/%s/pull/%d", s.webURL(), base["full_name"], p.Number),
		"created_at": p.UpdatedAt.UTC().Format(time.RFC3339), "updated_at": p.UpdatedAt.UTC().Format(time.RFC3339),
		"head": map[string]any{"label": label(head, p.HeadRef), "ref": p.HeadRef, "sha": p.HeadSHA, "repo": head,
			"user": map[string]any{"login": p.User}},
		"base": map[string]any{"label": label(base, p.BaseRef), "ref": p.BaseRef, "sha": s.branchSHA(p.RepoID, p.BaseRef),
			"repo": base, "user": base["owner"]},
	}
	if p.State == "closed" {
		m["closed_at"] = p.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return m
}

// PullPayload is the body of a pull_request delivery (action opened,
// synchronize, reopened, closed, …) for the pull request as it is now.
func (s *Server) PullPayload(action string, repoID int64, number int, installationID int64) []byte {
	s.mu.Lock()
	p := s.pulls[repoID][number]
	var pull map[string]any
	if p != nil {
		pull = s.pullJSON(p)
	}
	s.mu.Unlock()
	body := map[string]any{
		"action": action, "number": number, "pull_request": pull, "repository": s.repoJSON(repoID, nil),
		"installation": s.installationRef(installationID), "sender": map[string]any{"login": s.User, "type": "User"},
	}
	if action == "synchronize" && p != nil {
		body["before"], body["after"] = strings.Repeat("1", 40), p.HeadSHA
	}
	b, _ := json.Marshal(body)
	return b
}

// pullOf finds the pull request of a request, for a token with perm at
// level on its repository. Call with s.mu held.
func (s *Server) pullOf(w http.ResponseWriter, r *http.Request, level int) *Pull {
	rp := s.repoOfTokenFor(w, r, "pull_requests", level)
	if rp == nil {
		return nil
	}
	n, err := strconv.Atoi(r.PathValue("number"))
	p := s.pulls[rp.ID][n]
	if err != nil || p == nil {
		message(w, http.StatusNotFound, "Not Found")
		return nil
	}
	return p
}

func (s *Server) getPull(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pullOf(w, r, 1); p != nil {
		writeJSON(w, http.StatusOK, s.pullJSON(p))
	}
}

func (s *Server) commentJSON(c *Comment) map[string]any {
	m := map[string]any{
		"id": c.ID, "node_id": "IC_" + fmt.Sprint(c.ID), "body": c.Body,
		"user":     map[string]any{"login": c.User, "type": c.UserType},
		"html_url": fmt.Sprintf("%s/pull/%d#issuecomment-%d", s.webURL(), c.Number, c.ID),
	}
	if c.AppID != 0 {
		m["performed_via_github_app"] = map[string]any{"id": c.AppID, "slug": strings.TrimSuffix(c.User, "[bot]")}
	} else {
		m["performed_via_github_app"] = nil
	}
	return m
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pullOf(w, r, 1)
	if p == nil {
		return
	}
	all := []map[string]any{}
	for _, c := range s.comments {
		if c.RepoID == p.RepoID && c.Number == p.Number {
			all = append(all, s.commentJSON(c))
		}
	}
	writeJSON(w, http.StatusOK, page(all, r))
}

// bot is the App bot a token writes as. Call with s.mu held.
func (s *Server) bot(r *http.Request) (string, int64) {
	raw, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if t := s.tokens[raw]; t != nil {
		if i := s.insts[t.InstallationID]; i != nil {
			if a := s.apps[i.AppID]; a != nil {
				return a.Slug + "[bot]", a.ID
			}
		}
	}
	return "", 0
}

func readCommentBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		message(w, http.StatusBadRequest, "Problems parsing JSON")
		return "", false
	}
	if body.Body == "" || len([]rune(body.Body)) > 65536 {
		message(w, http.StatusUnprocessableEntity, "Validation Failed")
		return "", false
	}
	return body.Body, true
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pullOf(w, r, 2)
	if p == nil {
		return
	}
	body, ok := readCommentBody(w, r)
	if !ok {
		return
	}
	login, appID := s.bot(r)
	c := &Comment{ID: s.id(), RepoID: p.RepoID, Number: p.Number, User: login, UserType: "Bot", AppID: appID, Body: body}
	s.comments = append(s.comments, c)
	writeJSON(w, http.StatusCreated, s.commentJSON(c))
}

func (s *Server) updateComment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.repoOfTokenFor(w, r, "pull_requests", 2)
	if rp == nil {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	i := slices.IndexFunc(s.comments, func(c *Comment) bool { return c.ID == id && c.RepoID == rp.ID })
	if i < 0 {
		message(w, http.StatusNotFound, "Not Found")
		return
	}
	c := s.comments[i]
	if login, _ := s.bot(r); c.User != login {
		// An App may only edit its own comments.
		message(w, http.StatusForbidden, "Resource not accessible by integration")
		return
	}
	body, ok := readCommentBody(w, r)
	if !ok {
		return
	}
	c.Body = body
	c.Edits++
	writeJSON(w, http.StatusOK, s.commentJSON(c))
}

// dev serves h only with Dev set.
func (s *Server) dev(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		dev := s.Dev
		s.mu.Unlock()
		if !dev {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}
}

// devRepo finds a repository by owner/name and the first installation
// that sees it.
func (s *Server) devRepo(full string) (repoID, instID, appID int64) {
	owner, name, _ := strings.Cut(full, "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rp := range s.repos {
		if rp.Owner == owner && rp.Name == name {
			repoID = rp.ID
		}
	}
	for _, i := range s.insts {
		if repoID != 0 && slices.ContainsFunc(s.reposOf(i), func(rp Repo) bool { return rp.ID == repoID }) {
			return repoID, i.ID, i.AppID
		}
	}
	return repoID, 0, 0
}

// devPull (fakegithub only): GET /_dev/pull_request?repo=owner/name&action=…
// changes a pull request as on GitHub and answers with the signed
// delivery GitHub would send, like /_dev/push:
//
//   - action=opened&base=main&head=feature (&fork=someone/name): opens a
//     pull request from the newest commit of head (of the fork, if given);
//   - action=synchronize&number=N: moves it to the newest commit of its
//     head branch;
//   - action=closed&number=N (&merged=1), action=reopened&number=N.
func (s *Server) devPull(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repoID, instID, appID := s.devRepo(q.Get("repo"))
	if instID == 0 {
		message(w, http.StatusNotFound, "no installation sees that repository")
		return
	}
	n, _ := strconv.Atoi(q.Get("number"))
	action := q.Get("action")
	switch action {
	case "opened":
		headRepo := repoID
		var forkID int64
		if f := q.Get("fork"); f != "" {
			forkID, _, _ = s.devRepo(f)
			if forkID == 0 {
				message(w, http.StatusNotFound, "no such fork")
				return
			}
			headRepo = forkID
		}
		sha := s.Head(headRepo, q.Get("head"))
		if sha == "" {
			message(w, http.StatusNotFound, "the head branch has no commit; post one to /_dev/commit")
			return
		}
		n = s.OpenPull(repoID, orDefault(q.Get("base"), "main"), q.Get("head"), forkID, sha, orDefault(q.Get("title"), "A change"), s.User)
	case "synchronize":
		s.mu.Lock()
		p := s.pulls[repoID][n]
		s.mu.Unlock()
		if p == nil {
			message(w, http.StatusNotFound, "no such pull request")
			return
		}
		headRepo := p.HeadRepoID
		if headRepo == 0 {
			headRepo = repoID
		}
		s.PushToPull(repoID, n, s.Head(headRepo, p.HeadRef))
	case "closed":
		s.ClosePull(repoID, n, q.Get("merged") != "")
	case "reopened":
		s.mu.Lock()
		if p := s.pulls[repoID][n]; p != nil {
			p.State, p.Merged = "open", false
		}
		s.mu.Unlock()
	default:
		message(w, http.StatusBadRequest, "unknown action")
		return
	}
	body := s.PullPayload(action, repoID, n, instID)
	for k, v := range s.Headers(appID, "pull_request", secret(8)+"-"+secret(4), body) {
		w.Header()["X-Dev-"+k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// devComments (fakegithub only): GET /_dev/comments?repo=owner/name&number=N
// lists the comments of a pull request.
func (s *Server) devComments(w http.ResponseWriter, r *http.Request) {
	repoID, _, _ := s.devRepo(r.URL.Query().Get("repo"))
	n, _ := strconv.Atoi(r.URL.Query().Get("number"))
	writeJSON(w, http.StatusOK, s.Comments(repoID, n))
}
