package transfer

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// gmail fills the fake server like a Gmail account.
func gmail(f *fakeIMAP) {
	f.folder("[Gmail]", imap.MailboxAttrNoSelect)
	f.folder("[Gmail]/All Mail", imap.MailboxAttrAll)
	f.folder("[Gmail]/Sent Mail", imap.MailboxAttrSent)
	f.folder("[Gmail]/Starred", imap.MailboxAttrFlagged)
	f.folder("[Gmail]/Important", imap.MailboxAttrImportant)
	f.folder("[Gmail]/Spam", imap.MailboxAttrJunk)
	f.folder("[Gmail]/Trash", imap.MailboxAttrTrash)
	f.add("INBOX", msg("m1"), imap.FlagSeen, imap.FlagFlagged)
	f.add("INBOX", msg("m2"))
	f.add("Work", msg("m3"), imap.FlagSeen, "$Forwarded")
	f.add("[Gmail]/Sent Mail", msg("m4"), imap.FlagSeen)
	f.add("[Gmail]/Spam", msg("m5"))
	f.add("[Gmail]/Trash", msg("m6"))
	f.add("[Gmail]/Starred", msg("m1"), imap.FlagSeen, imap.FlagFlagged)
	f.add("[Gmail]/Important", msg("m2"))
	for _, m := range []string{"m1", "m2", "m3", "m4", "m7"} {
		f.add("[Gmail]/All Mail", msg(m))
	}
	f.add("INBOX", msg("deleted"), imap.FlagDeleted)
}

func subjects(bodies []string) []string {
	var out []string
	for _, b := range bodies {
		for _, l := range strings.Split(b, "\r\n") {
			if s, ok := strings.CutPrefix(l, "Subject: "); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestIMAPCheckAndImport(t *testing.T) {
	e := newEnv(t)
	gmail(e.imap)
	ctx := context.Background()

	c, err := e.m.Check(ctx, idAlice, e.src())
	if err != nil {
		t.Fatal(err)
	}
	sel := map[string]bool{}
	local := map[string]string{}
	for _, f := range c.Folders {
		sel[f.Remote], local[f.Remote] = f.Selected, f.Local
	}
	if !sel["INBOX"] || !sel["Work"] || !sel["[Gmail]/All Mail"] || sel["[Gmail]/Spam"] || sel["[Gmail]/Trash"] ||
		sel["[Gmail]/Starred"] || sel["[Gmail]/Important"] || local["[Gmail]/Sent Mail"] != "Sent" {
		t.Fatalf("check %+v", c)
	}
	if c.Messages != 3+1+1+5 || c.QuotaLeft != -1 {
		t.Fatalf("totals %+v", c)
	}

	j, err := e.m.StartIMAP(ctx, idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	j = e.wait(idAlice, j.ID, config.ImportDone)
	got := e.messages(idAlice)
	for folder, want := range map[string][]string{
		"INBOX": {"m1", "m2"}, "Work": {"m3"}, "Sent": {"m4"}, "Archive": {"m7"}, "Junk": nil, "Trash": nil,
	} {
		if s := subjects(got[folder]); !slices.Equal(s, want) {
			t.Errorf("%s: %v, want %v", folder, s, want)
		}
	}
	if j.Copied != 5 || j.Skipped != 4 || j.Failed != 0 {
		t.Fatalf("counts %+v", j)
	}
	// Flags and dates come along.
	m1 := e.message(idAlice, "INBOX", 0)
	if !slices.Equal(m1.Flags, []string{storage.FlagFlagged, storage.FlagSeen}) || !m1.InternalDate.Equal(time.Date(2023, 5, 1, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("m1 %v %v", m1.Flags, m1.InternalDate)
	}
	if m3 := e.message(idAlice, "Work", 0); !slices.Equal(m3.Flags, []string{"$Forwarded", storage.FlagSeen}) {
		t.Fatalf("m3 %v", m3.Flags)
	}
	// The source stays as it was: read-only, \Seen untouched.
	if ws, _ := e.imap.counters(); ws != 0 {
		t.Fatalf("%d SELECT commands", ws)
	}
	if fl := e.imap.flagsOf("INBOX"); len(fl[1]) != 0 {
		t.Fatalf("source flags changed: %v", fl)
	}
	if k := e.eventKinds(); !slices.Equal(k, []string{config.EventImportFinished}) {
		t.Fatalf("events %v", k)
	}
	if !strings.Contains(e.events[0].Detail, "old@example.com at imap.test") || e.events[0].Address != "alice@example.org" {
		t.Fatalf("event %+v", e.events[0])
	}

	// A repeat fetches nothing old (watermarks) and copies what is new.
	_, fetches := e.imap.counters()
	e.imap.add("INBOX", msg("m8"))
	j2, err := e.m.StartIMAP(ctx, idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	j2 = e.wait(idAlice, j2.ID, config.ImportDone)
	if _, f2 := e.imap.counters(); f2 != fetches+1 || j2.Copied != 1 || j2.Skipped != 0 {
		t.Fatalf("repeat fetched %d bodies, %+v", f2-fetches, j2)
	}
	if s := subjects(e.messages(idAlice)["INBOX"]); !slices.Equal(s, []string{"m1", "m2", "m8"}) {
		t.Fatalf("after repeat %v", s)
	}
	if strings.Contains(e.jobJSON(idAlice), password) {
		t.Fatal("password in a job")
	}
}

func TestIMAPChoicesAndNamespace(t *testing.T) {
	e := newEnv(t)
	e.imap.nsPrefix = "INBOX/"
	e.imap.folder("INBOX/Projects")
	e.imap.add("INBOX/Projects", msg("p1"))
	e.imap.folder("Junk")
	e.imap.add("Junk", msg("j1"))
	e.imap.add("INBOX", msg("i1"))
	ctx := context.Background()
	j, err := e.m.StartIMAP(ctx, idAlice, config.ImportStart{Source: e.src(), Choices: config.ImportChoices{IncludeJunk: true}})
	if err != nil {
		t.Fatal(err)
	}
	j = e.wait(idAlice, j.ID, config.ImportDone)
	got := e.messages(idAlice)
	if !slices.Equal(subjects(got["Projects"]), []string{"p1"}) || !slices.Equal(subjects(got["Junk"]), []string{"j1"}) ||
		!slices.Equal(subjects(got["INBOX"]), []string{"i1"}) {
		t.Fatalf("got %v", got)
	}
	var mapping []string
	for _, f := range j.Folders {
		mapping = append(mapping, f.Remote+"→"+f.Local)
	}
	if !slices.Equal(mapping, []string{"INBOX→INBOX", "INBOX/Projects→Projects", "Junk→Junk"}) {
		t.Fatalf("mapping %v", mapping)
	}
	// An unknown folder in the choices fails the job.
	j, err = e.m.StartIMAP(ctx, idAlice, config.ImportStart{Source: e.src(), Choices: config.ImportChoices{Folders: []string{"Nope"}}})
	if err != nil {
		t.Fatal(err)
	}
	if j = e.wait(idAlice, j.ID, config.ImportFailed); !strings.Contains(j.Error, "Nope") {
		t.Fatalf("error %q", j.Error)
	}
}

func TestIMAPSignInFailure(t *testing.T) {
	e := newEnv(t)
	// A server that repeats what it was sent must not get it into errors.
	e.imap.loginText = "Invalid credentials for old@example.com/" + password
	src := e.src()
	src.Username = "wrong@example.com"
	_, err := e.m.Check(context.Background(), idAlice, src)
	if status(err) != http.StatusBadGateway || !strings.Contains(err.Error(), "signing in to imap.test failed") ||
		strings.Contains(err.Error(), password) {
		t.Fatalf("check: %v", err)
	}
	j, err := e.m.StartIMAP(context.Background(), idAlice, config.ImportStart{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	j = e.wait(idAlice, j.ID, config.ImportFailed)
	if strings.Contains(j.Error, password) || !strings.Contains(j.Error, "***") {
		t.Fatalf("job error %q", j.Error)
	}
	e.imap.mu.Lock()
	logins := e.imap.logins
	e.imap.mu.Unlock()
	if logins != 2 {
		t.Fatalf("%d sign-ins: a failed one must not be retried", logins)
	}
	b, _ := json.Marshal(j)
	if strings.Contains(string(b), password) {
		t.Fatal("password in job")
	}
}

func TestIMAPRefusesPrivateAddresses(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.AllowPrivate = false })
	e.imap.addr = "10.1.2.3"
	_, err := e.m.Check(context.Background(), idAlice, e.src())
	if status(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "private") {
		t.Fatalf("private: %v", err)
	}
	e.imap.addr = "127.0.0.1"
	if _, err := e.m.Check(context.Background(), idAlice, e.src()); status(err) != http.StatusBadRequest {
		t.Fatalf("loopback: %v", err)
	}
	for _, src := range []config.ImportSource{
		{Host: "imap.test", Port: 25, Username: "u", Password: "p"},
		{Host: "imap test", Port: 993, Username: "u", Password: "p"},
		{Host: "imap.test", Port: 993, Username: "u\r\nA1 LOGOUT", Password: "p"},
		{Host: "imap.test", Port: 993, Username: "u"},
	} {
		if _, err := e.m.Check(context.Background(), idAlice, src); status(err) != http.StatusBadRequest {
			t.Errorf("%v: %v", src, err)
		}
	}
	if _, err := e.m.Check(context.Background(), mailtestUnknown, e.src()); status(err) != http.StatusNotFound {
		t.Fatalf("unknown mailbox: %v", err)
	}
}

const mailtestUnknown = "cccccccccccccccccccccccccccccccc"

func TestIMAPStartTLS(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.src()
	src.Port = 143

	// Port 143 with STARTTLS works.
	e.imap = startFakeIMAP(t, "starttls")
	e.imap.add("INBOX", msg("tls"))
	if c, err := e.m.Check(ctx, idAlice, src); err != nil || c.Messages != 1 {
		t.Fatalf("starttls: %+v %v", c, err)
	}
	// Without STARTTLS the password is never sent.
	e.imap = startFakeIMAP(t, "plain")
	if _, err := e.m.Check(ctx, idAlice, src); status(err) != http.StatusBadGateway || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("plain: %v", err)
	}
	if e.imap.logins != 0 {
		t.Fatal("signed in without encryption")
	}
	// Port 993 to a server without TLS fails at the handshake.
	src.Port = 993
	if _, err := e.m.Check(ctx, idAlice, src); status(err) != http.StatusBadGateway {
		t.Fatalf("no tls: %v", err)
	}
	// A certificate for another name is refused.
	e.imap = startFakeIMAP(t, "tls")
	other := resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	})
	e.m.opts.Resolver = other
	src.Host = "imap.other.test"
	if _, err := e.m.Check(ctx, idAlice, src); status(err) != http.StatusBadGateway || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("wrong name: %v", err)
	}
}

func TestIMAPPausedByDownloadLimit(t *testing.T) {
	e := newEnv(t)
	for i := range 5 {
		e.imap.add("INBOX", msg(sprintf("n%d", i)))
	}
	e.imap.alertAfter = 2
	ctx := context.Background()
	j, err := e.m.StartIMAP(ctx, idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	j = e.wait(idAlice, j.ID, config.ImportPaused)
	if j.Copied != 2 || !strings.Contains(j.Error, "limits downloads") || !j.Finished.IsZero() {
		t.Fatalf("paused %+v", j)
	}
	if k := e.eventKinds(); !slices.Equal(k, []string{config.EventImportPaused}) {
		t.Fatalf("events %v", k)
	}
	// Continuing needs the password.
	if _, err := e.m.Resume(ctx, idAlice, j.ID, config.ImportResume{}); status(err) != http.StatusBadRequest {
		t.Fatalf("resume without password: %v", err)
	}
	e.imap.mu.Lock()
	e.imap.alertAfter = 0
	e.imap.mu.Unlock()
	if _, err := e.m.Resume(ctx, idAlice, j.ID, config.ImportResume{Password: password}); err != nil {
		t.Fatal(err)
	}
	j = e.wait(idAlice, j.ID, config.ImportDone)
	if j.Copied != 5 || j.Skipped != 0 {
		t.Fatalf("after resume %+v", j)
	}
}

func TestIMAPRestartInterrupts(t *testing.T) {
	e := newEnv(t)
	for i := range 100 {
		e.imap.add("INBOX", msg(sprintf("n%d", i)))
	}
	e.imap.fetchDelay = 20 * time.Millisecond
	ctx := context.Background()
	j, err := e.m.StartIMAP(ctx, idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	for j.Copied < 3 {
		j = e.wait(idAlice, j.ID, config.ImportRunning)
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	e.m.Shutdown(sctx)
	cancel()
	if j, _ = e.store.Import(ctx, idAlice, j.ID); j.State != config.ImportRunning {
		t.Fatalf("after shutdown %s", j.State)
	}
	e.m = e.manager()
	j = e.wait(idAlice, j.ID, config.ImportInterrupted)
	if !strings.Contains(j.Error, "password") || !slices.Contains(e.eventKinds(), config.EventImportInterrupted) {
		t.Fatalf("interrupted %+v %v", j, e.eventKinds())
	}
	copied := len(e.messages(idAlice)["INBOX"])
	e.imap.mu.Lock()
	e.imap.fetchDelay = 0
	e.imap.mu.Unlock()
	_, before := e.imap.counters()
	if _, err := e.m.Resume(ctx, idAlice, j.ID, config.ImportResume{Password: password}); err != nil {
		t.Fatal(err)
	}
	j = e.wait(idAlice, j.ID, config.ImportDone)
	_, after := e.imap.counters()
	if n := len(e.messages(idAlice)["INBOX"]); n != 100 || after-before != 100-copied {
		t.Fatalf("%d messages, %d fetched after resume (%d before)", n, after-before, copied)
	}
}

func TestIMAPQuota(t *testing.T) {
	e := newEnv(t)
	e.quota = int64(3*len(msg("n0")) + 10)
	e.setMailboxes(idAlice, idBob)
	for i := range 6 {
		e.imap.add("INBOX", msg(sprintf("n%d", i)))
	}
	c, err := e.m.Check(context.Background(), idAlice, e.src())
	if err != nil {
		t.Fatal(err)
	}
	if c.QuotaBytes != e.quota || c.QuotaLeft != e.quota {
		t.Fatalf("check %+v", c)
	}
	j, err := e.m.StartIMAP(context.Background(), idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	j = e.wait(idAlice, j.ID, config.ImportFailed)
	if j.Copied != 3 || !strings.Contains(j.Error, "quota") {
		t.Fatalf("quota %+v", j)
	}
	if !slices.Contains(e.eventKinds(), config.EventImportFailed) {
		t.Fatal("no event")
	}
}

func TestIMAPDiskSpace(t *testing.T) {
	e := newEnv(t)
	e.imap.add("INBOX", msg("n0"))
	e.free.Store(1<<30 + 10)
	j, err := e.m.StartIMAP(context.Background(), idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	if j = e.wait(idAlice, j.ID, config.ImportFailed); !strings.Contains(j.Error, "disk") || j.Copied != 0 {
		t.Fatalf("disk %+v", j)
	}
}

func TestDeleteMailboxDuringJob(t *testing.T) {
	e := newEnv(t)
	for i := range 200 {
		e.imap.add("INBOX", msg(sprintf("n%d", i)))
	}
	e.imap.fetchDelay = 20 * time.Millisecond
	ctx := context.Background()
	j, err := e.m.StartIMAP(ctx, idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	for j.Copied < 2 {
		j = e.wait(idAlice, j.ID, config.ImportRunning)
	}
	// As the panel does: remove from the configuration, then delete.
	e.setMailboxes(idBob)
	if err := e.m.StopMailbox(ctx, idAlice); err != nil {
		t.Fatal(err)
	}
	must(t, e.store.DeleteMailbox(ctx, idAlice))
	time.Sleep(100 * time.Millisecond)
	ids, _ := e.store.StorageIDs(ctx)
	if slices.Contains(ids, idAlice) {
		t.Fatal("deleted mailbox came back")
	}
	if js, _ := e.store.ImportsIn(ctx, config.ImportRunning, config.ImportWaiting, config.ImportCancelled); len(js) != 0 {
		t.Fatalf("jobs left: %+v", js)
	}
	if _, err := e.store.ImportWatermark(ctx, idAlice, "imap.test", "old@example.com", "INBOX"); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimitAndBusy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := range config.ImportRateLimit {
		if _, err := e.m.Check(ctx, idAlice, e.src()); err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
	}
	if _, err := e.m.Check(ctx, idAlice, e.src()); status(err) != http.StatusTooManyRequests {
		t.Fatalf("over the limit: %v", err)
	}
	// Another mailbox is not affected; a second job of it waits its turn.
	e.imap.fetchDelay = 50 * time.Millisecond
	e.imap.add("INBOX", msg("x"))
	if _, err := e.m.StartIMAP(ctx, idBob, config.ImportStart{Source: e.src()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.StartIMAP(ctx, idBob, config.ImportStart{Source: e.src()}); status(err) != http.StatusConflict {
		t.Fatalf("second job: %v", err)
	}
}

func TestCancel(t *testing.T) {
	e := newEnv(t)
	for i := range 200 {
		e.imap.add("INBOX", msg(sprintf("n%d", i)))
	}
	e.imap.fetchDelay = 20 * time.Millisecond
	ctx := context.Background()
	j, err := e.m.StartIMAP(ctx, idAlice, config.ImportStart{Source: e.src()})
	if err != nil {
		t.Fatal(err)
	}
	for j.Copied < 1 {
		j = e.wait(idAlice, j.ID, config.ImportRunning)
	}
	must(t, e.m.Cancel(ctx, idAlice, j.ID))
	if j, _ = e.m.Job(ctx, idAlice, j.ID); j.State != config.ImportCancelled || j.Finished.IsZero() {
		t.Fatalf("after cancel %+v", j)
	}
	// Cancelling a finished job removes it.
	must(t, e.m.Cancel(ctx, idAlice, j.ID))
	if _, err := e.m.Job(ctx, idAlice, j.ID); status(err) != http.StatusNotFound {
		t.Fatalf("removed: %v", err)
	}
}
