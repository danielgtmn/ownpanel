package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/panel"
)

// fakeTransferService is the mail service with import and export.
type fakeTransferService struct {
	fakeMailService
	mu       sync.Mutex
	checkErr error
	sources  []config.ImportSource
	parts    []string
	offsets  []int64
	resumes  []config.ImportResume
	jobs     []config.ImportJob
	canceled []string
}

func (f *fakeTransferService) CheckImport(_ context.Context, _ string, src config.ImportSource) (config.ImportCheck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sources = append(f.sources, src)
	if f.checkErr != nil {
		return config.ImportCheck{}, f.checkErr
	}
	return config.ImportCheck{Folders: []config.ImportFolder{{Remote: "INBOX", Local: "INBOX", Messages: 1, Bytes: 5, Selected: true}}, Messages: 1, Bytes: 5, QuotaLeft: -1}, nil
}

func (f *fakeTransferService) addJob(j config.ImportJob) config.ImportJob {
	j.ID = strconv.Itoa(len(f.jobs) + 1)
	f.jobs = append(f.jobs, j)
	return j
}

func (f *fakeTransferService) StartImport(_ context.Context, sid string, st config.ImportStart) (config.ImportJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sources = append(f.sources, st.Source)
	return f.addJob(config.ImportJob{StorageID: sid, Kind: config.ImportIMAP, State: config.ImportWaiting, Host: st.Source.Host, Port: st.Source.Port, Username: st.Source.Username, Choices: st.Choices}), nil
}

func (f *fakeTransferService) CreateUpload(_ context.Context, sid string, req config.UploadRequest) (config.ImportJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addJob(config.ImportJob{StorageID: sid, Kind: config.ImportFile, State: config.ImportUploading, FileName: req.Name, FileSize: req.Size}), nil
}

func (f *fakeTransferService) UploadPart(_ context.Context, _, _ string, offset int64, part io.Reader, size int64) (int64, error) {
	b, err := io.ReadAll(io.LimitReader(part, size))
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.parts = append(f.parts, string(b))
	f.offsets = append(f.offsets, offset)
	return offset + int64(len(b)), nil
}

func (f *fakeTransferService) job(sid, id string) (config.ImportJob, error) {
	for _, j := range f.jobs {
		if j.ID == id && j.StorageID == sid {
			return j, nil
		}
	}
	return config.ImportJob{}, &config.Error{Status: 404, Msg: "unknown import"}
}

func (f *fakeTransferService) ResumeImport(_ context.Context, sid, id string, r config.ImportResume) (config.ImportJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes = append(f.resumes, r)
	return f.job(sid, id)
}

func (f *fakeTransferService) ImportJobs(_ context.Context, _ string) ([]config.ImportJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]config.ImportJob{}, f.jobs...), nil
}

func (f *fakeTransferService) ImportJob(_ context.Context, sid, id string) (config.ImportJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, err := f.job(sid, id)
	j.Failures = []config.ImportFailure{{Folder: "INBOX", Reason: "too large"}}
	return j, err
}

func (f *fakeTransferService) CancelImport(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, id)
	return nil
}

func (f *fakeTransferService) Export(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("PK\x03\x04 the zip")), nil
}

const otherPassword = "other-account-secret"

func TestMailTransferRoutes(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	mail := &fakeTransferService{}
	e := newMailEnvWith(t, mail)
	wantStatus(t, e.as("PUT", "/api/settings", `{"mail_host":"mail.example.net"}`), 200, "")
	wantStatus(t, e.as("POST", "/api/mail/domains", `{"domain":"example.com"}`), 201, "")
	wantStatus(t, e.as("POST", "/api/mail/domains/example.com/mailboxes", `{"local_part":"info","password":"mailbox-password-1"}`), 201, "")
	base := "/api/mail/domains/example.com/mailboxes/info"

	// Checking another account: the password goes to the service only.
	src := `{"host":"imap.example.org","port":993,"username":"me@example.org","password":"` + otherPassword + `"}`
	rec := e.as("POST", base+"/imports/check", src)
	wantStatus(t, rec, 200, "")
	wantKeys(t, rec.Body.Bytes(), "folders", "messages", "bytes", "quota_bytes", "used_bytes", "quota_left")
	if len(mail.sources) != 1 || mail.sources[0].Password != otherPassword {
		t.Fatalf("passed on %v", mail.sources)
	}
	mail.checkErr = &config.Error{Status: 502, Msg: "the other server refused the sign-in"}
	rec = e.as("POST", base+"/imports/check", src)
	wantStatus(t, rec, 502, "refused the sign-in")
	mail.checkErr = &config.Error{Status: 507, Msg: "not enough disk space"}
	wantStatus(t, e.as("POST", base+"/imports/check", src), 507, "disk space")
	mail.checkErr = nil
	wantStatus(t, e.as("POST", base+"/imports/check", `{"host":"imap.example.org","port":25,"username":"u","password":"p"}`), 400, "port")
	wantStatus(t, e.as("POST", base+"/imports/check", `{"port":"x"}`), 400, "JSON")
	wantStatus(t, e.as("POST", "/api/mail/domains/example.com/mailboxes/nobody/imports/check", src), 404, "")

	// Starting it.
	rec = e.as("POST", base+"/imports", strings.TrimSuffix(src, "}")+`,"choices":{"include_junk":true}}`)
	wantStatus(t, rec, 201, "")
	if strings.Contains(rec.Body.String(), otherPassword) || strings.Contains(rec.Body.String(), "storage_id") {
		t.Fatalf("answer %s", rec.Body)
	}
	j := decode[panel.MailImport](t, rec)
	if j.ID != "1" || j.Address != "info@example.com" || j.Host != "imap.example.org" || !mail.jobs[0].Choices.IncludeJunk {
		t.Fatalf("job %s", rec.Body)
	}

	// A file in two parts.
	rec = e.as("POST", base+"/uploads", `{"name":"old.mbox","size":6}`)
	wantStatus(t, rec, 201, "")
	if j := decode[panel.MailImport](t, rec); j.ID != "2" || j.State != config.ImportUploading || j.FileName != "old.mbox" {
		t.Fatalf("upload %s", rec.Body)
	}
	rec = e.as("PUT", base+"/uploads/2?offset=0", "abc")
	wantStatus(t, rec, 200, "")
	if p := decode[config.UploadProgress](t, rec); p.Received != 3 {
		t.Fatalf("progress %s", rec.Body)
	}
	rec = e.as("PUT", base+"/uploads/2?offset=3", "def")
	if p := decode[config.UploadProgress](t, rec); rec.Code != 200 || p.Received != 6 {
		t.Fatalf("progress %s", rec.Body)
	}
	if strings.Join(mail.parts, "") != "abcdef" || mail.offsets[1] != 3 {
		t.Fatalf("parts %q %v", mail.parts, mail.offsets)
	}
	wantStatus(t, e.as("PUT", base+"/uploads/2", "abc"), 400, "offset")
	wantStatus(t, e.as("PUT", base+"/uploads/X?offset=0", "abc"), 404, "")
	big := httptest.NewRequest("PUT", base+"/uploads/2?offset=0", strings.NewReader("x"))
	big.ContentLength = config.MaxUploadPart + 1
	big.AddCookie(&http.Cookie{Name: SessionCookie, Value: e.cookie})
	big.Header.Set(CSRFHeader, "1")
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, big)
	wantStatus(t, rec, 413, "256 MB")

	rec = e.as("POST", base+"/imports/2/start", `{"choices":{"folders":["old"]}}`)
	wantStatus(t, rec, 200, "")
	if r := mail.resumes[len(mail.resumes)-1]; len(r.Choices.Folders) != 1 || r.Choices.Folders[0] != "old" {
		t.Fatalf("resume %+v", r)
	}
	wantStatus(t, e.as("POST", base+"/imports/2/start", ""), 200, "")
	// Continuing an import from another account needs its password.
	wantStatus(t, e.as("POST", base+"/imports/1/start", ""), 400, "password")
	wantStatus(t, e.as("POST", base+"/imports/1/start", `{"password":"`+otherPassword+`"}`), 200, "")

	rec = e.as("GET", base+"/imports", "")
	if list := decode[[]panel.MailImport](t, rec); rec.Code != 200 || len(list) != 2 {
		t.Fatalf("list %s", rec.Body)
	}
	rec = e.as("GET", base+"/imports/2", "")
	if j := decode[panel.MailImport](t, rec); rec.Code != 200 || len(j.Failures) != 1 {
		t.Fatalf("job %s", rec.Body)
	}
	wantStatus(t, e.as("GET", base+"/imports/9", ""), 404, "")
	wantStatus(t, e.as("GET", base+"/imports/..%2F2", ""), 404, "")
	wantStatus(t, e.as("DELETE", base+"/imports/A2", ""), 404, "")
	wantStatus(t, e.as("DELETE", base+"/imports/2", ""), 204, "")
	if len(mail.canceled) != 1 || mail.canceled[0] != "2" {
		t.Fatalf("canceled %v", mail.canceled)
	}

	// The export downloads as a file.
	rec = e.as("GET", base+"/export", "")
	wantStatus(t, rec, 200, "")
	date := time.Now().UTC().Format("2006-01-02")
	if ct, cd := rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"); ct != "application/zip" ||
		cd != `attachment; filename="mail-info@example.com-`+date+`.zip"` || rec.Body.String() != "PK\x03\x04 the zip" {
		t.Fatalf("export %q %q %q", ct, cd, rec.Body)
	}

	// Read tokens see imports but neither import nor export.
	_, read := e.createToken("read", "null")
	wantStatus(t, e.do(req{method: "GET", path: base + "/imports", token: read}), 200, "")
	wantStatus(t, e.do(req{method: "GET", path: base + "/imports/1", token: read}), 200, "")
	wantStatus(t, e.do(req{method: "GET", path: base + "/export", token: read}), 403, "only read")
	wantStatus(t, e.do(req{method: "POST", path: base + "/imports/check", body: src, token: read}), 403, "only read")
	wantStatus(t, e.do(req{method: "POST", path: base + "/uploads", body: `{"name":"a.mbox","size":1}`, token: read}), 403, "only read")
	wantStatus(t, e.do(req{method: "DELETE", path: base + "/imports/1", token: read}), 403, "only read")

	// Activity names the source, never the password.
	rec = e.as("GET", "/api/activity", "")
	wantStatus(t, rec, 200, "")
	for _, want := range []string{"mail.mailbox.import-started", "mail.mailbox.exported", "mail.mailbox.import-cancelled", "me@example.org at imap.example.org"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("activity misses %q: %s", want, rec.Body)
		}
	}
	if strings.Contains(rec.Body.String(), otherPassword) || strings.Contains(logs.String(), otherPassword) {
		t.Fatal("the other account's password was recorded or logged")
	}
}
