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
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/filter"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Answers. Texts go to the sending server and may end up in bounces, so
// they are plain and name no internals.
var (
	errUnknownRcpt    = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such recipient here"}
	errForwardOnly    = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "This address forwards elsewhere; forwarding is not available"}
	errNoPostmstr     = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Postmaster mailbox not set up yet, try again later"}
	errMailboxFull    = &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 2, 2}, Message: "Mailbox full, try again later"}
	errDiskFull       = &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 3, 1}, Message: "Insufficient system storage, try again later"}
	errShutdown       = &smtp.SMTPError{Code: 421, EnhancedCode: smtp.EnhancedCode{4, 3, 2}, Message: "Service shutting down, try again later"}
	errLineTooLong    = &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "Line too long"}
	errHeaderTooBig   = &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "Message header too large"}
	errLocal          = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Local error, try again later"}
	errGreylisted     = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "Greylisted, please try again in a few minutes"}
	errReportsOnly    = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "This address only accepts delivery status notifications"}
	errForwardLimited = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "Forwarding limit reached, try again later"}
	errOwnDomain      = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "Sender address of a domain hosted here, but not sent by its servers"}
	errBadFrom        = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 6, 0}, Message: "The message has no readable From header"}
)

func errListed(zone string) error {
	return &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 7, 1},
		Message: "Your server's address is listed on " + zone + "; mail from it is refused"}
}

func errDMARCReject(domain string) error {
	return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1},
		Message: "Message refused by the DMARC policy of " + sanitize(domain)}
}

func errAttachment(name string) error {
	return &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 7, 1},
		Message: "Attachments of this type are not accepted (" + printable(name, 80) + ")"}
}

// forward is mail for external targets reached through the aliases of one
// of our domains.
type forward struct {
	domain  string   // the alias domain: the SRS address is in it
	alias   string   // first alias address (for events)
	targets []string // deduplicated
	// res is the reservation against the domain's sending limit while the
	// message is not queued yet.
	res *reservation
}

// reservation is one message counted against sending limits at a time;
// release takes it back.
type reservation struct {
	limits []storage.SendLimit
	at     time.Time
}

type session struct {
	s      *Server
	conn   *smtp.Conn
	remote string
	ip     netip.Addr
	tls    bool
	tlsVer string

	// Per connection, looked up on first use.
	blDone   bool
	bl       filter.BlocklistResult
	ptrDone  bool
	hasPTR   bool
	ptrKnown bool

	// One transaction:
	dir         *directory.Directory
	from        string
	spfDone     bool
	spf         auth.SPFResult
	boxes       []*config.Mailbox // deduplicated over all recipients
	rcptDomains []string          // our domains among the recipients
	forwards    []*forward
	reports     []string       // original senders of bounces to SRS addresses
	reportDoms  []string       // the domains of those SRS addresses
	notes       []config.Event // recorded once the message is accepted
}

func (s *Server) newSession(c *smtp.Conn) (smtp.Session, error) {
	ra := c.Conn().RemoteAddr()
	sess := &session{s: s, conn: c, remote: hostOf(ra), ip: s.opts.ClientIP(ra)}
	if sess.ip.IsValid() {
		sess.remote = sess.ip.String()
	}
	if st, ok := c.TLSConnectionState(); ok {
		sess.tls = true
		sess.tlsVer = tls.VersionName(st.Version)
	}
	return sess, nil
}

func (ss *session) Reset() {
	ss.dir = nil
	ss.from = ""
	ss.spfDone = false
	ss.spf = auth.SPFResult{}
	ss.boxes = nil
	ss.rcptDomains = nil
	ss.forwards = nil
	ss.reports = nil
	ss.reportDoms = nil
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
	ss.blocklists()
	return nil
}

// blocklists looks the client up once per connection.
func (ss *session) blocklists() {
	if ss.blDone {
		return
	}
	ss.blDone = true
	if !filter.Checkable(ss.ip) {
		return
	}
	zones := ss.dir.Config().Filter.WithDefaults().Blocklists
	ss.bl = ss.s.opts.Filter.Blocklists(context.Background(), ss.ip, zones)
	if ss.bl.Refuse != "" {
		ss.s.log.Info("smtp: client listed", "remote", ss.remote, "zone", ss.bl.Refuse)
	}
}

// ptr looks up the client's reverse DNS name once per connection.
func (ss *session) ptr() (has, known bool) {
	if !ss.ptrDone {
		ss.ptrDone = true
		if filter.Checkable(ss.ip) {
			ss.hasPTR, ss.ptrKnown = ss.s.opts.Filter.PTR(context.Background(), ss.ip)
		} else {
			ss.hasPTR, ss.ptrKnown = true, true
		}
	}
	return ss.hasPTR, ss.ptrKnown
}

// spfResult evaluates SPF once per transaction.
func (ss *session) spfResult() auth.SPFResult {
	if !ss.spfDone {
		ss.spfDone = true
		if ss.ip.IsValid() {
			v := auth.Verifier{Resolver: ss.s.opts.Filter.Resolver()}
			ss.spf = v.SPF(context.Background(), ss.ip, ss.conn.Hostname(), ss.from)
		} else {
			ss.spf = auth.SPFResult{Result: auth.None}
		}
	}
	return ss.spf
}

// ours tells whether d is one of our domains or the host name.
func (ss *session) ours(d string) bool {
	_, ok := ss.dir.Domain(d)
	return ok || d == ss.dir.Hostname()
}

func (ss *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if ss.dir == nil {
		return errLocal
	}
	if err := ss.checkDisk(); err != nil {
		return err
	}
	if ss.bl.Refuse != "" {
		return errListed(ss.bl.Refuse)
	}
	addr := directory.Normalize(to)
	local, dom, ok := config.SplitAddress(addr)
	if ok && auth.IsSRS(local) && ss.ours(dom) {
		return ss.rcptSRS(addr, dom)
	}
	route, out := ss.dir.Resolve(addr)
	switch out {
	case directory.Unknown:
		return errUnknownRcpt
	case directory.NoPostmaster:
		return errNoPostmstr
	case directory.ForwardOnly:
		if ss.s.opts.Queue == nil {
			return errForwardOnly
		}
	}
	if err := ss.greylist(addr); err != nil {
		return err
	}
	// Refuse at once when every mailbox behind the address is full and
	// nothing goes elsewhere.
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
	external := route.External
	if ss.s.opts.Queue == nil {
		for _, t := range external {
			ss.notes = append(ss.notes, config.Event{Kind: config.EventNotForwarded, Address: addr,
				Detail: fmt.Sprintf("a message to %s was not forwarded to %s: forwarding is not available", addr, t)})
		}
		external = nil
	}
	if len(room) == 0 && len(external) == 0 {
		return errMailboxFull
	}
	for _, m := range room {
		if !containsBox(ss.boxes, m) {
			ss.boxes = append(ss.boxes, m)
		}
	}
	if len(route.Mailboxes) > 0 && !slices.Contains(ss.rcptDomains, dom) {
		ss.rcptDomains = append(ss.rcptDomains, dom)
	}
	if len(external) > 0 {
		var fw *forward
		for _, f := range ss.forwards {
			if f.domain == dom {
				fw = f
			}
		}
		if fw == nil {
			fw = &forward{domain: dom, alias: addr}
			ss.forwards = append(ss.forwards, fw)
		}
		for _, t := range external {
			if !slices.Contains(fw.targets, t) {
				fw.targets = append(fw.targets, t)
			}
		}
	}
	return nil
}

// rcptSRS accepts a bounce to an SRS address of ours whose signature
// checks out and that is not too old; it goes back to the original
// sender.
func (ss *session) rcptSRS(addr, dom string) error {
	if ss.s.opts.Queue == nil {
		return errUnknownRcpt
	}
	orig, err := ss.s.opts.SRS.Reverse(addr)
	if err != nil || !config.ValidAddress(directory.Normalize(orig)) {
		return errUnknownRcpt
	}
	if ss.from != "" {
		return errReportsOnly
	}
	if err := ss.greylist(addr); err != nil {
		return err
	}
	if !slices.Contains(ss.reports, orig) {
		ss.reports = append(ss.reports, orig)
	}
	if !slices.Contains(ss.reportDoms, dom) {
		ss.reportDoms = append(ss.reportDoms, dom)
	}
	return nil
}

// greylist delays the first message of a (network, sender, recipient)
// from clients without reverse DNS or without an SPF pass, when
// greylisting is on.
func (ss *session) greylist(rcpt string) error {
	if !ss.dir.Config().Filter.Greylisting || !filter.Checkable(ss.ip) {
		return nil
	}
	has, known := ss.ptr()
	if (has || !known) && ss.spfResult().Result == auth.Pass {
		return nil
	}
	pass, err := ss.s.opts.Filter.Greylist(context.Background(), ss.ip, ss.from, rcpt)
	if err != nil {
		ss.s.log.Error("smtp: greylisting", "err", err)
		return nil
	}
	if !pass {
		return errGreylisted
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

// verdict is what the checks of one message found.
type verdict struct {
	spf   auth.SPFResult
	dkim  []auth.DKIMResult
	dmarc []auth.DMARCResult
	score filter.Score
	junk  bool
	why   string // why it is junk, for events
}

func (ss *session) Data(r io.Reader) error {
	if ss.dir == nil || (len(ss.boxes) == 0 && len(ss.forwards) == 0 && len(ss.reports) == 0) {
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
	ctx := context.Background()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return errLocal
	}
	fields, bodyStart, sep, err := readHeader(f)
	if errors.Is(err, errHeaderTooLarge) {
		return errHeaderTooBig
	}
	if err != nil {
		return errLocal
	}
	v, err := ss.check(ctx, f, fields)
	if err != nil {
		return err
	}

	// What every copy starts with: trace, verdicts, the header without
	// fields that claim to come from us.
	var prefix bytes.Buffer
	prefix.Write(ss.received())
	prefix.WriteString(auth.ResultsHeader(ss.dir.Hostname(), v.spf, v.dkim, v.dmarc))
	prefix.WriteString(v.score.Header())
	host := ss.dir.Hostname()
	for _, fl := range fields {
		if strings.EqualFold(fl.name, filter.ScoreHeaderName) ||
			strings.EqualFold(fl.name, "Authentication-Results") && auth.AuthServID(fl.value()) == host {
			continue
		}
		prefix.Write(fl.raw)
		if !bytes.HasSuffix(fl.raw, []byte("\n")) {
			prefix.WriteString("\r\n")
		}
	}
	prefix.Write(sep)
	message := func(returnPath string) (io.Reader, error) {
		if _, err := f.Seek(bodyStart, io.SeekStart); err != nil {
			return nil, err
		}
		return io.MultiReader(strings.NewReader(returnPath), bytes.NewReader(prefix.Bytes()), f), nil
	}

	// Forwarding and delivery reports passed back: the domain's sending
	// limit is counted before anything is stored, so a message that cannot
	// go anywhere gets a temporary error. Reservations of copies that are
	// not queued in the end (the message is refused, e.g. a mailbox could
	// not store it) are taken back, so the sender's retry does not count
	// twice against the limit.
	var sendable []*forward
	var reportsRes *reservation
	defer func() {
		for _, fw := range sendable {
			ss.release(fw.res)
		}
		ss.release(reportsRes)
	}()
	if !v.junk {
		for _, fw := range ss.forwards {
			if ss.reserveForward(ctx, fw) {
				sendable = append(sendable, fw)
			}
		}
		if len(ss.reports) > 0 {
			reportsRes = ss.reserveReports(ctx)
		}
		if len(ss.boxes) == 0 && len(sendable) == 0 && reportsRes == nil {
			return errForwardLimited
		}
	}

	returnPath := fmt.Sprintf("Return-Path: <%s>\r\n", sanitize(ss.from))
	delivered, full, failed := 0, 0, 0
	for _, m := range ss.boxes {
		msg, err := message(returnPath)
		if err != nil {
			return errLocal
		}
		err = ss.store(ctx, m, msg, v.junk)
		switch {
		case err == nil:
			delivered++
		case errors.Is(err, storage.ErrOverQuota):
			full++
			ss.notes = append(ss.notes, config.Event{Kind: config.EventBounced, StorageID: m.StorageID, Address: m.Address,
				Detail: fmt.Sprintf("a message of %d bytes was not stored: the mailbox is full", size)})
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
	queued := 0
	if v.junk {
		for _, fw := range ss.forwards {
			ss.notes = append(ss.notes, config.Event{Kind: config.EventNotForwarded, Address: fw.alias,
				Detail: fmt.Sprintf("a message to %s was not forwarded to %s: %s", fw.alias, strings.Join(fw.targets, ", "), v.why)})
		}
		for _, o := range ss.reports {
			ss.notes = append(ss.notes, config.Event{Kind: config.EventNotForwarded, Address: o,
				Detail: fmt.Sprintf("a delivery report for %s was not passed on: %s", o, v.why)})
		}
	} else {
		for _, fw := range sendable {
			msg, err := message("")
			if err != nil {
				return errLocal
			}
			sender, storageID := ss.forwardSender(fw.domain)
			if err := ss.s.opts.Queue.Enqueue(ctx, msg, sender, storageID, fw.targets); err != nil {
				ss.s.log.Error("smtp: enqueue forward", "err", err)
				return errLocal
			}
			// Queued: it goes out, so it stays counted.
			fw.res = nil
			queued++
		}
		if reportsRes != nil {
			msg, err := message("")
			if err != nil {
				return errLocal
			}
			if err := ss.s.opts.Queue.Enqueue(ctx, msg, "", "", ss.reports); err != nil {
				ss.s.log.Error("smtp: enqueue report", "err", err)
				return errLocal
			}
			reportsRes = nil
			queued++
		}
	}
	if delivered == 0 && queued == 0 && len(ss.boxes) > 0 && full == len(ss.boxes) {
		return errMailboxFull
	}
	for _, e := range ss.notes {
		if err := store.AddEvent(ctx, e); err != nil {
			ss.s.log.Error("smtp: record event", "err", err)
		}
	}
	ss.s.log.Info("smtp: message received", "remote", ss.remote, "bytes", size, "mailboxes", delivered,
		"forwarded", queued, "junk", v.junk, "score", v.score.Points, "tls", ss.tls)
	return nil
}

// check runs the authentication and content checks. It returns an SMTP
// error for messages that are refused.
func (ss *session) check(ctx context.Context, f *os.File, fields []field) (verdict, error) {
	var v verdict
	cfg := ss.dir.Config().Filter.WithDefaults()
	ver := auth.Verifier{Resolver: ss.s.opts.Filter.Resolver()}
	v.spf = ss.spfResult()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return v, errLocal
	}
	v.dkim = ver.DKIM(ctx, f)
	hdr := mimeHeader(fields)
	sc, fromDomains, unreadable := filter.CheckHeader(hdr, ss.rcptDomains, ss.ours)
	v.score.Merge(sc)
	// A From that cannot be read gives no domain, so DMARC and the refusal
	// of forged own domains would not see it, while mail clients often
	// still show it. Mail from the internet must have a readable From
	// (RFC 5322 3.6); local programs (loopback) keep the score only.
	if unreadable && !ss.ip.IsLoopback() {
		ss.s.log.Info("smtp: refused unreadable From", "remote", ss.remote)
		return v, errBadFrom
	}
	quarantine := ""
	for i, d := range fromDomains {
		if i >= 5 {
			break
		}
		r := ver.DMARC(ctx, d, v.spf, v.dkim)
		v.dmarc = append(v.dmarc, r)
		if r.Result != auth.Fail {
			continue
		}
		switch r.Policy {
		case "reject":
			ss.s.log.Info("smtp: refused by DMARC policy", "remote", ss.remote, "domain", d)
			return v, errDMARCReject(d)
		case "quarantine":
			quarantine = d
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return v, errLocal
	}
	name, parsed := filter.DangerousAttachment(f)
	if name != "" {
		ss.s.log.Info("smtp: refused attachment", "remote", ss.remote)
		return v, errAttachment(name)
	}
	if !parsed {
		v.score.Add(filter.PointsBrokenMIME, "mime-broken")
	}
	if has, known := ss.ptr(); known && !has {
		v.score.Add(filter.PointsNoPTR, "no-ptr")
	}
	switch v.spf.Result {
	case auth.SoftFail:
		v.score.Add(filter.PointsSPFSoftFail, "spf-softfail")
	case auth.Fail:
		v.score.Add(filter.PointsSPFFail, "spf-fail")
	}
	if d := ss.unauthenticatedOwn(v, fromDomains); d != "" {
		ss.s.log.Info("smtp: refused unauthenticated mail of a local domain", "remote", ss.remote, "domain", d)
		return v, errOwnDomain
	}
	dkimPass, dkimFail := false, false
	for _, d := range v.dkim {
		dkimPass = dkimPass || d.Result == auth.Pass
		dkimFail = dkimFail || d.Result == auth.Fail || d.Result == auth.PermError
	}
	if dkimFail && !dkimPass {
		v.score.Add(filter.PointsDKIMFail, "dkim-fail")
	}
	v.score.Merge(ss.bl.Score)
	switch {
	case quarantine != "":
		v.junk, v.why = true, "the DMARC policy of "+quarantine+" asks to treat it as junk"
		v.score.Reasons = append(v.score.Reasons, "dmarc-quarantine")
	case v.score.Points >= cfg.JunkScore:
		v.junk, v.why = true, fmt.Sprintf("it was judged as junk (score %s)", v.score.String())
	}
	return v, nil
}

// spfPassFor tells whether SPF passed for the envelope sender's domain d
// (not for the HELO name).
func (ss *session) spfPassFor(d string) bool {
	r := ss.spfResult()
	return r.Result == auth.Pass && !r.Helo && strings.EqualFold(r.Domain, d)
}

// unauthenticatedOwn returns one of our domains that the envelope sender or
// a header From claims while neither SPF nor a DKIM signature passed for it
// (aligned as DMARC relaxed: the same organizational domain), or "". Mail
// of our domains is sent by this server and DKIM-signed, so such mail from
// outside is forged, whatever the domain's published DMARC policy says.
//
// Clients on a loopback address are exempt: they are programs on this
// server (local tools handing mail to port 25) that already run on the
// machine; the check exists to stop the internet. A proxy in front of
// port 25 would make every client look local, so none is supported.
func (ss *session) unauthenticatedOwn(v verdict, fromDomains []string) string {
	if ss.ip.IsLoopback() {
		return ""
	}
	authenticated := func(d string) bool {
		if v.spf.Result == auth.Pass && sameOrg(v.spf.Domain, d) {
			return true
		}
		for _, r := range v.dkim {
			if r.Result == auth.Pass && sameOrg(r.Domain, d) {
				return true
			}
		}
		return false
	}
	var claimed []string
	if _, d, ok := config.SplitAddress(directory.Normalize(ss.from)); ok {
		claimed = append(claimed, d)
	}
	claimed = append(claimed, fromDomains...)
	for _, d := range claimed {
		if ss.ours(d) && !authenticated(d) {
			return d
		}
	}
	return ""
}

// sameOrg tells whether a and b share their organizational domain.
func sameOrg(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSuffix(a, ".")), strings.ToLower(strings.TrimSuffix(b, "."))
	if a == "" || b == "" {
		return false
	}
	return a == b || auth.OrgDomain(a) == auth.OrgDomain(b)
}

// store delivers into the mailbox's INBOX, or its Junk folder.
func (ss *session) store(ctx context.Context, m *config.Mailbox, msg io.Reader, junk bool) error {
	folder := storage.Inbox
	if junk {
		folder = ss.junkFolder(ctx, m.StorageID)
	}
	_, err := ss.s.opts.Store.Append(ctx, m.StorageID, folder, msg, storage.AppendOptions{})
	return err
}

// junkFolder is the folder marked \Junk (the person may have renamed it),
// else INBOX.
func (ss *session) junkFolder(ctx context.Context, id string) string {
	st := ss.s.opts.Store
	if err := st.EnsureMailbox(ctx, id); err != nil {
		return storage.Inbox
	}
	folders, err := st.ListFolders(ctx, id)
	if err != nil {
		return storage.Inbox
	}
	for _, f := range folders {
		if f.SpecialUse == storage.SpecialJunk {
			return f.Name
		}
	}
	return storage.Inbox
}

// reserveForward counts one forwarded message against the alias domain's
// hourly sending limit; false (and an event) when it is reached.
func (ss *session) reserveForward(ctx context.Context, fw *forward) bool {
	res, ok, dom, max := ss.reserveDomains(ctx, []string{fw.domain})
	if ok {
		fw.res = res
		return true
	}
	ss.notes = append(ss.notes, config.Event{Kind: config.EventNotForwarded, Address: fw.alias,
		Detail: fmt.Sprintf("a message to %s was not forwarded to %s: the domain %s reached its limit of %d messages per hour",
			fw.alias, strings.Join(fw.targets, ", "), dom, max)})
	return false
}

// reserveReports counts the delivery reports passed back through SRS
// addresses once per message against each SRS domain's hourly sending
// limit, like forwards: otherwise anyone with an alias that forwards could
// make this server send unlimited null-sender mail to an address of their
// choice. nil (and events) when a limit is reached; then none of the
// reports is passed on.
func (ss *session) reserveReports(ctx context.Context) *reservation {
	res, ok, dom, max := ss.reserveDomains(ctx, ss.reportDoms)
	if ok {
		if res == nil {
			// Counting failed: passed on anyway, nothing to take back.
			res = &reservation{}
		}
		return res
	}
	for _, o := range ss.reports {
		ss.notes = append(ss.notes, config.Event{Kind: config.EventNotForwarded, Address: o,
			Detail: fmt.Sprintf("a delivery report for %s was not passed on: the domain %s reached its limit of %d messages per hour",
				o, dom, max)})
	}
	return nil
}

// reserveDomains counts one message against the hourly sending limit of
// every domain in doms, all or nothing, and returns the reservation (nil
// when counting failed and the message goes anyway). When a limit is
// reached it returns false, that domain and its limit.
func (ss *session) reserveDomains(ctx context.Context, doms []string) (*reservation, bool, string, int) {
	now := ss.s.opts.Now()
	limits := make([]storage.SendLimit, 0, len(doms))
	for _, dom := range doms {
		max := config.DefaultDomainSendPerHour
		if d, ok := ss.dir.Domain(dom); ok && d.SendPerHour > 0 {
			max = d.SendPerHour
		}
		limits = append(limits, storage.SendLimit{Key: storage.KeyDomainSends + dom, Since: now.Add(-time.Hour), Max: max})
	}
	i, err := ss.s.opts.Store.ReserveSend(ctx, limits, now)
	if err != nil {
		// Counting failed: send anyway rather than lose the message.
		ss.s.log.Error("smtp: count forward", "err", err)
		return nil, true, "", 0
	}
	if i < 0 {
		return &reservation{limits: limits, at: now}, true, "", 0
	}
	return nil, false, doms[i], limits[i].Max
}

// release takes back a reservation of a message that was not queued.
func (ss *session) release(r *reservation) {
	if r == nil || len(r.limits) == 0 {
		return
	}
	if err := ss.s.opts.Store.ReleaseSend(context.Background(), r.limits, r.at); err != nil {
		ss.s.log.Error("smtp: release forward count", "err", err)
	}
}

// forwardSender is the envelope sender of forwarded mail and the mailbox
// that gets its bounces: the null sender stays null; a sender of our own
// domains stays when SPF passed for it on this connection (bounces go to
// its mailbox, if it is one); everyone else gets an SRS address in the
// alias domain. Without the SPF condition, mail forged with one of our
// addresses would leave this server under that address, pass SPF at the
// target and send its bounces into the forged mailbox.
func (ss *session) forwardSender(aliasDomain string) (sender, storageID string) {
	if ss.from == "" {
		return "", ""
	}
	addr := directory.Normalize(ss.from)
	if _, d, ok := config.SplitAddress(addr); ok && ss.ours(d) && ss.spfPassFor(d) {
		if m, ok := ss.dir.Mailbox(addr); ok {
			return ss.from, m.StorageID
		}
		return ss.from, ""
	}
	srs, err := ss.s.opts.SRS.Forward(ss.from, aliasDomain)
	if err != nil {
		return "", ""
	}
	return srs, ""
}

// received is the Received header added on delivery.
func (ss *session) received() []byte {
	var b strings.Builder
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
		ss.s.opts.Now().Format(time.RFC1123Z))
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

// printable keeps printable ASCII (with spaces) up to n bytes.
func printable(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < n; i++ {
		if c := s[i]; c >= 0x20 && c < 0x7f {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func randomKey() []byte {
	k := make([]byte, 32)
	rand.Read(k)
	return k
}
