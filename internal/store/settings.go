package store

import (
	"context"
	"maps"
	"slices"

	"github.com/danielgtmn/ablage"
)

// Settings returns every stored setting by key.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	list, err := s.r.settings.Find(ctx, ablage.Query{})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, v := range list {
		out[v.ID] = v.Value
	}
	return out, nil
}

// PutSettings stores several settings at once, atomically. An empty value
// removes the key.
func (s *Store) PutSettings(ctx context.Context, values map[string]string) error {
	return s.tx(ctx, func(r repos) error {
		for _, k := range slices.Sorted(maps.Keys(values)) {
			var err error
			if v := values[k]; v == "" {
				_, err = r.settings.DeleteWhere(ctx, ablage.Eq("id", k))
			} else {
				err = r.settings.Upsert(ctx, &settingRow{ID: k, Value: v})
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}
