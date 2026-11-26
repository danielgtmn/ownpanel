package panel

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// The sign-in users of ../store/testdata/sesam_v1.sql, written by sesam
// v1.0.0 at sesamV1Time.
const (
	sesamV1Root         = "v1-signin-root"
	sesamV1Bob          = "v1-signin-bob"
	sesamV1RootCookie   = "B7ljy4WKTe3_NpTkn7uvXCP0kYLeRoKptLY2irdt6-k"
	sesamV1BobResetLink = "0pGy5dJb-B1S1TDJQXUz8QD1NEeBGD49-_fknAJ3LR4"
)

var sesamV1Time = time.Date(2026, 11, 20, 10, 0, 0, 123456000, time.UTC)

// TestSignInFromSesamV1: the auth_* tables as sesam 1.x (the panel's
// sign-in before sesam v2) left them keep working as they are: the
// session stays signed in, passwords sign in, failed attempts keep
// counting, a reset link works once.
func TestSignInFromSesamV1(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newEmptyServiceOn(t, func(t *testing.T, dir string) *store.Store {
		file := filepath.Join(dir, "ownpanel.db")
		dump, err := os.ReadFile("../store/testdata/sesam_v1.sql")
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", file+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(dump)); err != nil {
			t.Fatal(err)
		}
		db.Close()
		st := openSQLite(t, dir)
		for _, u := range []struct{ name, auth, role string }{
			{"root", sesamV1Root, store.RoleAdmin}, {"bob", sesamV1Bob, store.RoleMember},
		} {
			if _, err := st.CreateUser(ctx, u.name, u.auth, u.role, false); err != nil {
				t.Fatal(err)
			}
		}
		return st
	})
	clk := withClock(svc)
	clk.t = sesamV1Time.Add(5 * time.Minute)

	// The stray sign-in user without a panel user ("nopw") is gone.
	if n, err := svc.auth.CountUsers(ctx); err != nil || n != 2 {
		t.Fatalf("%d sign-in users (%v)", n, err)
	}
	a, err := svc.SessionActor(ctx, sesamV1RootCookie)
	if err != nil || a.Name != "root" || !a.Admin {
		t.Fatalf("session of sesam v1: %+v %v", a, err)
	}
	if _, _, err := svc.Login(ctx, Credentials{Name: "root", Password: "root-password-1", ClientIP: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	// bob failed 3 times under sesam v1: 2 more failures, then the limit.
	bad := Credentials{Name: "bob", Password: "wrong-password", ClientIP: "198.51.100.1"}
	for range loginMaxPerName - 3 {
		_, _, err := svc.Login(ctx, bad)
		wantKind(t, err, Unauthenticated)
	}
	_, _, err = svc.Login(ctx, Credentials{Name: "bob", Password: "bob-password-1", ClientIP: "198.51.100.1"})
	wantKind(t, err, Throttled)
	// The reset link sesam v1 sent works, once, and the new password
	// signs in once the window is over.
	if err := svc.ResetPassword(ctx, sesamV1BobResetLink, "bob-password-2"); err != nil {
		t.Fatal(err)
	}
	wantKind(t, svc.ResetPassword(ctx, sesamV1BobResetLink, "bob-password-3"), Invalid)
	clk.advance(loginWindow)
	if _, _, err := svc.Login(ctx, Credentials{Name: "bob@example.org", Password: "bob-password-2"}); err != nil {
		t.Fatal(err)
	}
	// Signing out ends the session from sesam v1.
	if err := svc.Logout(ctx, sesamV1RootCookie); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SessionActor(ctx, sesamV1RootCookie)
	wantKind(t, err, Unauthenticated)
}
