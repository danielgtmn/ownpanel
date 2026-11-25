package panel

import (
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/passwd"
)

// Password length of panel users, in characters (sesam is configured with
// the same bounds; checking here first keeps the panel's wording).
const (
	minPasswordLen = 10
	maxPasswordLen = 200
)

func validatePassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < minPasswordLen || n > maxPasswordLen {
		return errorf(Invalid, "the password must be between %d and %d characters long", minPasswordLen, maxPasswordLen)
	}
	return nil
}

// Mailbox passwords are hashed here (panel users' passwords by sesam).
// mailboxHashSlots bounds concurrent hashing: each one takes 64 MiB.
var mailboxHashSlots = passwd.NewSlots(4)

func hashPassword(pw string) (string, error) { return mailboxHashSlots.Hash(pw) }
