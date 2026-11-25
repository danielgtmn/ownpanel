// Package account is the self-service API of the mail service: a mailbox
// signs in with its address and main password at https://<mail host>/ and
// manages its own account (wiki/Groupware-Architecture.md, G1). Caddy
// passes /account/api/… to the mail service's web socket (package web),
// which hands it to this handler.
//
// Security rules:
//
//   - Only the main password signs in here, also when the mailbox lets mail
//     programs in with app passwords only; app passwords never do
//     (signin.Checker.CheckMain on a Checker and hashing slots of its own).
//   - Sessions live in mail.db, keyed by a hash of a random 256-bit id that
//     only the cookie __Host-ownpanel_mailbox holds (Secure, HttpOnly,
//     SameSite=Strict). They end after IdleTimeout without a request, after
//     MaxAge in any case, on sign-out, and as soon as the mailbox is gone or
//     its password is another one than at sign-in (checked on every request
//     against the directory, so a reset by the panel ends them at once).
//   - Every request that changes something needs the header
//     X-Ownpanel-Request: 1, and is refused when the browser says it comes
//     from another site (Origin, Sec-Fetch-Site).
//   - Changes go through server.ApplyAccountChange, which reports them to
//     the panel with hashes and MACs only. A new app password is returned
//     once and never stored, logged or reported.
package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/server"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
	"github.com/danielgtmn/ownpanel/internal/mail/web"
	"github.com/danielgtmn/ownpanel/internal/mailsetup"
	"github.com/danielgtmn/ownpanel/internal/passwd"
)

// Names and limits.
const (
	CookieName = "__Host-ownpanel_mailbox"
	// CSRFHeader must be "1" on every request that changes something (as
	// in the panel's API).
	CSRFHeader = "X-Ownpanel-Request"

	IdleTimeout = 30 * time.Minute
	MaxAge      = 12 * time.Hour

	// HashSlots is how many passwords the self-service area hashes at the
	// same time, apart from the slots of mail programs.
	HashSlots = 1
	// MaxBodyBytes bounds a request body.
	MaxBodyBytes = 16 << 10
	// WriteLimit changes per session per WriteWindow.
	WriteLimit  = 30
	WriteWindow = 15 * time.Minute

	// touchEvery: the last request of a session is written at most this
	// often.
	touchEvery = time.Minute
	// pruneEvery: how often ended sessions are deleted.
	pruneEvery = 10 * time.Minute
	// maxWriteWindows bounds the table of write limits.
	maxWriteWindows = 100_000
)

// Backend is what the handler needs of the mail service (*server.Server).
type Backend interface {
	Directory() *directory.Directory
	Store() *storage.Store
	ApplyAccountChange(ctx context.Context, storageID string, ch config.AccountChange) (config.AccountChange, error)
	RecordEvent(config.Event)
}

// Options configure a Handler.
type Options struct {
	Backend Backend
	Logger  *slog.Logger
	// Slots bound concurrent hashing; nil means passwd.NewSlots(HashSlots).
	Slots passwd.Slots
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
}

// Handler serves /account/api/… (everything but health, which package web
// answers).
type Handler struct {
	opts   Options
	log    *slog.Logger
	signIn *signin.Checker
	mux    *http.ServeMux

	wmu    sync.Mutex
	writes map[[32]byte]*writeWindow
}

type writeWindow struct {
	start time.Time
	n     int
}

// New returns the handler.
func New(opts Options) *Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Slots == nil {
		opts.Slots = passwd.NewSlots(HashSlots)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	h := &Handler{
		opts:   opts,
		log:    opts.Logger,
		signIn: signin.New(signin.Options{Slots: opts.Slots, Event: opts.Backend.RecordEvent, Now: opts.Now}),
		writes: map[[32]byte]*writeWindow{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /account/api/session", h.createSession)
	mux.HandleFunc("DELETE /account/api/session", h.deleteSession)
	mux.HandleFunc("GET /account/api/me", h.authed(h.me))
	mux.HandleFunc("PUT /account/api/password", h.authed(h.changePassword))
	mux.HandleFunc("PUT /account/api/display-name", h.authed(h.setDisplayName))
	mux.HandleFunc("PUT /account/api/app-passwords-only", h.authed(h.setAppPasswordsOnly))
	mux.HandleFunc("POST /account/api/app-passwords", h.authed(h.createAppPassword))
	mux.HandleFunc("DELETE /account/api/app-passwords/{id}", h.authed(h.revokeAppPassword))
	mux.HandleFunc("GET /account/api/apple-setup", h.authed(h.appleSetup))
	mux.HandleFunc("GET /account/api/collections", h.authed(h.collections))
	mux.HandleFunc("GET /account/api/collections/{kind}/{name}/export", h.authed(h.exportCollection))
	mux.HandleFunc("POST /account/api/collections/{kind}/{name}/import", h.authed(h.importCollection))
	mux.HandleFunc("/account/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Not found.")
	})
	h.mux = mux
	return h
}

var _ Backend = (*server.Server)(nil)

// Run deletes ended sessions now and then until ctx ends.
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

// Prune deletes the sessions that have ended: idle, too old, or of a
// mailbox that is gone or has another password now.
func (h *Handler) Prune(ctx context.Context) {
	now := h.opts.Now()
	dir := h.opts.Backend.Directory()
	n, err := h.opts.Backend.Store().PruneAccountSessions(ctx, now.Add(-IdleTimeout), now.Add(-MaxAge), func(a storage.AccountSession) bool {
		_, ok := current(dir, a)
		return ok
	})
	if err != nil && ctx.Err() == nil {
		h.log.Error("mail: prune self-service sessions", "err", err)
	} else if n > 0 {
		h.log.Debug("mail: self-service sessions ended", "count", n)
	}
	h.wmu.Lock()
	for k, w := range h.writes {
		if now.Sub(w.start) >= WriteWindow {
			delete(h.writes, k)
		}
	}
	h.wmu.Unlock()
}

// ServeHTTP checks the host and, for changing requests, the CSRF rules.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	host := h.opts.Backend.Directory().Hostname()
	if host == "" || !strings.EqualFold(hostOnly(r.Host), host) {
		writeError(w, http.StatusNotFound, "Not found.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if msg := crossSite(r); msg != "" {
			writeError(w, http.StatusForbidden, msg)
			return
		}
	}
	h.mux.ServeHTTP(w, r)
}

// crossSite tells why a changing request is refused, or "".
func crossSite(r *http.Request) string {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return "Requests from other web sites are not allowed."
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, r.Host) {
			return "Requests from other web sites are not allowed."
		}
	}
	if r.Header.Get(CSRFHeader) != "1" {
		return "This request is missing the " + CSRFHeader + " header."
	}
	return ""
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		hostport = h
	}
	return strings.TrimSuffix(hostport, ".")
}

// ---- sessions ----

// session is a signed-in request.
type session struct {
	hash    []byte
	mailbox config.Mailbox // a copy, as the directory has it now
}

// fingerprint ties a session to the password hash it was opened with.
func fingerprint(passwordHash string) string {
	s := sha256.Sum256([]byte("ownpanel self-service session\x00" + passwordHash))
	return hex.EncodeToString(s[:])
}

// current returns the mailbox of a session if the session is still valid
// for it: same storage, same address, same password.
func current(dir *directory.Directory, a storage.AccountSession) (*config.Mailbox, bool) {
	m, ok := dir.MailboxByStorageID(a.StorageID)
	if !ok || m.Address != a.Address || fingerprint(m.PasswordHash) != a.Credential {
		return nil, false
	}
	return m, true
}

func idHash(raw []byte) []byte {
	s := sha256.Sum256(raw)
	return s[:]
}

func (h *Handler) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: value, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter) { h.setCookie(w, "", -1) }

// sessionHash reads the cookie; nil if there is none or it is malformed.
func sessionHash(r *http.Request) []byte {
	c, err := r.Cookie(CookieName)
	if err != nil || len(c.Value) != 43 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != 32 {
		return nil
	}
	return idHash(raw)
}

var errEnded = "Your session has ended. Sign in again."

// authed runs fn for a valid session; otherwise it answers 401 and ends
// what is left of the session. Changes are limited per session.
func (h *Handler) authed(fn func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash := sessionHash(r)
		if hash == nil {
			h.clearCookie(w)
			writeError(w, http.StatusUnauthorized, errEnded)
			return
		}
		st := h.opts.Backend.Store()
		a, ok, err := st.AccountSessionByHash(r.Context(), hash)
		if err != nil {
			h.fail(w, err)
			return
		}
		now := h.opts.Now()
		var m *config.Mailbox
		if ok {
			if now.Sub(a.LastSeen) >= IdleTimeout || now.Sub(a.Created) >= MaxAge {
				ok = false
			} else {
				m, ok = current(h.opts.Backend.Directory(), a)
			}
			if !ok {
				if err := st.DeleteAccountSession(r.Context(), hash); err != nil {
					h.fail(w, err)
					return
				}
			}
		}
		if !ok {
			h.clearCookie(w)
			writeError(w, http.StatusUnauthorized, errEnded)
			return
		}
		if now.Sub(a.LastSeen) >= touchEvery {
			if err := st.TouchAccountSession(r.Context(), hash, now); err != nil {
				h.fail(w, err)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !h.allowWrite(hash, now) {
			writeError(w, http.StatusTooManyRequests, "Too many changes in a short time. Wait a few minutes and try again.")
			return
		}
		fn(w, r, &session{hash: hash, mailbox: *m})
	}
}

// allowWrite counts a change of the session; false when over WriteLimit.
func (h *Handler) allowWrite(hash []byte, now time.Time) bool {
	var k [32]byte
	copy(k[:], hash)
	h.wmu.Lock()
	defer h.wmu.Unlock()
	wnd := h.writes[k]
	if wnd == nil || now.Sub(wnd.start) >= WriteWindow {
		if wnd == nil && len(h.writes) >= maxWriteWindows {
			return false
		}
		wnd = &writeWindow{start: now}
		h.writes[k] = wnd
	}
	if wnd.n >= WriteLimit {
		return false
	}
	wnd.n++
	return true
}

func clientIP(r *http.Request) netip.Addr {
	// Unknown clients share one bucket of the throttle: never trusted,
	// never unlimited.
	ip, _ := web.ClientIP(r)
	return ip
}

// createSession signs a mailbox in.
func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Address  string `json:"address"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Address) == "" || in.Password == "" {
		writeError(w, http.StatusBadRequest, "Enter your address and password.")
		return
	}
	dir := h.opts.Backend.Directory()
	g, err := h.signIn.CheckMain(dir, clientIP(r), in.Address, in.Password)
	switch {
	case errors.Is(err, signin.ErrThrottled):
		writeError(w, http.StatusTooManyRequests, "Too many failed sign-ins. Try again in 15 minutes.")
		return
	case err != nil:
		writeError(w, http.StatusUnauthorized, "Wrong address or password.")
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		h.fail(w, err)
		return
	}
	now := h.opts.Now()
	hash := idHash(raw)
	if err := h.opts.Backend.Store().CreateAccountSession(r.Context(), hash, storage.AccountSession{
		StorageID: g.StorageID, Address: g.Address, Credential: fingerprint(g.PasswordHash), Created: now, LastSeen: now,
	}); err != nil {
		h.fail(w, err)
		return
	}
	h.setCookie(w, base64.RawURLEncoding.EncodeToString(raw), int(MaxAge/time.Second))
	m := g.Mailbox
	h.writeMe(w, r, http.StatusOK, &session{hash: hash, mailbox: m})
}

// deleteSession signs out; it succeeds also without a valid session.
func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	if hash := sessionHash(r); hash != nil {
		if err := h.opts.Backend.Store().DeleteAccountSession(r.Context(), hash); err != nil {
			h.fail(w, err)
			return
		}
	}
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// ---- the account ----

type appPasswordView struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	CreatedAt       time.Time  `json:"created_at"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	LastUsedNetwork string     `json:"last_used_network"`
}

type meView struct {
	Address          string            `json:"address"`
	DisplayName      string            `json:"display_name"`
	UsedBytes        int64             `json:"used_bytes"`
	Messages         int               `json:"messages"`
	QuotaBytes       int64             `json:"quota_bytes"`
	AppPasswords     []appPasswordView `json:"app_passwords"`
	AppPasswordsOnly bool              `json:"app_passwords_only"`
	// AppPasswordsAvailable is false while the panel has not set up app
	// passwords on this server.
	AppPasswordsAvailable bool `json:"app_passwords_available"`
	// UsesKnownDays is how far back the last use of app passwords is known.
	UsesKnownDays int                      `json:"uses_known_days"`
	Client        mailsetup.ClientSettings `json:"client"`
	// Warning is set only on the answer to a password change whose
	// password was changed but not everything that came with it.
	Warning string `json:"warning,omitempty"`
}

func (h *Handler) view(ctx context.Context, m *config.Mailbox) (meView, error) {
	dir := h.opts.Backend.Directory()
	st := h.opts.Backend.Store()
	u, err := st.Usage(ctx, m.StorageID)
	if err != nil {
		return meView{}, err
	}
	uses, err := st.AppPasswordUses(ctx, m.StorageID)
	if err != nil {
		return meView{}, err
	}
	v := meView{
		Address: m.Address, DisplayName: m.DisplayName, UsedBytes: u.Bytes, Messages: u.Messages, QuotaBytes: m.QuotaBytes,
		AppPasswords: []appPasswordView{}, AppPasswordsOnly: m.AppPasswordsOnly,
		AppPasswordsAvailable: dir.Config().AppPasswordKey != "",
		UsesKnownDays:         int(storage.EventRetention / (24 * time.Hour)),
		Client:                mailsetup.Settings(dir.Hostname()).WithDAV(),
	}
	for _, ap := range m.AppPasswords {
		pv := appPasswordView{ID: ap.ID, Name: ap.Name, CreatedAt: ap.Created.UTC()}
		if u, ok := uses[ap.ID]; ok {
			at := u.At
			pv.LastUsedAt, pv.LastUsedNetwork = &at, u.Network
		}
		v.AppPasswords = append(v.AppPasswords, pv)
	}
	return v, nil
}

func (h *Handler) writeMe(w http.ResponseWriter, r *http.Request, status int, s *session) {
	v, err := h.view(r.Context(), &s.mailbox)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, status, v)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request, s *session) {
	h.writeMe(w, r, http.StatusOK, s)
}

// apply makes a change and answers with the account as it is afterwards.
func (h *Handler) apply(w http.ResponseWriter, r *http.Request, s *session, ch config.AccountChange) bool {
	if _, err := h.opts.Backend.ApplyAccountChange(r.Context(), s.mailbox.StorageID, ch); err != nil {
		h.changeFailed(w, err)
		return false
	}
	return true
}

// refreshed is the session's mailbox as the directory has it now.
func (h *Handler) refreshed(w http.ResponseWriter, r *http.Request, s *session) {
	m, ok := h.opts.Backend.Directory().MailboxByStorageID(s.mailbox.StorageID)
	if !ok {
		h.clearCookie(w)
		writeError(w, http.StatusUnauthorized, errEnded)
		return
	}
	s.mailbox = *m
	h.writeMe(w, r, http.StatusOK, s)
}

func (h *Handler) changeFailed(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, server.ErrUnknownMailbox):
		h.clearCookie(w)
		writeError(w, http.StatusUnauthorized, errEnded)
	case errors.Is(err, server.ErrTooManyAppPasswords):
		writeError(w, http.StatusConflict, "You have as many app passwords as there can be ("+strconv.Itoa(config.MaxAppPasswords)+"). Revoke one you no longer use first.")
	case errors.Is(err, server.ErrNoAppPasswordKey):
		writeError(w, http.StatusConflict, "App passwords can't be created on this server yet. Ask the people who manage it.")
	case errors.Is(err, server.ErrInvalidChange):
		writeError(w, http.StatusBadRequest, "That change isn't possible.")
	default:
		h.fail(w, err)
	}
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		CurrentPassword    string `json:"current_password"`
		NewPassword        string `json:"new_password"`
		RevokeAppPasswords bool   `json:"revoke_app_passwords"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.CurrentPassword == "" {
		writeError(w, http.StatusBadRequest, "Enter your current password.")
		return
	}
	if err := config.CheckNewPassword(in.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, sentence(err.Error()))
		return
	}
	dir := h.opts.Backend.Directory()
	g, err := h.signIn.CheckMain(dir, clientIP(r), s.mailbox.Address, in.CurrentPassword)
	switch {
	case errors.Is(err, signin.ErrThrottled):
		writeError(w, http.StatusTooManyRequests, "Too many wrong passwords. Try again in 15 minutes.")
		return
	case err != nil || g.StorageID != s.mailbox.StorageID:
		writeError(w, http.StatusBadRequest, "Your current password isn't right.")
		return
	}
	hash, err := h.opts.Slots.Hash(in.NewPassword)
	if err != nil {
		h.fail(w, err)
		return
	}
	st := h.opts.Backend.Store()
	// This session stays signed in: it gets the new password's
	// fingerprint first (and back if the change fails), every other
	// session of the mailbox ends with the change.
	if err := st.SetAccountSessionCredential(r.Context(), s.hash, fingerprint(hash)); err != nil {
		h.fail(w, err)
		return
	}
	if _, err := h.opts.Backend.ApplyAccountChange(r.Context(), s.mailbox.StorageID, config.AccountChange{PasswordHash: hash}); err != nil {
		if rerr := st.SetAccountSessionCredential(context.WithoutCancel(r.Context()), s.hash, fingerprint(s.mailbox.PasswordHash)); rerr != nil {
			h.log.Error("mail: restore self-service session", "err", rerr)
		}
		h.changeFailed(w, err)
		return
	}
	if err := st.DeleteAccountSessions(r.Context(), s.mailbox.StorageID, s.hash); err != nil {
		h.log.Error("mail: end other self-service sessions", "err", err)
	}
	// The password is changed now: whatever happens to the app passwords,
	// the answer says so, and which are left.
	notRevoked := 0
	if in.RevokeAppPasswords {
		m, _ := h.opts.Backend.Directory().MailboxByStorageID(s.mailbox.StorageID)
		if m != nil {
			for _, ap := range m.AppPasswords {
				_, err := h.opts.Backend.ApplyAccountChange(r.Context(), s.mailbox.StorageID, config.AccountChange{AppPasswordID: ap.ID})
				if err != nil && !errors.Is(err, server.ErrInvalidChange) { // already revoked meanwhile
					h.log.Error("mail: revoke app password after a password change", "err", err)
					notRevoked++
				}
			}
		}
	}
	m, ok := h.opts.Backend.Directory().MailboxByStorageID(s.mailbox.StorageID)
	if !ok {
		h.clearCookie(w)
		writeError(w, http.StatusUnauthorized, errEnded)
		return
	}
	s.mailbox = *m
	v, err := h.view(r.Context(), &s.mailbox)
	if err != nil {
		h.fail(w, err)
		return
	}
	if notRevoked > 0 {
		v.Warning = notRevokedWarning(notRevoked)
	}
	writeJSON(w, http.StatusOK, v)
}

// notRevokedWarning says that the password was changed but n app
// passwords are still there.
func notRevokedWarning(n int) string {
	if n == 1 {
		return "Your password was changed, but 1 app password could not be revoked. Revoke it below."
	}
	return "Your password was changed, but " + strconv.Itoa(n) + " app passwords could not be revoked. Revoke them below."
}

func (h *Handler) setDisplayName(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		DisplayName string `json:"display_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.DisplayName)
	if !config.ValidDisplayName(name) {
		writeError(w, http.StatusBadRequest, "Use one line of at most "+strconv.Itoa(config.MaxDisplayName)+` characters, without < > and ".`)
		return
	}
	if name != s.mailbox.DisplayName && !h.apply(w, r, s, config.AccountChange{DisplayName: &name}) {
		return
	}
	h.refreshed(w, r, s)
}

func (h *Handler) setAppPasswordsOnly(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		AppPasswordsOnly *bool `json:"app_passwords_only"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.AppPasswordsOnly == nil {
		writeError(w, http.StatusBadRequest, "Say whether only app passwords may be used.")
		return
	}
	on := *in.AppPasswordsOnly
	if on != s.mailbox.AppPasswordsOnly && !h.apply(w, r, s, config.AccountChange{AppPasswordsOnly: &on}) {
		return
	}
	h.refreshed(w, r, s)
}

func (h *Handler) createAppPassword(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, `Enter the device or program, for example "Phone".`)
		return
	}
	if !config.ValidDisplayName(name) {
		writeError(w, http.StatusBadRequest, "Use one line of at most "+strconv.Itoa(config.MaxDisplayName)+` characters, without < > and ".`)
		return
	}
	key := h.opts.Backend.Directory().Config().AppPasswordKey
	if key == "" {
		h.changeFailed(w, server.ErrNoAppPasswordKey)
		return
	}
	id, secret, token := config.NewAppPassword()
	ap := config.AppPassword{
		ID: id, Name: name, MAC: config.AppPasswordMAC(key, s.mailbox.StorageID, id, secret),
		Created: h.opts.Now().UTC().Truncate(time.Second),
	}
	if !h.apply(w, r, s, config.AccountChange{AppPassword: &ap}) {
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		AppPassword appPasswordView `json:"app_password"`
		Password    string          `json:"password"`
	}{appPasswordView{ID: ap.ID, Name: ap.Name, CreatedAt: ap.Created}, token})
}

func (h *Handler) revokeAppPassword(w http.ResponseWriter, r *http.Request, s *session) {
	id := r.PathValue("id")
	found := false
	for _, ap := range s.mailbox.AppPasswords {
		found = found || ap.ID == id
	}
	if !config.ValidAppPasswordID(id) || !found {
		writeError(w, http.StatusNotFound, "That app password doesn't exist (anymore).")
		return
	}
	if !h.apply(w, r, s, config.AccountChange{AppPasswordID: id}) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) appleSetup(w http.ResponseWriter, r *http.Request, s *session) {
	body := mailsetup.AppleProfile(s.mailbox.Address, mailsetup.Settings(h.opts.Backend.Directory().Hostname()).WithDAV())
	hd := w.Header()
	hd.Set("Content-Type", mailsetup.ProfileContentType)
	hd.Set("Content-Disposition", `attachment; filename="`+mailsetup.ProfileFileName(s.mailbox.Address)+`"`)
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// ---- helpers ----

// decode reads a JSON body of at most MaxBodyBytes into v; on failure it
// has answered.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Send the request as JSON.")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "The request is too large.")
		} else {
			writeError(w, http.StatusBadRequest, "The request isn't valid.")
		}
		return false
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "The request isn't valid.")
		return false
	}
	return true
}

// sentence starts msg with a capital letter and ends it with a full stop.
func sentence(msg string) string {
	r, n := utf8.DecodeRuneInString(msg)
	msg = string(unicode.ToUpper(r)) + msg[n:]
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail answers 500; the error goes to the log (store errors carry no
// secrets).
func (h *Handler) fail(w http.ResponseWriter, err error) {
	h.log.Error("mail: self-service request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "Something went wrong on the server. Try again in a moment.")
}
