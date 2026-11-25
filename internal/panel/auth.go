package panel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/danielgtmn/sesam"
	"github.com/danielgtmn/sesam/plugins/password"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// A sign-in ends after SessionIdleTimeout without use, and in any case
// SessionLifetime after it started.
const (
	SessionIdleTimeout = 3 * 24 * time.Hour
	SessionLifetime    = 30 * 24 * time.Hour
)

// Sign-in throttling: failed attempts per user (or unknown name), and per
// client address across all names (so trying many names from one place is
// slowed too). sesam counts them; the setup code uses the per-name limit.
const (
	loginWindow       = 15 * time.Minute
	loginMaxPerName   = 5
	loginMaxPerClient = 20
	// maxPasswordChecks bounds the sign-ins and password changes in flight;
	// beyond it requests are turned away instead of queueing for the
	// (memory-hungry) password hashing.
	maxPasswordChecks = 16
)

// TokenPrefix starts every access token, so they are recognisable (and
// findable by secret scanners).
const TokenPrefix = "op_"

var userNameRe = regexp.MustCompile(`^[a-z0-9._-]{3,32}$`)

// User is what callers see of a user. Email is "" when none is set.
type User struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

func userView(u store.User, au sesam.User) User {
	return User{ID: u.ID, Name: u.Name, Email: au.Email, Role: u.Role, CreatedAt: u.CreatedAt}
}

// Credentials are a sign-in attempt. Name is the user name or the e-mail
// address. ClientIP keys the attempt limit.
type Credentials struct {
	Name      string
	Password  string
	ClientIP  string
	UserAgent string
}

func (c Credentials) meta() sesam.SessionMeta {
	return sesam.SessionMeta{IP: c.ClientIP, UserAgent: c.UserAgent}
}

func normalizeUserName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func validateUserName(name string) error {
	if !userNameRe.MatchString(name) {
		return errorf(Invalid, "the user name must be 3 to 32 characters: lowercase letters, digits, dots, underscores and hyphens")
	}
	return nil
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	// crypto/rand.Read never fails on supported platforms.
	rand.Read(b)
	return b
}

func randomString(n int) string { return base64.RawURLEncoding.EncodeToString(randomBytes(n)) }

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// SessionActor returns the actor of a session cookie value. Using a session
// keeps it alive, up to its full lifetime.
func (s *Service) SessionActor(ctx context.Context, secret string) (Actor, error) {
	ended := errorf(Unauthenticated, "your session has ended; please sign in again")
	sess, au, err := s.auth.Session(ctx, secret)
	if errors.Is(err, sesam.ErrUnauthenticated) {
		return Actor{}, ended
	}
	if err != nil {
		return Actor{}, newError(Internal, err)
	}
	u, err := s.store.UserByAuthID(ctx, au.ID)
	if errors.Is(err, store.ErrNotFound) {
		return Actor{}, ended
	}
	if err != nil {
		return Actor{}, newError(Internal, err)
	}
	return Actor{UserID: u.ID, Name: u.Name, Admin: u.Role == store.RoleAdmin, Via: ViaSession, sessionID: sess.ID, authUserID: au.ID}, nil
}

// TokenActor returns the actor of an access token. Its rights are the
// user's current rights, narrowed by the token.
func (s *Service) TokenActor(ctx context.Context, token string) (Actor, error) {
	invalid := errorf(Unauthenticated, "the access token is not valid")
	if !strings.HasPrefix(token, TokenPrefix) {
		return Actor{}, invalid
	}
	t, u, err := s.store.TokenByHash(ctx, sha256Hex(token))
	if errors.Is(err, store.ErrNotFound) {
		return Actor{}, invalid
	}
	if err != nil {
		return Actor{}, newError(Internal, err)
	}
	if tokenExpired(t, s.now()) {
		return Actor{}, invalid
	}
	if err := s.store.TouchToken(ctx, t.ID, s.now()); err != nil {
		slog.Warn("could not record token use", "err", err)
	}
	return Actor{
		UserID:    u.ID,
		Name:      u.Name,
		Admin:     u.Role == store.RoleAdmin,
		ReadOnly:  t.Access != "write",
		Sites:     t.Sites,
		Via:       ViaToken,
		tokenName: t.Name,
		tokenHash: t.Hash,
	}, nil
}

// sessionActive tells whether a session of a user is still running.
func (s *Service) sessionActive(ctx context.Context, u store.User, sessionID string) (bool, error) {
	if u.AuthUserID == "" {
		return false, nil
	}
	list, err := s.auth.Sessions(ctx, u.AuthUserID)
	if err != nil {
		return false, err
	}
	for _, sess := range list {
		if sess.ID == sessionID {
			return true, nil
		}
	}
	return false, nil
}

// refreshActor re-reads an actor saved earlier, so a deleted token, an ended
// session or a changed role takes effect. A missing credential is NotFound
// for the caller's purposes.
func (s *Service) refreshActor(ctx context.Context, a Actor) (Actor, error) {
	var (
		u   store.User
		t   store.Token
		err error
	)
	switch a.Via {
	case ViaToken:
		t, u, err = s.store.TokenByHash(ctx, a.tokenHash)
	case ViaSession:
		if u, err = s.store.User(ctx, a.UserID); err == nil {
			var ok bool
			if ok, err = s.sessionActive(ctx, u, a.sessionID); err == nil && !ok {
				err = store.ErrNotFound
			}
		}
	default:
		return a, nil
	}
	if err != nil {
		return Actor{}, err
	}
	if a.Via == ViaToken && tokenExpired(t, s.now()) {
		return Actor{}, store.ErrNotFound
	}
	fresh := Actor{UserID: u.ID, Name: u.Name, Admin: u.Role == store.RoleAdmin, Via: a.Via, sessionID: a.sessionID, authUserID: u.AuthUserID}
	if a.Via == ViaToken {
		fresh.ReadOnly, fresh.Sites, fresh.tokenName, fresh.tokenHash = t.Access != "write", t.Sites, t.Name, a.tokenHash
	}
	return fresh, nil
}

// Me returns the actor's own user.
func (s *Service) Me(ctx context.Context, a Actor) (User, error) {
	if err := needAuth(a); err != nil {
		return User{}, err
	}
	u, err := s.store.User(ctx, a.UserID)
	if err != nil {
		return User{}, storeError(err)
	}
	au, err := s.authUser(ctx, u)
	if err != nil {
		return User{}, err
	}
	return userView(u, au), nil
}

// Login checks a name or e-mail address and a password and starts a
// session. It returns the user and the secret for the session cookie.
// Failed attempts are limited per user (or unknown name) and per client
// address.
func (s *Service) Login(ctx context.Context, c Credentials) (User, string, error) {
	release, err := s.passwordCheck()
	if err != nil {
		return User{}, "", err
	}
	defer release()
	wrong := errorf(Unauthenticated, "Wrong name or password.")
	// Expired sessions, links and counters are cleaned up whenever someone
	// signs in.
	if err := s.auth.DeleteExpired(ctx); err != nil {
		slog.Warn("could not remove expired sessions", "err", err)
	}
	au, secret, _, err := s.pw.SignIn(ctx, password.Credentials{Identifier: c.Name, Password: c.Password}, c.meta())
	if errors.Is(err, sesam.ErrInvalidCredentials) {
		return User{}, "", wrong
	}
	if err != nil {
		return User{}, "", authError(err)
	}
	u, err := s.store.UserByAuthID(ctx, au.ID)
	if err != nil {
		// A sign-in user without a panel user (left over, removed at the
		// next start) cannot use the panel.
		if serr := s.auth.SignOut(ctx, secret); serr != nil {
			slog.Warn("could not end a session", "err", serr)
		}
		if errors.Is(err, store.ErrNotFound) {
			return User{}, "", wrong
		}
		return User{}, "", newError(Internal, err)
	}
	s.record(ctx, Actor{Name: u.Name}, "account.login", u.Name, "")
	return userView(u, au), secret, nil
}

// passwordCheck takes one of the slots for checking a password, or answers
// Throttled when all are taken. Call release when done.
func (s *Service) passwordCheck() (release func(), err error) {
	select {
	case s.passwordChecks <- struct{}{}:
		return func() { <-s.passwordChecks }, nil
	default:
		return nil, errBusy()
	}
}

// SignOutEverywhere ends every session of the actor's user, including the
// current one. Access tokens keep working.
func (s *Service) SignOutEverywhere(ctx context.Context, a Actor) error {
	if err := needSession(a, "signing out everywhere"); err != nil {
		return err
	}
	if _, err := s.auth.RevokeUserSessions(ctx, a.authUserID, ""); err != nil {
		return newError(Internal, err)
	}
	s.record(ctx, a, "account.logout_everywhere", a.Name, "")
	return nil
}

// Logout ends the session with the given cookie value. Unknown sessions are
// fine.
func (s *Service) Logout(ctx context.Context, secret string) error {
	if err := s.auth.SignOut(ctx, secret); err != nil {
		return newError(Internal, err)
	}
	return nil
}

// ChangePassword sets the actor's own password. It needs the current one and
// ends the user's other sessions.
func (s *Service) ChangePassword(ctx context.Context, a Actor, current, next string) error {
	if err := needSession(a, "changing the password"); err != nil {
		return err
	}
	if err := validatePassword(next); err != nil {
		return err
	}
	release, err := s.passwordCheck()
	if err != nil {
		return err
	}
	defer release()
	if err := s.pw.ChangePassword(ctx, a.authUserID, current, next, a.sessionID); err != nil {
		return authError(err)
	}
	s.record(ctx, a, "account.password", a.Name, "")
	return nil
}

// SetOwnEmail sets or (with "") removes the actor's own e-mail address,
// where password reset links are sent.
func (s *Service) SetOwnEmail(ctx context.Context, a Actor, email string) (User, error) {
	if err := needSession(a, "changing the e-mail address"); err != nil {
		return User{}, err
	}
	u, err := s.store.User(ctx, a.UserID)
	if err != nil {
		return User{}, storeError(err)
	}
	au, changed, err := s.setEmail(ctx, u, email)
	if err != nil {
		return User{}, err
	}
	if changed != "" {
		s.record(ctx, a, "account.email", u.Name, changed)
	}
	return userView(u, au), nil
}

// setEmail sets a user's e-mail address. changed describes the change for
// the activity log ("" for none); the address itself is not logged.
func (s *Service) setEmail(ctx context.Context, u store.User, email string) (sesam.User, string, error) {
	e, err := normalizeEmail(email)
	if err != nil {
		return sesam.User{}, "", err
	}
	au, err := s.authUser(ctx, u)
	if err != nil {
		return sesam.User{}, "", err
	}
	if e == au.Email {
		return au, "", nil
	}
	next, err := s.auth.UpdateUser(ctx, au.ID, sesam.UserUpdate{Email: &e})
	if err != nil {
		return sesam.User{}, "", authError(err)
	}
	if e == "" {
		return next, "e-mail address removed", nil
	}
	return next, "e-mail address changed", nil
}

// SetupRequired reports whether no user exists yet.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return false, newError(Internal, err)
	}
	return n == 0, nil
}

// setupAlphabet leaves out letters and digits that are easily confused.
const setupAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newSetupCode() string {
	var b strings.Builder
	// 32 symbols: each random byte maps without bias via its low 5 bits.
	for i, r := range randomBytes(24) {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(setupAlphabet[r&31])
	}
	return b.String()
}

// EnsureSetupCode makes sure the setup code file exists while there is no
// user, and is gone once there is one. It reports whether setup is needed.
func (s *Service) EnsureSetupCode(ctx context.Context) (bool, error) {
	needed, err := s.SetupRequired(ctx)
	if err != nil {
		return false, err
	}
	if !needed {
		if err := os.Remove(s.setupCodeFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		return false, nil
	}
	f, err := os.OpenFile(s.setupCodeFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	_, err = f.WriteString(newSetupCode() + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return true, err
}

// Setup creates the first admin with the one-time setup code and signs them
// in. Once any user exists it always answers Conflict.
func (s *Service) Setup(ctx context.Context, code string, c Credentials) (User, string, error) {
	alreadySetUp := errorf(Conflict, "the panel is already set up")
	if needed, err := s.SetupRequired(ctx); err != nil {
		return User{}, "", err
	} else if !needed {
		return User{}, "", alreadySetUp
	}
	key := "setup-code:" + c.ClientIP
	if err := s.auth.Limit(ctx, key, loginMaxPerName, loginWindow); err != nil {
		return User{}, "", authError(err)
	}
	want, err := os.ReadFile(s.setupCodeFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return User{}, "", newError(Internal, err)
	}
	wantCode := strings.TrimSpace(string(want))
	gotCode := strings.ToUpper(strings.TrimSpace(code))
	if wantCode == "" || subtle.ConstantTimeCompare([]byte(gotCode), []byte(wantCode)) != 1 {
		return User{}, "", errorf(Invalid, "the setup code is wrong")
	}
	if err := s.auth.ResetLimit(ctx, key); err != nil {
		return User{}, "", newError(Internal, err)
	}

	name := normalizeUserName(c.Name)
	if err := validateUserName(name); err != nil {
		return User{}, "", err
	}
	if err := validatePassword(c.Password); err != nil {
		return User{}, "", err
	}
	au, err := s.pw.CreateUser(ctx, sesam.NewUser{Username: name}, c.Password)
	if errors.Is(err, sesam.ErrUsernameTaken) {
		return User{}, "", alreadySetUp
	}
	if err != nil {
		return User{}, "", authError(err)
	}
	u, err := s.store.CreateUser(ctx, name, au.ID, store.RoleAdmin, true)
	if err != nil {
		s.dropAuthUser(ctx, au.ID)
		if errors.Is(err, store.ErrConflict) {
			return User{}, "", alreadySetUp
		}
		return User{}, "", storeError(err)
	}
	if err := os.Remove(s.setupCodeFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Error("could not remove the setup code file", "file", s.setupCodeFile, "err", err)
	}
	secret, _, err := s.auth.SignIn(ctx, au, c.meta())
	if err != nil {
		return User{}, "", newError(Internal, err)
	}
	s.record(ctx, Actor{Name: u.Name}, "setup", u.Name, "first admin created")
	return userView(u, au), secret, nil
}

// dropAuthUser removes a sign-in user whose panel user could not be
// created. If that fails too, the next start removes it.
func (s *Service) dropAuthUser(ctx context.Context, id string) {
	if err := s.auth.DeleteUser(ctx, id); err != nil {
		slog.Warn("could not remove an unused sign-in user", "err", err)
	}
}
