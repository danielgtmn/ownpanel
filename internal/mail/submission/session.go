package submission

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/netip"
	"net/textproto"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/deliver"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/queue"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Answers. They go to the person's mail client, which may show them.
var (
	errAuthRequired  = &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "Authentication required"}
	errThrottled     = &smtp.SMTPError{Code: 454, EnhancedCode: smtp.EnhancedCode{4, 7, 0}, Message: "Too many failed sign-ins, try again later"}
	errSignInGone    = &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "Sign-in no longer valid, sign in again"}
	errBlocked       = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "Sending is blocked for this mailbox; ask your administrator"}
	errNullSender    = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "An empty sender address is not allowed here"}
	errSenderDenied  = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "This mailbox may not send as this address"}
	errFromDenied    = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "The From address of the message may not be used by this mailbox"}
	errNoFrom        = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 6, 0}, Message: "The message has no readable From header"}
	errManyFrom      = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 6, 0}, Message: "The message must have exactly one From header"}
	errNeedSender    = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 6, 0}, Message: "A From header with several addresses needs a Sender header"}
	errBadSender     = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 6, 0}, Message: "The Sender header must hold exactly one address"}
	errSenderHdr     = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "The Sender address of the message may not be used by this mailbox"}
	errHeaderTooBig  = &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "Message header too large"}
	errRateLimited   = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "Sending limit reached, try again later"}
	errBadRcpt       = &smtp.SMTPError{Code: 553, EnhancedCode: smtp.EnhancedCode{5, 1, 3}, Message: "Recipient address not valid"}
	errUnknownRcpt   = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such recipient here"}
	errNoPostmaster  = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Postmaster mailbox not set up yet, try again later"}
	errAllFull       = &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 2, 2}, Message: "Mailbox full, try again later"}
	errDiskFull      = &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 3, 1}, Message: "Insufficient system storage, try again later"}
	errShutdown      = &smtp.SMTPError{Code: 421, EnhancedCode: smtp.EnhancedCode{4, 3, 2}, Message: "Service shutting down, try again later"}
	errLineTooLong   = &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "Line too long"}
	errLocal         = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Local error, try again later"}
	errNoRecipients  = &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 5, 1}, Message: "No valid recipients"}
	errSenderAddress = &smtp.SMTPError{Code: 553, EnhancedCode: smtp.EnhancedCode{5, 1, 7}, Message: "Sender address not valid"}
)

type session struct {
	s    *Server
	conn *smtp.Conn
	ip   netip.Addr
	box  *signin.Grant // signed-in mailbox (a copy), nil before AUTH

	// One transaction:
	dir      *directory.Directory
	from     string
	local    []*config.Mailbox
	rcptOf   map[string]string // storage id → the first recipient address that reached it
	external []string
}

func newSession(s *Server, c *smtp.Conn) *session {
	ap, _ := netip.ParseAddrPort(c.Conn().RemoteAddr().String())
	return &session{s: s, conn: c, ip: ap.Addr().Unmap()}
}

func (ss *session) Reset() {
	ss.dir, ss.from = nil, ""
	ss.local, ss.external, ss.rcptOf = nil, nil, nil
}

func (ss *session) Logout() error { return nil }

// AuthMechanisms are offered only over TLS (go-smtp checks that, since
// AllowInsecureAuth stays false).
func (ss *session) AuthMechanisms() []string { return []string{sasl.Plain, sasl.Login} }

func (ss *session) Auth(mech string) (sasl.Server, error) {
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(identity, user, pass string) error {
			if identity != "" && !strings.EqualFold(identity, user) {
				return smtp.ErrAuthFailed
			}
			return ss.signIn(user, pass)
		}), nil
	case sasl.Login:
		return &loginServer{auth: ss.signIn}, nil
	}
	return nil, smtp.ErrAuthUnknownMechanism
}

func (ss *session) signIn(user, pass string) error {
	m, err := ss.s.opts.SignIn.Check(ss.s.opts.Directory(), ss.ip, user, pass)
	if errors.Is(err, signin.ErrThrottled) {
		return errThrottled
	}
	if err != nil {
		return smtp.ErrAuthFailed
	}
	ss.box = &m
	if ss.s.opts.Limits != nil {
		ss.s.opts.Limits.SignedIn(context.Background(), m.Mailbox, ss.ip)
	}
	return nil
}

func (ss *session) Mail(from string, _ *smtp.MailOptions) error {
	if ss.s.smtp.Stopping() {
		return errShutdown
	}
	ss.Reset()
	if ss.box == nil {
		return errAuthRequired
	}
	dir := ss.s.opts.Directory()
	if !signin.Valid(dir, *ss.box) {
		ss.box = nil
		return errSignInGone
	}
	ctx := context.Background()
	b, err := ss.s.opts.Store.Blocked(ctx, ss.box.StorageID)
	if err != nil {
		ss.s.log.Error("submission: block state", "err", err)
		return errLocal
	}
	if b.Blocked {
		return errBlocked
	}
	if from == "" {
		return errNullSender
	}
	addr := directory.Normalize(from)
	if !config.ValidAddress(addr) {
		return errSenderAddress
	}
	if !dir.MaySendAs(&ss.box.Mailbox, addr) {
		return errSenderDenied
	}
	if ss.s.opts.Limits != nil {
		if err := ss.s.opts.Limits.Check(ctx, dir, ss.box.Mailbox); err != nil {
			return err
		}
	}
	ss.dir, ss.from = dir, addr
	return nil
}

func (ss *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if ss.dir == nil {
		return errLocal
	}
	addr := directory.Normalize(to)
	local, dom, ok := config.SplitAddress(addr)
	if !ok || !config.ValidDomain(dom) || len(addr) > 254 || !validLocal(local) {
		return errBadRcpt
	}
	_, isLocal := ss.dir.Domain(dom)
	if !isLocal && dom != ss.dir.Hostname() {
		if !slices.Contains(ss.external, addr) {
			ss.external = append(ss.external, addr)
		}
		return nil
	}
	route, out := ss.dir.Resolve(addr)
	switch out {
	case directory.Unknown:
		return errUnknownRcpt
	case directory.NoPostmaster:
		return errNoPostmaster
	}
	for _, m := range route.Mailboxes {
		if !slices.ContainsFunc(ss.local, func(b *config.Mailbox) bool { return b.StorageID == m.StorageID }) {
			ss.local = append(ss.local, m)
			if ss.rcptOf == nil {
				ss.rcptOf = map[string]string{}
			}
			ss.rcptOf[m.StorageID] = addr
		}
	}
	// External targets of local aliases go out with the rest: the sender
	// is a mailbox of ours, so no SRS is needed, and the message counts
	// once against the mailbox's and its domain's limits like any other.
	for _, t := range route.External {
		if !slices.Contains(ss.external, t) {
			ss.external = append(ss.external, t)
		}
	}
	return nil
}

// validLocal accepts printable ASCII local parts without spaces (quoted
// local parts with spaces and UTF-8 addresses are not supported).
func validLocal(l string) bool {
	if l == "" || len(l) > 64 {
		return false
	}
	for i := 0; i < len(l); i++ {
		if c := l[i]; c <= 0x20 || c >= 0x7f || c == '<' || c == '>' || c == '\\' {
			return false
		}
	}
	return true
}

func (ss *session) checkDisk() error {
	free, err := ss.s.opts.FreeBytes()
	if err != nil {
		ss.s.log.Error("submission: free disk space", "err", err)
		return errLocal
	}
	if free < ss.s.opts.MinFreeBytes {
		return errDiskFull
	}
	return nil
}

func (ss *session) Data(r io.Reader) error {
	if ss.dir == nil || ss.box == nil {
		return errLocal
	}
	if len(ss.local) == 0 && len(ss.external) == 0 {
		return errNoRecipients
	}
	ctx := context.Background()
	store := ss.s.opts.Store
	f, err := os.CreateTemp(store.TempDir(), "submit-*")
	if err != nil {
		ss.s.log.Error("submission: spool", "err", err)
		return errLocal
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()
	bw := bufio.NewWriter(f)
	_, err = io.Copy(&crlfWriter{w: bw}, r)
	if err == nil {
		err = bw.Flush()
	}
	if err != nil {
		var se *smtp.SMTPError
		if errors.As(err, &se) {
			return se // message too large
		}
		if errors.Is(err, smtp.ErrTooLongLine) {
			return errLineTooLong
		}
		return errLocal
	}
	if err := ss.checkDisk(); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return errLocal
	}
	hdr, rawHeader, err := readHeader(f)
	if errors.Is(err, errHeaderTooLarge) {
		return errHeaderTooBig
	}
	if err != nil {
		return errNoFrom
	}
	fromDomain, err := ss.checkFrom(hdr)
	if err != nil {
		return err
	}
	if ss.s.opts.Limits != nil {
		if err := ss.s.opts.Limits.Reserve(ctx, ss.dir, ss.box.Mailbox); err != nil {
			return err
		}
	}

	prefix := ss.addedHeaders(hdr)
	body := func() (io.Reader, error) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		return io.MultiReader(bytes.NewReader(prefix), f), nil
	}
	sig := ss.sign(fromDomain, body)
	message := func() (io.Reader, error) {
		b, err := body()
		if err != nil {
			return nil, err
		}
		return io.MultiReader(strings.NewReader(sig), b), nil
	}

	// Local recipients first: if the queue then fails, the client tries
	// again and they get the message twice, which is better than outside
	// recipients getting it twice.
	var full []queue.Failure
	failed := 0
	// Mail between mailboxes runs the recipients' filters (and
	// out-of-office replies) like mail from outside; it comes from a
	// signed-in mailbox, so it passed every check.
	dm := &deliver.Message{Dir: ss.dir, Open: message, Size: ss.size(f, prefix, sig), EnvelopeFrom: ss.from, SenderVerified: true}
	for _, m := range ss.local {
		err := ss.s.opts.Deliver.Deliver(ctx, dm, deliver.Recipient{Mailbox: m, Rcpt: ss.rcptOf[m.StorageID]})
		switch {
		case err == nil:
		case errors.Is(err, storage.ErrOverQuota):
			full = append(full, queue.Failure{Recipient: m.Address, Status: "5.2.2", Diagnostic: "552 5.2.2 the mailbox of the recipient is full"})
		default:
			failed++
			ss.s.log.Error("submission: store message", "storage_id", m.StorageID, "err", err)
		}
	}
	if failed > 0 {
		return errLocal
	}
	if len(full) == len(ss.local) && len(ss.local) > 0 && len(ss.external) == 0 {
		return errAllFull
	}
	if len(ss.external) > 0 {
		msg, err := message()
		if err != nil {
			return errLocal
		}
		if err := ss.s.opts.Queue.Enqueue(ctx, msg, ss.from, ss.box.StorageID, ss.external); err != nil {
			ss.s.log.Error("submission: enqueue", "err", err)
			return errLocal
		}
	}
	if len(full) > 0 {
		ss.s.opts.Queue.Bounce(ctx, ss.from, ss.box.StorageID, rawHeader, full)
	}
	ss.s.log.Info("submission: message accepted", "storage_id", ss.box.StorageID,
		"local", len(ss.local)-len(full), "external", len(ss.external), "signed", sig != "")
	return nil
}

// size is the size of the message as stored.
func (ss *session) size(f *os.File, prefix []byte, sig string) int64 {
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	return st.Size() + int64(len(prefix)+len(sig))
}

// checkFrom checks the header From (exactly one field) and, when present,
// Sender (exactly one field with one address) against the sign-in, as RFC
// 5322 3.6.2 wants them: several From addresses need a Sender. Every
// address must be one the mailbox may send as. It returns the domain of the
// first From address (the one that signs).
func (ss *session) checkFrom(hdr textproto.MIMEHeader) (string, error) {
	values := hdr.Values("From")
	if len(values) == 0 {
		return "", errNoFrom
	}
	if len(values) > 1 {
		return "", errManyFrom
	}
	list, err := (&mail.AddressParser{}).ParseList(values[0])
	if err != nil || len(list) == 0 {
		return "", errNoFrom
	}
	senders := hdr.Values("Sender")
	switch {
	case len(senders) > 1:
		return "", errBadSender
	case len(senders) == 1:
		sl, err := (&mail.AddressParser{}).ParseList(senders[0])
		if err != nil || len(sl) != 1 {
			return "", errBadSender
		}
		if !ss.dir.MaySendAs(&ss.box.Mailbox, directory.Normalize(sl[0].Address)) {
			return "", errSenderHdr
		}
	case len(list) > 1:
		return "", errNeedSender
	}
	domain := ""
	for _, a := range list {
		addr := directory.Normalize(a.Address)
		if !ss.dir.MaySendAs(&ss.box.Mailbox, addr) {
			return "", errFromDenied
		}
		if domain == "" {
			_, domain, _ = config.SplitAddress(addr)
		}
	}
	return domain, nil
}

// addedHeaders are the Received header and, when missing, Message-ID and
// Date.
func (ss *session) addedHeaders(hdr textproto.MIMEHeader) []byte {
	var b bytes.Buffer
	id := make([]byte, 8)
	rand.Read(id)
	now := ss.s.opts.Now()
	with := "ESMTPSA"
	if st, ok := ss.conn.TLSConnectionState(); ok {
		with += " (" + tlsVersion(st.Version) + ")"
	}
	fmt.Fprintf(&b, "Received: from [%s] (authenticated)\r\n\tby %s (ownpanel) with %s id %s;\r\n\t%s\r\n",
		ss.ip, ss.dir.Hostname(), with, hex.EncodeToString(id), now.Format(time.RFC1123Z))
	if hdr.Get("Message-Id") == "" {
		mid := make([]byte, 16)
		rand.Read(mid)
		fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(mid), ss.dir.Hostname())
	}
	if hdr.Get("Date") == "" {
		fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	}
	return b.Bytes()
}

// sign returns the DKIM-Signature header for the message, or "" when the
// domain has no key or signing failed (the message goes out unsigned then;
// signing is a matter of deliverability, not of accepting mail).
func (ss *session) sign(domain string, body func() (io.Reader, error)) string {
	d, ok := ss.dir.Domain(domain)
	if !ok {
		return ""
	}
	r, err := body()
	if err != nil {
		return ""
	}
	sig, err := ss.s.opts.Signer.Signature(d, r)
	if errors.Is(err, auth.ErrNoKey) {
		return ""
	}
	if err != nil {
		ss.s.log.Error("submission: DKIM signing failed", "domain", domain, "err", err)
		return ""
	}
	return sig
}

func tlsVersion(v uint16) string {
	switch v {
	case 0x0304:
		return "TLS1.3"
	case 0x0303:
		return "TLS1.2"
	}
	return "TLS"
}

// maxHeaderBytes bounds the header section of a submitted message; larger
// ones are refused, so the From check always sees the whole header.
const maxHeaderBytes = 1 << 20

var errHeaderTooLarge = errors.New("header too large")

// readHeader parses the header of the message in r and returns it with its
// raw bytes. A header section longer than maxHeaderBytes is
// errHeaderTooLarge. r has CRLF line endings (crlfWriter).
func readHeader(r io.Reader) (textproto.MIMEHeader, []byte, error) {
	var raw bytes.Buffer
	br := bufio.NewReader(io.TeeReader(io.LimitReader(r, maxHeaderBytes+1), &raw))
	h, err := textproto.NewReader(br).ReadMIMEHeader()
	b := raw.Bytes()
	end := bytes.Index(b, []byte("\r\n\r\n"))
	if end+4 > maxHeaderBytes || end < 0 && len(b) > maxHeaderBytes {
		return nil, nil, errHeaderTooLarge
	}
	if err != nil && !(errors.Is(err, io.EOF) && len(h) > 0) {
		return nil, nil, err
	}
	if end >= 0 {
		b = b[:end+2]
	}
	return h, b, nil
}

// crlfWriter turns bare LF into CRLF, so the stored and signed message has
// the line endings it will have on the wire.
type crlfWriter struct {
	w      io.Writer
	lastCR bool
}

func (c *crlfWriter) Write(p []byte) (int, error) {
	var out []byte
	start := 0
	for i, b := range p {
		if b == '\n' && !(i == 0 && c.lastCR || i > 0 && p[i-1] == '\r') {
			if out == nil {
				out = make([]byte, 0, len(p)+16)
			}
			out = append(out, p[start:i]...)
			out = append(out, '\r', '\n')
			start = i + 1
		}
	}
	if len(p) > 0 {
		c.lastCR = p[len(p)-1] == '\r'
	}
	if out == nil {
		_, err := c.w.Write(p)
		return len(p), err
	}
	out = append(out, p[start:]...)
	_, err := c.w.Write(out)
	return len(p), err
}
