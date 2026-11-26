package panel

import (
	"slices"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// How an actor authenticated.
const (
	ViaSession = "session"
	ViaToken   = "token"
	ViaSystem  = "system"
)

// Actor is who performs an operation. Front doors get one from
// SessionActor or TokenActor; the zero Actor may do nothing.
type Actor struct {
	UserID   string
	Name     string
	Admin    bool
	ReadOnly bool     // token with access "read"
	Sites    []string // non-nil: token limited to these site names
	Via      string   // "session" | "token" | "system"

	sessionID  string // hash of the session, to keep it on password change
	authUserID string // the sign-in user (sessions only)
	tokenName  string // for the activity log
	tokenHash  string // to re-check the token later (upload links)
}

// System is the panel itself, e.g. the background reconcile loop.
var System = Actor{Name: "system", Admin: true, Via: ViaSystem}

func (a Actor) authenticated() bool {
	switch a.Via {
	case ViaSession, ViaToken:
		return a.UserID != ""
	case ViaSystem:
		return true
	}
	return false
}

// administers: admin rights over users and the activity log. Read-only and
// site-limited tokens never have them, even when they belong to an admin.
func (a Actor) administers() bool {
	return a.authenticated() && a.Admin && !a.ReadOnly && a.Sites == nil
}

func (a Actor) canSee(site store.Site) bool {
	if !a.authenticated() {
		return false
	}
	if a.Sites != nil && !slices.Contains(a.Sites, site.Name) {
		return false
	}
	return a.Admin || site.OwnerID == a.UserID
}

// label names the actor in the activity log.
func (a Actor) label() string {
	if a.Via == ViaToken {
		return a.Name + " via token " + a.tokenName
	}
	return a.Name
}

func needAuth(a Actor) error {
	if !a.authenticated() {
		return errorf(Unauthenticated, "please sign in")
	}
	return nil
}

func needAdmin(a Actor) error {
	if err := needAuth(a); err != nil {
		return err
	}
	if !a.administers() {
		return errorf(Forbidden, "only admins can do this, signed in or with an unrestricted write token")
	}
	return nil
}

func needSession(a Actor, what string) error {
	if err := needAuth(a); err != nil {
		return err
	}
	if a.Via != ViaSession {
		return errorf(Forbidden, "%s only works when signed in with a password, not with an access token", what)
	}
	return nil
}
