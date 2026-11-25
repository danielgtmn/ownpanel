// Package dav serves calendars (CalDAV, RFC 4791) and contacts (CardDAV,
// RFC 6352) of mailboxes, with sync (RFC 6578), at https://<mail host>/dav/
// (wiki/Groupware-Architecture.md, G4a). Caddy passes /dav/… and
// /.well-known/caldav|carddav to the mail service's web socket (package
// web), which hands them to this handler.
//
// The tree:
//
//	/dav/                                        root (discovery starts here)
//	/dav/principals/<address>/                   the mailbox as a principal
//	/dav/calendars/<address>/                    calendar home
//	/dav/calendars/<address>/<calendar>/<object> calendars and their objects
//	/dav/addressbooks/<address>/                 address book home
//	/dav/addressbooks/<address>/<book>/<object>  address books and contacts
//	/dav/addressbooks/<address>/directory/       the organisation's directory (read only)
//
// Security rules:
//
//   - Every request but OPTIONS signs in with HTTP Basic over the TLS hop
//     Caddy terminates: the mailbox's address and its main password
//     (unless the mailbox allows mail programs app passwords only) or an
//     app password, through the same signin.Checker as IMAP and SMTP, so
//     its throttle (per address and network) and cache apply.
//   - Requests are counted per mailbox (RateLimit per RateWindow).
//   - Which homes and collections a mailbox sees is decided by an
//     Authorizer on every request; resources it may not see answer 404.
//   - Request bodies are untrusted: sizes, XML depth and element counts
//     are bounded, objects are parsed and checked before they are stored.
//
// The XML of the protocols is read and written here (encoding/xml);
// iCalendar and vCard data is parsed by package davobj.
package dav

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
	"github.com/danielgtmn/ownpanel/internal/mail/web"
)

// Limits.
const (
	// RateLimit requests per mailbox per RateWindow.
	RateLimit  = 3000
	RateWindow = 5 * time.Minute
	// maxRateEntries bounds the table of request counts.
	maxRateEntries = 100_000
	pruneEvery     = time.Hour
)

// Prefix is where the tree starts.
const Prefix = "/dav/"

// Backend is what the handler needs of the mail service (*server.Server).
type Backend interface {
	Directory() *directory.Directory
	Store() *storage.Store
	SignIn() *signin.Checker
}

// Options configure a Handler.
type Options struct {
	Backend Backend
	// Authorizer decides who sees what; nil means OwnOnly.
	Authorizer Authorizer
	Logger     *slog.Logger
	// QueryBudget is the time one calendar query may spend expanding
	// recurrences; 0 means davobj.DefaultQueryBudget.
	QueryBudget time.Duration
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
}

// Handler serves /dav/… and /.well-known/caldav|carddav.
type Handler struct {
	opts Options
	log  *slog.Logger

	rmu   sync.Mutex
	rates map[string]*rateWindow

	dirMu    sync.Mutex
	dirFor   *directory.Directory
	dirCache map[string]*dirBook
}

type rateWindow struct {
	start time.Time
	n     int
}

// New returns the handler.
func New(opts Options) *Handler {
	if opts.Authorizer == nil {
		opts.Authorizer = OwnOnly{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Handler{opts: opts, log: opts.Logger, rates: map[string]*rateWindow{}, dirCache: map[string]*dirBook{}}
}

// Run prunes old deletions and request counts now and then until ctx
// ends.
func (h *Handler) Run(ctx context.Context) {
	t := time.NewTicker(pruneEvery)
	defer t.Stop()
	for {
		h.Prune(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Prune forgets deletions older than storage.DAVDeletionRetention and
// expired request counts.
func (h *Handler) Prune(ctx context.Context) {
	if err := h.opts.Backend.Store().PruneDAVDeletions(ctx); err != nil && ctx.Err() == nil {
		h.log.Error("mail: prune calendar and contact deletions", "err", err)
	}
	now := h.opts.Now()
	h.rmu.Lock()
	for k, w := range h.rates {
		if now.Sub(w.start) >= RateWindow {
			delete(h.rates, k)
		}
	}
	h.rmu.Unlock()
}

// allow counts a request of a mailbox; false when over RateLimit.
func (h *Handler) allow(storageID string) bool {
	now := h.opts.Now()
	h.rmu.Lock()
	defer h.rmu.Unlock()
	w := h.rates[storageID]
	if w == nil || now.Sub(w.start) >= RateWindow {
		if w == nil && len(h.rates) >= maxRateEntries {
			return false
		}
		w = &rateWindow{start: now}
		h.rates[storageID] = w
	}
	if w.n >= RateLimit {
		return false
	}
	w.n++
	return true
}

// davHeader lists what the server supports. Class 2 (locking) is not
// offered.
const davHeader = "1, 3, access-control, calendar-access, addressbook, extended-mkcol"

const allowHeader = "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, REPORT, MKCOL, MKCALENDAR"

// request is one authenticated request.
type request struct {
	ctx  context.Context
	dir  *directory.Directory
	st   *storage.Store
	user *config.Mailbox
}

// ServeHTTP authenticates and dispatches.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("DAV", davHeader)
	dir := h.opts.Backend.Directory()
	host := dir.Hostname()
	if host == "" || !strings.EqualFold(hostOnly(r.Host), host) {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/.well-known/caldav" || r.URL.Path == "/.well-known/carddav" {
		switch r.Method {
		case http.MethodGet, http.MethodHead, "PROPFIND", http.MethodOptions:
			hd.Set("Location", Prefix)
			w.WriteHeader(http.StatusMovedPermanently)
		default:
			hd.Set("Allow", "OPTIONS, GET, HEAD, PROPFIND")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if r.Method == http.MethodOptions {
		hd.Set("Allow", allowHeader)
		w.WriteHeader(http.StatusOK)
		return
	}
	user, ok := h.authenticate(w, r, dir)
	if !ok {
		return
	}
	if !h.allow(user.StorageID) {
		hd.Set("Retry-After", strconv.Itoa(int(RateWindow/time.Second)))
		http.Error(w, "Too many requests. Wait a few minutes.", http.StatusTooManyRequests)
		return
	}
	rq := &request{ctx: r.Context(), dir: dir, st: h.opts.Backend.Store(), user: user}
	if err := rq.st.EnsureDefaultDAVCollections(rq.ctx, user.StorageID); err != nil {
		h.fail(w, err)
		return
	}
	res, err := h.resolve(rq, r.URL.Path)
	if err != nil {
		h.fail(w, err)
		return
	}
	switch r.Method {
	case "PROPFIND":
		h.propfind(w, r, rq, res)
	case "PROPPATCH":
		h.proppatch(w, r, rq, res)
	case "REPORT":
		h.report(w, r, rq, res)
	case http.MethodGet, http.MethodHead:
		h.get(w, r, rq, res)
	case http.MethodPut:
		h.put(w, r, rq, res)
	case http.MethodDelete:
		h.delete(w, r, rq, res)
	case "MKCALENDAR", "MKCOL":
		h.mkcol(w, r, rq, res)
	default:
		hd.Set("Allow", allowHeader)
		http.Error(w, "This method is not supported here.", http.StatusMethodNotAllowed)
	}
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		hostport = h
	}
	return strings.TrimSuffix(hostport, ".")
}

// authenticate checks HTTP Basic credentials; on failure it has answered.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request, dir *directory.Directory) (*config.Mailbox, bool) {
	challenge := func() {
		w.Header().Set("WWW-Authenticate", `Basic realm="Calendars and contacts", charset="UTF-8"`)
		http.Error(w, "Sign in with your e-mail address and password.", http.StatusUnauthorized)
	}
	user, pass, ok := basicAuth(r)
	if !ok {
		challenge()
		return nil, false
	}
	ip, _ := web.ClientIP(r) // unknown clients share one bucket
	g, err := h.opts.Backend.SignIn().Check(dir, ip, user, pass)
	switch {
	case errors.Is(err, signin.ErrThrottled):
		w.Header().Set("Retry-After", strconv.Itoa(int(signin.Window/time.Second)))
		http.Error(w, "Too many failed sign-ins. Try again in 15 minutes.", http.StatusTooManyRequests)
		return nil, false
	case err != nil:
		challenge()
		return nil, false
	}
	m, ok := dir.MailboxByStorageID(g.StorageID)
	if !ok {
		challenge()
		return nil, false
	}
	return m, true
}

// basicAuth reads HTTP Basic credentials, bounded in size.
func basicAuth(r *http.Request) (user, pass string, ok bool) {
	v := r.Header.Get("Authorization")
	const prefix = "Basic "
	if len(v) < len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) || len(v) > 4096 {
		return "", "", false
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	user, pass, ok = strings.Cut(string(b), ":")
	return user, pass, ok && user != ""
}

// ---- resources ----

type resType int

const (
	resNone resType = iota
	resRoot
	resPrincipal
	resHome
	resCollection
	resObject
)

// resource is what a path names. For a collection or object that does not
// exist (yet), typ is set and exists is false.
type resource struct {
	typ    resType
	path   string
	owner  *config.Mailbox
	kind   string // davobj.Calendar or davobj.AddressBook (homes and below)
	privs  Privileges
	coll   *storage.DAVCollection
	dir    *dirBook // the directory address book
	name   string   // collection name for resCollection, object name for resObject
	obj    *storage.DAVObject
	card   *dirCard // an object of the directory
	exists bool
}

// directoryName is the reserved name of the directory address book.
const directoryName = "directory"

func homeSegment(kind string) string {
	if kind == davobj.Calendar {
		return "calendars"
	}
	return "addressbooks"
}

func principalPath(m *config.Mailbox) string {
	return Prefix + "principals/" + url.PathEscape(m.Address) + "/"
}

func homePath(m *config.Mailbox, kind string) string {
	return Prefix + homeSegment(kind) + "/" + url.PathEscape(m.Address) + "/"
}

func collectionPath(m *config.Mailbox, kind, name string) string {
	return homePath(m, kind) + url.PathEscape(name) + "/"
}

func objectPath(m *config.Mailbox, kind, coll, name string) string {
	return collectionPath(m, kind, coll) + url.PathEscape(name)
}

// resolve finds what a path names. Unknown paths and resources the user
// may not see give resNone.
func (h *Handler) resolve(rq *request, p string) (*resource, error) {
	none := &resource{typ: resNone, path: p}
	if p != "/dav" && !strings.HasPrefix(p, Prefix) {
		return none, nil
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(p, "/dav"), "/")
	trailing := strings.HasSuffix(trimmed, "/")
	trimmed = strings.TrimSuffix(trimmed, "/")
	var segs []string
	if trimmed != "" {
		segs = strings.Split(trimmed, "/")
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return none, nil
		}
	}
	if len(segs) == 0 {
		return &resource{typ: resRoot, path: Prefix, privs: PrivRead, exists: true}, nil
	}
	if len(segs) < 2 {
		return none, nil
	}
	owner, ok := h.opts.Authorizer.Home(rq.dir, rq.user, segs[1])
	if !ok {
		return none, nil
	}
	switch segs[0] {
	case "principals":
		if len(segs) != 2 {
			return none, nil
		}
		return &resource{typ: resPrincipal, path: principalPath(owner), owner: owner, privs: PrivRead, exists: true}, nil
	case "calendars", "addressbooks":
	default:
		return none, nil
	}
	kind := davobj.Calendar
	if segs[0] == "addressbooks" {
		kind = davobj.AddressBook
	}
	homePrivs := h.opts.Authorizer.Collection(rq.dir, rq.user, owner, kind, "")
	if homePrivs == PrivNone {
		return none, nil
	}
	if len(segs) == 2 {
		return &resource{typ: resHome, path: homePath(owner, kind), owner: owner, kind: kind, privs: homePrivs, exists: true}, nil
	}
	if len(segs) > 4 {
		return none, nil
	}
	collName := segs[2]
	res := &resource{typ: resCollection, owner: owner, kind: kind, name: collName, path: collectionPath(owner, kind, collName)}
	if kind == davobj.AddressBook && collName == directoryName && owner.StorageID == rq.user.StorageID {
		res.dir = h.directoryBook(rq.dir, rq.user)
		res.privs, res.exists = PrivRead, true
	} else {
		res.privs = h.opts.Authorizer.Collection(rq.dir, rq.user, owner, kind, collName)
		if res.privs == PrivNone {
			return none, nil
		}
		c, err := rq.st.DAVCollection(rq.ctx, owner.StorageID, kind, collName)
		switch {
		case errors.Is(err, storage.ErrNoCollection):
			if len(segs) == 4 {
				return none, nil
			}
			// A new collection may be created here.
			res.privs = homePrivs
			return res, nil
		case err != nil:
			return nil, err
		}
		res.coll, res.exists = &c, true
	}
	if len(segs) == 3 {
		return res, nil
	}
	if trailing {
		return none, nil
	}
	res.typ, res.name, res.exists = resObject, segs[3], false
	res.path = objectPath(owner, kind, collName, segs[3])
	if res.dir != nil {
		if card := res.dir.byName[res.name]; card != nil {
			res.card, res.exists = card, true
		}
		return res, nil
	}
	if !storage.ValidDAVName(res.name) {
		return none, nil
	}
	o, err := rq.st.DAVObject(rq.ctx, *res.coll, res.name)
	switch {
	case errors.Is(err, storage.ErrNoObject):
	case err != nil:
		return nil, err
	default:
		res.obj, res.exists = &o, true
	}
	return res, nil
}

// resolveHref resolves an href of a REPORT (a path or a URL of this host).
func (h *Handler) resolveHref(rq *request, raw string) (*resource, string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return &resource{typ: resNone}, raw, nil
	}
	res, err := h.resolve(rq, u.Path)
	return res, raw, err
}

// fail answers 500; the error goes to the log.
func (h *Handler) fail(w http.ResponseWriter, err error) {
	h.log.Error("mail: calendar or contacts request failed", "err", err)
	http.Error(w, "Something went wrong on the server. Try again in a moment.", http.StatusInternalServerError)
}

