package imapd

import (
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// TestCloseAfterLosingWrite: CLOSE on a folder whose write right went away
// while it was open closes it without expunging (as for a read-only
// folder) instead of failing and leaving it open.
func TestCloseAfterLosingWrite(t *testing.T) {
	se := startShared(t)
	se.put(idTeam, "INBOX", simple("one"), storage.FlagDeleted)
	a, _ := se.as(alice)
	if _, err := a.Select(stName, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	cfg := sharedConfig(t)
	for i := range cfg.Mailboxes {
		if cfg.Mailboxes[i].StorageID == idTeam {
			cfg.Mailboxes[i].Grants = []config.Grant{{StorageID: idAlice, Rights: []string{config.RightRead}}}
		}
	}
	se.reconfigure(cfg)
	if err := a.UnselectAndExpunge().Wait(); err != nil {
		t.Fatalf("CLOSE: %v", err)
	}
	if st := a.Mailbox(); st != nil {
		t.Errorf("still selected: %+v", st)
	}
	if st, _ := se.store.FolderStatus(t.Context(), idTeam, "INBOX"); st.Messages != 1 {
		t.Errorf("expunged without the write right: %+v", st)
	}
}
