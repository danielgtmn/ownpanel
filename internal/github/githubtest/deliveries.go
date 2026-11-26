package githubtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/github"
)

// Deliveries as GitHub sends them to an App's webhook: the payloads below
// carry the fields GitHub documents (and a few of the many others), and
// Headers signs a body with the App's webhook secret like GitHub does.

// Push describes a push delivery.
type Push struct {
	RepoID         int64
	InstallationID int64
	// Ref is the full ref, e.g. refs/heads/main or refs/tags/v1.
	Ref string
	// After is the commit the ref points to now; "" makes one up.
	After   string
	Deleted bool
	// Message, AuthorName, AuthorLogin of the head commit; Pusher is the
	// login of who pushed.
	Message     string
	AuthorName  string
	AuthorLogin string
	Pusher      string
	// PushedAt is when it was pushed; zero means the fake's now.
	PushedAt time.Time
}

func (s *Server) repoJSON(id int64, pushedAt any) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.repos {
		if r.ID == id {
			m := s.repoMap(r)
			if pushedAt != nil {
				m["pushed_at"] = pushedAt
			}
			return m
		}
	}
	return map[string]any{"id": id, "name": "unknown", "full_name": "someone/unknown"}
}

func (s *Server) repoMap(r Repo) map[string]any {
	return map[string]any{
		"id": r.ID, "node_id": "R_" + fmt.Sprint(r.ID), "name": r.Name, "full_name": r.Owner + "/" + r.Name,
		"private": r.Private, "owner": map[string]any{"login": r.Owner, "type": "Organization"},
		"html_url": s.webURL() + "/" + r.Owner + "/" + r.Name, "default_branch": r.DefaultBranch,
		"created_at": 1557933565,
	}
}

func (s *Server) installationRef(id int64) map[string]any {
	return map[string]any{"id": id, "node_id": "I_" + fmt.Sprint(id)}
}

// PushPayload is the body of a push delivery.
func (s *Server) PushPayload(p Push) []byte {
	if p.After == "" {
		p.After = secret(20)
	}
	at := p.PushedAt
	if at.IsZero() {
		at = s.Now()
	}
	before := strings.Repeat("0", 40)
	var head any
	commits := []any{}
	if !p.Deleted {
		c := map[string]any{
			"id": p.After, "tree_id": secret(20), "distinct": true, "message": p.Message,
			"timestamp": at.Format(time.RFC3339),
			"author":    map[string]any{"name": p.AuthorName, "email": p.AuthorLogin + "@users.noreply.github.com", "username": p.AuthorLogin},
			"committer": map[string]any{"name": "GitHub", "email": "noreply@github.com", "username": "web-flow"},
			"added":     []string{}, "removed": []string{}, "modified": []string{"index.html"},
		}
		head, commits = c, []any{c}
	} else {
		before, p.After = p.After, strings.Repeat("0", 40)
	}
	body := map[string]any{
		"ref": p.Ref, "before": before, "after": p.After, "created": false, "deleted": p.Deleted, "forced": false,
		"base_ref": nil, "compare": s.webURL() + "/compare", "commits": commits, "head_commit": head,
		"repository":   s.repoJSON(p.RepoID, at.Unix()),
		"pusher":       map[string]any{"name": p.Pusher, "email": p.Pusher + "@users.noreply.github.com"},
		"sender":       map[string]any{"login": p.Pusher, "type": "User"},
		"installation": s.installationRef(p.InstallationID),
	}
	b, _ := json.Marshal(body)
	return b
}

// InstallationPayload is the body of an installation delivery (action
// created, deleted, suspend, unsuspend, …).
func (s *Server) InstallationPayload(action string, installationID int64) []byte {
	s.mu.Lock()
	inst := map[string]any{"id": installationID}
	if i, ok := s.insts[installationID]; ok {
		inst = s.installationJSON(i)
	}
	s.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"action": action, "installation": inst, "sender": map[string]any{"login": s.User}})
	return b
}

// InstallationReposPayload is the body of an installation_repositories
// delivery (action added or removed).
func (s *Server) InstallationReposPayload(action string, installationID int64, repoIDs []int64) []byte {
	list := []any{}
	for _, id := range repoIDs {
		r := s.repoJSON(id, nil)
		list = append(list, map[string]any{"id": r["id"], "node_id": r["node_id"], "name": r["name"], "full_name": r["full_name"], "private": r["private"]})
	}
	added, removed := []any{}, []any{}
	if action == "added" {
		added = list
	} else {
		removed = list
	}
	b, _ := json.Marshal(map[string]any{
		"action": action, "installation": s.installationRef(installationID), "repository_selection": "selected",
		"repositories_added": added, "repositories_removed": removed, "requester": nil,
		"sender": map[string]any{"login": s.User},
	})
	return b
}

// RepositoryPayload is the body of a repository delivery (renamed,
// transferred, deleted, …) for the repository as the fake has it now.
func (s *Server) RepositoryPayload(action string, repoID int64, installationID int64) []byte {
	body := map[string]any{
		"action": action, "repository": s.repoJSON(repoID, nil), "installation": s.installationRef(installationID),
		"sender": map[string]any{"login": s.User},
	}
	if action == "renamed" {
		body["changes"] = map[string]any{"repository": map[string]any{"name": map[string]any{"from": "old-name"}}}
	}
	b, _ := json.Marshal(body)
	return b
}

// WebhookSecret returns the webhook secret of an App ("" if unknown).
func (s *Server) WebhookSecret(appID int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.apps[appID]; ok {
		return a.WebhookSecret
	}
	return ""
}

// Headers are the headers GitHub sends with a delivery of the App, signed
// with its webhook secret.
func (s *Server) Headers(appID int64, event, deliveryID string, body []byte) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "GitHub-Hookshot/fake")
	h.Set(github.HeaderDelivery, deliveryID)
	h.Set(github.HeaderEvent, event)
	h.Set("X-GitHub-Hook-ID", "1")
	h.Set(github.HeaderTargetID, fmt.Sprint(appID))
	h.Set(github.HeaderTargetType, github.TargetTypeApp)
	h.Set(github.HeaderSignature, github.Sign([]byte(s.WebhookSecret(appID)), body))
	return h
}

// Deliver sends a delivery to url as GitHub would.
func (s *Server) Deliver(url string, appID int64, event, deliveryID string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = s.Headers(appID, event, deliveryID, body)
	return http.DefaultClient.Do(req)
}

// DevDelivery is for clicking through in development (fakegithub -dev):
// GET /_dev/push?repo=owner/name&branch=main&message=…&author=… answers
// with the body of a push delivery to the first installation that sees
// the repository, and the headers GitHub would send with it as
// X-Dev-<header> (X-Dev-X-Hub-Signature-256, …), so it can be sent to the
// panel with curl. Never enable it outside development.
func (s *Server) devPush(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	owner, name, _ := strings.Cut(q.Get("repo"), "/")
	s.mu.Lock()
	var repoID, instID, appID int64
	for _, rp := range s.repos {
		if rp.Owner == owner && rp.Name == name {
			repoID = rp.ID
		}
	}
	for _, i := range s.insts {
		if repoID != 0 && slices.ContainsFunc(s.reposOf(i), func(rp Repo) bool { return rp.ID == repoID }) {
			instID, appID = i.ID, i.AppID
			break
		}
	}
	s.mu.Unlock()
	if instID == 0 {
		message(w, http.StatusNotFound, "no installation sees that repository")
		return
	}
	branch := q.Get("branch")
	if branch == "" {
		branch = "main"
	}
	author := q.Get("author")
	if author == "" {
		author = s.User
	}
	body := s.PushPayload(Push{RepoID: repoID, InstallationID: instID, Ref: "refs/heads/" + branch, After: q.Get("sha"),
		Message: q.Get("message"), AuthorName: author, AuthorLogin: author, Pusher: author})
	id := q.Get("delivery")
	if id == "" {
		id = secret(8) + "-" + secret(4)
	}
	for k, v := range s.Headers(appID, "push", id, body) {
		w.Header()["X-Dev-"+k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}
