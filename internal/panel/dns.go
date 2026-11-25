package panel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// DNS connections: a person connects a DNS provider account (for Hetzner:
// one Cloud project) with an API token. The token is stored encrypted and
// only ever used for its owner's own requests: admins see every connection
// and may remove it, but cannot read or change records through someone
// else's.

// DNSProviders are the providers a connection can be made to.
var DNSProviders = []string{"hetzner"}

const (
	// secretPurpose binds sealed tokens to their use.
	secretPurpose = "ownpanel dns connection token"
	// zoneCacheTTL keeps zone lists well below Hetzner's 3600 requests/hour.
	zoneCacheTTL = 60 * time.Second
	// checkCacheTTL is how long a domain lookup is reused.
	checkCacheTTL = 30 * time.Second
	// lookupTimeout bounds one domain lookup.
	lookupTimeout = 8 * time.Second
	// pointTTL is the TTL of records written by PointDomain.
	pointTTL = 300
)

// DefaultDNSProvider returns a client for the real provider API.
func DefaultDNSProvider(provider, token string) (dns.Provider, error) {
	switch provider {
	case "hetzner":
		return hetzner.New(token), nil
	}
	return nil, fmt.Errorf("unknown DNS provider %q", provider)
}

func providerLabel(provider string) string {
	if provider == "hetzner" {
		return "Hetzner"
	}
	return provider
}

// ---- caches ----

type zonesEntry struct {
	zones []dns.Zone
	at    time.Time
}

type checkEntry struct {
	check dns.Check
	at    time.Time
}

type dnsCache struct {
	mu     sync.Mutex
	zones  map[int64]zonesEntry
	checks map[string]checkEntry
}

func newDNSCache() *dnsCache {
	return &dnsCache{zones: map[int64]zonesEntry{}, checks: map[string]checkEntry{}}
}

func (c *dnsCache) getZones(id int64, now time.Time) ([]dns.Zone, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.zones[id]
	if !ok || now.Sub(e.at) >= zoneCacheTTL || now.Before(e.at) {
		return nil, false
	}
	return slices.Clone(e.zones), true
}

func (c *dnsCache) putZones(id int64, zones []dns.Zone, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.zones[id] = zonesEntry{zones: slices.Clone(zones), at: now}
}

func (c *dnsCache) dropZones(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.zones, id)
}

func (c *dnsCache) getCheck(domain string, now time.Time) (dns.Check, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.checks[domain]
	if !ok || now.Sub(e.at) >= checkCacheTTL || now.Before(e.at) {
		return dns.Check{}, false
	}
	return e.check, true
}

func (c *dnsCache) putCheck(domain string, check dns.Check, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks[domain] = checkEntry{check: check, at: now}
}

func (c *dnsCache) dropCheck(domain string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.checks, domain)
}

// dropAllChecks forgets every lookup, e.g. when the server's addresses change.
func (c *dnsCache) dropAllChecks() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.checks)
}

// ---- views ----

// DNSConnection is what callers see of a connection; never its token.
type DNSConnection struct {
	ID            int64      `json:"id"`
	Provider      string     `json:"provider"`
	Name          string     `json:"name"`
	Owner         string     `json:"owner"`
	CreatedAt     time.Time  `json:"created_at"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	LastError     string     `json:"last_error"`
	CanWrite      *bool      `json:"can_write"`
	ZoneCount     *int       `json:"zone_count"`
}

func connectionView(c store.DNSConnection) DNSConnection {
	return DNSConnection{
		ID: c.ID, Provider: c.Provider, Name: c.Name, Owner: c.Owner, CreatedAt: c.CreatedAt,
		LastCheckedAt: c.LastCheckedAt, LastError: c.LastError, CanWrite: c.CanWrite, ZoneCount: c.ZoneCount,
	}
}

// DNSZone is a zone reachable through one of the actor's connections.
type DNSZone struct {
	ConnectionID   int64  `json:"connection_id"`
	ConnectionName string `json:"connection_name"`
	Name           string `json:"name"`
	ReadOnly       bool   `json:"read_only"`
	// Nameservers are the provider's nameservers for the zone, to be set
	// at the domain's registrar.
	Nameservers []string `json:"nameservers"`
	// Adopted: the person chose to manage the zone in the panel.
	Adopted bool `json:"adopted"`
	// UsedBy describes what uses a domain in the zone (sites, redirects,
	// e-mail, the panel itself); such a zone is in the panel without being
	// adopted, and stays while it is used.
	UsedBy []string `json:"used_by"`
}

func zoneView(c store.DNSConnection, z dns.Zone) DNSZone {
	ns := slices.Clone(z.Nameservers)
	if ns == nil {
		ns = []string{}
	}
	return DNSZone{ConnectionID: c.ID, ConnectionName: c.Name, Name: z.Name, ReadOnly: z.ReadOnly, Nameservers: ns, UsedBy: []string{}}
}

// DNSProblem says why the zones of a connection could not be listed.
type DNSProblem struct {
	ConnectionID   int64  `json:"connection_id"`
	ConnectionName string `json:"connection_name"`
	Error          string `json:"error"`
}

// DNSZones is the result of listing zones across connections.
type DNSZones struct {
	Zones    []DNSZone    `json:"zones"`
	Problems []DNSProblem `json:"problems"`
}

// RecordSet is all records of one name and type. TTL nil is the zone's
// default; TXT values are plain text, without quotes.
type RecordSet struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	TTL    *int     `json:"ttl"`
	Values []string `json:"values"`
	Locked bool     `json:"locked"`
}

func recordSetView(rs dns.RecordSet) RecordSet {
	v := RecordSet{Name: rs.Name, Type: rs.Type, Values: slices.Clone(rs.Values), Locked: rs.Locked}
	if v.Values == nil {
		v.Values = []string{}
	}
	if rs.TTL > 0 {
		ttl := rs.TTL
		v.TTL = &ttl
	}
	return v
}

// Domain states of DomainStatus.
const (
	DomainPointsHere      = "points_here"
	DomainPointsElsewhere = "points_elsewhere"
	DomainNoRecord        = "no_record"
	DomainLookupFailed    = "lookup_failed"
)

// DomainZone is where PointDomain would write for a domain.
type DomainZone struct {
	ConnectionID   int64  `json:"connection_id"`
	ConnectionName string `json:"connection_name"`
	Zone           string `json:"zone"`
	RecordName     string `json:"record_name"`
}

// DomainStatus tells where a domain of a site points.
type DomainStatus struct {
	Domain string      `json:"domain"`
	State  string      `json:"state"`
	Found  []string    `json:"found"`
	Zone   *DomainZone `json:"zone"`
}

// PointResult lists the record sets PointDomain changed; a removed record
// set has no values.
type PointResult struct {
	Changed []RecordSet `json:"changed"`
}

// ---- permissions ----

// needDNS: reading zones and records, and using one's own connections.
// Tokens limited to sites only get the per-site operations.
func needDNS(a Actor) error {
	if err := needAuth(a); err != nil {
		return err
	}
	if a.Via == ViaSystem || a.UserID == 0 {
		return errorf(Forbidden, "DNS can only be used by a person")
	}
	if a.Sites != nil {
		return errorf(Forbidden, "this access token is limited to certain sites and cannot manage DNS")
	}
	return nil
}

// needDNSManage: adding, checking and removing connections.
func needDNSManage(a Actor) error {
	if err := needDNS(a); err != nil {
		return err
	}
	if a.Via != ViaSession && a.ReadOnly {
		return errorf(Forbidden, "managing DNS connections needs a password sign-in or an access token with write access to all sites")
	}
	return nil
}

// ---- connections ----

// DNSConnections lists the actor's connections; admins see everyone's.
func (s *Service) DNSConnections(ctx context.Context, a Actor) ([]DNSConnection, error) {
	if err := needDNS(a); err != nil {
		return nil, err
	}
	owner := a.UserID
	if a.Admin {
		owner = 0
	}
	list, err := s.store.DNSConnections(ctx, owner)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]DNSConnection, 0, len(list))
	for _, c := range list {
		out = append(out, connectionView(c))
	}
	return out, nil
}

// ownConnections are the connections the actor may use (none for actors
// that are not a person).
func (s *Service) ownConnections(ctx context.Context, a Actor) ([]store.DNSConnection, error) {
	if a.UserID == 0 {
		return nil, nil
	}
	list, err := s.store.DNSConnections(ctx, a.UserID)
	if err != nil {
		return nil, storeError(err)
	}
	return list, nil
}

// usableConnection returns one of the actor's own connections.
func (s *Service) usableConnection(ctx context.Context, a Actor, id int64) (store.DNSConnection, error) {
	c, err := s.store.DNSConnection(ctx, id)
	if err != nil {
		return store.DNSConnection{}, storeError(err)
	}
	if c.UserID != a.UserID {
		if a.Admin && a.Sites == nil {
			return store.DNSConnection{}, errorf(Forbidden, "a DNS connection can only be used by the person who added it; this one belongs to %s", c.Owner)
		}
		return store.DNSConnection{}, errorf(NotFound, "DNS connection not found")
	}
	return c, nil
}

func validToken(token string) error {
	if token == "" {
		return errorf(Invalid, "enter the API token")
	}
	if len(token) > 512 || !utf8.ValidString(token) || strings.ContainsFunc(token, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return errorf(Invalid, "the token contains spaces, line breaks or is far too long; copy it again")
	}
	return nil
}

// CreateDNSConnection connects a DNS provider account with an API token. The
// token is tried first (by listing the zones) and stored only if it works.
func (s *Service) CreateDNSConnection(ctx context.Context, a Actor, provider, name, token string) (DNSConnection, error) {
	if err := needDNSManage(a); err != nil {
		return DNSConnection{}, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "hetzner"
	}
	if !slices.Contains(DNSProviders, provider) {
		return DNSConnection{}, errorf(Invalid, "unknown DNS provider %q; supported: %s", provider, strings.Join(DNSProviders, ", "))
	}
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > 64 || strings.ContainsFunc(name, unicode.IsControl) {
		return DNSConnection{}, errorf(Invalid, "the connection name must be 1 to 64 characters long")
	}
	token = strings.TrimSpace(token)
	if err := validToken(token); err != nil {
		return DNSConnection{}, err
	}
	if s.secrets == nil {
		return DNSConnection{}, errorf(Internal, "no secret key configured; DNS connections cannot be stored")
	}
	existing, err := s.ownConnections(ctx, a)
	if err != nil {
		return DNSConnection{}, err
	}
	if slices.ContainsFunc(existing, func(c store.DNSConnection) bool { return c.Name == name }) {
		return DNSConnection{}, errorf(Conflict, "you already have a DNS connection named %q", name)
	}

	p, err := s.newProvider(provider, token)
	if err != nil {
		return DNSConnection{}, &Error{Kind: Invalid, Msg: scrub(err.Error(), token)}
	}
	zones, err := p.Zones(ctx)
	if errors.Is(err, dns.ErrUnauthorized) {
		return DNSConnection{}, errorf(Invalid, "The token was rejected by %s. Check that you copied the whole token and that it has not been deleted.", providerLabel(provider))
	}
	if err != nil {
		return DNSConnection{}, dnsError(provider, name, token, err)
	}
	now := s.now()
	count := len(zones)
	c, err := s.store.CreateDNSConnection(ctx, store.DNSConnection{
		UserID: a.UserID, Provider: provider, Name: name,
		Secret:        s.secrets.Seal([]byte(token), secretPurpose),
		LastCheckedAt: &now, ZoneCount: &count,
	})
	if err != nil {
		return DNSConnection{}, storeError(err)
	}
	s.dnsCache.putZones(c.ID, zones, now)
	s.record(ctx, a, "dns.connect", name, fmt.Sprintf("%s, %d zones", providerLabel(provider), count))
	return connectionView(c), nil
}

// CheckDNSConnection lists the connection's zones afresh and returns the
// connection with the outcome (LastError is empty if the token works).
func (s *Service) CheckDNSConnection(ctx context.Context, a Actor, id int64) (DNSConnection, error) {
	if err := needDNSManage(a); err != nil {
		return DNSConnection{}, err
	}
	c, err := s.usableConnection(ctx, a, id)
	if err != nil {
		return DNSConnection{}, err
	}
	s.dnsCache.dropZones(c.ID)
	if _, err := s.zonesOf(ctx, c); err != nil && KindOf(err) == Internal {
		return DNSConnection{}, err
	}
	c, err = s.store.DNSConnection(ctx, id)
	if err != nil {
		return DNSConnection{}, storeError(err)
	}
	return connectionView(c), nil
}

// DeleteDNSConnection removes a connection and forgets its token. Admins may
// remove anyone's.
func (s *Service) DeleteDNSConnection(ctx context.Context, a Actor, id int64) error {
	if err := needAuth(a); err != nil {
		return err
	}
	c, err := s.store.DNSConnection(ctx, id)
	if err != nil {
		return storeError(err)
	}
	detail := providerLabel(c.Provider)
	switch {
	case c.UserID == a.UserID:
		if err := needDNSManage(a); err != nil {
			return err
		}
	case a.Admin && a.Sites == nil:
		if err := needAdmin(a); err != nil {
			return err
		}
		detail += ", owner " + c.Owner
	default:
		return errorf(NotFound, "DNS connection not found")
	}
	if err := s.store.DeleteDNSConnection(ctx, id); err != nil {
		return storeError(err)
	}
	s.dnsCache.dropZones(id)
	s.record(ctx, a, "dns.disconnect", c.Name, detail)
	return nil
}

// ---- provider access ----

func scrub(msg, token string) string {
	if token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, token, "***")
}

// dnsError turns a provider error into a message for people. It never
// contains the token.
func dnsError(provider, connName, token string, err error) error {
	label := providerLabel(provider)
	e := func(kind Kind, format string, args ...any) error {
		return &Error{Kind: kind, Msg: scrub(fmt.Sprintf(format, args...), token), Err: err}
	}
	switch {
	case errors.Is(err, dns.ErrUnauthorized):
		return e(Invalid, "the token of the DNS connection %q was rejected by %s; it may have been deleted. Remove the connection and add it again with a new token", connName, label)
	case errors.Is(err, dns.ErrForbidden):
		return e(Conflict, "the token of the DNS connection %q may only read; create a token with Read & Write access in the %s project and connect it again", connName, label)
	case errors.Is(err, dns.ErrNotFound):
		return e(NotFound, "not found at %s", label)
	case errors.Is(err, dns.ErrReadOnlyZone):
		return e(Conflict, "this zone is not a primary zone at %s, so its records cannot be read or changed here", label)
	case errors.Is(err, dns.ErrLocked):
		return e(Conflict, "the records are protected at %s; remove the protection there first", label)
	case errors.Is(err, dns.ErrExists):
		return e(Conflict, "it already exists at %s, possibly in another project or account", label)
	case errors.Is(err, dns.ErrRateLimited):
		return e(Throttled, "%s's request limit is reached; try again in a few minutes", label)
	case errors.Is(err, dns.ErrInvalid):
		detail := strings.TrimPrefix(err.Error(), dns.ErrInvalid.Error()+": ")
		return e(Invalid, "%s refused the change: %s", label, detail)
	case errors.Is(err, context.Canceled):
		return newError(Internal, err)
	case errors.Is(err, context.DeadlineExceeded):
		return e(Unavailable, "%s did not answer in time; try again later", label)
	}
	return e(Unavailable, "%s could not be reached or reported a problem; try again later (%s)", label, err.Error())
}

// provider opens a client for the connection with its decrypted token.
func (s *Service) provider(c store.DNSConnection) (dns.Provider, string, error) {
	if s.secrets == nil {
		return nil, "", errorf(Internal, "no secret key configured; DNS connections cannot be used")
	}
	token, err := s.secrets.Open(c.Secret, secretPurpose)
	if err != nil {
		return nil, "", errorf(Conflict, "the token of the DNS connection %q cannot be read because the server's secret key has changed; remove the connection and add it again", c.Name)
	}
	p, err := s.newProvider(c.Provider, string(token))
	if err != nil {
		return nil, "", &Error{Kind: Internal, Msg: scrub(err.Error(), string(token))}
	}
	return p, string(token), nil
}

// zonesOf returns the zones of a connection, from the cache if it is fresh.
// Listing them at the provider also records the outcome on the connection.
func (s *Service) zonesOf(ctx context.Context, c store.DNSConnection) ([]dns.Zone, error) {
	if zones, ok := s.dnsCache.getZones(c.ID, s.now()); ok {
		return zones, nil
	}
	p, token, err := s.provider(c)
	if err != nil {
		if KindOf(err) != Internal {
			s.noteCheck(ctx, c, nil, err.Error())
		}
		return nil, err
	}
	zones, err := p.Zones(ctx)
	if err != nil {
		derr := dnsError(c.Provider, c.Name, token, err)
		if !errors.Is(err, context.Canceled) {
			s.noteCheck(ctx, c, nil, derr.Error())
		}
		return nil, derr
	}
	s.dnsCache.putZones(c.ID, zones, s.now())
	n := len(zones)
	s.noteCheck(ctx, c, &n, "")
	return zones, nil
}

// noteCheck stores the outcome of listing zones. A refused write is not
// undone by a successful listing, so its message stays.
func (s *Service) noteCheck(ctx context.Context, c store.DNSConnection, zoneCount *int, msg string) {
	if msg == "" && c.CanWrite != nil && !*c.CanWrite {
		msg = c.LastError
	}
	if err := s.store.RecordDNSCheck(context.WithoutCancel(ctx), c.ID, s.now(), zoneCount, msg); err != nil {
		slog.Error("could not record a DNS check", "connection", c.ID, "err", err)
	}
}

// noteWrite records whether a write through the connection was allowed.
func (s *Service) noteWrite(ctx context.Context, c store.DNSConnection, err error, derr error) {
	var canWrite bool
	var msg string
	switch {
	case err == nil:
		if c.CanWrite != nil && *c.CanWrite && c.LastError == "" {
			return
		}
		canWrite = true
	case errors.Is(err, dns.ErrForbidden):
		msg = derr.Error()
	default:
		return
	}
	if err := s.store.RecordDNSWrite(context.WithoutCancel(ctx), c.ID, canWrite, msg); err != nil {
		slog.Error("could not record a DNS write", "connection", c.ID, "err", err)
	}
}

func normalizeZoneName(zone string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone), "."))
}

// zoneIn returns the zone named zone of the connection.
func (s *Service) zoneIn(ctx context.Context, c store.DNSConnection, zone string) (dns.Zone, error) {
	zones, err := s.zonesOf(ctx, c)
	if err != nil {
		return dns.Zone{}, err
	}
	name := normalizeZoneName(zone)
	for _, z := range zones {
		if z.Name == name {
			return z, nil
		}
	}
	return dns.Zone{}, errorf(NotFound, "the zone %s is not part of the DNS connection %q", name, c.Name)
}

// ---- zones and records ----

// DNSZones lists the zones that are in the panel: of the actor's own
// connections, those the actor adopted and those something uses (a site, a
// redirect, e-mail or the panel itself). A connection that fails is reported
// in Problems; the others are still listed.
func (s *Service) DNSZones(ctx context.Context, a Actor) (DNSZones, error) {
	return s.listZones(ctx, a, true)
}

// AvailableDNSZones lists the zones of the actor's own connections that are
// not in the panel yet, to choose from when adding a domain.
func (s *Service) AvailableDNSZones(ctx context.Context, a Actor) (DNSZones, error) {
	return s.listZones(ctx, a, false)
}

// listZones lists the zones of the actor's own connections that are in the
// panel (inPanel) or the others.
func (s *Service) listZones(ctx context.Context, a Actor, inPanel bool) (DNSZones, error) {
	if err := needDNS(a); err != nil {
		return DNSZones{}, err
	}
	conns, err := s.ownConnections(ctx, a)
	if err != nil {
		return DNSZones{}, err
	}
	out := DNSZones{Zones: []DNSZone{}, Problems: []DNSProblem{}}
	if len(conns) == 0 {
		return out, nil
	}
	adopted, err := s.adoptedZones(ctx, a)
	if err != nil {
		return DNSZones{}, err
	}
	uses, err := s.domainUses(ctx, a)
	if err != nil {
		return DNSZones{}, err
	}
	for _, c := range conns {
		zones, err := s.zonesOf(ctx, c)
		if err != nil {
			if ctx.Err() != nil {
				return DNSZones{}, newError(Internal, ctx.Err())
			}
			msg := err.Error()
			if KindOf(err) == Internal {
				msg = "something went wrong on the server"
				slog.Error("listing DNS zones failed", "connection", c.ID, "err", err)
			}
			out.Problems = append(out.Problems, DNSProblem{ConnectionID: c.ID, ConnectionName: c.Name, Error: msg})
			continue
		}
		for _, z := range zones {
			v := zoneView(c, z)
			v.Adopted = adopted[zoneKey{c.ID, z.Name}]
			v.UsedBy = usersOf(uses, z.Name)
			if (v.Adopted || len(v.UsedBy) > 0) == inPanel {
				out.Zones = append(out.Zones, v)
			}
		}
	}
	slices.SortStableFunc(out.Zones, func(x, y DNSZone) int {
		if c := strings.Compare(x.Name, y.Name); c != 0 {
			return c
		}
		return strings.Compare(x.ConnectionName, y.ConnectionName)
	})
	return out, nil
}

// DNSRecords lists the record sets of a zone of one of the actor's own
// connections, "@" first, then by name and type.
func (s *Service) DNSRecords(ctx context.Context, a Actor, connID int64, zone string) ([]RecordSet, error) {
	if err := needDNS(a); err != nil {
		return nil, err
	}
	c, err := s.usableConnection(ctx, a, connID)
	if err != nil {
		return nil, err
	}
	z, err := s.zoneIn(ctx, c, zone)
	if err != nil {
		return nil, err
	}
	p, token, err := s.provider(c)
	if err != nil {
		return nil, err
	}
	sets, err := p.RecordSets(ctx, z.Name)
	if err != nil {
		return nil, dnsError(c.Provider, c.Name, token, err)
	}
	out := make([]RecordSet, 0, len(sets))
	for _, rs := range sets {
		out = append(out, recordSetView(rs))
	}
	slices.SortStableFunc(out, func(x, y RecordSet) int {
		if (x.Name == "@") != (y.Name == "@") {
			if x.Name == "@" {
				return -1
			}
			return 1
		}
		if c := strings.Compare(x.Name, y.Name); c != 0 {
			return c
		}
		return strings.Compare(x.Type, y.Type)
	})
	return out, nil
}

// writableZone returns the zone for a change through the connection.
func (s *Service) writableZone(ctx context.Context, a Actor, connID int64, zone string) (store.DNSConnection, dns.Zone, error) {
	if err := needDNS(a); err != nil {
		return store.DNSConnection{}, dns.Zone{}, err
	}
	if err := needWrite(a); err != nil {
		return store.DNSConnection{}, dns.Zone{}, err
	}
	c, err := s.usableConnection(ctx, a, connID)
	if err != nil {
		return store.DNSConnection{}, dns.Zone{}, err
	}
	z, err := s.zoneIn(ctx, c, zone)
	if err != nil {
		return store.DNSConnection{}, dns.Zone{}, err
	}
	if z.ReadOnly {
		return store.DNSConnection{}, dns.Zone{}, errorf(Conflict, "the zone %s is not a primary zone at %s, so its records cannot be changed here", z.Name, providerLabel(c.Provider))
	}
	return c, z, nil
}

func fqdn(name, zone string) string {
	if name == "@" {
		return zone
	}
	return name + "." + zone
}

// SetDNSRecord creates or replaces a record set. ttl nil means the zone's
// default. It returns the record set as written.
func (s *Service) SetDNSRecord(ctx context.Context, a Actor, connID int64, zone, name, typ string, ttl *int, values []string) (RecordSet, error) {
	c, z, err := s.writableZone(ctx, a, connID, zone)
	if err != nil {
		return RecordSet{}, err
	}
	rs, err := validateRecordSet(z.Name, name, typ, ttl, values)
	if err != nil {
		return RecordSet{}, err
	}
	p, token, err := s.provider(c)
	if err != nil {
		return RecordSet{}, err
	}
	err = p.SetRecordSet(ctx, z.Name, rs)
	s.dnsCache.dropZones(c.ID)
	if err != nil {
		derr := dnsError(c.Provider, c.Name, token, err)
		s.noteWrite(ctx, c, err, derr)
		return RecordSet{}, derr
	}
	s.noteWrite(ctx, c, nil, nil)
	s.dnsCache.dropCheck(fqdn(rs.Name, z.Name))
	s.record(ctx, a, "dns.record.set", z.Name, recordDetail(rs))
	return recordSetView(rs), nil
}

// DeleteDNSRecord removes a record set; removing one that does not exist is
// fine. SOA and the zone's own NS records cannot be removed.
func (s *Service) DeleteDNSRecord(ctx context.Context, a Actor, connID int64, zone, name, typ string) error {
	c, z, err := s.writableZone(ctx, a, connID, zone)
	if err != nil {
		return err
	}
	n, err := recordName(z.Name, name)
	if err != nil {
		return err
	}
	t, err := recordType(n, typ)
	if err != nil {
		return err
	}
	p, token, err := s.provider(c)
	if err != nil {
		return err
	}
	err = p.DeleteRecordSet(ctx, z.Name, n, t)
	s.dnsCache.dropZones(c.ID)
	if err != nil {
		derr := dnsError(c.Provider, c.Name, token, err)
		s.noteWrite(ctx, c, err, derr)
		return derr
	}
	s.noteWrite(ctx, c, nil, nil)
	s.dnsCache.dropCheck(fqdn(n, z.Name))
	s.record(ctx, a, "dns.record.delete", z.Name, n+" "+t)
	return nil
}

// ---- per-domain status and "point here" ----

type zoneMatch struct {
	conn store.DNSConnection
	zone dns.Zone
	name string // record name relative to the zone
}

// zoneMatches finds the zones that hold domain across the actor's own
// connections: all with the longest matching zone name (more than one means
// the same zone is reachable through several connections). Connections that
// fail are returned as problems.
func (s *Service) zoneMatches(ctx context.Context, conns []store.DNSConnection, domain string) ([]zoneMatch, []error) {
	var best []zoneMatch
	var problems []error
	for _, c := range conns {
		zones, err := s.zonesOf(ctx, c)
		if err != nil {
			problems = append(problems, fmt.Errorf("DNS connection %q: %w", c.Name, err))
			continue
		}
		z, name, ok := dns.FindZone(zones, domain)
		if !ok {
			continue
		}
		switch {
		case len(best) == 0 || len(z.Name) > len(best[0].zone.Name):
			best = []zoneMatch{{conn: c, zone: z, name: name}}
		case len(z.Name) == len(best[0].zone.Name):
			best = append(best, zoneMatch{conn: c, zone: z, name: name})
		}
	}
	return best, problems
}

// checkDomain looks up where domain points, reusing results for 30 s.
func (s *Service) checkDomain(ctx context.Context, domain string) dns.Check {
	if c, ok := s.dnsCache.getCheck(domain, s.now()); ok {
		return c
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	c := dns.CheckDomain(lctx, s.resolver, domain, s.effectiveServerIPs())
	if ctx.Err() == nil {
		s.dnsCache.putCheck(domain, c, s.now())
	}
	return c
}

func stateName(st dns.State) string {
	switch st {
	case dns.PointsHere:
		return DomainPointsHere
	case dns.PointsElsewhere:
		return DomainPointsElsewhere
	case dns.NoRecord:
		return DomainNoRecord
	}
	return DomainLookupFailed
}

// DomainStatuses tells for each domain of a site where it points, and in
// which of the actor's zones PointDomain would change it.
func (s *Service) DomainStatuses(ctx context.Context, a Actor, siteName string) ([]DomainStatus, error) {
	site, err := s.visibleSite(ctx, a, siteName)
	if err != nil {
		return nil, err
	}
	if err := s.needServerIPs(); err != nil {
		return nil, err
	}
	return s.domainStatuses(ctx, a, site.Domains)
}

// controlledDomain normalizes domain and checks that it is a domain the
// actor controls: a domain of a site it may see, or the domain of a domain
// redirect it may see.
func (s *Service) controlledDomain(ctx context.Context, a Actor, domain string) (string, error) {
	if err := needAuth(a); err != nil {
		return "", err
	}
	d, err := store.NormalizeDomain(domain)
	if err != nil {
		return "", errorf(NotFound, "you have no site or redirect with the domain %q", strings.TrimSpace(domain))
	}
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return "", storeError(err)
	}
	for _, site := range sites {
		if slices.Contains(site.Domains, d) && a.canSee(site) {
			return d, nil
		}
	}
	if ok, err := s.visibleDomainRedirect(ctx, a, d); err != nil || ok {
		return d, err
	}
	return "", errorf(NotFound, "you have no site or redirect with the domain %q", d)
}

// DomainStatusOf tells where one domain the actor controls (of a site or a
// domain redirect) points, like one element of DomainStatuses.
func (s *Service) DomainStatusOf(ctx context.Context, a Actor, domain string) (DomainStatus, error) {
	d, err := s.controlledDomain(ctx, a, domain)
	if err != nil {
		return DomainStatus{}, err
	}
	if err := s.needServerIPs(); err != nil {
		return DomainStatus{}, err
	}
	out, err := s.domainStatuses(ctx, a, []string{d})
	if err != nil {
		return DomainStatus{}, err
	}
	return out[0], nil
}

// PointDomainOf is PointDomain for any domain the actor controls (of a site
// or a domain redirect).
func (s *Service) PointDomainOf(ctx context.Context, a Actor, domain string) (PointResult, error) {
	d, err := s.controlledDomain(ctx, a, domain)
	if err != nil {
		return PointResult{}, err
	}
	if err := needWrite(a); err != nil {
		return PointResult{}, err
	}
	if err := s.needServerIPs(); err != nil {
		return PointResult{}, err
	}
	return s.pointDomain(ctx, a, d)
}

func (s *Service) domainStatuses(ctx context.Context, a Actor, domains []string) ([]DomainStatus, error) {
	out := make([]DomainStatus, len(domains))
	var wg sync.WaitGroup
	for i, d := range domains {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := s.checkDomain(ctx, d)
			found := c.Found
			if found == nil {
				found = []string{}
			}
			out[i] = DomainStatus{Domain: d, State: stateName(c.State), Found: found}
		}()
	}
	conns, err := s.ownConnections(ctx, a)
	if err != nil {
		wg.Wait()
		return nil, err
	}
	matches := make([][]zoneMatch, len(domains))
	for i, d := range domains {
		matches[i], _ = s.zoneMatches(ctx, conns, d)
	}
	wg.Wait()
	for i, m := range matches {
		if len(m) == 1 {
			out[i].Zone = &DomainZone{ConnectionID: m[0].conn.ID, ConnectionName: m[0].conn.Name, Zone: m[0].zone.Name, RecordName: m[0].name}
		}
	}
	return out, nil
}

func sameAddresses(a, b []string) bool {
	canon := func(in []string) []string {
		out := []string{}
		for _, v := range in {
			if ip, err := netip.ParseAddr(strings.TrimSpace(v)); err == nil {
				v = ip.Unmap().String()
			}
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(canon(a), canon(b))
}

// PointDomain makes a domain of a site point at this server: it writes A
// and AAAA records with the server's addresses into the zone that holds the
// domain, found among the actor's own connections. An address record set of
// a family the server has no address in is removed, since it would point
// elsewhere. A CNAME at the name is never removed; the request is refused.
// It returns what was changed, nothing if all was correct already.
func (s *Service) PointDomain(ctx context.Context, a Actor, siteName, domain string) (PointResult, error) {
	site, err := s.visibleSite(ctx, a, siteName)
	if err != nil {
		return PointResult{}, err
	}
	if err := needWrite(a); err != nil {
		return PointResult{}, err
	}
	d, err := store.NormalizeDomain(domain)
	if err != nil || !slices.Contains(site.Domains, d) {
		return PointResult{}, errorf(NotFound, "the site %s has no domain %q", site.Name, domain)
	}
	if err := s.needServerIPs(); err != nil {
		return PointResult{}, err
	}
	return s.pointDomain(ctx, a, d)
}

// pointDomain does PointDomain for a domain whose permissions are checked.
func (s *Service) pointDomain(ctx context.Context, a Actor, d string) (PointResult, error) {
	conns, err := s.ownConnections(ctx, a)
	if err != nil {
		return PointResult{}, err
	}
	matches, problems := s.zoneMatches(ctx, conns, d)
	switch {
	case len(matches) == 0 && len(problems) > 0:
		return PointResult{}, &Error{Kind: KindOf(problems[0]), Msg: problems[0].Error(), Err: problems[0]}
	case len(matches) == 0:
		return PointResult{}, errorf(Conflict, "none of your DNS connections has a zone for %s; connect the DNS provider account that holds the domain first", d)
	case len(matches) > 1:
		var names []string
		for _, m := range matches {
			names = append(names, fmt.Sprintf("%q", m.conn.Name))
		}
		return PointResult{}, errorf(Conflict, "the zone %s is in more than one of your DNS connections (%s); remove the duplicate connection so it is clear which one to change",
			matches[0].zone.Name, strings.Join(names, " and "))
	}
	m := matches[0]
	if m.zone.ReadOnly {
		return PointResult{}, errorf(Conflict, "the zone %s is not a primary zone at %s, so its records cannot be changed here", m.zone.Name, providerLabel(m.conn.Provider))
	}
	p, token, err := s.provider(m.conn)
	if err != nil {
		return PointResult{}, err
	}
	sets, err := p.RecordSets(ctx, m.zone.Name)
	if err != nil {
		return PointResult{}, dnsError(m.conn.Provider, m.conn.Name, token, err)
	}
	existing := map[string]dns.RecordSet{}
	for _, rs := range sets {
		if strings.EqualFold(rs.Name, m.name) {
			existing[strings.ToUpper(rs.Type)] = rs
		}
	}
	if _, ok := existing["CNAME"]; ok {
		return PointResult{}, errorf(Conflict, "%s has a CNAME record; remove it first", d)
	}

	desired := dns.AddressRecords(m.name, s.effectiveServerIPs(), pointTTL)
	changed := []RecordSet{}
	var types []string
	finish := func(err error) (PointResult, error) {
		// The cached status is out of date whatever happened: re-check next time.
		s.dnsCache.dropCheck(d)
		if len(changed) > 0 {
			s.dnsCache.dropZones(m.conn.ID)
			s.noteWrite(ctx, m.conn, nil, nil)
			s.record(ctx, a, "dns.point", d, fmt.Sprintf("%s %s in zone %s (connection %q)", m.name, strings.Join(types, ", "), m.zone.Name, m.conn.Name))
		}
		if err != nil {
			derr := dnsError(m.conn.Provider, m.conn.Name, token, err)
			s.noteWrite(ctx, m.conn, err, derr)
			return PointResult{}, derr
		}
		return PointResult{Changed: changed}, nil
	}
	for _, typ := range []string{"A", "AAAA"} {
		i := slices.IndexFunc(desired, func(rs dns.RecordSet) bool { return rs.Type == typ })
		ex, has := existing[typ]
		switch {
		case i >= 0 && has && sameAddresses(ex.Values, desired[i].Values):
			// Already right; its TTL is left alone.
		case i >= 0:
			if err := p.SetRecordSet(ctx, m.zone.Name, desired[i]); err != nil {
				return finish(err)
			}
			changed = append(changed, recordSetView(desired[i]))
			types = append(types, typ)
		case has:
			if err := p.DeleteRecordSet(ctx, m.zone.Name, ex.Name, typ); err != nil {
				return finish(err)
			}
			changed = append(changed, recordSetView(dns.RecordSet{Name: ex.Name, Type: typ}))
			types = append(types, typ+" removed")
		}
	}
	return finish(nil)
}
