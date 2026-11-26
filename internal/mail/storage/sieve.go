package storage

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtmn/ablage"
)

// Filter scripts (Sieve) of a mailbox and what their runs remember (G2):
// out-of-office replies sent and ids the duplicate test saw. Scripts are
// stored as written; checking them is the caller's job.

// Errors of script changes.
var (
	ErrNoScript      = errors.New("no such script")
	ErrScriptExists  = errors.New("a script of that name exists")
	ErrScriptActive  = errors.New("the active script cannot be deleted")
	ErrTooManyScript = errors.New("too many scripts")
)

// SieveScript describes one script.
type SieveScript struct {
	Name    string
	Active  bool
	Size    int
	Updated time.Time
}

// scriptWhere selects a mailbox's script by name.
func scriptWhere(id, name string) ablage.Cond {
	return ablage.And(ablage.Eq("storage_id", id), ablage.Eq("name", name))
}

// SieveScripts lists a mailbox's scripts by name.
func (s *Store) SieveScripts(ctx context.Context, id string) ([]SieveScript, error) {
	list, err := s.r.sieveScripts.Find(ctx, ablage.Query{Where: ablage.Eq("storage_id", id), OrderBy: []ablage.Order{ablage.Asc("name")}})
	if err != nil {
		return nil, err
	}
	var out []SieveScript
	for _, r := range list {
		out = append(out, SieveScript{Name: r.Name, Active: r.ActiveFor != nil, Size: int(r.Size), Updated: local(r.Updated)})
	}
	return out, nil
}

// SieveScript returns one script; ErrNoScript when there is none.
func (s *Store) SieveScript(ctx context.Context, id, name string) (content string, active bool, err error) {
	r, err := s.r.sieveScripts.First(ctx, ablage.Query{Where: scriptWhere(id, name)})
	if errors.Is(err, ablage.ErrNotFound) {
		return "", false, ErrNoScript
	}
	return r.Content, r.ActiveFor != nil, err
}

// ActiveSieveScript returns the active script; ok is false when none is.
func (s *Store) ActiveSieveScript(ctx context.Context, id string) (name, content string, ok bool, err error) {
	r, err := s.r.sieveScripts.First(ctx, ablage.Query{Where: ablage.Eq("active_for", id)})
	if errors.Is(err, ablage.ErrNotFound) {
		return "", "", false, nil
	}
	return r.Name, r.Content, err == nil, err
}

// deactivate makes no script of the mailbox active (except the one named
// keep, "" for none).
func deactivate(ctx context.Context, tx *repos, id, keep string) error {
	_, err := tx.sieveScripts.UpdateWhere(ctx, ablage.And(ablage.Eq("active_for", id), ablage.Ne("name", keep)), ablage.Row{"active_for": nil})
	return err
}

// putScript writes a script, creating it or replacing its content; with
// activate it becomes the active one (the others must be inactive by then).
func (s *Store) putScript(ctx context.Context, tx *repos, id, name, content string, activate bool) error {
	r, err := tx.sieveScripts.First(ctx, ablage.Query{Where: scriptWhere(id, name)})
	if err != nil && !errors.Is(err, ablage.ErrNotFound) {
		return err
	}
	set := ablage.Row{"content": content, "size": int64(len(content)), "updated": sec(s.now())}
	if activate {
		set["active_for"] = id
	}
	if err == nil {
		return tx.sieveScripts.Patch(ctx, r.ID, set)
	}
	n := sieveScriptRec{StorageID: id, Name: name, Content: content, Size: int64(len(content)), Updated: sec(s.now())}
	if activate {
		n.ActiveFor = &id
	}
	return tx.sieveScripts.Create(ctx, &n)
}

// PutSieveScript stores a script (replacing one of the same name). A new
// script beyond max scripts is ErrTooManyScript. activate also makes it
// the active one.
func (s *Store) PutSieveScript(ctx context.Context, id, name, content string, max int, activate bool) error {
	if err := checkID(id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *repos, _ *changes) error {
		n, err := tx.sieveScripts.Count(ctx, ablage.Eq("storage_id", id))
		if err != nil {
			return err
		}
		exists, err := tx.sieveScripts.Count(ctx, scriptWhere(id, name))
		if err != nil {
			return err
		}
		if exists == 0 && n >= int64(max) {
			return ErrTooManyScript
		}
		if activate {
			if err := deactivate(ctx, tx, id, name); err != nil {
				return err
			}
		}
		return s.putScript(ctx, tx, id, name, content, activate)
	})
}

// SetActiveSieveScript makes name the active script; "" makes none active.
func (s *Store) SetActiveSieveScript(ctx context.Context, id, name string) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		var r sieveScriptRec
		if name != "" {
			var err error
			r, err = tx.sieveScripts.First(ctx, ablage.Query{Where: scriptWhere(id, name)})
			if errors.Is(err, ablage.ErrNotFound) {
				return ErrNoScript
			}
			if err != nil {
				return err
			}
		}
		if err := deactivate(ctx, tx, id, ""); err != nil {
			return err
		}
		if name == "" {
			return nil
		}
		return tx.sieveScripts.Patch(ctx, r.ID, ablage.Row{"active_for": id})
	})
}

// DeleteSieveScript removes a script that is not active.
func (s *Store) DeleteSieveScript(ctx context.Context, id, name string) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		r, err := tx.sieveScripts.First(ctx, ablage.Query{Where: scriptWhere(id, name)})
		if errors.Is(err, ablage.ErrNotFound) {
			return ErrNoScript
		}
		if err != nil {
			return err
		}
		if r.ActiveFor != nil {
			return ErrScriptActive
		}
		return tx.sieveScripts.Delete(ctx, r.ID)
	})
}

// RenameSieveScript renames a script; it stays active if it was.
func (s *Store) RenameSieveScript(ctx context.Context, id, from, to string) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		n, err := tx.sieveScripts.Count(ctx, scriptWhere(id, to))
		if err != nil {
			return err
		}
		if n > 0 && from != to {
			return ErrScriptExists
		}
		r, err := tx.sieveScripts.First(ctx, ablage.Query{Where: scriptWhere(id, from)})
		if errors.Is(err, ablage.ErrNotFound) {
			return ErrNoScript
		}
		if err != nil {
			return err
		}
		return tx.sieveScripts.Patch(ctx, r.ID, ablage.Row{"name": to, "updated": sec(s.now())})
	})
}

// SieveReplied records that sender gets an out-of-office reply of handle
// now, unless one was sent within period: it returns true when the reply
// may be sent (and is recorded), false when one was sent already.
func (s *Store) SieveReplied(ctx context.Context, id, handle, sender string, period time.Duration, now time.Time) (bool, error) {
	ok := false
	err := s.write(ctx, func(tx *repos, _ *changes) error {
		ok = false
		r, err := tx.sieveReplies.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("storage_id", id), ablage.Eq("handle", handle), ablage.Eq("sender", sender))})
		if err != nil && !errors.Is(err, ablage.ErrNotFound) {
			return err
		}
		if err == nil && r.Until.Unix() > now.Unix() {
			return nil
		}
		ok = true
		return tx.sieveReplies.UpsertOn(ctx, &sieveReplyRec{StorageID: id, Handle: handle, Sender: sender, Until: sec(now.Add(period))},
			"storage_id", "handle", "sender")
	})
	return ok, err
}

// ForgetSieveReply takes back what SieveReplied recorded (the reply could
// not be sent after all).
func (s *Store) ForgetSieveReply(ctx context.Context, id, handle, sender string) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		_, err := tx.sieveReplies.DeleteWhere(ctx, ablage.And(ablage.Eq("storage_id", id), ablage.Eq("handle", handle), ablage.Eq("sender", sender)))
		return err
	})
}

// SieveDuplicate tells whether the duplicate test saw key within period
// and records it: a new key is kept for period; a seen one is moved
// forward only with last (RFC 7352 :last).
func (s *Store) SieveDuplicate(ctx context.Context, id, key string, period time.Duration, last bool, now time.Time) (bool, error) {
	seen := false
	err := s.write(ctx, func(tx *repos, _ *changes) error {
		r, err := tx.sieveDuplicates.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("storage_id", id), ablage.Eq("key", key))})
		if err != nil && !errors.Is(err, ablage.ErrNotFound) {
			return err
		}
		seen = err == nil && r.Until.Unix() > now.Unix()
		if seen && !last {
			return nil
		}
		return tx.sieveDuplicates.UpsertOn(ctx, &sieveDuplicateRec{StorageID: id, Key: key, Until: sec(now.Add(period))}, "storage_id", "key")
	})
	return seen, err
}

// SieveDuplicateSeen tells whether the duplicate test saw key and still
// remembers it, without recording anything.
func (s *Store) SieveDuplicateSeen(ctx context.Context, id, key string, now time.Time) (bool, error) {
	r, err := s.r.sieveDuplicates.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("storage_id", id), ablage.Eq("key", key))})
	if errors.Is(err, ablage.ErrNotFound) {
		return false, nil
	}
	return err == nil && r.Until.Unix() > now.Unix(), err
}

// PruneSieve forgets replies and duplicate ids whose time has passed.
func (s *Store) PruneSieve(ctx context.Context, now time.Time) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		if _, err := tx.sieveReplies.DeleteWhere(ctx, ablage.Lte("until", sec(now))); err != nil {
			return err
		}
		_, err := tx.sieveDuplicates.DeleteWhere(ctx, ablage.Lte("until", sec(now)))
		return err
	})
}

// ErrSieveChanged: the scripts changed since they were read (a mail
// program changed them meanwhile).
var ErrSieveChanged = errors.New("the filter scripts changed meanwhile")

// SieveGuard is what a change of the generated script expects to find:
// the name of the active script ("" for none) and the generated script's
// content (HasGenerated false: no such script).
type SieveGuard struct {
	Active       string
	Generated    string
	HasGenerated bool
}

// ReplaceGeneratedSieve writes the generated script name and makes it the
// active one, or with content "" deactivates the active script and
// deletes name. All in one transaction and only if the scripts are still
// as guard says (ErrSieveChanged otherwise), so a change a mail program
// made in between is never overwritten. A new script beyond max is
// ErrTooManyScript.
func (s *Store) ReplaceGeneratedSieve(ctx context.Context, id, name, content string, max int, guard SieveGuard) error {
	if err := checkID(id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *repos, _ *changes) error {
		active := ""
		r, err := tx.sieveScripts.First(ctx, ablage.Query{Where: ablage.Eq("active_for", id)})
		if err == nil {
			active = r.Name
		} else if !errors.Is(err, ablage.ErrNotFound) {
			return err
		}
		g, err := tx.sieveScripts.First(ctx, ablage.Query{Where: scriptWhere(id, name)})
		hasGen := err == nil
		if err != nil && !errors.Is(err, ablage.ErrNotFound) {
			return err
		}
		if active != guard.Active || hasGen != guard.HasGenerated || hasGen && g.Content != guard.Generated {
			return ErrSieveChanged
		}
		if content == "" {
			if err := deactivate(ctx, tx, id, ""); err != nil {
				return err
			}
			_, err := tx.sieveScripts.DeleteWhere(ctx, scriptWhere(id, name))
			return err
		}
		if !hasGen {
			n, err := tx.sieveScripts.Count(ctx, ablage.Eq("storage_id", id))
			if err != nil {
				return err
			}
			if n >= int64(max) {
				return ErrTooManyScript
			}
		}
		if err := deactivate(ctx, tx, id, ""); err != nil {
			return err
		}
		return s.putScript(ctx, tx, id, name, content, true)
	})
}
