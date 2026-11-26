package store

import (
	"context"
	"time"

	"github.com/danielgtmn/ablage"
)

// Roles.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// User is a person who can sign in. How they sign in (password, sessions,
// e-mail address) is kept in the auth_* tables under AuthUserID.
type User struct {
	ID         string
	Name       string
	Role       string
	AuthUserID string // "" until the user was moved to the auth tables
	CreatedAt  time.Time
}

func userOf(r userRow) User {
	return User{ID: r.ID, Name: r.Name, Role: r.Role, AuthUserID: deref(r.AuthUserID), CreatedAt: seconds(r.CreatedAt)}
}

var errUserNotFound = notFoundf("user not found")

func getUser(ctx context.Context, r repos, id string) (userRow, error) {
	if id == "" {
		return userRow{}, errUserNotFound
	}
	u, err := r.users.Get(ctx, id)
	return u, missing(err, errUserNotFound)
}

// userNames returns the names of the users with the given ids, in one
// query. Unknown and empty ids are left out.
func userNames(ctx context.Context, r repos, ids []string) (map[string]string, error) {
	out := map[string]string{}
	ids = unique(ids)
	if len(ids) == 0 {
		return out, nil
	}
	list, err := r.users.GetMany(ctx, ids...)
	if err != nil {
		return nil, err
	}
	for _, u := range list {
		out[u.ID] = u.Name
	}
	return out, nil
}

// CountUsers returns how many users exist.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	n, err := s.r.users.Count(ctx, ablage.All)
	return int(n), err
}

// CreateUser adds a user linked to the auth user authUserID. When firstOnly is set it fails with ErrConflict if
// any user exists already, atomically. A new admin takes over sites that
// have no owner (imported before any user existed).
func (s *Store) CreateUser(ctx context.Context, name, authUserID, role string, firstOnly bool) (User, error) {
	var u User
	err := s.tx(ctx, func(r repos) error {
		if firstOnly {
			n, err := r.users.Count(ctx, ablage.All)
			if err != nil {
				return err
			}
			if n > 0 {
				return conflictf("the panel is already set up")
			}
		}
		// The password lives in the auth tables; password_hash stays empty.
		row := userRow{ID: s.NewUserID(), Name: name, Role: role, AuthUserID: optional(authUserID), CreatedAt: s.now()}
		if err := r.users.Create(ctx, &row); err != nil {
			return dup(err, conflictf("the user name %q is already taken", name))
		}
		if role == RoleAdmin {
			if _, err := r.sites.UpdateWhere(ctx, ablage.Null("owner_id"), ablage.Row{"owner_id": row.ID}); err != nil {
				return err
			}
		}
		u = userOf(row)
		return nil
	})
	return u, err
}

// User returns one user by id.
func (s *Store) User(ctx context.Context, id string) (User, error) {
	u, err := getUser(ctx, s.r, id)
	if err != nil {
		return User{}, err
	}
	return userOf(u), nil
}

// UserByAuthID returns the user linked to an auth user.
func (s *Store) UserByAuthID(ctx context.Context, authUserID string) (User, error) {
	if authUserID == "" {
		return User{}, errUserNotFound
	}
	u, err := s.r.users.First(ctx, ablage.Query{Where: ablage.Eq("auth_user_id", authUserID)})
	if err != nil {
		return User{}, missing(err, errUserNotFound)
	}
	return userOf(u), nil
}

// UserByName returns one user by name.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	u, err := s.r.users.First(ctx, ablage.Query{Where: ablage.Eq("name", name)})
	if err != nil {
		return User{}, missing(err, errUserNotFound)
	}
	return userOf(u), nil
}

// Users lists all users by name.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	list, err := s.r.users.Find(ctx, ablage.Query{OrderBy: []ablage.Order{ablage.Asc("name")}})
	if err != nil {
		return nil, err
	}
	out := make([]User, len(list))
	for i, u := range list {
		out[i] = userOf(u)
	}
	return out, nil
}

// SetRole changes a user's role. The last admin cannot be demoted.
func (s *Store) SetRole(ctx context.Context, id, role string) error {
	return s.tx(ctx, func(r repos) error {
		u, err := getUser(ctx, r, id)
		if err != nil {
			return err
		}
		if u.Role == RoleAdmin && role != RoleAdmin {
			if err := ensureOtherAdmin(ctx, r, id, "the last admin cannot be made a member"); err != nil {
				return err
			}
		}
		return r.users.Patch(ctx, id, ablage.Row{"role": role})
	})
}

// DeleteUser removes a user with their sessions, tokens and DNS
// connections. It refuses while the user owns sites, domain redirects or
// mail domains, and for the last admin.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	return s.tx(ctx, func(r repos) error {
		u, err := getUser(ctx, r, id)
		if err != nil {
			return err
		}
		if u.Role == RoleAdmin {
			if err := ensureOtherAdmin(ctx, r, id, "the last admin cannot be deleted"); err != nil {
				return err
			}
		}
		sites, err := r.sites.Count(ctx, ablage.Eq("owner_id", id))
		if err != nil {
			return err
		}
		if sites > 0 {
			return conflictf("%s still owns sites; delete them first", u.Name)
		}
		if err := ownsNoDomainRedirects(ctx, r, id, u.Name); err != nil {
			return err
		}
		if err := ownsNoMailDomains(ctx, r, id, u.Name); err != nil {
			return err
		}
		return r.users.Delete(ctx, id)
	})
}

func ensureOtherAdmin(ctx context.Context, r repos, id, msg string) error {
	n, err := r.users.Count(ctx, ablage.And(ablage.Eq("role", RoleAdmin), ablage.Ne("id", id)))
	if err != nil {
		return err
	}
	if n == 0 {
		return conflictf("%s", msg)
	}
	return nil
}
