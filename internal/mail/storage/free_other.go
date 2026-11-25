//go:build !unix

package storage

import "math"

// FreeBytes is not measured on this platform; it reports plenty.
func (s *Store) FreeBytes() (int64, error) { return math.MaxInt64, nil }
