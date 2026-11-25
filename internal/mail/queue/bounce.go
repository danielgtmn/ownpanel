package queue

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Failure is a recipient a message could not be delivered to.
type Failure struct {
	Recipient string
	// Status is the enhanced status code, e.g. "5.1.1".
	Status string
	// Diagnostic is the reply of the receiving server or what went wrong.
	Diagnostic string
}

// maxOriginalHeader bounds the copy of the original header in a bounce.
const maxOriginalHeader = 64 << 10

func (q *Queue) bounce(ctx context.Context, m storage.QueuedMessage, failed []Failure) {
	var header []byte
	if f, err := q.opts.Store.OpenQueued(m); err == nil {
		header = readHeader(f)
		f.Close()
	}
	q.Bounce(ctx, m.Sender, m.StorageID, header, failed)
}

// Bounce delivers a delivery status notification about failed recipients
// into the INBOX of the sender's local mailbox and records an event.
// Without a local mailbox, the notification is queued to the address
// Options.ReturnTo gives for the sender (forwarded mail). Nothing is sent
// for the null sender.
// originalHeader is the header of the failed message (may be nil).
func (q *Queue) Bounce(ctx context.Context, sender, storageID string, originalHeader []byte, failed []Failure) {
	if len(failed) == 0 {
		return
	}
	var rcpts []string
	for _, f := range failed {
		rcpts = append(rcpts, f.Recipient)
	}
	detail := truncate(fmt.Sprintf("a message to %s could not be delivered: %s",
		strings.Join(rcpts, ", "), failed[0].Diagnostic), 500)
	if q.opts.Event != nil {
		q.opts.Event(config.Event{Kind: config.EventBounced, StorageID: storageID, Address: sender, Detail: detail})
	}
	q.log.Info("mail queue: bounced", "recipients", len(failed), "status", failed[0].Status)
	if sender == "" {
		return // no bounce for bounces
	}
	dir := q.opts.Directory()
	if storageID == "" {
		// Forwarded mail: the bounce goes back to the original sender,
		// from the null sender (so it is never bounced itself).
		if q.opts.ReturnTo == nil {
			return
		}
		orig, ok := q.opts.ReturnTo(sender)
		if !ok {
			return
		}
		msg := BuildDSN(dir.Hostname(), orig, q.now(), originalHeader, failed)
		if err := q.Enqueue(ctx, bytes.NewReader(msg), "", "", []string{orig}); err != nil {
			q.log.Error("mail queue: enqueue bounce", "err", err)
		}
		return
	}
	if _, ok := dir.MailboxByStorageID(storageID); !ok {
		q.log.Warn("mail queue: bounce dropped, mailbox no longer exists", "storage_id", storageID)
		return
	}
	msg := BuildDSN(dir.Hostname(), sender, q.now(), originalHeader, failed)
	_, err := q.opts.Store.Append(ctx, storageID, storage.Inbox, bytes.NewReader(msg), storage.AppendOptions{})
	if err != nil {
		q.log.Error("mail queue: store bounce", "storage_id", storageID, "err", err)
	}
}

// readHeader reads the header of a message (up to the empty line).
func readHeader(r io.Reader) []byte {
	br := bufio.NewReader(io.LimitReader(r, maxOriginalHeader))
	var out bytes.Buffer
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			break
		}
		out.Write(line)
		if err != nil {
			break
		}
	}
	return out.Bytes()
}

// BuildDSN builds a delivery status notification (RFC 3464, a
// multipart/report) from MAILER-DAEMON@hostname to sender, with a
// Return-Path of the null sender, ready to be stored.
func BuildDSN(hostname, sender string, now time.Time, originalHeader []byte, failed []Failure) []byte {
	rnd := make([]byte, 12)
	rand.Read(rnd)
	boundary := "dsn-" + hex.EncodeToString(rnd)
	var b bytes.Buffer
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("Return-Path: <>\r\n")
	w("From: Mail Delivery System <MAILER-DAEMON@%s>\r\n", hostname)
	w("To: <%s>\r\n", sender)
	w("Subject: Undelivered Mail Returned to Sender\r\n")
	w("Date: %s\r\n", now.Format(time.RFC1123Z))
	w("Message-ID: <%s.dsn@%s>\r\n", hex.EncodeToString(rnd), hostname)
	w("Auto-Submitted: auto-replied\r\n")
	w("MIME-Version: 1.0\r\n")
	w("Content-Type: multipart/report; report-type=delivery-status;\r\n\tboundary=\"%s\"\r\n", boundary)
	w("\r\n")
	w("This is a MIME-encapsulated message.\r\n\r\n")

	w("--%s\r\n", boundary)
	w("Content-Type: text/plain; charset=utf-8\r\n")
	w("Content-Transfer-Encoding: 8bit\r\n\r\n")
	w("This is the mail system at %s.\r\n\r\n", hostname)
	w("Your message could not be delivered to the following recipients:\r\n\r\n")
	for _, f := range failed {
		w("  <%s>: %s\r\n", f.Recipient, oneLine(f.Diagnostic))
	}
	w("\r\n")

	w("--%s\r\n", boundary)
	w("Content-Type: message/delivery-status\r\n\r\n")
	w("Reporting-MTA: dns; %s\r\n", hostname)
	w("Arrival-Date: %s\r\n", now.Format(time.RFC1123Z))
	for _, f := range failed {
		w("\r\n")
		w("Final-Recipient: rfc822; %s\r\n", f.Recipient)
		w("Action: failed\r\n")
		status := f.Status
		if status == "" {
			status = "5.0.0"
		}
		w("Status: %s\r\n", status)
		w("Diagnostic-Code: smtp; %s\r\n", oneLine(f.Diagnostic))
	}
	w("\r\n")

	if len(originalHeader) > 0 {
		w("--%s\r\n", boundary)
		w("Content-Type: text/rfc822-headers\r\n\r\n")
		b.Write(bytes.TrimRight(originalHeader, "\r\n"))
		w("\r\n\r\n")
	}
	w("--%s--\r\n", boundary)
	return b.Bytes()
}
