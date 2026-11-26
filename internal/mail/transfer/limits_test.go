package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// repeatReader yields n bytes of c (n < 0: forever).
type repeatReader struct {
	c byte
	n int64
}

func (r *repeatReader) Read(b []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	if r.n > 0 && int64(len(b)) > r.n {
		b = b[:r.n]
	}
	for i := range b {
		b[i] = r.c
	}
	if r.n > 0 {
		r.n -= int64(len(b))
	}
	return len(b), nil
}

// countReader counts the bytes read through it.
type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

// A message with one line of several MiB is read in pieces, not held
// whole, and the messages around it still split correctly.
func TestMboxLongLineInPieces(t *testing.T) {
	const long = 4 << 20
	r := io.MultiReader(
		strings.NewReader("From a@x Mon Jan  1 00:00:00 2024\nSubject: one\n\n"),
		&repeatReader{c: 'x', n: long},
		strings.NewReader("\n\nFrom b@x Mon Jan  1 00:00:01 2024\nSubject: two\n\nz\n"),
	)
	mr, err := newMboxReader(context.Background(), r, false)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mr.nextMessage()
	if err != nil || msg == nil {
		t.Fatal(msg, err)
	}
	buf := make([]byte, 2*long)
	total := 0
	for {
		n, err := msg.Read(buf)
		if n > 2*mboxPiece {
			t.Fatalf("one read returned %d bytes: the line was held whole", n)
		}
		total += n
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if want := len("Subject: one\n\n") + long + 1; total != want {
		t.Fatalf("first message %d bytes, want %d", total, want)
	}
	msg, err = mr.nextMessage()
	if err != nil || msg == nil || !msg.from.Equal(time.Date(2024, 1, 1, 0, 0, 1, 0, time.UTC)) {
		t.Fatalf("second message %+v %v", msg, err)
	}
	b, _ := io.ReadAll(msg)
	if string(b) != "Subject: two\n\nz\n" {
		t.Fatalf("second message %q", b)
	}
}

// A "From " first line too long for a date is not kept whole either.
func TestMboxLongFirstLine(t *testing.T) {
	r := io.MultiReader(strings.NewReader("From "), &repeatReader{c: 'a', n: 1 << 20},
		strings.NewReader("\nSubject: x\n\nbody\n"))
	mr, err := newMboxReader(context.Background(), r, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(mr.next) > mboxPiece {
		t.Fatalf("first line held with %d bytes", len(mr.next))
	}
	msg, _ := mr.nextMessage()
	b, _ := io.ReadAll(msg)
	if string(b) != "Subject: x\n\nbody\n" {
		t.Fatalf("%q", b)
	}
}

// The header limit holds while a line is read, not after it was buffered.
func TestPrepareLongHeaderLineBounded(t *testing.T) {
	cr := &countReader{r: io.MultiReader(strings.NewReader("X: "), &repeatReader{c: 'a', n: 8 << 20})}
	_, err := prepare(cr, io.Discard, prepOptions{})
	if !errors.Is(err, errHeaderTooLong) {
		t.Fatal(err)
	}
	if cr.n > maxHeaderBytes+256<<10 {
		t.Fatalf("read %d bytes before giving up", cr.n)
	}
	// A long body line is fine (bounded by the message size limit).
	msg := "Subject: x\r\n\r\n" + strings.Repeat("b", 3<<20) + "\r\n"
	p, err := prepare(strings.NewReader(msg), io.Discard, prepOptions{})
	if err != nil || p.size != int64(len(msg)) {
		t.Fatal(p, err)
	}
}

// fromLineDate takes linear time on long lines of many fields.
func TestFromLineDateLongLine(t *testing.T) {
	line := "From a " + strings.Repeat("b ", 16000) + "\n"
	start := time.Now()
	for range 4 {
		if d := fromLineDate(line); !d.IsZero() {
			t.Fatal(d)
		}
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("took %v", d)
	}
	// What follows the date (uucp) is still ignored.
	if d := fromLineDate("From a@x Mon Jan  1 00:00:00 2024 remote from host\n"); !d.Equal(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(d)
	}
}

// Cancelling stops reading inside a message, not only between messages.
func TestMboxReadCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// One message that never ends.
	r := io.MultiReader(strings.NewReader("From a@x Mon Jan  1 00:00:00 2024\nSubject: x\n\n"), &repeatReader{c: '\n', n: -1})
	mr, err := newMboxReader(ctx, r, false)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mr.nextMessage()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, msg)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading did not stop")
	}
}

// An upload whose one line unpacks to several MiB is scanned and imported.
func TestUploadLongLine(t *testing.T) {
	e := newEnv(t)
	data := "From a@example.org Mon Jan  1 00:00:00 2024\nSubject: long\n\n" + strings.Repeat("x", 3<<20) + "\n" +
		"From a@example.org Mon Jan  1 00:00:01 2024\nSubject: short\n\nhi\n"
	zipped := makeZip(t, zipEntry{name: "Inbox.mbox", data: data})
	j := e.upload(idAlice, "mail.zip", zipped)
	if j.State != config.ImportReady {
		t.Fatalf("%s %s", j.State, j.Error)
	}
	f, ok := checkFolder(j.Check, "Inbox")
	if !ok || f.Messages != 2 {
		t.Fatalf("%+v", j.Check)
	}
}

// Scans count against MaxRunning, and a scan the service never finished
// is tried once more after a restart, then given up.
func TestScansLimitedAndNotRepeatedForever(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.MaxRunning = 1 })
	ctx := context.Background()
	var mbox bytes.Buffer
	for i := range 2000 {
		mbox.WriteString(sprintf("From x@y Fri Jan  5 11:00:00 2024\nSubject: n%d\n\n%s\n", i, strings.Repeat("y", 500)))
	}
	first := e.upload(idAlice, "box.mbox", mbox.Bytes())
	if first.State != config.ImportReady {
		t.Fatalf("%s %s", first.State, first.Error)
	}
	sctx, cancel := context.WithTimeout(ctx, time.Second)
	e.m.Shutdown(sctx)
	cancel()
	// As if the service had been killed while scanning three uploads: the
	// first was started once, the second maxScans times, the third never.
	var ids []int64
	for i, n := range []int{1, maxScans, 0} {
		j := first
		if i > 0 {
			j.ID = 0
			must(t, e.store.CreateImport(ctx, &j))
			must(t, os.Link(e.m.spoolPath(first.ID), e.m.spoolPath(j.ID)))
		}
		j.State, j.Check, j.Scans = config.ImportChecking, nil, n
		must(t, e.store.SaveImport(ctx, &j))
		ids = append(ids, j.ID)
	}
	e.m = e.manager(func(o *Options) { o.MaxRunning = 1 })
	e.m.mu.Lock()
	scans := 0
	for _, a := range e.m.active {
		if a.scanning {
			scans++
		}
	}
	e.m.mu.Unlock()
	if scans > 1 {
		t.Fatalf("%d scans at once with MaxRunning 1", scans)
	}
	if j := e.wait(idAlice, ids[0], config.ImportReady, config.ImportFailed); j.State != config.ImportReady {
		t.Fatalf("scanned once before: %s %s", j.State, j.Error)
	}
	if j := e.wait(idAlice, ids[1], config.ImportReady, config.ImportFailed); j.State != config.ImportFailed || !strings.Contains(j.Error, "could not be checked") {
		t.Fatalf("scanned %d times before: %s %s", maxScans, j.State, j.Error)
	}
	if j := e.wait(idAlice, ids[2], config.ImportReady, config.ImportFailed); j.State != config.ImportReady {
		t.Fatalf("never scanned: %s %s", j.State, j.Error)
	}
}
