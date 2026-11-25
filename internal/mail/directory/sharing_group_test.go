package directory

import (
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// TestSendAsNotForGroups: the send-as right on one mailbox does not cover
// an alias that also delivers elsewhere (a group).
func TestSendAsNotForGroups(t *testing.T) {
	cfg := sharingDir().Config()
	next := *cfg
	next.Aliases = append(append([]config.Alias{}, cfg.Aliases...),
		config.Alias{Address: "mixed@example.com", Targets: []string{"team@example.com", "ben@example.de"}},
		config.Alias{Address: "out@example.com", Targets: []string{"team@example.com", "x@outside.example"}},
	)
	d := New(&next)
	anna, _ := d.Mailbox("anna@example.com")
	for _, addr := range []string{"mixed@example.com", "out@example.com"} {
		if mode, _ := d.SendMode(anna, addr); mode != SendDenied {
			t.Errorf("send as the group %s: %v", addr, mode)
		}
	}
	if mode, _ := d.SendMode(anna, "hello@example.com"); mode != SendAsGrant {
		t.Errorf("alias of only the shared mailbox: %v", mode)
	}
}
