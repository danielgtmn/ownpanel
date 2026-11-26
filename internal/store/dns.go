package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/danielgtmn/ablage"
)

// DNSConnection is one user's access to a DNS provider account. Secret is
// stored as given; the panel encrypts it before.
type DNSConnection struct {
	ID            string
	UserID        string
	Owner         string // the owner's user name
	Provider      string
	Name          string
	Secret        []byte
	CreatedAt     time.Time
	LastCheckedAt *time.Time
	LastError     string
	CanWrite      *bool // nil: unknown
	ZoneCount     *int  // nil: never listed
}

var errDNSConnNotFound = notFoundf("DNS connection not found")

func dnsConnectionsOf(ctx context.Context, r repos, rows []dnsConnectionRow) ([]DNSConnection, error) {
	ids := make([]string, len(rows))
	for i, c := range rows {
		ids[i] = c.UserID
	}
	names, err := userNames(ctx, r, ids)
	if err != nil {
		return nil, err
	}
	out := make([]DNSConnection, len(rows))
	for i, c := range rows {
		out[i] = DNSConnection{ID: c.ID, UserID: c.UserID, Owner: names[c.UserID], Provider: c.Provider, Name: c.Name,
			Secret: c.Secret, CreatedAt: c.CreatedAt, LastCheckedAt: c.LastCheckedAt, LastError: c.LastError,
			CanWrite: c.CanWrite, ZoneCount: c.ZoneCount}
	}
	return out, nil
}

// CreateDNSConnection stores a connection. Names are unique per user.
// UserID, Provider, Name, Secret, LastCheckedAt and ZoneCount are taken from
// c; the rest starts empty.
func (s *Store) CreateDNSConnection(ctx context.Context, c DNSConnection) (DNSConnection, error) {
	row := dnsConnectionRow{UserID: c.UserID, Provider: c.Provider, Name: c.Name, Secret: c.Secret, CreatedAt: s.now(),
		LastCheckedAt: secondsPtr(c.LastCheckedAt), ZoneCount: c.ZoneCount}
	if row.Secret == nil {
		row.Secret = []byte{}
	}
	if err := s.r.dnsConns.Create(ctx, &row); err != nil {
		if errors.Is(err, ablage.ErrReference) {
			return DNSConnection{}, errUserNotFound
		}
		return DNSConnection{}, dup(err, conflictf("you already have a DNS connection named %q", c.Name))
	}
	return s.DNSConnection(ctx, row.ID)
}

// DNSConnection returns one connection.
func (s *Store) DNSConnection(ctx context.Context, id string) (DNSConnection, error) {
	row, err := s.r.dnsConns.Get(ctx, id)
	if err != nil {
		return DNSConnection{}, missing(err, errDNSConnNotFound)
	}
	list, err := dnsConnectionsOf(ctx, s.r, []dnsConnectionRow{row})
	if err != nil {
		return DNSConnection{}, err
	}
	return list[0], nil
}

// DNSConnections lists the connections of one user, or of everyone if
// userID is "", ordered by owner name, then name.
func (s *Store) DNSConnections(ctx context.Context, userID string) ([]DNSConnection, error) {
	where := ablage.All
	if userID != "" {
		where = ablage.Eq("user_id", userID)
	}
	rows, err := s.r.dnsConns.Find(ctx, ablage.Query{Where: where, OrderBy: []ablage.Order{ablage.Asc("name")}})
	if err != nil {
		return nil, err
	}
	out, err := dnsConnectionsOf(ctx, s.r, rows)
	if err != nil {
		return nil, err
	}
	// The owner's name lives in users: sort by it in Go (stable, so name
	// and id stay the order within one owner).
	slices.SortStableFunc(out, func(a, b DNSConnection) int { return cmp.Compare(a.Owner, b.Owner) })
	return out, nil
}

// DeleteDNSConnection removes a connection.
func (s *Store) DeleteDNSConnection(ctx context.Context, id string) error {
	return missing(s.r.dnsConns.Delete(ctx, id), errDNSConnNotFound)
}

// RecordDNSCheck stores the outcome of listing a connection's zones:
// when, the error ("" if none) and, if known, the number of zones.
func (s *Store) RecordDNSCheck(ctx context.Context, id string, at time.Time, zoneCount *int, lastError string) error {
	set := ablage.Row{"last_checked_at": seconds(at), "last_error": lastError}
	if zoneCount != nil {
		set["zone_count"] = *zoneCount
	}
	_, err := s.r.dnsConns.UpdateWhere(ctx, ablage.Eq("id", id), set)
	return err
}

// RecordDNSWrite stores whether a write through the connection was allowed,
// and the error to show ("" if none).
func (s *Store) RecordDNSWrite(ctx context.Context, id string, canWrite bool, lastError string) error {
	_, err := s.r.dnsConns.UpdateWhere(ctx, ablage.Eq("id", id), ablage.Row{"can_write": canWrite, "last_error": lastError})
	return err
}

// AdoptedDNSZone is a zone someone chose to manage in the panel.
type AdoptedDNSZone struct {
	ConnectionID string
	Zone         string
	CreatedAt    time.Time
}

// AdoptDNSZone records that a zone of a connection is managed in the panel.
// It reports whether the zone was new; adopting it again changes nothing.
func (s *Store) AdoptDNSZone(ctx context.Context, connID, zone string) (bool, error) {
	err := s.r.dnsZones.Create(ctx, &dnsZoneRow{ConnectionID: connID, Zone: zone, CreatedAt: s.now()})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ablage.ErrDuplicate):
		return false, nil
	case errors.Is(err, ablage.ErrReference):
		return false, errDNSConnNotFound
	}
	return false, err
}

// ReleaseDNSZone forgets that a zone is managed in the panel.
func (s *Store) ReleaseDNSZone(ctx context.Context, connID, zone string) error {
	n, err := s.r.dnsZones.DeleteWhere(ctx, ablage.And(ablage.Eq("connection_id", connID), ablage.Eq("zone", zone)))
	if err != nil {
		return err
	}
	if n == 0 {
		return notFoundf("the domain %s is not managed in the panel", zone)
	}
	return nil
}

// AdoptedDNSZones lists the adopted zones of one user's connections,
// ordered by zone and connection.
func (s *Store) AdoptedDNSZones(ctx context.Context, userID string) ([]AdoptedDNSZone, error) {
	conns, err := s.r.dnsConns.Find(ctx, ablage.Query{Where: ablage.Eq("user_id", userID)})
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(conns))
	for i, c := range conns {
		ids[i] = c.ID
	}
	rows, err := s.r.dnsZones.Find(ctx, ablage.Query{
		Where:   ablage.In("connection_id", ids...),
		OrderBy: []ablage.Order{ablage.Asc("zone"), ablage.Asc("connection_id")},
	})
	if err != nil {
		return nil, err
	}
	out := make([]AdoptedDNSZone, len(rows))
	for i, z := range rows {
		out[i] = AdoptedDNSZone{ConnectionID: z.ConnectionID, Zone: z.Zone, CreatedAt: z.CreatedAt}
	}
	return out, nil
}
