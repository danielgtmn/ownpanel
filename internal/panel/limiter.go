package panel

import (
	"sync"
	"time"
)

// limiter counts failed attempts per key (client address and name) in
// memory. After max failures within window the key is blocked until the
// oldest failure leaves the window.
type limiter struct {
	mu        sync.Mutex
	max       int
	window    time.Duration
	now       func() time.Time
	fails     map[string][]time.Time
	lastSweep time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, now: time.Now, fails: map[string][]time.Time{}}
}

// attempt registers an attempt for key before it is checked, so parallel
// guesses cannot slip past the limit. It returns how long the caller must
// wait (0: go ahead). A successful attempt must call succeed afterwards.
func (l *limiter) attempt(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	recent := l.recent(key, now)
	if len(recent) >= l.max {
		return recent[0].Add(l.window).Sub(now)
	}
	l.fails[key] = append(recent, now)
	return 0
}

func (l *limiter) succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

func (l *limiter) recent(key string, now time.Time) []time.Time {
	list := l.fails[key]
	i := 0
	for i < len(list) && !list[i].After(now.Add(-l.window)) {
		i++
	}
	return list[i:]
}

// sweep drops stale keys now and then, so the map cannot grow without bound.
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for key := range l.fails {
		if len(l.recent(key, now)) == 0 {
			delete(l.fails, key)
		}
	}
}

func throttled(wait time.Duration) error {
	minutes := int((wait + time.Minute - 1) / time.Minute)
	unit := "minutes"
	if minutes == 1 {
		unit = "minute"
	}
	return errorf(Throttled, "too many failed attempts. Please try again in %d %s.", minutes, unit)
}
