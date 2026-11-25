package submission

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Sending for shared mailboxes and to distribution groups (G3).

const (
	carol = "carol@other.test"
	team  = "shared@example.org"
)

var (
	idCarol = mailtest.ID('c')
	idTeam  = mailtest.ID('d')
)

// withSharing: alice may send as the shared mailbox (and read it), bob on
// behalf of it; carol (another organisation) has nothing. staff@ takes
// mail from its members only, org@ from the organisation only.
func (e *env) withSharing(change func(*config.Config)) {
	e.t.Helper()
	cfg := testConfig(e.t)
	cfg.Mailboxes = append(cfg.Mailboxes,
		config.Mailbox{StorageID: idCarol, Address: carol, PasswordHash: cheapHash},
		config.Mailbox{StorageID: idTeam, Address: team, Kind: config.KindShared, KeepSentCopy: true, Grants: []config.Grant{
			{StorageID: idAlice, Rights: []string{config.RightRead, config.RightSendAs}},
			{StorageID: idBob, Rights: []string{config.RightSendOnBehalf}},
		}},
	)
	cfg.Aliases = append(cfg.Aliases,
		config.Alias{Address: "hello@example.org", Targets: []string{team}},
		config.Alias{Address: "staff@example.org", Targets: []string{alice}, SenderPolicy: config.SenderMembers},
		config.Alias{Address: "org@example.org", Targets: []string{bob}, SenderPolicy: config.SenderOrganisation},
	)
	if change != nil {
		change(cfg)
	}
	if err := cfg.Validate(); err != nil {
		e.t.Fatal(err)
	}
	e.dir.Store(directory.New(cfg))
}

func withSender(body, sender string) string {
	return "Sender: " + sender + "\r\n" + body
}

func TestSendAsAndOnBehalf(t *testing.T) {
	e := start(t)
	e.withSharing(nil)
	for _, tc := range []struct {
		name     string
		user     string
		mailFrom string
		body     string
		code     int // 0: accepted
	}{
		{"send as", alice, team, message(team, bob, "as"), 0},
		{"send as an alias", alice, "hello@example.org", message("hello@example.org", bob, "alias"), 0},
		{"send as with own envelope", alice, alice, message(team, bob, "own envelope"), 0},
		{"on behalf with own Sender", bob, team, withSender(message(team, alice, "behalf"), bob), 0},
		{"on behalf without Sender", bob, team, message(team, alice, "no sender"), 550},
		{"on behalf with the team as Sender", bob, team, withSender(message(team, alice, "team sender"), team), 550},
		{"on behalf: aliases are not the address", bob, "hello@example.org", message("hello@example.org", alice, "x"), 550},
		{"on behalf with own envelope", bob, bob, withSender(message(team, alice, "own envelope"), bob), 0},
		{"no right", carol, team, message(team, bob, "carol"), 550},
		{"no right in From", carol, carol, message(team, bob, "carol from"), 550},
	} {
		c := e.client(tc.user)
		err := send(c, tc.mailFrom, []string{alice, bob}, tc.body)
		if got := code(err); got != tc.code || (tc.code == 0 && err != nil) {
			t.Errorf("%s: %v, want %d", tc.name, err, tc.code)
		}
	}
	// Signed for the From domain.
	for _, m := range e.inbox(idBob) {
		if strings.Contains(m, "From: "+team) && !strings.Contains(m, "DKIM-Signature: ") {
			t.Errorf("not signed: %q", m)
		}
	}
}

func (e *env) folder(id, name string) []string {
	e.t.Helper()
	_, msgs, err := e.store.SelectAs(context.Background(), id, name, idAlice)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		f, err := e.store.Open(m)
		if err != nil {
			e.t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		out = append(out, strings.Join(m.Flags, " ")+"|"+string(b))
	}
	return out
}

func TestKeepSentCopy(t *testing.T) {
	e := start(t)
	e.withSharing(nil)
	c := e.client(alice)
	body := "Message-ID: <kept-1@example.org>\r\n" + message(team, "friend@remote.test", "kept")
	if err := send(c, team, []string{"friend@remote.test"}, body); err != nil {
		t.Fatal(err)
	}
	sent := e.folder(idTeam, "Sent")
	if len(sent) != 1 || !strings.Contains(sent[0], "Subject: kept") || !strings.HasPrefix(sent[0], storage.FlagSeen+"|") {
		t.Fatalf("sent folder %q", sent)
	}
	// The program saves its own copy afterwards (IMAP APPEND with the same
	// Message-ID): stored once. And sending again with the same Message-ID
	// keeps one copy too.
	res, err := e.store.Append(context.Background(), idTeam, "Sent", strings.NewReader(body), storage.AppendOptions{Dedupe: true})
	if err != nil || !res.Duplicate {
		t.Fatalf("program's copy: %+v %v", res, err)
	}
	if err := send(c, team, []string{"friend@remote.test"}, body); err != nil {
		t.Fatal(err)
	}
	if n := len(e.folder(idTeam, "Sent")); n != 1 {
		t.Errorf("%d copies", n)
	}
	// Own mail and mailboxes without the option keep nothing.
	if err := send(c, alice, []string{bob}, message(alice, bob, "own")); err != nil {
		t.Fatal(err)
	}
	if n := len(e.folder(idTeam, "Sent")); n != 1 {
		t.Errorf("own mail copied: %d", n)
	}
	e.sink.Wait(2, 5*time.Second)
}

func TestSharedMailboxLimits(t *testing.T) {
	e := start(t)
	e.withSharing(func(c *config.Config) {
		for i := range c.Mailboxes {
			if c.Mailboxes[i].StorageID == idTeam {
				c.Mailboxes[i].SendPerHour = 1
			}
		}
	})
	a := e.client(alice)
	if err := send(a, team, []string{bob}, message(team, bob, "one")); err != nil {
		t.Fatal(err)
	}
	// The shared mailbox's limit counts for everyone sending for it...
	b := e.client(bob)
	if err := send(b, team, []string{alice}, withSender(message(team, alice, "two"), bob)); code(err) != 451 {
		t.Errorf("over the shared mailbox's limit: %v", err)
	}
	// ...and not for their own mail.
	if err := send(b, bob, []string{alice}, message(bob, alice, "own")); err != nil {
		t.Errorf("own mail: %v", err)
	}
}

func TestGroupSenderPolicy(t *testing.T) {
	e := start(t)
	e.withSharing(nil)
	a, b, c := e.client(alice), e.client(bob), e.client(carol)
	if err := send(a, alice, []string{"staff@example.org"}, message(alice, "staff@example.org", "member")); err != nil {
		t.Errorf("member: %v", err)
	}
	if err := send(b, bob, []string{"staff@example.org"}, message(bob, "staff@example.org", "not a member")); code(err) != 550 {
		t.Errorf("non-member: %v", err)
	}
	if err := send(b, bob, []string{"org@example.org"}, message(bob, "org@example.org", "org")); err != nil {
		t.Errorf("organisation: %v", err)
	}
	if err := send(c, carol, []string{"org@example.org"}, message(carol, "org@example.org", "outside")); code(err) != 550 {
		t.Errorf("other organisation: %v", err)
	}
	// Alice sending as the shared mailbox still counts as herself.
	if err := send(a, team, []string{"staff@example.org"}, message(team, "staff@example.org", "as team")); err != nil {
		t.Errorf("member sending as the team: %v", err)
	}
}
