package directory

import (
	"fmt"
	"slices"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// Shared mailboxes, delegation and distribution groups (G3). The rules are
// checked here again on every load, whatever the panel pushed and whatever
// mailboxes set for themselves: a grant only counts when the mailbox it
// names exists, is personal and belongs to the same organisation as the
// mailbox it is on (config.GrantProblem). Grants that do not fit are
// ignored and listed by Ignored, so the service can log them.

// Access is a mailbox someone has rights on through a grant.
type Access struct {
	Mailbox *config.Mailbox
	Rights  config.Rights
}

// Grantee is a mailbox that has rights on another one.
type Grantee struct {
	Mailbox *config.Mailbox
	Rights  config.Rights
}

type grantKey struct{ owner, grantee string }

// buildGrants fills the grant tables of d from its configuration.
func (d *Directory) buildGrants() {
	d.rights = map[grantKey]config.Rights{}
	d.access = map[string][]Access{}
	d.grantees = map[string][]Grantee{}
	orgs := map[string]string{}
	for name := range d.domains {
		orgs[name], _ = d.Organisation(name)
	}
	for _, m := range d.byStorage {
		for _, g := range m.Grants {
			r, err := config.ParseRights(g.Rights)
			problem := config.GrantProblem(*m, g, d.byStorage, orgs)
			if err != nil {
				problem = err.Error()
			}
			if g.StorageID == m.StorageID {
				problem = "a mailbox cannot be given rights on itself"
			}
			if problem != "" {
				d.ignored = append(d.ignored, fmt.Sprintf("mailbox %s: grant to %s ignored: %s", m.Address, g.StorageID, problem))
				continue
			}
			k := grantKey{m.StorageID, g.StorageID}
			if _, dup := d.rights[k]; dup {
				d.ignored = append(d.ignored, fmt.Sprintf("mailbox %s: second grant to %s ignored", m.Address, g.StorageID))
				continue
			}
			d.rights[k] = r
			grantee := d.byStorage[g.StorageID]
			d.access[g.StorageID] = append(d.access[g.StorageID], Access{Mailbox: m, Rights: r})
			d.grantees[m.StorageID] = append(d.grantees[m.StorageID], Grantee{Mailbox: grantee, Rights: r})
		}
	}
	for _, l := range d.access {
		slices.SortFunc(l, func(a, b Access) int { return strings.Compare(a.Mailbox.Address, b.Mailbox.Address) })
	}
	for _, l := range d.grantees {
		slices.SortFunc(l, func(a, b Grantee) int { return strings.Compare(a.Mailbox.Address, b.Mailbox.Address) })
	}
	slices.Sort(d.ignored)
}

// Ignored lists the grants of the configuration that do not count, one line
// each, without secrets.
func (d *Directory) Ignored() []string { return d.ignored }

// Rights returns what the mailbox grantee may do with the mailbox owner (both
// storage ids): every right on itself (unless it is shared: nobody signs in
// to it), else what a grant gives, else nothing.
func (d *Directory) Rights(owner, grantee string) config.Rights {
	if owner == grantee {
		if m, ok := d.byStorage[owner]; ok && !m.Shared() {
			return config.AllRights
		}
		return 0
	}
	return d.rights[grantKey{owner, grantee}]
}

// Access lists the mailboxes grantee (a storage id) has rights on, sorted by
// address.
func (d *Directory) Access(grantee string) []Access { return d.access[grantee] }

// Grantees lists the mailboxes that have rights on owner (a storage id),
// sorted by address.
func (d *Directory) Grantees(owner string) []Grantee { return d.grantees[owner] }

// SendMode says how a mailbox may use an address as its sender.
type SendMode int

const (
	// SendDenied: not at all.
	SendDenied SendMode = iota
	// SendOwn: its own address or one of its aliases (MaySendAs).
	SendOwn
	// SendAsGrant: the address (or an alias) of a mailbox it may send as.
	SendAsGrant
	// SendOnBehalfGrant: the address of a mailbox it may send on behalf of;
	// From only with a Sender header of its own.
	SendOnBehalfGrant
)

// SendMode tells how mailbox m may send as addr (normalized) and for which
// mailbox: its own address and aliases (m itself), the address of a mailbox
// it has the send-as right on or an alias of it, or exactly the address of a
// mailbox it has the send-on-behalf right on.
func (d *Directory) SendMode(m *config.Mailbox, addr string) (SendMode, *config.Mailbox) {
	if d.MaySendAs(m, addr) {
		return SendOwn, m
	}
	var owners []*config.Mailbox
	if o, ok := d.mailboxes[addr]; ok {
		owners = []*config.Mailbox{o}
	} else if local, _, ok := config.SplitAddress(addr); ok && local != "*" {
		if targets, ok := d.aliases[addr]; ok {
			r, _ := d.expand(targets)
			owners = r.Mailboxes
		}
	}
	var behalf *config.Mailbox
	for _, o := range owners {
		r := d.Rights(o.StorageID, m.StorageID)
		if r.Has(config.SendAs) {
			return SendAsGrant, o
		}
		if r.Has(config.SendOnBehalf) && o.Address == addr && behalf == nil {
			behalf = o
		}
	}
	if behalf != nil {
		return SendOnBehalfGrant, behalf
	}
	return SendDenied, nil
}

// Group returns the alias with address addr (normalized), if there is one.
func (d *Directory) Group(addr string) (*config.Alias, bool) {
	a, ok := d.groups[addr]
	return a, ok
}

// GroupAllows tells whether sender (a normalized address, "" for the null
// sender) may write to the alias g under its sender policy. boxes are the
// mailboxes the sender is known to act as (the signed-in mailbox and the one
// it sends for); they count as members when the group delivers to them.
// The organisation is that of the sender address's domain, which must be
// one of ours. Whether the sender really is who it claims is up to the
// caller (a sign-in at submission, an aligned SPF or DKIM pass on port 25).
func (d *Directory) GroupAllows(g *config.Alias, sender string, boxes ...*config.Mailbox) bool {
	switch g.SenderPolicy {
	case "", config.SenderAnyone:
		return true
	}
	_, gd, _ := config.SplitAddress(g.Address)
	groupOrg, ok := d.Organisation(gd)
	if !ok {
		return false
	}
	_, sd, ok := config.SplitAddress(sender)
	if !ok {
		return false
	}
	senderOrg, ok := d.Organisation(sd)
	if !ok || senderOrg != groupOrg {
		return false
	}
	if g.SenderPolicy == config.SenderOrganisation {
		return true
	}
	// Members: the sender address is a target, or a mailbox it acts as (or
	// that owns the address) receives the group's mail.
	if slices.Contains(g.Targets, sender) {
		return true
	}
	r, _ := d.expand(g.Targets)
	for _, m := range r.Mailboxes {
		if d.MaySendAs(m, sender) {
			return true
		}
		for _, b := range boxes {
			if b != nil && b.StorageID == m.StorageID {
				return true
			}
		}
	}
	return false
}
