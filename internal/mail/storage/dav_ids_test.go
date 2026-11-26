package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
)

// TestDAVCollectionIDsNeverReused: a calendar deleted and created again
// (the default one comes back by itself) gets a new id, so ETags and sync
// tokens of the old one never match the new one.
func TestDAVCollectionIDsNeverReused(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir(), 0)
	if err := s.EnsureDefaultDAVCollections(ctx, idA); err != nil {
		t.Fatal(err)
	}
	// The address book was created last, so its id is the highest.
	old, err := s.DAVCollection(ctx, idA, davobj.AddressBook, DefaultAddressBook)
	if err != nil {
		t.Fatal(err)
	}
	card := []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:c1\r\nFN:X\r\nEND:VCARD\r\n")
	info, _, err := davobj.ParseCard(card)
	if err != nil {
		t.Fatal(err)
	}
	oldObj, _, err := s.PutDAVObject(ctx, old, "c1.vcf", card, info, PutCondition{})
	if err != nil {
		t.Fatal(err)
	}
	old, _ = s.DAVCollection(ctx, idA, davobj.AddressBook, DefaultAddressBook)
	oldToken := old.SyncToken()
	if err := s.DeleteDAVCollection(ctx, idA, old.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureDefaultDAVCollections(ctx, idA); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.DAVCollection(ctx, idA, davobj.AddressBook, DefaultAddressBook)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == old.ID {
		t.Fatalf("id %d reused", fresh.ID)
	}
	if _, _, err := s.DAVChanges(ctx, fresh, oldToken); !errors.Is(err, ErrInvalidSyncToken) {
		t.Fatalf("old token accepted: %v", err)
	}
	newObj, _, err := s.PutDAVObject(ctx, fresh, "c1.vcf", card, info, PutCondition{})
	if err != nil {
		t.Fatal(err)
	}
	if newObj.ETag() == oldObj.ETag() {
		t.Fatal("ETag of the old object matches the new one")
	}
}
