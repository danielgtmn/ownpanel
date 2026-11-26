package panel

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/danielgtmn/sesam"

	"github.com/danielgtmn/ownpanel/internal/store"
)

func validateRole(role string) error {
	if role != store.RoleAdmin && role != store.RoleMember {
		return errorf(Invalid, "the role must be %q or %q", store.RoleAdmin, store.RoleMember)
	}
	return nil
}

// Users lists all users (admins only).
func (s *Service) Users(ctx context.Context, a Actor) ([]User, error) {
	if err := needAdmin(a); err != nil {
		return nil, err
	}
	list, err := s.store.Users(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	auth, err := s.authUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]User, 0, len(list))
	for _, u := range list {
		out = append(out, userView(u, auth[u.AuthUserID]))
	}
	return out, nil
}

// NewUser is a user an admin creates. Email is optional.
type NewUser struct {
	Name     string
	Email    string
	Password string
	Role     string
}

// CreateUser adds a user (admins only).
func (s *Service) CreateUser(ctx context.Context, a Actor, in NewUser) (User, error) {
	if err := needAdmin(a); err != nil {
		return User{}, err
	}
	name := normalizeUserName(in.Name)
	if err := validateUserName(name); err != nil {
		return User{}, err
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return User{}, err
	}
	if err := validateRole(in.Role); err != nil {
		return User{}, err
	}
	if err := validatePassword(in.Password); err != nil {
		return User{}, err
	}
	if _, err := s.store.UserByName(ctx, name); err == nil {
		return User{}, errorf(Conflict, "the user name %q is already taken", name)
	}
	au, err := s.pw.CreateUser(ctx, sesam.NewUser{Username: name, Email: email}, in.Password)
	if err != nil {
		return User{}, authError(err)
	}
	u, err := s.store.CreateUser(ctx, name, au.ID, in.Role, false)
	if err != nil {
		s.dropAuthUser(ctx, au.ID)
		return User{}, storeError(err)
	}
	detail := "role " + in.Role
	if email != "" {
		detail += ", with e-mail address"
	}
	s.record(ctx, a, "user.create", u.Name, detail)
	return userView(u, au), nil
}

// UserUpdate changes the fields that are not nil. An empty Email removes
// the address.
type UserUpdate struct {
	Role     *string
	Password *string
	Email    *string
}

// UpdateUser changes a user's role, e-mail address and/or password (admins
// only). A new password ends all of the user's sessions.
func (s *Service) UpdateUser(ctx context.Context, a Actor, userID string, up UserUpdate) (User, error) {
	if err := needAdmin(a); err != nil {
		return User{}, err
	}
	id := userID
	u, err := s.store.User(ctx, id)
	if err != nil {
		return User{}, storeError(err)
	}
	if up.Role != nil {
		if err := validateRole(*up.Role); err != nil {
			return User{}, err
		}
	}
	if up.Email != nil {
		if _, err := normalizeEmail(*up.Email); err != nil {
			return User{}, err
		}
	}
	if up.Password != nil {
		if err := validatePassword(*up.Password); err != nil {
			return User{}, err
		}
	}
	var changes []string
	if up.Role != nil && *up.Role != u.Role {
		if err := s.store.SetRole(ctx, id, *up.Role); err != nil {
			return User{}, storeError(err)
		}
		changes = append(changes, "role "+*up.Role)
	}
	if up.Email != nil {
		_, changed, err := s.setEmail(ctx, u, *up.Email)
		if err != nil {
			return User{}, s.recordPartial(ctx, a, u.Name, changes, err)
		}
		if changed != "" {
			changes = append(changes, changed)
		}
	}
	if up.Password != nil {
		au, err := s.authUser(ctx, u)
		if err == nil {
			err = authError(s.pw.SetPassword(ctx, au.ID, *up.Password))
		}
		if err != nil {
			return User{}, s.recordPartial(ctx, a, u.Name, changes, err)
		}
		changes = append(changes, "password changed")
	}
	if len(changes) > 0 {
		s.record(ctx, a, "user.update", u.Name, strings.Join(changes, ", "))
	}
	if u, err = s.store.User(ctx, id); err != nil {
		return User{}, storeError(err)
	}
	au, err := s.authUser(ctx, u)
	if err != nil {
		return User{}, err
	}
	return userView(u, au), nil
}

// recordPartial logs the changes made before err stopped an update, and
// returns err.
func (s *Service) recordPartial(ctx context.Context, a Actor, name string, changes []string, err error) error {
	if len(changes) > 0 {
		s.record(ctx, a, "user.update", name, strings.Join(changes, ", "))
	}
	return err
}

// DeleteUser removes a user (admins only). Refused while the user owns
// sites, and for the last admin.
func (s *Service) DeleteUser(ctx context.Context, a Actor, userID string) error {
	if err := needAdmin(a); err != nil {
		return err
	}
	id := userID
	u, err := s.store.User(ctx, id)
	if err != nil {
		return storeError(err)
	}
	if err := s.store.DeleteUser(ctx, id); err != nil {
		return storeError(err)
	}
	// With the panel user gone its sign-in no longer opens the panel; the
	// sign-in user (sessions, password) goes too.
	if u.AuthUserID != "" {
		s.dropAuthUser(ctx, u.AuthUserID)
	}
	s.record(ctx, a, "user.delete", u.Name, "")
	return nil
}

// Activity is one entry of the activity log.
type Activity struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
}

// Activities returns the newest log entries (admins only). limit is clamped
// to 1..200; 0 means 50.
func (s *Service) Activities(ctx context.Context, a Actor, limit int) ([]Activity, error) {
	if err := needAdmin(a); err != nil {
		return nil, err
	}
	switch {
	case limit <= 0:
		limit = 50
	case limit > 200:
		limit = 200
	}
	list, err := s.store.Activities(ctx, limit)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]Activity, 0, len(list))
	for _, e := range list {
		out = append(out, Activity{ID: e.ID, At: e.At, Actor: e.Actor, Action: e.Action, Target: e.Target, Detail: e.Detail})
	}
	return out, nil
}

// record writes an activity entry. A failure is logged, not returned: the
// change itself has happened.
func (s *Service) record(ctx context.Context, a Actor, action, target, detail string) {
	err := s.store.AddActivity(ctx, store.Activity{Actor: a.label(), Action: action, Target: target, Detail: detail})
	if err != nil {
		slog.Error("could not write the activity log", "action", action, "err", err)
	}
}
