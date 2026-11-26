package server

import (
	"context"
	"errors"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/rules"
	"github.com/danielgtmn/ownpanel/internal/mail/sieve"
)

// A script a mail program activates while the page saves is not silently
// switched off again: the save fails instead.
func TestUpdateRulesDoesNotRevertConcurrentActivation(t *testing.T) {
	ctx := context.Background()
	in := run(t, t.TempDir(), socketDir(t))
	defer in.stop(t)
	if err := in.client.Load(ctx, testConfig(t)); err != nil {
		t.Fatal(err)
	}
	st := in.srv.Store()
	_, err := in.srv.UpdateRules(ctx, idAlice, false, func(s *rules.Settings) error {
		// Between reading and writing, a mail program activates its own
		// script (ManageSieve SETACTIVE).
		if err := st.PutSieveScript(ctx, idAlice, "mine", "keep;", sieve.MaxScripts, true); err != nil {
			t.Fatal(err)
		}
		s.OutOfOffice = config.OutOfOffice{Enabled: true, Text: "away", Days: 7}
		return nil
	})
	if !errors.Is(err, ErrNotManaged) {
		t.Fatalf("save went through: %v", err)
	}
	if name, _, _, _ := st.ActiveSieveScript(ctx, idAlice); name != "mine" {
		t.Fatalf("active %q: the mail program's script was switched off", name)
	}
}
