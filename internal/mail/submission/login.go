package submission

import (
	"errors"

	"github.com/emersion/go-smtp"
)

// loginServer is the server side of the obsolete but common SASL LOGIN
// mechanism (go-sasl has only the client): the server asks for the user
// name and the password, each base64-encoded by the protocol. A user name
// sent with the AUTH command (initial response) skips the first question.
type loginServer struct {
	auth func(user, pass string) error
	step int
	user string
}

var errLoginSequence = errors.New("unexpected LOGIN step")

func (l *loginServer) Next(response []byte) ([]byte, bool, error) {
	switch l.step {
	case 0:
		if len(response) == 0 {
			l.step = 1
			return []byte("Username:"), false, nil
		}
		l.user = string(response)
		l.step = 2
		return []byte("Password:"), false, nil
	case 1:
		l.user = string(response)
		l.step = 2
		return []byte("Password:"), false, nil
	case 2:
		l.step = 3
		if l.user == "" {
			return nil, true, smtp.ErrAuthFailed
		}
		if err := l.auth(l.user, string(response)); err != nil {
			return nil, true, err
		}
		return nil, true, nil
	}
	return nil, false, errLoginSequence
}
