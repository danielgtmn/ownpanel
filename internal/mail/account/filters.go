package account

// Filters and out-of-office of the signed-in mailbox (G2): the simple
// settings that write the generated script (package rules).
//
//	GET /account/api/filters        → filtersView
//	PUT /account/api/out-of-office  {out_of_office, replace} → filtersView
//	PUT /account/api/forwarding     {forwarding, replace}    → filtersView
//	PUT /account/api/filters        {filters, replace}       → filtersView
//
// 409 when a script written in a mail program is active and replace is
// not set.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/rules"
	"github.com/danielgtmn/ownpanel/internal/mail/server"
)

// RulesBackend is what the filter pages need of the mail service
// (*server.Server); a Backend without it answers 404 there.
type RulesBackend interface {
	MailRules(ctx context.Context, storageID string) (config.MailRules, error)
	UpdateRules(ctx context.Context, storageID string, replace bool, fn func(*rules.Settings) error) (config.MailRules, error)
}

var _ RulesBackend = (*server.Server)(nil)

// maxRulesBody bounds a request of the filter pages (50 rules of 10
// conditions are larger than other requests).
const maxRulesBody = 256 << 10

type filtersView struct {
	Rules config.MailRules `json:"rules"`
	// Folders the filter editor can move mail to.
	Folders []string `json:"folders"`
	// MayForward: the domain owner allows forwarding for this mailbox.
	MayForward bool `json:"may_forward"`
}

func (h *Handler) filterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /account/api/filters", h.authed(h.getFilters))
	mux.HandleFunc("PUT /account/api/out-of-office", h.authed(h.setOutOfOffice))
	mux.HandleFunc("PUT /account/api/forwarding", h.authed(h.setForwarding))
	mux.HandleFunc("PUT /account/api/filters", h.authed(h.setFilters))
}

func (h *Handler) rulesBackend(w http.ResponseWriter) (RulesBackend, bool) {
	rb, ok := h.opts.Backend.(RulesBackend)
	if !ok {
		writeError(w, http.StatusNotFound, "Not found.")
	}
	return rb, ok
}

func (h *Handler) filtersView(ctx context.Context, s *session, mr config.MailRules) (filtersView, error) {
	v := filtersView{Rules: mr, Folders: []string{}, MayForward: !s.mailbox.NoForwarding}
	st := h.opts.Backend.Store()
	if err := st.EnsureMailbox(ctx, s.mailbox.StorageID); err != nil {
		return v, err
	}
	folders, err := st.ListFolders(ctx, s.mailbox.StorageID)
	if err != nil {
		return v, err
	}
	for _, f := range folders {
		v.Folders = append(v.Folders, f.Name)
	}
	return v, nil
}

func (h *Handler) getFilters(w http.ResponseWriter, r *http.Request, s *session) {
	rb, ok := h.rulesBackend(w)
	if !ok {
		return
	}
	mr, err := rb.MailRules(r.Context(), s.mailbox.StorageID)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.writeFilters(w, r, s, mr)
}

func (h *Handler) writeFilters(w http.ResponseWriter, r *http.Request, s *session, mr config.MailRules) {
	v, err := h.filtersView(r.Context(), s, mr)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// update applies a change and answers.
func (h *Handler) update(w http.ResponseWriter, r *http.Request, s *session, replace bool, fn func(*rules.Settings) error) {
	rb, ok := h.rulesBackend(w)
	if !ok {
		return
	}
	mr, err := rb.UpdateRules(r.Context(), s.mailbox.StorageID, replace, fn)
	var inv *server.RulesInvalid
	switch {
	case errors.Is(err, server.ErrNotManaged):
		writeError(w, http.StatusConflict, "Your filters were written in a mail program. Saving here replaces them.")
		return
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, sentence(inv.Error()))
		return
	case errors.Is(err, server.ErrUnknownMailbox):
		writeError(w, http.StatusUnauthorized, errEnded)
		return
	case err != nil:
		h.fail(w, err)
		return
	}
	h.writeFilters(w, r, s, mr)
}

func (h *Handler) setOutOfOffice(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		OutOfOffice config.OutOfOffice `json:"out_of_office"`
		Replace     bool               `json:"replace"`
	}
	if !decodeRules(w, r, &in) {
		return
	}
	o := in.OutOfOffice
	// Set by the person: whoever set it before no longer counts.
	o.SetBy, o.SetAt = "", config.OutOfOffice{}.SetAt
	h.update(w, r, s, in.Replace, func(st *rules.Settings) error {
		st.OutOfOffice = o
		return nil
	})
}

func (h *Handler) setForwarding(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		Forwarding config.Forwarding `json:"forwarding"`
		Replace    bool              `json:"replace"`
	}
	if !decodeRules(w, r, &in) {
		return
	}
	f := in.Forwarding
	f.SetBy, f.SetAt = "", config.Forwarding{}.SetAt
	h.update(w, r, s, in.Replace, func(st *rules.Settings) error {
		st.Forwarding = f
		return nil
	})
}

func (h *Handler) setFilters(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		Filters []config.FilterRule `json:"filters"`
		Replace bool                `json:"replace"`
	}
	if !decodeRules(w, r, &in) {
		return
	}
	if in.Filters == nil {
		in.Filters = []config.FilterRule{}
	}
	h.update(w, r, s, in.Replace, func(st *rules.Settings) error {
		st.Filters = in.Filters
		return nil
	})
}

// decodeRules is decode with the larger limit of the filter pages.
func decodeRules(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Send the request as JSON.")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRulesBody))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "The request is too large.")
		} else {
			writeError(w, http.StatusBadRequest, "The request isn't valid.")
		}
		return false
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "The request isn't valid.")
		return false
	}
	return true
}
