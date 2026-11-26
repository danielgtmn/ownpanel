package panel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// GitHub connections: an administrator connects the panel to a GitHub
// account or organisation. The panel creates its own GitHub App from a
// manifest (every panel has its own App; there is no central one), the
// person confirms it on GitHub, installs it and chooses the repositories
// it may see.
//
// A private App can only be installed on the account that owns it, so
// every account or organisation gets its own App ("Add another account"
// creates the next one). The App's private key, webhook secret and client
// secret are sealed with secret.key like DNS tokens and never leave the
// panel. Installation tokens are requested when needed, narrowed to what
// they are for and never stored.
//
// Who may do what:
//   - connecting (a browser flow) needs an administrator signed in with a
//     password; removing and refreshing an administrator signed in or with
//     an unrestricted write token;
//   - seeing the connections and the repositories needs an administrator
//     (a read-only token is enough);
//   - using a repository for a site is decided in one place,
//     UsableGitHubRepository: only administrators, because the
//     repositories belong to the administrators' accounts and
//     organisations. Members get "not found", exactly as for a repository
//     that does not exist.

// Purposes of the sealed App secrets. They are part of the stored format:
// never change them.
const (
	githubKeyPurpose     = "ownpanel github app private key"
	githubWebhookPurpose = "ownpanel github app webhook secret"
	githubClientPurpose  = "ownpanel github app client secret"
)

// Paths on the panel's domain that GitHub sends people and deliveries to.
// They are written into the App when it is created, so they must not
// change.
const (
	// GitHubManifestPath is the panel's page that posts the manifest to
	// GitHub (it needs its own content security policy).
	GitHubManifestPath = "/api/github/manifest"
	// GitHubCreatedPath is where GitHub sends the person back with the
	// temporary code after creating the App (the manifest's redirect_url).
	GitHubCreatedPath = "/api/github/app-created"
	// GitHubInstalledPath is where GitHub sends the person after
	// installing the App or changing its repositories (setup_url).
	GitHubInstalledPath = "/api/github/installed"
	// GitHubWebhookPath receives GitHub's deliveries, signed with the
	// App's webhook secret.
	GitHubWebhookPath = "/api/github/webhook"
)

const (
	// githubFlowTTL is how long a started connection waits for the person
	// to come back from GitHub. GitHub's code itself is valid for an hour.
	githubFlowTTL = 20 * time.Minute
	// githubMaxFlows bounds the connections being set up at once.
	githubMaxFlows = 64
	// githubRepoCacheTTL keeps repository lists for a minute, like DNS
	// zones.
	githubRepoCacheTTL = 60 * time.Second
	// githubTimeout bounds one operation's requests to GitHub.
	githubTimeout = 45 * time.Second
	// githubAppNameMax is GitHub's limit for App names.
	githubAppNameMax = 34
)

// githubPermissions are the permissions the App asks for. Nothing more:
// GitHub never grants a token more than these, and an installation must
// approve every addition.
var githubPermissions = map[string]string{
	// Fetch a commit to build it (#113) and list branches (#108).
	"contents": "read",
	// Required of every App: names and settings of repositories.
	"metadata": "read",
	// Report a build's result on the commit (#113).
	"statuses": "write",
	// Post and update the preview address on a pull request (#116);
	// also needed to receive pull request events.
	"pull_requests": "write",
}

// githubEvents are the deliveries the App subscribes to: pushes (#109),
// changes of repositories such as renames (#109) and pull requests
// (#116). Installation changes are always delivered.
var githubEvents = []string{"push", "repository", "pull_request"}

// GitHubPermissions returns what the App asks for, for documentation and
// tests.
func GitHubPermissions() map[string]string {
	out := map[string]string{}
	for k, v := range githubPermissions {
		out[k] = v
	}
	return out
}

// DefaultGitHub returns a client for github.com.
func DefaultGitHub() *github.Client {
	c, err := github.New(github.DefaultURL)
	if err != nil {
		panic(err)
	}
	return c
}

// ---- views ----

// GitHub is the state of the GitHub connections.
type GitHub struct {
	// Ready: the panel has a public address with HTTPS, so it can be
	// connected. Problem says why not.
	Ready   bool        `json:"ready"`
	Problem string      `json:"problem,omitempty"`
	Apps    []GitHubApp `json:"apps"`
	// SitesWithoutConnection are sites linked to a repository whose
	// connection or installation was removed (status connection_missing).
	SitesWithoutConnection []GitHubLinkedSite `json:"sites_without_connection"`
}

// GitHubLinkedSite is a site linked to a repository, as the settings list
// it.
type GitHubLinkedSite struct {
	Site       string `json:"site"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
}

// GitHubApp is one connection: an App the panel created. Never its
// secrets.
type GitHubApp struct {
	ID        string `json:"id"`
	AppID     int64  `json:"app_id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Owner     string `json:"owner"`
	OwnerType string `json:"owner_type"` // "user" | "organization"
	HTMLURL   string `json:"html_url"`
	// SettingsURL is the App's settings page on GitHub, where it is
	// deleted.
	SettingsURL   string               `json:"settings_url"`
	CreatedBy     string               `json:"created_by"`
	CreatedAt     time.Time            `json:"created_at"`
	LastCheckedAt *time.Time           `json:"last_checked_at"`
	LastError     string               `json:"last_error"`
	Installations []GitHubInstallation `json:"installations"`
}

// GitHubInstallation is an account the App is installed on.
type GitHubInstallation struct {
	ID          int64  `json:"id"`
	Account     string `json:"account"`
	AccountType string `json:"account_type"` // "user" | "organization"
	// RepositorySelection: "all" repositories of the account, or "selected"
	// ones.
	RepositorySelection string `json:"repository_selection"`
	Suspended           bool   `json:"suspended"`
	// RepositoryCount is nil until the repositories were listed once.
	RepositoryCount *int `json:"repository_count"`
	// SettingsURL is where the repositories are chosen on GitHub.
	SettingsURL string `json:"settings_url"`
	// Sites are the sites linked to one of its repositories (only filled
	// in the list of connections).
	Sites []GitHubLinkedSite `json:"sites"`
}

// GitHubRepository is a repository an installation may see.
type GitHubRepository struct {
	ID             int64  `json:"id"`
	InstallationID int64  `json:"installation_id"`
	Owner          string `json:"owner"`
	Name           string `json:"name"`
	FullName       string `json:"full_name"`
	Private        bool   `json:"private"`
	Archived       bool   `json:"archived"`
	DefaultBranch  string `json:"default_branch"`
	HTMLURL        string `json:"html_url"`
}

// GitHubRepositories is the list of one installation. Truncated: there
// are more than the panel lists (3000).
type GitHubRepositories struct {
	Repositories []GitHubRepository `json:"repositories"`
	Truncated    bool               `json:"truncated"`
}

// GitHubStart is where the browser goes next.
type GitHubStart struct {
	URL string `json:"url"`
}

// GitHubManifestForm is what the manifest page posts to GitHub.
type GitHubManifestForm struct {
	Action   string // GitHub's form address, with the state
	Manifest string // JSON
	// Origin is GitHub's origin, which the page's policy allows posting to.
	Origin string
}

// GitHubRemoved tells what is left to do after removing a connection.
type GitHubRemoved struct {
	Name string `json:"name"`
	// SettingsURL is where the App itself is deleted on GitHub.
	SettingsURL string `json:"settings_url"`
}

func accountTypeView(t string) string {
	if t == github.AccountOrganization {
		return "organization"
	}
	return "user"
}

func (s *Service) githubAppView(a store.GitHubApp, insts []store.GitHubInstallation) GitHubApp {
	owner := github.Account{Login: a.OwnerLogin, Type: a.OwnerType}
	v := GitHubApp{
		ID: a.ID, AppID: a.AppID, Name: a.Name, Slug: a.Slug, Owner: a.OwnerLogin, OwnerType: accountTypeView(a.OwnerType),
		HTMLURL: a.HTMLURL, SettingsURL: s.github.AppSettingsURL(owner, a.Slug), CreatedBy: a.CreatedBy,
		CreatedAt: a.CreatedAt, LastCheckedAt: a.LastCheckedAt, LastError: a.LastError,
		Installations: []GitHubInstallation{},
	}
	for _, i := range insts {
		if i.AppID != a.ID {
			continue
		}
		acc := github.Account{Login: i.AccountLogin, Type: i.AccountType}
		v.Installations = append(v.Installations, GitHubInstallation{
			ID: i.ID, Account: i.AccountLogin, AccountType: accountTypeView(i.AccountType),
			RepositorySelection: i.RepositorySelection, Suspended: i.Suspended, RepositoryCount: i.RepositoryCount,
			SettingsURL: s.github.InstallationSettingsURL(acc, i.ID), Sites: []GitHubLinkedSite{},
		})
	}
	return v
}

func repositoryView(installationID int64, r github.Repository) GitHubRepository {
	return GitHubRepository{
		ID: r.ID, InstallationID: installationID, Owner: r.Owner, Name: r.Name, FullName: r.FullName,
		Private: r.Private, Archived: r.Archived, DefaultBranch: r.DefaultBranch, HTMLURL: r.HTMLURL,
	}
}

// ---- permissions ----

// needGitHubSee: seeing the connections and their repositories.
func needGitHubSee(a Actor) error {
	if err := needAuth(a); err != nil {
		return err
	}
	if !a.Admin || a.Sites != nil {
		return errorf(Forbidden, "only administrators can see the GitHub connections")
	}
	return nil
}

// needGitHubBrowser: the steps of connecting, which happen in the
// browser of an administrator signed in with a password, bound to that
// sign-in.
func needGitHubBrowser(a Actor) error {
	if err := needSession(a, "connecting GitHub"); err != nil {
		return err
	}
	if err := needAdmin(a); err != nil {
		return err
	}
	if a.sessionID == "" {
		return errorf(Forbidden, "connecting GitHub only works when signed in with a password")
	}
	return nil
}

// ---- the panel's address ----

// githubBase is the panel's public address GitHub sends people and
// deliveries to. Without a panel domain with HTTPS there is none.
func (s *Service) githubBase() (string, error) {
	d := s.PanelDomain()
	if d == "" {
		return "", errorf(Conflict, "GitHub can only be connected when the panel has its own domain: GitHub sends you back to it and reports changes to it. Set the panel domain in the settings first")
	}
	if s.opts.TLSMode == caddy.TLSOff {
		return "", errorf(Conflict, "GitHub can only be connected when the panel is reached over HTTPS; this server runs without certificates")
	}
	host := d
	if p := s.opts.HTTPSPort; p != 0 && p != 443 {
		host += ":" + strconv.Itoa(p)
	}
	return "https://" + host, nil
}

// ---- flows ----

const (
	flowCreate  = "create"
	flowInstall = "install"
)

// githubFlow is a connection step started in the panel and waiting for the
// person to come back from GitHub. It is found by the hash of its state
// and bound to the sign-in that started it.
type githubFlow struct {
	purpose string
	session string
	userID  string
	org     string // flowCreate: the organisation, "" for the person's own account
	appID   string // flowInstall: the panel's id of the App
	expires time.Time
}

type githubFlows struct {
	mu sync.Mutex
	m  map[string]githubFlow
}

func stateKey(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

// validState: the shape of the states the panel makes (32 random bytes).
func validState(state string) bool {
	if len(state) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(state)
	return err == nil
}

func (f *githubFlows) start(fl githubFlow, now time.Time) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", newError(Internal, err)
	}
	state := base64.RawURLEncoding.EncodeToString(b)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]githubFlow{}
	}
	for k, v := range f.m {
		if !now.Before(v.expires) {
			delete(f.m, k)
		}
	}
	if len(f.m) >= githubMaxFlows {
		return "", errorf(Throttled, "too many GitHub connections are being set up at once; try again in a few minutes")
	}
	fl.expires = now.Add(githubFlowTTL)
	f.m[stateKey(state)] = fl
	return state, nil
}

// get returns the flow of state if it is for purpose, was started by this
// sign-in and has not expired. take also uses it up. Every failure looks
// the same.
func (f *githubFlows) get(state, purpose string, a Actor, now time.Time, take bool) (githubFlow, error) {
	invalid := errorf(Invalid, "this confirmation from GitHub is not valid any more (it was used, it is older than %d minutes, or it was started in another browser or sign-in); start again", int(githubFlowTTL/time.Minute))
	if !validState(state) {
		return githubFlow{}, invalid
	}
	key := stateKey(state)
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.m[key]
	if !ok || !now.Before(fl.expires) {
		delete(f.m, key)
		return githubFlow{}, invalid
	}
	if fl.purpose != purpose || fl.session != a.sessionID || fl.userID != a.UserID {
		// Someone else's state: leave it for its owner.
		return githubFlow{}, invalid
	}
	if take {
		delete(f.m, key)
	}
	return fl, nil
}

// ---- GitHub access ----

// githubError turns an error of the GitHub client into a message for
// people. The client never puts secrets into errors.
func githubError(conn string, err error) error {
	var ge *github.Error
	detail := ""
	if errors.As(err, &ge) && ge.Message != "" {
		detail = " (" + ge.Message + ")"
	}
	e := func(kind Kind, format string, args ...any) error {
		return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...), Err: err}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return newError(Internal, err)
	case errors.Is(err, context.DeadlineExceeded):
		return e(Unavailable, "GitHub did not answer in time; try again later")
	case errors.Is(err, github.ErrUnauthorized):
		return e(Conflict, "GitHub no longer accepts the connection %s; the app may have been deleted on GitHub. Remove the connection here and connect again", conn)
	case errors.Is(err, github.ErrNotFound):
		return e(NotFound, "not found at GitHub")
	case errors.Is(err, github.ErrRateLimited):
		return e(Throttled, "GitHub's request limit is reached; try again in a few minutes")
	case errors.Is(err, github.ErrForbidden):
		return e(Conflict, "GitHub refused the request for %s%s", conn, detail)
	case errors.Is(err, github.ErrInvalid):
		return e(Conflict, "GitHub could not process the request for %s%s", conn, detail)
	case errors.Is(err, github.ErrBadAnswer):
		return e(Unavailable, "GitHub sent an unexpected answer; try again later%s", detail)
	}
	return e(Unavailable, "GitHub could not be reached; try again later%s", detail)
}

// appAuth signs in as the App with its unsealed private key.
func (s *Service) appAuth(a store.GitHubApp) (*github.AppAuth, error) {
	if s.secrets == nil {
		return nil, errorf(Internal, "no secret key configured; GitHub connections cannot be used")
	}
	key, err := s.secrets.Open(a.PrivateKey, githubKeyPurpose)
	if err != nil {
		return nil, errorf(Conflict, "the GitHub connection %s cannot be used because the server's secret key has changed; remove it and connect again", a.Name)
	}
	auth, err := s.github.AsApp(a.ClientID, key)
	if err != nil {
		return nil, errorf(Conflict, "the stored key of the GitHub connection %s is not usable; remove it and connect again", a.Name)
	}
	return auth, nil
}

type githubRepoEntry struct {
	repos     []github.Repository
	truncated bool
	at        time.Time
}

type githubRepoCache struct {
	mu sync.Mutex
	m  map[int64]githubRepoEntry
}

func (c *githubRepoCache) get(id int64, now time.Time) (githubRepoEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok || now.Sub(e.at) >= githubRepoCacheTTL || now.Before(e.at) {
		return githubRepoEntry{}, false
	}
	e.repos = slices.Clone(e.repos)
	return e, true
}

func (c *githubRepoCache) put(id int64, e githubRepoEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[int64]githubRepoEntry{}
	}
	e.repos = slices.Clone(e.repos)
	c.m[id] = e
}

func (c *githubRepoCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.m)
}

// ---- operations ----

// GitHubConnections returns the connections and whether the panel can be
// connected.
func (s *Service) GitHubConnections(ctx context.Context, a Actor) (GitHub, error) {
	if err := needGitHubSee(a); err != nil {
		return GitHub{}, err
	}
	out := GitHub{Ready: true, Apps: []GitHubApp{}, SitesWithoutConnection: []GitHubLinkedSite{}}
	if _, err := s.githubBase(); err != nil {
		out.Ready, out.Problem = false, err.Error()
	}
	sources, err := s.store.GitHubSiteSources(ctx)
	if err != nil {
		return GitHub{}, storeError(err)
	}
	apps, err := s.store.GitHubApps(ctx)
	if err != nil {
		return GitHub{}, storeError(err)
	}
	insts, err := s.store.GitHubInstallations(ctx, "")
	if err != nil {
		return GitHub{}, storeError(err)
	}
	byInstallation := map[int64][]GitHubLinkedSite{}
	for _, src := range sources {
		linked := GitHubLinkedSite{Site: src.Site, Repository: src.Repository, Branch: src.Branch}
		byInstallation[src.InstallationID] = append(byInstallation[src.InstallationID], linked)
	}
	known := map[int64]bool{}
	for _, app := range apps {
		v := s.githubAppView(app, insts)
		for i := range v.Installations {
			known[v.Installations[i].ID] = true
			if sites := byInstallation[v.Installations[i].ID]; sites != nil {
				v.Installations[i].Sites = sites
			}
		}
		out.Apps = append(out.Apps, v)
	}
	// Sources keep their installation id only while it exists; anything
	// else lost its connection.
	for _, src := range sources {
		if !known[src.InstallationID] {
			out.SitesWithoutConnection = append(out.SitesWithoutConnection,
				GitHubLinkedSite{Site: src.Site, Repository: src.Repository, Branch: src.Branch})
		}
	}
	return out, nil
}

// StartGitHubApp starts connecting an account: the person's own GitHub
// account when org is "", else that organisation. It returns the panel's
// page that sends the browser on to GitHub.
func (s *Service) StartGitHubApp(ctx context.Context, a Actor, org string) (GitHubStart, error) {
	if err := needGitHubBrowser(a); err != nil {
		return GitHubStart{}, err
	}
	if _, err := s.githubBase(); err != nil {
		return GitHubStart{}, err
	}
	if s.secrets == nil {
		return GitHubStart{}, errorf(Internal, "no secret key configured; GitHub connections cannot be stored")
	}
	org = strings.TrimSpace(org)
	if org != "" {
		if !github.ValidLogin(org) {
			return GitHubStart{}, errorf(Invalid, "%q is not a GitHub organisation name: letters, digits and single hyphens, up to 39 characters", org)
		}
		apps, err := s.store.GitHubApps(ctx)
		if err != nil {
			return GitHubStart{}, storeError(err)
		}
		for _, app := range apps {
			if strings.EqualFold(app.OwnerLogin, org) {
				return GitHubStart{}, errorf(Conflict, "%s is already connected", app.OwnerLogin)
			}
		}
	}
	state, err := s.githubFlows.start(githubFlow{purpose: flowCreate, session: a.sessionID, userID: a.UserID, org: org}, s.now())
	if err != nil {
		return GitHubStart{}, err
	}
	return GitHubStart{URL: GitHubManifestPath + "?state=" + state}, nil
}

// githubAppName is the App's name: GitHub needs it unique among all Apps
// and at most 34 characters long; the person can still change it there.
func githubAppName(domain string) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	suffix := " " + hex.EncodeToString(b)
	name := "ownpanel " + domain
	if max := githubAppNameMax - len(suffix); utf8.RuneCountInString(name) > max {
		name = strings.TrimRight(string([]rune(name)[:max]), ".-")
	}
	return name + suffix
}

// githubManifest is the App the panel asks GitHub to create.
func (s *Service) githubManifest(base string) (string, error) {
	type hook struct {
		URL    string `json:"url"`
		Active bool   `json:"active"`
	}
	m := struct {
		Name               string            `json:"name"`
		URL                string            `json:"url"`
		Description        string            `json:"description"`
		HookAttributes     hook              `json:"hook_attributes"`
		RedirectURL        string            `json:"redirect_url"`
		SetupURL           string            `json:"setup_url"`
		SetupOnUpdate      bool              `json:"setup_on_update"`
		Public             bool              `json:"public"`
		DefaultPermissions map[string]string `json:"default_permissions"`
		DefaultEvents      []string          `json:"default_events"`
	}{
		Name:               githubAppName(s.PanelDomain()),
		URL:                base + "/",
		Description:        "Publishes sites of the ownpanel at " + s.PanelDomain() + " from your repositories.",
		HookAttributes:     hook{URL: base + GitHubWebhookPath, Active: true},
		RedirectURL:        base + GitHubCreatedPath,
		SetupURL:           base + GitHubInstalledPath,
		SetupOnUpdate:      true,
		Public:             false,
		DefaultPermissions: GitHubPermissions(),
		DefaultEvents:      slices.Clone(githubEvents),
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// GitHubManifestForm returns the form that creates the App on GitHub, for
// a state from StartGitHubApp of the same sign-in. Showing it again (a
// reload) is harmless; the state is used up when the person comes back.
func (s *Service) GitHubManifestForm(ctx context.Context, a Actor, state string) (GitHubManifestForm, error) {
	if err := needGitHubBrowser(a); err != nil {
		return GitHubManifestForm{}, err
	}
	fl, err := s.githubFlows.get(state, flowCreate, a, s.now(), false)
	if err != nil {
		return GitHubManifestForm{}, err
	}
	base, err := s.githubBase()
	if err != nil {
		return GitHubManifestForm{}, err
	}
	manifest, err := s.githubManifest(base)
	if err != nil {
		return GitHubManifestForm{}, newError(Internal, err)
	}
	return GitHubManifestForm{Action: s.github.ManifestFormURL(fl.org, state), Manifest: manifest, Origin: s.github.WebURL()}, nil
}

// FinishGitHubApp completes creating the App: the state must come from
// StartGitHubApp of this very sign-in, unused and fresh; the code from
// GitHub is exchanged for the App's credentials, which are stored sealed.
// Neither the code nor the credentials appear in any answer, log or
// activity entry.
func (s *Service) FinishGitHubApp(ctx context.Context, a Actor, code, state string) (GitHubApp, error) {
	if err := needGitHubBrowser(a); err != nil {
		return GitHubApp{}, err
	}
	if _, err := s.githubFlows.get(state, flowCreate, a, s.now(), true); err != nil {
		return GitHubApp{}, err
	}
	if !github.ValidCode(code) {
		return GitHubApp{}, errorf(Invalid, "the confirmation from GitHub is incomplete; start again")
	}
	if s.secrets == nil {
		return GitHubApp{}, errorf(Internal, "no secret key configured; GitHub connections cannot be stored")
	}
	ctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	creds, err := s.github.ConvertManifest(ctx, code)
	if errors.Is(err, github.ErrNotFound) || errors.Is(err, github.ErrInvalid) {
		return GitHubApp{}, &Error{Kind: Invalid, Msg: "GitHub did not accept the confirmation; it may be older than an hour or already used. Start again", Err: err}
	}
	if err != nil {
		return GitHubApp{}, githubError("", err)
	}
	app, err := s.store.CreateGitHubApp(context.WithoutCancel(ctx), store.GitHubApp{
		AppID: creds.ID, Slug: creds.Slug, Name: creds.Name, ClientID: creds.ClientID,
		OwnerLogin: creds.Owner.Login, OwnerType: creds.Owner.Type, HTMLURL: creds.HTMLURL,
		PrivateKey:    s.secrets.Seal(creds.PrivateKey, githubKeyPurpose),
		WebhookSecret: s.secrets.Seal([]byte(creds.WebhookSecret), githubWebhookPurpose),
		ClientSecret:  s.secrets.Seal([]byte(creds.ClientSecret), githubClientPurpose),
	}, a.UserID)
	if err != nil {
		return GitHubApp{}, storeError(err)
	}
	s.record(ctx, a, "github.connect", app.Name, fmt.Sprintf("%s %s, app %d", accountTypeView(app.OwnerType), app.OwnerLogin, app.AppID))
	return s.githubAppView(app, nil), nil
}

// StartGitHubInstall returns GitHub's page where the App of a connection
// is installed and its repositories are chosen.
func (s *Service) StartGitHubInstall(ctx context.Context, a Actor, id string) (GitHubStart, error) {
	if err := needGitHubBrowser(a); err != nil {
		return GitHubStart{}, err
	}
	app, err := s.store.GitHubApp(ctx, id)
	if err != nil {
		return GitHubStart{}, storeError(err)
	}
	state, err := s.githubFlows.start(githubFlow{purpose: flowInstall, session: a.sessionID, userID: a.UserID, appID: app.ID}, s.now())
	if err != nil {
		return GitHubStart{}, err
	}
	return GitHubStart{URL: s.github.InstallURL(app.Slug, state)}, nil
}

// FinishGitHubInstall records an installation when the person comes back
// from GitHub. installationID comes from the browser and proves nothing:
// it is used only if the state is valid for this sign-in and GitHub,
// asked as the App of that state, confirms the installation is its own.
func (s *Service) FinishGitHubInstall(ctx context.Context, a Actor, installationID int64, state string) (GitHubApp, error) {
	if err := needGitHubBrowser(a); err != nil {
		return GitHubApp{}, err
	}
	fl, err := s.githubFlows.get(state, flowInstall, a, s.now(), true)
	if err != nil {
		return GitHubApp{}, err
	}
	app, err := s.store.GitHubApp(ctx, fl.appID)
	if err != nil {
		return GitHubApp{}, storeError(err)
	}
	auth, err := s.appAuth(app)
	if err != nil {
		return GitHubApp{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	inst, err := auth.Installation(ctx, installationID)
	if errors.Is(err, github.ErrNotFound) {
		return GitHubApp{}, &Error{Kind: NotFound, Msg: fmt.Sprintf("GitHub does not know this installation of %s", app.Name), Err: err}
	}
	if err != nil {
		return GitHubApp{}, githubError(app.Name, err)
	}
	if inst.AppID != app.AppID {
		return GitHubApp{}, errorf(NotFound, "GitHub does not know this installation of %s", app.Name)
	}
	if err := s.store.PutGitHubInstallation(context.WithoutCancel(ctx), installationRow(app.ID, inst)); err != nil {
		return GitHubApp{}, storeError(err)
	}
	s.githubRepos.clear()
	s.countRepositories(ctx, app, auth, inst.ID)
	s.record(ctx, a, "github.install", app.Name, fmt.Sprintf("on %s, %s repositories", inst.Account.Login, inst.RepositorySelection))
	return s.githubApp(context.WithoutCancel(ctx), app.ID)
}

func installationRow(appID string, i github.Installation) store.GitHubInstallation {
	return store.GitHubInstallation{
		ID: i.ID, AppID: appID, AccountLogin: i.Account.Login, AccountType: i.Account.Type,
		RepositorySelection: i.RepositorySelection, Suspended: i.Suspended,
	}
}

func (s *Service) githubApp(ctx context.Context, id string) (GitHubApp, error) {
	app, err := s.store.GitHubApp(ctx, id)
	if err != nil {
		return GitHubApp{}, storeError(err)
	}
	insts, err := s.store.GitHubInstallations(ctx, id)
	if err != nil {
		return GitHubApp{}, storeError(err)
	}
	return s.githubAppView(app, insts), nil
}

// RefreshGitHub asks GitHub, as each App, where it is installed and how
// many repositories each installation may see, and stores the answer.
// Problems are recorded on the connection, not returned.
func (s *Service) RefreshGitHub(ctx context.Context, a Actor) (GitHub, error) {
	if err := needAdmin(a); err != nil {
		return GitHub{}, err
	}
	apps, err := s.store.GitHubApps(ctx)
	if err != nil {
		return GitHub{}, storeError(err)
	}
	s.githubRepos.clear()
	for _, app := range apps {
		if err := s.refreshGitHubApp(ctx, app); err != nil && KindOf(err) == Internal {
			return GitHub{}, err
		}
	}
	return s.GitHubConnections(ctx, a)
}

func (s *Service) refreshGitHubApp(ctx context.Context, app store.GitHubApp) error {
	ctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	note := func(err error) error {
		msg := ""
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			msg = err.Error()
		}
		if rerr := s.store.RecordGitHubCheck(context.WithoutCancel(ctx), app.ID, s.now(), msg); rerr != nil {
			slog.Error("could not record a GitHub check", "connection", app.ID, "err", rerr)
		}
		return err
	}
	auth, err := s.appAuth(app)
	if err != nil {
		return note(err)
	}
	list, err := auth.Installations(ctx)
	if err != nil {
		return note(githubError(app.Name, err))
	}
	rows := make([]store.GitHubInstallation, 0, len(list))
	for _, i := range list {
		if i.AppID != app.AppID {
			continue
		}
		rows = append(rows, installationRow(app.ID, i))
	}
	if err := s.store.ReplaceGitHubInstallations(context.WithoutCancel(ctx), app.ID, rows); err != nil {
		return storeError(err)
	}
	for _, r := range rows {
		s.countRepositories(ctx, app, auth, r.ID)
	}
	return note(nil)
}

// countRepositories lists an installation's repositories once to store
// their number; failures only leave the count as it was.
func (s *Service) countRepositories(ctx context.Context, app store.GitHubApp, auth *github.AppAuth, id int64) {
	if _, err := s.listRepositories(ctx, app, auth, id); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("could not list the repositories of a GitHub installation", "connection", app.ID, "installation", id, "err", err)
	}
}

// listRepositories lists what the installation may see, with a token that
// may only read metadata.
func (s *Service) listRepositories(ctx context.Context, app store.GitHubApp, auth *github.AppAuth, id int64) (githubRepoEntry, error) {
	if e, ok := s.githubRepos.get(id, s.now()); ok {
		return e, nil
	}
	tok, err := auth.InstallationToken(ctx, id, github.TokenRequest{Permissions: map[string]string{"metadata": "read"}})
	if err != nil {
		return githubRepoEntry{}, githubError(app.Name, err)
	}
	repos, truncated, err := s.github.InstallationRepositories(ctx, tok)
	if err != nil {
		return githubRepoEntry{}, githubError(app.Name, err)
	}
	e := githubRepoEntry{repos: repos, truncated: truncated, at: s.now()}
	s.githubRepos.put(id, e)
	if err := s.store.SetGitHubRepositoryCount(context.WithoutCancel(ctx), id, len(repos)); err != nil {
		slog.Error("could not store a repository count", "installation", id, "err", err)
	}
	return e, nil
}

// installationApp returns a stored installation and its App.
func (s *Service) installationApp(ctx context.Context, installationID int64) (store.GitHubInstallation, store.GitHubApp, error) {
	inst, err := s.store.GitHubInstallation(ctx, installationID)
	if err != nil {
		return store.GitHubInstallation{}, store.GitHubApp{}, storeError(err)
	}
	app, err := s.store.GitHubApp(ctx, inst.AppID)
	if err != nil {
		return store.GitHubInstallation{}, store.GitHubApp{}, storeError(err)
	}
	return inst, app, nil
}

// GitHubRepositories lists the repositories an installation may see
// (cached for a minute; RefreshGitHub starts afresh).
func (s *Service) GitHubRepositories(ctx context.Context, a Actor, installationID int64) (GitHubRepositories, error) {
	if err := needGitHubSee(a); err != nil {
		return GitHubRepositories{}, err
	}
	_, app, err := s.installationApp(ctx, installationID)
	if err != nil {
		return GitHubRepositories{}, err
	}
	auth, err := s.appAuth(app)
	if err != nil {
		return GitHubRepositories{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	e, err := s.listRepositories(ctx, app, auth, installationID)
	if err != nil {
		return GitHubRepositories{}, err
	}
	out := GitHubRepositories{Repositories: make([]GitHubRepository, 0, len(e.repos)), Truncated: e.truncated}
	for _, r := range e.repos {
		out.Repositories = append(out.Repositories, repositoryView(installationID, r))
	}
	return out, nil
}

// UsableGitHubRepository is the one check for using a repository of a
// GitHub connection, e.g. to link a site to it (#108): the actor must be
// an administrator (not limited to sites), and the installation must
// currently see the repository. Everyone else gets "not found", like for
// a repository that does not exist, so names do not leak.
func (s *Service) UsableGitHubRepository(ctx context.Context, a Actor, installationID, repositoryID int64) (GitHubRepository, error) {
	notFound := errorf(NotFound, "GitHub repository not found")
	if err := needAuth(a); err != nil {
		return GitHubRepository{}, err
	}
	if !a.Admin || a.Sites != nil {
		return GitHubRepository{}, notFound
	}
	list, err := s.GitHubRepositories(ctx, a, installationID)
	if KindOf(err) == NotFound {
		return GitHubRepository{}, notFound
	}
	if err != nil {
		return GitHubRepository{}, err
	}
	for _, r := range list.Repositories {
		if r.ID == repositoryID {
			return r, nil
		}
	}
	return GitHubRepository{}, notFound
}

// githubReadToken returns a token that may only read the contents of one
// repository, valid for at most an hour, for fetching its code (#113). It
// is not offered through the API or MCP; never store or log it.
func (s *Service) githubReadToken(ctx context.Context, installationID, repositoryID int64) (github.Token, error) {
	inst, app, err := s.installationApp(ctx, installationID)
	if err != nil {
		return github.Token{}, err
	}
	if inst.Suspended {
		return github.Token{}, errorf(Conflict, "the GitHub app %s is suspended on %s", app.Name, inst.AccountLogin)
	}
	auth, err := s.appAuth(app)
	if err != nil {
		return github.Token{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	tok, err := auth.InstallationToken(ctx, installationID, github.TokenRequest{
		RepositoryIDs: []int64{repositoryID},
		Permissions:   map[string]string{"contents": "read", "metadata": "read"},
	})
	if err != nil {
		return github.Token{}, githubError(app.Name, err)
	}
	return tok, nil
}

// DeleteGitHubApp forgets a connection: its stored key and secrets and its
// installations. The App itself stays on GitHub until it is deleted there
// (at SettingsURL); without its key the panel can no longer use it.
func (s *Service) DeleteGitHubApp(ctx context.Context, a Actor, id string) (GitHubRemoved, error) {
	if err := needAdmin(a); err != nil {
		return GitHubRemoved{}, err
	}
	app, err := s.store.GitHubApp(ctx, id)
	if err != nil {
		return GitHubRemoved{}, storeError(err)
	}
	if err := s.store.DeleteGitHubApp(ctx, id); err != nil {
		return GitHubRemoved{}, storeError(err)
	}
	s.githubRepos.clear()
	s.record(ctx, a, "github.disconnect", app.Name, fmt.Sprintf("%s %s, app %d", accountTypeView(app.OwnerType), app.OwnerLogin, app.AppID))
	owner := github.Account{Login: app.OwnerLogin, Type: app.OwnerType}
	return GitHubRemoved{Name: app.Name, SettingsURL: s.github.AppSettingsURL(owner, app.Slug)}, nil
}
