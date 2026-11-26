package dav

import (
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
)

// Privileges are what the signed-in mailbox may do with a collection or
// home (RFC 3744 privileges, as current-user-privilege-set reports them).
type Privileges uint8

const (
	PrivRead Privileges = 1 << iota
	// PrivWriteContent: create, change and delete objects.
	PrivWriteContent
	// PrivWriteProperties: change the collection's name, colour,
	// description.
	PrivWriteProperties
	// PrivBind and PrivUnbind: create and delete collections (homes) or
	// objects.
	PrivBind
	PrivUnbind

	PrivNone Privileges = 0
	PrivAll             = PrivRead | PrivWriteContent | PrivWriteProperties | PrivBind | PrivUnbind
)

// Has tells whether all of q are in p.
func (p Privileges) Has(q Privileges) bool { return p&q == q }

// Authorizer decides who sees which calendars and address books. G4a
// only lets a mailbox see its own; sharing (G4b) plugs in here with an
// implementation that also knows shares, checked on every request.
type Authorizer interface {
	// Home resolves the address in a URL (/dav/calendars/<address>/…) to
	// the mailbox whose data it names, if user may see anything of it.
	Home(dir *directory.Directory, user *config.Mailbox, address string) (*config.Mailbox, bool)
	// Collection returns user's privileges on owner's collection of kind
	// (davobj.Calendar or davobj.AddressBook) named name; PrivNone hides
	// it. name "" asks for the home itself.
	Collection(dir *directory.Directory, user, owner *config.Mailbox, kind, name string) Privileges
}

// OwnOnly is the Authorizer of G4a: a mailbox has every privilege on its
// own homes and collections and sees nothing of others.
type OwnOnly struct{}

func (OwnOnly) Home(dir *directory.Directory, user *config.Mailbox, address string) (*config.Mailbox, bool) {
	if directory.Normalize(address) != user.Address {
		return nil, false
	}
	return user, true
}

func (OwnOnly) Collection(_ *directory.Directory, user, owner *config.Mailbox, _, _ string) Privileges {
	if user.StorageID != owner.StorageID {
		return PrivNone
	}
	return PrivAll
}
