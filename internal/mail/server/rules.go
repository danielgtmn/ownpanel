package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	maildeliver "github.com/danielgtmn/ownpanel/internal/mail/deliver"
	"github.com/danielgtmn/ownpanel/internal/mail/rules"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Filters and out-of-office of a mailbox (G2): the simple settings of the
// self-service area and the panel live in the generated script
// (package rules); scripts written elsewhere are left alone unless a
// change says to replace them.

// ErrNotManaged: the mailbox runs a script written elsewhere; the simple
// settings can only replace it.
var ErrNotManaged = errors.New("the mailbox uses filters written in a mail program; saving here replaces them")

// ErrNoForwarding: the mailbox may not forward.
var ErrNoForwarding = errors.New("forwarding is turned off for this mailbox")

// rulesMu serializes changes of the simple settings.
var rulesMu sync.Mutex

// RulesInvalid is a change the person must fix; its text is for people.
type RulesInvalid struct{ Err error }

func (e *RulesInvalid) Error() string { return e.Err.Error() }

// MailRules reports the filters of a mailbox.
func (s *Server) MailRules(ctx context.Context, storageID string) (config.MailRules, error) {
	if _, ok := s.Directory().MailboxByStorageID(storageID); !ok {
		return config.MailRules{}, ErrUnknownMailbox
	}
	st, managed, active, script, err := s.loadRules(ctx, storageID)
	if err != nil {
		return config.MailRules{}, err
	}
	return rulesView(st, managed, active, script), nil
}

func rulesView(st rules.Settings, managed bool, active, script string) config.MailRules {
	out := config.MailRules{Managed: managed, ActiveScript: active, Filters: []config.FilterRule{}}
	if !managed {
		// The settings do not run: only the script that does is shown.
		out.Script = script
		return out
	}
	out.OutOfOffice, out.Forwarding = st.OutOfOffice, st.Forwarding
	if st.Filters != nil {
		out.Filters = st.Filters
	}
	return out
}

// loadRules reads the simple settings. managed is false when another
// script (or an edited generated one) is active; then st is what the
// generated script holds, if it still exists unchanged, else empty.
func (s *Server) loadRules(ctx context.Context, id string) (st rules.Settings, managed bool, active, script string, err error) {
	name, src, ok, err := s.store.ActiveSieveScript(ctx, id)
	if err != nil {
		return st, false, "", "", err
	}
	if ok && name == rules.ScriptName {
		if parsed, ok := rules.Parse(src); ok {
			return parsed, true, name, src, nil
		}
		return rules.Settings{}, false, name, src, nil
	}
	// Another script or none is active: the generated script, if it is
	// still unchanged, is where a replacement starts from.
	if gen, _, err := s.store.SieveScript(ctx, id, rules.ScriptName); err == nil {
		if parsed, pok := rules.Parse(gen); pok {
			st = parsed
		}
	} else if !errors.Is(err, storage.ErrNoScript) {
		return st, false, "", "", err
	}
	if ok {
		return st, false, name, src, nil
	}
	return st, true, "", "", nil
}

// UpdateRules changes the simple settings with fn and saves the generated
// script as the active one. Without replace it refuses with ErrNotManaged
// when another script is active.
func (s *Server) UpdateRules(ctx context.Context, storageID string, replace bool, fn func(*rules.Settings) error) (config.MailRules, error) {
	box, ok := s.Directory().MailboxByStorageID(storageID)
	if !ok {
		return config.MailRules{}, ErrUnknownMailbox
	}
	rulesMu.Lock()
	defer rulesMu.Unlock()
	st, managed, _, _, err := s.loadRules(ctx, storageID)
	if err != nil {
		return config.MailRules{}, err
	}
	if !managed && !replace {
		return config.MailRules{}, ErrNotManaged
	}
	if err := fn(&st); err != nil {
		return config.MailRules{}, err
	}
	if err := rules.Check(&st); err != nil {
		return config.MailRules{}, &RulesInvalid{err}
	}
	if box.NoForwarding {
		if st.Forwarding.Enabled {
			return config.MailRules{}, &RulesInvalid{ErrNoForwarding}
		}
		for _, r := range st.Filters {
			if r.ForwardTo != "" {
				return config.MailRules{}, &RulesInvalid{ErrNoForwarding}
			}
		}
	}
	if st.Empty() {
		if name, _, ok, err := s.store.ActiveSieveScript(ctx, storageID); err != nil {
			return config.MailRules{}, err
		} else if ok && (name == rules.ScriptName || replace) {
			if err := s.store.SetActiveSieveScript(ctx, storageID, ""); err != nil {
				return config.MailRules{}, err
			}
		}
		if err := s.store.DeleteSieveScript(ctx, storageID, rules.ScriptName); err != nil && !errors.Is(err, storage.ErrNoScript) {
			return config.MailRules{}, err
		}
	} else {
		// The generated script counts against the limit like any other.
		if err := s.store.PutSieveScript(ctx, storageID, rules.ScriptName, rules.Generate(st), maildeliver.MaxScripts, true); err != nil {
			if errors.Is(err, storage.ErrTooManyScript) {
				return config.MailRules{}, &RulesInvalid{errors.New("the mailbox has the most filter scripts allowed; delete one in your mail program first")}
			}
			return config.MailRules{}, err
		}
	}
	st, managed, active, script, err := s.loadRules(ctx, storageID)
	if err != nil {
		return config.MailRules{}, err
	}
	return rulesView(st, managed, active, script), nil
}

// outOfOfficeOn tells whether a mailbox has an out-of-office reply on now
// (only replies set with the simple settings are known).
func (s *Server) outOfOfficeOn(ctx context.Context, id string) bool {
	name, src, ok, err := s.store.ActiveSieveScript(ctx, id)
	if err != nil || !ok || name != rules.ScriptName {
		return false
	}
	st, ok := rules.Parse(src)
	return ok && st.OutOfOfficeOn(time.Now())
}

// Admin socket handlers.

func (s *Server) rulesRoutes(mux *http.ServeMux) {
	mb := config.PathMailbox + "{id}"
	mux.HandleFunc("GET "+mb+config.PathRules, s.handleRules)
	mux.HandleFunc("PUT "+mb+config.PathOutOfOffice, s.handleSetOutOfOffice)
	mux.HandleFunc("PUT "+mb+config.PathForwarding, s.handleSetForwarding)
}

func (s *Server) rulesAnswer(w http.ResponseWriter, v config.MailRules, err error) {
	var inv *RulesInvalid
	switch {
	case errors.Is(err, ErrUnknownMailbox):
		http.Error(w, "unknown mailbox", http.StatusNotFound)
	case errors.Is(err, ErrNotManaged):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &inv):
		http.Error(w, inv.Error(), http.StatusBadRequest)
	case err != nil:
		s.internalError(w, "filters", err)
	default:
		writeJSON(w, v)
	}
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !config.ValidStorageID(id) {
		http.Error(w, "invalid storage id", http.StatusBadRequest)
		return
	}
	v, err := s.MailRules(r.Context(), id)
	s.rulesAnswer(w, v, err)
}

func (s *Server) handleSetOutOfOffice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var ch config.OutOfOfficeChange
	if !config.ValidStorageID(id) || json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&ch) != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	v, err := s.UpdateRules(r.Context(), id, ch.Replace, func(st *rules.Settings) error {
		o := ch.OutOfOffice
		if o.SetBy != "" {
			o.SetAt = time.Now().UTC().Truncate(time.Second)
		}
		if !o.Enabled {
			o = config.OutOfOffice{}
		}
		st.OutOfOffice = o
		return nil
	})
	s.rulesAnswer(w, v, err)
}

func (s *Server) handleSetForwarding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var ch config.ForwardingChange
	if !config.ValidStorageID(id) || json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&ch) != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	v, err := s.UpdateRules(r.Context(), id, ch.Replace, func(st *rules.Settings) error {
		f := ch.Forwarding
		if f.SetBy != "" {
			f.SetAt = time.Now().UTC().Truncate(time.Second)
		}
		if !f.Enabled {
			f = config.Forwarding{}
		}
		st.Forwarding = f
		return nil
	})
	s.rulesAnswer(w, v, err)
}
