package auth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/authres"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-msgauth/dmarc"
	"golang.org/x/net/publicsuffix"
)

// Resolver is the DNS the checks of incoming mail use; *net.Resolver is
// one. Tests replace it, so they never touch the network.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// Result values, as in Authentication-Results (RFC 8601).
const (
	Pass      = "pass"
	Fail      = "fail"
	SoftFail  = "softfail"
	Neutral   = "neutral"
	None      = "none"
	TempError = "temperror"
	PermError = "permerror"
)

// lookupTimeout bounds one group of DNS lookups (SPF, DKIM, DMARC).
const lookupTimeout = 20 * time.Second

// Verifier checks SPF, DKIM and DMARC of incoming mail.
type Verifier struct {
	Resolver Resolver
}

// SPFResult is the SPF verdict for the envelope sender.
type SPFResult struct {
	Result string
	// Domain is the domain that was checked: of MAIL FROM, or the HELO name
	// for the null sender.
	Domain string
	// Helo tells whether Domain is the HELO name.
	Helo bool
}

// SPF evaluates the SPF policy of the envelope sender's domain (of the
// HELO name for the null sender) for the client ip.
func (v *Verifier) SPF(ctx context.Context, ip netip.Addr, helo, sender string) SPFResult {
	res := SPFResult{Domain: domainOf(sender)}
	if res.Domain == "" {
		res.Domain, res.Helo = strings.ToLower(helo), true
	}
	if res.Domain == "" || net.ParseIP(res.Domain) != nil || !strings.Contains(res.Domain, ".") {
		res.Result = None
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	r, _ := spf.CheckHostWithSender(net.IP(ip.Unmap().AsSlice()), helo, sender,
		spf.WithContext(ctx), spf.WithResolver(v.Resolver))
	res.Result = string(r)
	if res.Result == "" {
		res.Result = TempError
	}
	return res
}

// DKIMResult is the verdict for one DKIM signature.
type DKIMResult struct {
	Result string
	Domain string // d=
}

// maxSignatures bounds the DKIM signatures verified per message.
const maxSignatures = 5

// DKIM verifies the DKIM signatures of the message in r (the message as
// received). A message without signatures gives none.
func (v *Verifier) DKIM(ctx context.Context, r io.Reader) []DKIMResult {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	verifs, err := dkim.VerifyWithOptions(r, &dkim.VerifyOptions{
		MaxVerifications: maxSignatures,
		LookupTXT: func(name string) ([]string, error) {
			return v.Resolver.LookupTXT(ctx, name)
		},
	})
	if err != nil && !errors.Is(err, dkim.ErrTooManySignatures) {
		// An unreadable header: no verdict to give.
		return nil
	}
	var out []DKIMResult
	for _, vr := range verifs {
		res := DKIMResult{Domain: strings.ToLower(vr.Domain), Result: Pass}
		switch {
		case vr.Err == nil:
		case dkim.IsTempFail(vr.Err):
			res.Result = TempError
		case dkim.IsPermFail(vr.Err):
			res.Result = PermError
		default:
			res.Result = Fail
		}
		out = append(out, res)
	}
	return out
}

// DMARCResult is the DMARC verdict for the From domain.
type DMARCResult struct {
	Result string // pass, fail, none, temperror, permerror
	// Domain is the From domain evaluated.
	Domain string
	// Policy is the policy that applies when Result is fail ("none",
	// "quarantine", "reject"), after the subdomain policy and a pct below
	// 100 (which lowers it one step) are taken into account.
	Policy string
}

// OrgDomain is the organizational domain of d (the registrable domain by
// the public suffix list), or d itself when it has none.
func OrgDomain(d string) string {
	d = strings.ToLower(strings.TrimSuffix(d, "."))
	if o, err := publicsuffix.EffectiveTLDPlusOne(d); err == nil {
		return o
	}
	return d
}

func aligned(a, b string, mode dmarc.AlignmentMode) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == "" || b == "" {
		return false
	}
	if mode == dmarc.AlignmentStrict {
		return a == b
	}
	return OrgDomain(a) == OrgDomain(b)
}

// DMARC evaluates the DMARC policy of fromDomain: the record of the domain
// itself, else of its organizational domain (whose sp= then applies). It
// passes when SPF or a DKIM signature passes for an aligned domain.
func (v *Verifier) DMARC(ctx context.Context, fromDomain string, spfRes SPFResult, dkimRes []DKIMResult) DMARCResult {
	fromDomain = strings.ToLower(fromDomain)
	res := DMARCResult{Domain: fromDomain, Result: None, Policy: string(dmarc.PolicyNone)}
	if fromDomain == "" {
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	lookup := func(d string) (*dmarc.Record, error) {
		return dmarc.LookupWithOptions(d, &dmarc.LookupOptions{LookupTXT: func(name string) ([]string, error) {
			return v.Resolver.LookupTXT(ctx, name)
		}})
	}
	rec, err := lookup(fromDomain)
	sub := false
	if errors.Is(err, dmarc.ErrNoPolicy) {
		if org := OrgDomain(fromDomain); org != fromDomain {
			rec, err = lookup(org)
			sub = true
		}
	}
	switch {
	case errors.Is(err, dmarc.ErrNoPolicy):
		return res
	case dmarc.IsTempFail(err):
		res.Result = TempError
		return res
	case err != nil:
		res.Result = PermError
		return res
	}
	policy := rec.Policy
	if sub && rec.SubdomainPolicy != "" {
		policy = rec.SubdomainPolicy
	}
	if rec.Percent != nil && *rec.Percent < 100 {
		// A policy being rolled out: apply the next lower one to all mail
		// rather than to a random share (RFC 7489 6.6.4).
		switch policy {
		case dmarc.PolicyReject:
			policy = dmarc.PolicyQuarantine
		case dmarc.PolicyQuarantine:
			policy = dmarc.PolicyNone
		}
	}
	res.Policy = string(policy)
	// With the null sender, the HELO name is the SPF identity (RFC 7489
	// 3.1.2).
	if spfRes.Result == Pass && aligned(spfRes.Domain, fromDomain, rec.SPFAlignment) {
		res.Result = Pass
		return res
	}
	for _, d := range dkimRes {
		if d.Result == Pass && aligned(d.Domain, fromDomain, rec.DKIMAlignment) {
			res.Result = Pass
			return res
		}
	}
	// A lookup error on the way may have kept a pass from happening:
	// never fail for that.
	if spfRes.Result == TempError {
		res.Result = TempError
		return res
	}
	for _, d := range dkimRes {
		if d.Result == TempError {
			res.Result = TempError
			return res
		}
	}
	res.Result = Fail
	return res
}

// ResultsHeader formats an Authentication-Results header field (with its
// final CRLF) for hostname.
func ResultsHeader(hostname string, spfRes SPFResult, dkimRes []DKIMResult, dmarcRes []DMARCResult) string {
	var results []authres.Result
	s := &authres.SPFResult{Value: authres.ResultValue(spfRes.Result)}
	if spfRes.Helo {
		s.Helo = spfRes.Domain
	} else {
		s.From = spfRes.Domain
	}
	results = append(results, s)
	if len(dkimRes) == 0 {
		results = append(results, &authres.DKIMResult{Value: authres.ResultNone})
	}
	for _, d := range dkimRes {
		results = append(results, &authres.DKIMResult{Value: authres.ResultValue(d.Result), Domain: d.Domain})
	}
	if len(dmarcRes) == 0 {
		results = append(results, &authres.DMARCResult{Value: authres.ResultNone})
	}
	for _, d := range dmarcRes {
		results = append(results, &authres.DMARCResult{Value: authres.ResultValue(d.Result), From: d.Domain})
	}
	return foldHeader("Authentication-Results: " + authres.Format(hostname, results))
}

// foldHeader breaks a header field after "; " so lines stay short, and
// ends it with CRLF.
func foldHeader(s string) string {
	parts := strings.Split(s, "; ")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ";\r\n\t") + "\r\n"
}

// AuthServID returns the authserv-id of an Authentication-Results value
// (the host name before the first ";"), lowercase.
func AuthServID(value string) string {
	v := value
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = v[:i]
	}
	f := strings.Fields(v)
	if len(f) == 0 {
		return ""
	}
	return strings.ToLower(f[0])
}

func domainOf(addr string) string {
	i := strings.LastIndexByte(addr, '@')
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[i+1:])
}
