package directory

import (
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// TestPruneGrantsSetByPanel: the panel changing a mailbox's grants at or
// after the mailbox's own change drops that change from the overlay.
func TestPruneGrantsSetByPanel(t *testing.T) {
	at := time.Unix(1000, 0)
	grants := []config.Grant{{StorageID: sid('b'), Rights: []string{config.RightRead}}}
	o := &Overlay{}
	o.Apply(config.AccountChange{Revision: 5, Grants: &grants}, at)
	o.PruneFor(config.Mailbox{Revision: 1, GrantsSetAt: 999})
	if o.Grants == nil {
		t.Fatal("dropped although the panel's change was older")
	}
	o.PruneFor(config.Mailbox{Revision: 1, GrantsSetAt: 1000})
	if o.Grants != nil {
		t.Fatal("kept although the panel changed the grants afterwards")
	}
}
