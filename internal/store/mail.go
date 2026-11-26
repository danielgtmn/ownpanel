package store

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtmn/ablage"
)

// MailDomain is a domain with e-mail. It belongs to OwnerID.
type MailDomain struct {
	Domain      string
	OwnerID     string
	Owner       string // the owner's user name
	Active      bool
	ActivatedAt *time.Time
	CheckedAt   *time.Time
	CheckError  string
	CreatedAt   time.Time
	Mailboxes   int // filled by MailDomains and MailDomain
	Aliases     int
	// OrganisationID is "" when the domain is its own organisation.
	OrganisationID      string
	OrganisationName    string
	OrganisationOwnerID string
}

// DKIMKey is one DKIM key of a mail domain. PrivateKey is sealed by the
// caller (with panzerschrank); the store never sees the plain key.
type DKIMKey struct {
	Domain     string
	Selector   string
	PrivateKey []byte
	PublicKey  string // base64 DER, for the DNS record
	State      string // DKIMActive, DKIMPending or DKIMRetired
	CreatedAt  time.Time
}

// DKIM key states.
const (
	DKIMActive  = "active"
	DKIMPending = "pending"
	DKIMRetired = "retired"
)

// Mailbox is a mailbox of a mail domain. OwnerID is the domain's owner.
type Mailbox struct {
	ID           string
	StorageID    string
	Domain       string
	LocalPart    string
	PasswordHash string
	QuotaBytes   int64
	SendPerHour  int
	SendPerDay   int
	CreatedAt    time.Time
	OwnerID      string
	Owner        string
	// DisplayName may be empty. AppPasswordsOnly: mail programs sign in
	// only with app passwords.
	DisplayName      string
	AppPasswordsOnly bool
	// Revision is the highest revision of the mailbox's own changes in the
	// mail service recorded here. Admin*At are when the panel last set the
	// password, the display name or AppPasswordsOnly itself (zero: never).
	Revision                int64
	AdminPasswordAt         time.Time
	AdminDisplayNameAt      time.Time
	AdminAppPasswordsOnlyAt time.Time
	// Kind is "personal" or "shared" (config.Kind*). KeepSentCopy: shared
	// mailboxes keep a copy of mail sent for them.
	Kind         string
	KeepSentCopy bool
	// AdminGrantsAt is when the panel last changed who has access to the
	// mailbox (zero: never).
	AdminGrantsAt time.Time
}

// MailAlias delivers mail for LocalPart@Domain to Targets. LocalPart "*" is
// the catch-all. OwnerID is the domain's owner.
type MailAlias struct {
	ID        string
	Domain    string
	LocalPart string
	Targets   []string
	CreatedAt time.Time
	OwnerID   string
	Owner     string
	// SenderPolicy is "anyone", "organisation" or "members";
	// HideFromDirectory leaves it out of the address book.
	SenderPolicy      string
	HideFromDirectory bool
}

var (
	errMailDomainNotFound = notFoundf("mail domain not found")
	errMailboxNotFound    = notFoundf("mailbox not found")
	errAliasNotFound      = notFoundf("alias not found")
)

// byCreation is the order of rows "as they were created": rows from before
// ablage got their order within a second as microseconds of created_at
// (legacy.go), newer ones sort by their id within a second.
var byCreation = []ablage.Order{ablage.Asc("created_at"), ablage.Asc("id")}

// ---- domains ----

// countBy counts the rows of repo per value of field among values.
func countBy[T any](ctx context.Context, repo *ablage.Repository[T], field string, values []string) (map[string]int, error) {
	rows, err := repo.Aggregate(ctx, ablage.Aggregation{
		Where:   ablage.In(field, values...),
		GroupBy: []string{field},
		Values:  []ablage.Agg{ablage.CountAll("n")},
	})
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows {
		k, _ := r[field].(string)
		n, _ := r["n"].(int64)
		out[k] = int(n)
	}
	return out, nil
}

// mailDomainsOf completes rows with owner names, counts and organisations,
// one query per relation.
func mailDomainsOf(ctx context.Context, r repos, rows []mailDomainRow) ([]MailDomain, error) {
	domains := make([]string, len(rows))
	owners := make([]string, len(rows))
	var orgIDs []string
	for i, d := range rows {
		domains[i], owners[i] = d.ID, d.OwnerID
		if d.OrganisationID != nil {
			orgIDs = append(orgIDs, *d.OrganisationID)
		}
	}
	orgs := map[string]organisationRow{}
	if len(orgIDs) > 0 {
		list, err := r.orgs.GetMany(ctx, unique(orgIDs)...)
		if err != nil {
			return nil, err
		}
		for _, o := range list {
			orgs[o.ID] = o
		}
	}
	names, err := userNames(ctx, r, owners)
	if err != nil {
		return nil, err
	}
	mailboxes, err := countBy(ctx, r.mailboxes, "domain", domains)
	if err != nil {
		return nil, err
	}
	aliases, err := countBy(ctx, r.aliases, "domain", domains)
	if err != nil {
		return nil, err
	}
	out := make([]MailDomain, len(rows))
	for i, d := range rows {
		m := MailDomain{Domain: d.ID, OwnerID: d.OwnerID, Owner: names[d.OwnerID], Active: d.Active, ActivatedAt: d.ActivatedAt,
			CheckedAt: d.CheckedAt, CheckError: d.CheckError, CreatedAt: d.CreatedAt, Mailboxes: mailboxes[d.ID], Aliases: aliases[d.ID]}
		if o, ok := orgs[deref(d.OrganisationID)]; ok {
			m.OrganisationID, m.OrganisationName, m.OrganisationOwnerID = o.ID, o.Name, o.OwnerID
		}
		out[i] = m
	}
	return out, nil
}

func getMailDomain(ctx context.Context, r repos, domain string) (MailDomain, error) {
	row, err := r.mailDomains.Get(ctx, domain)
	if err != nil {
		return MailDomain{}, missing(err, errMailDomainNotFound)
	}
	list, err := mailDomainsOf(ctx, r, []mailDomainRow{row})
	if err != nil {
		return MailDomain{}, err
	}
	return list[0], nil
}

// MailDomains returns every mail domain, sorted by name.
func (s *Store) MailDomains(ctx context.Context) ([]MailDomain, error) {
	rows, err := s.r.mailDomains.Find(ctx, ablage.Query{})
	if err != nil {
		return nil, err
	}
	return mailDomainsOf(ctx, s.r, rows)
}

// MailDomain returns one mail domain.
func (s *Store) MailDomain(ctx context.Context, domain string) (MailDomain, error) {
	return getMailDomain(ctx, s.r, domain)
}

// dkimKeyRowOf is the row of a key; ActiveDomain is set while it is active.
func dkimKeyRowOf(k DKIMKey) (dkimKeyRow, error) {
	switch k.State {
	case DKIMActive, DKIMPending, DKIMRetired:
	default:
		return dkimKeyRow{}, invalidf("unknown DKIM key state %q", k.State)
	}
	row := dkimKeyRow{Domain: k.Domain, Selector: k.Selector, PrivateKey: k.PrivateKey, PublicKey: k.PublicKey, State: k.State}
	if row.PrivateKey == nil {
		row.PrivateKey = []byte{}
	}
	if k.State == DKIMActive {
		row.ActiveDomain = &row.Domain
	}
	return row, nil
}

// CreateMailDomain stores a new, inactive mail domain with its first DKIM
// key. It refuses a domain that has e-mail already, and an owner who has
// limit mail domains already (limit <= 0: no limit).
func (s *Store) CreateMailDomain(ctx context.Context, domain, ownerID string, key DKIMKey, limit int) (MailDomain, error) {
	key.Domain = domain
	k, err := dkimKeyRowOf(key)
	if err != nil {
		return MailDomain{}, err
	}
	var out MailDomain
	err = s.tx(ctx, func(r repos) error {
		if limit > 0 {
			n, err := r.mailDomains.Count(ctx, ablage.Eq("owner_id", ownerID))
			if err != nil {
				return err
			}
			if n >= int64(limit) {
				return conflictf("at most %d mail domains are possible per person", limit)
			}
		}
		now := s.now()
		if err := r.mailDomains.Create(ctx, &mailDomainRow{ID: domain, OwnerID: ownerID, CreatedAt: now}); err != nil {
			if errors.Is(err, ablage.ErrReference) {
				return errUserNotFound
			}
			return dup(err, conflictf("e-mail is already enabled for %s", domain))
		}
		k.CreatedAt = now
		if err := r.dkimKeys.Create(ctx, &k); err != nil {
			return err
		}
		out, err = getMailDomain(ctx, r, domain)
		return err
	})
	return out, err
}

// RecordMailDomainCheck stores the outcome of looking at a domain's MX
// record. active true activates the domain (for good); false leaves an
// active domain active.
func (s *Store) RecordMailDomainCheck(ctx context.Context, domain string, at time.Time, active bool, problem string) error {
	at = seconds(at)
	if !active {
		return missing(s.r.mailDomains.Patch(ctx, domain, ablage.Row{"checked_at": at, "check_error": problem}), errMailDomainNotFound)
	}
	return s.tx(ctx, func(r repos) error {
		d, err := r.mailDomains.Get(ctx, domain)
		if err != nil {
			return missing(err, errMailDomainNotFound)
		}
		activated := d.ActivatedAt
		if activated == nil {
			activated = &at
		}
		return r.mailDomains.Patch(ctx, domain, ablage.Row{"checked_at": at, "check_error": "", "active": true, "activated_at": activated})
	})
}

// DeleteMailDomain removes a mail domain with its keys, mailboxes and
// aliases. The storage ids of its mailboxes are kept as pending deletes and
// returned.
func (s *Store) DeleteMailDomain(ctx context.Context, domain string) ([]string, error) {
	var ids []string
	err := s.tx(ctx, func(r repos) error {
		list, err := r.mailboxes.Find(ctx, ablage.Query{Where: ablage.Eq("domain", domain), OrderBy: byCreation})
		if err != nil {
			return err
		}
		for _, m := range list {
			ids = append(ids, m.StorageID)
			if err := addPendingDelete(ctx, r, m.StorageID, s.now()); err != nil {
				return err
			}
		}
		return missing(r.mailDomains.Delete(ctx, domain), errMailDomainNotFound)
	})
	return ids, err
}

// DKIMKeys returns the keys of a domain, oldest first.
func (s *Store) DKIMKeys(ctx context.Context, domain string) ([]DKIMKey, error) {
	rows, err := s.r.dkimKeys.Find(ctx, ablage.Query{Where: ablage.Eq("domain", domain), OrderBy: byCreation})
	if err != nil {
		return nil, err
	}
	out := make([]DKIMKey, len(rows))
	for i, k := range rows {
		out[i] = DKIMKey{Domain: k.Domain, Selector: k.Selector, PrivateKey: k.PrivateKey, PublicKey: k.PublicKey, State: k.State,
			CreatedAt: seconds(k.CreatedAt)}
	}
	return out, nil
}

// ---- mailboxes ----

// mailDomainOwners returns the owner of each of the domains (one query).
func mailDomainOwners(ctx context.Context, r repos, domains []string) (map[string]string, error) {
	out := map[string]string{}
	domains = unique(domains)
	if len(domains) == 0 {
		return out, nil
	}
	list, err := r.mailDomains.GetMany(ctx, domains...)
	if err != nil {
		return nil, err
	}
	for _, d := range list {
		out[d.ID] = d.OwnerID
	}
	return out, nil
}

// owners returns the owner and the owner's name for each domain.
func owners(ctx context.Context, r repos, domains []string) (map[string]string, map[string]string, error) {
	ids, err := mailDomainOwners(ctx, r, domains)
	if err != nil {
		return nil, nil, err
	}
	var list []string
	for _, id := range ids {
		list = append(list, id)
	}
	names, err := userNames(ctx, r, list)
	return ids, names, err
}

func mailboxesOf(ctx context.Context, r repos, rows []mailboxRow) ([]Mailbox, error) {
	domains := make([]string, len(rows))
	for i, m := range rows {
		domains[i] = m.Domain
	}
	ids, names, err := owners(ctx, r, domains)
	if err != nil {
		return nil, err
	}
	out := make([]Mailbox, len(rows))
	for i, m := range rows {
		out[i] = mailboxOf(m)
		out[i].OwnerID = ids[m.Domain]
		out[i].Owner = names[out[i].OwnerID]
	}
	return out, nil
}

func mailboxOf(m mailboxRow) Mailbox {
	return Mailbox{ID: m.ID, StorageID: m.StorageID, Domain: m.Domain, LocalPart: m.LocalPart, PasswordHash: m.PasswordHash,
		QuotaBytes: m.QuotaBytes, SendPerHour: m.SendPerHour, SendPerDay: m.SendPerDay, CreatedAt: seconds(m.CreatedAt),
		DisplayName: m.DisplayName, AppPasswordsOnly: m.AppPasswordsOnly, Revision: m.Revision,
		AdminPasswordAt: m.AdminPasswordAt, AdminDisplayNameAt: m.AdminDisplayNameAt,
		AdminAppPasswordsOnlyAt: m.AdminAppPasswordsOnlyAt, Kind: m.Kind, KeepSentCopy: m.KeepSentCopy,
		AdminGrantsAt: m.AdminGrantsAt}
}

func findMailbox(ctx context.Context, r repos, where ablage.Cond) (Mailbox, error) {
	row, err := r.mailboxes.First(ctx, ablage.Query{Where: where})
	if err != nil {
		return Mailbox{}, missing(err, errMailboxNotFound)
	}
	list, err := mailboxesOf(ctx, r, []mailboxRow{row})
	if err != nil {
		return Mailbox{}, err
	}
	return list[0], nil
}

// Mailboxes returns the mailboxes of domain ("": of all domains) in the
// order they were created.
func (s *Store) Mailboxes(ctx context.Context, domain string) ([]Mailbox, error) {
	where := ablage.All
	if domain != "" {
		where = ablage.Eq("domain", domain)
	}
	rows, err := s.r.mailboxes.Find(ctx, ablage.Query{Where: where, OrderBy: byCreation})
	if err != nil {
		return nil, err
	}
	return mailboxesOf(ctx, s.r, rows)
}

// Mailbox returns one mailbox.
func (s *Store) Mailbox(ctx context.Context, id string) (Mailbox, error) {
	if id == "" {
		return Mailbox{}, errMailboxNotFound
	}
	return findMailbox(ctx, s.r, ablage.Eq("id", id))
}

// validMailbox checks what the table checked before ablage.
func validMailbox(quota int64, perHour, perDay int) error {
	if quota < 0 || perHour < 0 || perDay < 0 {
		return invalidf("the quota and the send limits cannot be negative")
	}
	return nil
}

// CreateMailbox stores a new mailbox. It refuses a local part that is
// taken by a mailbox or an alias of the domain, and a domain owner who has
// limit mailboxes already (limit <= 0: no limit).
func (s *Store) CreateMailbox(ctx context.Context, m Mailbox, limit int) (Mailbox, error) {
	if err := validMailbox(m.QuotaBytes, m.SendPerHour, m.SendPerDay); err != nil {
		return Mailbox{}, err
	}
	var out Mailbox
	err := s.tx(ctx, func(r repos) error {
		d, err := r.mailDomains.Get(ctx, m.Domain)
		if err != nil {
			return missing(err, errMailDomainNotFound)
		}
		if err := aliasFree(ctx, r, m.Domain, m.LocalPart, ""); err != nil {
			return err
		}
		if limit > 0 {
			n, err := countMailboxesOf(ctx, r, d.OwnerID)
			if err != nil {
				return err
			}
			if n >= limit {
				return conflictf("at most %d mailboxes are possible per person", limit)
			}
		}
		kind := m.Kind
		if kind == "" {
			kind = "personal"
		}
		row := mailboxRow{StorageID: m.StorageID, Domain: m.Domain, LocalPart: m.LocalPart, PasswordHash: m.PasswordHash,
			QuotaBytes: m.QuotaBytes, SendPerHour: m.SendPerHour, SendPerDay: m.SendPerDay, CreatedAt: s.now(),
			DisplayName: m.DisplayName, Kind: kind, KeepSentCopy: m.KeepSentCopy}
		if err := r.mailboxes.Create(ctx, &row); err != nil {
			if !errors.Is(err, ablage.ErrDuplicate) {
				return err
			}
			if n, e := r.mailboxes.Count(ctx, ablage.And(ablage.Eq("domain", m.Domain), ablage.Eq("local_part", m.LocalPart))); e == nil && n > 0 {
				return conflictf("the mailbox %s@%s already exists", m.LocalPart, m.Domain)
			}
			return conflictf("the storage id is already used; try again")
		}
		out, err = findMailbox(ctx, r, ablage.Eq("id", row.ID))
		return err
	})
	return out, err
}

// countMailboxesOf counts the mailboxes of all mail domains of owner.
func countMailboxesOf(ctx context.Context, r repos, owner string) (int, error) {
	domains, err := r.mailDomains.Find(ctx, ablage.Query{Where: ablage.Eq("owner_id", owner)})
	if err != nil {
		return 0, err
	}
	names := make([]string, len(domains))
	for i, d := range domains {
		names[i] = d.ID
	}
	n, err := r.mailboxes.Count(ctx, ablage.In("domain", names...))
	return int(n), err
}

// SetMailboxPassword replaces a mailbox's password hash (a change by the
// panel: it records AdminPasswordAt).
func (s *Store) SetMailboxPassword(ctx context.Context, id, hash string) error {
	return s.updateMailbox(ctx, id, ablage.Row{"password_hash": hash, "admin_password_at": s.now()})
}

// SetMailboxAccount replaces a mailbox's display name and AppPasswordsOnly;
// nil fields stay as they are. A change by the panel: it records the
// admin time of each field it writes, and only of those.
func (s *Store) SetMailboxAccount(ctx context.Context, id string, displayName *string, appPasswordsOnly *bool) error {
	now := s.now()
	set := ablage.Row{}
	if displayName != nil {
		set["display_name"], set["admin_display_name_at"] = *displayName, now
	}
	if appPasswordsOnly != nil {
		set["app_passwords_only"], set["admin_app_passwords_only_at"] = *appPasswordsOnly, now
	}
	if len(set) == 0 {
		// Nothing to change; the mailbox must exist all the same.
		ok, err := s.r.mailboxes.Exists(ctx, id)
		if err == nil && !ok {
			err = errMailboxNotFound
		}
		return err
	}
	return s.updateMailbox(ctx, id, set)
}

// SetMailboxLimits replaces a mailbox's quota and send limits.
func (s *Store) SetMailboxLimits(ctx context.Context, id string, quota int64, perHour, perDay int) error {
	if err := validMailbox(quota, perHour, perDay); err != nil {
		return err
	}
	return s.updateMailbox(ctx, id, ablage.Row{"quota_bytes": quota, "send_per_hour": perHour, "send_per_day": perDay})
}

func (s *Store) updateMailbox(ctx context.Context, id string, set ablage.Row) error {
	return patchMailbox(ctx, s.r, id, set)
}

func patchMailbox(ctx context.Context, r repos, id string, set ablage.Row) error {
	if id == "" {
		return errMailboxNotFound
	}
	return missing(r.mailboxes.Patch(ctx, id, set), errMailboxNotFound)
}

// DeleteMailbox removes a mailbox and keeps its storage id as a pending
// delete, which it returns.
func (s *Store) DeleteMailbox(ctx context.Context, id string) (string, error) {
	var storageID string
	err := s.tx(ctx, func(r repos) error {
		if id == "" {
			return errMailboxNotFound
		}
		m, err := r.mailboxes.Get(ctx, id)
		if err != nil {
			return missing(err, errMailboxNotFound)
		}
		storageID = m.StorageID
		if err := addPendingDelete(ctx, r, storageID, s.now()); err != nil {
			return err
		}
		return r.mailboxes.Delete(ctx, id)
	})
	return storageID, err
}

func addPendingDelete(ctx context.Context, r repos, storageID string, now time.Time) error {
	return ignoreDup(r.pending.Create(ctx, &pendingDeleteRow{ID: storageID, CreatedAt: now}))
}

// PendingMailDeletes returns the storage ids whose data still has to be
// deleted in the mail service, oldest first.
func (s *Store) PendingMailDeletes(ctx context.Context) ([]string, error) {
	rows, err := s.r.pending.Find(ctx, ablage.Query{OrderBy: byCreation})
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, p := range rows {
		out[i] = p.ID
	}
	return out, nil
}

// DonePendingMailDelete forgets a pending delete the service confirmed.
func (s *Store) DonePendingMailDelete(ctx context.Context, storageID string) error {
	_, err := s.r.pending.DeleteWhere(ctx, ablage.Eq("id", storageID))
	return err
}

// ---- aliases ----

func aliasesOf(ctx context.Context, r repos, rows []mailAliasRow) ([]MailAlias, error) {
	domains := make([]string, len(rows))
	for i, a := range rows {
		domains[i] = a.Domain
	}
	ids, names, err := owners(ctx, r, domains)
	if err != nil {
		return nil, err
	}
	out := make([]MailAlias, len(rows))
	for i, a := range rows {
		out[i] = MailAlias{ID: a.ID, Domain: a.Domain, LocalPart: a.LocalPart, Targets: a.Targets, CreatedAt: a.CreatedAt,
			OwnerID: ids[a.Domain], Owner: names[ids[a.Domain]], SenderPolicy: a.SenderPolicy, HideFromDirectory: a.HideFromDirectory}
		if out[i].Targets == nil {
			out[i].Targets = []string{}
		}
	}
	return out, nil
}

func getAlias(ctx context.Context, r repos, id string) (MailAlias, error) {
	if id == "" {
		return MailAlias{}, errAliasNotFound
	}
	row, err := r.aliases.Get(ctx, id)
	if err != nil {
		return MailAlias{}, missing(err, errAliasNotFound)
	}
	list, err := aliasesOf(ctx, r, []mailAliasRow{row})
	if err != nil {
		return MailAlias{}, err
	}
	return list[0], nil
}

// MailAliases returns the aliases of domain ("": of all domains), sorted by
// domain and local part.
func (s *Store) MailAliases(ctx context.Context, domain string) ([]MailAlias, error) {
	where := ablage.All
	if domain != "" {
		where = ablage.Eq("domain", domain)
	}
	rows, err := s.r.aliases.Find(ctx, ablage.Query{Where: where, OrderBy: []ablage.Order{ablage.Asc("domain"), ablage.Asc("local_part")}})
	if err != nil {
		return nil, err
	}
	return aliasesOf(ctx, s.r, rows)
}

// MailAlias returns one alias.
func (s *Store) MailAlias(ctx context.Context, id string) (MailAlias, error) {
	return getAlias(ctx, s.r, id)
}

// aliasFree refuses a local part that is a mailbox of the domain, or an
// alias other than except.
func aliasFree(ctx context.Context, r repos, domain, local, except string) error {
	n, err := r.aliases.Count(ctx, ablage.And(ablage.Eq("domain", domain), ablage.Eq("local_part", local), ablage.Ne("id", except)))
	if err != nil {
		return err
	}
	if n > 0 {
		return conflictf("%s@%s is already an alias", local, domain)
	}
	return nil
}

func mailboxFree(ctx context.Context, r repos, domain, local string) error {
	n, err := r.mailboxes.Count(ctx, ablage.And(ablage.Eq("domain", domain), ablage.Eq("local_part", local)))
	if err != nil {
		return err
	}
	if n > 0 {
		return conflictf("%s@%s is a mailbox; an address is either a mailbox or an alias", local, domain)
	}
	return nil
}

// senderPolicy checks what the table checked before ablage; "" is "anyone".
func senderPolicy(p string) (string, error) {
	switch p {
	case "":
		return "anyone", nil
	case "anyone", "organisation", "members":
		return p, nil
	}
	return "", invalidf("unknown sender policy %q", p)
}

// CreateMailAlias stores a new alias. The local part must be neither a
// mailbox nor an alias of the domain yet.
func (s *Store) CreateMailAlias(ctx context.Context, a MailAlias) (MailAlias, error) {
	policy, err := senderPolicy(a.SenderPolicy)
	if err != nil {
		return MailAlias{}, err
	}
	var out MailAlias
	err = s.tx(ctx, func(r repos) error {
		ok, err := r.mailDomains.Exists(ctx, a.Domain)
		if err != nil {
			return err
		}
		if !ok {
			return errMailDomainNotFound
		}
		if err := mailboxFree(ctx, r, a.Domain, a.LocalPart); err != nil {
			return err
		}
		if err := aliasFree(ctx, r, a.Domain, a.LocalPart, ""); err != nil {
			return err
		}
		row := mailAliasRow{Domain: a.Domain, LocalPart: a.LocalPart, Targets: targetsOf(a.Targets), CreatedAt: s.now(),
			SenderPolicy: policy, HideFromDirectory: a.HideFromDirectory}
		if err := r.aliases.Create(ctx, &row); err != nil {
			return err
		}
		out, err = getAlias(ctx, r, row.ID)
		return err
	})
	return out, err
}

// targetsOf keeps nil targets as an empty list, as before ablage (JSON
// "null" was never written).
func targetsOf(t []string) []string {
	if t == nil {
		return []string{}
	}
	return t
}

// UpdateMailAlias replaces the local part, targets, sender policy and
// directory switch of an alias (its domain stays), with the checks of
// CreateMailAlias.
func (s *Store) UpdateMailAlias(ctx context.Context, a MailAlias) (MailAlias, error) {
	policy, err := senderPolicy(a.SenderPolicy)
	if err != nil {
		return MailAlias{}, err
	}
	var out MailAlias
	err = s.tx(ctx, func(r repos) error {
		cur, err := getAlias(ctx, r, a.ID)
		if err != nil {
			return err
		}
		if err := mailboxFree(ctx, r, cur.Domain, a.LocalPart); err != nil {
			return err
		}
		if err := aliasFree(ctx, r, cur.Domain, a.LocalPart, a.ID); err != nil {
			return err
		}
		if err := r.aliases.Patch(ctx, a.ID, ablage.Row{"local_part": a.LocalPart, "targets": targetsOf(a.Targets),
			"sender_policy": policy, "hide_from_directory": a.HideFromDirectory}); err != nil {
			return err
		}
		out, err = getAlias(ctx, r, a.ID)
		return err
	})
	return out, err
}

// DeleteMailAlias removes an alias.
func (s *Store) DeleteMailAlias(ctx context.Context, id string) error {
	if id == "" {
		return errAliasNotFound
	}
	return missing(s.r.aliases.Delete(ctx, id), errAliasNotFound)
}

func ownsNoMailDomains(ctx context.Context, r repos, userID, name string) error {
	n, err := r.mailDomains.Count(ctx, ablage.Eq("owner_id", userID))
	if err != nil {
		return err
	}
	if n > 0 {
		return conflictf("%s still owns mail domains; delete them first", name)
	}
	return nil
}
