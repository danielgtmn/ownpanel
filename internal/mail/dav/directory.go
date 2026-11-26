package dav

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
)

// The directory (global address list): a read-only address book in every
// mailbox's address book home, listing the mailboxes and aliases of the
// mailbox's organisation with their display names, for autocompletion.
// It is generated from the configuration; its ETags are hashes of the
// cards, so they only change when a card does.

// Names shown in programs.
const (
	directoryDisplayName = "Directory"
	directoryDescription = "Everyone in your organisation"
)

// dirCard is one entry of the directory.
type dirCard struct {
	name     string // resource name
	etag     string
	data     []byte
	modified time.Time
}

// dirBook is the directory of one organisation.
type dirBook struct {
	cards  []*dirCard
	byName map[string]*dirCard
	ctag   string
}

func (b *dirBook) token() string { return "urn:ownpanel:directory:" + b.ctag }

// directoryBook returns the directory of user's organisation, built once
// per configuration.
func (h *Handler) directoryBook(dir *directory.Directory, user *config.Mailbox) *dirBook {
	org, _ := dir.OrganisationOf(user.StorageID)
	h.dirMu.Lock()
	defer h.dirMu.Unlock()
	if h.dirFor != dir {
		h.dirFor, h.dirCache = dir, map[string]*dirBook{}
	}
	if b := h.dirCache[org]; b != nil {
		return b
	}
	b := buildDirectory(dir, org)
	h.dirCache[org] = b
	return b
}

// hidden tells whether an entry asks to be left out of the directory
// (a HideFromDirectory field, which aliases get with G3; looked up by
// name so this works with and without it).
func hidden(v any) bool {
	rv := reflect.Indirect(reflect.ValueOf(v))
	if rv.Kind() != reflect.Struct {
		return false
	}
	f := rv.FieldByName("HideFromDirectory")
	return f.IsValid() && f.Kind() == reflect.Bool && f.Bool()
}

func buildDirectory(dir *directory.Directory, org string) *dirBook {
	cfg := dir.Config()
	inOrg := func(addr string) bool {
		_, dom, ok := config.SplitAddress(addr)
		if !ok {
			return false
		}
		o, ok := dir.Organisation(dom)
		return ok && o == org
	}
	b := &dirBook{byName: map[string]*dirCard{}}
	add := func(addr, name string) {
		key := sha256.Sum256([]byte("ownpanel directory\x00" + addr))
		id := hex.EncodeToString(key[:16])
		c := &dirCard{name: id + ".vcf", data: directoryCard("ownpanel-directory-"+id, addr, name)}
		sum := sha256.Sum256(c.data)
		c.etag = hex.EncodeToString(sum[:12])
		b.cards = append(b.cards, c)
		b.byName[c.name] = c
	}
	for i := range cfg.Mailboxes {
		m := &cfg.Mailboxes[i]
		if inOrg(m.Address) && !hidden(m) {
			add(m.Address, m.DisplayName)
		}
	}
	for i := range cfg.Aliases {
		a := &cfg.Aliases[i]
		if strings.HasPrefix(a.Address, "*@") || !inOrg(a.Address) || hidden(a) {
			continue
		}
		if _, isMailbox := dir.Mailbox(a.Address); isMailbox {
			continue
		}
		add(a.Address, "")
	}
	sort.Slice(b.cards, func(i, j int) bool { return b.cards[i].name < b.cards[j].name })
	h := sha256.New()
	for _, c := range b.cards {
		h.Write([]byte(c.name + "\x00" + c.etag + "\x00"))
	}
	b.ctag = hex.EncodeToString(h.Sum(nil)[:12])
	// Cards change only with the configuration; their time is not known,
	// so a fixed one keeps Last-Modified stable.
	for _, c := range b.cards {
		c.modified = time.Unix(0, 0)
	}
	return b
}

// directoryCard is a vCard 3.0 (what every program reads) for an address.
func directoryCard(uid, addr, name string) []byte {
	fn := name
	if fn == "" {
		fn = addr
	}
	family, given := fn, ""
	if name != "" {
		if i := strings.LastIndexByte(name, ' '); i > 0 {
			given, family = name[:i], name[i+1:]
		}
	}
	var s strings.Builder
	s.WriteString("BEGIN:VCARD\r\nVERSION:3.0\r\n")
	s.WriteString("UID:" + uid + "\r\n")
	s.WriteString("FN:" + vcardText(fn) + "\r\n")
	s.WriteString("N:" + vcardText(family) + ";" + vcardText(given) + ";;;\r\n")
	s.WriteString("EMAIL;TYPE=INTERNET:" + addr + "\r\n")
	s.WriteString("END:VCARD\r\n")
	return []byte(s.String())
}

func vcardText(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ",", `\,`, ";", `\;`, "\n", `\n`, "\r", "")
	return r.Replace(s)
}

func xmlName(ns, local string) xml.Name { return xml.Name{Space: ns, Local: local} }
