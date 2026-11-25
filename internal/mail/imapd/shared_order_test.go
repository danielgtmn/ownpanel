package imapd

import (
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/directory"
)

// TestReconfigureInOrder: configuration changes reach a session in the
// order they were made, even when applying one takes a while: the last
// one wins.
func TestReconfigureInOrder(t *testing.T) {
	se := startShared(t)
	se.as(alice)
	without := sharedConfig(t)
	for i := range without.Mailboxes {
		without.Mailboxes[i].Grants = nil
	}
	withDir, withoutDir := directory.New(sharedConfig(t)), directory.New(without)
	reconfigureDelay = func(d *directory.Directory) {
		if d == withoutDir {
			time.Sleep(200 * time.Millisecond)
		}
	}
	t.Cleanup(func() { reconfigureDelay = nil })
	se.dir.Store(withoutDir)
	se.srv.Reconfigure()
	se.dir.Store(withDir)
	se.srv.Reconfigure()
	time.Sleep(500 * time.Millisecond)

	se.srv.mu.Lock()
	var sess *session
	for s := range se.srv.sessions {
		sess = s
	}
	se.srv.mu.Unlock()
	sess.watch.mu.Lock()
	_, watched := sess.watch.cancel[idTeam]
	sess.watch.mu.Unlock()
	if !watched {
		t.Fatal("an older configuration was applied after the newer one")
	}
}
