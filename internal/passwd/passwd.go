// Package passwd hashes and verifies passwords with argon2id. The panel uses
// it for its users and for mailbox passwords; the mail service verifies
// mailbox passwords with it, so both read the same format.
package passwd

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters for new hashes. Verification reads the parameters
// from the stored hash, so these can be raised later.
const (
	argonTime    = 2
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonSaltLen = 16
	argonKeyLen  = 32
)

// Slots bounds concurrent hashing in one process: each hash takes 64 MiB.
type Slots chan struct{}

// NewSlots returns room for n hashes at the same time.
func NewSlots(n int) Slots { return make(Slots, n) }

// Hash returns an encoded argon2id hash of pw.
func (s Slots) Hash(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	s <- struct{}{}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-s
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify compares pw with an encoded argon2id hash in constant time.
// Malformed hashes never match.
func (s Slots) Verify(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	// Bounds keep a corrupted hash from exhausting memory or CPU.
	if memory < 8 || memory > 1<<20 || time < 1 || time > 16 || threads < 1 {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false
	}
	s <- struct{}{}
	got := argon2.IDKey([]byte(pw), salt, time, memory, threads, uint32(len(want)))
	<-s
	return subtle.ConstantTimeCompare(got, want) == 1
}

// WellFormed tells whether encoded looks like a hash Verify can check,
// without computing anything.
func WellFormed(encoded string) bool {
	parts := strings.Split(encoded, "$")
	return len(parts) == 6 && parts[0] == "" && parts[1] == "argon2id" &&
		strings.HasPrefix(parts[2], "v=") && strings.HasPrefix(parts[3], "m=")
}
