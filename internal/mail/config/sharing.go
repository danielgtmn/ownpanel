package config

// Shared mailboxes, delegation and distribution groups (G3 in
// wiki/Groupware-Architecture.md).
//
// A mailbox is personal (people sign in to it) or shared (nobody signs in;
// personal mailboxes of the same organisation get rights on it through
// grants). A personal mailbox may also delegate itself: its grants work the
// same way. Grants never cross an organisation, are never given to the
// mailbox itself and are never held by a shared mailbox (only personal
// mailboxes sign in). Validate refuses what breaks these rules, DropInvalid
// leaves such grants out, and the mail service checks them again on every
// load (directory.New), so a grant that no longer fits is ignored.

import "fmt"

// Mailbox kinds. "" means KindPersonal. KindResource comes with G5b.
const (
	KindPersonal = "personal"
	KindShared   = "shared"
)

// Rights a grant gives, as they appear in the configuration.
const (
	// RightRead: see folders and messages, with an own read state.
	RightRead = "read"
	// RightWrite: flags, moving, deleting, creating folders (implies read).
	RightWrite = "write"
	// RightManage: filters and out-of-office of the mailbox.
	RightManage = "manage"
	// RightSendAs: From (and envelope sender) may be the mailbox's address
	// and its aliases.
	RightSendAs = "send-as"
	// RightSendOnBehalf: From may be the mailbox's address when Sender is
	// the signed-in mailbox's own address.
	RightSendOnBehalf = "send-on-behalf"
)

// Rights is a set of rights.
type Rights uint8

// The rights as bits.
const (
	Read Rights = 1 << iota
	Write
	Manage
	SendAs
	SendOnBehalf
)

// AllRights is every right; a mailbox has them on itself.
const AllRights = Read | Write | Manage | SendAs | SendOnBehalf

var rightNames = []struct {
	r    Rights
	name string
}{
	{Read, RightRead}, {Write, RightWrite}, {Manage, RightManage}, {SendAs, RightSendAs}, {SendOnBehalf, RightSendOnBehalf},
}

// ParseRights reads a list of right names. Write implies Read. An empty
// list, an unknown name or a name listed twice is an error.
func ParseRights(names []string) (Rights, error) {
	var r Rights
	if len(names) == 0 {
		return 0, fmt.Errorf("a grant needs at least one right")
	}
	for _, n := range names {
		var bit Rights
		for _, rn := range rightNames {
			if rn.name == n {
				bit = rn.r
			}
		}
		if bit == 0 {
			return 0, fmt.Errorf("unknown right %q", n)
		}
		if r&bit != 0 {
			return 0, fmt.Errorf("right %q is listed twice", n)
		}
		r |= bit
	}
	if r&Write != 0 {
		r |= Read
	}
	return r, nil
}

// Has tells whether r contains every right of x.
func (r Rights) Has(x Rights) bool { return r&x == x }

// Names lists the rights in a fixed order.
func (r Rights) Names() []string {
	out := []string{}
	for _, n := range rightNames {
		if r&n.r != 0 {
			out = append(out, n.name)
		}
	}
	return out
}

// Grant gives the personal mailbox with StorageID rights on the mailbox
// that lists it.
type Grant struct {
	StorageID string   `json:"storage_id"`
	Rights    []string `json:"rights"`
}

// MaxGrants bounds the grants of one mailbox.
const MaxGrants = 100

// Sender policies of an alias used as a distribution group.
const (
	// SenderAnyone: everyone may write to the address ("" means this).
	SenderAnyone = "anyone"
	// SenderOrganisation: only addresses of the group's organisation.
	SenderOrganisation = "organisation"
	// SenderMembers: only the group's members.
	SenderMembers = "members"
)

// ValidSenderPolicy tells whether p is a sender policy ("" included).
func ValidSenderPolicy(p string) bool {
	return p == "" || p == SenderAnyone || p == SenderOrganisation || p == SenderMembers
}

// Shared tells whether the mailbox is a shared mailbox.
func (m *Mailbox) Shared() bool { return m.Kind == KindShared }

// validKind tells whether k is a mailbox kind this version knows.
func validKind(k string) bool { return k == "" || k == KindPersonal || k == KindShared }

// validateGrantForm checks a grant of mailbox m on its own (not the
// mailbox it names).
func validateGrantForm(m Mailbox, g Grant) error {
	if !ValidStorageID(g.StorageID) {
		return fmt.Errorf("grant to %q: not a storage id", g.StorageID)
	}
	if g.StorageID == m.StorageID {
		return fmt.Errorf("a mailbox cannot be given rights on itself")
	}
	if _, err := ParseRights(g.Rights); err != nil {
		return fmt.Errorf("grant to %s: %v", g.StorageID, err)
	}
	return nil
}

// organisationOf is the organisation of a domain as the mail service sees
// it (StandaloneOrganisation for a domain without one).
func organisationOf(d Domain) string {
	if d.Organisation == "" {
		return StandaloneOrganisation(d.Name)
	}
	return d.Organisation
}

// GrantProblem tells why grant g of mailbox owner does not fit the other
// mailboxes (by storage id) and the organisations of the domains (by
// name), or "" if it does: the grantee must exist, be personal and belong
// to owner's organisation.
func GrantProblem(owner Mailbox, g Grant, byStorage map[string]*Mailbox, orgs map[string]string) string {
	grantee, ok := byStorage[g.StorageID]
	if !ok {
		return "the mailbox it names does not exist"
	}
	if grantee.Shared() {
		return "it is given to a shared mailbox"
	}
	_, od, _ := SplitAddress(owner.Address)
	_, gd, _ := SplitAddress(grantee.Address)
	if orgs[od] == "" || orgs[od] != orgs[gd] {
		return "the mailbox it names belongs to another organisation"
	}
	return ""
}

// domainOrganisations maps every domain of c to its organisation.
func (c *Config) domainOrganisations() map[string]string {
	out := make(map[string]string, len(c.Domains))
	for _, d := range c.Domains {
		out[d.Name] = organisationOf(d)
	}
	return out
}

func (c *Config) mailboxesByStorage() map[string]*Mailbox {
	out := make(map[string]*Mailbox, len(c.Mailboxes))
	for i := range c.Mailboxes {
		out[c.Mailboxes[i].StorageID] = &c.Mailboxes[i]
	}
	return out
}

// CheckGrants checks the grants of mailbox m on their own: at most
// MaxGrants, each valid (validateGrantForm), none for the same mailbox
// twice. Whether the mailboxes they name fit is GrantProblem.
func CheckGrants(m Mailbox, grants []Grant) error {
	if len(grants) > MaxGrants {
		return fmt.Errorf("more than %d grants", MaxGrants)
	}
	seen := map[string]bool{}
	for _, g := range grants {
		if err := validateGrantForm(m, g); err != nil {
			return err
		}
		if seen[g.StorageID] {
			return fmt.Errorf("two grants for the same mailbox")
		}
		seen[g.StorageID] = true
	}
	return nil
}

// OrganisationOf returns the organisation of the domain named domain in c
// ("" if c has no such domain).
func (c *Config) OrganisationOf(domain string) string { return c.domainOrganisations()[domain] }
