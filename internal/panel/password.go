package panel

import (
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/passwd"
)

// Password length of panel users, in characters (sesam is configured with
// the same bounds; checking here first keeps the panel's wording). They are
// the rules mailboxes get too (internal/passwd).
const (
	minPasswordLen = passwd.MinLength
	maxPasswordLen = passwd.MaxLength
)

// validatePassword applies the rules for new passwords, shared with the
// mail service's self-service area, including the one against the form of
// app passwords.
func validatePassword(pw string) error {
	if err := config.CheckNewPassword(pw); err != nil {
		return errorf(Invalid, "%s", err.Error())
	}
	return nil
}

// Mailbox passwords are hashed here (panel users' passwords by sesam).
// mailboxHashSlots bounds concurrent hashing: each one takes 64 MiB.
var mailboxHashSlots = passwd.NewSlots(4)

func hashPassword(pw string) (string, error) { return mailboxHashSlots.Hash(pw) }
