package imapd

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Shared mailboxes over IMAP (G3): two people, a shared mailbox and a
// delegated personal one.

const (
	bob    = "bob@example.org"
	carol  = "carol@example.org"
	team   = "team@example.org"
	boss   = "boss@example.org"
	stName = "Shared/" + team + "/INBOX"
)

var (
	idBob   = mailtest.ID('b')
	idCarol = mailtest.ID('c')
	idTeam  = mailtest.ID('d')
	idBoss  = mailtest.ID('e')
)

// sharedConfig: alice may read and write the shared team mailbox, bob may
// read it, carol has no access; boss delegates reading to alice.
func sharedConfig(t testing.TB) *config.Config {
	cfg := testConfig(t)
	h := hash(t)
	cfg.Mailboxes = append(cfg.Mailboxes,
		config.Mailbox{StorageID: idBob, Address: bob, PasswordHash: h},
		config.Mailbox{StorageID: idCarol, Address: carol, PasswordHash: h},
		config.Mailbox{StorageID: idTeam, Address: team, Kind: config.KindShared, KeepSentCopy: true, Grants: []config.Grant{
			{StorageID: idAlice, Rights: []string{config.RightWrite, config.RightSendAs}},
			{StorageID: idBob, Rights: []string{config.RightRead}},
		}},
		config.Mailbox{StorageID: idBoss, Address: boss, PasswordHash: h, Grants: []config.Grant{
			{StorageID: idAlice, Rights: []string{config.RightRead}},
		}},
	)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

type imapClient = imapclient.Client

type sharedEnv struct {
	*env
	mu     sync.Mutex
	quotas map[string]int64
}

func startShared(t *testing.T) *sharedEnv {
	t.Helper()
	se := &sharedEnv{env: &env{t: t}, quotas: map[string]int64{}}
	e := se.env
	e.free.Store(10 << 30)
	st, err := storage.Open(t.TempDir(), storage.Options{Quota: func(id string) int64 {
		se.mu.Lock()
		defer se.mu.Unlock()
		return se.quotas[id]
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.store = st
	cfg := sharedConfig(t)
	e.dir.Store(directory.New(cfg))
	kp, err := tls.X509KeyPair([]byte(cfg.Certificates[0].CertPEM), []byte(cfg.Certificates[0].KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	e.srv = New(Options{
		Store:     st,
		Directory: e.dir.Load,
		SignIn:    signin.New(signin.Options{Slots: slots}),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{kp}},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		FreeBytes: func() (int64, error) { return e.free.Load(), nil },
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.addr = ln.Addr().String()
	done := make(chan error, 1)
	go func() { done <- e.srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e.srv.Shutdown(ctx)
		<-done
	})
	return se
}

func (se *sharedEnv) as(who string) (*imapClient, *updates) {
	se.t.Helper()
	c, u := se.dial()
	if err := c.Login(who, password).Wait(); err != nil {
		se.t.Fatalf("login %s: %v", who, err)
	}
	return c, u
}

func (se *sharedEnv) put(id, folder, msg string, flags ...string) uint32 {
	se.t.Helper()
	res, err := se.store.Append(context.Background(), id, folder, strings.NewReader(msg), storage.AppendOptions{Flags: flags})
	if err != nil {
		se.t.Fatal(err)
	}
	return res.UID
}

// reconfigure swaps in cfg like a configuration load does.
func (se *sharedEnv) reconfigure(cfg *config.Config) {
	se.dir.Store(directory.New(cfg))
	se.srv.Reconfigure()
}

func listNames(t *testing.T, c *imapClient, options *imap.ListOptions) map[string][]imap.MailboxAttr {
	t.Helper()
	list, err := c.List("", "*", options).Collect()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]imap.MailboxAttr{}
	for _, l := range list {
		out[l.Mailbox] = l.Attrs
	}
	return out
}

func TestSharedListAndNamespace(t *testing.T) {
	se := startShared(t)
	c, _ := se.as(alice)
	names := listNames(t, c, nil)
	for _, want := range []string{"INBOX", "Sent", "Shared", "Shared/" + team, stName, "Shared/" + team + "/Sent",
		"Shared/" + team + "/Trash", "Shared/" + boss + "/INBOX"} {
		if _, ok := names[want]; !ok {
			t.Errorf("LIST misses %s: %v", want, names)
		}
	}
	// Never the special uses of another mailbox; parents are not selectable.
	for name, attrs := range names {
		if strings.HasPrefix(name, "Shared/") {
			for _, a := range attrs {
				switch a {
				case imap.MailboxAttrSent, imap.MailboxAttrDrafts, imap.MailboxAttrTrash, imap.MailboxAttrJunk, imap.MailboxAttrArchive:
					t.Errorf("%s carries %s", name, a)
				}
			}
		}
	}
	if !slices.Contains(names["Shared/"+team], imap.MailboxAttrNoSelect) || !slices.Contains(names["Sent"], imap.MailboxAttrSent) {
		t.Errorf("attributes %v", names)
	}
	// Subscribed without asking (iOS and Outlook list only those).
	sub := listNames(t, c, &imap.ListOptions{SelectSubscribed: true})
	if _, ok := sub[stName]; !ok {
		t.Errorf("not subscribed: %v", sub)
	}
	if err := c.Unsubscribe(stName).Wait(); err != nil {
		t.Errorf("unsubscribe: %v", err)
	}
	ns, err := c.Namespace().Wait()
	if err != nil || len(ns.Shared) != 1 || ns.Shared[0].Prefix != "Shared/" {
		t.Fatalf("namespace %+v %v", ns, err)
	}
	if _, err := c.Select("Shared/"+team, nil).Wait(); code(err) != imap.ResponseCodeCannot {
		t.Errorf("select parent: %v", err)
	}

	// Carol sees nothing shared and cannot reach it by name either.
	cc, _ := se.as(carol)
	for name := range listNames(t, cc, nil) {
		if strings.HasPrefix(name, "Shared") {
			t.Errorf("carol sees %s", name)
		}
	}
	if _, err := cc.Select(stName, nil).Wait(); code(err) != imap.ResponseCodeNonExistent {
		t.Errorf("carol select: %v", err)
	}
	if err := cc.Create("Shared/"+team+"/Mine", nil).Wait(); code(err) != imap.ResponseCodeCannot {
		t.Errorf("carol create under Shared: %v", err)
	}

	// Nobody signs in to the shared mailbox itself.
	c4, _ := se.dial()
	if err := c4.Login(team, password).Wait(); code(err) != imap.ResponseCodeAuthenticationFailed {
		t.Errorf("shared login: %v", err)
	}
}

func TestSharedReadOnly(t *testing.T) {
	se := startShared(t)
	se.put(idTeam, "INBOX", simple("one"))
	se.put(idBob, "INBOX", simple("own"))
	c, _ := se.as(bob)
	sd, err := c.Select(stName, nil).Wait()
	if err != nil || !sd.ReadOnly || sd.NumMessages != 1 {
		t.Fatalf("select %+v %v", sd, err)
	}
	if !slices.Equal(sd.PermanentFlags, []imap.Flag{imap.FlagSeen}) {
		t.Errorf("permanent flags %v", sd.PermanentFlags)
	}
	store := func(op imap.StoreFlagsOp, f imap.Flag) error {
		return c.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: op, Flags: []imap.Flag{f}, Silent: true}, nil).Close()
	}
	if err := store(imap.StoreFlagsAdd, imap.FlagFlagged); code(err) != imap.ResponseCodeNoPerm {
		t.Errorf("flag: %v", err)
	}
	if err := store(imap.StoreFlagsAdd, imap.FlagSeen); err != nil {
		t.Errorf("own read state: %v", err)
	}
	if err := c.Expunge().Close(); code(err) != imap.ResponseCodeNoPerm {
		t.Errorf("expunge: %v", err)
	}
	if _, err := c.Move(imap.SeqSetNum(1), "INBOX").Wait(); code(err) != imap.ResponseCodeNoPerm {
		t.Errorf("move out: %v", err)
	}
	if err := c.Create("Shared/"+team+"/New", nil).Wait(); code(err) != imap.ResponseCodeNoPerm {
		t.Errorf("create: %v", err)
	}
	msg := simple("in")
	cmd := c.Append(stName, int64(len(msg)), nil)
	cmd.Write([]byte(msg))
	cmd.Close()
	if _, err := cmd.Wait(); code(err) != imap.ResponseCodeNoPerm {
		t.Errorf("append: %v", err)
	}
	// Copying out needs read only; copying in needs write.
	if _, err := c.Copy(imap.SeqSetNum(1), "INBOX").Wait(); err != nil {
		t.Errorf("copy out: %v", err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Copy(imap.SeqSetNum(1), stName).Wait(); code(err) != imap.ResponseCodeNoPerm {
		t.Errorf("copy in: %v", err)
	}
	if st, _ := se.store.FolderStatus(context.Background(), idBob, "INBOX"); st.Messages != 2 {
		t.Errorf("bob's inbox %+v", st)
	}

	// Alice may write.
	a, _ := se.as(alice)
	sd, err = a.Select(stName, nil).Wait()
	if err != nil || sd.ReadOnly {
		t.Fatalf("alice select %+v %v", sd, err)
	}
	if err := a.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagFlagged}}, nil).Close(); err != nil {
		t.Errorf("alice flag: %v", err)
	}
	if err := a.Create("Shared/"+team+"/Projects", nil).Wait(); err != nil {
		t.Errorf("alice create: %v", err)
	}
	if _, err := se.store.FolderStatus(context.Background(), idTeam, "Projects"); err != nil {
		t.Errorf("folder not in the shared mailbox: %v", err)
	}
	// EXAMINE stays read-only for everything, the own read state too.
	if _, err := a.Select(stName, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := a.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close(); code(err) != imap.ResponseCodeCannot {
		t.Errorf("store after examine: %v", err)
	}
}

func seen(t *testing.T, c *imapClient, seq uint32) bool {
	t.Helper()
	msgs, err := c.Fetch(imap.SeqSetNum(seq), &imap.FetchOptions{Flags: true}).Collect()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("fetch flags: %v %v", msgs, err)
	}
	return slices.Contains(msgs[0].Flags, imap.FlagSeen)
}

func unseen(t *testing.T, c *imapClient) uint32 {
	t.Helper()
	st, err := c.Status(stName, &imap.StatusOptions{NumUnseen: true}).Wait()
	if err != nil {
		t.Fatal(err)
	}
	return *st.NumUnseen
}

func TestSharedSeenPerReader(t *testing.T) {
	se := startShared(t)
	se.put(idTeam, "INBOX", simple("one"))
	se.put(idTeam, "INBOX", simple("two"))
	a, ua := se.as(alice)
	b, ub := se.as(bob)
	if unseen(t, a) != 2 || unseen(t, b) != 2 {
		t.Fatal("unseen before")
	}
	for _, c := range []*imapClient{a, b} {
		if _, err := c.Select(stName, nil).Wait(); err != nil {
			t.Fatal(err)
		}
	}
	// Alice reads message 1 (implicit \Seen); only she sees it read.
	if _, err := a.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{{}}}).Collect(); err != nil {
		t.Fatal(err)
	}
	if !seen(t, a, 1) || seen(t, b, 1) {
		t.Errorf("after alice read: alice %v bob %v", seen(t, a, 1), seen(t, b, 1))
	}
	// Bob (read only) reads message 2 as well, with the implicit \Seen.
	if _, err := b.Fetch(imap.SeqSetNum(2), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{{}}}).Collect(); err != nil {
		t.Fatal(err)
	}
	if seen(t, a, 2) || !seen(t, b, 2) {
		t.Errorf("after bob read: alice %v bob %v", seen(t, a, 2), seen(t, b, 2))
	}
	sr, err := a.Search(&imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}, nil).Wait()
	if err != nil || !slices.Equal(sr.AllSeqNums(), []uint32{2}) {
		t.Errorf("alice UNSEEN %v %v", sr, err)
	}
	sr, err = b.Search(&imap.SearchCriteria{Flag: []imap.Flag{imap.FlagSeen}}, nil).Wait()
	if err != nil || !slices.Equal(sr.AllSeqNums(), []uint32{2}) {
		t.Errorf("bob SEEN %v %v", sr, err)
	}
	if err := a.Unselect().Wait(); err != nil {
		t.Fatal(err)
	}
	if unseen(t, a) != 1 {
		t.Errorf("alice unseen")
	}
	if _, err := a.Select(stName, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	ua.take()
	ub.take()

	// IDLE: bob's read state never reaches alice; her other session's does,
	// and so do new messages and others' flags.
	idle, err := a.Idle()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := b.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}, Silent: true}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ua.ch:
		t.Errorf("alice was told about bob's read state: %v", ua.flags)
	case <-time.After(300 * time.Millisecond):
	}
	a2, _ := se.as(alice)
	if _, err := a2.Select(stName, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := a2.Store(imap.SeqSetNum(2), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}, Silent: true}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	waitUpdate(t, ua, "alice's own read state in another session")
	se.put(idTeam, "INBOX", simple("three"))
	waitUpdate(t, ua, "a new message of the shared mailbox")
	if err := idle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := idle.Wait(); err != nil {
		t.Fatal(err)
	}
	_, exists, flags := ua.take()
	if !slices.Contains(exists, 3) || !slices.Contains(flags[2], imap.FlagSeen) {
		t.Errorf("alice updates: exists %v flags %v", exists, flags)
	}
	if _, ok := flags[1]; ok {
		t.Errorf("bob's read state broadcast: %v", flags)
	}
}

func waitUpdate(t *testing.T, u *updates, what string) {
	t.Helper()
	select {
	case <-u.ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("no update for %s", what)
	}
}

func TestSharedRevoke(t *testing.T) {
	se := startShared(t)
	se.put(idTeam, "INBOX", simple("one"))
	a, _ := se.as(alice)
	b, _ := se.as(bob)
	for _, c := range []*imapClient{a, b} {
		if _, err := c.Select(stName, nil).Wait(); err != nil {
			t.Fatal(err)
		}
	}
	// Bob loses his access, alice keeps only read.
	cfg := sharedConfig(t)
	for i := range cfg.Mailboxes {
		if cfg.Mailboxes[i].StorageID == idTeam {
			cfg.Mailboxes[i].Grants = []config.Grant{{StorageID: idAlice, Rights: []string{config.RightRead}}}
		}
	}
	se.reconfigure(cfg)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := b.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect()
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bob's session goes on after his access was removed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	err := a.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagFlagged}}, nil).Close()
	if code(err) != imap.ResponseCodeNoPerm {
		t.Errorf("alice after losing write: %v", err)
	}
	// Reading still works.
	seen(t, a, 1)
	// A new session of bob no longer sees the folders.
	b2, _ := se.as(bob)
	for name := range listNames(t, b2, nil) {
		if strings.HasPrefix(name, "Shared/"+team) {
			t.Errorf("bob still sees %s", name)
		}
	}

	// Moving the shared mailbox to another organisation ends the access
	// although the grant is still in the configuration.
	cfg = sharedConfig(t)
	cfg.Domains = append(cfg.Domains, config.Domain{Name: "other.example"})
	for i := range cfg.Mailboxes {
		if cfg.Mailboxes[i].StorageID == idTeam {
			cfg.Mailboxes[i].Address = "team@other.example"
		}
	}
	a3, _ := se.as(alice)
	se.reconfigure(cfg)
	if _, err := a3.Select("Shared/team@other.example/INBOX", nil).Wait(); code(err) != imap.ResponseCodeNonExistent {
		t.Errorf("across organisations: %v", err)
	}
	// Alice's first session had the folder open: it ended.
	if _, err := a.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect(); err == nil {
		t.Errorf("selected session goes on across organisations")
	}
}

func TestSharedCopyMoveQuota(t *testing.T) {
	se := startShared(t)
	se.put(idAlice, "INBOX", simple("mine"), storage.FlagSeen)
	se.put(idTeam, "INBOX", simple("team"))
	a, _ := se.as(alice)
	if _, err := a.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	// The destination's quota counts.
	se.mu.Lock()
	se.quotas[idTeam] = 10
	se.mu.Unlock()
	if _, err := a.Copy(imap.SeqSetNum(1), stName).Wait(); code(err) != imap.ResponseCodeOverQuota {
		t.Errorf("copy over quota: %v", err)
	}
	se.mu.Lock()
	se.quotas[idTeam] = 0
	se.mu.Unlock()
	cd, err := a.Copy(imap.SeqSetNum(1), stName).Wait()
	if err != nil || len(cd.DestUIDs) != 1 {
		t.Fatalf("copy in: %+v %v", cd, err)
	}
	u, _ := se.store.Usage(context.Background(), idTeam)
	if u.Messages != 2 {
		t.Errorf("team usage %+v", u)
	}
	// Alice's \Seen is hers in the shared mailbox; bob has not read it.
	b, _ := se.as(bob)
	if _, err := b.Select(stName, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if seen(t, b, 2) {
		t.Errorf("bob sees alice's read state")
	}
	if _, err := a.Select(stName, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if !seen(t, a, 2) || seen(t, a, 1) {
		t.Errorf("alice's read state after copy")
	}
	// Move out into her own Archive: gone from the shared mailbox.
	if _, err := a.Move(imap.SeqSetNum(1), "Archive").Wait(); err != nil {
		t.Fatalf("move out: %v", err)
	}
	if st, _ := se.store.FolderStatus(context.Background(), idTeam, "INBOX"); st.Messages != 1 {
		t.Errorf("team inbox after move %+v", st)
	}
	if st, _ := se.store.FolderStatus(context.Background(), idAlice, "Archive"); st.Messages != 1 {
		t.Errorf("alice archive %+v", st)
	}
	_, msgs, err := se.store.Select(context.Background(), idAlice, "Archive")
	if err != nil || len(msgs) != 1 {
		t.Fatal(err)
	}
	f, err := se.store.Open(msgs[0])
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(f)
	f.Close()
	if !strings.Contains(string(body), "body team") {
		t.Errorf("moved message %q", body)
	}
	// RENAME across mailboxes is refused.
	if err := a.Rename(stName[:len(stName)-len("INBOX")]+"Projects", "Projects", nil).Wait(); err == nil {
		t.Errorf("rename across mailboxes worked")
	}
}

func TestSharedDelegatedPersonal(t *testing.T) {
	se := startShared(t)
	se.put(idBoss, "INBOX", simple("for the boss"))
	a, _ := se.as(alice)
	name := "Shared/" + boss + "/INBOX"
	sd, err := a.Select(name, nil).Wait()
	if err != nil || !sd.ReadOnly {
		t.Fatalf("select %+v %v", sd, err)
	}
	// In a delegated personal mailbox the read state is the mailbox's own:
	// read only means read only.
	if err := a.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close(); code(err) != imap.ResponseCodeNoPerm {
		t.Errorf("store: %v", err)
	}
	if fl := flagsOfID(t, se, idBoss, "INBOX", 1); len(fl) != 0 {
		t.Errorf("boss flags %v", fl)
	}
}

func flagsOfID(t *testing.T, se *sharedEnv, id, folder string, uid uint32) []string {
	t.Helper()
	msgs, err := se.store.Messages(context.Background(), id, folder, []uint32{uid})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("messages %v %v", msgs, err)
	}
	return msgs[0].Flags
}

func TestSharedSentCopyOnce(t *testing.T) {
	se := startShared(t)
	a, _ := se.as(alice)
	msg := "From: team@example.org\r\nMessage-ID: <sent-1@example.org>\r\nSubject: hi\r\n\r\nbody\r\n"
	put := func() *imap.AppendData {
		cmd := a.Append("Shared/"+team+"/Sent", int64(len(msg)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}})
		cmd.Write([]byte(msg))
		cmd.Close()
		d, err := cmd.Wait()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	first, second := put(), put()
	if first.UID != second.UID {
		t.Errorf("stored twice: %d %d", first.UID, second.UID)
	}
	if st, _ := se.store.FolderStatus(context.Background(), idTeam, "Sent"); st.Messages != 1 {
		t.Errorf("sent %+v", st)
	}
	// Another folder of the shared mailbox is not de-duplicated.
	for range 2 {
		cmd := a.Append(stName, int64(len(msg)), nil)
		cmd.Write([]byte(msg))
		cmd.Close()
		if _, err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := se.store.FolderStatus(context.Background(), idTeam, "INBOX"); st.Messages != 2 {
		t.Errorf("inbox %+v", st)
	}
}
