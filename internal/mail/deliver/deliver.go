// Package deliver stores a message in a local mailbox and runs the
// mailbox's filters on the way (G2 in wiki/Groupware-Architecture.md).
// Port 25 (smtpd) and submission (mail between mailboxes of this server)
// deliver through it, once per recipient mailbox, after their own checks.
//
// What a filter run decides is carried out here:
//
//   - keep and the implicit keep store into INBOX, or into Junk for mail
//     judged as junk; fileinto stores where it says (also out of Junk).
//   - redirect forwards: with SRS for outside senders, counted against the
//     domain's sending limit, at most config.MaxForwardTargets addresses per
//     message, only for mailboxes allowed to forward, never for junk or mail
//     that failed DMARC, never to the mailbox's own addresses, and not when
//     the message carries this mailbox's loop marker or Delivered-To.
//   - vacation sends an out-of-office reply under the rules of RFC 5230 and
//     RFC 3834 (see vacation.go).
//
// A message is never lost to a filter: when a run fails or a redirect is
// not allowed, the message is kept.
package deliver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/queue"
	"github.com/danielgtmn/ownpanel/internal/mail/sieve"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Limits.
const (
	// MaxScripts bounds the scripts of one mailbox.
	MaxScripts = sieve.MaxScripts
	// maxHops: a message with more Received fields is not redirected.
	maxHops = 30
	// maxLoopMarks: a message redirected this often here is not redirected
	// again.
	maxLoopMarks = 5
	// maxDepth bounds deliveries nested through redirects to local
	// addresses.
	maxDepth = 5
	// LoopHeader marks messages this server redirected.
	LoopHeader = "X-Ownpanel-Loop"
	// cacheSize bounds the compiled scripts kept.
	cacheSize = 256
)

// Options configure a Deliverer.
type Options struct {
	Store *storage.Store
	// Queue sends forwarded mail and replies outside; nil: nothing leaves
	// the server (forwards and replies outside are dropped with an event).
	Queue  *queue.Queue
	Signer *auth.Signer
	SRS    *auth.SRS
	Event  func(config.Event)
	Logger *slog.Logger
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
	// Location is the zone of dates in filters without one; nil means the
	// server's.
	Location *time.Location
}

// Deliverer stores messages and runs filters.
type Deliverer struct {
	opts Options
	log  *slog.Logger

	mu    sync.Mutex
	cache map[[32]byte]*sieve.Script
}

// New returns a Deliverer.
func New(opts Options) *Deliverer {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	if opts.Event == nil {
		opts.Event = func(config.Event) {}
	}
	return &Deliverer{opts: opts, log: opts.Logger, cache: map[[32]byte]*sieve.Script{}}
}

// Message is one message for local mailboxes.
type Message struct {
	Dir *directory.Directory
	// Open returns the message as it is stored, without Return-Path, from
	// the start; it may be called several times.
	Open func() (io.Reader, error)
	Size int64
	// EnvelopeFrom is the envelope sender ("" for the null sender).
	EnvelopeFrom string
	// SenderVerified: the envelope sender is a mailbox of ours that signed
	// in, or SPF passed for it; forwards keep it instead of an SRS address.
	SenderVerified bool
	// Junk: the message was judged as junk. DMARCFail: DMARC failed (any
	// policy). AuthFail: SPF failed, or a DKIM signature failed and none
	// passed. Each stops out-of-office replies; Junk and DMARCFail also stop
	// forwarding.
	Junk      bool
	DMARCFail bool
	AuthFail  bool
	// SenderAuthenticated: the envelope sender is confirmed (SPF passed
	// for its domain, a DKIM signature of its domain passed, or it is a
	// mailbox here that signed in). Out-of-office replies go only to such
	// senders, so forged senders get no backscatter.
	SenderAuthenticated bool
	// SpamTest and SpamPercent are the spamtest values (SpamValues).
	SpamTest    string
	SpamPercent string

	parsedOnce sync.Once
	parsed     *parsed
	depth      int
}

func (m *Message) msg() *parsed {
	m.parsedOnce.Do(func() { m.parsed = &parsed{open: m.Open, size: m.Size} })
	return m.parsed
}

// SpamValues maps a content score to the spamtest values "0".."10" and
// "0".."100" (RFC 5235): 1 is clean, 10 certainly junk.
func SpamValues(points, threshold float64, junk bool) (test, percent string) {
	if threshold <= 0 {
		threshold = config.DefaultJunkScore
	}
	p := max(points, 0) / threshold * 100
	if junk {
		p = 100
	}
	p = min(p, 100)
	v := 1 + int(p/100*9)
	return fmt.Sprint(min(v, 10)), fmt.Sprint(int(p))
}

// Recipient is one mailbox the message is for.
type Recipient struct {
	Mailbox *config.Mailbox
	// Rcpt is the address the sender used (an alias, if the mail came
	// through one): envelope :to in filters.
	Rcpt string
}

// store is one copy to store.
type store struct {
	folder string
	flags  []string
}

// Pending is a delivery whose copies are stored. Finish then does what
// leaves the mailbox (forwards outside, out-of-office replies, recording
// duplicate ids); Abort takes back what was reserved for it when the
// message is refused after all (another mailbox could not store it and
// the sender will try again). Exactly one of them must be called.
type Pending struct {
	d   *Deliverer
	m   *Message
	r   Recipient
	vac *sieve.Vacation
	// dups are recorded once the message is delivered (RFC 7352).
	dups []sieve.DuplicateMark
	// external targets of forwards, counted against the limits in res.
	external []string
	res      *reservation
	prefix   []byte
	// stored: a copy was stored here or at a local forward target, so a
	// forward outside that fails loses nothing.
	stored     bool
	keepFolder string
	nested     []*Pending
	done       bool
}

// reservation is one message counted against sending limits at a time.
type reservation struct {
	limits []storage.SendLimit
	at     time.Time
}

// Deliver stores the message for one mailbox (Store) and then carries out
// the rest (Finish). Callers that deliver to several mailboxes use Store
// for all of them first.
func (d *Deliverer) Deliver(ctx context.Context, m *Message, r Recipient) error {
	p, err := d.Store(ctx, m, r)
	if err != nil {
		return err
	}
	p.Finish(ctx)
	return nil
}

// Store runs the mailbox's filters and stores the copies they ask for,
// also in local mailboxes the message is forwarded to. It returns
// storage.ErrOverQuota when the mailbox is full and other errors when
// something could not be stored; then nothing has left the server and
// nothing stays reserved.
func (d *Deliverer) Store(ctx context.Context, m *Message, r Recipient) (*Pending, error) {
	box := r.Mailbox
	p := &Pending{d: d, m: m, r: r, keepFolder: storage.Inbox}
	if m.Junk {
		p.keepFolder = d.junkFolder(ctx, box.StorageID)
	}
	res := d.run(ctx, m, r)
	var stores []store
	add := func(folder string, flags []string) {
		for i := range stores {
			if strings.EqualFold(stores[i].folder, folder) {
				stores[i].flags = mergeFlags(stores[i].flags, flags)
				return
			}
		}
		stores = append(stores, store{folder: folder, flags: flags})
	}
	if res == nil {
		add(p.keepFolder, nil)
		if err := d.store(ctx, m, box, stores); err != nil {
			return nil, err
		}
		p.stored = true
		return p, nil
	}
	p.dups = res.Duplicates
	keep := res.ImplicitKeep
	var redirects []string
	for _, a := range res.Actions {
		switch a.Kind {
		case sieve.ActKeep:
			add(p.keepFolder, a.Flags)
		case sieve.ActFileinto:
			folder, ok := d.folder(ctx, box.StorageID, a.Folder, a.Create)
			if !ok {
				folder = p.keepFolder
			}
			add(folder, a.Flags)
		case sieve.ActRedirect:
			redirects = append(redirects, a.Address)
		case sieve.ActVacation:
			p.vac = a.Vacation
		}
	}
	// Forwards that may not happen keep the message instead.
	allowed, refused := d.checkRedirects(ctx, m, box, redirects)
	local, external, refusedLocal := d.splitTargets(m, box, allowed)
	refused = refused || refusedLocal
	if len(external) > 0 {
		if res, ok := d.reserveForward(ctx, m, box, external); ok {
			p.external, p.res = external, res
		} else {
			refused = true
		}
	}
	if refused && !res.ImplicitKeep && len(stores) == 0 && !hasDiscard(res) {
		keep = true
	}
	if keep {
		add(p.keepFolder, res.KeepFlags)
	}
	if err := d.store(ctx, m, box, stores); err != nil {
		p.Abort(ctx)
		return nil, err
	}
	p.stored = len(stores) > 0
	if len(local) > 0 {
		p.prefix = forwardPrefix(m.Dir, box)
		nm := withPrefix(m, p.prefix)
		for _, lr := range local {
			np, err := d.Store(ctx, nm, lr)
			switch {
			case err == nil:
				p.nested = append(p.nested, np)
				p.stored = true
			case errors.Is(err, storage.ErrOverQuota):
				d.opts.Event(config.Event{Kind: config.EventBounced, StorageID: lr.Mailbox.StorageID, Address: lr.Mailbox.Address,
					Detail: fmt.Sprintf("a message forwarded by a filter of %s was not stored: the mailbox is full", box.Address)})
			default:
				// The sender tries again; nothing went out yet.
				p.Abort(ctx)
				return nil, err
			}
		}
	}
	if !p.stored && len(p.external) == 0 && !hasDiscard(res) && (len(redirects) > 0 || keep) {
		// Every target refused or full: keep it here after all.
		if err := d.store(ctx, m, box, []store{{folder: p.keepFolder}}); err != nil {
			p.Abort(ctx)
			return nil, err
		}
		p.stored = true
	}
	return p, nil
}

// Finish does what leaves the mailbox once every copy is stored.
func (p *Pending) Finish(ctx context.Context) {
	if p == nil || p.done {
		return
	}
	p.done = true
	d, box := p.d, p.r.Mailbox
	now := d.opts.Now()
	for _, dm := range p.dups {
		if _, err := d.opts.Store.SieveDuplicate(ctx, box.StorageID, dm.ID, dm.Period, dm.Last, now); err != nil {
			d.log.Error("deliver: record duplicate", "storage_id", box.StorageID, "err", err)
		}
	}
	if len(p.external) > 0 {
		if !d.enqueue(ctx, p.m, box, forwardPrefix(p.m.Dir, box), p.external) {
			release(ctx, d.opts.Store, p.res, d.log)
			if !p.stored {
				// Could not be queued and nothing kept: keep it after all.
				if err := d.store(ctx, p.m, box, []store{{folder: p.keepFolder}}); err != nil {
					d.log.Error("deliver: keep a message that could not be forwarded", "storage_id", box.StorageID, "err", err)
				}
			}
		}
	}
	if p.vac != nil {
		d.vacation(ctx, p.m, p.r, p.vac)
	}
	for _, np := range p.nested {
		np.Finish(ctx)
	}
}

// Abort takes back what Store reserved.
func (p *Pending) Abort(ctx context.Context) {
	if p == nil || p.done {
		return
	}
	p.done = true
	release(ctx, p.d.opts.Store, p.res, p.d.log)
	for _, np := range p.nested {
		np.Abort(ctx)
	}
}

func release(ctx context.Context, st *storage.Store, r *reservation, log *slog.Logger) {
	if r == nil || len(r.limits) == 0 {
		return
	}
	if err := st.ReleaseSend(ctx, r.limits, r.at); err != nil {
		log.Error("deliver: release send count", "err", err)
	}
}

func hasDiscard(res *sieve.Result) bool {
	for _, a := range res.Actions {
		if a.Kind == sieve.ActDiscard {
			return true
		}
	}
	return false
}

// run executes the mailbox's active script; nil means "just keep" (no
// script, or it failed).
func (d *Deliverer) run(ctx context.Context, m *Message, r Recipient) *sieve.Result {
	st := d.opts.Store
	id := r.Mailbox.StorageID
	name, src, ok, err := st.ActiveSieveScript(ctx, id)
	if err != nil {
		d.log.Error("deliver: load filters", "storage_id", id, "err", err)
		return nil
	}
	if !ok {
		return nil
	}
	script, err := d.compile(src)
	if err != nil {
		d.log.Warn("deliver: filter script not valid", "storage_id", id, "script", name, "err", err)
		return nil
	}
	now := d.opts.Now()
	env := &sieve.Env{
		Message:      m.msg(),
		EnvelopeFrom: m.EnvelopeFrom,
		EnvelopeTo:   r.Rcpt,
		Now:          now,
		Location:     d.opts.Location,
		SpamTest:     m.SpamTest,
		SpamPercent:  m.SpamPercent,
		FolderExists: func(f string) bool {
			_, ok := d.existingFolder(ctx, id, f)
			return ok
		},
		// Only a look: the ids are recorded once the message is stored
		// (Pending.Finish).
		Duplicate: func(key string) bool {
			seen, err := st.SieveDuplicateSeen(ctx, id, key, now)
			if err != nil {
				d.log.Error("deliver: duplicate", "storage_id", id, "err", err)
			}
			return seen
		},
		Include: func(rctx context.Context, n string) (*sieve.Script, bool, error) {
			src, _, err := st.SieveScript(rctx, id, n)
			if errors.Is(err, storage.ErrNoScript) {
				return nil, false, nil
			}
			if err != nil {
				return nil, false, err
			}
			s, err := d.compile(src)
			return s, err == nil, err
		},
		CharsetReader: charsetReader,
	}
	rctx, cancel := context.WithTimeout(ctx, sieve.DefaultTimeout)
	defer cancel()
	res, err := script.Run(rctx, env)
	if err != nil {
		d.log.Warn("deliver: filter failed; message kept", "storage_id", id, "script", name, "err", err)
		return nil
	}
	return res
}

func (d *Deliverer) compile(src string) (*sieve.Script, error) {
	key := sha256.Sum256([]byte(src))
	d.mu.Lock()
	s, ok := d.cache[key]
	d.mu.Unlock()
	if ok {
		return s, nil
	}
	s, err := sieve.Compile(src)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	if len(d.cache) >= cacheSize {
		clear(d.cache)
	}
	d.cache[key] = s
	d.mu.Unlock()
	return s, nil
}

// store appends the copies; the first error ends it.
//
// In a shared mailbox (G3) every reader has their own read state, kept
// apart from the message's flags; a filter's \Seen would be nobody's and
// would only show up if the mailbox became personal again. It is left out
// there: a new message is unread for everyone. Other flags stay.
func (d *Deliverer) store(ctx context.Context, m *Message, box *config.Mailbox, stores []store) error {
	for _, s := range stores {
		msg, err := m.Open()
		if err != nil {
			return err
		}
		flags := validFlags(s.flags)
		if box.Shared() {
			flags = slices.DeleteFunc(flags, func(f string) bool { return f == storage.FlagSeen })
		}
		rp := "Return-Path: <" + sanitize(m.EnvelopeFrom) + ">\r\n"
		_, err = d.opts.Store.Append(ctx, box.StorageID, s.folder, io.MultiReader(strings.NewReader(rp), msg),
			storage.AppendOptions{Flags: flags})
		if err != nil {
			return err
		}
	}
	return nil
}

// junkFolder is the folder marked \Junk, else INBOX.
func (d *Deliverer) junkFolder(ctx context.Context, id string) string {
	st := d.opts.Store
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

// existingFolder finds a folder by name (INBOX in any case).
func (d *Deliverer) existingFolder(ctx context.Context, id, name string) (string, bool) {
	n, err := storage.NormalizeFolderName(name)
	if err != nil {
		return "", false
	}
	st := d.opts.Store
	if err := st.EnsureMailbox(ctx, id); err != nil {
		return "", false
	}
	folders, err := st.ListFolders(ctx, id)
	if err != nil {
		return "", false
	}
	for _, f := range folders {
		if f.Name == n {
			return f.Name, true
		}
	}
	return "", false
}

// folder resolves the folder of a fileinto; with create it is made when
// missing. ok is false when it does not exist (the message is kept then).
func (d *Deliverer) folder(ctx context.Context, id, name string, create bool) (string, bool) {
	if f, ok := d.existingFolder(ctx, id, name); ok {
		return f, true
	}
	if !create {
		d.log.Info("deliver: filter folder missing; message kept", "storage_id", id)
		return "", false
	}
	n, err := storage.NormalizeFolderName(name)
	if err != nil {
		return "", false
	}
	if err := d.opts.Store.CreateFolder(ctx, id, n); err != nil && !errors.Is(err, storage.ErrFolderExists) {
		d.log.Warn("deliver: create filter folder", "storage_id", id, "err", err)
		return "", false
	}
	return n, true
}

// validFlags drops flags storage would refuse.
func validFlags(in []string) []string {
	var out []string
	for _, f := range in {
		if n, err := storage.NormalizeFlags([]string{f}); err == nil {
			out = append(out, n...)
		}
	}
	return out
}

func mergeFlags(a, b []string) []string {
	out := append([]string{}, a...)
	for _, f := range b {
		found := false
		for _, x := range out {
			if strings.EqualFold(x, f) {
				found = true
			}
		}
		if !found {
			out = append(out, f)
		}
	}
	return out
}

// ownAddresses are the mailbox's address and the aliases (not the
// catch-all) that deliver to it.
func ownAddresses(dir *directory.Directory, box *config.Mailbox) []string {
	out := []string{box.Address}
	for _, a := range dir.Config().Aliases {
		if dir.MaySendAs(box, a.Address) {
			out = append(out, a.Address)
		}
	}
	return out
}

// loopMark is the mailbox's opaque mark in LoopHeader: it does not reveal
// the address.
func loopMark(host, storageID string) string {
	h := sha256.Sum256([]byte("ownpanel loop\x00" + host + "\x00" + storageID))
	return hex.EncodeToString(h[:10])
}

// sanitize keeps printable ASCII without spaces or angle brackets.
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

// withPrefix is m with header lines added at the top (a nested delivery
// through a redirect to a local address).
func withPrefix(m *Message, prefix []byte) *Message {
	open := m.Open
	return &Message{
		Dir: m.Dir, Size: m.Size + int64(len(prefix)),
		Open: func() (io.Reader, error) {
			r, err := open()
			if err != nil {
				return nil, err
			}
			return io.MultiReader(bytes.NewReader(prefix), r), nil
		},
		EnvelopeFrom: m.EnvelopeFrom, SenderVerified: m.SenderVerified,
		Junk: m.Junk, DMARCFail: m.DMARCFail, AuthFail: m.AuthFail, SenderAuthenticated: m.SenderAuthenticated,
		SpamTest: m.SpamTest, SpamPercent: m.SpamPercent,
		depth: m.depth + 1,
	}
}
