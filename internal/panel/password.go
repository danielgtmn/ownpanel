package panel

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

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

	minPasswordLen = 10
	maxPasswordLen = 200
)

// hashSlots bounds concurrent hashing: each one takes 64 MiB.
var hashSlots = make(chan struct{}, 4)

func validatePassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < minPasswordLen || n > maxPasswordLen {
		return errorf(Invalid, "the password must be between %d and %d characters long", minPasswordLen, maxPasswordLen)
	}
	return nil
}

func hashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hashSlots <- struct{}{}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-hashSlots
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// verifyPassword compares pw with an encoded argon2id hash in constant time.
// Malformed hashes never match.
func verifyPassword(encoded, pw string) bool {
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
	// Bounds keep a corrupted row from exhausting memory or CPU.
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
	hashSlots <- struct{}{}
	got := argon2.IDKey([]byte(pw), salt, time, memory, threads, uint32(len(want)))
	<-hashSlots
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is verified against when a user does not exist, so a login for
// an unknown name takes as long as one with a wrong password.
var dummyHash = sync.OnceValue(func() string {
	h, err := hashPassword(randomString(32))
	if err != nil {
		panic(err)
	}
	return h
})
