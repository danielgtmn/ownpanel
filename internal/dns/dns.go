// Package dns is a provider-neutral layer for managing DNS records of a
// domain, plus small helpers to find the right zone and to check whether a
// domain already points at this server.
package dns

import (
	"context"
	"errors"
)

// Zone is a DNS zone at a provider.
type Zone struct {
	ID       string // provider id
	Name     string // "example.com", lower case, no trailing dot
	ReadOnly bool   // e.g. a secondary zone: records cannot be changed here
}

// RecordSet is all records of one name and type.
type RecordSet struct {
	Name   string   // relative to the zone: "www", "@" for the zone itself
	Type   string   // "A", "AAAA", "CNAME", "TXT", "MX", ...
	TTL    int      // seconds; 0 = the zone's default
	Values []string // provider-neutral: TXT values are the plain text, unquoted
	Locked bool     // provider refuses changes (protected)
}

// Provider manages records at one DNS provider account.
type Provider interface {
	Zones(ctx context.Context) ([]Zone, error)
	// RecordSets lists all record sets of a zone (zone = Zone.Name).
	RecordSets(ctx context.Context, zone string) ([]RecordSet, error)
	// SetRecordSet creates or replaces a record set. It returns when the
	// provider has applied the change.
	SetRecordSet(ctx context.Context, zone string, rs RecordSet) error
	// DeleteRecordSet removes a record set. It returns nil if it did not exist.
	DeleteRecordSet(ctx context.Context, zone, name, typ string) error
}

// Sentinel errors. Providers wrap them with %w and keep a message that can be
// shown to end users.
var (
	ErrUnauthorized = errors.New("dns: the access key is invalid or expired")
	ErrForbidden    = errors.New("dns: the access key may only read, not change records")
	ErrNotFound     = errors.New("dns: not found")
	ErrReadOnlyZone = errors.New("dns: the zone is read-only")
	ErrLocked       = errors.New("dns: the record set is protected by the provider")
	ErrRateLimited  = errors.New("dns: too many requests, try again later")
	ErrInvalid      = errors.New("dns: the provider rejected the input")
)
