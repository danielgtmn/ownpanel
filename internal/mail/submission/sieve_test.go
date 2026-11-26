package submission

import (
	"context"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/deliver"
)

// Mail between mailboxes runs the recipient's filters: an out-of-office
// reply goes back to the colleague, signed, and filters move mail.
func TestInternalMailRunsFilters(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := e.store.PutSieveScript(ctx, idBob, "ooo", `require ["vacation", "fileinto", "mailbox"];
vacation :subject "Out" "Back on Monday.";
if header :contains "subject" "report" { fileinto :create "Reports"; }`, deliver.MaxScripts, true); err != nil {
		t.Fatal(err)
	}
	c := e.client(alice)
	if err := send(c, alice, []string{bob}, "From: Alice <alice@example.org>\r\nTo: bob@example.org\r\nSubject: The report\r\nMessage-ID: <r1@example.org>\r\n\r\nHi.\r\n"); err != nil {
		t.Fatal(err)
	}
	if got := e.inbox(idBob); len(got) != 0 {
		t.Fatalf("bob INBOX %q", got)
	}
	_, msgs, err := e.store.Select(ctx, idBob, "Reports")
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Reports: %v %d", err, len(msgs))
	}
	in := e.inbox(idAlice)
	if len(in) != 1 {
		t.Fatalf("no reply for alice: %d", len(in))
	}
	for _, want := range []string{"Return-Path: <>\r\n", "DKIM-Signature: ", "From: <bob@example.org>\r\n", "Subject: Out\r\n",
		"Auto-Submitted: auto-replied\r\n", "In-Reply-To: <r1@example.org>\r\n", "Back on Monday."} {
		if !strings.Contains(in[0], want) {
			t.Errorf("reply lacks %q:\n%s", want, in[0])
		}
	}
	// Once per period.
	if err := send(c, alice, []string{bob}, "From: alice@example.org\r\nTo: bob@example.org\r\nSubject: again\r\n\r\nHi.\r\n"); err != nil {
		t.Fatal(err)
	}
	if got := e.inbox(idAlice); len(got) != 1 {
		t.Fatalf("second reply: %d", len(got))
	}
	if got := e.inbox(idBob); len(got) != 1 {
		t.Fatalf("bob INBOX after second: %d", len(got))
	}
}
