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

	"github.com/danielgtmn/ownpanel/internal/store"
)

// A sign-in ends after SessionIdleTimeout without use, and in any case
// SessionLifetime after it started.
const (
	SessionIdleTimeout = 3 * 24 * time.Hour
	SessionLifetime    = 30 * 24 * time.Hour
)

// Sign-in throttling: failed attempts per address and name, and per address
// across all names (so trying many names from one place is slowed too).
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

// User is what callers see of a user. The password hash never leaves the
// package.
type User struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

func userView(u store.User) User {
	return User{ID: u.ID, Name: u.Name, Role: u.Role, CreatedAt: u.CreatedAt}
}

// Credentials are a sign-in attempt. ClientIP keys the attempt limit.
type Credentials struct {
	Name     string
	Password string
	ClientIP string
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

// newSession stores a session for u and returns the secret for the cookie.
func (s *Service) newSession(ctx context.Context, u store.User) (string, error) {
	// Expired rows are cleaned up whenever someone signs in.
	if err := s.store.DeleteExpiredSessions(ctx, s.now()); err != nil {
		slog.Warn("could not remove expired sessions", "err", err)
	}
	secret := randomString(32)
	if err := s.store.CreateSession(ctx, sha256Hex(secret), u.ID, s.now().Add(SessionIdleTimeout)); err != nil {
		return "", newError(Internal, err)
	}
	return secret, nil
}

// SessionActor returns the actor of a session cookie value.
func (s *Service) SessionActor(ctx context.Context, secret string) (Actor, error) {
	id := sha256Hex(secret)
	u, err := s.store.SessionUser(ctx, id, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return Actor{}, errorf(Unauthenticated, "your session has ended; please sign in again")
	}
	if err != nil {
		return Actor{}, newError(Internal, err)
	}
	// Using a session keeps it alive, up to its full lifetime.
	if err := s.store.ExtendSession(ctx, id, s.now().Add(SessionIdleTimeout), SessionLifetime); err != nil {
		slog.Warn("could not extend a session", "err", err)
	}
	return Actor{UserID: u.ID, Name: u.Name, Admin: u.Role == store.RoleAdmin, Via: ViaSession, sessionID: id}, nil
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
		u, err = s.store.SessionUser(ctx, a.sessionID, s.now())
	default:
		return a, nil
	}
	if err != nil {
		return Actor{}, err
	}
	if a.Via == ViaToken && tokenExpired(t, s.now()) {
		return Actor{}, store.ErrNotFound
	}
	fresh := Actor{UserID: u.ID, Name: u.Name, Admin: u.Role == store.RoleAdmin, Via: a.Via, sessionID: a.sessionID}
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
	return userView(u), nil
}

// Login checks a name and password and starts a session. It returns the
// user and the secret for the session cookie.
func (s *Service) Login(ctx context.Context, c Credentials) (User, string, error) {
	release, err := s.passwordCheck()
	if err != nil {
		return User{}, "", err
	}
	defer release()
	name := normalizeUserName(c.Name)
	key := c.ClientIP + "\n" + name
	if _, wait, err := s.limiter.attempt(ctx, key); err != nil {
		return User{}, "", err
	} else if wait > 0 {
		return User{}, "", throttled(wait)
	}
	clientAttempt, wait, err := s.clientLimiter.attempt(ctx, c.ClientIP+"\nany name")
	if err != nil {
		return User{}, "", err
	} else if wait > 0 {
		return User{}, "", throttled(wait)
	}
	u, err := s.store.UserByName(ctx, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		verifyPassword(dummyHash(), c.Password)
		return User{}, "", errorf(Unauthenticated, "Wrong name or password.")
	case err != nil:
		return User{}, "", newError(Internal, err)
	}
	if !verifyPassword(u.PasswordHash, c.Password) {
		return User{}, "", errorf(Unauthenticated, "Wrong name or password.")
	}
	// A successful sign-in clears the failures for this name and does not
	// count against the address.
	if err := s.limiter.succeed(ctx, key); err != nil {
		return User{}, "", err
	}
	if err := s.clientLimiter.forget(ctx, clientAttempt); err != nil {
		return User{}, "", err
	}
	secret, err := s.newSession(ctx, u)
	if err != nil {
		return User{}, "", err
	}
	s.record(ctx, Actor{Name: u.Name}, "account.login", u.Name, "")
	return userView(u), secret, nil
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
	if err := s.store.DeleteUserSessions(ctx, a.UserID); err != nil {
		return newError(Internal, err)
	}
	s.record(ctx, a, "account.logout_everywhere", a.Name, "")
	return nil
}

// Logout ends the session with the given cookie value. Unknown sessions are
// fine.
func (s *Service) Logout(ctx context.Context, secret string) error {
	if secret == "" {
		return nil
	}
	if err := s.store.DeleteSession(ctx, sha256Hex(secret)); err != nil {
		return newError(Internal, err)
	}
	return nil
}

// ChangePassword sets the actor's own password. It needs the current one and
// ends the user's other sessions.
func (s *Service) ChangePassword(ctx context.Context, a Actor, current, next, clientIP string) error {
	if err := needSession(a, "changing the password"); err != nil {
		return err
	}
	u, err := s.store.User(ctx, a.UserID)
	if err != nil {
		return storeError(err)
	}
	release, err := s.passwordCheck()
	if err != nil {
		return err
	}
	defer release()
	key := clientIP + "\n" + u.Name
	if _, wait, err := s.limiter.attempt(ctx, key); err != nil {
		return err
	} else if wait > 0 {
		return throttled(wait)
	}
	if !verifyPassword(u.PasswordHash, current) {
		return errorf(Invalid, "the current password is wrong")
	}
	if err := s.limiter.succeed(ctx, key); err != nil {
		return err
	}
	if err := validatePassword(next); err != nil {
		return err
	}
	hash, err := hashPassword(next)
	if err != nil {
		return newError(Internal, err)
	}
	if err := s.store.SetPassword(ctx, u.ID, hash, a.sessionID); err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "account.password", u.Name, "")
	return nil
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
	key := c.ClientIP + "\nsetup code"
	if _, wait, err := s.limiter.attempt(ctx, key); err != nil {
		return User{}, "", err
	} else if wait > 0 {
		return User{}, "", throttled(wait)
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
	if err := s.limiter.succeed(ctx, key); err != nil {
		return User{}, "", err
	}

	name := normalizeUserName(c.Name)
	if err := validateUserName(name); err != nil {
		return User{}, "", err
	}
	if err := validatePassword(c.Password); err != nil {
		return User{}, "", err
	}
	hash, err := hashPassword(c.Password)
	if err != nil {
		return User{}, "", newError(Internal, err)
	}
	u, err := s.store.CreateUser(ctx, name, hash, store.RoleAdmin, true)
	if errors.Is(err, store.ErrConflict) {
		return User{}, "", alreadySetUp
	}
	if err != nil {
		return User{}, "", storeError(err)
	}
	if err := os.Remove(s.setupCodeFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Error("could not remove the setup code file", "file", s.setupCodeFile, "err", err)
	}
	secret, err := s.newSession(ctx, u)
	if err != nil {
		return User{}, "", err
	}
	s.record(ctx, Actor{Name: u.Name}, "setup", u.Name, "first admin created")
	return userView(u), secret, nil
}
