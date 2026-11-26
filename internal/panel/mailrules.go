package panel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Out-of-office and forwarding of a mailbox, for the domain owner (G2 in
// wiki/Groupware-Architecture.md: "For staff who left"). The filters are
// the person's data in the mail service; the panel sets or clears only
// these two, logs it, and the person sees who set them. The panel shows
// whether an out-of-office reply is on, and the text and forwarding
// address only when they were set from the panel.

// MailRules is what the panel can do with a mailbox's filters (the mail
// service's admin socket, *config.Client).
type MailRules interface {
	MailRules(ctx context.Context, storageID string) (config.MailRules, error)
	SetOutOfOffice(ctx context.Context, storageID string, ch config.OutOfOfficeChange) (config.MailRules, error)
	SetForwarding(ctx context.Context, storageID string, ch config.ForwardingChange) (config.MailRules, error)
}

var _ MailRules = (*config.Client)(nil)

// MailboxOutOfOffice is a mailbox's out-of-office reply as the panel shows
// it.
type MailboxOutOfOffice struct {
	Enabled bool `json:"enabled"`
	// On: enabled and within its dates now.
	On    bool   `json:"on"`
	Start string `json:"start"`
	End   string `json:"end"`
	// SetBy is the panel user who set it ("" when the person did; then
	// Subject and Text are not shown).
	SetBy   string     `json:"set_by"`
	SetAt   *time.Time `json:"set_at"`
	Subject string     `json:"subject"`
	Text    string     `json:"text"`
	Days    int        `json:"days"`
}

// MailboxForwarding is a mailbox's forwarding as the panel shows it.
type MailboxForwarding struct {
	Enabled bool `json:"enabled"`
	// To and KeepCopy only when it was set from the panel.
	To       string     `json:"to"`
	KeepCopy bool       `json:"keep_copy"`
	SetBy    string     `json:"set_by"`
	SetAt    *time.Time `json:"set_at"`
}

// MailboxRules is what the panel shows of a mailbox's filters.
type MailboxRules struct {
	OutOfOffice MailboxOutOfOffice `json:"out_of_office"`
	Forwarding  MailboxForwarding  `json:"forwarding"`
	// HandWritten: the person's filters were written in a mail program;
	// setting out-of-office or forwarding here replaces them (Replace).
	HandWritten bool `json:"hand_written"`
	// MayForward: the mailbox's filters may forward mail.
	MayForward bool `json:"may_forward"`
}

// OutOfOfficeInput sets (Enabled) or clears the reply.
type OutOfOfficeInput struct {
	Enabled bool
	Subject string
	Text    string
	Start   string
	End     string
	Zone    string
	Days    int
	// Replace filters written in a mail program.
	Replace bool
}

// ForwardingInput sets (Enabled) or clears forwarding.
type ForwardingInput struct {
	Enabled  bool
	To       string
	KeepCopy bool
	Replace  bool
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func rulesView(r config.MailRules, mayForward bool) MailboxRules {
	o, f := r.OutOfOffice, r.Forwarding
	v := MailboxRules{
		OutOfOffice: MailboxOutOfOffice{Enabled: o.Enabled, Start: o.Start, End: o.End, SetBy: o.SetBy, SetAt: timePtr(o.SetAt)},
		Forwarding:  MailboxForwarding{Enabled: f.Enabled, SetBy: f.SetBy, SetAt: timePtr(f.SetAt)},
		HandWritten: !r.Managed,
		MayForward:  mayForward,
	}
	if o.Enabled {
		today := time.Now()
		if o.Zone != "" {
			if z, err := time.Parse("-0700", o.Zone); err == nil {
				today = today.In(z.Location())
			}
		}
		d := today.Format("2006-01-02")
		v.OutOfOffice.On = (o.Start == "" || d >= o.Start) && (o.End == "" || d <= o.End)
	}
	if setByPanel(o.SetBy) {
		v.OutOfOffice.Subject, v.OutOfOffice.Text, v.OutOfOffice.Days = o.Subject, o.Text, o.Days
	}
	if setByPanel(f.SetBy) {
		v.Forwarding.To, v.Forwarding.KeepCopy = f.To, f.KeepCopy
	}
	return v
}

// setByPanel tells whether a panel user set out-of-office or forwarding,
// so the panel may show what was set. The mailbox's own settings stay
// private, and so do those of someone with the Manage right acting for it
// (an assistant setting a reply for their boss): those are recorded by
// mailbox address, while panel user names never contain "@".
func setByPanel(setBy string) bool {
	return setBy != "" && !strings.Contains(setBy, "@")
}

func (s *Service) mailRules() (MailRules, error) {
	if s.mail == nil {
		return nil, errorf(Conflict, "the mail service is not set up on this server")
	}
	r, ok := s.mail.(MailRules)
	if !ok {
		return nil, errorf(Conflict, "the mail service cannot manage filters")
	}
	return r, nil
}

// rulesError explains a failed request about filters.
func rulesError(err error) error {
	var e *config.Error
	if errors.As(err, &e) {
		switch e.Status {
		case 409:
			return &Error{Kind: Conflict, Code: CodeHandWrittenFilters, Msg: "the person's filters were written in a mail program; confirm to replace them", Err: err}
		case 400:
			return &Error{Kind: Invalid, Msg: strings.TrimSpace(e.Msg), Err: err}
		}
	}
	return mailServiceError(err)
}

// MailboxRulesOf shows a mailbox's out-of-office and forwarding.
func (s *Service) MailboxRulesOf(ctx context.Context, a Actor, id int64) (MailboxRules, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return MailboxRules{}, err
	}
	mr, err := s.mailRules()
	if err != nil {
		return MailboxRules{}, err
	}
	may, err := s.store.MailboxMayForward(ctx, id)
	if err != nil {
		return MailboxRules{}, storeError(err)
	}
	ctx2, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	r, err := mr.MailRules(ctx2, m.StorageID)
	if err != nil {
		return MailboxRules{}, rulesError(err)
	}
	return rulesView(r, may), nil
}

// SetMailboxOutOfOffice sets or clears a mailbox's out-of-office reply
// for the domain owner. The person sees who set it.
func (s *Service) SetMailboxOutOfOffice(ctx context.Context, a Actor, id int64, in OutOfOfficeInput) (MailboxRules, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return MailboxRules{}, err
	}
	if err := needMailWrite(a); err != nil {
		return MailboxRules{}, err
	}
	o := config.OutOfOffice{Enabled: in.Enabled, Subject: in.Subject, Text: in.Text, Start: in.Start, End: in.End, Zone: in.Zone, Days: in.Days, SetBy: a.Name}
	if in.Enabled {
		if err := config.CheckOutOfOffice(&o); err != nil {
			return MailboxRules{}, errorf(Invalid, "%s", err.Error())
		}
	} else {
		o = config.OutOfOffice{}
	}
	mr, err := s.mailRules()
	if err != nil {
		return MailboxRules{}, err
	}
	may, err := s.store.MailboxMayForward(ctx, id)
	if err != nil {
		return MailboxRules{}, storeError(err)
	}
	ctx2, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	r, err := mr.SetOutOfOffice(ctx2, m.StorageID, config.OutOfOfficeChange{OutOfOffice: o, Replace: in.Replace})
	if err != nil {
		return MailboxRules{}, rulesError(err)
	}
	detail := "off"
	if in.Enabled {
		detail = "on"
		if o.Start != "" || o.End != "" {
			detail += fmt.Sprintf(" from %s to %s", orDash(o.Start), orDash(o.End))
		}
	}
	if in.Replace {
		detail += "; filters written in a mail program replaced"
	}
	s.record(ctx, a, "mail.mailbox.out-of-office", m.LocalPart+"@"+m.Domain, detail)
	return rulesView(r, may), nil
}

// SetMailboxForwarding sets or clears a mailbox's forwarding for the
// domain owner.
func (s *Service) SetMailboxForwarding(ctx context.Context, a Actor, id int64, in ForwardingInput) (MailboxRules, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return MailboxRules{}, err
	}
	if err := needMailWrite(a); err != nil {
		return MailboxRules{}, err
	}
	may, err := s.store.MailboxMayForward(ctx, id)
	if err != nil {
		return MailboxRules{}, storeError(err)
	}
	f := config.Forwarding{Enabled: in.Enabled, To: in.To, KeepCopy: in.KeepCopy, SetBy: a.Name}
	if in.Enabled {
		if !may {
			return MailboxRules{}, errorf(Conflict, "forwarding is turned off for this mailbox; allow it first")
		}
		if err := config.CheckForwarding(&f); err != nil {
			return MailboxRules{}, errorf(Invalid, "%s", err.Error())
		}
		if f.To == m.LocalPart+"@"+m.Domain {
			return MailboxRules{}, errorf(Invalid, "a mailbox cannot forward to itself")
		}
	} else {
		f = config.Forwarding{}
	}
	mr, err := s.mailRules()
	if err != nil {
		return MailboxRules{}, err
	}
	ctx2, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	r, err := mr.SetForwarding(ctx2, m.StorageID, config.ForwardingChange{Forwarding: f, Replace: in.Replace})
	if err != nil {
		return MailboxRules{}, rulesError(err)
	}
	detail := "off"
	if in.Enabled {
		detail = "to " + f.To
		if f.KeepCopy {
			detail += ", keeping a copy"
		}
	}
	if in.Replace {
		detail += "; filters written in a mail program replaced"
	}
	s.record(ctx, a, "mail.mailbox.forwarding", m.LocalPart+"@"+m.Domain, detail)
	return rulesView(r, may), nil
}

// SetMailboxMayForward allows or forbids the mailbox's filters to forward
// mail (allowed by default).
func (s *Service) SetMailboxMayForward(ctx context.Context, a Actor, id int64, allowed bool) error {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return err
	}
	if err := needMailWrite(a); err != nil {
		return err
	}
	cur, err := s.store.MailboxMayForward(ctx, id)
	if err != nil {
		return storeError(err)
	}
	if cur == allowed {
		return nil
	}
	if err := s.store.SetMailboxMayForward(ctx, id, allowed); err != nil {
		return storeError(err)
	}
	detail := "allowed"
	if !allowed {
		detail = "turned off"
	}
	s.record(ctx, a, "mail.mailbox.may-forward", m.LocalPart+"@"+m.Domain, detail)
	return s.mailChanged(ctx, m.Domain)
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

// applyNoForwarding marks the mailboxes of cfg that may not forward.
func (s *Service) applyNoForwarding(ctx context.Context, cfg *config.Config, mailboxes []store.Mailbox) error {
	no, err := s.store.MailboxesNoForwarding(ctx)
	if err != nil {
		return err
	}
	if len(no) == 0 {
		return nil
	}
	bySID := map[string]bool{}
	for _, m := range mailboxes {
		if no[m.ID] {
			bySID[m.StorageID] = true
		}
	}
	for i := range cfg.Mailboxes {
		if bySID[cfg.Mailboxes[i].StorageID] {
			cfg.Mailboxes[i].NoForwarding = true
		}
	}
	return nil
}
