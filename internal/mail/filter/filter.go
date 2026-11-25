// Package filter has the checks of incoming mail on port 25 besides
// authentication (package auth): DNS blocklists, greylisting, the content
// score and the attachment rules. The smtpd session decides what to do
// with the results.
//
// See wiki/E-Mail-Architecture.md, "Filtering".
package filter

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Options configure a Filter.
type Options struct {
	// Resolver: nil means net.DefaultResolver.
	Resolver auth.Resolver
	Store    *storage.Store
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
	// Event records an event for the panel; nil drops them.
	Event  func(config.Event)
	Logger *slog.Logger
}

// Filter runs the checks. It is safe for concurrent use.
type Filter struct {
	opts Options
}

// New returns a Filter.
func New(opts Options) *Filter {
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Event == nil {
		opts.Event = func(config.Event) {}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Filter{opts: opts}
}

// Resolver is the resolver the checks use.
func (f *Filter) Resolver() auth.Resolver { return f.opts.Resolver }

// Points of the content score. A message whose score reaches the Junk
// threshold (config.Filter.JunkScore, default 5) goes to Junk.
const (
	PointsNoMessageID = 1.0
	PointsNoDate      = 1.0
	PointsBadFrom     = 2.5 // missing, unreadable or several From fields
	PointsNoPTR       = 1.5
	PointsSpoofedName = 3.0 // display name with an address of the recipient's domain
	PointsSPFSoftFail = 1.0
	PointsSPFFail     = 2.5
	PointsDKIMFail    = 1.5
	PointsListed      = 3.0 // per blocklist listing that does not refuse
	PointsBrokenMIME  = 1.0
)

// Score is the content score and its reasons (short tokens).
type Score struct {
	Points  float64
	Reasons []string
}

// Add adds points for a reason.
func (s *Score) Add(points float64, reason string) {
	s.Points += points
	s.Reasons = append(s.Reasons, reason)
}

// Merge adds another score.
func (s *Score) Merge(o Score) {
	s.Points += o.Points
	s.Reasons = append(s.Reasons, o.Reasons...)
}

// String is "3.5 (no-ptr spf-softfail)", or "0" without reasons.
func (s Score) String() string {
	p := strconv.FormatFloat(s.Points, 'f', -1, 64)
	if len(s.Reasons) == 0 {
		return p
	}
	return p + " (" + strings.Join(s.Reasons, " ") + ")"
}

// Header is the X-Ownpanel-Score header field with its final CRLF.
func (s Score) Header() string { return ScoreHeaderName + ": " + s.String() + "\r\n" }

// ScoreHeaderName is the header field of the score; incoming fields of
// that name are removed, so nobody can fake one.
const ScoreHeaderName = "X-Ownpanel-Score"

// Checkable tells whether ip is a public address worth asking blocklists
// and reverse DNS about (not loopback, private or link-local).
func Checkable(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}

// PTR tells whether ip has a reverse DNS name. known is false when the
// lookup failed for another reason than the name not existing.
func (f *Filter) PTR(ctx context.Context, ip netip.Addr) (has, known bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	names, err := f.opts.Resolver.LookupAddr(ctx, ip.Unmap().String())
	if err != nil {
		if de, ok := err.(*net.DNSError); ok && de.IsNotFound {
			return false, true
		}
		return false, false
	}
	return len(names) > 0, true
}

// BlocklistResult is what the blocklists say about an address.
type BlocklistResult struct {
	// Refuse is the zone of a listing that refuses mail ("" if none).
	Refuse string
	// Score for listings that do not refuse.
	Score Score
}

// spamhausRefuse are the answers of Spamhaus zen that refuse mail: SBL
// (2), CSS (3), XBL (4-7), DROP (9). PBL (10, 11) only scores.
var spamhausRefuse = map[byte]bool{2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 9: true}

func isSpamhaus(zone string) bool {
	return strings.HasSuffix(zone, "spamhaus.org") || strings.HasSuffix(zone, "spamhaus.net")
}

// Blocklists looks ip up in the zones (IPv4 only; IPv6 addresses are not
// looked up). Answers in 127.255.255.0/24 mean the list refused the query
// (typical through public resolvers): that is a lookup error, recorded as
// an event at most once a day per zone.
func (f *Filter) Blocklists(ctx context.Context, ip netip.Addr, zones []string) BlocklistResult {
	var res BlocklistResult
	ip = ip.Unmap()
	if !ip.Is4() || len(zones) == 0 {
		return res
	}
	b := ip.As4()
	rev := fmt.Sprintf("%d.%d.%d.%d", b[3], b[2], b[1], b[0])
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	answers := make([][]net.IPAddr, len(zones))
	var wg sync.WaitGroup
	for i, z := range zones {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := f.opts.Resolver.LookupIPAddr(ctx, rev+"."+z)
			if err == nil {
				answers[i] = a
			}
		}()
	}
	wg.Wait()
	for i, z := range zones {
		listed, refused := false, false
		for _, a := range answers[i] {
			v4 := a.IP.To4()
			if v4 == nil || v4[0] != 127 {
				continue
			}
			if v4[1] == 255 && v4[2] == 255 {
				refused = true
				continue
			}
			if isSpamhaus(z) && v4[1] == 0 && v4[2] == 0 && spamhausRefuse[v4[3]] {
				if res.Refuse == "" {
					res.Refuse = z
				}
				continue
			}
			listed = true
		}
		if refused {
			f.queryRefused(z)
		}
		if listed {
			res.Score.Add(PointsListed, "listed:"+z)
		}
	}
	return res
}

func (f *Filter) queryRefused(zone string) {
	if f.opts.Store == nil {
		return
	}
	now := f.opts.Now()
	added, _, err := f.opts.Store.MarkOnce(context.Background(), storage.KeyBlocklistRefused+zone, "", now.Add(-24*time.Hour), now)
	if err != nil {
		f.opts.Logger.Error("filter: record blocklist refusal", "err", err)
		return
	}
	if !added {
		return
	}
	f.opts.Logger.Warn("filter: blocklist refused our queries", "zone", zone)
	f.opts.Event(config.Event{Kind: config.EventBlocklistRefused,
		Detail: "the blocklist " + zone + " refused to answer this server's queries (usually because the server uses a public DNS resolver); incoming mail is not checked against it"})
}

// Greylisting timings.
const (
	GreylistDelay = 5 * time.Minute
	// GreylistRetry: a sender that does not come back within this time
	// starts over.
	GreylistRetry = 24 * time.Hour
	// GreylistValid: a triplet that passed passes for this long after its
	// last message.
	GreylistValid = 30 * 24 * time.Hour
)

// Greylist tells whether a message from ip (counted per /24 or /64) with
// the envelope sender to rcpt may pass now.
func (f *Filter) Greylist(ctx context.Context, ip netip.Addr, sender, rcpt string) (bool, error) {
	key := Network(ip) + "|" + strings.ToLower(sender) + "|" + strings.ToLower(rcpt)
	return f.opts.Store.GreylistCheck(ctx, key, f.opts.Now(), storage.Greylist{
		Delay: GreylistDelay, Retry: GreylistRetry, Valid: GreylistValid,
	})
}

// Prune drops greylisting triplets that are no longer looked at.
func (f *Filter) Prune(ctx context.Context) error {
	return f.opts.Store.PruneGreylist(ctx, f.opts.Now().Add(-GreylistValid))
}

// Network is ip's /24 (IPv4) or /64 (IPv6).
func Network(ip netip.Addr) string {
	ip = ip.Unmap()
	bits := 24
	if ip.Is6() {
		bits = 64
	}
	p, err := ip.Prefix(bits)
	if err != nil {
		return ip.String()
	}
	return p.String()
}
