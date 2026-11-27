// Package githubtest is a fake of the parts of GitHub the panel uses to
// connect repositories: the manifest form and the installation page (both
// answer at once with a redirect, as if the person had confirmed), the
// manifest conversion, the App's installations, installation tokens and the
// repositories of an installation. It serves the browser pages at / and the
// API at /api/v3, like GitHub Enterprise Server, so a github.Client created
// with the fake's URL talks to it.
//
// The fake checks what GitHub checks: the App's JWT (RS256 with the App's
// key, issuer, lifetime), that an installation belongs to the App, and
// that an installation token is used only for what it was narrowed to. It
// records every JWT and token so tests can look at them.
package githubtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Manifest is the part of an App manifest the fake reads.
type Manifest struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	HookAttributes struct {
		URL    string `json:"url"`
		Active *bool  `json:"active"`
	} `json:"hook_attributes"`
	RedirectURL        string            `json:"redirect_url"`
	SetupURL           string            `json:"setup_url"`
	SetupOnUpdate      bool              `json:"setup_on_update"`
	Description        string            `json:"description"`
	Public             bool              `json:"public"`
	DefaultEvents      []string          `json:"default_events"`
	DefaultPermissions map[string]string `json:"default_permissions"`
}

// App is an App created through the fake.
type App struct {
	ID            int64
	Name          string
	Slug          string
	ClientID      string
	Owner         string
	OwnerType     string
	Manifest      Manifest
	Key           *rsa.PrivateKey
	WebhookSecret string
	ClientSecret  string
	// Deleted makes every request as the App fail, as after deleting it on
	// GitHub.
	Deleted bool
}

// Installation is an App installed on an account.
type Installation struct {
	ID          int64
	AppID       int64
	Account     string
	AccountType string
	// Repos are the ids of the repositories it may see; nil means all of
	// the account's.
	Repos     []int64
	Suspended bool
}

// Repo is a repository of an account.
type Repo struct {
	ID            int64
	Owner         string
	Name          string
	Private       bool
	DefaultBranch string
	// Branches are the names of its branches (DefaultBranch first).
	Branches []string
}

// IssuedToken is an installation token handed out by the fake.
type IssuedToken struct {
	Value          string
	InstallationID int64
	RepositoryIDs  []int64 // nil: all of the installation's
	Permissions    map[string]string
	ExpiresAt      time.Time
}

// JWT is a verified App JWT the fake received.
type JWT struct {
	Raw      string
	Issuer   string
	IssuedAt time.Time
	Expires  time.Time
}

// Server is the fake. Create it with New.
type Server struct {
	*httptest.Server

	// User is the login of the person who is "signed in" at the fake; Apps
	// created without an organisation belong to them.
	User string
	// Now is the fake's clock.
	Now func() time.Time
	// AskToConfirm makes the manifest form answer with a page and a
	// "Create GitHub App" button, as GitHub does, instead of redirecting
	// back at once. A browser needs it: the panel's page only allows its
	// form to go to GitHub, and that rule also covers redirects.
	AskToConfirm bool
	// Dev serves /_dev/push (see devPush). Set it before the first
	// request.
	Dev bool

	mu       sync.Mutex
	nextID   int64
	codes    map[string]int64 // manifest code -> App id, until converted
	apps     map[int64]*App
	insts    map[int64]*Installation
	repos    []Repo
	tokens   map[string]*IssuedToken
	jwts     []JWT
	requests []string
	fail     map[string]int // "METHOD /path" prefix -> status to answer once

	// commits.go
	commits      map[string]*commit
	heads        map[int64]map[string]string // repository -> branch -> commit
	downloads    map[string]string           // one-time download code -> commit
	statuses     []Status
	tarballDelay time.Duration

	// pulls.go
	pulls    map[int64]map[int]*Pull // base repository -> number -> pull request
	pullSHAs map[string]int64        // head commit -> base repository that serves it
	comments []*Comment
}

// New starts a fake on a random local port. Close it when done
// (t.Cleanup(srv.Close)).
func New() *Server {
	s := newServer()
	s.Server = httptest.NewServer(s.handler())
	return s
}

// NewAt starts a fake on addr (host:port), for development.
func NewAt(addr string) (*Server, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := newServer()
	s.Server = httptest.NewUnstartedServer(s.handler())
	s.Server.Listener.Close()
	s.Server.Listener = l
	s.Server.Start()
	return s, nil
}

func newServer() *Server {
	return &Server{
		User:   "octo-admin",
		Now:    time.Now,
		nextID: 1000,
		codes:  map[string]int64{},
		apps:   map[int64]*App{},
		insts:  map[int64]*Installation{},
		tokens: map[string]*IssuedToken{},
		fail:   map[string]int{},
	}
}

func (s *Server) id() int64 {
	s.nextID++
	return s.nextID
}

func secret(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// AddRepo adds a repository of an account and returns its id.
func (s *Server) AddRepo(owner, name string, private bool) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.id()
	s.repos = append(s.repos, Repo{ID: id, Owner: owner, Name: name, Private: private, DefaultBranch: "main", Branches: []string{"main"}})
	return id
}

// AddBranch adds a branch to a repository.
func (s *Server) AddBranch(repoID int64, branch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.repos {
		if s.repos[i].ID == repoID && !slices.Contains(s.repos[i].Branches, branch) {
			s.repos[i].Branches = append(s.repos[i].Branches, branch)
		}
	}
}

// RenameRepo renames a repository; its id stays.
func (s *Server) RenameRepo(repoID int64, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.repos {
		if s.repos[i].ID == repoID {
			s.repos[i].Name = name
		}
	}
}

// SetInstallationRepos changes which repositories an installation may
// see, as choosing them on GitHub does (nil: all of the account's).
func (s *Server) SetInstallationRepos(id int64, repos []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.insts[id]; ok {
		i.Repos = repos
	}
}

// AddInstallation installs an App on an account directly (as if someone
// did it on GitHub). repos nil means all repositories of the account.
func (s *Server) AddInstallation(appID int64, account, accountType string, repos []int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.id()
	s.insts[id] = &Installation{ID: id, AppID: appID, Account: account, AccountType: accountType, Repos: repos}
	return id
}

// RemoveInstallation uninstalls.
func (s *Server) RemoveInstallation(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.insts, id)
}

// App returns a copy of an App created through the fake.
func (s *Server) App(id int64) (App, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.apps[id]
	if !ok {
		return App{}, false
	}
	return *a, true
}

// Apps lists the ids of the Apps created through the fake.
func (s *Server) Apps() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []int64{}
	for id := range s.apps {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// DeleteApp marks an App deleted: its JWTs are refused from now on.
func (s *Server) DeleteApp(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.apps[id]; ok {
		a.Deleted = true
	}
}

// Installations lists the installations of an App.
func (s *Server) Installations(appID int64) []Installation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Installation{}
	for _, i := range s.insts {
		if i.AppID == appID {
			out = append(out, *i)
		}
	}
	slices.SortFunc(out, func(a, b Installation) int { return int(a.ID - b.ID) })
	return out
}

// Tokens lists every installation token handed out.
func (s *Server) Tokens() []IssuedToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []IssuedToken{}
	for _, t := range s.tokens {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b IssuedToken) int { return a.ExpiresAt.Compare(b.ExpiresAt) })
	return out
}

// JWTs lists every verified App JWT received.
func (s *Server) JWTs() []JWT {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.jwts)
}

// Requests lists "METHOD /path" of every request received.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// FailOnce makes the next request whose "METHOD /path" starts with prefix
// answer status.
func (s *Server) FailOnce(prefix string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[prefix] = status
}

// CreateCode creates a pending App from a manifest, as the manifest form
// does, and returns the code the browser would bring back.
func (s *Server) CreateCode(m Manifest, org string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createCode(m, org)
}

func (s *Server) createCode(m Manifest, org string) (string, error) {
	if m.URL == "" || m.HookAttributes.URL == "" {
		return "", fmt.Errorf("url and hook_attributes.url are required")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", err
	}
	id := s.id()
	owner, ownerType := s.User, "User"
	if org != "" {
		owner, ownerType = org, "Organization"
	}
	name := m.Name
	if name == "" {
		name = "app " + strconv.FormatInt(id, 10)
	}
	slug := strings.Trim(strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '-'
	}, name), "-")
	s.apps[id] = &App{
		ID: id, Name: name, Slug: slug, ClientID: "Iv23fake" + strconv.FormatInt(id, 10), Owner: owner, OwnerType: ownerType,
		Manifest: m, Key: key, WebhookSecret: secret(16), ClientSecret: secret(20),
	}
	code := secret(10)
	s.codes[code] = id
	return code, nil
}

// ---- HTTP ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func message(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	// Browser pages.
	mux.HandleFunc("POST /settings/apps/new", func(w http.ResponseWriter, r *http.Request) { s.manifestForm(w, r, "") })
	mux.HandleFunc("POST /organizations/{org}/settings/apps/new", func(w http.ResponseWriter, r *http.Request) {
		s.manifestForm(w, r, r.PathValue("org"))
	})
	mux.HandleFunc("POST /settings/apps/confirm", s.confirmForm)
	mux.HandleFunc("GET /apps/{slug}/installations/new", s.installPage)
	mux.HandleFunc("GET /apps/{slug}", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "App page") })
	// API.
	mux.HandleFunc("POST /api/v3/app-manifests/{code}/conversions", s.convert)
	mux.HandleFunc("GET /api/v3/app/installations", s.listInstallations)
	mux.HandleFunc("GET /api/v3/app/installations/{id}", s.getInstallation)
	mux.HandleFunc("POST /api/v3/app/installations/{id}/access_tokens", s.createToken)
	mux.HandleFunc("GET /api/v3/installation/repositories", s.listRepos)
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/branches", s.listBranches)
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/branches/{branch...}", s.getBranch)
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/tarball/{ref}", s.tarball)
	mux.HandleFunc("POST /api/v3/repos/{owner}/{repo}/statuses/{sha}", s.postStatus)
	mux.HandleFunc("GET /_codeload/{file}", s.codeload)
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/pulls/{number}", s.getPull)
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/issues/{number}/comments", s.listComments)
	mux.HandleFunc("POST /api/v3/repos/{owner}/{repo}/issues/{number}/comments", s.createComment)
	mux.HandleFunc("PATCH /api/v3/repos/{owner}/{repo}/issues/comments/{id}", s.updateComment)
	mux.HandleFunc("GET /_dev/pull_request", s.dev(s.devPull))
	mux.HandleFunc("GET /_dev/comments", s.dev(s.devComments))
	mux.HandleFunc("POST /_dev/commit", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		dev := s.Dev
		s.mu.Unlock()
		if !dev {
			http.NotFound(w, r)
			return
		}
		s.devCommit(w, r)
	})
	mux.HandleFunc("GET /_dev/push", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		dev := s.Dev
		s.mu.Unlock()
		if !dev {
			http.NotFound(w, r)
			return
		}
		s.devPush(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		s.mu.Lock()
		s.requests = append(s.requests, key)
		status := 0
		for prefix, st := range s.fail {
			if strings.HasPrefix(key, prefix) {
				status = st
				delete(s.fail, prefix)
				break
			}
		}
		s.mu.Unlock()
		if status != 0 {
			message(w, status, "injected failure")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) manifestForm(w http.ResponseWriter, r *http.Request, org string) {
	raw := r.PostFormValue("manifest")
	var m Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		http.Error(w, "invalid manifest", http.StatusBadRequest)
		return
	}
	state := r.URL.Query().Get("state")
	if s.AskToConfirm {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="color-scheme" content="light dark"><title>Create GitHub App</title></head>
<body><h1>Create GitHub App (stand-in)</h1><p>App %s for %s</p>
<form method="post" action="/settings/apps/confirm"><input type="hidden" name="manifest" value="%s"><input type="hidden" name="org" value="%s"><input type="hidden" name="state" value="%s">
<button type="submit">Create GitHub App</button></form></body></html>`,
			html.EscapeString(m.Name), html.EscapeString(orDefault(org, s.User)), html.EscapeString(raw), html.EscapeString(org), html.EscapeString(state))
		return
	}
	s.createAndReturn(w, r, m, org, state)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (s *Server) confirmForm(w http.ResponseWriter, r *http.Request) {
	var m Manifest
	if err := json.Unmarshal([]byte(r.PostFormValue("manifest")), &m); err != nil {
		http.Error(w, "invalid manifest", http.StatusBadRequest)
		return
	}
	s.createAndReturn(w, r, m, r.PostFormValue("org"), r.PostFormValue("state"))
}

func (s *Server) createAndReturn(w http.ResponseWriter, r *http.Request, m Manifest, org, state string) {
	s.mu.Lock()
	code, err := s.createCode(m, org)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := url.Values{"code": {code}}
	if state != "" {
		q.Set("state", state)
	}
	http.Redirect(w, r, m.RedirectURL+"?"+q.Encode(), http.StatusFound)
}

func (s *Server) installPage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var app *App
	for _, a := range s.apps {
		if a.Slug == r.PathValue("slug") && !a.Deleted {
			app = a
		}
	}
	if app == nil {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	// A private App can only be installed on its owner; reinstalling
	// updates the existing installation.
	var inst *Installation
	action := "install"
	for _, i := range s.insts {
		if i.AppID == app.ID {
			inst, action = i, "update"
		}
	}
	if inst == nil {
		var repos []int64
		for _, rp := range s.repos {
			if rp.Owner == app.Owner {
				repos = append(repos, rp.ID)
			}
		}
		inst = &Installation{ID: s.id(), AppID: app.ID, Account: app.Owner, AccountType: app.OwnerType, Repos: repos}
		s.insts[inst.ID] = inst
	}
	setup := app.Manifest.SetupURL
	s.mu.Unlock()
	q := url.Values{"installation_id": {strconv.FormatInt(inst.ID, 10)}, "setup_action": {action}}
	if st := r.URL.Query().Get("state"); st != "" {
		q.Set("state", st)
	}
	http.Redirect(w, r, setup+"?"+q.Encode(), http.StatusFound)
}

func (s *Server) webURL() string {
	if s.Server != nil && s.Server.URL != "" {
		return s.Server.URL
	}
	return "http://127.0.0.1"
}

func (s *Server) appJSON(a *App) map[string]any {
	return map[string]any{
		"id": a.ID, "slug": a.Slug, "node_id": "A_" + a.Slug, "client_id": a.ClientID,
		"owner": map[string]any{"login": a.Owner, "type": a.OwnerType, "id": 1},
		"name":  a.Name, "description": a.Manifest.Description, "external_url": a.Manifest.URL,
		"html_url":    s.webURL() + "/apps/" + a.Slug,
		"permissions": a.Manifest.DefaultPermissions, "events": a.Manifest.DefaultEvents,
		"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
	}
}

func (s *Server) convert(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.codes[r.PathValue("code")]
	if !ok {
		message(w, http.StatusNotFound, "Not Found")
		return
	}
	delete(s.codes, r.PathValue("code"))
	a := s.apps[id]
	body := s.appJSON(a)
	body["client_secret"] = a.ClientSecret
	body["webhook_secret"] = a.WebhookSecret
	body["pem"] = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(a.Key)}))
	writeJSON(w, http.StatusCreated, body)
}

// appOf verifies the request's JWT and returns its App.
func (s *Server) appOf(w http.ResponseWriter, r *http.Request) *App {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(raw, ".")
	if !ok || len(parts) != 3 {
		message(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
		return nil
	}
	var header struct{ Alg, Typ string }
	var claims struct {
		IAT int64  `json:"iat"`
		EXP int64  `json:"exp"`
		ISS string `json:"iss"`
	}
	hb, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	cb, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || json.Unmarshal(hb, &header) != nil || json.Unmarshal(cb, &claims) != nil || header.Alg != "RS256" {
		message(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
		return nil
	}
	var app *App
	for _, a := range s.apps {
		if a.ClientID == claims.ISS || strconv.FormatInt(a.ID, 10) == claims.ISS {
			app = a
		}
	}
	if app == nil || app.Deleted {
		message(w, http.StatusUnauthorized, "Integration not found")
		return nil
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&app.Key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		message(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
		return nil
	}
	now := s.Now().Unix()
	if claims.IAT > now+60 || claims.EXP <= now || claims.EXP-claims.IAT > 600 {
		message(w, http.StatusUnauthorized, "'Expiration time' claim ('exp') is too far in the future")
		return nil
	}
	s.jwts = append(s.jwts, JWT{Raw: raw, Issuer: claims.ISS, IssuedAt: time.Unix(claims.IAT, 0), Expires: time.Unix(claims.EXP, 0)})
	return app
}

func (s *Server) installationJSON(i *Installation) map[string]any {
	sel := "selected"
	if i.Repos == nil {
		sel = "all"
	}
	var suspended any
	if i.Suspended {
		suspended = "2026-01-01T00:00:00Z"
	}
	return map[string]any{
		"id": i.ID, "app_id": i.AppID, "target_type": i.AccountType,
		"account":              map[string]any{"login": i.Account, "type": i.AccountType},
		"repository_selection": sel, "suspended_at": suspended,
		"html_url": fmt.Sprintf("%s/settings/installations/%d", s.webURL(), i.ID),
	}
}

func (s *Server) listInstallations(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app := s.appOf(w, r)
	if app == nil {
		return
	}
	out := []map[string]any{}
	ids := []int64{}
	for id, i := range s.insts {
		if i.AppID == app.ID {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		out = append(out, s.installationJSON(s.insts[id]))
	}
	writeJSON(w, http.StatusOK, page(out, r))
}

func page[T any](all []T, r *http.Request) []T {
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per <= 0 || per > 100 {
		per = 30
	}
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p <= 0 {
		p = 1
	}
	from := (p - 1) * per
	if from >= len(all) {
		return []T{}
	}
	return all[from:min(from+per, len(all))]
}

func (s *Server) installationOf(w http.ResponseWriter, r *http.Request, app *App) *Installation {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	i, ok := s.insts[id]
	if !ok || i.AppID != app.ID {
		message(w, http.StatusNotFound, "Not Found")
		return nil
	}
	return i
}

func (s *Server) getInstallation(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app := s.appOf(w, r)
	if app == nil {
		return
	}
	if i := s.installationOf(w, r, app); i != nil {
		writeJSON(w, http.StatusOK, s.installationJSON(i))
	}
}

func (s *Server) reposOf(i *Installation) []Repo {
	out := []Repo{}
	for _, rp := range s.repos {
		if rp.Owner == i.Account && (i.Repos == nil || slices.Contains(i.Repos, rp.ID)) {
			out = append(out, rp)
		}
	}
	return out
}

var levels = map[string]int{"read": 1, "write": 2, "admin": 3}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app := s.appOf(w, r)
	if app == nil {
		return
	}
	i := s.installationOf(w, r, app)
	if i == nil {
		return
	}
	if i.Suspended {
		message(w, http.StatusForbidden, "This installation has been suspended")
		return
	}
	var body struct {
		RepositoryIDs []int64           `json:"repository_ids"`
		Repositories  []string          `json:"repositories"`
		Permissions   map[string]string `json:"permissions"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			message(w, http.StatusBadRequest, "Problems parsing JSON")
			return
		}
	}
	visible := s.reposOf(i)
	for _, id := range body.RepositoryIDs {
		if !slices.ContainsFunc(visible, func(rp Repo) bool { return rp.ID == id }) {
			message(w, http.StatusUnprocessableEntity, "There is at least one repository that does not exist or is not accessible to the parent installation.")
			return
		}
	}
	perms := body.Permissions
	if perms == nil {
		perms = app.Manifest.DefaultPermissions
	}
	for k, v := range perms {
		if levels[v] == 0 || levels[app.Manifest.DefaultPermissions[k]] < levels[v] {
			message(w, http.StatusUnprocessableEntity, "The permissions requested are not granted to this installation.")
			return
		}
	}
	t := &IssuedToken{
		Value: "ghs_fake" + secret(18), InstallationID: i.ID, RepositoryIDs: body.RepositoryIDs,
		Permissions: perms, ExpiresAt: s.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}
	s.tokens[t.Value] = t
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": t.Value, "expires_at": t.ExpiresAt.Format(time.RFC3339), "permissions": perms,
	})
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	t, ok := s.tokens[raw]
	if !ok || !s.Now().Before(t.ExpiresAt) {
		message(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	if levels[t.Permissions["metadata"]] < 1 {
		message(w, http.StatusForbidden, "Resource not accessible by integration")
		return
	}
	i, ok := s.insts[t.InstallationID]
	if !ok {
		message(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	all := []map[string]any{}
	for _, rp := range s.reposOf(i) {
		if t.RepositoryIDs != nil && !slices.Contains(t.RepositoryIDs, rp.ID) {
			continue
		}
		all = append(all, map[string]any{
			"id": rp.ID, "name": rp.Name, "full_name": rp.Owner + "/" + rp.Name,
			"owner":   map[string]any{"login": rp.Owner, "type": i.AccountType},
			"private": rp.Private, "archived": false, "disabled": false, "default_branch": rp.DefaultBranch,
			"html_url": s.webURL() + "/" + rp.Owner + "/" + rp.Name,
		})
	}
	sel := "selected"
	if i.Repos == nil {
		sel = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(all), "repository_selection": sel, "repositories": page(all, r)})
}

// repoOfToken finds the repository of a branch request and checks the
// token may read its contents, as GitHub does (a repository the token may
// not see is "Not Found").
func (s *Server) repoOfToken(w http.ResponseWriter, r *http.Request) *Repo {
	return s.repoOfTokenFor(w, r, "contents", 1)
}

// repoOfTokenFor is repoOfToken for another permission and level (1 read,
// 2 write).
func (s *Server) repoOfTokenFor(w http.ResponseWriter, r *http.Request, perm string, level int) *Repo {
	raw, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	t, ok := s.tokens[raw]
	if !ok || !s.Now().Before(t.ExpiresAt) {
		message(w, http.StatusUnauthorized, "Bad credentials")
		return nil
	}
	i, ok := s.insts[t.InstallationID]
	if !ok {
		message(w, http.StatusUnauthorized, "Bad credentials")
		return nil
	}
	for _, rp := range s.reposOf(i) {
		if !strings.EqualFold(rp.Owner, r.PathValue("owner")) || !strings.EqualFold(rp.Name, r.PathValue("repo")) {
			continue
		}
		if t.RepositoryIDs != nil && !slices.Contains(t.RepositoryIDs, rp.ID) {
			break
		}
		if levels[t.Permissions[perm]] < level {
			message(w, http.StatusForbidden, "Resource not accessible by integration")
			return nil
		}
		for k := range s.repos {
			if s.repos[k].ID == rp.ID {
				return &s.repos[k]
			}
		}
	}
	message(w, http.StatusNotFound, "Not Found")
	return nil
}

func (s *Server) branchJSON(repoID int64, name string) map[string]any {
	return map[string]any{"name": name, "commit": map[string]any{"sha": s.branchSHA(repoID, name)}, "protected": false}
}

func (s *Server) listBranches(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.repoOfToken(w, r)
	if rp == nil {
		return
	}
	all := []map[string]any{}
	for _, b := range rp.Branches {
		all = append(all, s.branchJSON(rp.ID, b))
	}
	writeJSON(w, http.StatusOK, page(all, r))
}

func (s *Server) getBranch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.repoOfToken(w, r)
	if rp == nil {
		return
	}
	name := r.PathValue("branch")
	if !slices.Contains(rp.Branches, name) {
		message(w, http.StatusNotFound, "Branch not found")
		return
	}
	writeJSON(w, http.StatusOK, s.branchJSON(rp.ID, name))
}
