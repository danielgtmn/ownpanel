package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mailsetup"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// keyMailEventsCursor is the id of the last mail service event recorded.
const keyMailEventsCursor = "mail_events_cursor"

// errMailCertsLoading: nothing is pushed until the mail host's certificate
// was loaded once, so a restart of the panel never pushes a configuration
// without it.
var errMailCertsLoading = errors.New("the certificate of the mail server is still being loaded")

// buildMailConfig builds the complete configuration of the mail service
// from the store: active domains with their DKIM keys, their mailboxes in
// the order they were created, their aliases, the mail host with its
// certificates and the postmaster address. It contains secrets.
func (s *Service) buildMailConfig(ctx context.Context) (*config.Config, error) {
	cfg := &config.Config{
		Hostname:          s.MailHost(),
		Certificates:      s.mailCertificates(),
		Domains:           []config.Domain{},
		Mailboxes:         []config.Mailbox{},
		Aliases:           []config.Alias{},
		PostmasterAddress: s.state().postmasterAddress,
	}
	if cfg.Certificates == nil {
		cfg.Certificates = []config.Certificate{}
	}
	filter, err := s.mailFilter()
	if err != nil {
		return nil, err
	}
	cfg.Filter = filter
	if s.secrets != nil {
		// The key exists from the start, so the mail service can create
		// app passwords before the panel ever did.
		if cfg.AppPasswordKey, err = s.appPasswordKey(ctx); err != nil {
			return nil, err
		}
	}
	domains, err := s.store.MailDomains(ctx)
	if err != nil {
		return nil, err
	}
	active := map[string]bool{}
	if cfg.Hostname != "" {
		for _, d := range domains {
			if !d.Active || d.Domain == cfg.Hostname {
				continue
			}
			active[d.Domain] = true
			// A domain that stands alone is pushed without an organisation:
			// the mail service treats it as its own (StandaloneOrganisation).
			// "dom-" and a domain of up to 253 characters would not fit an
			// organisation id and fail the push for everyone.
			cd := config.Domain{Name: d.Domain, DKIM: []config.DKIMKey{}, SendPerHour: config.DefaultDomainSendPerHour,
				Organisation: d.OrganisationID}
			keys, err := s.store.DKIMKeys(ctx, d.Domain)
			if err != nil {
				return nil, err
			}
			for _, k := range keys {
				if k.State == store.DKIMRetired {
					continue
				}
				if s.secrets == nil {
					return nil, errors.New("no secret key configured; DKIM keys cannot be read")
				}
				pemKey, err := s.secrets.Open(k.PrivateKey, dkimPurpose)
				if err != nil {
					return nil, fmt.Errorf("the DKIM key %s of %s cannot be read (has the server's secret key changed?)", k.Selector, d.Domain)
				}
				cd.DKIM = append(cd.DKIM, config.DKIMKey{Selector: k.Selector, PrivateKeyPEM: string(pemKey), Active: k.State == store.DKIMActive})
			}
			cfg.Domains = append(cfg.Domains, cd)
		}
	}
	mailboxes, err := s.store.Mailboxes(ctx, "")
	if err != nil {
		return nil, err
	}
	appPasswords, err := s.store.AppPasswords(ctx, 0)
	if err != nil {
		return nil, err
	}
	byMailbox := map[int64][]config.AppPassword{}
	for _, ap := range appPasswords {
		byMailbox[ap.MailboxID] = append(byMailbox[ap.MailboxID], config.AppPassword{ID: ap.ID, Name: ap.Name, MAC: ap.MAC, Created: ap.CreatedAt})
	}
	grants, err := s.store.MailboxGrants(ctx, 0, 0)
	if err != nil {
		return nil, err
	}
	grantsOf := configGrants(grants)
	for _, m := range mailboxes {
		if active[m.Domain] {
			cm := config.Mailbox{
				StorageID: m.StorageID, Address: m.LocalPart + "@" + m.Domain, PasswordHash: m.PasswordHash,
				QuotaBytes: m.QuotaBytes, SendPerHour: m.SendPerHour, SendPerDay: m.SendPerDay,
				DisplayName: m.DisplayName, AppPasswords: byMailbox[m.ID], AppPasswordsOnly: m.AppPasswordsOnly,
				Revision: m.Revision, PasswordSetAt: unixOrZero(m.AdminPasswordAt),
				DisplayNameSetAt: unixOrZero(m.AdminDisplayNameAt), AppPasswordsOnlySetAt: unixOrZero(m.AdminAppPasswordsOnlyAt),
				Grants: grantsOf[m.ID], GrantsSetAt: unixOrZero(m.AdminGrantsAt),
			}
			// Personal is pushed as "", so configurations without shared
			// mailboxes stay as they were.
			if m.Kind == config.KindShared {
				cm.Kind, cm.KeepSentCopy = config.KindShared, m.KeepSentCopy
			}
			cfg.Mailboxes = append(cfg.Mailboxes, cm)
		}
	}
	aliases, err := s.store.MailAliases(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, a := range aliases {
		if active[a.Domain] {
			ca := config.Alias{Address: a.LocalPart + "@" + a.Domain, Targets: a.Targets, HideFromDirectory: a.HideFromDirectory}
			if a.SenderPolicy != config.SenderAnyone {
				ca.SenderPolicy = a.SenderPolicy
			}
			cfg.Aliases = append(cfg.Aliases, ca)
		}
	}
	// One domain, mailbox or alias the mail service would refuse must not
	// stop the push for everyone: it is left out or cut down, and logged.
	s.logLeftOut(cfg.DropInvalid())
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("the mail configuration is not valid: %w", err)
	}
	return cfg, nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// logLeftOut logs what buildMailConfig left out of the configuration, once
// for as long as it stays the same (the configuration is built on every
// round of the background loop and for the status).
func (s *Service) logLeftOut(notes []string) {
	key := strings.Join(notes, "\n")
	s.mailLeftOutMu.Lock()
	defer s.mailLeftOutMu.Unlock()
	if key == s.mailLeftOut {
		return
	}
	s.mailLeftOut = key
	for _, n := range notes {
		slog.Error("mail configuration: left out of the push", "what", n)
	}
}

// mailSync pushes the configuration to the mail service when it differs
// from the one the service reports, then asks it to delete the data of
// deleted mailboxes. Without a mail service it does nothing. It has its own
// lock and timeout, independent of the web server sync.
func (s *Service) mailSync(ctx context.Context) error {
	if s.mail == nil {
		return nil
	}
	s.mailSyncMu.Lock()
	defer s.mailSyncMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	if !s.mailCertsLoaded() {
		return errMailCertsLoading
	}
	cfg, err := s.buildMailConfig(ctx)
	if err != nil {
		return err
	}
	st, err := s.mail.Status(ctx)
	if err != nil {
		return err
	}
	if st.ConfigHash != cfg.Hash() {
		if err := s.mail.Load(ctx, cfg); err != nil {
			return err
		}
	}
	return s.deletePendingMailboxes(ctx)
}

// deletePendingMailboxes asks the mail service to delete the data of
// deleted mailboxes; those it confirms are forgotten.
func (s *Service) deletePendingMailboxes(ctx context.Context) error {
	ids, err := s.store.PendingMailDeletes(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, id := range ids {
		if !config.ValidStorageID(id) {
			slog.Error("dropping an invalid pending mailbox delete", "storage_id", id)
			_ = s.store.DonePendingMailDelete(ctx, id)
			continue
		}
		if err := s.mail.DeleteMailbox(ctx, id); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := s.store.DonePendingMailDelete(ctx, id); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// mailSaved syncs the mail service after a change that is already saved.
// On failure the background loop retries; the caller is still told.
func (s *Service) mailSaved(ctx context.Context) error {
	err := s.mailSync(ctx)
	if err == nil {
		return nil
	}
	slog.Error("mail sync failed", "err", err)
	return &Error{
		Kind: NotSynced,
		Msg:  "saved, but the mail server could not be updated yet: " + err.Error(),
		Err:  err,
	}
}

// SyncMail is mailSync for the system and unrestricted admins.
func (s *Service) SyncMail(ctx context.Context, a Actor) error {
	if a.Via != ViaSystem {
		if err := needAdmin(a); err != nil {
			return err
		}
	}
	return s.mailSync(ctx)
}

// importEventActions names the activity of the service's import events
// like the panel's own (mail.mailbox.import-started).
var importEventActions = map[string]string{
	config.EventImportFinished:    "mail.mailbox.import-finished",
	config.EventImportFailed:      "mail.mailbox.import-failed",
	config.EventImportPaused:      "mail.mailbox.import-paused",
	config.EventImportInterrupted: "mail.mailbox.import-interrupted",
}

// keyMailEventsPending holds the account events that could not be
// recorded yet (JSON), oldest first; see recordMailEvents.
const keyMailEventsPending = "mail_events_pending"

// maxPendingAccountEvents bounds keyMailEventsPending.
const maxPendingAccountEvents = 1000

// recordMailEvents copies new events of the mail service into the
// activity log and remembers how far it got.
//
// Account events (applyAccountEvent) of one mailbox must be recorded in
// order. One that cannot be recorded (the database failed) is kept in
// keyMailEventsPending together with every later account event of the
// same mailbox, and tried again first on every round; events of other
// mailboxes and all other events go on. Until then the mail service keeps
// the mailbox's change (its overlay), so nothing is lost.
func (s *Service) recordMailEvents(ctx context.Context) error {
	if s.mail == nil {
		return nil
	}
	s.mailEventsMu.Lock()
	defer s.mailEventsMu.Unlock()
	m, err := s.store.Settings(ctx)
	if err != nil {
		return err
	}
	cursor, _ := strconv.ParseInt(m[keyMailEventsCursor], 10, 64)
	var pending []config.Event
	if v := m[keyMailEventsPending]; v != "" {
		if err := json.Unmarshal([]byte(v), &pending); err != nil {
			slog.Error("dropping damaged pending mailbox changes", "err", err)
			pending = nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	events, fetchErr := s.mail.Events(ctx, cursor)

	held := map[string]bool{} // storage ids whose account events wait
	var still []config.Event
	accountChanged := false
	apply := func(e config.Event, retry bool) {
		if held[e.StorageID] {
			still = append(still, e)
			return
		}
		changed, err := s.applyAccountEvent(ctx, e)
		if err != nil {
			held[e.StorageID] = true
			still = append(still, e)
			if !retry {
				slog.Error("recording a mailbox change failed; it is tried again", "kind", e.Kind, "err", err)
			}
			return
		}
		accountChanged = accountChanged || changed
	}
	for _, e := range pending {
		apply(e, true)
	}
	if len(still) > 0 && len(pending) > 0 {
		slog.Warn("mailbox changes of the mail server are still not recorded", "count", len(still))
	}

	last := cursor
	refusals := blocklistRefusals(m)
	refused := false
	for _, e := range events {
		if e.ID <= last {
			continue
		}
		last = e.ID
		if accountEventKinds[e.Kind] {
			apply(e, false)
			continue
		}
		target := maskSecretZones(e.Address)
		if target == "" {
			target = e.StorageID
		}
		// Event details name blocklist zones, whose access keys stay out
		// of the activity log.
		detail := maskSecretZones(e.Detail)
		if e.Kind == config.EventBlocklistRefused {
			if z := refusedZoneRe.FindStringSubmatch(detail); z != nil {
				at := e.At
				if at.IsZero() || at.After(s.now()) {
					at = s.now()
				}
				if at.After(refusals[z[1]]) {
					refusals[z[1]] = at
				}
				refused = true
				if target == "" {
					target = z[1]
				}
			}
		}
		if utf8.RuneCountInString(detail) > 500 {
			detail = string([]rune(detail)[:499]) + "…"
		}
		if !utf8.ValidString(detail) {
			detail = ""
		}
		action := "mail." + e.Kind
		if a, ok := importEventActions[e.Kind]; ok {
			action = a
		}
		s.record(ctx, mailActor, action, target, detail)
	}
	if len(still) > maxPendingAccountEvents {
		slog.Error("too many mailbox changes of the mail server not recorded; dropping the newest", "dropped", len(still)-maxPendingAccountEvents)
		still = still[:maxPendingAccountEvents]
	}
	pendingValue := ""
	if len(still) > 0 {
		b, err := json.Marshal(still)
		if err != nil {
			return err
		}
		pendingValue = string(b)
	}
	if last != cursor || pendingValue != m[keyMailEventsPending] || refused {
		values := map[string]string{keyMailEventsCursor: strconv.FormatInt(last, 10), keyMailEventsPending: pendingValue}
		if refused {
			values[keyBlocklistRefused] = encodeRefusals(refusals, s.now())
		}
		if err := s.store.PutSettings(ctx, values); err != nil {
			return err
		}
	}
	if accountChanged {
		// The mail service keeps its own change until a push includes it;
		// push now rather than on the next round.
		if err := s.mailSync(ctx); err != nil {
			return err
		}
	}
	return fetchErr
}

// catchUpMailEvents records what mailboxes changed themselves in the mail
// service before an admin changes their password, display name or switch:
// the push that follows then carries a revision that includes those
// changes, and the admin's value takes effect at once rather than after
// the next round of the background loop. (The push also says when the
// admin set the field, so the mail service gives way even if this fails;
// see config.Mailbox.PasswordSetAt.) Failures are logged only.
func (s *Service) catchUpMailEvents(ctx context.Context) {
	if err := s.recordMailEvents(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("reading the mail server's events before a change failed", "err", err)
	}
}

// runMail is the mail part of Run: it activates domains whose MX record
// now points here, keeps the mail service in sync, records its events and
// looks after the mail host's certificate. Nothing here can block or fail
// the web server sync.
func (s *Service) runMail(ctx context.Context, interval time.Duration) {
	if s.certs != nil {
		go s.runMailCerts(ctx)
	}
	healthy := true
	for {
		s.activateMailDomains(ctx)
		if s.checkMailSetupHosts(ctx) && ctx.Err() == nil {
			// A name for setting up mail programs points here now (or no
			// more): the web server serves it from now on (or stops).
			if err := s.sync(ctx); err != nil {
				slog.Warn("web server not in sync", "err", err)
			}
		}
		err := s.mailSync(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			if healthy || !errors.Is(err, errMailCertsLoading) {
				slog.Warn("mail server not in sync", "err", err)
			}
			healthy = false
		case err == nil && !healthy:
			slog.Info("mail server back in sync")
			healthy = true
		}
		if err := s.recordMailEvents(ctx); err != nil && ctx.Err() == nil && healthy {
			slog.Warn("reading the mail server's events failed", "err", err)
		}
		wait := interval
		if !healthy {
			wait = s.unhealthyRetry
			if wait < 5*time.Second && s.mail != nil {
				wait = 5 * time.Second
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// ---- status ----

// MailListener is the state of one port of the mail service.
type MailListener struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	Listening bool   `json:"listening"`
	Error     string `json:"error"`
}

// MailClientSettings are what people enter in their mail program.
type MailClientSettings = mailsetup.ClientSettings

// MailStatus tells how the mail service is doing.
type MailStatus struct {
	MailHost string `json:"mail_host"`
	// ServiceSetUp is false when the panel runs without a mail service;
	// mail domains and mailboxes are then only stored.
	ServiceSetUp bool `json:"service_set_up"`
	Reachable    bool `json:"reachable"`
	// InSync: the service runs the configuration the panel would push.
	InSync             bool               `json:"in_sync"`
	Problem            string             `json:"problem"`
	Listeners          []MailListener     `json:"listeners"`
	CertificateExpires *time.Time         `json:"certificate_expires"`
	QueueLength        int                `json:"queue_length"`
	Client             MailClientSettings `json:"client"`
}

// MailStatus reports the mail service's state and the settings for mail
// programs. Everyone who may see mail may read it.
func (s *Service) MailStatus(ctx context.Context, a Actor) (MailStatus, error) {
	if err := needMailRead(a); err != nil {
		return MailStatus{}, err
	}
	host := s.MailHost()
	out := MailStatus{
		MailHost:  host,
		Listeners: []MailListener{},
		Client:    s.clientSettings(host),
	}
	if host == "" {
		out.Problem = "The name of the mail server is not set yet; an admin sets it in the settings."
	}
	if s.mail == nil {
		out.Problem = "The mail service is not set up on this server."
		return out, nil
	}
	out.ServiceSetUp = true
	sctx, cancel := context.WithTimeout(ctx, mailStatusTimeout)
	defer cancel()
	st, err := s.mail.Status(sctx)
	if err != nil {
		out.Problem = "The mail service could not be reached."
		return out, nil
	}
	out.Reachable, out.QueueLength = true, st.QueueLength
	for _, l := range st.Listeners {
		out.Listeners = append(out.Listeners, MailListener{Name: l.Name, Address: l.Address, Listening: l.Listening, Error: l.Error})
	}
	if !st.CertificateExpires.IsZero() {
		t := st.CertificateExpires
		out.CertificateExpires = &t
	}
	if s.mailCertsLoaded() {
		if cfg, err := s.buildMailConfig(ctx); err == nil {
			out.InSync = cfg.Hash() == st.ConfigHash
		}
	}
	if !out.InSync && out.Problem == "" {
		out.Problem = "The mail service does not run the current settings yet; they are sent again shortly."
	}
	return out, nil
}

// CheckMailOutbound asks the mail service to try reaching port 25 of
// another server (admins only).
func (s *Service) CheckMailOutbound(ctx context.Context, a Actor) (config.OutboundCheck, error) {
	if err := needAdminRead(a); err != nil {
		return config.OutboundCheck{}, err
	}
	if s.mail == nil {
		return config.OutboundCheck{}, errorf(Conflict, "the mail service is not set up on this server")
	}
	ctx, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	r, err := s.mail.CheckOutbound(ctx)
	if err != nil {
		return config.OutboundCheck{}, mailServiceError(err)
	}
	return r, nil
}
