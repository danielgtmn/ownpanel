package dns

import (
	"context"
	"net"
	"slices"

	"golang.org/x/net/dns/dnsmessage"
)

// TXTResolver looks up the TXT records of a name. *net.Resolver and
// *Authoritative satisfy it.
type TXTResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// SRVResolver looks up SRV records like net.Resolver: with service and proto
// set, the name looked up is _service._proto.name; with both empty, name
// itself. *net.Resolver and *Authoritative satisfy it.
type SRVResolver interface {
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
}

// AddrResolver does reverse lookups (PTR) of an address. *net.Resolver and
// *Authoritative satisfy it.
type AddrResolver interface {
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// MailResolver is everything the mail DNS checks look up. *Authoritative
// satisfies it.
type MailResolver interface {
	MXResolver
	TXTResolver
	SRVResolver
}

var (
	_ MailResolver = (*Authoritative)(nil)
	_ AddrResolver = (*Authoritative)(nil)
	_ MailResolver = (*net.Resolver)(nil)
	_ AddrResolver = (*net.Resolver)(nil)
)

// LookupTXT implements TXTResolver at the authoritative nameservers of
// name's zone. The strings of one record are joined into one value, like
// net.Resolver does. A name without TXT records gives a not-found
// *net.DNSError. If no nameserver can be reached, the system resolver is
// asked when it can look up TXT records.
func (a *Authoritative) LookupTXT(ctx context.Context, name string) ([]string, error) {
	host := normalizeName(name)
	actx, cancel := context.WithTimeout(ctx, authBudget)
	r, err := a.lookupType(actx, host, dnsmessage.TypeTXT)
	cancel()
	if err == nil && len(r.txt) == 0 {
		err = a.noRecords(host, r)
	}
	if err == nil || isNotFound(err) {
		return r.txt, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if sys, ok := a.System.(TXTResolver); ok {
		return sys.LookupTXT(ctx, host)
	}
	return nil, err
}

// LookupSRV implements SRVResolver at the authoritative nameservers of the
// name's zone. Records are sorted by priority, then weight (highest first);
// targets end in a dot. The returned cname is the name that was looked up. A
// name without SRV records gives a not-found *net.DNSError. If no
// nameserver can be reached, the system resolver is asked when it can look
// up SRV records.
func (a *Authoritative) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	host := normalizeName(name)
	if service != "" || proto != "" {
		host = "_" + service + "._" + proto + "." + host
	}
	actx, cancel := context.WithTimeout(ctx, authBudget)
	r, err := a.lookupType(actx, host, dnsmessage.TypeSRV)
	cancel()
	if err == nil && len(r.srv) == 0 {
		err = a.noRecords(host, r)
	}
	if err == nil {
		slices.SortStableFunc(r.srv, func(x, y *net.SRV) int {
			if x.Priority != y.Priority {
				return int(x.Priority) - int(y.Priority)
			}
			return int(y.Weight) - int(x.Weight)
		})
		return host + ".", r.srv, nil
	}
	if isNotFound(err) {
		return "", nil, err
	}
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	if sys, ok := a.System.(SRVResolver); ok {
		return sys.LookupSRV(ctx, "", "", host)
	}
	return "", nil, err
}

// LookupAddr implements AddrResolver. Reverse zones are rarely served by the
// same nameservers as forward zones and are not cached for long, so the
// system resolver is asked when it can; otherwise the lookup fails.
func (a *Authoritative) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	if sys, ok := a.System.(AddrResolver); ok {
		return sys.LookupAddr(ctx, addr)
	}
	return nil, &net.DNSError{Err: "no reverse resolver", Name: addr}
}

func (a *Authoritative) lookupType(ctx context.Context, host string, qt dnsmessage.Type) (answer, error) {
	_, servers, err := a.zoneFor(ctx, host)
	if err != nil {
		return answer{}, err
	}
	return a.query(ctx, servers, host, qt)
}

// noRecords is the error for an answer without records of the asked type.
func (a *Authoritative) noRecords(host string, r answer) error {
	if r.cname != "" {
		// An alias without its records in the answer is not followed.
		return errCNAMEOutside{target: r.cname}
	}
	return notFound(host)
}
