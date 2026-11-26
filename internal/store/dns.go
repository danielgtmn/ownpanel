package store

import (
	"context"
	"database/sql"
	"time"
)

// DNSConnection is one user's access to a DNS provider account. Secret is
// stored as given; the panel encrypts it before.
type DNSConnection struct {
	ID            int64
	UserID        int64
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

const dnsConnCols = `c.id, c.user_id, u.name, c.provider, c.name, c.secret, c.created_at,
	c.last_checked_at, c.last_error, c.can_write, c.zone_count`

const dnsConnFrom = ` FROM dns_connections c JOIN users u ON u.id = c.user_id`

func scanDNSConnection(row interface{ Scan(...any) error }) (DNSConnection, error) {
	var c DNSConnection
	var created int64
	var checked, canWrite, zones sql.NullInt64
	err := row.Scan(&c.ID, &c.UserID, &c.Owner, &c.Provider, &c.Name, &c.Secret, &created,
		&checked, &c.LastError, &canWrite, &zones)
	if err != nil {
		if noRows(err) {
			return DNSConnection{}, notFoundf("DNS connection not found")
		}
		return DNSConnection{}, err
	}
	c.CreatedAt = unix(created)
	if checked.Valid {
		at := unix(checked.Int64)
		c.LastCheckedAt = &at
	}
	if canWrite.Valid {
		b := canWrite.Int64 == 1
		c.CanWrite = &b
	}
	if zones.Valid {
		n := int(zones.Int64)
		c.ZoneCount = &n
	}
	return c, nil
}

// CreateDNSConnection stores a connection. Names are unique per user.
// UserID, Provider, Name, Secret, LastCheckedAt and ZoneCount are taken from
// c; the rest starts empty.
func (s *Store) CreateDNSConnection(ctx context.Context, c DNSConnection) (DNSConnection, error) {
	var checked, zones sql.NullInt64
	if c.LastCheckedAt != nil {
		checked = sql.NullInt64{Int64: c.LastCheckedAt.Unix(), Valid: true}
	}
	if c.ZoneCount != nil {
		zones = sql.NullInt64{Int64: int64(*c.ZoneCount), Valid: true}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO dns_connections
		(user_id, provider, name, secret, created_at, last_checked_at, zone_count) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.UserID, c.Provider, c.Name, c.Secret, s.now(), checked, zones)
	if isUnique(err) {
		return DNSConnection{}, conflictf("you already have a DNS connection named %q", c.Name)
	}
	if err != nil {
		return DNSConnection{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return DNSConnection{}, err
	}
	return s.DNSConnection(ctx, id)
}

// DNSConnection returns one connection.
func (s *Store) DNSConnection(ctx context.Context, id int64) (DNSConnection, error) {
	return scanDNSConnection(s.db.QueryRowContext(ctx, `SELECT `+dnsConnCols+dnsConnFrom+` WHERE c.id = ?`, id))
}

// DNSConnections lists the connections of one user, or of everyone if
// userID is 0, ordered by owner name, then name.
func (s *Store) DNSConnections(ctx context.Context, userID int64) ([]DNSConnection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+dnsConnCols+dnsConnFrom+`
		WHERE ? = 0 OR c.user_id = ? ORDER BY u.name, c.name, c.id`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DNSConnection{}
	for rows.Next() {
		c, err := scanDNSConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteDNSConnection removes a connection.
func (s *Store) DeleteDNSConnection(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM dns_connections WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("DNS connection not found")
	}
	return nil
}

// RecordDNSCheck stores the outcome of listing a connection's zones:
// when, the error ("" if none) and, if known, the number of zones.
func (s *Store) RecordDNSCheck(ctx context.Context, id int64, at time.Time, zoneCount *int, lastError string) error {
	var zones sql.NullInt64
	if zoneCount != nil {
		zones = sql.NullInt64{Int64: int64(*zoneCount), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE dns_connections
		SET last_checked_at = ?, zone_count = coalesce(?, zone_count), last_error = ? WHERE id = ?`,
		at.Unix(), zones, lastError, id)
	return err
}

// RecordDNSWrite stores whether a write through the connection was allowed,
// and the error to show ("" if none).
func (s *Store) RecordDNSWrite(ctx context.Context, id int64, canWrite bool, lastError string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dns_connections SET can_write = ?, last_error = ? WHERE id = ?`,
		canWrite, lastError, id)
	return err
}

// AdoptedDNSZone is a zone someone chose to manage in the panel.
type AdoptedDNSZone struct {
	ConnectionID int64
	Zone         string
	CreatedAt    time.Time
}

// AdoptDNSZone records that a zone of a connection is managed in the panel.
// It reports whether the zone was new; adopting it again changes nothing.
func (s *Store) AdoptDNSZone(ctx context.Context, connID int64, zone string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO dns_zones (connection_id, zone, created_at) VALUES (?, ?, ?)
		ON CONFLICT (connection_id, zone) DO NOTHING`, connID, zone, s.now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReleaseDNSZone forgets that a zone is managed in the panel.
func (s *Store) ReleaseDNSZone(ctx context.Context, connID int64, zone string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM dns_zones WHERE connection_id = ? AND zone = ?`, connID, zone)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("the domain %s is not managed in the panel", zone)
	}
	return nil
}

// AdoptedDNSZones lists the adopted zones of one user's connections,
// ordered by zone and connection.
func (s *Store) AdoptedDNSZones(ctx context.Context, userID int64) ([]AdoptedDNSZone, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT z.connection_id, z.zone, z.created_at
		FROM dns_zones z JOIN dns_connections c ON c.id = z.connection_id
		WHERE c.user_id = ? ORDER BY z.zone, z.connection_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdoptedDNSZone{}
	for rows.Next() {
		var z AdoptedDNSZone
		var created int64
		if err := rows.Scan(&z.ConnectionID, &z.Zone, &created); err != nil {
			return nil, err
		}
		z.CreatedAt = unix(created)
		out = append(out, z)
	}
	return out, rows.Err()
}
