package submission

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Abuse rules (wiki/E-Mail-Architecture.md, "Limits and abuse").
const (
	// HitsToBlock hourly limit hits within HitWindow block a mailbox. At
	// most one hit is counted per hour, so a client retrying a refused
	// message counts once.
	HitsToBlock = 3
	HitWindow   = 24 * time.Hour
	// More than MaxNetworks different networks (IPv4 /24, IPv6 /64) with a
	// successful sign-in within NetworkWindow block a mailbox.
	MaxNetworks   = 10
	NetworkWindow = time.Hour
	// SendLogRetention: older send log entries are no longer looked at.
	SendLogRetention = 48 * time.Hour
)

// Limiter applies the send limits and blocks mailboxes. Counting:
//
//   - One accepted message counts 1 against the mailbox's hourly and daily
//     limits and the domain's hourly limit, whatever its number of
//     recipients (the recipients per message are bounded separately by
//     config.Limits.MaxRecipients). Local and external recipients count
//     alike.
//   - The windows slide (the last 60 minutes, the last 24 hours) and are
//     kept in mail.db, so a restart does not reset them.
//   - The domain of the signed-in mailbox counts, not the domain of the
//     From address.
//
// It is shared by both submission ports.
type Limiter struct {
	store *storage.Store
	now   func() time.Time
	event func(config.Event)
	log   *slog.Logger
}

// NewLimiter returns a Limiter. now may be nil (time.Now); event records
// events for the panel.
func NewLimiter(store *storage.Store, now func() time.Time, event func(config.Event), log *slog.Logger) *Limiter {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	if event == nil {
		event = func(config.Event) {}
	}
	return &Limiter{store: store, now: now, event: event, log: log}
}

const (
	limitHour = iota
	limitDay
	limitDomain
)

func (l *Limiter) limits(dir *directory.Directory, box config.Mailbox, now time.Time) ([]storage.SendLimit, string) {
	perHour := box.SendPerHour
	if perHour <= 0 {
		perHour = config.DefaultSendPerHour
	}
	perDay := box.SendPerDay
	if perDay <= 0 {
		perDay = config.DefaultSendPerDay
	}
	_, dom, _ := config.SplitAddress(box.Address)
	domHour := config.DefaultDomainSendPerHour
	if d, ok := dir.Domain(dom); ok && d.SendPerHour > 0 {
		domHour = d.SendPerHour
	}
	return []storage.SendLimit{
		limitHour:   {Key: storage.KeySends + box.StorageID, Since: now.Add(-time.Hour), Max: perHour},
		limitDay:    {Key: storage.KeySends + box.StorageID, Since: now.Add(-24 * time.Hour), Max: perDay},
		limitDomain: {Key: storage.KeyDomainSends + dom, Since: now.Add(-time.Hour), Max: domHour},
	}, dom
}

// Check refuses when a limit is already reached, without counting (at
// MAIL FROM, so a client learns early).
func (l *Limiter) Check(ctx context.Context, dir *directory.Directory, box config.Mailbox) error {
	now := l.now()
	limits, dom := l.limits(dir, box, now)
	for i, lim := range limits {
		n, err := l.store.CountMarks(ctx, lim.Key, lim.Since)
		if err != nil {
			l.log.Error("submission: count sends", "err", err)
			return errLocal
		}
		if n >= lim.Max {
			l.refused(ctx, box, dom, i, lim.Max, now)
			return errRateLimited
		}
	}
	return nil
}

// Reserve counts one message, or refuses it when a limit is reached.
func (l *Limiter) Reserve(ctx context.Context, dir *directory.Directory, box config.Mailbox) error {
	now := l.now()
	limits, dom := l.limits(dir, box, now)
	i, err := l.store.ReserveSend(ctx, limits, now)
	if err != nil {
		l.log.Error("submission: count sends", "err", err)
		return errLocal
	}
	if i >= 0 {
		l.refused(ctx, box, dom, i, limits[i].Max, now)
		return errRateLimited
	}
	return nil
}

func (l *Limiter) refused(ctx context.Context, box config.Mailbox, dom string, which, max int, now time.Time) {
	var detail string
	switch which {
	case limitHour:
		detail = fmt.Sprintf("reached its limit of %d messages per hour", max)
	case limitDay:
		detail = fmt.Sprintf("reached its limit of %d messages per day", max)
	case limitDomain:
		detail = fmt.Sprintf("was refused: the domain %s reached its limit of %d messages per hour", dom, max)
	}
	added, _, err := l.store.MarkOnce(ctx, storage.KeyNotices+box.StorageID, fmt.Sprint(which), now.Add(-time.Hour), now)
	if err != nil {
		l.log.Error("submission: record limit", "err", err)
		return
	}
	if !added {
		return
	}
	l.log.Warn("submission: send limit reached", "storage_id", box.StorageID, "limit", which)
	l.event(config.Event{Kind: config.EventSendLimit, StorageID: box.StorageID, Address: box.Address,
		Detail: box.Address + " " + detail})
	if which != limitHour {
		return
	}
	// One hit per hour (the notice above is deduped the same way).
	if _, _, err := l.store.MarkOnce(ctx, storage.KeyHourlyHits+box.StorageID, "", now.Add(-time.Hour), now); err != nil {
		l.log.Error("submission: record limit hit", "err", err)
		return
	}
	hits, err := l.store.CountMarks(ctx, storage.KeyHourlyHits+box.StorageID, now.Add(-HitWindow))
	if err != nil {
		l.log.Error("submission: count limit hits", "err", err)
		return
	}
	if hits >= HitsToBlock {
		l.block(ctx, box, fmt.Sprintf("reached the hourly sending limit %d times within 24 hours", hits))
	}
}

// SignedIn records the network of a successful sign-in and blocks the
// mailbox when it signed in from too many networks within an hour.
func (l *Limiter) SignedIn(ctx context.Context, box config.Mailbox, ip netip.Addr) {
	if !ip.IsValid() {
		return
	}
	now := l.now()
	_, n, err := l.store.MarkOnce(ctx, storage.KeyNetworks+box.StorageID, network(ip), now.Add(-NetworkWindow), now)
	if err != nil {
		l.log.Error("submission: record sign-in network", "err", err)
		return
	}
	if n > MaxNetworks {
		l.block(ctx, box, fmt.Sprintf("signed in from %d different networks within an hour", n))
	}
}

// network is the unit sign-ins are counted in: /24 for IPv4, /64 for IPv6
// (one customer or phone network usually has at least that).
func network(ip netip.Addr) string {
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

func (l *Limiter) block(ctx context.Context, box config.Mailbox, reason string) {
	b, err := l.store.Blocked(ctx, box.StorageID)
	if err != nil {
		l.log.Error("submission: block state", "err", err)
		return
	}
	if b.Blocked {
		return
	}
	if err := l.store.SetBlocked(ctx, box.StorageID, reason); err != nil {
		l.log.Error("submission: block mailbox", "err", err)
		return
	}
	l.log.Warn("submission: mailbox blocked from sending", "storage_id", box.StorageID, "reason", reason)
	l.event(config.Event{Kind: config.EventBlocked, StorageID: box.StorageID, Address: box.Address,
		Detail: "sending was blocked for " + box.Address + ": it " + reason + "; receiving goes on"})
}

// Prune drops send log entries no rule looks at any more.
func (l *Limiter) Prune(ctx context.Context) error {
	return l.store.PruneSendLog(ctx, l.now().Add(-SendLogRetention))
}
