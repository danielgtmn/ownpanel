package imapd

import (
	"context"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// TestReservedSharedNames: renaming a parent cannot put own folders under
// Shared/<address>; own folders of such names from before (an import,
// another server) stay reachable when no shared mailbox has the address.
func TestReservedSharedNames(t *testing.T) {
	se := startShared(t)
	a, _ := se.as(alice)
	for _, n := range []string{"Foo", "Foo/someone@example.org", "Foo/someone@example.org/Notes"} {
		if err := a.Create(n, nil).Wait(); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}
	if err := a.Rename("Foo", "Shared", nil).Wait(); code(err) != imap.ResponseCodeCannot {
		t.Errorf("rename of a parent into the reserved names: %v", err)
	}
	if err := a.Rename("Foo/someone@example.org", "Shared/x@example.org", nil).Wait(); code(err) != imap.ResponseCodeCannot {
		t.Errorf("rename into the reserved names: %v", err)
	}
	if err := a.Create("Shared/new@example.org/Mine", nil).Wait(); code(err) != imap.ResponseCodeCannot {
		t.Errorf("create under the reserved names: %v", err)
	}

	// An own folder of a reserved name from before.
	if err := se.store.CreateFolder(context.Background(), idAlice, "Shared/old@example.org/Notes"); err != nil {
		t.Fatal(err)
	}
	se.put(idAlice, "Shared/old@example.org/Notes", simple("kept"))
	if _, ok := listNames(t, a, nil)["Shared/old@example.org/Notes"]; !ok {
		t.Error("old own folder not listed")
	}
	sd, err := a.Select("Shared/old@example.org/Notes", nil).Wait()
	if err != nil || sd.NumMessages != 1 || sd.ReadOnly {
		t.Fatalf("old own folder: %+v %v", sd, err)
	}
	// The name of a shared mailbox one may read always means that mailbox.
	sd, err = a.Select(stName, nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
}
