package account

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/danielgtmn/ownpanel/internal/mail/dav"
	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Calendars and address books in the self-service area: a list, and the
// upload (.ics, .vcf) and download of one collection. Programs use the
// mail service's DAV tree (package dav) for everything else.

type collectionView struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Color       string `json:"color"`
	Objects     int    `json:"objects"`
	Bytes       int64  `json:"bytes"`
}

func (h *Handler) collections(w http.ResponseWriter, r *http.Request, s *session) {
	st := h.opts.Backend.Store()
	if err := st.EnsureDefaultDAVCollections(r.Context(), s.mailbox.StorageID); err != nil {
		h.fail(w, err)
		return
	}
	list, err := st.DAVCollections(r.Context(), s.mailbox.StorageID, "")
	if err != nil {
		h.fail(w, err)
		return
	}
	out := struct {
		Collections []collectionView `json:"collections"`
		MaxFileSize int              `json:"max_file_bytes"`
	}{Collections: []collectionView{}, MaxFileSize: davobj.MaxFileBytes}
	for _, c := range list {
		name := c.DisplayName
		if name == "" {
			name = c.Name
		}
		out.Collections = append(out.Collections, collectionView{Kind: c.Kind, Name: c.Name, DisplayName: name, Color: c.Color, Objects: c.Objects, Bytes: c.Bytes})
	}
	writeJSON(w, http.StatusOK, out)
}

// collectionOf finds the collection named in the path; on failure it has
// answered.
func (h *Handler) collectionOf(w http.ResponseWriter, r *http.Request, s *session) (storage.DAVCollection, bool) {
	kind := r.PathValue("kind")
	if kind != davobj.Calendar && kind != davobj.AddressBook {
		writeError(w, http.StatusNotFound, "That calendar or address book doesn't exist (anymore).")
		return storage.DAVCollection{}, false
	}
	c, err := h.opts.Backend.Store().DAVCollection(r.Context(), s.mailbox.StorageID, kind, r.PathValue("name"))
	if errors.Is(err, storage.ErrNoCollection) || errors.Is(err, storage.ErrInvalidID) {
		writeError(w, http.StatusNotFound, "That calendar or address book doesn't exist (anymore).")
		return c, false
	}
	if err != nil {
		h.fail(w, err)
		return c, false
	}
	return c, true
}

func (h *Handler) exportCollection(w http.ResponseWriter, r *http.Request, s *session) {
	c, ok := h.collectionOf(w, r, s)
	if !ok {
		return
	}
	title := c.DisplayName
	if title == "" {
		title = c.Name
	}
	hd := w.Header()
	hd.Set("Content-Type", dav.ExportType(c.Kind))
	hd.Set("Content-Disposition", `attachment; filename="`+dav.ExportFileName(title, c.Kind)+`"`)
	w.WriteHeader(http.StatusOK)
	if err := dav.Export(r.Context(), h.opts.Backend.Store(), c, w); err != nil {
		h.log.Error("mail: download of a calendar or address book failed", "err", err)
	}
}

func (h *Handler) importCollection(w http.ResponseWriter, r *http.Request, s *session) {
	c, ok := h.collectionOf(w, r, s)
	if !ok {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, davobj.MaxFileBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "The file is larger than "+strconv.Itoa(davobj.MaxFileBytes>>20)+" MB. Split it into smaller files.")
		} else {
			writeError(w, http.StatusBadRequest, "The file couldn't be read.")
		}
		return
	}
	res, err := dav.Import(r.Context(), h.opts.Backend.Store(), c, data)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, res)
	case davobj.IsInvalid(err):
		msg := "This file can't be read as a calendar. Choose a file ending in .ics."
		if c.Kind == davobj.AddressBook {
			msg = "This file can't be read as contacts. Choose a file ending in .vcf."
		}
		writeError(w, http.StatusBadRequest, msg)
	case errors.Is(err, storage.ErrOverQuota):
		writeJSON(w, http.StatusInsufficientStorage, struct {
			dav.ImportResult
			Error string `json:"error"`
		}{res, "Your mailbox is full. " + importedSoFar(res) + "Delete mail or entries you no longer need and try again."})
	case errors.Is(err, storage.ErrTooManyObjects):
		writeJSON(w, http.StatusInsufficientStorage, struct {
			dav.ImportResult
			Error string `json:"error"`
		}{res, "This calendar or address book holds as many entries as it can. " + importedSoFar(res)})
	default:
		h.fail(w, err)
	}
}

func importedSoFar(res dav.ImportResult) string {
	n := res.Added + res.Replaced
	switch n {
	case 0:
		return "Nothing was imported. "
	case 1:
		return "1 entry was imported before that. "
	}
	return strconv.Itoa(n) + " entries were imported before that. "
}
