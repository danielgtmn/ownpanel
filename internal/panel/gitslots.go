package panel

import (
	"context"
	"sync"
	"time"
)

// Connections to Git hosts are limited in three ways, so one person or one
// site cannot take the panel's memory, disk or connections:
//
//   - a rate per person (gitChecks, gitsource.go);
//   - checks (fetching a server key, asking for branches): at most
//     gitCheckSlots at once in the whole panel, at most gitCheckPerKey per
//     person (or per site for the panel's own checks);
//   - fetches for builds: at most gitFetchSlots at once, one per site.
//
// A check waits at most gitSlotWait for a free slot and is then refused as
// busy; a fetch waits as long as its context allows (builds wait in line).
const (
	gitCheckSlots  = 4
	gitCheckPerKey = 2
	gitFetchSlots  = 2
	gitFetchPerKey = 1
	gitSlotWait    = 20 * time.Second
)

// slotLimiter hands out a bounded number of slots in all and per key.
type slotLimiter struct {
	mu     sync.Mutex
	size   int
	perKey int
	slots  chan struct{}
	keys   map[string]int
}

func (l *slotLimiter) init() {
	if l.slots == nil {
		l.slots = make(chan struct{}, l.size)
		l.keys = map[string]int{}
	}
}

// acquire takes a slot for key. It refuses at once when key already holds
// its share, and waits at most wait (0: as long as ctx allows) for one of
// the panel's slots.
func (l *slotLimiter) acquire(ctx context.Context, key string, wait time.Duration) (func(), error) {
	l.mu.Lock()
	l.init()
	if l.keys[key] >= l.perKey {
		l.mu.Unlock()
		return nil, errorf(Throttled, "another connection to a Git server for the same person or site is still running; try again when it has finished")
	}
	l.keys[key]++
	slots := l.slots
	l.mu.Unlock()
	undo := func() {
		l.mu.Lock()
		if l.keys[key]--; l.keys[key] <= 0 {
			delete(l.keys, key)
		}
		l.mu.Unlock()
	}
	var timeout <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		undo()
		return nil, gitError(ctx.Err())
	case <-timeout:
		undo()
		return nil, errorf(Throttled, "the panel is busy connecting to other Git servers; try again in a minute")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-slots
			undo()
		})
	}, nil
}

// beginGitCheck takes the rate limit and a check slot for a person.
func (s *Service) beginGitCheck(ctx context.Context, a Actor) (func(), error) {
	if err := s.allowGitCheck(a); err != nil {
		return nil, err
	}
	key := "user:" + a.UserID
	if a.Via == ViaSystem {
		key = "system"
	}
	return s.gitCheckSlots.acquire(ctx, key, gitSlotWait)
}
