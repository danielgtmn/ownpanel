package panel

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/danielgtmn/panzerschrank"
)

// TestSealedValuesStayReadable pins values sealed by the panel's former
// internal/secrets package (fixed key, the panel's real purposes). Stored
// DNS tokens, DKIM keys and blocklist zones must open unchanged with the
// existing secret.key; if this fails, data on running servers is lost.
// The purposes are part of the stored format and must not be renamed.
func TestSealedValuesStayReadable(t *testing.T) {
	box, err := panzerschrank.New(bytes.Repeat([]byte{0x42}, panzerschrank.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ purpose, plain, sealed string }{
		{secretPurpose, "dummy-dns-token", "082cc29aec06b56cd4c39d5d2454cbc8ec3d3e05a1fef45a9cc9476764c76bb09776011bc6e6a6028cd634"},
		{dkimPurpose, "dummy-dkim-key", "e7ecdf7a2ad2bd60d4b59f3836a0bfbdbee50c1ab946cd7d478bd54d02935896bc8c9d27c5bb514c3333"},
		{blocklistPurpose, "zen.example.org", "c638b80ca5632b0270188db482b4580703740c281666a0d8a7027f0791504d213355193532635bc40bba0a"},
	} {
		sealed, _ := hex.DecodeString(c.sealed)
		got, err := box.Open(sealed, c.purpose)
		if err != nil || string(got) != c.plain {
			t.Errorf("%s: got %q, %v", c.purpose, got, err)
		}
	}
}
