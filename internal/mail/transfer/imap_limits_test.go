package transfer

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// setVar sets a package variable for one test.
func setVar[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// A server that lists endless folders is not followed to its end.
func TestIMAPListBounded(t *testing.T) {
	setVar(t, &maxListed, 5)
	e := newEnv(t)
	for i := range 10 {
		e.imap.folder(sprintf("F%d", i))
	}
	_, err := e.m.Check(context.Background(), idAlice, e.src())
	if err == nil || !strings.Contains(err.Error(), "more than 5 folders") {
		t.Fatalf("check: %v", err)
	}
}

// One huge answer (here a folder name) ends the session instead of being
// held in memory.
func TestIMAPResponseBudget(t *testing.T) {
	setVar(t, &controlBytes, 64<<10)
	e := newEnv(t)
	e.imap.folder(strings.Repeat("n", 100<<10))
	_, err := e.m.Check(context.Background(), idAlice, e.src())
	if err == nil || !strings.Contains(err.Error(), "more data than expected") {
		t.Fatalf("check: %v", err)
	}
	// Normal servers stay well inside the budget.
	e.imap.mu.Lock()
	e.imap.folders = e.imap.folders[:1]
	e.imap.mu.Unlock()
	e.imap.add("INBOX", msg("m1"))
	j, err := e.m.StartIMAP(context.Background(), idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	if j = e.wait(idAlice, j.ID, config.ImportDone, config.ImportFailed); j.State != config.ImportDone || j.Copied != 1 {
		t.Fatalf("%s %s %+v", j.State, j.Error, j)
	}
}

// A server that stops answering does not hold a job forever.
func TestIMAPIdleTimeout(t *testing.T) {
	setVar(t, &imapIdleTimeout, 100*time.Millisecond)
	e := newEnv(t)
	e.imap.add("INBOX", msg("m1"))
	e.imap.fetchDelay = 2 * time.Second
	j, err := e.m.StartIMAP(context.Background(), idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	if j = e.wait(idAlice, j.ID, config.ImportDone, config.ImportFailed); j.State != config.ImportFailed || !strings.Contains(j.Error, "lost") {
		t.Fatalf("%s %s", j.State, j.Error)
	}
}

// Flags and sizes are fetched in batches, a pass looks at a bounded
// number of messages per folder, and the repeat brings the rest.
func TestIMAPBatchesAndFolderCap(t *testing.T) {
	setVar(t, &fetchBatch, 2)
	setVar(t, &maxFolderMessages, 3)
	e := newEnv(t)
	for i := range 5 {
		e.imap.add("INBOX", msg(sprintf("m%d", i)))
	}
	ctx := context.Background()
	c, err := e.m.Check(ctx, idAlice, e.src())
	if err != nil || c.Messages != 5 {
		t.Fatalf("check %+v %v", c, err)
	}
	j, err := e.m.StartIMAP(ctx, idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	j = e.wait(idAlice, j.ID, config.ImportDone, config.ImportFailed)
	if j.State != config.ImportDone || j.Copied != 3 {
		t.Fatalf("first pass %s %s %+v", j.State, j.Error, j)
	}
	full, err := e.m.Job(ctx, idAlice, j.ID)
	must(t, err)
	if len(full.Failures) != 1 || !strings.Contains(full.Failures[0].Reason, "more than 3 new messages") {
		t.Fatalf("failures %+v", full.Failures)
	}
	j, err = e.m.StartIMAP(ctx, idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	j = e.wait(idAlice, j.ID, config.ImportDone, config.ImportFailed)
	if j.State != config.ImportDone || j.Copied != 2 {
		t.Fatalf("repeat %s %s %+v", j.State, j.Error, j)
	}
	if s := subjects(e.messages(idAlice)["INBOX"]); !slices.Equal(s, []string{"m0", "m1", "m2", "m3", "m4"}) {
		t.Fatalf("INBOX %v", s)
	}
}
