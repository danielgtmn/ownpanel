package store

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
)

func TestSettingsRoundTrip(t *testing.T) {
	s, _ := open(t)
	got, err := s.Settings(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("fresh database: %v %v", got, err)
	}
	if err := s.PutSettings(ctx, map[string]string{"panel_domain": "panel.example.com", "server_ips": "203.0.113.5"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSettings(ctx, map[string]string{"panel_domain": "other.example.com", "server_ips": ""}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"panel_domain": "other.example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("settings %v, want %v", got, want)
	}
}

func TestFreshInstallIsNotSetUp(t *testing.T) {
	s, _ := open(t)
	if _, err := s.CreateUser(ctx, "root", "", RoleAdmin, true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Settings(ctx)
	if _, ok := got["setup_complete"]; ok {
		t.Fatal("a new install must go through the wizard")
	}
}

// A database from before the wizard that already has users counts as set
// up; one without users does not.
func TestSettingsMigrationMarksOldInstallsComplete(t *testing.T) {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, name := range []string{"0001_init.sql", "0002_dns_connections.sql"} {
		b, err := fs.ReadFile(sub, name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: b}
	}
	for _, withUser := range []bool{true, false} {
		file := filepath.Join(t.TempDir(), "ownpanel.db")
		db, err := sql.Open("sqlite", file)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if err := migrate(db, old); err != nil {
			t.Fatal(err)
		}
		if withUser {
			if _, err := db.Exec(`INSERT INTO users (name, password_hash, role, created_at) VALUES ('root', 'x', 'admin', 0)`); err != nil {
				t.Fatal(err)
			}
		}
		db.Close()

		s, err := Open(file)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Settings(ctx)
		s.Close()
		if err != nil {
			t.Fatal(err)
		}
		if complete := got["setup_complete"] == "true"; complete != withUser {
			t.Errorf("with users %v: setup_complete %q", withUser, got["setup_complete"])
		}
	}
}
