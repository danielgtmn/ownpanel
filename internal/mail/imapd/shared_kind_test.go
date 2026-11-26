package imapd

import (
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// TestSharedKindChangeWhileSelected: when a delegated personal mailbox
// becomes shared while one of its folders is open, the read state changes
// meaning (the mailbox's own → each reader's), so the selection ends.
func TestSharedKindChangeWhileSelected(t *testing.T) {
	se := startShared(t)
	se.put(idBoss, "INBOX", simple("for the boss"))
	a, _ := se.as(alice)
	if _, err := a.Select("Shared/"+boss+"/INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	cfg := sharedConfig(t)
	for i := range cfg.Mailboxes {
		if cfg.Mailboxes[i].StorageID == idBoss {
			cfg.Mailboxes[i].Kind, cfg.Mailboxes[i].PasswordHash = config.KindShared, ""
		}
	}
	se.reconfigure(cfg)
	if _, err := a.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect(); err == nil {
		t.Fatal("selection goes on with the old read state after the mailbox became shared")
	}
	// A new session works with the new kind: its own read state.
	a2, _ := se.as(alice)
	if _, err := a2.Select("Shared/"+boss+"/INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := a2.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close(); err != nil {
		t.Errorf("own read state after the change: %v", err)
	}
}
