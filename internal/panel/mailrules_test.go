package panel

import (
	"context"
	"errors"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// fakeRules is the mail service's state of filters, per storage id.
var fakeRules = map[*fakeMail]map[string]*config.MailRules{}

func (f *fakeMail) rules(id string) *config.MailRules {
	m := fakeRules[f]
	if m == nil {
		m = map[string]*config.MailRules{}
		fakeRules[f] = m
	}
	if m[id] == nil {
		m[id] = &config.MailRules{Managed: true, Filters: []config.FilterRule{}}
	}
	return m[id]
}

func (f *fakeMail) MailRules(_ context.Context, id string) (config.MailRules, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return config.MailRules{}, f.statusErr
	}
	return *f.rules(id), nil
}

func (f *fakeMail) SetOutOfOffice(_ context.Context, id string, ch config.OutOfOfficeChange) (config.MailRules, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rules(id)
	if !r.Managed && !ch.Replace {
		return config.MailRules{}, &config.Error{Status: 409, Msg: "hand written"}
	}
	r.Managed, r.OutOfOffice = true, ch.OutOfOffice
	return *r, nil
}

func (f *fakeMail) SetForwarding(_ context.Context, id string, ch config.ForwardingChange) (config.MailRules, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rules(id)
	if !r.Managed && !ch.Replace {
		return config.MailRules{}, &config.Error{Status: 409, Msg: "hand written"}
	}
	r.Managed, r.Forwarding = true, ch.Forwarding
	return *r, nil
}

var _ MailRules = (*fakeMail)(nil)

func TestMailRulesPermissions(t *testing.T) {
	ctx := context.Background()
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", admin}, {"admin write token", rootWriteTok}, {"member", bob}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody},
	}
	F, N, U := Forbidden, NotFound, Unauthenticated
	ops := []struct {
		name string
		run  func(w *mailWorld, a Actor) error
		want []Kind
	}{
		{"show member's", func(w *mailWorld, a Actor) error {
			_, err := w.svc.MailboxRulesOf(ctx, a, w.bobBox)
			return err
		},
			[]Kind{ok, ok, ok, ok, F, ok, F, U}},
		{"show admin's", func(w *mailWorld, a Actor) error {
			_, err := w.svc.MailboxRulesOf(ctx, a, w.rootBox)
			return err
		},
			[]Kind{ok, ok, N, N, F, ok, F, U}},
		{"set member's out-of-office", func(w *mailWorld, a Actor) error {
			_, err := w.svc.SetMailboxOutOfOffice(ctx, a, w.bobBox, OutOfOfficeInput{Enabled: true, Text: "Gone."})
			return err
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"set admin's out-of-office", func(w *mailWorld, a Actor) error {
			_, err := w.svc.SetMailboxOutOfOffice(ctx, a, w.rootBox, OutOfOfficeInput{Enabled: true, Text: "Gone."})
			return err
		}, []Kind{ok, ok, N, N, F, F, F, U}},
		{"set member's forwarding", func(w *mailWorld, a Actor) error {
			_, err := w.svc.SetMailboxForwarding(ctx, a, w.bobBox, ForwardingInput{Enabled: true, To: "new@example.net"})
			return err
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"forbid member's forwarding", func(w *mailWorld, a Actor) error {
			return w.svc.SetMailboxMayForward(ctx, a, w.bobBox, false)
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
	}
	for _, op := range ops {
		for i, ac := range actors {
			w := newMailWorld(t)
			err := op.run(w, ac.a)
			got := ok
			if err != nil {
				got = KindOf(err)
			}
			if got != op.want[i] {
				t.Errorf("%s as %s: got %v (%v), want %v", op.name, ac.name, got, err, op.want[i])
			}
		}
	}
}

func TestMailRules(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	sid := w.mailboxStorageID(t, w.bobBox)

	r, err := w.svc.SetMailboxOutOfOffice(ctx, bob, w.bobBox, OutOfOfficeInput{Enabled: true, Subject: "Left", Text: "Please write to sales@.", Start: "2026-11-01"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.OutOfOffice.Enabled || !r.OutOfOffice.On || r.OutOfOffice.SetBy != bob.Name || r.OutOfOffice.Text == "" || r.OutOfOffice.Days != 7 {
		t.Fatalf("set: %+v", r.OutOfOffice)
	}
	if got := fakeRules[w.mail][sid].OutOfOffice.SetBy; got != bob.Name {
		t.Fatalf("set by %q", got)
	}
	if _, err := w.svc.SetMailboxOutOfOffice(ctx, bob, w.bobBox, OutOfOfficeInput{Enabled: true, Text: ""}); KindOf(err) != Invalid {
		t.Fatalf("empty text: %v", err)
	}
	// Set by the person: the panel shows that it is on, not the text.
	w.mail.set(func(f *fakeMail) {
		f.rules(sid).OutOfOffice = config.OutOfOffice{Enabled: true, Subject: "Private", Text: "Private text", Days: 7}
	})
	r, err = w.svc.MailboxRulesOf(ctx, bob, w.bobBox)
	if err != nil || !r.OutOfOffice.On || r.OutOfOffice.Text != "" || r.OutOfOffice.Subject != "" {
		t.Fatalf("person's own: %+v %v", r.OutOfOffice, err)
	}
	// Hand-written filters are replaced only when asked.
	w.mail.set(func(f *fakeMail) { f.rules(sid).Managed = false })
	_, err = w.svc.SetMailboxOutOfOffice(ctx, bob, w.bobBox, OutOfOfficeInput{Enabled: false})
	var pe *Error
	if !errors.As(err, &pe) || pe.Kind != Conflict || CodeOf(err) != CodeHandWrittenFilters {
		t.Fatalf("hand written: %v", err)
	}
	if _, err := w.svc.SetMailboxOutOfOffice(ctx, bob, w.bobBox, OutOfOfficeInput{Enabled: false, Replace: true}); err != nil {
		t.Fatal(err)
	}

	// Forwarding: allowed by default, can be turned off; then it cannot
	// be set, and the configuration says so.
	if _, err := w.svc.SetMailboxForwarding(ctx, bob, w.bobBox, ForwardingInput{Enabled: true, To: "info@bobsite.example.com"}); KindOf(err) != Invalid {
		t.Fatalf("to itself: %v", err)
	}
	r, err = w.svc.SetMailboxForwarding(ctx, bob, w.bobBox, ForwardingInput{Enabled: true, To: "New@Example.net", KeepCopy: true})
	if err != nil || r.Forwarding.To != "new@example.net" || !r.Forwarding.KeepCopy || !r.MayForward {
		t.Fatalf("forwarding: %+v %v", r, err)
	}
	if err := w.svc.SetMailboxMayForward(ctx, bob, w.bobBox, false); err != nil {
		t.Fatal(err)
	}
	if !w.lastMailbox(t, sid).NoForwarding {
		t.Fatal("not pushed")
	}
	if _, err := w.svc.SetMailboxForwarding(ctx, bob, w.bobBox, ForwardingInput{Enabled: true, To: "x@example.net"}); KindOf(err) != Conflict {
		t.Fatalf("not allowed: %v", err)
	}
	if err := w.svc.SetMailboxMayForward(ctx, bob, w.bobBox, true); err != nil {
		t.Fatal(err)
	}
	if w.lastMailbox(t, sid).NoForwarding {
		t.Fatal("still off")
	}

	// The activity log names the changes, never the text.
	acts, err := w.svc.store.Activities(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"mail.mailbox.out-of-office": false, "mail.mailbox.forwarding": false, "mail.mailbox.may-forward": false}
	for _, a := range acts {
		if _, ok := want[a.Action]; ok {
			want[a.Action] = true
			if a.Detail == "Please write to sales@." {
				t.Fatal("text in the activity log")
			}
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("no activity %s", k)
		}
	}
}

func (w *mailWorld) mailboxStorageID(t *testing.T, id string) string {
	t.Helper()
	m, err := w.svc.store.Mailbox(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return m.StorageID
}

func (w *mailWorld) lastMailbox(t *testing.T, sid string) config.Mailbox {
	t.Helper()
	cfg := w.mail.last()
	for _, m := range cfg.Mailboxes {
		if m.StorageID == sid {
			return m
		}
	}
	t.Fatalf("mailbox %s not pushed", sid)
	return config.Mailbox{}
}

// Settings made by the person, or by someone with the Manage right acting
// for the mailbox, stay private; those a panel user made are shown.
func TestRulesViewPrivacy(t *testing.T) {
	r := config.MailRules{Managed: true}
	r.OutOfOffice = config.OutOfOffice{Enabled: true, Subject: "Away", Text: "Back Monday", Days: 7}
	r.Forwarding = config.Forwarding{Enabled: true, To: "boss@elsewhere.example"}
	for setBy, shown := range map[string]bool{"": false, "anna@example.com": false, "admin": true} {
		r.OutOfOffice.SetBy, r.Forwarding.SetBy = setBy, setBy
		v := rulesView(r, true)
		if got := v.OutOfOffice.Text != "" && v.Forwarding.To != ""; got != shown {
			t.Errorf("set by %q: shown %v, want %v", setBy, got, shown)
		}
		if !v.OutOfOffice.Enabled || !v.Forwarding.Enabled {
			t.Errorf("set by %q: whether it is on must always show", setBy)
		}
	}
}
