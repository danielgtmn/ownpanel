package panel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtmn/sesam"
	"github.com/danielgtmn/sesam/adapter"
	"github.com/danielgtmn/sesam/adapters/sqladapter"
	"github.com/danielgtmn/sesam/plugins/password"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Signing in is done by sesam: its users, password hashes, sessions,
// attempt counters and reset links live in the auth_* tables of the panel's
// database. A panel user (users table: id, name, role) points at its sesam
// user with auth_user_id; the panel's own foreign keys keep using the
// integer id. The panel calls sesam's Go API from its own handlers and
// keeps its own cookie and request checks (internal/api), because it is
// served on the panel domain and on the server's addresses, and the panel
// domain changes at runtime.

// resetSenderLocalPart is who password reset mails come from.
const resetSenderLocalPart = "no-reply"

// initAuth sets up sesam on the store's database, adds its tables and moves
// users from before sesam over.
func (s *Service) initAuth(ctx context.Context) error {
	db, err := sqladapter.New(s.store.DB(), sqladapter.SQLite)
	if err != nil {
		return err
	}
	opts := password.Options{
		Identifier:           password.EmailOrUsername,
		DisableSignUp:        true,
		MinLength:            minPasswordLen,
		MaxLength:            maxPasswordLen,
		MaxAttemptsPerName:   loginMaxPerName,
		MaxAttemptsPerClient: loginMaxPerClient,
		AttemptWindow:        loginWindow,
		ResetEmail:           s.resetEmail,
	}
	cfg := sesam.Config{
		Adapter: db,
		// BaseURL, cookie and request header settings only matter for
		// sesam's own HTTP handler, which the panel does not use.
		BaseURL: "http://localhost",
		Session: sesam.SessionConfig{IdleTimeout: SessionIdleTimeout, MaxLifetime: SessionLifetime},
		Now:     func() time.Time { return s.now() },
	}
	if s.mail != nil {
		// Whether a reset mail can actually go out is decided per request
		// (PasswordResetAvailable): it depends on the panel domain and the
		// mail domains, which change at runtime.
		cfg.SendEmail = s.sendEmail
		opts.ResetURL = s.resetURL
	}
	s.pw = password.New(opts)
	cfg.Plugins = []sesam.Plugin{s.pw}
	if s.auth, err = sesam.New(cfg); err != nil {
		return err
	}
	if err := s.auth.Migrate(ctx); err != nil {
		return fmt.Errorf("sign-in tables: %w", err)
	}
	return s.moveUsersToAuth(ctx)
}

// moveUsersToAuth gives every panel user from before sesam a sesam user:
// the name becomes the username, the password hash is taken over unchanged
// and running sessions are copied, so nobody has to sign in again. Each
// step can be repeated, and the panel user is only linked (and its old hash
// and sessions removed) at the end, so an interrupted run loses nothing and
// the next start finishes it. Afterwards sesam users that no panel user
// points at (left over by an interrupted run or a failed create or delete)
// are removed.
//
// The store allows one connection: nothing here runs inside a transaction
// of the other side.
func (s *Service) moveUsersToAuth(ctx context.Context) error {
	users, err := s.store.UnmovedUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if err := s.moveUserToAuth(ctx, u); err != nil {
			// The user stays as it is and is tried again at the next start.
			slog.Error("could not move a user to the new sign-in", "user", u.Name, "err", err)
			continue
		}
		slog.Info("moved a user to the new sign-in", "user", u.Name)
	}
	linked, err := s.store.AuthUserIDs(ctx)
	if err != nil {
		return err
	}
	for offset := 0; ; {
		list, err := s.auth.Users(ctx, sesam.ListOptions{Limit: 100, Offset: offset})
		if err != nil {
			return err
		}
		for _, au := range list {
			if linked[au.ID] {
				offset++
				continue
			}
			if err := s.auth.DeleteUser(ctx, au.ID); err != nil {
				return err
			}
		}
		if len(list) < 100 {
			return nil
		}
	}
}

func (s *Service) moveUserToAuth(ctx context.Context, u store.UnmovedUser) error {
	au, err := s.auth.UserByUsername(ctx, u.Name)
	if errors.Is(err, sesam.ErrNotFound) {
		au, err = s.auth.CreateUser(ctx, sesam.NewUser{Username: u.Name})
	}
	if err != nil {
		return err
	}
	if u.PasswordHash != "" {
		// Setting the hash ends the sesam user's sessions; they are copied
		// (again) below.
		if err := s.pw.SetPasswordHash(ctx, au.ID, u.PasswordHash); err != nil {
			if !errors.Is(err, sesam.ErrInvalidInput) {
				return err
			}
			slog.Warn("the password of a user could not be taken over; an admin has to set a new one", "user", u.Name)
		}
	}
	now := s.now()
	sessions, err := s.store.OldSessions(ctx, u.ID, now)
	if err != nil {
		return err
	}
	for _, o := range sessions {
		// Last used: when the idle timeout started that ends it.
		seen := o.ExpiresAt.Add(-SessionIdleTimeout)
		if seen.Before(o.CreatedAt) {
			seen = o.CreatedAt
		}
		err := s.auth.Adapter().Create(ctx, sesam.TableSessions, adapter.Row{
			"id":           o.ID,
			"user_id":      au.ID,
			"created_at":   adapter.NormalizeTime(o.CreatedAt),
			"expires_at":   adapter.NormalizeTime(o.ExpiresAt),
			"last_seen_at": adapter.NormalizeTime(seen),
		})
		if err != nil && !errors.Is(err, adapter.ErrConflict) {
			return err
		}
	}
	return s.store.LinkAuthUser(ctx, u.ID, au.ID)
}

// authUser returns the sesam user of a panel user.
func (s *Service) authUser(ctx context.Context, u store.User) (sesam.User, error) {
	if u.AuthUserID == "" {
		return sesam.User{}, errorf(Internal, "the sign-in of %s has not been set up yet; restart the panel", u.Name)
	}
	au, err := s.auth.User(ctx, u.AuthUserID)
	if err != nil {
		return sesam.User{}, newError(Internal, err)
	}
	return au, nil
}

// authUsers returns all sesam users by id.
func (s *Service) authUsers(ctx context.Context) (map[string]sesam.User, error) {
	out := map[string]sesam.User{}
	for offset := 0; ; offset += 100 {
		list, err := s.auth.Users(ctx, sesam.ListOptions{Limit: 100, Offset: offset})
		if err != nil {
			return nil, newError(Internal, err)
		}
		for _, au := range list {
			out[au.ID] = au
		}
		if len(list) < 100 {
			return out, nil
		}
	}
}

// authError turns an error of sesam into one of the panel, with the
// panel's wording.
func authError(err error) error {
	var rl *sesam.RateLimitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &rl):
		return throttled(rl.RetryAfter)
	case errors.Is(err, sesam.ErrUsernameTaken):
		return errorf(Conflict, "this user name is already taken")
	case errors.Is(err, sesam.ErrEmailTaken):
		return errorf(Conflict, "this e-mail address is already used by another person")
	case errors.Is(err, sesam.ErrInvalidToken):
		return errorf(Invalid, "this link is invalid or has expired; ask for a new one")
	case errors.Is(err, sesam.ErrWrongPassword):
		return errorf(Invalid, "the current password is wrong")
	case errors.Is(err, sesam.ErrInvalidInput):
		return errorf(Invalid, "%s", lowerFirst(strings.TrimSuffix(err.Error(), ".")))
	}
	return newError(Internal, err)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// normalizeEmail checks an optional e-mail address: "" stays "".
func normalizeEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", nil
	}
	e, err := sesam.NormalizeEmail(email)
	if err != nil {
		return "", errorf(Invalid, "enter a valid e-mail address, like name@example.com")
	}
	return e, nil
}

// throttled says how long to wait after too many attempts.
func throttled(wait time.Duration) error {
	if wait < time.Second {
		wait = time.Second
	}
	minutes := int((wait + time.Minute - 1) / time.Minute)
	unit := "minutes"
	if minutes == 1 {
		unit = "minute"
	}
	return errorf(Throttled, "too many failed attempts. Please try again in %d %s.", minutes, unit)
}

// errBusy answers when too many passwords are being checked at once.
func errBusy() error {
	return errorf(Throttled, "too many sign-in attempts right now. Please try again in a minute.")
}

// Password reset by e-mail.

// resetSender is the address reset mails come from: no-reply@ the panel
// domain, or of the closest parent domain of it, that is an active mail
// domain of this server (so the mail is signed and covered by its SPF
// record). ok is false when there is none or no mail service is set up.
func (s *Service) resetSender(ctx context.Context) (string, bool) {
	panelDomain, host := s.PanelDomain(), s.MailHost()
	if s.mail == nil || panelDomain == "" || host == "" {
		return "", false
	}
	domains, err := s.store.MailDomains(ctx)
	if err != nil {
		slog.Warn("could not read the mail domains", "err", err)
		return "", false
	}
	active := map[string]bool{}
	for _, d := range domains {
		// Like buildMailConfig: the mail host itself is no mail domain.
		if d.Active && d.Domain != host {
			active[d.Domain] = true
		}
	}
	for d := panelDomain; strings.Contains(d, "."); d = d[strings.Index(d, ".")+1:] {
		if active[d] {
			return resetSenderLocalPart + "@" + d, true
		}
	}
	return "", false
}

// PasswordResetAvailable tells whether people can reset a forgotten
// password by e-mail: a mail service is set up and a reset mail can be sent
// from the panel's domain (see resetSender). Anyone may ask.
func (s *Service) PasswordResetAvailable(ctx context.Context) bool {
	_, ok := s.resetSender(ctx)
	return ok && s.pw.ResetEnabled()
}

// resetURL is the link in the reset mail, on the panel domain current at
// the time of the request. The token is in the fragment, so it never
// reaches a server log.
func (s *Service) resetURL(token string) string {
	host := s.PanelDomain()
	if p := s.opts.HTTPSPort; p != 0 && p != 443 {
		host += ":" + strconv.Itoa(p)
	}
	return "https://" + host + "/#/reset-password?token=" + token
}

// resetEmail writes the reset mail.
func (s *Service) resetEmail(d password.ResetEmailData) sesam.Email {
	valid := "1 hour"
	if d.ValidFor != time.Hour {
		valid = d.ValidFor.Round(time.Minute).String()
	}
	text := fmt.Sprintf(`Hello %s,

someone asked to reset the password of your account %q on the panel at %s.
To choose a new password, open this link:

%s

The link works once and for %s. If you did not ask for this, you can
ignore this e-mail; your password stays the same.
`, d.User.Username, d.User.Username, s.PanelDomain(), d.Link, valid)
	return sesam.Email{Subject: "Reset your password for " + s.PanelDomain(), Text: text}
}

// sendEmail hands a mail written by sesam to the mail service, which signs
// it and delivers it locally or through its queue.
func (s *Service) sendEmail(ctx context.Context, e sesam.Email) error {
	from, ok := s.resetSender(ctx)
	if !ok {
		return errors.New("no mail domain to send from")
	}
	if err := s.mayMailResetTo(ctx, e.To); err != nil {
		return err
	}
	return s.mail.Send(ctx, config.OutgoingMail{
		From: from, FromName: "ownpanel", To: e.To, Subject: e.Subject, Text: e.Text, HTML: e.HTML,
	})
}

// RequestPasswordReset sends a reset link to the address if it belongs to
// a user. Unknown addresses get the same answer, so nobody learns which
// addresses are known; a mail that cannot be sent is logged for admins
// for the same reason.
func (s *Service) RequestPasswordReset(ctx context.Context, email, clientIP string) error {
	if !s.PasswordResetAvailable(ctx) {
		return errorf(NotFound, "a forgotten password cannot be reset by e-mail here; ask an administrator")
	}
	if e, err := normalizeEmail(email); err != nil || e == "" {
		return errorf(Invalid, "enter a valid e-mail address, like name@example.com")
	}
	err := s.pw.RequestReset(ctx, email, sesam.SessionMeta{IP: clientIP})
	var rl *sesam.RateLimitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &rl):
		return errorf(Throttled, "too many requests for a new password. Please try again later.")
	case errors.Is(err, sesam.ErrInvalidInput):
		return authError(err)
	}
	slog.Error("could not send a password reset mail", "err", err)
	s.record(ctx, System, "account.password_reset_failed", "", "a reset link could not be sent by e-mail")
	return nil
}

// ResetPassword sets a new password with the token from a reset link. It
// ends all sessions of the user.
func (s *Service) ResetPassword(ctx context.Context, token, next string) error {
	if err := validatePassword(next); err != nil {
		return err
	}
	release, err := s.passwordCheck()
	if err != nil {
		return err
	}
	defer release()
	au, err := s.pw.ResetPassword(ctx, token, next)
	if err != nil {
		return authError(err)
	}
	// Named like a sign-in: the person acted, the link proved who they are.
	name := au.Username
	if u, err := s.store.UserByAuthID(ctx, au.ID); err == nil {
		name = u.Name
	}
	s.record(ctx, Actor{Name: name}, "account.password_reset", name, "")
	return nil
}
