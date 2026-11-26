package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtmn/ablage"

	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
)

// Calendars and address books of a mailbox (wiki/Groupware-Architecture.md,
// G4a). Like messages, every object is a file, under
// dav/<storage_id>/<collection id>/, written to a temporary name, fsynced,
// renamed into place and its directory fsynced before the index row is
// committed. The index in mail.db keeps collections, objects and, for
// sync (RFC 6578), deletions.
//
// Every change of a collection takes the next number of its sequence
// (dav_collections.seq). An object keeps the number of its last change,
// a deletion the number it was deleted with. A sync token names the
// epoch, the collection and a number: the changes since are the objects
// and deletions with a higher number. Deletions are kept DAVDeletionRetention;
// pruning them raises pruned_seq, and older tokens are refused.
//
// The epoch (meta dav_epoch) is part of every sync token, ETag and ctag.
// It changes when the start-up check found index and files apart or after
// a restore, like UIDVALIDITY for mail, so clients drop what they cached.
// Collection ids are never reused (those from before ablage are integers
// that keep their digits, new ones ablage ids): a calendar deleted and
// created again never accepts the old one's ETags or sync tokens. The
// collection's directory is named by its id. In ETags and sync tokens an
// integer id is written in hex, as before ablage, so the tokens clients
// hold stay valid; an ablage id as it is.
//
// DAV data counts towards the mailbox's quota: its bytes, plus
// DAVObjectOverhead per object and DAVDeletionOverhead per kept deletion
// record, are part of mailboxes.used_bytes (not of messages). At most
// maxDAVDeletions deletion records are kept per collection.

// Limits and names of DAV data.
const (
	davDir = "dav"
	// MaxDAVCollections is the most collections (calendars and address
	// books together) a mailbox may have.
	MaxDAVCollections = 100
	// MaxDAVObjects is the most objects one collection may hold.
	MaxDAVObjects = 100_000
	// DAVDeletionRetention is how long deletions are kept for sync.
	DAVDeletionRetention = 90 * 24 * time.Hour
	// MaxDAVName bounds the name of a collection or object (a URL segment).
	MaxDAVName = 255
	// DAVObjectOverhead and DAVDeletionOverhead are counted towards the
	// quota for every object and every kept deletion record, for their
	// index rows.
	DAVObjectOverhead   = 512
	DAVDeletionOverhead = 128
)

// maxDAVDeletions bounds the deletion records kept per collection; the
// oldest go first and sync tokens from before them are refused (a
// variable for tests).
var maxDAVDeletions = 10_000

// Errors of DAV storage.
var (
	ErrNoCollection       = errors.New("no such collection")
	ErrCollectionExists   = errors.New("collection already exists")
	ErrTooManyCollections = errors.New("too many collections")
	ErrTooManyObjects     = errors.New("too many objects in the collection")
	ErrNoObject           = errors.New("no such object")
	// ErrPrecondition: If-Match or If-None-Match did not hold.
	ErrPrecondition = errors.New("precondition failed")
	// ErrInvalidSyncToken: the token is from another epoch or collection,
	// or older than the kept deletions.
	ErrInvalidSyncToken = errors.New("invalid sync token")
	ErrInvalidDAVName   = errors.New("invalid name")
)

// UIDConflictError: another object of the collection has the UID.
type UIDConflictError struct{ Name string }

func (e *UIDConflictError) Error() string { return "another object has this UID: " + e.Name }

// DAVCollection is a calendar or an address book.
type DAVCollection struct {
	ID          string
	StorageID   string
	Kind        string // davobj.Calendar or davobj.AddressBook
	Name        string // URL segment
	DisplayName string
	Color       string
	Description string
	// Components a calendar takes (VEVENT, VTODO); empty means both.
	Components []string
	Seq        int64
	PrunedSeq  int64
	Created    time.Time
	// Objects and Bytes are counted when listed.
	Objects int
	Bytes   int64
	// Epoch is the DAV epoch at the time it was read.
	Epoch int64
}

// CTag changes with every change of the collection's content.
func (c DAVCollection) CTag() string { return fmt.Sprintf("%x-%x", c.Epoch, c.Seq) }

// SyncToken is the token of the collection's current state.
func (c DAVCollection) SyncToken() string { return SyncToken(c.Epoch, c.ID, c.Seq) }

// SyncToken formats a sync token.
func SyncToken(epoch int64, collection string, seq int64) string {
	return fmt.Sprintf("urn:ownpanel:sync:%x:%s:%x", epoch, collToken(collection), seq)
}

// collToken is how a collection id appears in ETags and sync tokens: an
// integer id (from before ablage) in hex, as it always did, an ablage id
// as it is. The two never look alike: hex of an int64 has at most 16
// characters, an ablage id 26.
func collToken(id string) string {
	if n, err := strconv.ParseInt(id, 10, 64); err == nil && n > 0 && strconv.FormatInt(n, 10) == id {
		return strconv.FormatInt(n, 16)
	}
	return id
}

// validCollID tells whether id can be a collection id (and so a directory
// name).
func validCollID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// ParseSyncToken reads a token of SyncToken; collection is the id as it
// appears in the token (see collToken).
func ParseSyncToken(tok string) (epoch int64, collection string, seq int64, ok bool) {
	rest, found := strings.CutPrefix(tok, "urn:ownpanel:sync:")
	if !found {
		return 0, "", 0, false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 || !validCollID(parts[1]) {
		return 0, "", 0, false
	}
	var v [2]int64
	for i, p := range []string{parts[0], parts[2]} {
		n, err := strconv.ParseInt(p, 16, 64)
		if err != nil || n < 0 {
			return 0, "", 0, false
		}
		v[i] = n
	}
	return v[0], parts[1], v[1], true
}

// DAVObject is a calendar object or a vCard.
type DAVObject struct {
	ID           string
	CollectionID string
	StorageID    string
	Name         string
	UID          string
	Component    string
	Size         int64
	Start, End   int64
	Seq          int64
	Modified     time.Time
	file         string
	epoch        int64
}

// ETag is the object's entity tag, without quotes.
func (o DAVObject) ETag() string {
	return fmt.Sprintf("%x-%s-%x", o.epoch, collToken(o.CollectionID), o.Seq)
}

// ValidDAVName tells whether name can be a collection or object name: one
// URL segment of printable UTF-8 without "/", not "." or "..".
func ValidDAVName(name string) bool {
	if name == "" || len(name) > MaxDAVName || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == 0xfffd {
			return false
		}
	}
	return true
}

func (s *Store) davDir(id string) string { return filepath.Join(s.dir, davDir, id) }

func (s *Store) collectionDir(id, coll string) string {
	return filepath.Join(s.davDir(id), coll)
}

// davEpoch reads the epoch, creating it the first time.
func (s *Store) davEpoch(ctx context.Context) (int64, error) {
	e, ok, err := hasMeta(ctx, s.r, metaDAVEpoch)
	if err != nil || ok {
		return e, err
	}
	err = s.write(ctx, func(tx *repos, _ *changes) error {
		var err error
		e, err = s.txEpoch(ctx, tx)
		return err
	})
	return e, err
}

func (s *Store) txEpoch(ctx context.Context, tx *repos) (int64, error) {
	e, ok, err := hasMeta(ctx, tx, metaDAVEpoch)
	if err != nil || ok {
		return e, err
	}
	return s.newDAVEpoch(ctx, tx)
}

// newDAVEpoch sets an epoch never used before: larger than the last one
// and at least the current time in seconds, so a restored database still
// gets a new one.
func (s *Store) newDAVEpoch(ctx context.Context, tx *repos) (int64, error) {
	last, err := metaValue(ctx, tx, metaDAVEpoch)
	if err != nil {
		return 0, err
	}
	v := max(last+1, s.now().Unix())
	return v, setMeta(ctx, tx, metaDAVEpoch, v)
}

func (r davCollectionRec) collection(epoch int64) DAVCollection {
	c := DAVCollection{ID: r.ID, StorageID: r.StorageID, Kind: r.Kind, Name: r.Name, DisplayName: r.DisplayName, Color: r.Color,
		Description: r.Description, Seq: r.Seq, PrunedSeq: r.PrunedSeq, Created: local(r.Created), Epoch: epoch}
	if r.Components != "" {
		c.Components = strings.Split(r.Components, ",")
	}
	return c
}

// DAVCollections lists a mailbox's collections of a kind ("" for all), by
// kind and name, with their object counts and sizes.
func (s *Store) DAVCollections(ctx context.Context, id, kind string) ([]DAVCollection, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	epoch, err := s.davEpoch(ctx)
	if err != nil {
		return nil, err
	}
	var out []DAVCollection
	err = s.read(ctx, func(tx *repos) error {
		where := ablage.Eq("storage_id", id)
		if kind != "" {
			where = ablage.And(where, ablage.Eq("kind", kind))
		}
		list, err := tx.davCollections.Find(ctx, ablage.Query{Where: where, OrderBy: []ablage.Order{ablage.Asc("kind"), ablage.Asc("name")}})
		if err != nil || len(list) == 0 {
			return err
		}
		ids := make([]string, len(list))
		for i, c := range list {
			ids[i] = c.ID
		}
		counts, err := tx.davObjects.Aggregate(ctx, ablage.Aggregation{
			Where: ablage.In("collection_id", ids...), GroupBy: []string{"collection_id"},
			Values: []ablage.Agg{ablage.CountAll("n"), ablage.Sum("size", "bytes")},
		})
		if err != nil {
			return err
		}
		by := map[string]ablage.Row{}
		for _, r := range counts {
			by[r["collection_id"].(string)] = r
		}
		for _, r := range list {
			c := r.collection(epoch)
			if n, ok := by[r.ID]; ok {
				c.Objects, c.Bytes = int(n["n"].(int64)), n["bytes"].(int64)
			}
			out = append(out, c)
		}
		return nil
	})
	return out, err
}

// DAVCollection returns one collection of a mailbox.
func (s *Store) DAVCollection(ctx context.Context, id, kind, name string) (DAVCollection, error) {
	if err := checkID(id); err != nil {
		return DAVCollection{}, err
	}
	epoch, err := s.davEpoch(ctx)
	if err != nil {
		return DAVCollection{}, err
	}
	r, err := s.r.davCollections.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("storage_id", id), ablage.Eq("kind", kind), ablage.Eq("name", name))})
	if errors.Is(err, ablage.ErrNotFound) {
		return DAVCollection{}, ErrNoCollection
	}
	if err != nil {
		return DAVCollection{}, err
	}
	return r.collection(epoch), nil
}

// CreateDAVCollection creates a collection (Kind, Name, DisplayName, Color,
// Description, Components of c).
func (s *Store) CreateDAVCollection(ctx context.Context, id string, c DAVCollection) (DAVCollection, error) {
	if err := checkID(id); err != nil {
		return c, err
	}
	if !ValidDAVName(c.Name) || c.Kind != davobj.Calendar && c.Kind != davobj.AddressBook {
		return c, ErrInvalidDAVName
	}
	err := s.write(ctx, func(tx *repos, _ *changes) error {
		return s.createCollection(ctx, tx, id, &c)
	})
	if err != nil {
		return c, err
	}
	return s.DAVCollection(ctx, id, c.Kind, c.Name)
}

func (s *Store) createCollection(ctx context.Context, tx *repos, id string, c *DAVCollection) error {
	n, err := tx.davCollections.Count(ctx, ablage.Eq("storage_id", id))
	if err != nil {
		return err
	}
	if n >= MaxDAVCollections {
		return ErrTooManyCollections
	}
	r := davCollectionRec{StorageID: id, Kind: c.Kind, Name: c.Name, DisplayName: c.DisplayName, Color: c.Color, Description: c.Description,
		Components: strings.Join(c.Components, ","), Created: sec(s.now())}
	err = tx.davCollections.Create(ctx, &r)
	if errors.Is(err, ablage.ErrDuplicate) {
		return ErrCollectionExists
	}
	c.ID = r.ID
	return err
}

// Default collections, created on first use.
const (
	DefaultCalendar    = "calendar"
	DefaultAddressBook = "contacts"
)

// EnsureDefaultDAVCollections creates the calendar "Calendar" and the
// address book "Contacts" for a mailbox that has no collection of that
// kind yet.
func (s *Store) EnsureDefaultDAVCollections(ctx context.Context, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	kinds, err := s.r.davCollections.Aggregate(ctx, ablage.Aggregation{Where: ablage.Eq("storage_id", id), GroupBy: []string{"kind"}})
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, k := range kinds {
		have[k["kind"].(string)] = true
	}
	if have[davobj.Calendar] && have[davobj.AddressBook] {
		return nil
	}
	return s.write(ctx, func(tx *repos, _ *changes) error {
		for _, d := range []DAVCollection{
			{Kind: davobj.Calendar, Name: DefaultCalendar, DisplayName: "Calendar"},
			{Kind: davobj.AddressBook, Name: DefaultAddressBook, DisplayName: "Contacts"},
		} {
			n, err := tx.davCollections.Count(ctx, ablage.And(ablage.Eq("storage_id", id), ablage.Eq("kind", d.Kind)))
			if err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			if err := s.createCollection(ctx, tx, id, &d); err != nil && !errors.Is(err, ErrCollectionExists) {
				return err
			}
		}
		return nil
	})
}

// DAVCollectionUpdate changes the properties of a collection; nil fields
// stay.
type DAVCollectionUpdate struct {
	DisplayName *string
	Color       *string
	Description *string
}

// UpdateDAVCollection changes a collection's properties.
func (s *Store) UpdateDAVCollection(ctx context.Context, id, coll string, u DAVCollectionUpdate) error {
	set := ablage.Row{}
	for _, f := range []struct {
		col string
		v   *string
	}{{"display_name", u.DisplayName}, {"color", u.Color}, {"description", u.Description}} {
		if f.v != nil {
			set[f.col] = *f.v
		}
	}
	return s.write(ctx, func(tx *repos, _ *changes) error {
		n, err := tx.davCollections.Count(ctx, ablage.And(ablage.Eq("id", coll), ablage.Eq("storage_id", id)))
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNoCollection
		}
		if len(set) == 0 {
			return nil
		}
		_, err = tx.davCollections.UpdateWhere(ctx, ablage.And(ablage.Eq("id", coll), ablage.Eq("storage_id", id)), set)
		return err
	})
}

// davBytes is what objects and deletion records count towards the quota.
func davBytes(ctx context.Context, tx *repos, objects, deletions ablage.Cond) (int64, error) {
	o, err := tx.davObjects.Aggregate(ctx, ablage.Aggregation{Where: objects, Values: []ablage.Agg{ablage.CountAll("n"), ablage.Sum("size", "bytes")}})
	if err != nil {
		return 0, err
	}
	d, err := tx.davDeletions.Count(ctx, deletions)
	if err != nil {
		return 0, err
	}
	return o[0]["bytes"].(int64) + DAVObjectOverhead*o[0]["n"].(int64) + DAVDeletionOverhead*d, nil
}

// DeleteDAVCollection deletes a collection with all its objects.
func (s *Store) DeleteDAVCollection(ctx context.Context, id, coll string) error {
	if err := checkID(id); err != nil {
		return err
	}
	if !validCollID(coll) {
		return ErrNoCollection
	}
	return s.write(ctx, func(tx *repos, ch *changes) error {
		bytes, err := davBytes(ctx, tx, ablage.And(ablage.Eq("collection_id", coll), ablage.Eq("storage_id", id)), ablage.Eq("collection_id", coll))
		if err != nil {
			return err
		}
		// Objects and deletion records go with it (cascade).
		n, err := tx.davCollections.DeleteWhere(ctx, ablage.And(ablage.Eq("id", coll), ablage.Eq("storage_id", id)))
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNoCollection
		}
		if err := addUsage(ctx, tx, id, -bytes, 0); err != nil {
			return err
		}
		return s.trashDir(ch, s.collectionDir(id, coll), id)
	})
}

// trashDir moves a directory to the trash in a write transaction (removed
// after commit, moved back on rollback).
func (s *Store) trashDir(ch *changes, dir, id string) error {
	gone := filepath.Join(s.dir, trashDir, id+"-"+randHex(8))
	if err := os.Rename(dir, gone); err == nil {
		ch.trashed = append(ch.trashed, rename{dir, gone})
		return syncDir(filepath.Dir(dir))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (r davObjectRec) object(epoch int64) DAVObject {
	return DAVObject{ID: r.ID, CollectionID: r.CollectionID, StorageID: r.StorageID, Name: r.Name, UID: r.UID, Component: r.Component,
		Size: r.Size, Start: r.StartAt, End: r.EndAt, Seq: r.Seq, Modified: local(r.Modified), file: r.File, epoch: epoch}
}

func (s *Store) findObjects(ctx context.Context, epoch int64, q ablage.Query) ([]DAVObject, error) {
	list, err := s.r.davObjects.Find(ctx, q)
	if err != nil {
		return nil, err
	}
	var out []DAVObject
	for _, r := range list {
		out = append(out, r.object(epoch))
	}
	return out, nil
}

// DAVObjects lists the objects of a collection by name. With start < end,
// only those whose span overlaps [start, end) (Unix seconds).
func (s *Store) DAVObjects(ctx context.Context, c DAVCollection, start, end int64) ([]DAVObject, error) {
	where := ablage.Eq("collection_id", c.ID)
	if start < end {
		where = ablage.And(where, ablage.Lt("start_at", end), ablage.Gte("end_at", start))
	}
	return s.findObjects(ctx, c.Epoch, ablage.Query{Where: where, OrderBy: []ablage.Order{ablage.Asc("name")}})
}

// DAVObject returns one object of a collection by name.
func (s *Store) DAVObject(ctx context.Context, c DAVCollection, name string) (DAVObject, error) {
	list, err := s.findObjects(ctx, c.Epoch, ablage.Query{Where: ablage.And(ablage.Eq("collection_id", c.ID), ablage.Eq("name", name))})
	if err != nil {
		return DAVObject{}, err
	}
	if len(list) == 0 {
		return DAVObject{}, ErrNoObject
	}
	return list[0], nil
}

// DAVObjectByUID returns the object of a collection with a UID.
func (s *Store) DAVObjectByUID(ctx context.Context, c DAVCollection, uid string) (DAVObject, error) {
	list, err := s.findObjects(ctx, c.Epoch, ablage.Query{Where: ablage.And(ablage.Eq("collection_id", c.ID), ablage.Eq("uid", uid))})
	if err != nil {
		return DAVObject{}, err
	}
	if len(list) == 0 {
		return DAVObject{}, ErrNoObject
	}
	return list[0], nil
}

// ReadDAVObject reads an object's data. An object replaced or deleted
// meanwhile answers ErrNoObject.
func (s *Store) ReadDAVObject(o DAVObject) ([]byte, error) {
	if !fileNameRe.MatchString(o.file) || checkID(o.StorageID) != nil || !validCollID(o.CollectionID) {
		return nil, ErrNoObject
	}
	b, err := os.ReadFile(filepath.Join(s.collectionDir(o.StorageID, o.CollectionID), o.file))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoObject
	}
	return b, err
}

// PutCondition are the conditions of a PUT or DELETE: If-Match ("*" or
// ETags) and If-None-Match ("*" or ETags), as given; empty means none.
type PutCondition struct {
	IfMatch     []string
	IfNoneMatch []string
}

func (pc PutCondition) holds(exists bool, etag string) bool {
	in := func(list []string) bool {
		for _, v := range list {
			if v == "*" && exists || exists && v == etag {
				return true
			}
		}
		return false
	}
	if len(pc.IfMatch) > 0 && !in(pc.IfMatch) {
		return false
	}
	if len(pc.IfNoneMatch) > 0 && in(pc.IfNoneMatch) {
		return false
	}
	return true
}

// PutDAVObject stores data (checked by the caller, described by info) as
// the object name of collection c, creating or replacing it. It returns
// the stored object and whether it was created.
func (s *Store) PutDAVObject(ctx context.Context, c DAVCollection, name string, data []byte, info davobj.Info, cond PutCondition) (DAVObject, bool, error) {
	var obj DAVObject
	if !ValidDAVName(name) {
		return obj, false, ErrInvalidDAVName
	}
	id := c.StorageID
	if err := checkID(id); err != nil {
		return obj, false, err
	}
	if err := s.ensure(ctx, id); err != nil {
		return obj, false, err
	}
	tmp, size, err := s.spoolBytes(data)
	if err != nil {
		return obj, false, err
	}
	defer os.Remove(tmp)
	quota := s.quota(id)
	created := false
	err = s.write(ctx, func(tx *repos, ch *changes) error {
		epoch, err := s.txEpoch(ctx, tx)
		if err != nil {
			return err
		}
		coll, err := tx.davCollections.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("id", c.ID), ablage.Eq("storage_id", id))})
		if errors.Is(err, ablage.ErrNotFound) {
			return ErrNoCollection
		} else if err != nil {
			return err
		}
		seq := coll.Seq
		old, err := tx.davObjects.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("collection_id", c.ID), ablage.Eq("name", name))})
		exists := err == nil
		if err != nil && !errors.Is(err, ablage.ErrNotFound) {
			return err
		}
		if !cond.holds(exists, old.object(epoch).ETag()) {
			return ErrPrecondition
		}
		other, err := tx.davObjects.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("collection_id", c.ID), ablage.Eq("uid", info.UID), ablage.Ne("name", name))})
		if err == nil {
			return &UIDConflictError{Name: other.Name}
		} else if !errors.Is(err, ablage.ErrNotFound) {
			return err
		}
		if !exists {
			n, err := tx.davObjects.Count(ctx, ablage.Eq("collection_id", c.ID))
			if err != nil {
				return err
			}
			if n >= MaxDAVObjects {
				return ErrTooManyObjects
			}
		}
		deleted, err := tx.davDeletions.Count(ctx, ablage.And(ablage.Eq("collection_id", c.ID), ablage.Eq("name", name)))
		if err != nil {
			return err
		}
		grow := size - old.Size - deleted*DAVDeletionOverhead
		if !exists {
			grow += DAVObjectOverhead
		}
		if quota > 0 && grow > 0 {
			used, err := usedBytes(ctx, tx, id)
			if err != nil {
				return err
			}
			if used+grow > quota {
				return ErrOverQuota
			}
		}
		file, err := s.placeIn(ch, s.collectionDir(id, c.ID), tmp)
		if err != nil {
			return err
		}
		seq++
		now := sec(s.now())
		o := davObjectRec{ID: old.ID, CollectionID: c.ID, StorageID: id, Name: name, UID: info.UID, Component: info.Component, Size: size,
			StartAt: info.Start, EndAt: info.End, File: file, Seq: seq, Modified: now}
		if exists {
			if err := tx.davObjects.Patch(ctx, old.ID, ablage.Row{"uid": info.UID, "component": info.Component, "size": size,
				"start_at": info.Start, "end_at": info.End, "file": file, "seq": seq, "modified": now}); err != nil {
				return err
			}
			if err := s.trashFile(ch, filepath.Join(s.collectionDir(id, c.ID), old.File), id); err != nil {
				return err
			}
		} else {
			if err := tx.davObjects.Create(ctx, &o); err != nil {
				return err
			}
			created = true
		}
		if _, err := tx.davDeletions.DeleteWhere(ctx, ablage.And(ablage.Eq("collection_id", c.ID), ablage.Eq("name", name))); err != nil {
			return err
		}
		if err := tx.davCollections.Patch(ctx, c.ID, ablage.Row{"seq": seq}); err != nil {
			return err
		}
		if err := addUsage(ctx, tx, id, grow, 0); err != nil {
			return err
		}
		obj = o.object(epoch)
		return nil
	})
	return obj, created, err
}

// DeleteDAVObject deletes an object (if cond holds) and records the
// deletion for sync.
func (s *Store) DeleteDAVObject(ctx context.Context, c DAVCollection, name string, cond PutCondition) error {
	id := c.StorageID
	if err := checkID(id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *repos, ch *changes) error {
		epoch, err := s.txEpoch(ctx, tx)
		if err != nil {
			return err
		}
		o, err := tx.davObjects.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("collection_id", c.ID), ablage.Eq("name", name), ablage.Eq("storage_id", id))})
		if errors.Is(err, ablage.ErrNotFound) {
			return ErrNoObject
		} else if err != nil {
			return err
		}
		if !cond.holds(true, o.object(epoch).ETag()) {
			return ErrPrecondition
		}
		coll, err := tx.davCollections.Get(ctx, c.ID)
		if err != nil {
			return err
		}
		seq := coll.Seq + 1
		if err := tx.davObjects.Delete(ctx, o.ID); err != nil {
			return err
		}
		if err := tx.davDeletions.UpsertOn(ctx, &davDeletionRec{CollectionID: c.ID, Name: name, Seq: seq, At: sec(s.now())}, "collection_id", "name"); err != nil {
			return err
		}
		if err := tx.davCollections.Patch(ctx, c.ID, ablage.Row{"seq": seq}); err != nil {
			return err
		}
		trimmed, err := trimDeletions(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		if err := addUsage(ctx, tx, id, -o.Size-DAVObjectOverhead+int64(1-trimmed)*DAVDeletionOverhead, 0); err != nil {
			return err
		}
		return s.trashFile(ch, filepath.Join(s.collectionDir(id, c.ID), o.File), id)
	})
}

// trimDeletions keeps at most maxDAVDeletions deletion records of a
// collection: the oldest go, and pruned_seq rises so that sync tokens from
// before them are refused. It returns how many went.
func trimDeletions(ctx context.Context, tx *repos, coll string) (int, error) {
	n, err := tx.davDeletions.Count(ctx, ablage.Eq("collection_id", coll))
	if err != nil {
		return 0, err
	}
	extra := int(n) - maxDAVDeletions
	if extra <= 0 {
		return 0, nil
	}
	oldest, err := tx.davDeletions.Find(ctx, ablage.Query{Where: ablage.Eq("collection_id", coll), OrderBy: []ablage.Order{ablage.Asc("seq")}, Limit: extra})
	if err != nil {
		return 0, err
	}
	upTo := oldest[len(oldest)-1].Seq
	if _, err := tx.davDeletions.DeleteWhere(ctx, ablage.And(ablage.Eq("collection_id", coll), ablage.Lte("seq", upTo))); err != nil {
		return 0, err
	}
	if _, err := tx.davCollections.UpdateWhere(ctx, ablage.And(ablage.Eq("id", coll), ablage.Lt("pruned_seq", upTo)), ablage.Row{"pruned_seq": upTo}); err != nil {
		return 0, err
	}
	return extra, nil
}

// DAVChanges returns what changed in a collection since a sync token:
// objects changed and names deleted. An empty token gives every object.
// It refuses tokens of another epoch or collection, newer than the
// collection, or older than its kept deletions (ErrInvalidSyncToken).
func (s *Store) DAVChanges(ctx context.Context, c DAVCollection, token string) (changed []DAVObject, deleted []string, err error) {
	if token == "" {
		changed, err = s.DAVObjects(ctx, c, 0, 0)
		return changed, nil, err
	}
	epoch, coll, seq, ok := ParseSyncToken(token)
	if !ok || epoch != c.Epoch || coll != collToken(c.ID) || seq > c.Seq || seq < c.PrunedSeq {
		return nil, nil, ErrInvalidSyncToken
	}
	err = s.read(ctx, func(tx *repos) error {
		list, err := tx.davObjects.Find(ctx, ablage.Query{Where: ablage.And(ablage.Eq("collection_id", c.ID), ablage.Gt("seq", seq)), OrderBy: []ablage.Order{ablage.Asc("seq")}})
		if err != nil {
			return err
		}
		for _, r := range list {
			changed = append(changed, r.object(c.Epoch))
		}
		dels, err := tx.davDeletions.Find(ctx, ablage.Query{Where: ablage.And(ablage.Eq("collection_id", c.ID), ablage.Gt("seq", seq)), OrderBy: []ablage.Order{ablage.Asc("seq")}})
		if err != nil {
			return err
		}
		for _, d := range dels {
			deleted = append(deleted, d.Name)
		}
		return nil
	})
	return changed, deleted, err
}

// PruneDAVDeletions forgets deletions older than DAVDeletionRetention; sync
// tokens from before them are refused from then on.
func (s *Store) PruneDAVDeletions(ctx context.Context) error {
	cutoff := sec(s.now().Add(-DAVDeletionRetention))
	return s.write(ctx, func(tx *repos, _ *changes) error {
		old := ablage.Lt("at", cutoff)
		rows, err := tx.davDeletions.Aggregate(ctx, ablage.Aggregation{Where: old, GroupBy: []string{"collection_id"},
			Values: []ablage.Agg{ablage.CountAll("n"), ablage.Max("seq", "seq")}})
		if err != nil || len(rows) == 0 {
			return err
		}
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r["collection_id"].(string)
		}
		colls, err := tx.davCollections.Find(ctx, ablage.Query{Where: ablage.In("id", ids...)})
		if err != nil {
			return err
		}
		owner := map[string]string{}
		for _, c := range colls {
			owner[c.ID] = c.StorageID
		}
		freed := map[string]int64{}
		for _, r := range rows {
			coll, upTo := r["collection_id"].(string), r["seq"].(int64)
			if _, err := tx.davCollections.UpdateWhere(ctx, ablage.And(ablage.Eq("id", coll), ablage.Lt("pruned_seq", upTo)), ablage.Row{"pruned_seq": upTo}); err != nil {
				return err
			}
			freed[owner[coll]] += r["n"].(int64)
		}
		if _, err := tx.davDeletions.DeleteWhere(ctx, old); err != nil {
			return err
		}
		for sid, n := range freed {
			if err := addUsage(ctx, tx, sid, -n*DAVDeletionOverhead, 0); err != nil {
				return err
			}
		}
		return nil
	})
}

// DAVUsage is what a mailbox's calendars and address books count towards
// its quota: their size and the overhead of objects and deletion records.
func (s *Store) DAVUsage(ctx context.Context, id string) (int64, error) {
	var n int64
	err := s.read(ctx, func(tx *repos) error {
		colls, err := tx.davCollections.Find(ctx, ablage.Query{Where: ablage.Eq("storage_id", id)})
		if err != nil {
			return err
		}
		ids := make([]string, len(colls))
		for i, c := range colls {
			ids[i] = c.ID
		}
		n, err = davBytes(ctx, tx, ablage.Eq("storage_id", id), ablage.In("collection_id", ids...))
		return err
	})
	return n, err
}

// spoolBytes writes data to a temporary file and fsyncs it.
func (s *Store) spoolBytes(data []byte) (string, int64, error) {
	f, err := os.CreateTemp(s.TempDir(), "dav-*")
	if err != nil {
		return "", 0, err
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return "", 0, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(name)
		return "", 0, err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", 0, err
	}
	return name, int64(len(data)), nil
}

// placeIn moves a spooled file into dir (created as needed, with its
// parents fsynced) under a new name and fsyncs dir. Callers hold the
// write lock.
func (s *Store) placeIn(ch *changes, dir, tmp string) (string, error) {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		for _, d := range []string{filepath.Dir(filepath.Dir(dir)), filepath.Dir(dir), dir} {
			if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			if err := syncDir(filepath.Dir(d)); err != nil {
				return "", err
			}
		}
	}
	name := fmt.Sprintf("%d.%s", s.now().UnixNano(), randHex(8))
	dst := filepath.Join(dir, name)
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	ch.placed = append(ch.placed, dst)
	if err := syncDir(dir); err != nil {
		return "", err
	}
	return name, nil
}

// trashFile moves a file to the trash in a write transaction.
func (s *Store) trashFile(ch *changes, file, id string) error {
	to := filepath.Join(s.dir, trashDir, id+".dav."+randHex(8))
	if err := os.Rename(file, to); err == nil {
		ch.trashed = append(ch.trashed, rename{file, to})
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// deleteDAV removes all DAV data of a mailbox in DeleteMailbox's
// transaction.
func (s *Store) deleteDAV(ctx context.Context, tx *repos, ch *changes, id string) error {
	// Objects and deletion records go with their collections (cascade).
	if _, err := tx.davObjects.DeleteWhere(ctx, ablage.Eq("storage_id", id)); err != nil {
		return err
	}
	if _, err := tx.davCollections.DeleteWhere(ctx, ablage.Eq("storage_id", id)); err != nil {
		return err
	}
	return s.trashDir(ch, s.davDir(id), id)
}

// davReport is what reconcileDAV found.
type davReport struct {
	missing, indexed, stray int
}

// reconcileDAV brings the DAV index and files in line, in Reconcile's
// transaction: rows without a file are dropped. Files without a row are
// removed (a write that crashed before its commit was never acknowledged)
// except after a restore, when they are indexed into their collection
// under the name their UID gives, if they are a valid object whose UID is
// not there yet. It sets a new epoch when anything differed or restored is set.
func (s *Store) reconcileDAV(ctx context.Context, tx *repos, restored bool) (davReport, bool, error) {
	var rep davReport
	objs, err := tx.davObjects.Find(ctx, ablage.Query{})
	if err != nil {
		return rep, false, err
	}
	var missing []string
	known := map[string]bool{}
	for _, o := range objs {
		p := filepath.Join(s.collectionDir(o.StorageID, o.CollectionID), o.File)
		known[p] = true
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, o.ID)
		}
	}
	if len(missing) > 0 {
		if _, err := tx.davObjects.DeleteWhere(ctx, ablage.In("id", missing...)); err != nil {
			return rep, false, err
		}
	}
	rep.missing = len(missing)

	sids, err := os.ReadDir(filepath.Join(s.dir, davDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return rep, false, err
	}
	for _, sd := range sids {
		if !sd.IsDir() || checkID(sd.Name()) != nil {
			continue
		}
		sid := sd.Name()
		colls, err := os.ReadDir(s.davDir(sid))
		if err != nil {
			return rep, false, err
		}
		for _, cd := range colls {
			collID := cd.Name()
			if !cd.IsDir() || !validCollID(collID) {
				continue
			}
			coll, err := tx.davCollections.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("id", collID), ablage.Eq("storage_id", sid))})
			haveColl := err == nil
			if err != nil && !errors.Is(err, ablage.ErrNotFound) {
				return rep, false, err
			}
			dir := s.collectionDir(sid, collID)
			files, err := os.ReadDir(dir)
			if err != nil {
				return rep, false, err
			}
			for _, f := range files {
				p := filepath.Join(dir, f.Name())
				if known[p] || !f.Type().IsRegular() {
					continue
				}
				ok := false
				if restored && haveColl {
					// Only after a restore: the files were written after the
					// copy of the index and acknowledged to clients. Without
					// a restore they come from a write that crashed before
					// its commit, which the client will repeat.
					ok, err = s.indexStray(ctx, tx, coll, p, f.Name())
				}
				if err != nil {
					return rep, false, err
				}
				if ok {
					rep.indexed++
				} else {
					rep.stray++
					if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
						return rep, false, err
					}
				}
			}
		}
	}
	if rep.missing > 0 || rep.indexed > 0 || restored {
		if _, err := s.newDAVEpoch(ctx, tx); err != nil {
			return rep, false, err
		}
		return rep, true, nil
	}
	return rep, false, nil
}

var simpleUIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,100}$`)

// strayName is the name a restored object gets: its UID when that is a
// simple name (what most programs use), else a hash of it.
func strayName(uid, ext string) string {
	if simpleUIDRe.MatchString(uid) {
		return uid + ext
	}
	sum := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(sum[:16]) + ext
}

// indexStray indexes a file found without a row, if it can be.
func (s *Store) indexStray(ctx context.Context, tx *repos, coll davCollectionRec, path, file string) (bool, error) {
	if !fileNameRe.MatchString(file) {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var info davobj.Info
	ext := ".ics"
	if coll.Kind == davobj.Calendar {
		var allowed []string
		if coll.Components != "" {
			allowed = strings.Split(coll.Components, ",")
		}
		info, _, err = davobj.ParseCalendar(data, allowed)
	} else {
		info, _, err = davobj.ParseCard(data)
		ext = ".vcf"
	}
	if err != nil {
		return false, nil
	}
	if n, err := tx.davObjects.Count(ctx, ablage.And(ablage.Eq("collection_id", coll.ID), ablage.Eq("uid", info.UID))); err != nil {
		return false, err
	} else if n > 0 {
		return false, nil
	}
	name := strayName(info.UID, ext)
	if taken, err := tx.davObjects.Count(ctx, ablage.And(ablage.Eq("collection_id", coll.ID), ablage.Eq("name", name))); err != nil {
		return false, err
	} else if taken > 0 {
		name = "restored-" + file + ext
	}
	if err := tx.davCollections.Patch(ctx, coll.ID, ablage.Row{"seq": ablage.Incr(1)}); err != nil {
		return false, err
	}
	c, err := tx.davCollections.Get(ctx, coll.ID)
	if err != nil {
		return false, err
	}
	err = tx.davObjects.Create(ctx, &davObjectRec{CollectionID: coll.ID, StorageID: coll.StorageID, Name: name, UID: info.UID, Component: info.Component,
		Size: int64(len(data)), StartAt: info.Start, EndAt: info.End, File: file, Seq: c.Seq, Modified: sec(s.now())})
	return err == nil, err
}

// ExportDAVCollection writes collection c as one .ics file (each time zone
// once) or one .vcf file. Objects deleted meanwhile are left out.
func (s *Store) ExportDAVCollection(ctx context.Context, c DAVCollection, w io.Writer) error {
	objs, err := s.DAVObjects(ctx, c, 0, 0)
	if err != nil {
		return err
	}
	var cw *davobj.CalendarWriter
	if c.Kind == davobj.Calendar {
		name := c.DisplayName
		if name == "" {
			name = c.Name
		}
		cw = davobj.NewCalendarWriter(w, name)
	}
	for _, o := range objs {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := s.ReadDAVObject(o)
		if errors.Is(err, ErrNoObject) {
			continue
		}
		if err != nil {
			return err
		}
		if cw != nil {
			if _, err := cw.Add(data); err != nil {
				return err
			}
		} else if err := davobj.WriteCard(w, data); err != nil {
			return err
		}
	}
	if cw != nil {
		return cw.Close()
	}
	return nil
}
