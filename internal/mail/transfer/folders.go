package transfer

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Limits of the folders of one job.
const (
	maxFolders     = 1000
	maxFolderDepth = 20
)

// role is what a source folder is for, from its special-use attribute or
// its well-known name.
type role int

const (
	roleNone role = iota
	roleSent
	roleDrafts
	roleJunk
	roleTrash
	roleArchive
	roleAll       // Gmail "All Mail": copied last into Archive
	roleStarred   // Gmail: repeats other folders
	roleImportant // Gmail: repeats other folders
)

// specialUse is the special use of our folder for a role.
func (r role) specialUse() string {
	switch r {
	case roleSent:
		return storage.SpecialSent
	case roleDrafts:
		return storage.SpecialDrafts
	case roleJunk:
		return storage.SpecialJunk
	case roleTrash:
		return storage.SpecialTrash
	case roleArchive, roleAll:
		return storage.SpecialArchive
	}
	return ""
}

// defaultName is our default folder of a special use.
func defaultName(use string) string {
	switch use {
	case storage.SpecialSent:
		return "Sent"
	case storage.SpecialDrafts:
		return "Drafts"
	case storage.SpecialJunk:
		return "Junk"
	case storage.SpecialTrash:
		return "Trash"
	case storage.SpecialArchive:
		return "Archive"
	}
	return ""
}

// roleOfAttr maps RFC 6154 (and RFC 8457) attributes.
func roleOfAttr(attr string) role {
	switch strings.ToLower(attr) {
	case `\sent`:
		return roleSent
	case `\drafts`:
		return roleDrafts
	case `\junk`:
		return roleJunk
	case `\trash`:
		return roleTrash
	case `\archive`:
		return roleArchive
	case `\all`:
		return roleAll
	case `\flagged`:
		return roleStarred
	case `\important`:
		return roleImportant
	}
	return roleNone
}

// wellKnown are folder names (lowercase, "/" as separator) that have a
// role without attributes.
var wellKnown = map[string]role{
	"sent": roleSent, "sent items": roleSent, "sent messages": roleSent, "sent mail": roleSent,
	"gesendet": roleSent, "gesendete objekte": roleSent, "gesendete elemente": roleSent,
	"gesendete nachrichten": roleSent, "[gmail]/sent mail": roleSent, "[google mail]/gesendet": roleSent,

	"drafts": roleDrafts, "draft": roleDrafts, "entwürfe": roleDrafts, "entwurf": roleDrafts,
	"[gmail]/drafts": roleDrafts, "[google mail]/entwürfe": roleDrafts,

	"junk": roleJunk, "spam": roleJunk, "junk e-mail": roleJunk, "junk email": roleJunk,
	"junk-e-mail": roleJunk, "bulk mail": roleJunk, "[gmail]/spam": roleJunk, "[google mail]/spam": roleJunk,

	"trash": roleTrash, "deleted items": roleTrash, "deleted messages": roleTrash, "bin": roleTrash,
	"papierkorb": roleTrash, "gelöschte objekte": roleTrash, "gelöschte elemente": roleTrash,
	"[gmail]/trash": roleTrash, "[gmail]/bin": roleTrash, "[google mail]/papierkorb": roleTrash,

	"archive": roleArchive, "archives": roleArchive, "archiv": roleArchive,

	"[gmail]/all mail": roleAll, "[google mail]/alle nachrichten": roleAll,
	"[gmail]/starred": roleStarred, "[google mail]/markiert": roleStarred,
	"[gmail]/important": roleImportant, "[google mail]/wichtig": roleImportant,
}

// roleOf decides the role of a folder: attributes first, then the name
// (with "/" as separator).
func roleOf(attrs []string, name string) role {
	for _, a := range attrs {
		if r := roleOfAttr(a); r != roleNone {
			return r
		}
	}
	return wellKnown[strings.ToLower(name)]
}

// sanitizeComponent makes one level of a folder name valid here: "/",
// "*", "%" and control characters become "_", an empty level "_".
func sanitizeComponent(c string) string {
	c = strings.ToValidUTF8(c, "_")
	c = strings.Map(func(r rune) rune {
		if r == '/' || r == '*' || r == '%' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, c)
	if strings.TrimSpace(c) == "" {
		return "_"
	}
	return c
}

// joinFolder builds a local folder name from its levels; "" when it is
// too deep. INBOX in any case at the top becomes INBOX. The name is cut
// to 255 bytes.
func joinFolder(levels []string) string {
	if len(levels) == 0 || len(levels) > maxFolderDepth {
		return ""
	}
	parts := make([]string, len(levels))
	for i, l := range levels {
		parts[i] = sanitizeComponent(l)
	}
	if strings.EqualFold(parts[0], storage.Inbox) {
		parts[0] = storage.Inbox
	}
	name := strings.Join(parts, storage.Separator)
	if len(name) > 255 {
		name = name[:255]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
		name = strings.TrimRight(name, storage.Separator)
	}
	n, err := storage.NormalizeFolderName(name)
	if err != nil {
		return ""
	}
	return n
}

// remoteLevels splits a remote folder name into levels: the personal
// namespace prefix is removed (except from INBOX itself), a zero delimiter
// means a flat name.
func remoteLevels(name string, delim rune, prefix string) []string {
	if prefix != "" && !strings.EqualFold(name, storage.Inbox) && strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
		name = name[len(prefix):]
	}
	if delim == 0 {
		return []string{name}
	}
	return strings.Split(name, string(delim))
}

// sourceFolder is a folder of a source as found.
type sourceFolder struct {
	remote   string // name at the source
	levels   []string
	attrs    []string
	messages int
	bytes    int64
	noSelect bool
}

// plan is the folder mapping of a source.
type plan struct {
	folders []planned
}

type planned struct {
	config.ImportFolder
	role role
}

// localFolders maps special uses to the names of the mailbox's folders
// that have them, so a renamed Sent folder is still the one used.
type localFolders map[string]string

func newLocalFolders(fs []storage.Folder) localFolders {
	m := localFolders{}
	for _, f := range fs {
		if f.SpecialUse != "" {
			if _, ok := m[f.SpecialUse]; !ok {
				m[f.SpecialUse] = f.Name
			}
		}
	}
	return m
}

func (l localFolders) name(use string) string {
	if n, ok := l[use]; ok {
		return n
	}
	return defaultName(use)
}

// makePlan maps source folders to ours and decides the default selection.
func makePlan(src []sourceFolder, local localFolders) plan {
	var p plan
	n := 0
	for _, f := range src {
		if f.noSelect {
			continue
		}
		name := strings.Join(f.levels, "/")
		r := roleOf(f.attrs, name)
		pf := planned{role: r, ImportFolder: config.ImportFolder{
			Remote: f.remote, Messages: f.messages, Bytes: f.bytes, Selected: true,
		}}
		if use := r.specialUse(); use != "" {
			pf.Local = local.name(use)
			pf.SpecialUse = use
		} else {
			pf.Local = joinFolder(f.levels)
		}
		switch r {
		case roleJunk:
			pf.Selected, pf.Note = false, config.ImportNoteJunk
		case roleTrash:
			pf.Selected, pf.Note = false, config.ImportNoteTrash
		case roleStarred:
			pf.Selected, pf.Note, pf.Local = false, config.ImportNoteStarred, ""
		case roleImportant:
			pf.Selected, pf.Note, pf.Local = false, config.ImportNoteImportant, ""
		case roleAll:
			pf.Note = config.ImportNoteAllMail
		}
		if pf.Local == "" && r != roleStarred && r != roleImportant {
			pf.Selected, pf.Note = false, config.ImportNoteTooMany
		}
		if n >= maxFolders {
			pf.Selected, pf.Note, pf.Local = false, config.ImportNoteTooMany, ""
		}
		n++
		p.folders = append(p.folders, pf)
	}
	return p
}

// check summarises a plan.
func (p plan) check() config.ImportCheck {
	c := config.ImportCheck{Folders: []config.ImportFolder{}}
	for _, f := range p.folders {
		c.Folders = append(c.Folders, f.ImportFolder)
		if f.Selected {
			c.Messages += f.Messages
			c.Bytes += f.Bytes
		}
	}
	return c
}

// errUnknownFolder: a choice names a folder the source does not have.
type errUnknownFolder string

func (e errUnknownFolder) Error() string { return "the source has no folder " + string(e) }

// choose returns the folders to copy, in order: Gmail's All Mail last.
func (p plan) choose(ch config.ImportChoices) ([]planned, error) {
	var out, last []planned
	pick := func(f planned) {
		if f.role == roleAll {
			last = append(last, f)
		} else {
			out = append(out, f)
		}
	}
	if ch.Folders != nil {
		byName := map[string]planned{}
		for _, f := range p.folders {
			byName[f.Remote] = f
		}
		seen := map[string]bool{}
		for _, name := range ch.Folders {
			f, ok := byName[name]
			if !ok {
				return nil, errUnknownFolder(name)
			}
			if seen[name] || f.Local == "" {
				continue
			}
			seen[name] = true
			pick(f)
		}
	} else {
		for _, f := range p.folders {
			if f.Selected || f.role == roleJunk && ch.IncludeJunk && f.Local != "" ||
				f.role == roleTrash && ch.IncludeTrash && f.Local != "" {
				pick(f)
			}
		}
	}
	return append(out, last...), nil
}
