package panel

import (
	"context"
	"log/slog"
	"strings"
	"time"

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
	out := make([]User, 0, len(list))
	for _, u := range list {
		out = append(out, userView(u))
	}
	return out, nil
}

// CreateUser adds a user (admins only).
func (s *Service) CreateUser(ctx context.Context, a Actor, name, password, role string) (User, error) {
	if err := needAdmin(a); err != nil {
		return User{}, err
	}
	name = normalizeUserName(name)
	if err := validateUserName(name); err != nil {
		return User{}, err
	}
	if err := validateRole(role); err != nil {
		return User{}, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, newError(Internal, err)
	}
	u, err := s.store.CreateUser(ctx, name, hash, role, false)
	if err != nil {
		return User{}, storeError(err)
	}
	s.record(ctx, a, "user.create", u.Name, "role "+role)
	return userView(u), nil
}

// UpdateUser changes a user's role and/or password (admins only). A new
// password ends the user's sessions (except the admin's own current one).
func (s *Service) UpdateUser(ctx context.Context, a Actor, id int64, role, password *string) (User, error) {
	if err := needAdmin(a); err != nil {
		return User{}, err
	}
	u, err := s.store.User(ctx, id)
	if err != nil {
		return User{}, storeError(err)
	}
	if role != nil {
		if err := validateRole(*role); err != nil {
			return User{}, err
		}
	}
	var hash string
	if password != nil {
		if err := validatePassword(*password); err != nil {
			return User{}, err
		}
		if hash, err = hashPassword(*password); err != nil {
			return User{}, newError(Internal, err)
		}
	}
	var changes []string
	if role != nil && *role != u.Role {
		if err := s.store.SetRole(ctx, id, *role); err != nil {
			return User{}, storeError(err)
		}
		changes = append(changes, "role "+*role)
	}
	if password != nil {
		keep := ""
		if id == a.UserID {
			keep = a.sessionID
		}
		if err := s.store.SetPassword(ctx, id, hash, keep); err != nil {
			return User{}, storeError(err)
		}
		changes = append(changes, "password changed")
	}
	if len(changes) > 0 {
		s.record(ctx, a, "user.update", u.Name, strings.Join(changes, ", "))
	}
	u, err = s.store.User(ctx, id)
	if err != nil {
		return User{}, storeError(err)
	}
	return userView(u), nil
}

// DeleteUser removes a user (admins only). Refused while the user owns
// sites, and for the last admin.
func (s *Service) DeleteUser(ctx context.Context, a Actor, id int64) error {
	if err := needAdmin(a); err != nil {
		return err
	}
	u, err := s.store.User(ctx, id)
	if err != nil {
		return storeError(err)
	}
	if err := s.store.DeleteUser(ctx, id); err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "user.delete", u.Name, "")
	return nil
}

// Activity is one entry of the activity log.
type Activity struct {
	ID     int64     `json:"id"`
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
		out = append(out, Activity(e))
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
