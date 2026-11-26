package store

import (
	"errors"
	"time"

	"github.com/danielgtmn/ablage"
)

// The rows as stored. Field (column) names are those of the tables from
// before ablage, so the one-time conversion (legacy.go) copies column by
// column. Times are whole seconds (see Store.now); a time.Time that is not
// a pointer and is zero means "never".

type userRow struct {
	ID string
	// Name is unique, compared byte by byte.
	Name string `db:",unique"`
	// PasswordHash is only kept for users from before sign-in moved to the
	// auth tables, until they are moved (authmove.go).
	PasswordHash string
	Role         string
	// AuthUserID points at the sign-in user (sesam's auth_users.id); nil
	// until the user was moved. While a panel user points at it, the
	// sign-in user cannot be deleted (restrict): the panel user goes first.
	AuthUserID *string `db:",ref=auth_users,unique"`
	CreatedAt  time.Time
}

// sessionRow is a session from before sign-in moved; only read to move
// unmoved users (authmove.go). id is the hex SHA-256 of the cookie value.
type sessionRow struct {
	ID        string
	UserID    string `db:",ref=users,cascade,index"`
	CreatedAt time.Time
	ExpiresAt time.Time
}

type tokenRow struct {
	ID     string
	UserID string `db:",ref=users,cascade,index"`
	Name   string
	Hash   string `db:",unique"`
	Access string
	// Sites is nil for every site the user may use.
	Sites      *[]string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
}

// siteRow: the id is the site name. A user who owns sites cannot be
// deleted (restrict).
type siteRow struct {
	ID        string
	OwnerID   *string `db:",ref=users,index"`
	CreatedAt time.Time
}

// siteDomainRow: the id is the domain.
type siteDomainRow struct {
	ID       string
	SiteName string `db:",ref=sites,cascade,index"`
}

// activityRow: Seq numbers the entries in the order they were written
// (entries from before ablage keep their former id as Seq).
type activityRow struct {
	ID     string
	Seq    int64 `db:",unique"`
	At     time.Time
	Actor  string
	Action string
	Target string
	Detail string
}

// settingRow: the id is the key.
type settingRow struct {
	ID    string
	Value string
}

type dnsConnectionRow struct {
	ID            string
	UserID        string `db:",ref=users,cascade"`
	Provider      string
	Name          string
	Secret        []byte // sealed by the panel
	CreatedAt     time.Time
	LastCheckedAt *time.Time
	LastError     string
	CanWrite      *bool
	ZoneCount     *int
}

type dnsZoneRow struct {
	ID           string
	ConnectionID string `db:",ref=dns_connections,cascade"`
	Zone         string
	CreatedAt    time.Time
}

type domainRow struct {
	ID        string
	UserID    string `db:",ref=users,cascade"`
	Domain    string
	CreatedAt time.Time
}

// redirectRow: SourcePath "" ⇔ SiteName nil (a domain redirect); checked in
// Go (checkRedirect) instead of a table constraint. A user who owns domain
// redirects cannot be deleted (restrict).
type redirectRow struct {
	ID           string
	OwnerID      *string `db:",ref=users,index"`
	SiteName     *string `db:",ref=sites,cascade,index"`
	SourceDomain string
	SourcePath   string
	Target       string
	Code         int
	KeepPath     bool
	KeepQuery    bool
	CreatedAt    time.Time
}

type organisationRow struct {
	ID        string
	OwnerID   string `db:",ref=users,cascade,index"`
	Name      string
	CreatedAt time.Time
}

// mailDomainRow: the id is the domain. A user who owns mail domains cannot
// be deleted (restrict).
type mailDomainRow struct {
	ID             string
	OwnerID        string `db:",ref=users,index"`
	Active         bool
	ActivatedAt    *time.Time
	CheckedAt      *time.Time
	CheckError     string
	CreatedAt      time.Time
	OrganisationID *string `db:",ref=organisations,setnull,index"`
}

// dkimKeyRow: ActiveDomain holds the domain while the key is active and is
// nil otherwise; its unique index allows one active key per domain.
type dkimKeyRow struct {
	ID           string
	Domain       string `db:",ref=mail_domains,cascade"`
	Selector     string
	PrivateKey   []byte // sealed by the panel
	PublicKey    string
	State        string
	CreatedAt    time.Time
	ActiveDomain *string `db:",unique"`
}

type mailboxRow struct {
	ID                      string
	StorageID               string `db:",unique"`
	Domain                  string `db:",ref=mail_domains,cascade"`
	LocalPart               string
	PasswordHash            string
	QuotaBytes              int64
	SendPerHour             int
	SendPerDay              int
	CreatedAt               time.Time
	DisplayName             string
	AppPasswordsOnly        bool
	Revision                int64
	AdminPasswordAt         time.Time
	AdminDisplayNameAt      time.Time
	AdminAppPasswordsOnlyAt time.Time
	Kind                    string
	KeepSentCopy            bool
	AdminGrantsAt           time.Time
}

type mailAliasRow struct {
	ID                string
	Domain            string `db:",ref=mail_domains,cascade"`
	LocalPart         string
	Targets           []string
	CreatedAt         time.Time
	SenderPolicy      string
	HideFromDirectory bool
}

// pendingDeleteRow: the id is the storage id.
type pendingDeleteRow struct {
	ID        string
	CreatedAt time.Time
}

type appPasswordRow struct {
	ID          string
	MailboxID   string `db:",ref=mailboxes,cascade,index"`
	Name        string
	MAC         string `db:"mac"`
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	LastUsedNet string
}

// grantRow: MailboxID != GranteeID, checked in Go.
type grantRow struct {
	ID        string
	MailboxID string `db:",ref=mailboxes,cascade"`
	GranteeID string `db:",ref=mailboxes,cascade,index"`
	Rights    string // names separated by spaces
	CreatedAt time.Time
}

type noForwardingRow struct {
	ID        string
	MailboxID string `db:",unique,ref=mailboxes,cascade"`
}

// githubAppRow is a GitHub App the panel created. AppID is GitHub's id of
// the App (external data, unique); the row id is the panel's own.
// OwnerType is "User" or "Organization", checked in Go (checkGitHubApp).
// PrivateKey, WebhookSecret and ClientSecret are sealed by the panel
// (internal/panel/github.go names the purposes); they are never stored in
// the clear and never leave the panel.
type githubAppRow struct {
	ID            string
	AppID         int64 `db:",unique"`
	Slug          string
	Name          string
	ClientID      string
	OwnerLogin    string
	OwnerType     string
	HTMLURL       string `db:"html_url"`
	PrivateKey    []byte
	WebhookSecret []byte
	ClientSecret  []byte
	CreatedBy     *string `db:",ref=users,setnull,index"`
	CreatedAt     time.Time
	LastCheckedAt *time.Time
	LastError     string
}

// githubInstallationRow is an installation as GitHub last reported it
// (GitHub stays the source of truth; a refresh replaces the rows of an
// App). The id is GitHub's installation id in decimal: it is the natural
// key, GitHub's webhooks and the install redirect name it, and it never
// changes. AccountType ("User", "Organization") and RepositorySelection
// ("all", "selected") are checked in Go (checkGitHubInstallation).
// RepositoryCount is nil until the repositories were listed once.
type githubInstallationRow struct {
	ID                  string
	AppID               string `db:",ref=github_apps,cascade,index"`
	AccountLogin        string
	AccountType         string
	RepositorySelection string
	Suspended           bool
	RepositoryCount     *int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// siteSourceRow is where a site's code comes from: at most one per site
// (unique SiteName), removed with the site. Kind is "github" or "git";
// checkSource stands for the former table constraints.
//
// Kind github: a repository of a GitHub App installation, identified by
// GitHub's numeric RepositoryID (external data, it survives renames);
// Repository holds owner/name for display and is refreshed whenever the
// panel sees the repository. InstallationID becomes nil when the
// installation or its whole connection is removed: the source stays, so
// the site shows "connection missing" with the repository it was linked
// to, until someone links it again or unlinks it.
//
// Kind git: any Git host (#111). Repository holds the checked clone
// address (SSH or HTTPS). For SSH, DeployKey is the sealed private key of
// the site's deploy key and DeployPublicKey its public half (an
// authorized_keys line); HostKey is the SSH server's key the person
// confirmed (authorized_keys format), and HostKeyNew the different key the
// server showed since, until someone accepts it or it shows the confirmed
// one again. NotifySecret is the sealed secret of the notification address
// and NotifyID the random id in that address (unique; nil for GitHub).
// CheckedAt and CheckError are the result of the last time the panel
// reached the repository (empty error: it worked).
type siteSourceRow struct {
	ID             string
	SiteName       string `db:",ref=sites,cascade,unique"`
	Kind           string
	InstallationID *string `db:",ref=github_installations,setnull,index"`
	RepositoryID   *int64  `db:",index"`
	Repository     string
	Branch         string
	PublishOnPush  bool
	DeployKey      []byte
	NotifySecret   []byte
	CreatedBy      *string `db:",ref=users,setnull,index"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// Kind git (#111).
	DeployPublicKey string
	HostKey         string
	HostKeyNew      string
	NotifyID        *string `db:",unique"`
	CheckedAt       *time.Time
	CheckError      string
}

// siteRunRow is one run of a site: one entry per push (later also "Build
// now") that should build and publish it. A run is only recorded here;
// building it is #113, which takes queued runs and reports their end
// (internal/panel/runs.go). The panel keeps the last runs of each site and
// removes older ones. The id is ablage's (ascending in the order the runs
// were recorded, so "newest first" is by id).
//
// Status is queued, running, succeeded, failed, cancelled or skipped;
// CommitSHA is 40 hex digits; both are checked in Go (runs.go). SourceKind,
// RepositoryID (GitHub's id, external data) and Repository are the source
// as it was when the run was recorded. Everything about the commit comes
// from GitHub's signed delivery and is text for display only: Message
// (first line), AuthorName, AuthorLogin and Pusher are shortened and never
// interpreted. Detail says why a run was skipped, failed or cancelled.
type siteRunRow struct {
	ID           string
	SiteName     string `db:",ref=sites,cascade"`
	Trigger      string
	Status       string
	SourceKind   string
	RepositoryID int64
	Repository   string
	Ref          string
	CommitSHA    string
	Message      string
	AuthorName   string
	AuthorLogin  string
	Pusher       string
	Detail       string
	CreatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// githubDeliveryRow is a GitHub delivery already handled, so one that
// arrives again is ignored. The id is GitHub's X-GitHub-Delivery (the
// natural key: it is what a repeat is recognised by). Kept for a bounded
// time; AppID is the panel's id of the App that signed it.
type githubDeliveryRow struct {
	ID         string
	AppID      string    `db:",ref=github_apps,cascade,index"`
	ReceivedAt time.Time `db:",index"`
}

// githubRepositoryGoneRow is a repository GitHub reported as no longer
// seen by an installation (taken away from it, or deleted). The id is
// "<installation>/<repository>" with GitHub's ids. Removed when GitHub
// shows the repository again or a site is linked to it anew, and with the
// installation (InstallationID references its row, whose id is GitHub's
// installation id).
type githubRepositoryGoneRow struct {
	ID             string
	InstallationID string `db:",ref=github_installations,cascade,index"`
	RepositoryID   int64
	Since          time.Time
}

// gitDeliveryRow is a notification from a Git host already handled for a
// source (kind git), so one that arrives again is ignored. The id is
// "<source row id>/<the host's delivery id>". Kept for a bounded time.
type gitDeliveryRow struct {
	ID         string
	SourceID   string    `db:",ref=site_sources,cascade,index"`
	ReceivedAt time.Time `db:",index"`
}

// formatRow marks the storage format: a row with id formatAblage means the
// database is kept by ablage (legacy.go).
type formatRow struct {
	ID string
}

const formatAblage = "ablage"

type repos struct {
	users        *ablage.Repository[userRow]
	sessions     *ablage.Repository[sessionRow]
	tokens       *ablage.Repository[tokenRow]
	sites        *ablage.Repository[siteRow]
	siteDomains  *ablage.Repository[siteDomainRow]
	activity     *ablage.Repository[activityRow]
	settings     *ablage.Repository[settingRow]
	dnsConns     *ablage.Repository[dnsConnectionRow]
	dnsZones     *ablage.Repository[dnsZoneRow]
	domains      *ablage.Repository[domainRow]
	redirects    *ablage.Repository[redirectRow]
	orgs         *ablage.Repository[organisationRow]
	mailDomains  *ablage.Repository[mailDomainRow]
	dkimKeys     *ablage.Repository[dkimKeyRow]
	mailboxes    *ablage.Repository[mailboxRow]
	aliases      *ablage.Repository[mailAliasRow]
	pending      *ablage.Repository[pendingDeleteRow]
	appPasswords *ablage.Repository[appPasswordRow]
	grants       *ablage.Repository[grantRow]
	noForwarding *ablage.Repository[noForwardingRow]
	githubApps   *ablage.Repository[githubAppRow]
	githubInsts  *ablage.Repository[githubInstallationRow]
	sources      *ablage.Repository[siteSourceRow]
	runs         *ablage.Repository[siteRunRow]
	deliveries   *ablage.Repository[githubDeliveryRow]
	reposGone    *ablage.Repository[githubRepositoryGoneRow]
	gitDelivs    *ablage.Repository[gitDeliveryRow]
	format       *ablage.Repository[formatRow]
}

func newRepos(st ablage.Store) (repos, error) {
	var r repos
	var errs []error
	add := func(err error) { errs = append(errs, err) }
	var err error
	// sesam's tables (auth_*) live in the same store: users reference
	// auth_users, and one Migrate covers both.
	add(declareSignIn(st))
	r.users, err = ablage.NewRepository[userRow](st, "users")
	add(err)
	r.sessions, err = ablage.NewRepository[sessionRow](st, "sessions")
	add(err)
	r.tokens, err = ablage.NewRepository[tokenRow](st, "tokens")
	add(err)
	r.sites, err = ablage.NewRepository[siteRow](st, "sites")
	add(err)
	r.siteDomains, err = ablage.NewRepository[siteDomainRow](st, "site_domains")
	add(err)
	r.activity, err = ablage.NewRepository[activityRow](st, "activity")
	add(err)
	r.settings, err = ablage.NewRepository[settingRow](st, "settings")
	add(err)
	r.dnsConns, err = ablage.NewRepository[dnsConnectionRow](st, "dns_connections", ablage.WithUniqueIndex("user_id", "name"))
	add(err)
	r.dnsZones, err = ablage.NewRepository[dnsZoneRow](st, "dns_zones", ablage.WithUniqueIndex("connection_id", "zone"))
	add(err)
	r.domains, err = ablage.NewRepository[domainRow](st, "domains", ablage.WithUniqueIndex("user_id", "domain"))
	add(err)
	r.redirects, err = ablage.NewRepository[redirectRow](st, "redirects", ablage.WithUniqueIndex("source_domain", "source_path"))
	add(err)
	r.orgs, err = ablage.NewRepository[organisationRow](st, "organisations")
	add(err)
	r.mailDomains, err = ablage.NewRepository[mailDomainRow](st, "mail_domains")
	add(err)
	r.dkimKeys, err = ablage.NewRepository[dkimKeyRow](st, "mail_dkim_keys", ablage.WithUniqueIndex("domain", "selector"))
	add(err)
	r.mailboxes, err = ablage.NewRepository[mailboxRow](st, "mailboxes", ablage.WithUniqueIndex("domain", "local_part"))
	add(err)
	r.aliases, err = ablage.NewRepository[mailAliasRow](st, "mail_aliases", ablage.WithUniqueIndex("domain", "local_part"))
	add(err)
	r.pending, err = ablage.NewRepository[pendingDeleteRow](st, "mail_pending_deletes")
	add(err)
	r.appPasswords, err = ablage.NewRepository[appPasswordRow](st, "mailbox_app_passwords")
	add(err)
	r.grants, err = ablage.NewRepository[grantRow](st, "mailbox_grants", ablage.WithUniqueIndex("mailbox_id", "grantee_id"))
	add(err)
	r.noForwarding, err = ablage.NewRepository[noForwardingRow](st, "mailbox_no_forwarding")
	add(err)
	r.githubApps, err = ablage.NewRepository[githubAppRow](st, "github_apps")
	add(err)
	r.githubInsts, err = ablage.NewRepository[githubInstallationRow](st, "github_installations")
	add(err)
	r.sources, err = ablage.NewRepository[siteSourceRow](st, "site_sources")
	add(err)
	r.runs, err = ablage.NewRepository[siteRunRow](st, "site_runs", ablage.WithIndex("site_name", "status"),
		ablage.WithIndex("status", "site_name"))
	add(err)
	r.deliveries, err = ablage.NewRepository[githubDeliveryRow](st, "github_deliveries")
	add(err)
	r.reposGone, err = ablage.NewRepository[githubRepositoryGoneRow](st, "github_repositories_gone")
	add(err)
	r.gitDelivs, err = ablage.NewRepository[gitDeliveryRow](st, "git_deliveries")
	add(err)
	r.format, err = ablage.NewRepository[formatRow](st, "store_format")
	add(err)
	return r, errors.Join(errs...)
}

// with binds every repository to the transaction tx.
func (r repos) with(tx ablage.Store) repos {
	return repos{
		users:        r.users.With(tx),
		sessions:     r.sessions.With(tx),
		tokens:       r.tokens.With(tx),
		sites:        r.sites.With(tx),
		siteDomains:  r.siteDomains.With(tx),
		activity:     r.activity.With(tx),
		settings:     r.settings.With(tx),
		dnsConns:     r.dnsConns.With(tx),
		dnsZones:     r.dnsZones.With(tx),
		domains:      r.domains.With(tx),
		redirects:    r.redirects.With(tx),
		orgs:         r.orgs.With(tx),
		mailDomains:  r.mailDomains.With(tx),
		dkimKeys:     r.dkimKeys.With(tx),
		mailboxes:    r.mailboxes.With(tx),
		aliases:      r.aliases.With(tx),
		pending:      r.pending.With(tx),
		appPasswords: r.appPasswords.With(tx),
		grants:       r.grants.With(tx),
		noForwarding: r.noForwarding.With(tx),
		githubApps:   r.githubApps.With(tx),
		githubInsts:  r.githubInsts.With(tx),
		sources:      r.sources.With(tx),
		runs:         r.runs.With(tx),
		deliveries:   r.deliveries.With(tx),
		reposGone:    r.reposGone.With(tx),
		gitDelivs:    r.gitDelivs.With(tx),
		format:       r.format.With(tx),
	}
}
