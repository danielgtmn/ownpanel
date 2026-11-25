package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// transferMail is the mail service with import and export.
type transferMail struct {
	*fakeMail
	mu       sync.Mutex
	err      error // answer of every transfer call when set
	sources  []config.ImportSource
	resumes  []config.ImportResume
	parts    []string
	jobs     map[int64]config.ImportJob
	canceled []int64
}

func newTransferMail(f *fakeMail) *transferMail {
	return &transferMail{fakeMail: f, jobs: map[int64]config.ImportJob{}}
}

func (t *transferMail) CheckImport(_ context.Context, sid string, src config.ImportSource) (config.ImportCheck, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sources = append(t.sources, src)
	if t.err != nil {
		return config.ImportCheck{}, t.err
	}
	return config.ImportCheck{Folders: []config.ImportFolder{{Remote: "INBOX", Local: "INBOX", Messages: 2, Bytes: 10, Selected: true}}, Messages: 2, Bytes: 10}, nil
}

func (t *transferMail) add(j config.ImportJob) config.ImportJob {
	j.ID = int64(len(t.jobs) + 1)
	t.jobs[j.ID] = j
	return j
}

func (t *transferMail) StartImport(_ context.Context, sid string, st config.ImportStart) (config.ImportJob, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sources = append(t.sources, st.Source)
	if t.err != nil {
		return config.ImportJob{}, t.err
	}
	return t.add(config.ImportJob{StorageID: sid, Kind: config.ImportIMAP, State: config.ImportWaiting, Host: st.Source.Host, Port: st.Source.Port, Username: st.Source.Username}), nil
}

func (t *transferMail) CreateUpload(_ context.Context, sid string, req config.UploadRequest) (config.ImportJob, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return config.ImportJob{}, t.err
	}
	return t.add(config.ImportJob{StorageID: sid, Kind: config.ImportFile, State: config.ImportUploading, FileName: req.Name, FileSize: req.Size}), nil
}

func (t *transferMail) UploadPart(_ context.Context, sid string, id, offset int64, part io.Reader, size int64) (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return 0, t.err
	}
	b, _ := io.ReadAll(io.LimitReader(part, size))
	t.parts = append(t.parts, string(b))
	return offset + int64(len(b)), nil
}

func (t *transferMail) ResumeImport(_ context.Context, sid string, id int64, r config.ImportResume) (config.ImportJob, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resumes = append(t.resumes, r)
	if t.err != nil {
		return config.ImportJob{}, t.err
	}
	j, ok := t.jobs[id]
	if !ok || j.StorageID != sid {
		return config.ImportJob{}, &config.Error{Status: 404, Msg: "unknown import"}
	}
	j.State = config.ImportWaiting
	t.jobs[id] = j
	return j, nil
}

func (t *transferMail) ImportJobs(_ context.Context, sid string) ([]config.ImportJob, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return nil, t.err
	}
	out := []config.ImportJob{}
	for _, j := range t.jobs {
		if j.StorageID == sid {
			out = append(out, j)
		}
	}
	return out, nil
}

func (t *transferMail) ImportJob(_ context.Context, sid string, id int64) (config.ImportJob, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return config.ImportJob{}, t.err
	}
	j, ok := t.jobs[id]
	if !ok || j.StorageID != sid {
		return config.ImportJob{}, &config.Error{Status: 404, Msg: "unknown import"}
	}
	return j, nil
}

func (t *transferMail) CancelImport(_ context.Context, sid string, id int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	t.canceled = append(t.canceled, id)
	return nil
}

func (t *transferMail) Export(_ context.Context, sid string) (io.ReadCloser, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return nil, t.err
	}
	return io.NopCloser(strings.NewReader("PK zip of " + sid)), nil
}

// newTransferWorld is the mail world whose service can import and export.
func newTransferWorld(t *testing.T) (*mailWorld, *transferMail) {
	w := newMailWorld(t)
	tm := newTransferMail(w.mail)
	w.svc.mail = tm
	return w, tm
}

func storageIDOf(t *testing.T, w *mailWorld, id int64) string {
	t.Helper()
	m, err := w.svc.store.Mailbox(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return m.StorageID
}

var testSource = config.ImportSource{Host: "imap.example.org", Port: 993, Username: "me@example.org", Password: "other-secret-pw"}

func TestMailTransferPermissions(t *testing.T) {
	ctx := context.Background()
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", admin}, {"admin write token", rootWriteTok}, {"member", bob}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody}, {"system", System},
	}
	F, N, U := Forbidden, NotFound, Unauthenticated
	read := []Kind{ok, ok, ok, ok, F, ok, F, U, ok}
	readOther := []Kind{ok, ok, N, N, F, ok, F, U, ok}
	write := []Kind{ok, ok, ok, F, F, F, F, U, F}
	writeOther := []Kind{ok, ok, N, N, F, F, F, U, F}
	// A job of each mailbox, made directly in the service.
	job := func(w *mailWorld, tm *transferMail, box int64) int64 {
		tm.mu.Lock()
		defer tm.mu.Unlock()
		return tm.add(config.ImportJob{StorageID: storageIDOf(t, w, box), Kind: config.ImportFile, State: config.ImportReady, FileName: "a.mbox"}).ID
	}
	type op struct {
		name string
		run  func(w *mailWorld, tm *transferMail, a Actor, box int64) error
	}
	ops := []op{
		{"list imports", func(w *mailWorld, tm *transferMail, a Actor, box int64) error {
			_, err := w.svc.MailImports(ctx, a, box)
			return err
		}},
		{"get import", func(w *mailWorld, tm *transferMail, a Actor, box int64) error {
			_, err := w.svc.MailImportOf(ctx, a, box, job(w, tm, box))
			return err
		}},
		{"check", func(w *mailWorld, tm *transferMail, a Actor, box int64) error {
			_, err := w.svc.CheckMailImport(ctx, a, box, testSource)
			return err
		}},
		{"start", func(w *mailWorld, tm *transferMail, a Actor, box int64) error {
			_, err := w.svc.StartMailImport(ctx, a, box, config.ImportStart{Source: testSource})
			return err
		}},
		{"upload", func(w *mailWorld, tm *transferMail, a Actor, box int64) error {
			_, err := w.svc.CreateMailUpload(ctx, a, box, config.UploadRequest{Name: "a.mbox", Size: 3})
			return err
		}},
		{"upload part", func(w *mailWorld, tm *transferMail, a Actor, box int64) error {
			_, err := w.svc.UploadMailPart(ctx, a, box, job(w, tm, box), 0, strings.NewReader("abc"), 3)
			return err
		}},
		{"start file", func(w *mailWorld, tm *transferMail, a Actor, box int64) error {
			_, err := w.svc.ResumeMailImport(ctx, a, box, job(w, tm, box), config.ImportResume{})
			return err
		}},
		{"cancel", func(w *mailWorld, tm *transferMail, a Actor, box int64) error {
			return w.svc.CancelMailImport(ctx, a, box, job(w, tm, box))
		}},
		{"export", func(w *mailWorld, tm *transferMail, a Actor, box int64) error {
			ex, err := w.svc.ExportMailbox(ctx, a, box)
			if err == nil {
				ex.Body.Close()
			}
			return err
		}},
	}
	for _, o := range ops {
		reads := strings.HasPrefix(o.name, "list") || strings.HasPrefix(o.name, "get")
		for _, other := range []bool{false, true} {
			want := write
			switch {
			case reads && other:
				want = readOther
			case reads:
				want = read
			case other:
				want = writeOther
			}
			for i, ac := range actors {
				name := o.name + "/" + ac.name
				if other {
					name += "/admin's mailbox"
				}
				t.Run(name, func(t *testing.T) {
					w, tm := newTransferWorld(t)
					box := w.bobBox
					if other {
						box = w.rootBox
					}
					err := o.run(w, tm, ac.a, box)
					if want[i] == ok {
						if err != nil {
							t.Fatalf("want success, got %v: %v", KindOf(err), err)
						}
					} else {
						wantKind(t, err, want[i])
					}
				})
			}
		}
	}
}

func TestMailImportByAddress(t *testing.T) {
	w, _ := newTransferWorld(t)
	ctx := context.Background()
	id, err := w.svc.MailboxByAddress(ctx, bobRead, "BobSite.example.com", "Info")
	if err != nil || id != w.bobBox {
		t.Fatalf("got %d %v", id, err)
	}
	// Another person's mailbox is not found, like a missing one.
	if _, err := w.svc.MailboxByAddress(ctx, bob, "rootsite.example.com", "root"); KindOf(err) != NotFound {
		t.Fatalf("other's mailbox: %v", err)
	}
	if _, err := w.svc.MailboxByAddress(ctx, bob, "bobsite.example.com", "nobody"); KindOf(err) != NotFound {
		t.Fatalf("missing mailbox: %v", err)
	}
}

func TestMailImportKeepsPasswordsOut(t *testing.T) {
	w, tm := newTransferWorld(t)
	ctx := context.Background()
	j, err := w.svc.StartMailImport(ctx, bob, w.bobBox, config.ImportStart{Source: config.ImportSource{
		Host: " IMAP.Example.org. ", Username: " me@example.org ", Password: "other-secret-pw"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := tm.sources[0]; got.Host != "imap.example.org" || got.Port != 993 || got.Username != "me@example.org" || got.Password != "other-secret-pw" {
		t.Fatalf("passed on %v", got)
	}
	b, _ := json.Marshal(j)
	if strings.Contains(string(b), "other-secret-pw") || strings.Contains(string(b), "storage_id") || !strings.Contains(string(b), `"address":"info@bobsite.example.com"`) {
		t.Fatalf("answer %s", b)
	}
	// Continuing an interrupted job needs the password again.
	tm.mu.Lock()
	jj := tm.jobs[j.ID]
	jj.State = config.ImportInterrupted
	tm.jobs[j.ID] = jj
	tm.mu.Unlock()
	if _, err := w.svc.ResumeMailImport(ctx, bob, w.bobBox, j.ID, config.ImportResume{}); KindOf(err) != Invalid {
		t.Fatalf("continue without password: %v", err)
	}
	if _, err := w.svc.ResumeMailImport(ctx, bob, w.bobBox, j.ID, config.ImportResume{Password: "other-secret-pw"}); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.CancelMailImport(ctx, bob, w.bobBox, j.ID); err != nil {
		t.Fatal(err)
	}
	ex, err := w.svc.ExportMailbox(ctx, bob, w.bobBox)
	if err != nil {
		t.Fatal(err)
	}
	ex.Body.Close()
	if ex.FileName != "mail-info@bobsite.example.com-2026-11-25.zip" {
		t.Fatalf("file name %q", ex.FileName)
	}
	acts, _ := w.svc.store.Activities(ctx, 100)
	var got []string
	for _, a := range acts {
		if strings.Contains(a.Detail, "other-secret-pw") || strings.Contains(a.Target, "other-secret-pw") {
			t.Fatalf("password in the activity log: %+v", a)
		}
		if strings.HasPrefix(a.Action, "mail.mailbox.") && a.Action != "mail.mailbox.create" {
			got = append(got, a.Action+" "+a.Target+" "+a.Detail)
		}
	}
	slices.Reverse(got)
	want := []string{
		"mail.mailbox.import-started info@bobsite.example.com import 1 from me@example.org at imap.example.org",
		"mail.mailbox.import-started info@bobsite.example.com import 1 from me@example.org at imap.example.org (continued)",
		"mail.mailbox.import-cancelled info@bobsite.example.com import 1 from me@example.org at imap.example.org",
		"mail.mailbox.exported info@bobsite.example.com ",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("activity\n%q\nwant\n%q", got, want)
	}
}

func TestMailImportInputAndErrors(t *testing.T) {
	w, tm := newTransferWorld(t)
	ctx := context.Background()
	for _, src := range []config.ImportSource{
		{Port: 993, Username: "u", Password: "p"},
		{Host: "h.example", Port: 25, Username: "u", Password: "p"},
		{Host: "h.example", Port: 993, Password: "p"},
		{Host: "h.example", Port: 993, Username: "u"},
	} {
		if _, err := w.svc.CheckMailImport(ctx, bob, w.bobBox, src); KindOf(err) != Invalid {
			t.Fatalf("%v: %v", src, err)
		}
	}
	if _, err := w.svc.CreateMailUpload(ctx, bob, w.bobBox, config.UploadRequest{Name: "big.zip", Size: config.MaxImportBytes + 1}); KindOf(err) != TooLarge {
		t.Fatalf("too large: %v", err)
	}
	if _, err := w.svc.UploadMailPart(ctx, bob, w.bobBox, 1, 0, bytes.NewReader(nil), config.MaxUploadPart+1); KindOf(err) != TooLarge {
		t.Fatalf("part too large: %v", err)
	}
	for status, kind := range map[int]Kind{400: Invalid, 404: NotFound, 409: Conflict, 413: TooLarge, 429: Throttled, 502: Unavailable, 507: NoSpace, 500: Unavailable} {
		tm.mu.Lock()
		tm.err = &config.Error{Status: status, Msg: "reason from the service"}
		tm.mu.Unlock()
		_, err := w.svc.MailImports(ctx, bob, w.bobBox)
		if KindOf(err) != kind {
			t.Fatalf("%d: %v %v", status, KindOf(err), err)
		}
		if status != 500 && err.Error() != "reason from the service" {
			t.Fatalf("%d: message %q", status, err)
		}
	}
}

func TestMailImportRateLimitPerPerson(t *testing.T) {
	w, _ := newTransferWorld(t)
	ctx := context.Background()
	for i := range importsPerPerson {
		if _, err := w.svc.CheckMailImport(ctx, bob, w.bobBox, testSource); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if _, err := w.svc.CheckMailImport(ctx, bob, w.bobBox, testSource); KindOf(err) != Throttled {
		t.Fatalf("over the limit: %v", err)
	}
	// Others are not affected, and an hour later bob may again.
	if _, err := w.svc.CheckMailImport(ctx, admin, w.bobBox, testSource); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(time.Hour)
	if _, err := w.svc.CheckMailImport(ctx, bob, w.bobBox, testSource); err != nil {
		t.Fatal(err)
	}
}

func TestMailImportEventsGoToTheActivityLog(t *testing.T) {
	w, _ := newTransferWorld(t)
	ctx := context.Background()
	w.mail.set(func(f *fakeMail) {
		f.events = []config.Event{
			{ID: 1, Kind: config.EventImportFinished, Address: "info@bobsite.example.com", Detail: "import 1 from a.mbox: 3 copied, 0 already there, 0 failed"},
			{ID: 2, Kind: config.EventImportFailed, Address: "info@bobsite.example.com", Detail: "import 2 from me at imap.example.org: 0 copied, 0 already there, 0 failed: sign-in refused"},
			{ID: 3, Kind: config.EventImportPaused, Address: "info@bobsite.example.com", Detail: "x"},
			{ID: 4, Kind: config.EventImportInterrupted, Address: "info@bobsite.example.com", Detail: "y"},
		}
	})
	if err := w.svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	acts, _ := w.svc.store.Activities(ctx, 100)
	var got []string
	for _, a := range acts {
		if a.Actor == "mail" {
			got = append(got, a.Action)
		}
	}
	want := []string{"mail.mailbox.import-interrupted", "mail.mailbox.import-paused", "mail.mailbox.import-failed", "mail.mailbox.import-finished"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestMailTransferWithoutService(t *testing.T) {
	w := newMailWorld(t) // the fake has no import and export
	if _, err := w.svc.MailImports(context.Background(), bob, w.bobBox); KindOf(err) != Conflict {
		t.Fatalf("got %v", err)
	}
}
