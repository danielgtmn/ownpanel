package transfer

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// BeginExport reserves an export of a mailbox (one per mailbox, at most
// config.MaxExports at once); call the returned function when done.
func (m *Manager) BeginExport(sid string) (func(), error) {
	if _, err := m.mailbox(sid); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.exporting[sid] {
		return nil, errorf(http.StatusConflict, "an export of this mailbox is already running")
	}
	if len(m.exporting) >= config.MaxExports {
		return nil, errorf(http.StatusTooManyRequests, "too many exports are running; try again later")
	}
	m.exporting[sid] = true
	return func() {
		m.mu.Lock()
		delete(m.exporting, sid)
		m.mu.Unlock()
	}, nil
}

// Export writes all mail of a mailbox to w as a zip file: one mboxrd file
// per folder (CRLF line endings) and manifest.json with the real folder
// names, special use, subscriptions and counts. Each folder is a
// snapshot taken when the export reaches it; messages expunged meanwhile
// are left out, as are messages flagged \Deleted.
func Export(ctx context.Context, st *storage.Store, sid string, w io.Writer, now time.Time) error {
	folders, err := st.ListFolders(ctx, sid)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	man := manifest{Format: exportFormat, Version: 1, Folders: []manifestFolder{}}
	paths := newPathSet()
	for _, f := range folders {
		if err := ctx.Err(); err != nil {
			return err
		}
		mf := manifestFolder{Name: f.Name, Path: paths.add(f.Name), SpecialUse: f.SpecialUse, Subscribed: f.Subscribed}
		_, msgs, err := st.Select(ctx, sid, f.Name)
		if errors.Is(err, storage.ErrNoFolder) {
			continue // deleted meanwhile
		}
		if err != nil {
			return err
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: mf.Path, Method: zip.Deflate, Modified: now})
		if err != nil {
			return err
		}
		bw := bufio.NewWriterSize(fw, 64<<10)
		for _, msg := range msgs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if msg.HasFlag(storage.FlagDeleted) {
				continue
			}
			ok, err := writeMboxMessage(bw, st, msg)
			if err != nil {
				return err
			}
			if ok {
				mf.Messages++
			}
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		man.Folders = append(man.Folders, mf)
	}
	if err := exportDAV(ctx, st, sid, zw, now); err != nil {
		return err
	}
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Deflate, Modified: now})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(man); err != nil {
		return err
	}
	return zw.Close()
}

// exportDAV adds the mailbox's calendars (calendars/<name>.ics) and
// address books (contacts/<name>.vcf). The import of our zip skips them.
func exportDAV(ctx context.Context, st *storage.Store, sid string, zw *zip.Writer, now time.Time) error {
	colls, err := st.DAVCollections(ctx, sid, "")
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, c := range colls {
		dir, ext := davCalendarsDir, ".ics"
		if c.Kind == davobj.AddressBook {
			dir, ext = davContactsDir, ".vcf"
		}
		base := davFileName(c.Name)
		p := dir + base + ext
		for n := 2; used[strings.ToLower(p)]; n++ {
			p = fmt.Sprintf("%s%s~%d%s", dir, base, n, ext)
		}
		used[strings.ToLower(p)] = true
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: p, Method: zip.Deflate, Modified: now})
		if err != nil {
			return err
		}
		if err := st.ExportDAVCollection(ctx, c, fw); err != nil {
			return err
		}
	}
	return nil
}

// Folders of calendars and address books in the export.
const (
	davCalendarsDir = "calendars/"
	davContactsDir  = "contacts/"
)

// davFileName keeps the characters of a collection name that are safe in
// a file name everywhere.
func davFileName(name string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '@', r == '+':
			return r
		}
		return '_'
	}, name)
	if strings.Trim(out, ".") == "" {
		out = "_" + out
	}
	return out
}

// writeMboxMessage writes one message in mboxrd form: the "From " line
// with the received date, Status/X-Status/X-Keywords for the flags, the
// message with ">" before lines matching ^>*From , CRLF endings and an
// empty line after it. false if the message is gone.
func writeMboxMessage(w *bufio.Writer, st *storage.Store, msg storage.Message) (bool, error) {
	f, err := st.Open(msg)
	if errors.Is(err, storage.ErrNoMessage) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	w.WriteString(fromLine(msg.InternalDate))
	status := "O"
	if msg.HasFlag(storage.FlagSeen) {
		status = "RO"
	}
	fmt.Fprintf(w, "Status: %s\r\n", status)
	var xs strings.Builder
	for _, p := range []struct {
		flag string
		c    byte
	}{{storage.FlagAnswered, 'A'}, {storage.FlagFlagged, 'F'}, {storage.FlagDraft, 'T'}} {
		if msg.HasFlag(p.flag) {
			xs.WriteByte(p.c)
		}
	}
	if xs.Len() > 0 {
		fmt.Fprintf(w, "X-Status: %s\r\n", xs.String())
	}
	var kws []string
	for _, fl := range msg.Flags {
		if !strings.HasPrefix(fl, `\`) {
			kws = append(kws, fl)
		}
	}
	if len(kws) > 0 {
		fmt.Fprintf(w, "X-Keywords: %s\r\n", strings.Join(kws, " "))
	}
	br := bufio.NewReaderSize(f, 64<<10)
	atStart := true
	endsWithNewline := true
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			if atStart && bytes.HasPrefix(bytes.TrimLeft(chunk, ">"), []byte("From ")) {
				w.WriteByte('>')
			}
			complete := chunk[len(chunk)-1] == '\n'
			if complete {
				body := bytes.TrimRight(chunk, "\r\n")
				w.Write(body)
				w.WriteString("\r\n")
			} else {
				w.Write(chunk)
			}
			atStart = complete
			endsWithNewline = complete
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false, err
		}
	}
	if !endsWithNewline {
		w.WriteString("\r\n")
	}
	_, err = w.WriteString("\r\n")
	return true, err
}

// pathSet gives every folder a safe, unique file path in the zip.
type pathSet struct{ used map[string]bool }

func newPathSet() *pathSet { return &pathSet{used: map[string]bool{}} }

// add escapes a folder name into a path ("a/b" → "a/b.mbox"): ".", "..",
// ":", "\" and control characters are replaced, a path that differs from
// another only in case gets a suffix.
func (ps *pathSet) add(name string) string {
	levels := strings.Split(name, storage.Separator)
	for i, l := range levels {
		l = strings.Map(func(r rune) rune {
			if r == ':' || r == '\\' || unicode.IsControl(r) {
				return '_'
			}
			return r
		}, l)
		if l == "." || l == ".." || l == "" {
			l = "_" + l
		}
		levels[i] = l
	}
	base := strings.Join(levels, "/")
	p := base + ".mbox"
	for n := 2; ps.used[strings.ToLower(p)]; n++ {
		p = fmt.Sprintf("%s~%d.mbox", base, n)
	}
	ps.used[strings.ToLower(p)] = true
	return p
}
