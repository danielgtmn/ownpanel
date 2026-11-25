package deliver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/header"
	"github.com/danielgtmn/ownpanel/internal/mail/sieve"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Why an out-of-office reply was not sent (for logs and tests).
const (
	skipJunk       = "junk"
	skipAuth       = "authentication failed"
	skipSender     = "sender"
	skipAuto       = "automatic message"
	skipList       = "list or bulk mail"
	skipNotToMe    = "not addressed to the mailbox"
	skipRepliedYet = "replied within the period"
	skipLimit      = "sending limit"
	skipBlocked    = "sending is blocked for the mailbox"
)

// vacationSkip tells why no reply may be sent, or "" if one may (before
// the once-per-period check). RFC 5230 4.5 and RFC 3834 2.
func vacationSkip(m *Message, own []string) string {
	h := m.msg()
	switch {
	case m.Junk:
		return skipJunk
	case m.DMARCFail || m.AuthFail || !m.SenderAuthenticated:
		// Only confirmed senders get a reply: a forged sender of a domain
		// without SPF or DMARC must not get backscatter.
		return skipAuth
	}
	sender := directory.Normalize(m.EnvelopeFrom)
	local, _, ok := config.SplitAddress(sender)
	if !ok || sender == "" {
		return skipSender
	}
	switch {
	case local == "mailer-daemon", local == "listserv", strings.HasPrefix(local, "owner-"),
		strings.HasSuffix(local, "-request"), strings.HasPrefix(local, "srs0="), strings.HasPrefix(local, "srs1="):
		return skipSender
	case slices.Contains(own, sender):
		return skipSender
	}
	for _, v := range h.Header("auto-submitted") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "no" && !strings.HasPrefix(v, "no;") && !strings.HasPrefix(v, "no ") {
			return skipAuto
		}
	}
	for _, v := range h.Header("precedence") {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "bulk", "list", "junk":
			return skipList
		}
	}
	for name := range h.headers() {
		if strings.HasPrefix(name, "list-") {
			return skipList
		}
	}
	// One of the person's addresses must be in To or Cc (not Bcc, not a
	// list it is on).
	for _, field := range []string{"to", "cc"} {
		for _, v := range h.Header(field) {
			list, err := mail.ParseAddressList(v)
			if err != nil {
				continue
			}
			for _, a := range list {
				if slices.Contains(own, directory.Normalize(a.Address)) {
					return ""
				}
			}
		}
	}
	return skipNotToMe
}

// vacation sends the out-of-office reply if the rules allow it.
func (d *Deliverer) vacation(ctx context.Context, m *Message, r Recipient, v *sieve.Vacation) {
	box := r.Mailbox
	own := ownAddresses(m.Dir, box)
	for _, a := range v.Addresses {
		if a = directory.Normalize(a); a != "" && !slices.Contains(own, a) {
			own = append(own, a)
		}
	}
	why := vacationSkip(m, own)
	if why == "" && d.blocked(ctx, box) {
		why = skipBlocked
	}
	if why != "" {
		d.log.Debug("deliver: no out-of-office reply", "storage_id", box.StorageID, "why", why)
		return
	}
	sender := directory.Normalize(m.EnvelopeFrom)
	st := d.opts.Store
	ok, err := st.SieveReplied(ctx, box.StorageID, v.Handle, sender, v.Period, d.opts.Now())
	if err != nil {
		d.log.Error("deliver: out-of-office state", "err", err)
		return
	}
	if !ok {
		d.log.Debug("deliver: no out-of-office reply", "storage_id", box.StorageID, "why", skipRepliedYet)
		return
	}
	if err := d.sendReply(ctx, m, box, v, sender); err != nil {
		d.log.Info("deliver: out-of-office reply not sent", "storage_id", box.StorageID, "err", err)
		st.ForgetSieveReply(ctx, box.StorageID, v.Handle, sender)
		return
	}
	d.log.Info("deliver: out-of-office reply sent", "storage_id", box.StorageID)
}

var errLimit = errors.New(skipLimit)

func (d *Deliverer) sendReply(ctx context.Context, m *Message, box *config.Mailbox, v *sieve.Vacation, to string) (err error) {
	dir := m.Dir
	fromAddr, fromName := box.Address, box.DisplayName
	if v.From != "" {
		if a, err := mail.ParseAddress(v.From); err == nil && dir.MaySendAs(box, directory.Normalize(a.Address)) {
			fromAddr = directory.Normalize(a.Address)
			if a.Name != "" && config.ValidDisplayName(a.Name) {
				fromName = a.Name
			}
		}
	}
	res, ok, _ := d.reserve(ctx, dir, box)
	if !ok {
		return errLimit
	}
	defer func() {
		if err != nil {
			release(ctx, d.opts.Store, res, d.log)
		}
	}()
	h := m.msg()
	subject := v.Subject
	if subject == "" {
		orig := ""
		if s := h.Header("subject"); len(s) > 0 {
			orig = decodeHeader(strings.TrimSpace(s[0]))
		}
		subject = "Auto: " + orig
	}
	msg, err := buildReply(dir.Hostname(), fromAddr, fromName, to, subject, v, h, d.opts.Now())
	if err != nil {
		return err
	}
	_, fromDomain, _ := config.SplitAddress(fromAddr)
	if dd, ok := dir.Domain(fromDomain); ok && d.opts.Signer != nil {
		sig, err := d.opts.Signer.Signature(dd, bytes.NewReader(msg))
		switch {
		case err == nil:
			msg = append([]byte(sig), msg...)
		case !errors.Is(err, auth.ErrNoKey):
			d.log.Error("deliver: DKIM signing failed", "domain", fromDomain, "err", err)
		}
	}
	// To a mailbox here: straight into its INBOX (its filters do not run;
	// an automatic reply never triggers another one).
	_, toDomain, _ := config.SplitAddress(to)
	if _, local := dir.Domain(toDomain); local || toDomain == dir.Hostname() {
		route, out := dir.Resolve(to)
		if out == directory.Deliver {
			for _, mb := range route.Mailboxes {
				if _, err := d.opts.Store.Append(ctx, mb.StorageID, storage.Inbox,
					bytes.NewReader(append([]byte("Return-Path: <>\r\n"), msg...)), storage.AppendOptions{}); err != nil {
					d.log.Warn("deliver: store out-of-office reply", "err", err)
				}
			}
		}
		if len(route.External) == 0 {
			return nil
		}
		to = route.External[0]
		if len(route.External) > 1 {
			return d.enqueueReply(ctx, msg, route.External)
		}
	}
	return d.enqueueReply(ctx, msg, []string{to})
}

func (d *Deliverer) enqueueReply(ctx context.Context, msg []byte, rcpts []string) error {
	if d.opts.Queue == nil {
		return errors.New("sending outside is not available")
	}
	// The null sender: replies to replies are never sent (RFC 3834 3.3).
	return d.opts.Queue.Enqueue(ctx, bytes.NewReader(msg), "", "", rcpts)
}

// buildReply writes the reply (RFC 3834 3).
func buildReply(host, from, fromName, to, subject string, v *sieve.Vacation, h *parsed, now time.Time) ([]byte, error) {
	var b bytes.Buffer
	id := make([]byte, 16)
	rand.Read(id)
	fromHdr := "<" + from + ">"
	if fromName != "" {
		fromHdr = mime.QEncoding.Encode("utf-8", fromName) + " " + fromHdr
		if isPlain(fromName) {
			fromHdr = `"` + fromName + `" <` + from + ">"
		}
	}
	fmt.Fprintf(&b, "From: %s\r\n", fromHdr)
	fmt.Fprintf(&b, "To: <%s>\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", encodeHeader(oneLine(subject)))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(id), host)
	b.WriteString("Auto-Submitted: auto-replied\r\n")
	if ids := h.Header("message-id"); len(ids) > 0 {
		mid := strings.TrimSpace(ids[0])
		if validMsgID(mid) {
			fmt.Fprintf(&b, "In-Reply-To: %s\r\n", mid)
			refs := ""
			if r := h.Header("references"); len(r) > 0 && len(r[0]) < 900 && !strings.ContainsAny(r[0], "\r\n") {
				refs = strings.TrimSpace(r[0]) + " "
			}
			fmt.Fprintf(&b, "References: %s%s\r\n", refs, mid)
		}
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	if v.MIME {
		if entity, ok := mimeReason(v.Reason); ok {
			b.WriteString(entity)
			return b.Bytes(), nil
		}
		// Not a MIME entity after all: sent as text.
	}
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	qp := quotedprintable.NewWriter(&b)
	text := strings.ReplaceAll(strings.ReplaceAll(v.Reason, "\r\n", "\n"), "\n", "\r\n")
	if _, err := qp.Write([]byte(text)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	b.WriteString("\r\n")
	return b.Bytes(), nil
}

// mimeReason rebuilds the reason of "vacation :mime" (a MIME entity: header
// fields, an empty line, the body). Only Content-* fields are kept, one
// line each, so a script cannot add From, Sender, Bcc or Reply-To to a
// reply that is signed for the domain. ok is false without the empty line.
func mimeReason(reason string) (string, bool) {
	text := strings.ReplaceAll(reason, "\r\n", "\n")
	head, body, found := strings.Cut(text, "\n\n")
	if !found {
		return "", false
	}
	fields, _, _, err := header.Read(strings.NewReader(head+"\n\n"), int64(len(head))+2)
	if err != nil {
		return "", false
	}
	var b strings.Builder
	hasType := false
	for _, f := range fields {
		name := strings.ToLower(f.Name)
		if !strings.HasPrefix(name, "content-") || !isToken(f.Name) {
			continue
		}
		v := oneLine(f.Value())
		if v == "" {
			continue
		}
		hasType = hasType || name == "content-type"
		b.WriteString(f.Name + ": " + v + "\r\n")
	}
	if !hasType {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\r\n")
	}
	return b.String(), true
}

// isToken: a field name of printable ASCII without colon or blanks.
func isToken(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= ' ' || c >= 0x7f || c == ':' {
			return false
		}
	}
	return s != ""
}

func isPlain(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

func encodeHeader(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x7f || s[i] < 0x20 {
			return mime.QEncoding.Encode("utf-8", s)
		}
	}
	return s
}

func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300])
	}
	return s
}

// validMsgID accepts "<…>" without spaces or line breaks.
func validMsgID(s string) bool {
	if len(s) < 3 || len(s) > 250 || s[0] != '<' || s[len(s)-1] != '>' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= 0x20 || c >= 0x7f {
			return false
		}
	}
	return true
}
