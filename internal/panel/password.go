package panel

import (
	"sync"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/passwd"
)

const (
	minPasswordLen = 10
	maxPasswordLen = 200
)

// hashSlots bounds concurrent hashing: each one takes 64 MiB.
var hashSlots = passwd.NewSlots(4)

func validatePassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < minPasswordLen || n > maxPasswordLen {
		return errorf(Invalid, "the password must be between %d and %d characters long", minPasswordLen, maxPasswordLen)
	}
	if config.LooksLikeAppPassword(pw) {
		// Mail programs would check it as an app password, never as this one.
		return errorf(Invalid, "this looks like an app password; choose another password")
	}
	return nil
}

func hashPassword(pw string) (string, error) { return hashSlots.Hash(pw) }

// verifyPassword compares pw with an encoded argon2id hash in constant time.
// Malformed hashes never match.
func verifyPassword(encoded, pw string) bool { return hashSlots.Verify(encoded, pw) }

// dummyHash is verified against when a user does not exist, so a login for
// an unknown name takes as long as one with a wrong password.
var dummyHash = sync.OnceValue(func() string {
	h, err := hashPassword(randomString(32))
	if err != nil {
		panic(err)
	}
	return h
})
