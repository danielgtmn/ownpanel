package smtpd

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// limitListener accepts connections and refuses (with a 421 line) those
// over the limit per IP address or in total, before any SMTP code runs.
type limitListener struct {
	net.Listener
	perIP atomic.Int64
	total int
	idle  time.Duration

	// refusal is written to refused connections; "" closes them silently
	// (for ports with implicit TLS, where plain text means nothing).
	refusal string

	mu    sync.Mutex
	count int
	byIP  map[string]int
}

func newLimitListener(l net.Listener, perIP, total int, idle time.Duration) *limitListener {
	ll := &limitListener{Listener: l, total: total, idle: idle, byIP: map[string]int{},
		refusal: "421 4.7.0 Too many connections, try again later\r\n"}
	ll.perIP.Store(int64(perIP))
	return ll
}

// LimitListener limits connections per IP address and in total for the
// other listeners of the mail service (IMAP, submission).
type LimitListener struct{ *limitListener }

// NewLimitListener wraps l. Connections over the limits get refusal (may
// be "") and are closed before any protocol code runs. idle > 0 applies an
// idle timeout to every read.
func NewLimitListener(l net.Listener, perIP, total int, idle time.Duration, refusal string) LimitListener {
	ll := newLimitListener(l, perIP, total, idle)
	ll.refusal = refusal
	return LimitListener{ll}
}

// SetPerIP changes the limit per IP address for new connections.
func (l LimitListener) SetPerIP(n int) { l.perIP.Store(int64(n)) }

// Accept returns the next connection within the limits. Refused
// connections are answered and closed here.
func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := hostOf(c.RemoteAddr())
		l.mu.Lock()
		ok := l.count < l.total && int64(l.byIP[ip]) < l.perIP.Load()
		if ok {
			l.count++
			l.byIP[ip]++
		}
		l.mu.Unlock()
		if !ok {
			go refuse(c, l.refusal)
			continue
		}
		return &limitConn{Conn: c, l: l, ip: ip, idle: l.idle}, nil
	}
}

func (l *limitListener) release(ip string) {
	l.mu.Lock()
	l.count--
	if l.byIP[ip]--; l.byIP[ip] <= 0 {
		delete(l.byIP, ip)
	}
	l.mu.Unlock()
}

// refuse writes one line and closes, without waiting long for a client that
// does not read.
func refuse(c net.Conn, line string) {
	if line == "" {
		c.Close()
		return
	}
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte(line))
	c.Close()
}

func hostOf(a net.Addr) string {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.IP.String()
	}
	h, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return h
}

// limitConn frees its slot on Close and applies an idle timeout to every
// read: a client may take long in total (a large message on a slow line)
// but may not stall. go-smtp's own ReadTimeout is absolute per command and
// would cut off a slow DATA transfer, so it stays unset.
type limitConn struct {
	net.Conn
	l    *limitListener
	ip   string
	idle time.Duration
	once sync.Once
}

func (c *limitConn) Read(b []byte) (int, error) {
	if c.idle > 0 {
		c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	}
	return c.Conn.Read(b)
}

func (c *limitConn) Close() error {
	c.once.Do(func() { c.l.release(c.ip) })
	return c.Conn.Close()
}

// chanListener hands connections from the accept loop to one go-smtp
// server generation.
type chanListener struct {
	addr   net.Addr
	ch     chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newChanListener(addr net.Addr) *chanListener {
	return &chanListener{addr: addr, ch: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

// deliver hands c to the server; false if the listener was closed.
func (l *chanListener) deliver(c net.Conn) bool {
	select {
	case l.ch <- c:
		return true
	case <-l.closed:
		return false
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }
