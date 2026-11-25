package smtpd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Answers. Texts go to the sending server and may end up in bounces, so
// they are plain and name no internals.
var (
	errUnknownRcpt = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such recipient here"}
	errForwardOnly = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "This address forwards elsewhere; forwarding is not available yet"}
	errNoPostmstr  = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Postmaster mailbox not set up yet, try again later"}
	errMailboxFull = &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 2, 2}, Message: "Mailbox full, try again later"}
	errDiskFull    = &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 3, 1}, Message: "Insufficient system storage, try again later"}
	errShutdown    = &smtp.SMTPError{Code: 421, EnhancedCode: smtp.EnhancedCode{4, 3, 2}, Message: "Service shutting down, try again later"}
	errLineTooLong = &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "Line too long"}
	errLocal       = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Local error, try again later"}
)

type session struct {
	s      *Server
	conn   *smtp.Conn
	remote string
	tls    bool
	tlsVer string

	dir   *directory.Directory
	from  string
	boxes []*config.Mailbox // deduplicated over all recipients
	notes []config.Event    // recorded once the message is accepted
}

func (s *Server) newSession(c *smtp.Conn) (smtp.Session, error) {
	sess := &session{s: s, conn: c, remote: hostOf(c.Conn().RemoteAddr())}
	if st, ok := c.TLSConnectionState(); ok {
		sess.tls = true
		sess.tlsVer = tls.VersionName(st.Version)
	}
	return sess, nil
}

func (ss *session) Reset() {
	ss.dir = nil
	ss.from = ""
	ss.boxes = nil
	ss.notes = nil
}

func (ss *session) Logout() error { return nil }

func (ss *session) Mail(from string, opts *smtp.MailOptions) error {
	if ss.s.stopping.Load() {
		return errShutdown
	}
	ss.Reset()
	// One configuration for the whole transaction.
	ss.dir = ss.s.opts.Directory()
	ss.from = from
	return nil
}

func (ss *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if ss.dir == nil {
		return errLocal
	}
	if err := ss.checkDisk(); err != nil {
		return err
	}
	addr := directory.Normalize(to)
	route, out := ss.dir.Resolve(addr)
	switch out {
	case directory.Unknown:
		return errUnknownRcpt
	case directory.ForwardOnly:
		return errForwardOnly
	case directory.NoPostmaster:
		return errNoPostmstr
	}
	// Refuse at once when every mailbox behind the address is full.
	var room []*config.Mailbox
	for _, m := range route.Mailboxes {
		full, err := ss.full(m, 0)
		if err != nil {
			ss.s.log.Error("smtp: usage", "err", err)
			return errLocal
		}
		if !full {
			room = append(room, m)
		}
	}
	if len(room) == 0 {
		return errMailboxFull
	}
	for _, m := range room {
		if !containsBox(ss.boxes, m) {
			ss.boxes = append(ss.boxes, m)
		}
	}
	for _, t := range route.External {
		ss.notes = append(ss.notes, config.Event{Kind: config.EventBounced, Address: addr,
			Detail: fmt.Sprintf("message to %s was not forwarded to %s: forwarding is not available yet", addr, t)})
	}
	return nil
}

func containsBox(list []*config.Mailbox, m *config.Mailbox) bool {
	for _, b := range list {
		if b.StorageID == m.StorageID {
			return true
		}
	}
	return false
}

// full tells whether adding size bytes would exceed the mailbox's quota
// (size 0: whether it is already full).
func (ss *session) full(m *config.Mailbox, size int64) (bool, error) {
	if m.QuotaBytes <= 0 {
		return false, nil
	}
	u, err := ss.s.opts.Store.Usage(context.Background(), m.StorageID)
	if err != nil {
		return false, err
	}
	if size == 0 {
		return u.Bytes >= m.QuotaBytes, nil
	}
	return u.Bytes+size > m.QuotaBytes, nil
}

func (ss *session) checkDisk() error {
	free, err := ss.s.opts.FreeBytes()
	if err != nil {
		ss.s.log.Error("smtp: free disk space", "err", err)
		return errLocal
	}
	if free < ss.s.opts.MinFreeBytes {
		return errDiskFull
	}
	return nil
}

func (ss *session) Data(r io.Reader) error {
	if ss.dir == nil || len(ss.boxes) == 0 {
		return errLocal
	}
	store := ss.s.opts.Store
	f, err := os.CreateTemp(store.TempDir(), "smtp-*")
	if err != nil {
		ss.s.log.Error("smtp: spool", "err", err)
		return errLocal
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()
	size, err := io.Copy(f, r)
	if err != nil {
		var se *smtp.SMTPError
		if errors.As(err, &se) {
			return se // message too large
		}
		if errors.Is(err, smtp.ErrTooLongLine) {
			return errLineTooLong
		}
		// A broken connection or full temp disk: never a permanent answer.
		return errLocal
	}
	if err := ss.checkDisk(); err != nil {
		return err
	}
	header := ss.traceHeader()
	total := int64(len(header)) + size
	ctx := context.Background()
	delivered, full, failed := 0, 0, 0
	for _, m := range ss.boxes {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return errLocal
		}
		_, err := store.Append(ctx, m.StorageID, storage.Inbox, io.MultiReader(bytes.NewReader(header), f), storage.AppendOptions{})
		switch {
		case err == nil:
			delivered++
		case errors.Is(err, storage.ErrOverQuota):
			full++
			ss.notes = append(ss.notes, config.Event{Kind: config.EventBounced, StorageID: m.StorageID, Address: m.Address,
				Detail: fmt.Sprintf("a message of %d bytes was not stored: the mailbox is full", total)})
		default:
			failed++
			ss.s.log.Error("smtp: store message", "storage_id", m.StorageID, "err", err)
		}
	}
	// A mailbox that failed for another reason than being full must not lose
	// the message: answer with a temporary error so the sender tries again.
	// The mailboxes that already have it get it twice, which is better than
	// never.
	if failed > 0 {
		return errLocal
	}
	if delivered == 0 {
		if full == len(ss.boxes) {
			return errMailboxFull
		}
		return errLocal
	}
	for _, e := range ss.notes {
		if err := store.AddEvent(ctx, e); err != nil {
			ss.s.log.Error("smtp: record event", "err", err)
		}
	}
	ss.s.log.Info("smtp: message received", "remote", ss.remote, "bytes", total, "mailboxes", delivered, "tls", ss.tls)
	return nil
}

// traceHeader is the Return-Path and Received header added on delivery.
func (ss *session) traceHeader() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "Return-Path: <%s>\r\n", sanitize(ss.from))
	helo := sanitize(ss.conn.Hostname())
	if helo == "" {
		helo = "unknown"
	}
	with := "ESMTP"
	tlsNote := ""
	if ss.tls {
		with = "ESMTPS"
		tlsNote = " (" + ss.tlsVer + ")"
	}
	id := make([]byte, 8)
	rand.Read(id)
	fmt.Fprintf(&b, "Received: from %s ([%s])\r\n\tby %s (ownpanel) with %s%s id %s;\r\n\t%s\r\n",
		helo, ss.remote, ss.dir.Hostname(), with, tlsNote, hex.EncodeToString(id),
		time.Now().Format(time.RFC1123Z))
	return []byte(b.String())
}

// sanitize keeps printable ASCII without spaces or angle brackets, so
// client-supplied names cannot break the header.
func sanitize(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < 255; i++ {
		c := s[i]
		if c > 0x20 && c < 0x7f && c != '<' && c != '>' && c != '(' && c != ')' {
			b.WriteByte(c)
		}
	}
	return b.String()
}
