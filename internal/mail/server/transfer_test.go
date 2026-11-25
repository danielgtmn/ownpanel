package server

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

func waitJob(t *testing.T, c *config.Client, sid string, id int64, states ...string) config.ImportJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, err := c.ImportJob(context.Background(), sid, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range states {
			if j.State == s {
				return j
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %d is %s (%s)", id, j.State, j.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func statusOf(err error) int {
	var e *config.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func TestImportUploadAndExport(t *testing.T) {
	ctx := context.Background()
	data, sock := t.TempDir(), socketDir(t)
	in := run(t, data, sock)
	defer in.stop(t)
	cfg := testConfig(t)
	cfg.Mailboxes = append(cfg.Mailboxes, config.Mailbox{StorageID: idBob, Address: "bob@example.org", PasswordHash: cfg.Mailboxes[0].PasswordHash})
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	c := in.client

	mbox := "From x@y Thu Jan  4 10:00:00 2024\nSubject: one\nStatus: RO\n\nbody\n\n" +
		"From x@y Thu Jan  4 11:00:00 2024\nSubject: two\n\n" + strings.Repeat("x", 20000) + "\n"
	j, err := c.CreateUpload(ctx, idAlice, config.UploadRequest{Name: "Inbox.mbox", Size: int64(len(mbox))})
	if err != nil {
		t.Fatal(err)
	}
	if j.State != config.ImportUploading || j.Kind != config.ImportFile {
		t.Fatalf("created %+v", j)
	}
	if _, err := c.UploadPart(ctx, idAlice, j.ID, 7, strings.NewReader(mbox), 10); statusOf(err) != http.StatusConflict {
		t.Fatalf("wrong offset: %v", err)
	}
	got, err := c.UploadPart(ctx, idAlice, j.ID, 0, strings.NewReader(mbox[:100]), 100)
	if err != nil || got != 100 {
		t.Fatalf("first part: %d %v", got, err)
	}
	got, err = c.UploadPart(ctx, idAlice, j.ID, 100, strings.NewReader(mbox[100:]), int64(len(mbox)-100))
	if err != nil || got != int64(len(mbox)) {
		t.Fatalf("second part: %d %v", got, err)
	}
	j = waitJob(t, c, idAlice, j.ID, config.ImportReady)
	if j.Check == nil || len(j.Check.Folders) != 1 || j.Check.Folders[0].Local != "INBOX" {
		t.Fatalf("check %+v", j.Check)
	}
	if _, err := c.ResumeImport(ctx, idAlice, j.ID, config.ImportResume{}); err != nil {
		t.Fatal(err)
	}
	j = waitJob(t, c, idAlice, j.ID, config.ImportDone)
	// The second message is over the limit of the test configuration.
	if j.Copied != 1 || j.Failed != 1 || len(j.Failures) != 1 || j.Failures[0].Subject != "" && j.Failures[0].Reason == "" {
		t.Fatalf("done %+v", j)
	}
	if !strings.Contains(j.Failures[0].Reason, "size limit") {
		t.Fatalf("failure %+v", j.Failures[0])
	}
	js, err := c.ImportJobs(ctx, idAlice)
	if err != nil || len(js) != 1 || js[0].Failures != nil {
		t.Fatalf("list %+v %v", js, err)
	}
	ev, _ := c.Events(ctx, 0)
	found := false
	for _, e := range ev {
		if e.Kind == config.EventImportFinished && e.StorageID == idAlice && strings.Contains(e.Detail, "Inbox.mbox") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no event: %+v", ev)
	}

	// Export streams a zip with the message and the manifest.
	rc, err := c.Export(ctx, idAlice)
	if err != nil {
		t.Fatal(err)
	}
	zipData, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["manifest.json"] || !names["INBOX.mbox"] || !names["Sent.mbox"] {
		t.Fatalf("zip entries %v", names)
	}
	if _, err := c.Export(ctx, mailtestUnknownID); statusOf(err) != http.StatusNotFound {
		t.Fatalf("unknown mailbox: %v", err)
	}

	// Into bob, then bob is deleted while a job of his waits.
	j2, err := c.CreateUpload(ctx, idBob, config.UploadRequest{Name: "alice.zip", Size: int64(len(zipData))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UploadPart(ctx, idBob, j2.ID, 0, bytes.NewReader(zipData), int64(len(zipData))); err != nil {
		t.Fatal(err)
	}
	waitJob(t, c, idBob, j2.ID, config.ImportReady)
	cfg.Mailboxes = cfg.Mailboxes[:1]
	if err := c.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteMailbox(ctx, idBob); err != nil {
		t.Fatal(err)
	}
	if _, err := in.srv.Store().Import(ctx, idBob, j2.ID); err == nil {
		t.Fatal("job of a deleted mailbox kept")
	}

	// Cancelling a finished job removes it.
	if err := c.CancelImport(ctx, idAlice, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ImportJob(ctx, idAlice, j.ID); statusOf(err) != http.StatusNotFound {
		t.Fatalf("after removal: %v", err)
	}
}

const mailtestUnknownID = "cccccccccccccccccccccccccccccccc"
