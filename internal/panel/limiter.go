package panel

import (
	"context"
	"time"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// limiter counts attempts per key (client address and name, or the address
// alone) in the database, so a restart does not reset it. After max
// attempts within window the key is blocked until the oldest one leaves the
// window. Keys are stored hashed.
type limiter struct {
	store  *store.Store
	max    int
	window time.Duration
	now    func() time.Time
}

func newLimiter(st *store.Store, max int, window time.Duration) *limiter {
	return &limiter{store: st, max: max, window: window, now: time.Now}
}

// attempt registers an attempt for key before it is checked, so parallel
// guesses cannot slip past the limit. It returns the id of the recorded
// attempt and how long the caller must wait (0: go ahead).
func (l *limiter) attempt(ctx context.Context, key string) (int64, time.Duration, error) {
	now := l.now()
	id, oldest, err := l.store.AddAttempt(ctx, sha256Hex(key), now, l.window, l.max)
	if err != nil {
		return 0, 0, newError(Internal, err)
	}
	if id == 0 {
		wait := oldest.Add(l.window).Sub(now)
		if wait < time.Second {
			wait = time.Second
		}
		return 0, wait, nil
	}
	return id, 0, nil
}

// succeed forgets every attempt for key.
func (l *limiter) succeed(ctx context.Context, key string) error {
	if err := l.store.DeleteAttempts(ctx, sha256Hex(key)); err != nil {
		return newError(Internal, err)
	}
	return nil
}

// forget removes one attempt that turned out to be fine.
func (l *limiter) forget(ctx context.Context, id int64) error {
	if err := l.store.DeleteAttempt(ctx, id); err != nil {
		return newError(Internal, err)
	}
	return nil
}

func throttled(wait time.Duration) error {
	minutes := int((wait + time.Minute - 1) / time.Minute)
	unit := "minutes"
	if minutes == 1 {
		unit = "minute"
	}
	return errorf(Throttled, "too many failed attempts. Please try again in %d %s.", minutes, unit)
}

// errBusy answers when too many passwords are being checked at once.
func errBusy() error {
	return errorf(Throttled, "too many sign-in attempts right now. Please try again in a minute.")
}
