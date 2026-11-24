package dns

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Why this exists: a recursive resolver caches NXDOMAIN for the zone's SOA
// minimum (an hour at Hetzner). A person who looks at a domain before its
// record exists, then creates the record, would keep seeing "no record" for
// that hour. Asking the zone's own nameservers has no such cache.

const (
	// zoneCacheLifetime is how long a zone's nameservers are remembered.
	zoneCacheLifetime = 10 * time.Minute
	// queryTimeout bounds one query to one nameserver.
	queryTimeout = 3 * time.Second
	// authBudget bounds the whole authoritative attempt, so that the system
	// resolver fallback still has time left within the caller's deadline.
	authBudget = 5 * time.Second
	// maxNameservers is how many nameserver addresses are ever tried.
	maxNameservers = 6
	maxCNAMEDepth  = 8
)

// SystemResolver is what Authoritative needs from the normal resolver:
// *net.Resolver satisfies it.
type SystemResolver interface {
	Resolver
	LookupNS(ctx context.Context, name string) ([]*net.NS, error)
}

// Authoritative looks names up at the authoritative nameservers of their
// zone, bypassing recursive caches. It implements Resolver. The system
// resolver is used to find the zone's nameservers and their addresses, for
// CNAME targets outside the zone, and as the fallback when no nameserver
// can be reached, so it is never worse than the system resolver alone.
type Authoritative struct {
	System SystemResolver
	// Dial opens the connection to a nameserver ("udp" or "tcp", "ip:53").
	// Nil means net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Timeout bounds one query to one nameserver; zero means 3 s.
	Timeout time.Duration
	// Now is the clock for the zone cache; nil means time.Now.
	Now func() time.Time

	mu    sync.Mutex
	zones map[string]zoneEntry // by queried name
}

type zoneEntry struct {
	zone    string
	servers []netip.Addr
	at      time.Time
}

// NewAuthoritative returns an Authoritative lookup backed by system, which
// is net.DefaultResolver if nil.
func NewAuthoritative(system SystemResolver) *Authoritative {
	if system == nil {
		system = net.DefaultResolver
	}
	return &Authoritative{System: system}
}

func (a *Authoritative) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Authoritative) timeout() time.Duration {
	if a.Timeout > 0 {
		return a.Timeout
	}
	return queryTimeout
}

func notFound(host string) error {
	return &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// LookupNetIP implements Resolver. The network argument is ignored: both
// address families are always asked for.
func (a *Authoritative) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	host = normalizeName(host)
	actx, cancel := context.WithTimeout(ctx, authBudget)
	addrs, err := a.lookup(actx, host, 0)
	cancel()
	if err == nil || isNotFound(err) {
		return addrs, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Nothing authoritative could be learned: behave like the system.
	return a.System.LookupNetIP(ctx, network, host)
}

func isNotFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

// errCNAMEOutside carries a CNAME target the authoritative servers cannot
// answer for.
type errCNAMEOutside struct{ target string }

func (e errCNAMEOutside) Error() string { return "CNAME to " + e.target }

func (a *Authoritative) lookup(ctx context.Context, host string, depth int) ([]netip.Addr, error) {
	if depth > maxCNAMEDepth {
		return nil, errors.New("too many CNAMEs")
	}
	zone, servers, err := a.zoneFor(ctx, host)
	if err != nil {
		return nil, err
	}
	type result struct {
		addrs []netip.Addr
		cname string
		err   error
	}
	types := []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA}
	res := make([]result, len(types))
	var wg sync.WaitGroup
	for i, t := range types {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := a.query(ctx, servers, host, t)
			res[i] = result{r.addrs, r.cname, err}
		}()
	}
	wg.Wait()
	var all []netip.Addr
	var cname string
	for _, r := range res {
		if r.err != nil {
			return nil, r.err
		}
		all = append(all, r.addrs...)
		if r.cname != "" {
			cname = r.cname
		}
	}
	if len(all) > 0 {
		return all, nil
	}
	if cname != "" {
		if cname == host {
			return nil, errors.New("CNAME loop")
		}
		if cname == zone || strings.HasSuffix(cname, "."+zone) {
			return a.lookup(ctx, cname, depth+1)
		}
		return a.System.LookupNetIP(ctx, "ip", cname)
	}
	// NXDOMAIN and NODATA alike mean: no address records.
	return nil, notFound(host)
}

// zoneFor finds the zone that holds host by walking up its labels with NS
// lookups, and the addresses of that zone's nameservers.
func (a *Authoritative) zoneFor(ctx context.Context, host string) (string, []netip.Addr, error) {
	now := a.now()
	a.mu.Lock()
	e, ok := a.zones[host]
	a.mu.Unlock()
	if ok && now.Sub(e.at) < zoneCacheLifetime && !now.Before(e.at) {
		return e.zone, e.servers, nil
	}
	for name := host; name != ""; {
		nss, err := a.System.LookupNS(ctx, name)
		if err != nil && !isNotFound(err) && len(nss) == 0 {
			return "", nil, err
		}
		if len(nss) > 0 {
			servers := a.nameserverAddrs(ctx, nss)
			if len(servers) == 0 {
				return "", nil, fmt.Errorf("no address for the nameservers of %s", name)
			}
			a.mu.Lock()
			if a.zones == nil {
				a.zones = map[string]zoneEntry{}
			}
			for k, v := range a.zones { // keep the map from growing without bound
				if now.Sub(v.at) >= zoneCacheLifetime {
					delete(a.zones, k)
				}
			}
			a.zones[host] = zoneEntry{zone: name, servers: servers, at: now}
			a.mu.Unlock()
			return name, servers, nil
		}
		_, rest, found := strings.Cut(name, ".")
		if !found {
			break
		}
		name = rest
	}
	return "", nil, fmt.Errorf("no zone found for %s", host)
}

func (a *Authoritative) nameserverAddrs(ctx context.Context, nss []*net.NS) []netip.Addr {
	perNS := make([][]netip.Addr, len(nss))
	var wg sync.WaitGroup
	for i, ns := range nss {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := normalizeName(ns.Host)
			if ip, err := netip.ParseAddr(h); err == nil {
				perNS[i] = []netip.Addr{ip}
				return
			}
			perNS[i], _ = a.System.LookupNetIP(ctx, "ip", h)
		}()
	}
	wg.Wait()
	// Interleave so that the first addresses come from different servers.
	var out []netip.Addr
	seen := map[netip.Addr]bool{}
	for round := 0; len(out) < maxNameservers; round++ {
		more := false
		for _, l := range perNS {
			if round < len(l) {
				more = true
				ip := l[round].Unmap().WithZone("")
				if !seen[ip] && len(out) < maxNameservers {
					seen[ip] = true
					out = append(out, ip)
				}
			}
		}
		if !more {
			break
		}
	}
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

type answer struct {
	addrs    []netip.Addr
	cname    string // set if the name is an alias without addresses in the answer
	nxdomain bool
}

// query asks the servers one after the other until one gives an
// authoritative answer.
func (a *Authoritative) query(ctx context.Context, servers []netip.Addr, host string, qt dnsmessage.Type) (answer, error) {
	var lastErr error
	for _, s := range servers {
		if err := ctx.Err(); err != nil {
			return answer{}, err
		}
		r, err := a.queryOne(ctx, s, host, qt)
		if err == nil {
			return r, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no nameserver to ask")
	}
	return answer{}, lastErr
}

func (a *Authoritative) queryOne(ctx context.Context, server netip.Addr, host string, qt dnsmessage.Type) (answer, error) {
	qname, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return answer{}, err
	}
	id := uint16(rand.UintN(1 << 16))
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: id}, // no recursion desired
		Questions: []dnsmessage.Question{{Name: qname, Type: qt, Class: dnsmessage.ClassINET}},
	}
	packed, err := msg.Pack()
	if err != nil {
		return answer{}, err
	}
	addr := net.JoinHostPort(server.String(), "53")
	qctx, cancel := context.WithTimeout(ctx, a.timeout())
	defer cancel()
	raw, err := a.exchange(qctx, "udp", addr, packed)
	var resp dnsmessage.Message
	if err == nil {
		err = resp.Unpack(raw)
	}
	if err == nil && resp.Truncated {
		if raw, err = a.exchange(qctx, "tcp", addr, packed); err == nil {
			resp = dnsmessage.Message{}
			err = resp.Unpack(raw)
		}
	}
	if err != nil {
		return answer{}, err
	}
	if resp.ID != id || !resp.Response || len(resp.Questions) != 1 ||
		resp.Questions[0].Type != qt || !strings.EqualFold(resp.Questions[0].Name.String(), qname.String()) {
		return answer{}, errors.New("mismatched DNS response")
	}
	switch resp.RCode {
	case dnsmessage.RCodeSuccess, dnsmessage.RCodeNameError:
	default:
		return answer{}, fmt.Errorf("nameserver %s answered %v", server, resp.RCode)
	}
	if !resp.Authoritative && len(resp.Answers) == 0 {
		return answer{}, fmt.Errorf("nameserver %s is not authoritative", server)
	}
	if resp.RCode == dnsmessage.RCodeNameError {
		return answer{nxdomain: true}, nil
	}
	return readAnswer(resp.Answers, host, qt), nil
}

// readAnswer follows the CNAME chain of the answer section from host.
func readAnswer(rrs []dnsmessage.Resource, host string, qt dnsmessage.Type) answer {
	cur := host
	for range maxCNAMEDepth + 1 {
		var next string
		var out answer
		for _, rr := range rrs {
			if normalizeName(rr.Header.Name.String()) != cur {
				continue
			}
			switch b := rr.Body.(type) {
			case *dnsmessage.AResource:
				if qt == dnsmessage.TypeA {
					out.addrs = append(out.addrs, netip.AddrFrom4(b.A))
				}
			case *dnsmessage.AAAAResource:
				if qt == dnsmessage.TypeAAAA {
					out.addrs = append(out.addrs, netip.AddrFrom16(b.AAAA))
				}
			case *dnsmessage.CNAMEResource:
				next = normalizeName(b.CNAME.String())
			}
		}
		if len(out.addrs) > 0 {
			return out
		}
		if next == "" {
			if cur != host {
				return answer{cname: cur}
			}
			return answer{}
		}
		cur = next
	}
	return answer{cname: cur}
}

func (a *Authoritative) exchange(ctx context.Context, network, addr string, packed []byte) ([]byte, error) {
	dial := a.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if network == "udp" {
		if _, err := conn.Write(packed); err != nil {
			return nil, err
		}
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	}
	frame := binary.BigEndian.AppendUint16(nil, uint16(len(packed)))
	if _, err := conn.Write(append(frame, packed...)); err != nil {
		return nil, err
	}
	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
