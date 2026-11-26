package mcp

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mailconfig "github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/panzerschrank"
)

type mxHere struct{}

func (mxHere) LookupMX(context.Context, string) ([]*net.MX, error) {
	return []*net.MX{{Host: "mail.example.net.", Pref: 10}}, nil
}

func TestMailTools(t *testing.T) {
	e := newEnvWith(t, "", func(c *panel.Config) {
		box, err := panzerschrank.OpenKeyFile(filepath.Join(t.TempDir(), panzerschrank.KeyFile))
		if err != nil {
			t.Fatal(err)
		}
		c.Secrets, c.MXResolver, c.MailHost = box, mxHere{}, "mail.example.net"
	})
	ctx := context.Background()
	cs := e.connect(t, e.write)

	var st mailStatusOutput
	ok(t, call_(t, cs, "get_mail_status", nil), &st)
	if st.MailHost != "mail.example.net" || st.ServiceSetUp || st.IMAPPort != 993 || st.Listening == nil {
		t.Fatalf("status %+v", st)
	}

	var d mailDomainOutput
	ok(t, call_(t, cs, "enable_mail_domain", map[string]any{"domain": "Example.com"}), &d)
	if d.MailDomain.Domain != "example.com" || !d.MailDomain.Active || d.MailDomain.DKIMRecord == "" || d.MailDomain.CreatedAt == "" {
		t.Fatalf("enabled %+v", d)
	}
	ok(t, call_(t, cs, "check_mail_domain", map[string]any{"domain": "example.com"}), &d)
	var list mailDomainsOutput
	ok(t, call_(t, cs, "list_mail_domains", nil), &list)
	if len(list.MailDomains) != 1 {
		t.Fatalf("list %+v", list)
	}

	// Mailboxes are created outside MCP (passwords never pass through it).
	box, err := e.svc.CreateMailbox(ctx, e.admin, "example.com", panel.MailboxInput{LocalPart: "info", Password: "mailbox-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	var boxes mailboxesOutput
	ok(t, call_(t, cs, "list_mailboxes", map[string]any{"domain": "example.com"}), &boxes)
	if len(boxes.Mailboxes) != 1 || boxes.Mailboxes[0].Address != "info@example.com" || boxes.Mailboxes[0].UsedBytes != nil {
		t.Fatalf("mailboxes %+v", boxes)
	}
	var mb mailboxOutput
	ok(t, call_(t, cs, "set_mailbox_limits", map[string]any{"id": box.ID, "send_per_hour": 10}), &mb)
	if mb.Mailbox.SendPerHour != 10 || mb.Mailbox.SendPerDay != 500 {
		t.Fatalf("limits %+v", mb)
	}
	refused(t, call_(t, cs, "unblock_mailbox", map[string]any{"id": box.ID}), "not set up")

	var al aliasOutput
	ok(t, call_(t, cs, "set_mail_alias", map[string]any{"domain": "example.com", "local_part": "sales", "targets": []string{"info@example.com"}}), &al)
	ok(t, call_(t, cs, "set_mail_alias", map[string]any{"id": al.Alias.ID, "local_part": "*", "targets": []string{"info@example.com"}}), &al)
	if !al.Alias.CatchAll || al.Alias.Address != "*@example.com" {
		t.Fatalf("alias %+v", al)
	}
	refused(t, call_(t, cs, "set_mail_alias", map[string]any{"domain": "example.com", "local_part": "info", "targets": []string{"a@example.org"}}), "mailbox")
	var aliases aliasesOutput
	ok(t, call_(t, cs, "list_mail_aliases", nil), &aliases)
	if len(aliases.Aliases) != 1 {
		t.Fatalf("aliases %+v", aliases)
	}
	refused(t, call_(t, cs, "delete_mail_alias", map[string]any{"id": al.Alias.ID, "confirm_address": "sales@example.com"}), "does not match")
	var del deletedOutput
	ok(t, call_(t, cs, "delete_mail_alias", map[string]any{"id": al.Alias.ID, "confirm_address": "*@example.com"}), &del)

	// Read-only tokens read only.
	ro := e.connect(t, e.read)
	ok(t, call_(t, ro, "list_mailboxes", nil), &boxes)
	refused(t, call_(t, ro, "enable_mail_domain", map[string]any{"domain": "example.org"}), "can only read")

	refused(t, call_(t, cs, "delete_mailbox", map[string]any{"id": box.ID, "confirm_address": "x@example.com"}), "does not match")
	ok(t, call_(t, cs, "delete_mailbox", map[string]any{"id": box.ID, "confirm_address": "Info@example.com"}), &del)
	if del.Deleted != "info@example.com" {
		t.Fatalf("deleted %+v", del)
	}
	refused(t, call_(t, cs, "delete_mail_domain", map[string]any{"domain": "example.com", "confirm_domain": "x"}), "repeat the domain")
	ok(t, call_(t, cs, "delete_mail_domain", map[string]any{"domain": "example.com", "confirm_domain": "example.com"}), &del)
	ok(t, call_(t, cs, "list_mail_domains", nil), &list)
	if len(list.MailDomains) != 0 || !strings.Contains(del.Deleted, "example.com") {
		t.Fatalf("after delete %+v", list)
	}
}

func TestMailRecordAndHealthTools(t *testing.T) {
	e := newEnvWith(t, "", func(c *panel.Config) {
		box, err := panzerschrank.OpenKeyFile(filepath.Join(t.TempDir(), panzerschrank.KeyFile))
		if err != nil {
			t.Fatal(err)
		}
		c.Secrets, c.MXResolver, c.MailHost = box, mxHere{}, "mail.example.net"
	})
	cs := e.connect(t, e.write)
	var d mailDomainOutput
	ok(t, call_(t, cs, "enable_mail_domain", map[string]any{"domain": "example.com"}), &d)

	var r mailRecordsOutput
	ok(t, call_(t, cs, "get_mail_records", map[string]any{"domain": "example.com"}), &r)
	if r.Domain != "example.com" || len(r.Records) != 8 || r.CanApply || r.ApplyNote == "" || r.Records[0].Kind != "mx" || r.Records[0].Status != "ok" {
		t.Fatalf("records %+v", r)
	}
	refused(t, call_(t, cs, "set_up_mail_records", map[string]any{"domain": "example.com"}), "DNS connections")

	var h mailHealthOutput
	ok(t, call_(t, cs, "get_mail_health", nil), &h)
	if h.MailHost != "mail.example.net" || len(h.Checks) != 10 || h.Checks[0].ID != "service" || h.Checks[0].State != "error" {
		t.Fatalf("health %+v", h)
	}

	ro := e.connect(t, e.read)
	ok(t, call_(t, ro, "get_mail_records", map[string]any{"domain": "example.com"}), &r)
	refused(t, call_(t, ro, "set_up_mail_records", map[string]any{"domain": "example.com", "confirm": true}), "can only read")
}

// importMail is a mail service with one import job per mailbox.
type importMail struct {
	mu       sync.Mutex
	hash     string
	canceled []string
}

func (f *importMail) Load(_ context.Context, cfg *mailconfig.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hash = cfg.Hash()
	return nil
}
func (f *importMail) Status(context.Context) (mailconfig.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return mailconfig.Status{ConfigHash: f.hash}, nil
}
func (f *importMail) Events(context.Context, int64) ([]mailconfig.Event, error) { return nil, nil }
func (f *importMail) Send(context.Context, mailconfig.OutgoingMail) error       { return nil }
func (f *importMail) Unblock(context.Context, string) error                     { return nil }
func (f *importMail) DeleteMailbox(context.Context, string) error               { return nil }
func (f *importMail) CheckOutbound(context.Context) (mailconfig.OutboundCheck, error) {
	return mailconfig.OutboundCheck{OK: true}, nil
}
func (f *importMail) CheckImport(context.Context, string, mailconfig.ImportSource) (mailconfig.ImportCheck, error) {
	return mailconfig.ImportCheck{}, nil
}
func (f *importMail) StartImport(context.Context, string, mailconfig.ImportStart) (mailconfig.ImportJob, error) {
	return mailconfig.ImportJob{}, nil
}
func (f *importMail) CreateUpload(context.Context, string, mailconfig.UploadRequest) (mailconfig.ImportJob, error) {
	return mailconfig.ImportJob{}, nil
}
func (f *importMail) UploadPart(context.Context, string, string, int64, io.Reader, int64) (int64, error) {
	return 0, nil
}
func (f *importMail) ResumeImport(context.Context, string, string, mailconfig.ImportResume) (mailconfig.ImportJob, error) {
	return mailconfig.ImportJob{}, nil
}
func (f *importMail) job(sid string) mailconfig.ImportJob {
	return mailconfig.ImportJob{ID: "7", StorageID: sid, Kind: mailconfig.ImportIMAP, State: mailconfig.ImportRunning,
		Host: "imap.example.org", Username: "me", Copied: 3, Created: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)}
}
func (f *importMail) ImportJobs(_ context.Context, sid string) ([]mailconfig.ImportJob, error) {
	return []mailconfig.ImportJob{f.job(sid)}, nil
}
func (f *importMail) ImportJob(_ context.Context, sid, id string) (mailconfig.ImportJob, error) {
	if id != "7" {
		return mailconfig.ImportJob{}, &mailconfig.Error{Status: 404, Msg: "unknown import"}
	}
	return f.job(sid), nil
}
func (f *importMail) CancelImport(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, id)
	return nil
}
func (f *importMail) Export(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func TestMailImportTools(t *testing.T) {
	mail := &importMail{}
	e := newEnvWith(t, "", func(c *panel.Config) {
		box, err := panzerschrank.OpenKeyFile(filepath.Join(t.TempDir(), panzerschrank.KeyFile))
		if err != nil {
			t.Fatal(err)
		}
		c.Secrets, c.MXResolver, c.MailHost, c.Mail = box, mxHere{}, "mail.example.net", mail
	})
	ctx := context.Background()
	if _, err := e.svc.EnableMailDomain(ctx, e.admin, "example.com"); err != nil {
		t.Fatal(err)
	}
	box, err := e.svc.CreateMailbox(ctx, e.admin, "example.com", panel.MailboxInput{LocalPart: "info", Password: "mailbox-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	ro := e.connect(t, e.read)
	var list mailImportsOutput
	ok(t, call_(t, ro, "list_mail_imports", map[string]any{"id": box.ID}), &list)
	if len(list.Imports) != 1 || list.Imports[0].Source != "me at imap.example.org" || list.Imports[0].Copied != 3 ||
		list.Imports[0].Address != "info@example.com" || list.Imports[0].Created != "2026-11-25T10:00:00Z" {
		t.Fatalf("imports %+v", list)
	}
	refused(t, call_(t, ro, "cancel_mail_import", map[string]any{"id": box.ID, "import_id": "7"}), "can only read")
	cs := e.connect(t, e.write)
	var out cancelledOutput
	ok(t, call_(t, cs, "cancel_mail_import", map[string]any{"id": box.ID, "import_id": "7"}), &out)
	refused(t, call_(t, cs, "cancel_mail_import", map[string]any{"id": box.ID, "import_id": "8"}), "unknown import")
	refused(t, call_(t, cs, "cancel_mail_import", map[string]any{"id": box.ID, "import_id": "../7"}), "not found")
	if out.Cancelled != "7" || len(mail.canceled) != 1 {
		t.Fatalf("cancel %+v %v", out, mail.canceled)
	}
}
