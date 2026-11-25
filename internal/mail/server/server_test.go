package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

var (
	idAlice = mailtest.ID('a')
	idBob   = mailtest.ID('b')
)

type instance struct {
	srv    *Server
	client *config.Client
	cancel context.CancelFunc
	done   chan error
}

// socketDir is short: Unix socket paths are limited to ~104 bytes on macOS.
func socketDir(t *testing.T) string {
	d, err := os.MkdirTemp("", "om")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func run(t *testing.T, data, sock string) *instance {
	t.Helper()
	srv, err := New(Options{
		DataDir:         data,
		AdminSocket:     filepath.Join(sock, "admin.sock"),
		SMTPAddr:        "127.0.0.1:0",
		SubmissionAddr:  ":587",
		SubmissionsAddr: ":465",
		IMAPSAddr:       "127.0.0.1:0",
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		FreeBytes:       func() (int64, error) { return 10 << 30, nil },
		Outbound: func(context.Context) config.OutboundCheck {
			return config.OutboundCheck{OK: true, Target: "mx.test:25"}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	in := &instance{srv: srv, client: config.NewClient(filepath.Join(sock, "admin.sock")), cancel: cancel, done: make(chan error, 1)}
	go func() { in.done <- srv.Run(ctx) }()
	// Wait for the socket.
	for i := 0; ; i++ {
		if _, err := in.client.Status(context.Background()); err == nil {
			break
		} else if i > 200 {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return in
}

func (in *instance) stop(t *testing.T) {
	in.cancel()
	if err := <-in.done; err != nil {
		t.Fatal(err)
	}
}

func testConfig(t *testing.T) *config.Config {
	return &config.Config{
		Hostname: "mail.example.net",
		Certificates: []config.Certificate{
			mailtest.Cert(t, "mail.example.net", false),
			mailtest.Cert(t, "mail.example.net", true),
		},
		Domains: []config.Domain{{Name: "example.org"}},
		Mailboxes: []config.Mailbox{
			{StorageID: idAlice, Address: "alice@example.org", PasswordHash: mailtest.Hash},
		},
		Limits: config.Limits{MaxMessageBytes: 10000},
	}
}

func deliver(t *testing.T, addr, to string) {
	t.Helper()
	c, err := smtp.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SendMail("x@sender.test", []string{to}, strings.NewReader("Subject: t\r\n\r\nhi\r\n")); err != nil {
		t.Fatal(err)
	}
}

func listener(st config.Status, name string) config.Listener {
	for _, l := range st.Listeners {
		if l.Name == name {
			return l
		}
	}
	return config.Listener{}
}

func TestLoadStatusResume(t *testing.T) {
	ctx := context.Background()
	data, sock := t.TempDir(), socketDir(t)
	in := run(t, data, sock)

	st, err := in.client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.ConfigHash != "" || listener(st, "smtp").Listening || listener(st, "imaps").Error != notReady ||
		listener(st, "submission").Error != notBuilt {
		t.Fatalf("empty status %+v", st)
	}
	if fi, err := os.Stat(filepath.Join(sock, "admin.sock")); err != nil || fi.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode: %v %v", fi.Mode(), err)
	}

	// Invalid configurations are refused with 400.
	bad := testConfig(t)
	bad.Mailboxes[0].Address = "alice@other.test"
	var ae *config.Error
	if err := in.client.Load(ctx, bad); !errors.As(err, &ae) || ae.Status != 400 {
		t.Fatalf("invalid config: %v", err)
	}

	cfg := testConfig(t)
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st, _ = in.client.Status(ctx)
	smtpL := listener(st, "smtp")
	if st.ConfigHash != cfg.Hash() || !smtpL.Listening || st.CertificateExpires.IsZero() {
		t.Fatalf("status %+v", st)
	}
	if fi, err := os.Stat(filepath.Join(data, configFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config.json: %v", err)
	}

	imapsL := listener(st, "imaps")
	if !imapsL.Listening || imapsL.Error != "" || imapsL.Address == "" || imapsL.Address == smtpL.Address {
		t.Fatalf("imaps %+v", imapsL)
	}
	// IMAP speaks TLS from the first byte.
	ic, err := imapclient.DialTLS(imapsL.Address, &imapclient.Options{TLSConfig: &tls.Config{InsecureSkipVerify: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ic.Noop().Wait(); err != nil {
		t.Fatal(err)
	}
	if !ic.Caps().Has(imap.CapIMAP4rev2) {
		t.Errorf("caps %v", ic.Caps())
	}
	if err := ic.Login("alice@example.org", "wrong").Wait(); err == nil {
		t.Error("login with a wrong password worked")
	}
	ic.Close()

	deliver(t, smtpL.Address, "alice@example.org")
	st, _ = in.client.Status(ctx)
	if len(st.Mailboxes) != 1 || st.Mailboxes[0].Messages != 1 || st.Mailboxes[0].UsedBytes == 0 {
		t.Fatalf("mailboxes %+v", st.Mailboxes)
	}

	// TLS: ECDSA for clients that can, RSA otherwise, whatever the SNI.
	for _, c := range []struct {
		suites []uint16
		sni    string
		rsa    bool
	}{
		{nil, "", false},
		{nil, "unknown.test", false},
		{[]uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}, "", true},
	} {
		tc := &tls.Config{InsecureSkipVerify: true, ServerName: c.sni, CipherSuites: c.suites, MaxVersion: tls.VersionTLS12}
		sc, err := smtp.DialStartTLS(smtpL.Address, tc)
		if err != nil {
			t.Fatal(err)
		}
		if err := sc.Noop(); err != nil { // the handshake happens on first use
			t.Fatal(err)
		}
		cs, _ := sc.TLSConnectionState()
		_, isRSA := cs.PeerCertificates[0].PublicKey.(interface{ Size() int })
		if isRSA != c.rsa {
			t.Errorf("%+v: got RSA=%v", c, isRSA)
		}
		sc.Close()
	}

	in.stop(t)

	// Resume: the next start has the configuration without a push.
	in = run(t, data, sock)
	st, _ = in.client.Status(ctx)
	if st.ConfigHash != cfg.Hash() || !listener(st, "smtp").Listening || !listener(st, "imaps").Listening || st.Mailboxes[0].Messages != 1 {
		t.Fatalf("after restart %+v", st)
	}
	in.stop(t)
}

func TestBindingRefused(t *testing.T) {
	ctx := context.Background()
	in := run(t, t.TempDir(), socketDir(t))
	defer in.stop(t)
	cfg := testConfig(t)
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st, _ := in.client.Status(ctx)
	deliver(t, listener(st, "smtp").Address, "alice@example.org")

	// The panel now claims alice's storage belongs to bob.
	swapped := testConfig(t)
	swapped.Mailboxes[0].Address = "bob@example.org"
	err := in.client.Load(ctx, swapped)
	var ae *config.Error
	if !errors.As(err, &ae) || ae.Status != 400 || !strings.Contains(ae.Msg, "belongs to alice@example.org") {
		t.Fatalf("binding: %v", err)
	}
	st, _ = in.client.Status(ctx)
	if st.ConfigHash != cfg.Hash() {
		t.Fatal("refused configuration was applied")
	}
}

func TestOrphansUnblockDelete(t *testing.T) {
	ctx := context.Background()
	in := run(t, t.TempDir(), socketDir(t))
	defer in.stop(t)
	cfg := testConfig(t)
	cfg.Mailboxes = append(cfg.Mailboxes, config.Mailbox{StorageID: idBob, Address: "bob@example.org", PasswordHash: mailtest.Hash})
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st, _ := in.client.Status(ctx)
	deliver(t, listener(st, "smtp").Address, "bob@example.org")
	store := in.srv.Store()
	if err := store.SetBlocked(ctx, idBob, "sent too much"); err != nil {
		t.Fatal(err)
	}
	st, _ = in.client.Status(ctx)
	if !st.Mailboxes[1].Blocked || st.Mailboxes[1].BlockedReason != "sent too much" {
		t.Fatalf("block not reported %+v", st.Mailboxes)
	}
	if err := in.client.Unblock(ctx, idBob); err != nil {
		t.Fatal(err)
	}
	st, _ = in.client.Status(ctx)
	if st.Mailboxes[1].Blocked {
		t.Fatal("still blocked")
	}
	var ae *config.Error
	if err := in.client.Unblock(ctx, mailtest.ID('9')); !errors.As(err, &ae) || ae.Status != 404 {
		t.Fatalf("unknown unblock: %v", err)
	}

	// Bob is removed from the configuration: his data stays and is
	// reported until the panel deletes it.
	if err := in.client.Load(ctx, testConfig(t)); err != nil {
		t.Fatal(err)
	}
	st, _ = in.client.Status(ctx)
	if len(st.Orphans) != 1 || st.Orphans[0] != idBob {
		t.Fatalf("orphans %v", st.Orphans)
	}
	if err := in.client.DeleteMailbox(ctx, idBob); err != nil {
		t.Fatal(err)
	}
	if err := in.client.DeleteMailbox(ctx, idBob); err != nil {
		t.Fatal("second delete:", err)
	}
	st, _ = in.client.Status(ctx)
	if len(st.Orphans) != 0 {
		t.Fatalf("orphans after delete %v", st.Orphans)
	}
	if u, _ := store.Usage(ctx, idBob); u != (storage.Usage{}) {
		t.Fatal("data left")
	}

	chk, err := in.client.CheckOutbound(ctx)
	if err != nil || !chk.OK {
		t.Fatalf("outbound %v %+v", err, chk)
	}
	store.AddEvent(ctx, config.Event{Kind: config.EventBlocked, StorageID: idAlice, Detail: "x"})
	ev, err := in.client.Events(ctx, 0)
	if err != nil || len(ev) != 1 || ev[0].Kind != config.EventBlocked {
		t.Fatalf("events %v %+v", err, ev)
	}
	ev, _ = in.client.Events(ctx, ev[0].ID)
	if len(ev) != 0 {
		t.Fatal("cursor")
	}
}

func TestListenerStates(t *testing.T) {
	ctx := context.Background()
	// Occupy a port to see the bind error.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	data, sock := t.TempDir(), socketDir(t)
	srv, err := New(Options{DataDir: data, AdminSocket: filepath.Join(sock, "a.sock"), SMTPAddr: busy.Addr().String(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Run(c) }()
	defer func() { cancel(); <-done }()
	client := config.NewClient(filepath.Join(sock, "a.sock"))
	for i := 0; ; i++ {
		if _, err := client.Status(ctx); err == nil {
			break
		} else if i > 200 {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Without a certificate nothing listens.
	cfg := testConfig(t)
	cfg.Certificates = nil
	if err := client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st, _ := client.Status(ctx)
	if l := listener(st, "smtp"); l.Listening || l.Error != notReady {
		t.Fatalf("not ready: %+v", l)
	}
	if err := client.Load(ctx, testConfig(t)); err != nil {
		t.Fatal(err)
	}
	st, _ = client.Status(ctx)
	if l := listener(st, "smtp"); l.Listening || l.Error != "address already in use" {
		t.Fatalf("busy: %+v", l)
	}
	ev, _ := client.Events(ctx, 0)
	if len(ev) != 1 || ev[0].Kind != config.EventListenerFailed {
		t.Fatalf("events %+v", ev)
	}
}

func TestStaleSocket(t *testing.T) {
	sock := socketDir(t)
	path := filepath.Join(sock, "admin.sock")
	// A socket file left by a crashed process.
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	in := run(t, t.TempDir(), sock)
	in.stop(t)
	// A second instance on a live socket refuses to start.
	in = run(t, t.TempDir(), sock)
	defer in.stop(t)
	srv, err := New(Options{DataDir: t.TempDir(), AdminSocket: path, SMTPAddr: "127.0.0.1:0", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("second instance: %v", err)
	}
}
