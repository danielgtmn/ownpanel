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
func (d *Deliverer) checkRedirects(m *Message, box *config.Mailbox, targets []string) (allowed []string, refused bool) {
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

func (d *Deliverer) notForwarded(box *config.Mailbox, targets []string, why string) {
	d.opts.Event(config.Event{Kind: config.EventNotForwarded, StorageID: box.StorageID, Address: box.Address,
		Detail: fmt.Sprintf("a filter of %s did not forward a message to %s: %s", box.Address, strings.Join(targets, ", "), why)})
}

// forward sends the message to the targets: local addresses get it
// directly (their own filters run), outside addresses through the queue.
// It returns whether it went anywhere.
func (d *Deliverer) forward(ctx context.Context, m *Message, box *config.Mailbox, targets []string) bool {
	dir := m.Dir
	prefix := []byte("Delivered-To: " + box.Address + "\r\n" + LoopHeader + ": " + loopMark(dir.Hostname(), box.StorageID) + "\r\n")
	var external []string
	var local []Recipient
	for _, t := range targets {
		_, dom, _ := config.SplitAddress(t)
		if _, ok := dir.Domain(dom); !ok && dom != dir.Hostname() {
			external = append(external, t)
			continue
		}
		route, out := dir.Resolve(t)
		if out == directory.Unknown || out == directory.NoPostmaster {
			d.notForwarded(box, []string{t}, "no such address here")
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
	went := false
	if len(local) > 0 {
		nm := withPrefix(m, prefix)
		for _, r := range local {
			if err := d.Deliver(ctx, nm, r); err != nil {
				d.log.Warn("deliver: forward to a local mailbox failed", "storage_id", r.Mailbox.StorageID, "err", err)
				continue
			}
			went = true
		}
	}
	if len(external) > 0 && d.enqueue(ctx, m, box, prefix, external) {
		went = true
	}
	return went
}

// enqueue sends a forwarded copy outside, counted once against the
// mailbox's domain's hourly limit.
func (d *Deliverer) enqueue(ctx context.Context, m *Message, box *config.Mailbox, prefix []byte, targets []string) bool {
	if d.opts.Queue == nil {
		d.notForwarded(box, targets, "sending outside is not available")
		return false
	}
	_, dom, _ := config.SplitAddress(box.Address)
	max := config.DefaultDomainSendPerHour
	if dd, ok := m.Dir.Domain(dom); ok && dd.SendPerHour > 0 {
		max = dd.SendPerHour
	}
	now := d.opts.Now()
	limits := []storage.SendLimit{{Key: storage.KeyDomainSends + dom, Since: now.Add(-time.Hour), Max: max}}
	i, err := d.opts.Store.ReserveSend(ctx, limits, now)
	if err != nil {
		d.log.Error("deliver: count forward", "err", err)
		limits = nil // send anyway, nothing to take back
	} else if i >= 0 {
		d.notForwarded(box, targets, fmt.Sprintf("the domain %s reached its limit of %d messages per hour", dom, max))
		return false
	}
	sender, storageID := d.forwardSender(m, dom)
	msg, err := m.Open()
	if err == nil {
		err = d.opts.Queue.Enqueue(ctx, io.MultiReader(bytes.NewReader(prefix), msg), sender, storageID, targets)
	}
	if err != nil {
		d.log.Error("deliver: enqueue forward", "err", err)
		if limits != nil {
			d.opts.Store.ReleaseSend(ctx, limits, now)
		}
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
