// Package queue delivers outgoing mail: messages are kept durably in the
// storage (rows in mail.db, files under queue/), delivered per recipient
// domain to its MX hosts by preference on port 25, retried on temporary
// failures (5 min, 15 min, 30 min, 1 h, 2 h, then every 4 h, for at most
// 5 days) and bounced to the sender's mailbox with a delivery status
// notification on permanent failure or when giving up. A message with the
// null sender is never bounced.
//
// STARTTLS is used whenever the receiving server offers it; its
// certificate is not checked (opportunistic TLS, RFC 7435). When the TLS
// handshake itself fails, the same host is tried once more without TLS.
//
// Outgoing connections use IPv4 unless Options.IPv6 is set: a server's
// IPv6 address often has no reverse DNS entry naming the mail host, and
// receivers refuse such mail. The panel decides later whether IPv6 is
// safe to use.
package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Resolver looks up mail servers and addresses; *net.Resolver is one.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Defaults for Options.
var (
	DefaultSchedule = []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour}
)

const (
	DefaultGiveUp  = 5 * 24 * time.Hour
	DefaultWorkers = 8
	DefaultTimeout = 5 * time.Minute
	// connLimit bounds one connection to a receiving server in total.
	connLimit = 30 * time.Minute
	// dueBatch is how many messages one dispatch round looks at.
	dueBatch = 200
)

// Options configure a Queue.
type Options struct {
	Store *storage.Store
	// Directory returns the current configuration view (host name for
	// EHLO and bounces, mailboxes that receive bounces).
	Directory func() *directory.Directory
	// Resolver: nil means net.DefaultResolver.
	Resolver Resolver
	// Dial connects to a receiving server; nil means a net.Dialer with a
	// 30 s timeout. network is "tcp4" or "tcp6".
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// Port of receiving servers; "" means "25".
	Port string
	// IPv6 allows connections over IPv6 (tried before IPv4 then).
	IPv6 bool
	// Workers bounds concurrent deliveries; 0 means DefaultWorkers.
	Workers int
	// Schedule is the delay after the 1st, 2nd, ... failed attempt; the
	// last entry repeats. Nil means DefaultSchedule.
	Schedule []time.Duration
	// GiveUp is how long a message is tried; 0 means DefaultGiveUp.
	GiveUp time.Duration
	// Timeout per SMTP command; 0 means DefaultTimeout.
	Timeout time.Duration
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
	// Event records an event for the panel; nil drops them.
	Event func(config.Event)
	// ReturnTo maps the envelope sender of a message without a local
	// mailbox (forwarded mail with an SRS sender) to the original sender a
	// bounce is sent to over the internet; nil, or false, sends none.
	ReturnTo func(sender string) (string, bool)
	Logger   *slog.Logger
}

// Queue is the outbound queue. Call Start to deliver in the background.
type Queue struct {
	opts Options
	log  *slog.Logger

	kick chan struct{}
	sem  chan struct{}

	mu       sync.Mutex
	inflight map[groupKey]bool
	stopping bool
	started  bool
	wg       sync.WaitGroup // workers
	loopDone chan struct{}
	stop     chan struct{}

	// workCtx ends deliveries that do not finish within the shutdown
	// grace time; they are retried after the next start.
	workCtx    context.Context
	cancelWork context.CancelFunc
}

type groupKey struct {
	id     int64
	domain string
}

// New returns a queue.
func New(opts Options) *Queue {
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	if opts.Dial == nil {
		d := &net.Dialer{Timeout: 30 * time.Second}
		opts.Dial = d.DialContext
	}
	if opts.Port == "" {
		opts.Port = "25"
	}
	if opts.Workers <= 0 {
		opts.Workers = DefaultWorkers
	}
	if len(opts.Schedule) == 0 {
		opts.Schedule = DefaultSchedule
	}
	if opts.GiveUp <= 0 {
		opts.GiveUp = DefaultGiveUp
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Queue{
		opts: opts, log: opts.Logger,
		kick:     make(chan struct{}, 1),
		sem:      make(chan struct{}, opts.Workers),
		inflight: map[groupKey]bool{},
		stop:     make(chan struct{}),
		loopDone: make(chan struct{}),
		workCtx:  ctx, cancelWork: cancel,
	}
}

func (q *Queue) now() time.Time { return q.opts.Now() }

// Enqueue stores a message for delivery to external recipients. sender is
// the envelope sender ("" for the null sender), storageID the local
// mailbox that receives bounces ("" for none). It returns once the message
// is durable.
func (q *Queue) Enqueue(ctx context.Context, r io.Reader, sender, storageID string, rcpts []string) error {
	m := storage.QueuedMessage{Sender: sender, StorageID: storageID, Created: q.now()}
	for _, a := range rcpts {
		_, d, ok := config.SplitAddress(a)
		if !ok {
			return fmt.Errorf("invalid recipient %q", a)
		}
		m.Rcpts = append(m.Rcpts, storage.QueuedRcpt{Address: a, Domain: strings.ToLower(d)})
	}
	if _, err := q.opts.Store.Enqueue(ctx, r, m); err != nil {
		return err
	}
	q.Kick()
	return nil
}

// Len is the number of messages waiting.
func (q *Queue) Len(ctx context.Context) (int, error) { return q.opts.Store.QueueLength(ctx) }

// Kick makes the queue look for due messages now.
func (q *Queue) Kick() {
	select {
	case q.kick <- struct{}{}:
	default:
	}
}

// Start delivers in the background until Shutdown.
func (q *Queue) Start() {
	q.mu.Lock()
	if q.started || q.stopping {
		q.mu.Unlock()
		return
	}
	q.started = true
	q.mu.Unlock()
	go q.loop()
}

// pollEvery bounds the wait between looks at the queue, so a changed
// clock or a missed kick never delays mail for long.
const pollEvery = time.Minute

func (q *Queue) loop() {
	defer close(q.loopDone)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		q.dispatch()
		wait := pollEvery
		if next, ok, err := q.opts.Store.QueueNext(context.Background()); err != nil {
			q.log.Error("mail queue: next", "err", err)
		} else if ok {
			wait = min(max(next.Sub(q.now()), time.Second), pollEvery)
		}
		timer.Reset(wait)
		select {
		case <-q.stop:
			return
		case <-q.kick:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

// dispatch starts a worker for every due (message, domain) that is not
// being delivered, as long as workers are free. It returns how many it
// started.
func (q *Queue) dispatch() int {
	msgs, err := q.opts.Store.QueueDue(context.Background(), q.now(), dueBatch)
	if err != nil {
		q.log.Error("mail queue: due", "err", err)
		return 0
	}
	started := 0
	for _, m := range msgs {
		groups := map[string][]storage.QueuedRcpt{}
		var order []string
		for _, r := range m.Rcpts {
			if _, ok := groups[r.Domain]; !ok {
				order = append(order, r.Domain)
			}
			groups[r.Domain] = append(groups[r.Domain], r)
		}
		for _, d := range order {
			key := groupKey{m.ID, d}
			q.mu.Lock()
			if q.stopping || q.inflight[key] {
				q.mu.Unlock()
				continue
			}
			select {
			case q.sem <- struct{}{}:
			default:
				q.mu.Unlock()
				return started // all workers busy; a finishing one kicks
			}
			q.inflight[key] = true
			q.wg.Add(1)
			q.mu.Unlock()
			started++
			go q.work(key, m, groups[d])
		}
	}
	return started
}

// Flush delivers everything that is due now and waits for it (tests and
// tools; do not combine with Start).
func (q *Queue) Flush() {
	for q.dispatch() > 0 {
		q.wg.Wait()
	}
	q.wg.Wait()
}

// Shutdown stops taking up new deliveries and waits for running ones until
// ctx ends; those still running then are cancelled and retried after the
// next start.
func (q *Queue) Shutdown(ctx context.Context) error {
	q.mu.Lock()
	if q.stopping {
		q.mu.Unlock()
		return nil
	}
	q.stopping = true
	started := q.started
	q.mu.Unlock()
	close(q.stop)
	if started {
		<-q.loopDone
	}
	done := make(chan struct{})
	go func() { q.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		q.cancelWork()
		<-done
	}
	q.cancelWork()
	return nil
}

// outcome of one recipient in one attempt.
type outcome int

const (
	delivered outcome = iota
	temporary
	permanent
)

type result struct {
	outcome outcome
	// status is the enhanced status code (e.g. "5.1.1"); diag the reply
	// of the receiving server or what went wrong, for bounces and logs.
	status string
	diag   string
}

func (q *Queue) work(key groupKey, m storage.QueuedMessage, rcpts []storage.QueuedRcpt) {
	defer func() {
		q.mu.Lock()
		delete(q.inflight, key)
		q.mu.Unlock()
		<-q.sem
		q.wg.Done()
		q.Kick()
	}()
	ctx := q.workCtx
	results := q.deliverDomain(ctx, m, key.domain, rcpts)
	if ctx.Err() != nil {
		// Shutting down: the rows stay as they are and are tried again.
		return
	}
	now := q.now()
	deadline := m.Created.Add(q.opts.GiveUp)
	var failed []Failure
	ok := 0
	for i := range rcpts {
		r := &rcpts[i]
		res := results[i]
		r.Attempts++
		r.LastError = truncate(res.diag, 500)
		switch res.outcome {
		case delivered:
			r.State = storage.RcptDelivered
			ok++
		case permanent:
			r.State = storage.RcptFailed
			failed = append(failed, Failure{Recipient: r.Address, Status: res.status, Diagnostic: res.diag})
		case temporary:
			if !now.Before(deadline) {
				r.State = storage.RcptFailed
				failed = append(failed, Failure{Recipient: r.Address, Status: "4.4.7",
					Diagnostic: "gave up after " + humanDur(q.opts.GiveUp) + ": " + res.diag})
				continue
			}
			next := now.Add(q.delay(r.Attempts))
			if next.After(deadline) {
				next = deadline
			}
			r.NextTry = next
		}
	}
	if ok > 0 {
		q.log.Info("mail queue: delivered", "domain", key.domain, "recipients", ok)
	}
	if len(failed) > 0 {
		q.bounce(context.Background(), m, failed)
	}
	if err := q.opts.Store.QueueUpdate(context.Background(), rcpts); err != nil {
		q.log.Error("mail queue: update", "err", err)
		return
	}
	if _, err := q.opts.Store.QueueRemoveIfDone(context.Background(), m.ID); err != nil {
		q.log.Error("mail queue: remove", "err", err)
	}
}

func (q *Queue) delay(attempts int) time.Duration {
	i := min(attempts-1, len(q.opts.Schedule)-1)
	return q.opts.Schedule[max(i, 0)]
}

// deliverDomain tries the MX hosts of domain in order and returns one
// result per recipient.
func (q *Queue) deliverDomain(ctx context.Context, m storage.QueuedMessage, domain string, rcpts []storage.QueuedRcpt) []result {
	all := func(r result) []result {
		out := make([]result, len(rcpts))
		for i := range out {
			out[i] = r
		}
		return out
	}
	hosts, res := q.mxHosts(ctx, domain)
	if res != nil {
		return all(*res)
	}
	last := result{outcome: temporary, status: "4.4.4", diag: "no mail server of " + domain + " could be reached"}
	for _, host := range hosts {
		ips, err := q.addresses(ctx, host)
		if err != nil {
			last = result{outcome: temporary, status: "4.4.3", diag: err.Error()}
			continue
		}
		for _, ip := range ips {
			if ctx.Err() != nil {
				return all(result{outcome: temporary, status: "4.3.2", diag: "shutting down"})
			}
			results, err := q.tryAddr(ctx, host, ip, m, rcpts)
			if err == nil {
				return results
			}
			// The host refused or failed before the transaction: the next
			// one may do better. Only if the last one refused for good is
			// the message bounced.
			last = classify(err)
			q.log.Info("mail queue: host failed", "host", host, "addr", ip.String(), "err", truncate(last.diag, 200))
		}
	}
	return all(last)
}

// mxHosts returns the hosts to try for domain in order, or a result for
// every recipient when there are none.
func (q *Queue) mxHosts(ctx context.Context, domain string) ([]string, *result) {
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	mxs, err := q.opts.Resolver.LookupMX(lctx, domain)
	var de *net.DNSError
	switch {
	case err != nil && !(errors.As(err, &de) && de.IsNotFound):
		return nil, &result{outcome: temporary, status: "4.4.3", diag: "looking up the mail servers of " + domain + " failed"}
	case err != nil || len(mxs) == 0:
		// No MX: the domain itself is the mail server (implicit MX), if it
		// has an address.
		_, aerr := q.opts.Resolver.LookupIPAddr(lctx, domain)
		if aerr != nil {
			if errors.As(aerr, &de) && de.IsNotFound {
				return nil, &result{outcome: permanent, status: "5.1.2", diag: "the domain " + domain + " has no mail server"}
			}
			return nil, &result{outcome: temporary, status: "4.4.3", diag: "looking up " + domain + " failed"}
		}
		return []string{domain}, nil
	}
	if len(mxs) == 1 && (mxs[0].Host == "." || mxs[0].Host == "") {
		return nil, &result{outcome: permanent, status: "5.1.10", diag: "the domain " + domain + " does not accept mail (null MX)"}
	}
	sort.SliceStable(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })
	var hosts []string
	for _, mx := range mxs {
		h := strings.TrimSuffix(mx.Host, ".")
		if h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		return nil, &result{outcome: permanent, status: "5.1.10", diag: "the domain " + domain + " does not accept mail (null MX)"}
	}
	return hosts, nil
}

// addresses returns the addresses of host to connect to: IPv4 only, or
// IPv6 first and then IPv4 when IPv6 is allowed.
func (q *Queue) addresses(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addrs, err := q.opts.Resolver.LookupIPAddr(lctx, host)
	if err != nil {
		return nil, fmt.Errorf("looking up the address of %s failed", host)
	}
	var v4, v6 []net.IP
	for _, a := range addrs {
		if a.IP.To4() != nil {
			v4 = append(v4, a.IP)
		} else if q.opts.IPv6 {
			v6 = append(v6, a.IP)
		}
	}
	out := append(v6, v4...)
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no IPv4 address (sending over IPv6 is off)", host)
	}
	if len(out) > 5 {
		out = out[:5]
	}
	return out, nil
}

func humanDur(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d days", d/(24*time.Hour))
	}
	return d.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
