package transfer

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Limits of uploaded zip files (untrusted).
// maxZipEntries and maxUnpacked are variables for tests.
//
// There is no limit on the compression ratio: archive/zip refuses entries
// that unpack to more than their declared size, the import stops at the
// mailbox's quota and at MinFreeBytes, and a folder of near-identical
// messages (newsletters) legitimately compresses far better than 100:1.
// maxUnpacked only bounds the work of reading a file.
var (
	maxZipEntries        = 100000
	maxUnpacked   uint64 = 5 * config.MaxImportBytes
)

type entryKind int

const (
	kindMbox entryKind = iota
	kindEML
)

// fileEntry is one mail file of an upload.
type fileEntry struct {
	open    func() (io.ReadCloser, error)
	kind    entryKind
	rd      bool // mboxrd (our export)
	sniff   bool // no extension: an mbox only if it starts with "From "
	takeout bool // Google Takeout: labels decide the folder
	levels  []string
	// From our manifest.
	use        string
	subscribed *bool
}

func (e *fileEntry) remote() string { return strings.Join(e.levels, "/") }

// fileSource is an uploaded file opened for reading.
type fileSource struct {
	entries []*fileEntry
	// empty are folders of our manifest without messages.
	empty   []*fileEntry
	skipped int
	// ownExport: a zip with our export's manifest.
	ownExport bool
	close     func() error
}

// manifest is the manifest.json of our export.
type manifest struct {
	Format  string           `json:"format"`
	Version int              `json:"version"`
	Folders []manifestFolder `json:"folders"`
	// DAV lists the calendars and address books in the zip; the import of
	// mail skips exactly these entries.
	DAV []manifestDAV `json:"dav,omitempty"`
}

// manifestDAV is a calendar (.ics) or address book (.vcf) in the zip.
type manifestDAV struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type manifestFolder struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	SpecialUse string `json:"special_use,omitempty"`
	Subscribed bool   `json:"subscribed"`
	Messages   int    `json:"messages"`
}

// exportFormat marks our export's manifest.
const exportFormat = "ownpanel-mail-export"

func badFile(format string, args ...any) error {
	return errorf(http.StatusBadRequest, format, args...)
}

// openSource opens an upload: a zip file, an mbox file or one message.
func openSource(file, name string, declared int64) (*fileSource, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	head := make([]byte, 5)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")) || bytes.HasPrefix(head, []byte("PK\x05\x06")):
		src, err := openZip(f, fi.Size(), declared)
		if err != nil {
			f.Close()
			return nil, err
		}
		src.close = f.Close
		return src, nil
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		f.Close()
		return nil, badFile("compressed tar files cannot be imported; export Google Takeout as .zip")
	}
	open := func() (io.ReadCloser, error) { return os.Open(file) }
	src := &fileSource{close: f.Close}
	if bytes.HasPrefix(head, []byte("From ")) {
		base := trimExt(name)
		src.entries = []*fileEntry{{open: open, kind: kindMbox, levels: []string{base}, takeout: isTakeoutName(name)}}
	} else {
		src.entries = []*fileEntry{{open: open, kind: kindEML, levels: []string{storage.Inbox}}}
	}
	return src, nil
}

func trimExt(n string) string {
	if e := path.Ext(n); e != "" && e != n {
		return strings.TrimSuffix(n, e)
	}
	return n
}

func isTakeoutName(n string) bool {
	return strings.HasPrefix(strings.ToLower(path.Base(n)), "all mail including spam and trash")
}

// zipName decodes the name of an entry: UTF-8 when the flag says so (or
// when it is valid UTF-8 anyway, as macOS writes it), else CP437.
func zipName(f *zip.File) string {
	if f.Flags&0x800 != 0 || utf8.ValidString(f.Name) {
		return f.Name
	}
	s, err := charmap.CodePage437.NewDecoder().String(f.Name)
	if err != nil {
		return strings.ToValidUTF8(f.Name, "_")
	}
	return s
}

// safeName refuses traversal, absolute paths, NUL and backslashes.
func safeName(n string) bool {
	if n == "" || strings.HasPrefix(n, "/") || strings.ContainsAny(n, "\x00\\") {
		return false
	}
	for _, c := range strings.Split(n, "/") {
		if c == ".." || c == "." {
			return false
		}
	}
	return true
}

func openZip(f *os.File, size, declared int64) (*fileSource, error) {
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return nil, badFile("the zip file cannot be read")
	}
	if len(zr.File) > maxZipEntries {
		return nil, badFile("the zip file has more than %d entries", maxZipEntries)
	}
	src := &fileSource{}
	var man *manifest
	for _, zf := range zr.File {
		if zf.Name == "manifest.json" && zf.UncompressedSize64 < 16<<20 {
			if m, err := readManifest(zf); err == nil && m.Format == exportFormat {
				man = m
			}
		}
	}
	byPath := map[string]manifestFolder{}
	davPaths := map[string]bool{}
	src.ownExport = man != nil
	if man != nil {
		for _, mf := range man.Folders {
			byPath[mf.Path] = mf
		}
		for _, d := range man.DAV {
			if _, isFolder := byPath[d.Path]; !isFolder {
				davPaths[d.Path] = true
			}
		}
	}
	var total uint64
	folders := map[string]string{} // lower-case remote → first spelling
	usedPaths := map[string]bool{}
	for _, zf := range zr.File {
		name := zipName(zf)
		if strings.HasSuffix(name, "/") {
			continue
		}
		base := path.Base(name)
		if strings.HasPrefix(name, "__MACOSX/") || strings.HasPrefix(base, "._") || base == ".DS_Store" ||
			man != nil && (name == "manifest.json" || davPaths[zf.Name]) {
			continue
		}
		if !safeName(name) {
			src.skipped++
			continue
		}
		if strings.EqualFold(path.Ext(name), ".zip") {
			src.skipped++ // nested archives are not opened
			continue
		}
		var e *fileEntry
		if man != nil {
			mf, ok := byPath[zf.Name]
			if !ok {
				src.skipped++
				continue
			}
			if zf.UncompressedSize64 == 0 {
				continue // an empty folder, below
			}
			sub := mf.Subscribed
			e = &fileEntry{kind: kindMbox, rd: true, levels: strings.Split(mf.Name, storage.Separator), use: mf.SpecialUse, subscribed: &sub}
			usedPaths[zf.Name] = true
		} else {
			e = classify(name)
			if e == nil {
				src.skipped++
				continue
			}
		}
		total += zf.UncompressedSize64
		if total > maxUnpacked {
			return nil, badFile("the zip file unpacks to more than %d GB", maxUnpacked>>30)
		}
		key := strings.ToLower(e.remote())
		if first, ok := folders[key]; ok {
			e.levels = strings.Split(first, "/") // case collisions: one folder
		} else {
			if len(folders) >= maxFolders {
				src.skipped++
				continue
			}
			folders[key] = e.remote()
		}
		e.open = func() (io.ReadCloser, error) { return zf.Open() }
		src.entries = append(src.entries, e)
	}
	if man != nil {
		for _, mf := range man.Folders {
			if usedPaths[mf.Path] {
				continue
			}
			sub := mf.Subscribed
			src.empty = append(src.empty, &fileEntry{levels: strings.Split(mf.Name, storage.Separator), use: mf.SpecialUse, subscribed: &sub})
		}
	}
	return src, nil
}

func readManifest(zf *zip.File) (*manifest, error) {
	rc, err := zf.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var m manifest
	if err := json.NewDecoder(io.LimitReader(rc, 16<<20)).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// classify maps the path of a zip entry to a mail file: Takeout's
// "Takeout/Mail/" prefix is dropped, Thunderbird's ".sbd" directories and
// extensionless mbox files, Apple Mail's "Name.mbox/mbox" bundles (other
// files in them ignored), ".mbox" files and ".eml" messages. nil for
// anything else.
func classify(name string) *fileEntry {
	comps := strings.Split(name, "/")
	takeout := false
	if len(comps) >= 3 && comps[0] == "Takeout" {
		comps, takeout = comps[2:], true
	}
	last := comps[len(comps)-1]
	dirs := comps[:len(comps)-1]
	inBundle := false
	levels := make([]string, 0, len(comps))
	for _, d := range dirs {
		lower := strings.ToLower(d)
		switch {
		case strings.HasSuffix(lower, ".mbox"):
			d, inBundle = d[:len(d)-len(".mbox")], true
		case strings.HasSuffix(lower, ".sbd"):
			d = d[:len(d)-len(".sbd")]
		}
		levels = append(levels, d)
	}
	lowerLast := strings.ToLower(last)
	ext := path.Ext(lowerLast)
	switch {
	case inBundle && len(dirs) > 0 && strings.HasSuffix(strings.ToLower(dirs[len(dirs)-1]), ".mbox"):
		if last != "mbox" {
			return nil // Info.plist, table_of_contents, …
		}
		return &fileEntry{kind: kindMbox, levels: levels, takeout: takeout}
	case ext == ".mbox" || ext == ".mbx":
		e := &fileEntry{kind: kindMbox, levels: append(levels, trimExt(last)), takeout: takeout}
		e.takeout = takeout || isTakeoutName(last)
		return e
	case ext == ".eml":
		if len(levels) == 0 {
			levels = []string{storage.Inbox}
		}
		return &fileEntry{kind: kindEML, levels: levels}
	case ext == "" && !strings.HasPrefix(last, "."):
		return &fileEntry{kind: kindMbox, sniff: true, levels: append(levels, last)}
	}
	return nil
}

// fileMessage is one message of an upload.
type fileMessage struct {
	entry *fileEntry
	body  io.Reader
	from  time.Time // date of the mbox "From " line
}

// each calls fn for every message of the upload, in a stable order.
// Entries that turn out not to be mail are counted in skipped.
func (src *fileSource) each(ctx context.Context, fn func(fileMessage) error) error {
	for _, e := range src.entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := src.eachIn(ctx, e, fn); err != nil {
			return err
		}
	}
	return nil
}

func (src *fileSource) eachIn(ctx context.Context, e *fileEntry, fn func(fileMessage) error) error {
	rc, err := e.open()
	if err != nil {
		src.skipped++
		return nil
	}
	defer rc.Close()
	br := bufio.NewReaderSize(rc, 64<<10)
	head, _ := br.Peek(5)
	if bytes.HasPrefix(head, []byte("PK\x03\x04")) {
		src.skipped++ // a zip inside, whatever its name
		return nil
	}
	if e.kind == kindEML {
		return fn(fileMessage{entry: e, body: br})
	}
	mr, err := newMboxReader(br, e.rd)
	if errors.Is(err, errNotMbox) {
		src.skipped++
		return nil
	}
	if err != nil {
		return badFile("the file cannot be read")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		msg, err := mr.nextMessage()
		if err != nil {
			return badFile("the file cannot be read")
		}
		if msg == nil {
			return nil
		}
		if err := fn(fileMessage{entry: e, body: msg, from: msg.from}); err != nil {
			return err
		}
	}
}

// takeoutFolder decides the folder of a Google Takeout message from its
// labels: Spam → Junk, Trash, Drafts, Sent, Inbox, the first user label,
// otherwise Archive. It returns the levels and the role.
func takeoutFolder(labels []string) ([]string, role) {
	has := func(names ...string) bool {
		for _, l := range labels {
			for _, n := range names {
				if strings.EqualFold(l, n) {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("Spam"):
		return []string{"Junk"}, roleJunk
	case has("Trash", "Papierkorb"):
		return []string{"Trash"}, roleTrash
	case has("Drafts", "Entwürfe"):
		return []string{"Drafts"}, roleDrafts
	case has("Sent", "Gesendet"):
		return []string{"Sent"}, roleSent
	case has("Inbox", "Posteingang"):
		return []string{storage.Inbox}, roleNone
	}
	for _, l := range labels {
		if !gmailSystemLabel(l) {
			return strings.Split(l, "/"), roleNone
		}
	}
	return []string{"Archive"}, roleArchive
}

func gmailSystemLabel(l string) bool {
	switch strings.ToLower(l) {
	case "important", "starred", "unread", "opened", "archived", "chat", "inbox", "sent", "spam", "trash", "drafts",
		"wichtig", "markiert", "ungelesen", "geöffnet", "archiviert", "posteingang", "gesendet", "papierkorb", "entwürfe":
		return true
	}
	lower := strings.ToLower(l)
	return strings.HasPrefix(lower, "category ") || strings.HasPrefix(lower, "kategorie ")
}

// takeoutFlags adds \Seen unless the message is labelled unread, and
// \Flagged when it is starred.
func takeoutFlags(labels, flags []string) []string {
	unread, starred := false, false
	for _, l := range labels {
		switch strings.ToLower(l) {
		case "unread", "ungelesen":
			unread = true
		case "starred", "markiert":
			starred = true
		}
	}
	if !unread {
		flags = append(flags, storage.FlagSeen)
	}
	if starred {
		flags = append(flags, storage.FlagFlagged)
	}
	return cleanFlags(flags)
}

// sourceOf returns the source folder of a prepared file message.
func sourceOf(fm fileMessage, p *prepared) (levels []string, attrs []string) {
	e := fm.entry
	if e.takeout && p != nil && p.labels != nil {
		lv, r := takeoutFolder(p.labels)
		return lv, roleAttrs(r)
	}
	if e.use != "" {
		return e.levels, []string{e.use}
	}
	return e.levels, nil
}

func roleAttrs(r role) []string {
	switch r {
	case roleJunk:
		return []string{storage.SpecialJunk}
	case roleTrash:
		return []string{storage.SpecialTrash}
	case roleDrafts:
		return []string{storage.SpecialDrafts}
	case roleSent:
		return []string{storage.SpecialSent}
	case roleArchive:
		return []string{storage.SpecialArchive}
	}
	return nil
}

// scan reads an upload once and records what it holds (state ready).
func (m *Manager) scan(ctx context.Context, a *active, j config.ImportJob) {
	check, err := m.scanFile(ctx, j)
	if m.reason(a) != stopNone {
		if m.reason(a) == stopShutdown {
			return // scanned again after the next start
		}
		j.State, j.Finished = config.ImportCancelled, m.now()
		os.Remove(m.spoolPath(j.ID))
		m.opts.Store.SaveImport(context.Background(), &j)
		return
	}
	if err != nil {
		j.State, j.Error, j.Finished = config.ImportFailed, errText(err), m.now()
		if !isPublicErr(err) {
			m.log.Error("mail: import scan failed", "job", j.ID, "err", err)
		}
		os.Remove(m.spoolPath(j.ID))
		if m.opts.Store.SaveImport(context.Background(), &j) == nil {
			m.emit(j, config.EventImportFailed)
		}
		return
	}
	j.State, j.Check = config.ImportReady, &check
	if err := m.opts.Store.SaveImport(context.Background(), &j); err != nil && !errors.Is(err, storage.ErrNoImport) {
		m.log.Error("mail: import scan", "job", j.ID, "err", err)
	}
}

func (m *Manager) scanFile(ctx context.Context, j config.ImportJob) (config.ImportCheck, error) {
	var c config.ImportCheck
	mb, err := m.mailbox(j.StorageID)
	if err != nil {
		return c, errMailboxGone
	}
	src, err := openSource(m.spoolPath(j.ID), j.FileName, j.FileSize)
	if err != nil {
		return c, err
	}
	defer src.close()
	d := m.opts.Directory()
	host, maxSize := d.Hostname(), d.Limits().MaxMessageBytes
	var order []string
	found := map[string]*sourceFolder{}
	add := func(levels, attrs []string) *sourceFolder {
		key := strings.Join(levels, "/")
		f, ok := found[key]
		if !ok {
			f = &sourceFolder{remote: key, levels: levels, attrs: attrs}
			found[key] = f
			order = append(order, key)
		}
		return f
	}
	// Every folder of our manifest, also empty ones.
	for _, e := range append(append([]*fileEntry{}, src.entries...), src.empty...) {
		if e.subscribed != nil {
			add(e.levels, attrsOf(e.use))
		}
	}
	err = src.each(ctx, func(fm fileMessage) error {
		p, err := prepare(fm.body, io.Discard, prepOptions{host: host, meta: true, max: maxSize})
		if err != nil && !errors.Is(err, errTooLarge) && !errors.Is(err, errHeaderTooLong) {
			return err
		}
		if p == nil {
			p = &prepared{}
		}
		levels, attrs := sourceOf(fm, p)
		f := add(levels, attrs)
		if !p.deleted {
			f.messages++
			f.bytes += p.size
		}
		return nil
	})
	if err != nil {
		return c, err
	}
	var folders []sourceFolder
	for _, k := range order {
		folders = append(folders, *found[k])
	}
	local, err := m.localFolders(ctx, j.StorageID)
	if err != nil {
		return c, err
	}
	c = makePlan(folders, local).check()
	c.SkippedEntries = src.skipped
	c.OwnExport = src.ownExport
	return c, m.quotaNumbers(ctx, mb, &c)
}

func attrsOf(use string) []string {
	if use == "" {
		return nil
	}
	return []string{use}
}

// runFile imports the chosen folders of an upload. After a restart it
// skips the messages it went through before (Position); keys keep it from
// copying a message twice in any case.
func (r *runner) runFile() error {
	src, err := openSource(r.m.spoolPath(r.j.ID), r.j.FileName, r.j.FileSize)
	if err != nil {
		return err
	}
	defer src.close()
	if r.j.Check == nil {
		return errors.New("the file was not checked")
	}
	// The plan is made from the overview of the scan, so the choices
	// refer to the same folders.
	var folders []sourceFolder
	for _, f := range r.j.Check.Folders {
		sf := sourceFolder{remote: f.Remote, levels: strings.Split(f.Remote, "/"), messages: f.Messages, bytes: f.Bytes}
		if f.SpecialUse != "" {
			sf.attrs = []string{f.SpecialUse}
		}
		if f.Note == config.ImportNoteJunk {
			sf.attrs = []string{storage.SpecialJunk}
		}
		if f.Note == config.ImportNoteTrash {
			sf.attrs = []string{storage.SpecialTrash}
		}
		folders = append(folders, sf)
	}
	chosen, err := makePlan(folders, r.local).choose(r.j.Choices)
	if err != nil {
		return err
	}
	byRemote := map[string]planned{}
	r.j.Messages = 0
	for _, f := range chosen {
		byRemote[f.Remote] = f
		r.j.Messages += f.Messages
	}
	// Folders of our manifest, also empty ones, with their subscription.
	for _, e := range append(append([]*fileEntry{}, src.empty...), src.entries...) {
		if f, ok := byRemote[e.remote()]; ok && e.subscribed != nil {
			if err := r.ensureFolder(f.Local, f.SpecialUse, e.subscribed); err != nil {
				return err
			}
			r.beginFolder(f.Remote, f.Local)
		}
	}
	idx := int64(-1)
	start := r.j.Position
	err = src.each(r.ctx, func(fm fileMessage) error {
		idx++
		if idx < start {
			return nil
		}
		if err := r.beforeWrite(0); err != nil {
			return err
		}
		p, perr := r.prepare(incoming{body: fm.body, meta: true})
		levels, _ := sourceOf(fm, p)
		f, ok := byRemote[strings.Join(levels, "/")]
		r.j.Position = idx + 1
		if !ok {
			return r.save(false)
		}
		r.beginFolder(f.Remote, f.Local)
		if err := r.ensureFolder(f.Local, f.SpecialUse, nil); err != nil {
			return err
		}
		if perr != nil {
			switch {
			case errors.Is(perr, errTooLarge):
				return r.failRaw(f.Remote, "", fm.from, "larger than the message size limit")
			case errors.Is(perr, errHeaderTooLong):
				return r.failRaw(f.Remote, "", fm.from, "header too large")
			}
			return perr
		}
		flags := p.flags
		if fm.entry.takeout && p.labels != nil {
			flags = takeoutFlags(p.labels, flags)
		}
		_, _, err := r.store(f.Local, false, incoming{flags: flags, date: fm.from}, p, f.Remote)
		return err
	})
	if err != nil {
		return err
	}
	for i := range r.j.Folders {
		r.j.Folders[i].Done = true
	}
	return r.save(true)
}
