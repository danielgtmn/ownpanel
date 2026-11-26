package store

import (
	"context"
	"time"

	"github.com/danielgtmn/ablage"
)

// Moving sign-in to the auth tables: panel users created before it have a
// password hash and sessions here and no auth user yet. The panel moves them
// once at start (internal/panel, moveUsersToAuth); these are the reads and
// the final step it needs.

// UnmovedUser is a user without an auth user yet, with the password hash
// still kept here.
type UnmovedUser struct {
	ID           string
	Name         string
	PasswordHash string
}

// UnmovedUsers lists users that have no auth user yet, oldest first.
func (s *Store) UnmovedUsers(ctx context.Context) ([]UnmovedUser, error) {
	list, err := s.r.users.Find(ctx, ablage.Query{
		Where:   ablage.Null("auth_user_id"),
		OrderBy: []ablage.Order{ablage.Asc("created_at")},
	})
	if err != nil {
		return nil, err
	}
	var out []UnmovedUser
	for _, u := range list {
		out = append(out, UnmovedUser{ID: u.ID, Name: u.Name, PasswordHash: u.PasswordHash})
	}
	return out, nil
}

// OldSession is a session kept here before sign-in moved. ID is the hex
// SHA-256 of the cookie value, as in the auth tables.
type OldSession struct {
	ID        string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// OldSessions lists a user's sessions here that have not expired at now.
func (s *Store) OldSessions(ctx context.Context, userID string, now time.Time) ([]OldSession, error) {
	list, err := s.r.sessions.Find(ctx, ablage.Query{
		Where:   ablage.And(ablage.Eq("user_id", userID), ablage.Gt("expires_at", seconds(now))),
		OrderBy: []ablage.Order{ablage.Asc("created_at")},
	})
	if err != nil {
		return nil, err
	}
	var out []OldSession
	for _, o := range list {
		out = append(out, OldSession{ID: o.ID, CreatedAt: o.CreatedAt, ExpiresAt: o.ExpiresAt})
	}
	return out, nil
}

// LinkAuthUser links a user to its auth user, forgets the password hash
// kept here and removes the user's sessions here (they were copied). It
// fails with ErrConflict when the user is linked already.
func (s *Store) LinkAuthUser(ctx context.Context, id, authUserID string) error {
	return s.tx(ctx, func(r repos) error {
		n, err := r.users.UpdateWhere(ctx, ablage.And(ablage.Eq("id", id), ablage.Null("auth_user_id")),
			ablage.Row{"auth_user_id": authUserID, "password_hash": ""})
		if err != nil {
			return err
		}
		if n == 0 {
			return conflictf("the user is linked already")
		}
		_, err = r.sessions.DeleteWhere(ctx, ablage.Eq("user_id", id))
		return err
	})
}

// AuthUserIDs returns the auth user ids that panel users are linked to.
func (s *Store) AuthUserIDs(ctx context.Context) (map[string]bool, error) {
	list, err := s.r.users.Find(ctx, ablage.Query{Where: ablage.NotNull("auth_user_id")})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, u := range list {
		out[*u.AuthUserID] = true
	}
	return out, nil
}

// AddOldSession stores a session as kept before sign-in moved. Only for
// tests of the move (a database from before has them already).
func (s *Store) AddOldSession(ctx context.Context, userID string, o OldSession) error {
	return s.r.sessions.Create(ctx, &sessionRow{ID: o.ID, UserID: userID, CreatedAt: seconds(o.CreatedAt), ExpiresAt: seconds(o.ExpiresAt)})
}

// AddUnmovedUser stores a user as created before sign-in moved, with a
// password hash here and no auth user. Only for tests of the move.
func (s *Store) AddUnmovedUser(ctx context.Context, name, passwordHash, role string) (User, error) {
	row := userRow{ID: s.NewUserID(), Name: name, PasswordHash: passwordHash, Role: role, CreatedAt: s.now()}
	if err := s.r.users.Create(ctx, &row); err != nil {
		return User{}, dup(err, conflictf("the user name %q is already taken", name))
	}
	return userOf(row), nil
}
