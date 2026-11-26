package panel

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/sesam/v2"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// oldUser writes a user and a session the way the panel stored them before
// sesam: password hash and sessions in its own tables, no auth user.
func oldUser(t *testing.T, st *store.Store, name, password string, sessionSecret string, now time.Time) string {
	t.Helper()
	hash, err := hashPassword(password) // same argon2id format as before
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.AddUnmovedUser(context.Background(), name, hash, store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if sessionSecret != "" {
		if err := st.AddOldSession(context.Background(), u.ID, store.OldSession{
			ID: sha256Hex(sessionSecret), CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(SessionIdleTimeout)}); err != nil {
			t.Fatal(err)
		}
	}
	return u.ID
}

func TestMoveUsersToAuth(t *testing.T) {
	eachStore(t, testMoveUsersToAuth)
}

func testMoveUsersToAuth(t *testing.T, open openStore) {
	svc, _, _ := newEmptyServiceOn(t, open)
	clk := withClock(svc)
	ctx := context.Background()
	st := svc.store
	rootID := oldUser(t, st, "root", "root-password-1", "root-cookie", clk.t)
	annaID := oldUser(t, st, "anna", "anna-password-1", "", clk.t)
	tok, err := svc.CreateToken(ctx, Actor{UserID: rootID, Name: "root", Admin: true, Via: ViaSession}, "ci", "write", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	// An interrupted earlier run left a sesam user for anna without the
	// link, and an unrelated leftover.
	if _, err := svc.auth.CreateUser(ctx, sesam.NewUser{Username: "anna"}); err != nil {
		t.Fatal(err)
	}
	stray, err := svc.auth.CreateUser(ctx, sesam.NewUser{Username: "stray"})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.moveUsersToAuth(ctx); err != nil {
		t.Fatal(err)
	}
	// Running it again (every start does) changes nothing.
	if err := svc.moveUsersToAuth(ctx); err != nil {
		t.Fatal(err)
	}

	// The running session still works, the old password still signs in,
	// the access token is untouched.
	a, err := svc.SessionActor(ctx, "root-cookie")
	if err != nil || a.UserID != rootID || !a.Admin {
		t.Fatalf("old session: %+v %v", a, err)
	}
	if _, _, err := svc.Login(ctx, Credentials{Name: "root", Password: "root-password-1", ClientIP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, Credentials{Name: "anna", Password: "anna-password-1", ClientIP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TokenActor(ctx, tok.Secret); err != nil {
		t.Fatal(err)
	}
	if un, _ := st.UnmovedUsers(ctx); len(un) != 0 {
		t.Fatalf("unmoved: %+v", un)
	}
	for _, id := range []string{rootID, annaID} {
		if old, err := st.OldSessions(ctx, id, time.Time{}); err != nil || len(old) != 0 {
			t.Fatalf("sessions left in the old table: %+v %v", old, err)
		}
	}
	if _, err := svc.auth.User(ctx, stray.ID); !errors.Is(err, sesam.ErrNotFound) {
		t.Fatalf("leftover sesam user kept: %v", err)
	}
	if n, _ := svc.auth.CountUsers(ctx); n != 2 {
		t.Fatalf("%d sesam users", n)
	}
	// Signing out the copied session ends it for good: it is not copied
	// back at the next start.
	if err := svc.Logout(ctx, "root-cookie"); err != nil {
		t.Fatal(err)
	}
	if err := svc.moveUsersToAuth(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SessionActor(ctx, "root-cookie")
	wantKind(t, err, Unauthenticated)
}

// TestMoveUserWithUnusableHash: a hash that cannot be taken over does not
// block the move; the user just needs a new password from an admin.
func TestMoveUserWithUnusableHash(t *testing.T) {
	svc, _, _ := newEmptyService(t)
	ctx := context.Background()
	if _, err := svc.store.AddUnmovedUser(ctx, "root", "not-a-hash", store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := svc.moveUsersToAuth(ctx); err != nil {
		t.Fatal(err)
	}
	if un, _ := svc.store.UnmovedUsers(ctx); len(un) != 0 {
		t.Fatalf("unmoved: %+v", un)
	}
	pw := "a-new-password"
	if _, err := svc.UpdateUser(ctx, admin, "1", UserUpdate{Password: &pw}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, Credentials{Name: "root", Password: pw}); err != nil {
		t.Fatal(err)
	}
}

func TestUserEmail(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, admin, NewUser{Name: "alice", Email: " Alice@Example.org ", Password: "alice-password-1", Role: "member"})
	if err != nil || u.Email != "alice@example.org" {
		t.Fatalf("%+v %v", u, err)
	}
	_, err = svc.CreateUser(ctx, admin, NewUser{Name: "bob", Email: "alice@example.org", Password: "bob-password-1", Role: "member"})
	wantKind(t, err, Conflict)
	_, err = svc.CreateUser(ctx, admin, NewUser{Name: "bob", Email: "not an address", Password: "bob-password-1", Role: "member"})
	wantKind(t, err, Invalid)
	_, err = svc.CreateUser(ctx, admin, NewUser{Name: "alice", Password: "alice-password-1", Role: "member"})
	wantKind(t, err, Conflict)

	// Sign in with the name or the address.
	for _, name := range []string{"alice", "ALICE@example.org"} {
		if _, _, err := svc.Login(ctx, Credentials{Name: name, Password: "alice-password-1", ClientIP: "1.2.3.4"}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	list, _ := svc.Users(ctx, admin)
	if len(list) != 2 || list[0].Name != "alice" || list[0].Email != "alice@example.org" || list[1].Email != "" {
		t.Fatalf("%+v", list)
	}

	// The person changes it on their own account; tokens cannot.
	_, secret, _ := svc.Login(ctx, Credentials{Name: "alice", Password: "alice-password-1", ClientIP: "1.2.3.4"})
	a, _ := svc.SessionActor(ctx, secret)
	// It needs the current password; a wrong one changes nothing.
	_, err = svc.SetOwnEmail(ctx, a, "", "a@example.net")
	wantKind(t, err, Invalid)
	_, err = svc.SetOwnEmail(ctx, a, "wrong-password-1", "a@example.net")
	wantKind(t, err, Invalid)
	if got, _ := svc.Me(ctx, a); got.Email != "alice@example.org" {
		t.Fatalf("changed with a wrong password: %+v", got)
	}
	me, err := svc.SetOwnEmail(ctx, a, "alice-password-1", "a@example.net")
	if err != nil || me.Email != "a@example.net" {
		t.Fatalf("%+v %v", me, err)
	}
	if got, _ := svc.Me(ctx, a); got.Email != "a@example.net" {
		t.Fatalf("me %+v", got)
	}
	nt, _ := svc.CreateToken(ctx, a, "ci", "write", nil, 0)
	tokActor, _ := svc.TokenActor(ctx, nt.Secret)
	_, err = svc.SetOwnEmail(ctx, tokActor, "alice-password-1", "x@example.net")
	wantKind(t, err, Forbidden)

	// Admins change and remove it.
	none := ""
	got, err := svc.UpdateUser(ctx, admin, u.ID, UserUpdate{Email: &none})
	if err != nil || got.Email != "" {
		t.Fatalf("%+v %v", got, err)
	}
	_, _, err = svc.Login(ctx, Credentials{Name: "a@example.net", Password: "alice-password-1", ClientIP: "1.2.3.4"})
	wantKind(t, err, Unauthenticated)

	// The addresses never appear in the activity log.
	acts, _ := svc.Activities(ctx, admin, 100)
	for _, e := range acts {
		if strings.Contains(e.Detail+e.Target, "@") {
			t.Fatalf("address in the log: %+v", e)
		}
	}

	// Deleting the user removes its sign-in, so the name can be used again.
	if err := svc.DeleteUser(ctx, admin, u.ID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SessionActor(ctx, secret)
	wantKind(t, err, Unauthenticated)
	if _, err := svc.CreateUser(ctx, admin, NewUser{Name: "alice", Password: "alice-password-2", Role: "member"}); err != nil {
		t.Fatal(err)
	}
}

// setPanelDomain saves a panel domain; the test web server has no panel
// upstream, which only stops the (irrelevant) web server update.
func setPanelDomain(t *testing.T, svc *Service, d string) {
	t.Helper()
	if _, err := svc.UpdateSettings(context.Background(), admin, SettingsUpdate{PanelDomain: &d}); err != nil && KindOf(err) != NotSynced {
		t.Fatal(err)
	}
	if svc.PanelDomain() != d {
		t.Fatalf("panel domain %q", svc.PanelDomain())
	}
}

// resetWorld: the mail world with a panel domain under root's active mail
// domain, so reset mails can be sent from no-reply@rootsite.example.com.
func resetWorld(t *testing.T) (*mailWorld, User) {
	t.Helper()
	w := newMailWorld(t)
	ctx := context.Background()
	setPanelDomain(t, w.svc, "panel.rootsite.example.com")
	// The mail service was attached after New: set sign-in up again with it.
	if err := w.svc.initAuth(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := w.svc.CreateUser(ctx, admin, NewUser{Name: "alice", Email: "alice@example.net", Password: "alice-password-1", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	return w, u
}

func TestPasswordReset(t *testing.T) {
	w, _ := resetWorld(t)
	svc := w.svc
	ctx := context.Background()
	if !svc.PasswordResetAvailable(ctx) {
		t.Fatal("reset must be available")
	}
	if from, _ := svc.resetSender(ctx); from != "no-reply@rootsite.example.com" {
		t.Fatalf("sender %q", from)
	}
	_, old, _ := svc.Login(ctx, Credentials{Name: "alice", Password: "alice-password-1", ClientIP: "1.2.3.4"})

	// Unknown addresses get the same answer and no mail.
	if err := svc.RequestPasswordReset(ctx, "nobody@example.net", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	wantKind(t, svc.RequestPasswordReset(ctx, "not an address", "1.2.3.4"), Invalid)
	if len(w.mail.sent) != 0 {
		t.Fatalf("sent %+v", w.mail.sent)
	}

	if err := svc.RequestPasswordReset(ctx, "Alice@Example.net", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if len(w.mail.sent) != 1 {
		t.Fatalf("sent %d", len(w.mail.sent))
	}
	m := w.mail.sent[0]
	if m.From != "no-reply@rootsite.example.com" || m.To != "alice@example.net" || !strings.Contains(m.Subject, "panel.rootsite.example.com") {
		t.Fatalf("%+v", m)
	}
	link := regexp.MustCompile(`https://panel\.rootsite\.example\.com:8443/#/reset-password\?token=([A-Za-z0-9_-]+)`).FindStringSubmatch(m.Text)
	if link == nil {
		t.Fatalf("no link in %q", m.Text)
	}
	token := link[1]

	wantKind(t, svc.ResetPassword(ctx, token, "short"), Invalid)
	wantKind(t, svc.ResetPassword(ctx, "wrong-token", "alice-password-2"), Invalid)
	if err := svc.ResetPassword(ctx, token, "alice-password-2"); err != nil {
		t.Fatal(err)
	}
	// The activity log names the person, like a sign-in.
	if acts, _ := svc.Activities(ctx, admin, 1); acts[0].Action != "account.password_reset" || acts[0].Actor != "alice" || acts[0].Target != "alice" {
		t.Fatalf("%+v", acts[0])
	}
	// The link works once; old sessions and the old password end.
	err := svc.ResetPassword(ctx, token, "alice-password-3")
	wantKind(t, err, Invalid)
	if !strings.Contains(err.Error(), "invalid or has expired") {
		t.Fatalf("message %q", err)
	}
	_, err = svc.SessionActor(ctx, old)
	wantKind(t, err, Unauthenticated)
	_, _, err = svc.Login(ctx, Credentials{Name: "alice", Password: "alice-password-1", ClientIP: "1.2.3.4"})
	wantKind(t, err, Unauthenticated)
	if _, _, err := svc.Login(ctx, Credentials{Name: "alice", Password: "alice-password-2", ClientIP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}

	// The link follows the panel domain current at the time of the request.
	setPanelDomain(t, svc, "admin.rootsite.example.com")
	w.now = w.now.Add(2 * time.Hour) // past the per-address limit window
	if err := svc.RequestPasswordReset(ctx, "alice@example.net", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if got := w.mail.sent[len(w.mail.sent)-1].Text; !strings.Contains(got, "https://admin.rootsite.example.com:8443/#/reset-password?token=") {
		t.Fatalf("link not on the current panel domain: %q", got)
	}

	// A mail that cannot be sent looks the same to the person, and admins
	// see it in the activity log.
	w.mail.set(func(f *fakeMail) { f.sendErr = errors.New("socket gone") })
	w.now = w.now.Add(2 * time.Hour)
	if err := svc.RequestPasswordReset(ctx, "alice@example.net", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	acts, _ := svc.Activities(ctx, admin, 5)
	if acts[0].Action != "account.password_reset_failed" {
		t.Fatalf("%+v", acts[0])
	}
}

func TestPasswordResetLimits(t *testing.T) {
	w, _ := resetWorld(t)
	svc := w.svc
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := svc.RequestPasswordReset(ctx, "alice@example.net", "1.2.3.4"); err != nil {
			t.Fatal(err)
		}
	}
	wantKind(t, svc.RequestPasswordReset(ctx, "alice@example.net", "5.6.7.8"), Throttled)
	if len(w.mail.sent) != 3 {
		t.Fatalf("sent %d", len(w.mail.sent))
	}
}

func TestPasswordResetUnavailable(t *testing.T) {
	ctx := context.Background()
	// Without a mail service.
	svc, _, _ := newService(t)
	if svc.PasswordResetAvailable(ctx) {
		t.Fatal("available without a mail service")
	}
	wantKind(t, svc.RequestPasswordReset(ctx, "a@example.net", "1.2.3.4"), NotFound)

	// With one, but the panel domain is not under an active mail domain.
	w, _ := resetWorld(t)
	setPanelDomain(t, w.svc, "panel.example.org")
	if w.svc.PasswordResetAvailable(ctx) {
		t.Fatal("available without a mail domain for the panel domain")
	}
	wantKind(t, w.svc.RequestPasswordReset(ctx, "alice@example.net", "1.2.3.4"), NotFound)
	if len(w.mail.sent) != 0 {
		t.Fatal("mail sent")
	}
}

func TestOwnEmailNeedsThePassword(t *testing.T) {
	svc, _, _ := newService(t)
	withClock(svc)
	ctx := context.Background()
	if _, err := svc.CreateUser(ctx, admin, NewUser{Name: "alice", Password: "alice-password-1", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	_, secret, _ := svc.Login(ctx, Credentials{Name: "alice", Password: "alice-password-1", ClientIP: "1.2.3.4"})
	a, _ := svc.SessionActor(ctx, secret)

	// Wrong guesses count per person, like changing the password.
	for i := 0; i < loginMaxPerName; i++ {
		_, err := svc.SetOwnEmail(ctx, a, "wrong-password-1", "a@example.net")
		wantKind(t, err, Invalid)
	}
	_, err := svc.SetOwnEmail(ctx, a, "alice-password-1", "a@example.net")
	wantKind(t, err, Throttled)
	wantKind(t, svc.ChangePassword(ctx, a, "alice-password-1", "alice-password-2"), Throttled)

	// An invalid address is reported before the password is checked.
	_, err = svc.SetOwnEmail(ctx, a, "", "not an address")
	wantKind(t, err, Invalid)
	if !strings.Contains(err.Error(), "valid e-mail address") {
		t.Fatalf("message %q", err)
	}

	// Admins change other people's addresses without a password.
	e := "alice@example.org"
	if _, err := svc.UpdateUser(ctx, admin, a.UserID, UserUpdate{Email: &e}); err != nil {
		t.Fatal(err)
	}
}
