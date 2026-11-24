package panel

import (
	"errors"
	"fmt"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// Kind tells callers what went wrong without string matching.
type Kind int

const (
	// Internal is everything that is not the caller's fault (the zero value,
	// so unclassified errors land here).
	Internal Kind = iota
	// NotFound: the thing does not exist, or the actor may not see it.
	NotFound
	// Invalid: bad input (name, domain, archive, password, setup code).
	Invalid
	// Conflict: the request is fine but cannot be done in the current state
	// (nothing to go back to, name taken, last admin).
	Conflict
	// NotSynced: the change is saved, but the web server could not be updated
	// yet. The background loop keeps retrying.
	NotSynced
	// Forbidden: the actor is known but may not do this.
	Forbidden
	// Unauthenticated: nobody is signed in, or the credentials are wrong.
	Unauthenticated
	// Throttled: too many failed attempts, or an outside service's rate
	// limit was hit; try again later.
	Throttled
	// Unavailable: an outside service (e.g. the DNS provider) could not be
	// reached or failed on its side.
	Unavailable
)

func (k Kind) String() string {
	switch k {
	case NotFound:
		return "not found"
	case Invalid:
		return "invalid"
	case Conflict:
		return "conflict"
	case NotSynced:
		return "not synced"
	case Forbidden:
		return "forbidden"
	case Unauthenticated:
		return "unauthenticated"
	case Throttled:
		return "throttled"
	case Unavailable:
		return "unavailable"
	}
	return "internal"
}

// Error is the error type of every Service method.
type Error struct {
	Kind Kind
	// Msg is meant to be shown to people as it is; for Internal errors it may
	// contain details, so front doors should not forward it.
	Msg string
	Err error // underlying cause, if any
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the kind of err, or Internal if it is not an *Error.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return Internal
}

func newError(kind Kind, err error) *Error {
	return &Error{Kind: kind, Msg: err.Error(), Err: err}
}

func errorf(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// storeError classifies errors of the store.
func storeError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return newError(NotFound, err)
	case errors.Is(err, store.ErrInvalid):
		return newError(Invalid, err)
	case errors.Is(err, store.ErrConflict):
		return newError(Conflict, err)
	}
	return newError(Internal, err)
}
