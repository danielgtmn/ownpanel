package storage

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtmn/ablage"
)

// Greylist rules: a triplet seen for the first time is delayed; once its
// sender retries after Delay (and within Retry), it passes for Valid after
// its last pass.
type Greylist struct {
	Delay time.Duration
	Retry time.Duration
	Valid time.Duration
	// PerNetwork bounds the triplets of one network that have not passed
	// yet, Max all triplets; 0 means no bound. A new triplet beyond either
	// is not recorded and does not pass (fail closed) until pruning makes
	// room again.
	PerNetwork int
	Max        int
}

// greylistRefresh: a passing triplet's last pass is written at most this
// often, so known senders do not cost a write per message.
const greylistRefresh = time.Hour

type greyAction int

const (
	greyNone    greyAction = iota // nothing to write
	greyPass                      // record a pass now
	greyRestart                   // expired: start over
)

func greyDecide(first, passed int64, now time.Time, g Greylist) (bool, greyAction) {
	t := now.Unix()
	switch {
	case passed > 0 && t-passed <= int64(g.Valid/time.Second):
		if t-passed >= int64(greylistRefresh/time.Second) {
			return true, greyPass
		}
		return true, greyNone
	case passed == 0 && t-first >= int64(g.Delay/time.Second) && t-first <= int64(g.Retry/time.Second):
		return true, greyPass
	case passed > 0 || t-first > int64(g.Retry/time.Second):
		return false, greyRestart
	default:
		return false, greyNone // still waiting
	}
}

// unix is a stored time in Unix seconds, 0 for nil.
func unix(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}

// GreylistCheck records an attempt of the triplet key from network (the
// unit PerNetwork counts in) at now and tells whether it may pass. An
// attempt that changes nothing (still waiting, or passing again shortly
// after the last pass) does not write.
func (s *Store) GreylistCheck(ctx context.Context, key, network string, now time.Time, g Greylist) (bool, error) {
	r, err := s.r.greylist.Get(ctx, key)
	if err == nil {
		if pass, act := greyDecide(r.First.Unix(), unix(r.Passed), now, g); act == greyNone {
			return pass, nil
		}
	} else if !errors.Is(err, ablage.ErrNotFound) {
		return false, err
	}
	pass := false
	err = s.write(ctx, func(tx *repos, _ *changes) error {
		r, err := tx.greylist.Get(ctx, key)
		if errors.Is(err, ablage.ErrNotFound) {
			if full, err := greyFull(ctx, tx, network, g); err != nil || full {
				return err
			}
			return tx.greylist.Create(ctx, &greylistRec{ID: key, Net: network, First: sec(now)})
		}
		if err != nil {
			return err
		}
		var act greyAction
		pass, act = greyDecide(r.First.Unix(), unix(r.Passed), now, g)
		switch act {
		case greyPass:
			t := sec(now)
			err = tx.greylist.Patch(ctx, key, ablage.Row{"passed": t})
		case greyRestart:
			err = tx.greylist.Patch(ctx, key, ablage.Row{"first": sec(now), "passed": nil})
		}
		return err
	})
	return pass, err
}

// greyFull tells whether a new triplet of network would exceed g's bounds.
func greyFull(ctx context.Context, tx *repos, network string, g Greylist) (bool, error) {
	if g.PerNetwork > 0 {
		n, err := tx.greylist.Count(ctx, ablage.And(ablage.Eq("net", network), ablage.Null("passed")))
		if err != nil {
			return false, err
		}
		if n >= int64(g.PerNetwork) {
			return true, nil
		}
	}
	if g.Max > 0 {
		n, err := tx.greylist.Count(ctx, ablage.All)
		if err != nil {
			return false, err
		}
		if n >= int64(g.Max) {
			return true, nil
		}
	}
	return false, nil
}

// PruneGreylist deletes triplets that never passed within g.Retry and
// passed ones whose last pass is older than g.Valid.
func (s *Store) PruneGreylist(ctx context.Context, now time.Time, g Greylist) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		_, err := tx.greylist.DeleteWhere(ctx, ablage.Or(
			ablage.And(ablage.Null("passed"), ablage.Lt("first", sec(now.Add(-g.Retry)))),
			ablage.Lt("passed", sec(now.Add(-g.Valid)))))
		return err
	})
}
