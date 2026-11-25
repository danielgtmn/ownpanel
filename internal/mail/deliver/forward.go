package deliver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// checkRedirects decides which redirect targets may be used. refused is
// set when one was not allowed (an event says why).
func (d *Deliverer) checkRedirects(ctx context.Context, m *Message, box *config.Mailbox, targets []string) (allowed []string, refused bool) {
	if len(targets) == 0 {
		return nil, false
	}
	why := ""
	h := m.msg()
	mark := loopMark(m.Dir.Hostname(), box.StorageID)
	marks := h.Header(strings.ToLower(LoopHeader))
	switch {
	case m.Junk:
		why = "it was judged as junk"
	case m.DMARCFail:
		why = "it failed the sender's DMARC check"
	case box.NoForwarding:
		why = "this mailbox may not forward mail"
	case d.blocked(ctx, box):
		why = "sending is blocked for this mailbox"
	case slices.ContainsFunc(marks, func(v string) bool { return strings.TrimSpace(v) == mark }),
		slices.ContainsFunc(h.Header("delivered-to"), func(v string) bool { return strings.EqualFold(strings.TrimSpace(v), box.Address) }):
		why = "it was forwarded by this mailbox before (a loop)"
	case len(marks) >= maxLoopMarks || len(h.Header("received")) > maxHops || m.depth >= maxDepth:
		why = "it was forwarded too often (a loop)"
	}
	if why != "" {
		d.notForwarded(box, targets, why)
		return nil, true
	}
	own := ownAddresses(m.Dir, box)
	for _, t := range targets {
		t = directory.Normalize(t)
		switch {
		case slices.Contains(allowed, t):
		case slices.Contains(own, t):
			d.notForwarded(box, []string{t}, "it is an address of this mailbox")
			refused = true
		case !config.ValidAddress(t):
			d.notForwarded(box, []string{t}, "the address is not valid")
			refused = true
		case len(allowed) >= config.MaxForwardTargets:
			d.notForwarded(box, []string{t}, fmt.Sprintf("at most %d addresses per message", config.MaxForwardTargets))
			refused = true
		default:
			allowed = append(allowed, t)
		}
	}
	return allowed, refused
}

// blocked tells whether the service stopped the mailbox from sending; a
// blocked mailbox forwards and replies to nothing either.
func (d *Deliverer) blocked(ctx context.Context, box *config.Mailbox) bool {
	b, err := d.opts.Store.Blocked(ctx, box.StorageID)
	if err != nil {
		d.log.Error("deliver: block state", "err", err)
		return true // when in doubt, nothing leaves
	}
	return b.Blocked
}

func (d *Deliverer) notForwarded(box *config.Mailbox, targets []string, why string) {
	d.opts.Event(config.Event{Kind: config.EventNotForwarded, StorageID: box.StorageID, Address: box.Address,
		Detail: fmt.Sprintf("a filter of %s did not forward a message to %s: %s", box.Address, strings.Join(targets, ", "), why)})
}

// splitTargets resolves forward targets: mailboxes here get the message
// directly (their own filters run), the rest goes out through the queue.
func (d *Deliverer) splitTargets(m *Message, box *config.Mailbox, targets []string) (local []Recipient, external []string, refused bool) {
	dir := m.Dir
	for _, t := range targets {
		_, dom, _ := config.SplitAddress(t)
		if _, ok := dir.Domain(dom); !ok && dom != dir.Hostname() {
			external = append(external, t)
			continue
		}
		route, out := dir.Resolve(t)
		if out == directory.Unknown || out == directory.NoPostmaster {
			d.notForwarded(box, []string{t}, "no such address here")
			refused = true
			continue
		}
		for _, mb := range route.Mailboxes {
			if mb.StorageID != box.StorageID && !slices.ContainsFunc(local, func(r Recipient) bool { return r.Mailbox.StorageID == mb.StorageID }) {
				local = append(local, Recipient{Mailbox: mb, Rcpt: t})
			}
		}
		for _, e := range route.External {
			if !slices.Contains(external, e) {
				external = append(external, e)
			}
		}
	}
	return local, external, refused
}

// forwardPrefix is what a forwarded copy starts with: Delivered-To and the
// mailbox's loop mark.
func forwardPrefix(dir *directory.Directory, box *config.Mailbox) []byte {
	return []byte("Delivered-To: " + box.Address + "\r\n" + LoopHeader + ": " + loopMark(dir.Hostname(), box.StorageID) + "\r\n")
}

// sendLimits are the mailbox's hourly and daily limits and its domain's
// hourly limit, as for mail it sends.
func sendLimits(dir *directory.Directory, box *config.Mailbox, now time.Time) []storage.SendLimit {
	perHour, perDay := box.SendPerHour, box.SendPerDay
	if perHour <= 0 {
		perHour = config.DefaultSendPerHour
	}
	if perDay <= 0 {
		perDay = config.DefaultSendPerDay
	}
	_, dom, _ := config.SplitAddress(box.Address)
	domHour := config.DefaultDomainSendPerHour
	if dd, ok := dir.Domain(dom); ok && dd.SendPerHour > 0 {
		domHour = dd.SendPerHour
	}
	return []storage.SendLimit{
		{Key: storage.KeySends + box.StorageID, Since: now.Add(-time.Hour), Max: perHour},
		{Key: storage.KeySends + box.StorageID, Since: now.Add(-24 * time.Hour), Max: perDay},
		{Key: storage.KeyDomainSends + dom, Since: now.Add(-time.Hour), Max: domHour},
	}
}

// reserve counts one message against the mailbox's sending limits. ok is
// false (and limit names the limit) when one is reached; reaching it never
// counts towards blocking the mailbox, so outside mail cannot get a
// mailbox blocked.
func (d *Deliverer) reserve(ctx context.Context, dir *directory.Directory, box *config.Mailbox) (res *reservation, ok bool, limit string) {
	now := d.opts.Now()
	limits := sendLimits(dir, box, now)
	i, err := d.opts.Store.ReserveSend(ctx, limits, now)
	if err != nil {
		d.log.Error("deliver: count send", "err", err)
		return nil, true, "" // counting failed: go on, nothing to take back
	}
	switch i {
	case -1:
		return &reservation{limits: limits, at: now}, true, ""
	case 0:
		return nil, false, fmt.Sprintf("its limit of %d messages per hour", limits[0].Max)
	case 1:
		return nil, false, fmt.Sprintf("its limit of %d messages per day", limits[1].Max)
	}
	_, dom, _ := config.SplitAddress(box.Address)
	return nil, false, fmt.Sprintf("the limit of %d messages per hour of the domain %s", limits[2].Max, dom)
}

// reserveForward counts a forward outside against the limits.
func (d *Deliverer) reserveForward(ctx context.Context, m *Message, box *config.Mailbox, targets []string) (*reservation, bool) {
	if d.opts.Queue == nil {
		d.notForwarded(box, targets, "sending outside is not available")
		return nil, false
	}
	res, ok, limit := d.reserve(ctx, m.Dir, box)
	if !ok {
		d.notForwarded(box, targets, "the mailbox reached "+limit)
	}
	return res, ok
}

// enqueue sends a forwarded copy outside.
func (d *Deliverer) enqueue(ctx context.Context, m *Message, box *config.Mailbox, prefix []byte, targets []string) bool {
	_, dom, _ := config.SplitAddress(box.Address)
	sender, storageID := d.forwardSender(m, dom)
	msg, err := m.Open()
	if err == nil {
		err = d.opts.Queue.Enqueue(ctx, io.MultiReader(bytes.NewReader(prefix), msg), sender, storageID, targets)
	}
	if err != nil {
		d.log.Error("deliver: enqueue forward", "err", err)
		return false
	}
	d.log.Info("deliver: forwarded by a filter", "storage_id", box.StorageID, "targets", len(targets))
	return true
}

// forwardSender is the envelope sender of a forward: null stays null; a
// verified sender stays (bounces go to its mailbox if it is one here);
// others get an SRS address in the mailbox's domain.
func (d *Deliverer) forwardSender(m *Message, domain string) (sender, storageID string) {
	if m.EnvelopeFrom == "" {
		return "", ""
	}
	if m.SenderVerified {
		if mb, ok := m.Dir.Mailbox(directory.Normalize(m.EnvelopeFrom)); ok {
			return m.EnvelopeFrom, mb.StorageID
		}
		return m.EnvelopeFrom, ""
	}
	if d.opts.SRS == nil {
		return "", ""
	}
	srs, err := d.opts.SRS.Forward(m.EnvelopeFrom, domain)
	if err != nil {
		return "", ""
	}
	return srs, ""
}
