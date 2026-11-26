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
		format:       r.format.With(tx),
	}
}
