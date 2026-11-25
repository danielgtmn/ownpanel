package dav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Import and export of whole calendars (.ics) and address books (.vcf),
// for the self-service area and the mailbox export.

// ImportResult counts what an import did.
type ImportResult struct {
	Added    int `json:"added"`
	Replaced int `json:"replaced"`
	Skipped  int `json:"skipped"`
}

// Import stores the events and tasks of an .ics file, or the contacts of a
// .vcf file, in collection c: an object whose UID is there already is
// replaced, others are added. It stops at the first error of the storage
// (a full mailbox: storage.ErrOverQuota) and returns what was done until
// then. A file that cannot be read gives a *davobj.Error.
func Import(ctx context.Context, st *storage.Store, c storage.DAVCollection, data []byte) (ImportResult, error) {
	var res ImportResult
	var objects [][]byte
	var err error
	ext := ".ics"
	if c.Kind == davobj.Calendar {
		objects, res.Skipped, err = davobj.SplitCalendars(data)
	} else {
		objects, res.Skipped, err = davobj.SplitCards(data)
		ext = ".vcf"
	}
	if err != nil {
		return res, err
	}
	for _, obj := range objects {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		var info davobj.Info
		if c.Kind == davobj.Calendar {
			info, _, err = davobj.ParseCalendar(obj, c.Components)
		} else {
			info, _, err = davobj.ParseCard(obj)
		}
		if err != nil {
			res.Skipped++
			continue
		}
		name := objectName(info.UID, ext)
		replace := false
		if old, err := st.DAVObjectByUID(ctx, c, info.UID); err == nil {
			name, replace = old.Name, true
		} else if !errors.Is(err, storage.ErrNoObject) {
			return res, err
		} else if _, err := st.DAVObject(ctx, c, name); err == nil {
			// Another UID maps to the same name: make it unique.
			sum := sha256.Sum256([]byte(info.UID))
			name = hex.EncodeToString(sum[:16]) + ext
		}
		_, _, err = st.PutDAVObject(ctx, c, name, obj, info, storage.PutCondition{})
		var uc *storage.UIDConflictError
		switch {
		case errors.As(err, &uc):
			res.Skipped++
			continue
		case err != nil:
			return res, err
		}
		if replace {
			res.Replaced++
		} else {
			res.Added++
		}
	}
	return res, nil
}

var simpleUID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,100}$`)

// objectName is the resource name of an imported object: the UID when it
// is simple, otherwise a hash of it.
func objectName(uid, ext string) string {
	if simpleUID.MatchString(uid) {
		return uid + ext
	}
	sum := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(sum[:16]) + ext
}

// Export writes collection c as one .ics or .vcf file.
func Export(ctx context.Context, st *storage.Store, c storage.DAVCollection, w io.Writer) error {
	return st.ExportDAVCollection(ctx, c, w)
}

// ExportFileName is a safe file name for a collection called title.
func ExportFileName(title, kind string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		case r == ' ':
			return '-'
		}
		return -1
	}, title)
	name = strings.Trim(name, ".-")
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		name = "export"
	}
	if kind == davobj.Calendar {
		return name + ".ics"
	}
	return name + ".vcf"
}

func exportType(kind string) string {
	if kind == davobj.Calendar {
		return "text/calendar; charset=utf-8"
	}
	return "text/vcard; charset=utf-8"
}

// ExportType is the media type of an exported collection.
func ExportType(kind string) string { return exportType(kind) }
