package passwd

import (
	"fmt"
	"unicode/utf8"
)

// Rules for new passwords: of panel users and of mailboxes, whether the
// panel or the mailbox itself (in the mail service) sets them. Lengths are
// in characters.
const (
	MinLength = 10
	MaxLength = 200
)

// ErrLength: a new password is too short or too long. Its text is meant
// for people.
var ErrLength = fmt.Errorf("the password must be between %d and %d characters long", MinLength, MaxLength)

// CheckNew tells whether pw may be a new password: ErrLength or nil.
func CheckNew(pw string) error {
	if n := utf8.RuneCountInString(pw); n < MinLength || n > MaxLength {
		return ErrLength
	}
	return nil
}
