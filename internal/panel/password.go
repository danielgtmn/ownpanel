package panel

import (
	"sync"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/passwd"
)

// hashSlots bounds concurrent hashing: each one takes 64 MiB.
var hashSlots = passwd.NewSlots(4)

// validatePassword applies the rules for new passwords (shared with the
// mail service's self-service area). Panel users get the same rules as
// mailboxes, including the one against the form of app passwords.
func validatePassword(pw string) error {
	if err := config.CheckNewPassword(pw); err != nil {
		return errorf(Invalid, "%s", err.Error())
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
