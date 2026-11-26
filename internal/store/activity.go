package store

import (
	"context"
	"time"

	"github.com/danielgtmn/ablage"
)

// Activity is one entry of the activity log.
type Activity struct {
	ID     string
	At     time.Time
	Actor  string
	Action string
	Target string
	Detail string
}

// AddActivity appends an entry; At is set to now.
func (s *Store) AddActivity(ctx context.Context, a Activity) error {
	return s.tx(ctx, func(r repos) error {
		// The next number; transactions are serialized (see Open), so no
		// one else takes it in between.
		res, err := r.activity.Aggregate(ctx, ablage.Aggregation{Values: []ablage.Agg{ablage.Max("seq", "last")}})
		if err != nil {
			return err
		}
		last, _ := res[0]["last"].(int64)
		return r.activity.Create(ctx, &activityRow{Seq: last + 1, At: s.now(), Actor: a.Actor, Action: a.Action, Target: a.Target, Detail: a.Detail})
	})
}

// Activities returns the newest entries first.
func (s *Store) Activities(ctx context.Context, limit int) ([]Activity, error) {
	list, err := s.r.activity.Find(ctx, ablage.Query{
		OrderBy: []ablage.Order{ablage.Desc("seq")},
		Limit:   max(limit, 0),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Activity, len(list))
	for i, a := range list {
		out[i] = Activity{ID: a.ID, At: a.At, Actor: a.Actor, Action: a.Action, Target: a.Target, Detail: a.Detail}
	}
	return out, nil
}
