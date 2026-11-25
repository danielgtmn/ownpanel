// Package directory is the mail service's immutable view of a loaded
// configuration: who exists and where mail for an address goes. A new
// Directory is built on every load and swapped in atomically, so a session
// can keep using the one it started with.
package directory

import (
	"slices"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// MaxAliasDepth bounds alias expansion.
const MaxAliasDepth = 10

// Directory is built from one configuration and never changes.
type Directory struct {
	cfg       *config.Config
	limits    config.Limits
	domains   map[string]*config.Domain
	mailboxes map[string]*config.Mailbox // by address
	byStorage map[string]*config.Mailbox
	aliases   map[string][]string
	// firstMailbox is the domain's first mailbox in config order (the panel
	// sends them ordered by creation, so this is the oldest one).
	firstMailbox map[string]*config.Mailbox
}

// New builds a Directory from a validated configuration. cfg must not be
// changed afterwards.
func New(cfg *config.Config) *Directory {
	d := &Directory{
		cfg:          cfg,
		limits:       cfg.Limits.WithDefaults(),
		domains:      map[string]*config.Domain{},
		mailboxes:    map[string]*config.Mailbox{},
		byStorage:    map[string]*config.Mailbox{},
		aliases:      map[string][]string{},
		firstMailbox: map[string]*config.Mailbox{},
	}
	for i := range cfg.Domains {
		d.domains[cfg.Domains[i].Name] = &cfg.Domains[i]
	}
	for i := range cfg.Mailboxes {
		m := &cfg.Mailboxes[i]
		d.mailboxes[m.Address] = m
		d.byStorage[m.StorageID] = m
		_, dom, _ := config.SplitAddress(m.Address)
		if d.firstMailbox[dom] == nil {
			d.firstMailbox[dom] = m
		}
	}
	for _, a := range cfg.Aliases {
		d.aliases[a.Address] = a.Targets
	}
	return d
}

// Config is the configuration the Directory was built from. Callers must
// not change it.
func (d *Directory) Config() *config.Config { return d.cfg }

// Hostname is the server's mail name.
func (d *Directory) Hostname() string { return d.cfg.Hostname }

// Limits are the service limits with defaults applied.
func (d *Directory) Limits() config.Limits { return d.limits }

// Domain returns a configured domain.
func (d *Directory) Domain(name string) (*config.Domain, bool) {
	dom, ok := d.domains[name]
	return dom, ok
}

// Mailbox returns the mailbox with an address (normalized by Normalize).
func (d *Directory) Mailbox(addr string) (*config.Mailbox, bool) {
	m, ok := d.mailboxes[addr]
	return m, ok
}

// MailboxByStorageID returns a configured mailbox by its storage id.
func (d *Directory) MailboxByStorageID(id string) (*config.Mailbox, bool) {
	m, ok := d.byStorage[id]
	return m, ok
}

// Normalize returns an address in the form the configuration uses:
// lowercase. Local parts are compared without case, like nearly every mail
// server does in practice.
func Normalize(addr string) string { return strings.ToLower(strings.TrimSpace(addr)) }

// Outcome says whether an address can receive mail.
type Outcome int

const (
	// Deliver: Route.Mailboxes has at least one local mailbox.
	Deliver Outcome = iota
	// Unknown: no such recipient here (550 5.1.1).
	Unknown
	// ForwardOnly: the address only forwards outside, which is not
	// available yet (550, nothing is accepted and then dropped).
	ForwardOnly
	// NoPostmaster: postmaster@/abuse@ without anyone to receive it
	// (temporary error).
	NoPostmaster
)

// Route is where mail for one recipient goes.
type Route struct {
	Mailboxes []*config.Mailbox
	// External targets reached through aliases; not delivered yet.
	External []string
	// Dropped targets that lead nowhere (unknown local addresses, loops
	// deeper than MaxAliasDepth).
	Dropped []string
}

// Resolve tells where mail for addr goes. addr must be normalized.
//
// Accepted are: a mailbox; an alias; postmaster@ and abuse@ of every
// configured domain and of the hostname (to the alias of that name, else
// the domain's oldest mailbox, else the postmaster address); and the
// catch-all of a configured domain.
func (d *Directory) Resolve(addr string) (Route, Outcome) {
	var r Route
	local, dom, ok := config.SplitAddress(addr)
	if !ok {
		return r, Unknown
	}
	if m, ok := d.mailboxes[addr]; ok {
		r.Mailboxes = []*config.Mailbox{m}
		return r, Deliver
	}
	if targets, ok := d.aliases[addr]; ok {
		return d.expand(targets)
	}
	_, isDomain := d.domains[dom]
	if (local == "postmaster" || local == "abuse") && (isDomain || dom == d.cfg.Hostname) {
		if isDomain {
			if m := d.firstMailbox[dom]; m != nil {
				r.Mailboxes = []*config.Mailbox{m}
				return r, Deliver
			}
		}
		pm := d.cfg.PostmasterAddress
		if pm == "" || pm == addr {
			return r, NoPostmaster
		}
		r, out := d.expand([]string{pm})
		if out != Deliver {
			// The postmaster must stay reachable later: ask the sender to
			// retry rather than refuse for good.
			return r, NoPostmaster
		}
		return r, Deliver
	}
	if isDomain {
		if targets, ok := d.aliases["*@"+dom]; ok {
			return d.expand(targets)
		}
	}
	return r, Unknown
}

// expand resolves alias targets recursively, removing duplicates. A target
// in a configured domain that is neither a mailbox nor an alias goes to
// the domain's catch-all, if any.
func (d *Directory) expand(targets []string) (Route, Outcome) {
	var r Route
	seenBox := map[string]bool{}
	seenAddr := map[string]bool{}
	var walk func(ts []string, depth int)
	walk = func(ts []string, depth int) {
		for _, t := range ts {
			t = Normalize(t)
			if seenAddr[t] {
				continue
			}
			seenAddr[t] = true
			if m, ok := d.mailboxes[t]; ok {
				if !seenBox[m.StorageID] {
					seenBox[m.StorageID] = true
					r.Mailboxes = append(r.Mailboxes, m)
				}
				continue
			}
			local, dom, ok := config.SplitAddress(t)
			if !ok {
				r.Dropped = append(r.Dropped, t)
				continue
			}
			if _, isDomain := d.domains[dom]; !isDomain {
				if !slices.Contains(r.External, t) {
					r.External = append(r.External, t)
				}
				continue
			}
			next, ok := d.aliases[t]
			if !ok && local != "*" {
				next, ok = d.aliases["*@"+dom]
			}
			if !ok || depth >= MaxAliasDepth {
				r.Dropped = append(r.Dropped, t)
				continue
			}
			walk(next, depth+1)
		}
	}
	walk(targets, 1)
	switch {
	case len(r.Mailboxes) > 0:
		return r, Deliver
	case len(r.External) > 0:
		return r, ForwardOnly
	default:
		return r, Unknown
	}
}
