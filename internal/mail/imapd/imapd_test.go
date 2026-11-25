package imapd

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
	"github.com/danielgtmn/ownpanel/internal/passwd"
)

const (
	alice    = "alice@example.org"
	password = "correct horse"
)

var (
	idAlice = mailtest.ID('a')
	slots   = passwd.NewSlots(4)

	hashOnce  sync.Once
	aliceHash string
)

func hash(t testing.TB) string {
	hashOnce.Do(func() {
		h, err := slots.Hash(password)
		if err != nil {
			t.Fatal(err)
		}
		aliceHash = h
	})
	return aliceHash
}

type env struct {
	t     *testing.T
	store *storage.Store
	srv   *Server
	addr  string
	dir   atomic.Pointer[directory.Directory]
	quota atomic.Int64
}

func testConfig(t testing.TB) *config.Config {
	return &config.Config{
		Hostname:     "mail.example.net",
		Certificates: []config.Certificate{mailtest.Cert(t, "mail.example.net", false)},
		Domains:      []config.Domain{{Name: "example.org"}},
		Mailboxes:    []config.Mailbox{{StorageID: idAlice, Address: alice, PasswordHash: hash(t)}},
		Limits:       config.Limits{MaxMessageBytes: 100000},
	}
}

func start(t *testing.T) *env {
	t.Helper()
	e := &env{t: t}
	st, err := storage.Open(t.TempDir(), storage.Options{Quota: func(string) int64 { return e.quota.Load() }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.store = st
	cfg := testConfig(t)
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
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return e
}

// updates collects unilateral responses of a client.
type updates struct {
	mu       sync.Mutex
	expunged []uint32
	exists   []uint32
	flags    map[uint32][]imap.Flag
	ch       chan struct{}
}

func (u *updates) handler() *imapclient.UnilateralDataHandler {
	poke := func() {
		select {
		case u.ch <- struct{}{}:
		default:
		}
	}
	return &imapclient.UnilateralDataHandler{
		Expunge: func(seq uint32) {
			u.mu.Lock()
			u.expunged = append(u.expunged, seq)
			u.mu.Unlock()
			poke()
		},
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				u.mu.Lock()
				u.exists = append(u.exists, *d.NumMessages)
				u.mu.Unlock()
				poke()
			}
		},
		Fetch: func(m *imapclient.FetchMessageData) {
			buf, err := m.Collect()
			if err != nil {
				return
			}
			u.mu.Lock()
			u.flags[buf.SeqNum] = buf.Flags
			u.mu.Unlock()
			poke()
		},
	}
}

func (u *updates) take() (expunged, exists []uint32, flags map[uint32][]imap.Flag) {
	u.mu.Lock()
	defer u.mu.Unlock()
	expunged, exists, flags = u.expunged, u.exists, u.flags
	u.expunged, u.exists, u.flags = nil, nil, map[uint32][]imap.Flag{}
	return
}

func (e *env) dial() (*imapclient.Client, *updates) {
	e.t.Helper()
	u := &updates{flags: map[uint32][]imap.Flag{}, ch: make(chan struct{}, 100)}
	c, err := imapclient.DialTLS(e.addr, &imapclient.Options{
		TLSConfig:             &tls.Config{InsecureSkipVerify: true},
		UnilateralDataHandler: u.handler(),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { c.Close() })
	return c, u
}

func (e *env) login() (*imapclient.Client, *updates) {
	e.t.Helper()
	c, u := e.dial()
	if err := c.Login(alice, password).Wait(); err != nil {
		e.t.Fatal(err)
	}
	return c, u
}

func (e *env) deliver(folder, msg string, flags ...string) uint32 {
	e.t.Helper()
	res, err := e.store.Append(context.Background(), idAlice, folder, strings.NewReader(msg), storage.AppendOptions{Flags: flags})
	if err != nil {
		e.t.Fatal(err)
	}
	return res.UID
}

func code(err error) imap.ResponseCode {
	var ie *imap.Error
	if errors.As(err, &ie) {
		return ie.Code
	}
	return ""
}

const msg1 = "From: Bob <bob@sender.test>\r\n" +
	"To: alice@example.org\r\n" +
	"Subject: Hello there\r\n" +
	"Date: Mon, 02 Mar 2026 10:00:00 +0000\r\n" +
	"Message-ID: <one@sender.test>\r\n" +
	"\r\n" +
	"First message body with a needle.\r\n"

const msg2 = "From: carol@sender.test\r\n" +
	"To: alice@example.org\r\n" +
	"Subject: Report\r\n" +
	"Date: Tue, 10 Mar 2026 10:00:00 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=b\r\n" +
	"\r\n" +
	"--b\r\n" +
	"Content-Type: text/plain\r\n" +
	"\r\n" +
	"Part one.\r\n" +
	"--b\r\n" +
	"Content-Type: application/octet-stream\r\n" +
	"\r\n" +
	"AAAA\r\n" +
	"--b--\r\n"

func simple(subject string) string {
	return "From: x@sender.test\r\nSubject: " + subject + "\r\n\r\nbody " + subject + "\r\n"
}

func TestLogin(t *testing.T) {
	e := start(t)

	// TLS from the first byte; LOGIN with the address in any case.
	c, _ := e.dial()
	if !c.Caps().Has(imap.CapIMAP4rev2) || !c.Caps().Has(imap.Cap("AUTH=PLAIN")) {
		t.Fatalf("caps before login %v", c.Caps())
	}
	if err := c.Login("Alice@Example.ORG", password).Wait(); err != nil {
		t.Fatal(err)
	}
	caps := c.Caps()
	for _, want := range []imap.Cap{imap.CapIMAP4rev1, imap.CapMove, imap.CapUIDPlus, imap.CapIdle, imap.CapSpecialUse, imap.CapNamespace, "APPENDLIMIT=100000"} {
		if !caps.Has(want) {
			t.Errorf("missing %s in %v", want, caps)
		}
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}

	// AUTHENTICATE PLAIN.
	c2, _ := e.dial()
	if err := c2.Authenticate(sasl.NewPlainClient("", alice, password)); err != nil {
		t.Fatal(err)
	}

	// Wrong password and unknown user answer the same.
	c3, _ := e.dial()
	err1 := c3.Login(alice, "wrong").Wait()
	err2 := c3.Login("nobody@example.org", password).Wait()
	if code(err1) != imap.ResponseCodeAuthenticationFailed || err1.Error() != err2.Error() {
		t.Fatalf("errors %v / %v", err1, err2)
	}
}

func TestThrottle(t *testing.T) {
	e := start(t)
	c, _ := e.dial()
	for i := 0; i < signin.PairLimit; i++ {
		if err := c.Login(alice, "wrong").Wait(); code(err) != imap.ResponseCodeAuthenticationFailed {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Now even the right password is refused, without hashing.
	if err := c.Login(alice, password).Wait(); code(err) != imap.ResponseCodeUnavailable {
		t.Fatalf("throttled: %v", err)
	}
}

func TestFetch(t *testing.T) {
	e := start(t)
	e.deliver("INBOX", msg1)
	e.deliver("INBOX", msg2)
	c, _ := e.login()
	sd, err := c.Select("inbox", nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if sd.NumMessages != 2 || sd.UIDNext != 3 || sd.UIDValidity == 0 {
		t.Fatalf("select %+v", sd)
	}

	msgs, err := c.Fetch(imap.SeqSetNum(1, 2), &imap.FetchOptions{
		UID: true, Flags: true, InternalDate: true, RFC822Size: true, Envelope: true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages", len(msgs))
	}
	m := msgs[0]
	if m.UID != 1 || m.RFC822Size != int64(len(msg1)) || m.InternalDate.IsZero() || len(m.Flags) != 0 {
		t.Errorf("message 1 %+v", m)
	}
	if env := m.Envelope; env == nil || env.Subject != "Hello there" || env.MessageID != "one@sender.test" ||
		len(env.From) != 1 || env.From[0].Name != "Bob" || env.From[0].Addr() != "bob@sender.test" {
		t.Errorf("envelope %+v", m.Envelope)
	}
	if mp, ok := msgs[1].BodyStructure.(*imap.BodyStructureMultiPart); !ok || len(mp.Children) != 2 || mp.Subtype != "mixed" {
		t.Errorf("body structure %#v", msgs[1].BodyStructure)
	}

	// BODY.PEEK does not set \Seen, partial fetch.
	peek := &imap.FetchItemBodySection{Peek: true}
	part := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Offset: 6, Size: 3}}
	text := &imap.FetchItemBodySection{Peek: true, Part: []int{1}}
	msgs, err = c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true, BodySection: []*imap.FetchItemBodySection{peek, part}}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(msgs[0].FindBodySection(peek)); got != msg1 {
		t.Errorf("BODY.PEEK[] = %q", got)
	}
	if got := string(msgs[0].FindBodySection(part)); got != msg1[6:9] {
		t.Errorf("partial = %q", got)
	}
	if slices.Contains(msgs[0].Flags, imap.FlagSeen) {
		t.Errorf("PEEK set \\Seen")
	}
	msgs, err = c.Fetch(imap.SeqSetNum(2), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{text}}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(msgs[0].FindBodySection(text)); got != "Part one." {
		t.Errorf("BODY[1] = %q", got)
	}

	// EXAMINE: BODY[] does not set \Seen.
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	body := &imap.FetchItemBodySection{}
	if _, err := c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{body}}).Collect(); err != nil {
		t.Fatal(err)
	}
	if fl := flagsOf(t, e, "INBOX", 1); slices.Contains(fl, storage.FlagSeen) {
		t.Errorf("EXAMINE set \\Seen")
	}
	if err := c.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagFlagged}}, nil).Close(); err == nil {
		t.Errorf("STORE worked read-only")
	}

	// SELECT: BODY[] sets \Seen and reports it.
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	msgs, err = c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{body}}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if string(msgs[0].FindBodySection(body)) != msg1 || !slices.Contains(msgs[0].Flags, imap.FlagSeen) {
		t.Errorf("BODY[]: %+v", msgs[0])
	}
	if fl := flagsOf(t, e, "INBOX", 1); !slices.Contains(fl, storage.FlagSeen) {
		t.Errorf("BODY[] did not set \\Seen: %v", fl)
	}
}

func flagsOf(t *testing.T, e *env, folder string, uid uint32) []string {
	t.Helper()
	ms, err := e.store.Messages(context.Background(), idAlice, folder, []uint32{uid})
	if err != nil || len(ms) != 1 {
		t.Fatalf("messages: %v %v", ms, err)
	}
	return ms[0].Flags
}

func TestStore(t *testing.T) {
	e := start(t)
	e.deliver("INBOX", simple("a"))
	e.deliver("INBOX", simple("b"))
	c, _ := e.login()
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	msgs, err := c.Store(imap.SeqSetNum(1, 2), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagFlagged, "$Label1"}}, nil).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || !slices.Contains(msgs[1].Flags, imap.FlagFlagged) || !slices.Contains(msgs[1].Flags, "$Label1") {
		t.Fatalf("store %+v", msgs)
	}
	msgs, err = c.Store(imap.UIDSetNum(2), &imap.StoreFlags{Op: imap.StoreFlagsDel, Silent: true, Flags: []imap.Flag{imap.FlagFlagged}}, nil).Collect()
	if err != nil || len(msgs) != 0 {
		t.Fatalf("silent store %v %v", msgs, err)
	}
	if fl := flagsOf(t, e, "INBOX", 2); !slices.Equal(fl, []string{"$Label1"}) {
		t.Errorf("flags %v", fl)
	}
	if err := c.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsSet, Flags: []imap.Flag{`\Bogus`}}, nil).Close(); err == nil {
		t.Error("invalid flag accepted")
	}
}

func TestTwoSessions(t *testing.T) {
	e := start(t)
	for _, s := range []string{"a", "b", "c", "d"} {
		e.deliver("INBOX", simple(s))
	}
	c1, _ := e.login()
	c2, u2 := e.login()
	for _, c := range []*imapclient.Client{c1, c2} {
		if _, err := c.Select("INBOX", nil).Wait(); err != nil {
			t.Fatal(err)
		}
	}

	// Session 1 deletes message 2 and expunges.
	if err := c1.Store(imap.SeqSetNum(2), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	got, err := c1.Expunge().Collect()
	if err != nil || !slices.Equal(got, []uint32{2}) {
		t.Fatalf("expunge %v %v", got, err)
	}

	// Session 2 must not see the expunge during FETCH: sequence numbers
	// stay as it knows them.
	msgs, err := c2.Fetch(imap.SeqSetNum(3), &imap.FetchOptions{UID: true}).Collect()
	if err != nil || len(msgs) != 1 || msgs[0].UID != 3 {
		t.Fatalf("fetch during pending expunge: %+v %v", msgs, err)
	}
	if exp, _, _ := u2.take(); len(exp) != 0 {
		t.Fatalf("expunge sent during FETCH: %v", exp)
	}
	if err := c2.Noop().Wait(); err != nil {
		t.Fatal(err)
	}
	exp, _, _ := u2.take()
	if !slices.Equal(exp, []uint32{2}) {
		t.Fatalf("session 2 expunges %v", exp)
	}
	msgs, err = c2.Fetch(imap.SeqSetNum(2), &imap.FetchOptions{UID: true}).Collect()
	if err != nil || len(msgs) != 1 || msgs[0].UID != 3 {
		t.Fatalf("after expunge: %+v %v", msgs, err)
	}

	// A new delivery: EXISTS.
	e.deliver("INBOX", simple("e"))
	if err := c2.Noop().Wait(); err != nil {
		t.Fatal(err)
	}
	if _, ex, _ := u2.take(); !slices.Equal(ex, []uint32{4}) {
		t.Fatalf("exists %v", ex)
	}

	// Session 1 moves message 1 (UID 1) to Archive; copies UID 3.
	md, err := c1.Move(imap.SeqSetNum(1), "Archive").Wait()
	if err != nil {
		t.Fatal(err)
	}
	if md.UIDValidity == 0 || md.SourceUIDs.String() != "1" || md.DestUIDs.String() != "1" {
		t.Errorf("move data %+v", md)
	}
	cd, err := c1.Copy(imap.UIDSetNum(3), "Trash").Wait()
	if err != nil || cd.SourceUIDs.String() != "3" || cd.DestUIDs.String() != "1" {
		t.Errorf("copy %+v %v", cd, err)
	}
	if _, err := c1.Copy(imap.UIDSetNum(3), "Nope").Wait(); code(err) != imap.ResponseCodeTryCreate {
		t.Errorf("copy to missing folder: %v", err)
	}
	if err := c2.Noop().Wait(); err != nil {
		t.Fatal(err)
	}
	if exp, _, _ := u2.take(); !slices.Equal(exp, []uint32{1}) {
		t.Fatalf("move expunges %v", exp)
	}
	// Both sessions agree: UIDs 3, 4, 5 at 1, 2, 3.
	for i, c := range []*imapclient.Client{c1, c2} {
		msgs, err := c.Fetch(imap.SeqSetNum(1, 2, 3), &imap.FetchOptions{UID: true}).Collect()
		if err != nil || len(msgs) != 3 || msgs[0].UID != 3 || msgs[1].UID != 4 || msgs[2].UID != 5 {
			t.Fatalf("session %d view: %+v %v", i+1, msgs, err)
		}
	}

	// UID EXPUNGE only removes the named UIDs.
	if err := c1.Store(imap.UIDSetNum(3, 4), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	got, err = c1.UIDExpunge(imap.UIDSetNum(4)).Collect()
	if err != nil || !slices.Equal(got, []uint32{2}) {
		t.Fatalf("uid expunge %v %v", got, err)
	}
	// CLOSE expunges the rest silently.
	if err := c1.UnselectAndExpunge().Wait(); err != nil {
		t.Fatal(err)
	}
	if err := c2.Noop().Wait(); err != nil {
		t.Fatal(err)
	}
	if exp, _, _ := u2.take(); !slices.Equal(exp, []uint32{2, 1}) {
		t.Fatalf("session 2 after uid expunge and close: %v", exp)
	}
}

func TestIdle(t *testing.T) {
	e := start(t)
	c, u := e.login()
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	idle, err := c.Idle()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	e.deliver("INBOX", simple("new"))
	select {
	case <-u.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no update during IDLE")
	}
	if err := idle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := idle.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, ex, _ := u.take(); !slices.Equal(ex, []uint32{1}) {
		t.Fatalf("exists %v", ex)
	}
}

func TestAppend(t *testing.T) {
	e := start(t)
	c, _ := e.login()
	appendMsg := func(folder, msg string) (*imap.AppendData, error) {
		cmd := c.Append(folder, int64(len(msg)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen, `\Recent`}})
		cmd.Write([]byte(msg))
		cmd.Close()
		return cmd.Wait()
	}
	ad, err := appendMsg("Drafts", simple("draft"))
	if err != nil || ad.UID != 1 || ad.UIDValidity == 0 {
		t.Fatalf("append %+v %v", ad, err)
	}
	if fl := flagsOf(t, e, "Drafts", 1); !slices.Equal(fl, []string{storage.FlagSeen}) {
		t.Errorf("flags %v", fl)
	}
	if _, err := appendMsg("Missing", simple("x")); code(err) != imap.ResponseCodeTryCreate {
		t.Errorf("missing folder: %v", err)
	}
	e.quota.Store(100)
	if _, err := appendMsg("INBOX", simple(strings.Repeat("y", 200))); code(err) != imap.ResponseCodeOverQuota {
		t.Errorf("over quota: %v", err)
	}
	// The connection is still usable.
	if err := c.Noop().Wait(); err != nil {
		t.Fatal(err)
	}

	// Over the size limit: refused before the client sends the message
	// (imapclient gives up the connection then, so this is raw).
	tc, err := tls.Dial("tcp", e.addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	tc.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(tc)
	r.ReadString('\n') // greeting
	io.WriteString(tc, "a LOGIN "+alice+" \""+password+"\"\r\nb APPEND INBOX {100001}\r\n")
	var lines []string
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err, lines)
		}
		lines = append(lines, l)
		if strings.HasPrefix(l, "b ") {
			break
		}
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "b NO [TOOBIG]") {
		t.Errorf("too large: %q", lines)
	}
}

func TestSearch(t *testing.T) {
	e := start(t)
	e.deliver("INBOX", msg1)
	e.deliver("INBOX", msg2, storage.FlagSeen)
	e.deliver("INBOX", simple("third"), storage.FlagFlagged)
	c, _ := e.login()
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		crit imap.SearchCriteria
		want []uint32
	}{
		{"all", imap.SearchCriteria{}, []uint32{1, 2, 3}},
		{"unseen", imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}, []uint32{1, 3}},
		{"flagged", imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}, []uint32{3}},
		{"subject", imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: "hello"}}}, []uint32{1}},
		{"from", imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "From", Value: "carol"}}}, []uint32{2}},
		{"body", imap.SearchCriteria{Body: []string{"NEEDLE"}}, []uint32{1}},
		{"text in part", imap.SearchCriteria{Text: []string{"part one"}}, []uint32{2}},
		{"text in header", imap.SearchCriteria{Text: []string{"report"}}, []uint32{2}},
		{"larger", imap.SearchCriteria{Larger: 150}, []uint32{1, 2}},
		{"sent before", imap.SearchCriteria{SentBefore: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)}, []uint32{1}},
		{"since", imap.SearchCriteria{Since: time.Now().Add(-48 * time.Hour)}, []uint32{1, 2, 3}},
		{"before", imap.SearchCriteria{Before: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}, nil},
		{"seq", imap.SearchCriteria{SeqNum: []imap.SeqSet{imap.SeqSetNum(2, 3)}}, []uint32{2, 3}},
		{"uid", imap.SearchCriteria{UID: []imap.UIDSet{imap.UIDSetNum(1)}}, []uint32{1}},
		{"not", imap.SearchCriteria{Not: []imap.SearchCriteria{{Flag: []imap.Flag{imap.FlagSeen}}}}, []uint32{1, 3}},
		{"or", imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{{Flag: []imap.Flag{imap.FlagSeen}}, {Flag: []imap.Flag{imap.FlagFlagged}}}}}, []uint32{2, 3}},
	} {
		sd, err := c.UIDSearch(&tc.crit, nil).Wait()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var got []uint32
		for _, u := range sd.AllUIDs() {
			got = append(got, uint32(u))
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	sd, err := c.Search(&imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}, nil).Wait()
	if err != nil || !slices.Equal(sd.AllSeqNums(), []uint32{3}) {
		t.Errorf("seq search %v %v", sd, err)
	}
}

func TestFolders(t *testing.T) {
	e := start(t)
	c, _ := e.login()
	list := func(opts *imap.ListOptions, pattern string) map[string][]imap.MailboxAttr {
		t.Helper()
		ls, err := c.List("", pattern, opts).Collect()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]imap.MailboxAttr{}
		for _, l := range ls {
			if l.Delim != '/' {
				t.Errorf("delimiter %q", l.Delim)
			}
			out[l.Mailbox] = l.Attrs
		}
		return out
	}
	all := list(nil, "*")
	for name, use := range map[string]imap.MailboxAttr{"INBOX": "", "Sent": imap.MailboxAttrSent, "Drafts": imap.MailboxAttrDrafts,
		"Junk": imap.MailboxAttrJunk, "Trash": imap.MailboxAttrTrash, "Archive": imap.MailboxAttrArchive} {
		attrs, ok := all[name]
		if !ok || (use != "" && !slices.Contains(attrs, use)) {
			t.Errorf("%s: %v %v", name, ok, attrs)
		}
	}
	special := list(&imap.ListOptions{SelectSpecialUse: true}, "*")
	if len(special) != 5 {
		t.Errorf("special-use %v", special)
	}

	if err := c.Create("Projects/2026", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := c.Create("Projects/2026", nil).Wait(); code(err) != imap.ResponseCodeAlreadyExists {
		t.Errorf("create twice: %v", err)
	}
	// The parent does not exist: listed as \Noselect where "%" stops.
	top := list(nil, "%")
	if attrs := top["Projects"]; !slices.Contains(attrs, imap.MailboxAttrNoSelect) {
		t.Errorf("implied parent %v", top)
	}
	if _, ok := list(nil, "*")["Projects/2026"]; !ok {
		t.Error("child not listed")
	}
	if err := c.Rename("Projects/2026", "Old", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	e.deliver("Old", simple("kept"))
	st, err := c.Status("Old", &imap.StatusOptions{NumMessages: true, UIDNext: true, Size: true, NumUnseen: true}).Wait()
	if err != nil || *st.NumMessages != 1 || st.UIDNext != 2 || *st.Size == 0 || *st.NumUnseen != 1 {
		t.Fatalf("status %+v %v", st, err)
	}
	// LIST-STATUS.
	ls, err := c.List("", "Old", &imap.ListOptions{ReturnStatus: &imap.StatusOptions{NumMessages: true}}).Collect()
	if err != nil || len(ls) != 1 || ls[0].Status == nil || *ls[0].Status.NumMessages != 1 {
		t.Fatalf("list-status %+v %v", ls, err)
	}
	if err := c.Unsubscribe("INBOX").Wait(); err != nil {
		t.Fatal(err)
	}
	subs := list(&imap.ListOptions{SelectSubscribed: true}, "*")
	if _, ok := subs["INBOX"]; ok || len(subs) != 5 {
		t.Errorf("subscribed %v", subs)
	}
	if err := c.Delete("Old").Wait(); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete("INBOX").Wait(); err == nil {
		t.Error("deleted INBOX")
	}
	if _, err := c.Select("Old", nil).Wait(); code(err) != imap.ResponseCodeNonExistent {
		t.Errorf("select deleted: %v", err)
	}
	ns, err := c.Namespace().Wait()
	if err != nil || len(ns.Personal) != 1 || ns.Personal[0].Delim != '/' {
		t.Errorf("namespace %+v %v", ns, err)
	}
	if err := c.Unselect().Wait(); err == nil {
		t.Error("UNSELECT without a selected folder worked")
	}
}

func TestConfigChangeEndsSession(t *testing.T) {
	e := start(t)
	c, _ := e.login()
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	h, err := slots.Hash("new password")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Mailboxes[0].PasswordHash = h
	e.dir.Store(directory.New(cfg))
	e.srv.Reconfigure()
	deadline := time.Now().Add(5 * time.Second)
	for c.Noop().Wait() == nil {
		if time.Now().After(deadline) {
			t.Fatal("session still open after the password changed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The old password no longer works, the new one does.
	c2, _ := e.dial()
	if err := c2.Login(alice, password).Wait(); err == nil {
		t.Error("old password accepted")
	}
	if err := c2.Login(alice, "new password").Wait(); err != nil {
		t.Error(err)
	}
}

func TestConnectionLimit(t *testing.T) {
	e := start(t)
	cfg := testConfig(t)
	cfg.Limits.ConnectionsPerIP = 2
	e.dir.Store(directory.New(cfg))
	e.srv.Reconfigure()
	e.dial()
	e.dial()
	tc, err := tls.Dial("tcp", e.addr, &tls.Config{InsecureSkipVerify: true})
	if err == nil {
		tc.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err = tc.Read(make([]byte, 1))
		tc.Close()
	}
	if err == nil {
		t.Fatal("third connection was served")
	}
}
