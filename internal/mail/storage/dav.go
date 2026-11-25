package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
// Collection ids are AUTOINCREMENT, never reused: a calendar deleted and
// created again never accepts the old one's ETags or sync tokens.
//
// DAV data counts towards the mailbox's quota: its bytes are part of
// mailboxes.used_bytes (not of messages).

const davSchema = `
CREATE TABLE IF NOT EXISTS dav_collections (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	storage_id   TEXT NOT NULL,
	kind         TEXT NOT NULL,
	name         TEXT NOT NULL,
	display_name TEXT NOT NULL DEFAULT '',
	color        TEXT NOT NULL DEFAULT '',
	description  TEXT NOT NULL DEFAULT '',
	components   TEXT NOT NULL DEFAULT '',
	seq          INTEGER NOT NULL DEFAULT 0,
	pruned_seq   INTEGER NOT NULL DEFAULT 0,
	created      INTEGER NOT NULL,
	UNIQUE (storage_id, kind, name)
);
CREATE TABLE IF NOT EXISTS dav_objects (
	id            INTEGER PRIMARY KEY,
	collection_id INTEGER NOT NULL REFERENCES dav_collections(id) ON DELETE CASCADE,
	storage_id    TEXT NOT NULL,
	name          TEXT NOT NULL,
	uid           TEXT NOT NULL,
	component     TEXT NOT NULL,
	size          INTEGER NOT NULL,
	start_at      INTEGER NOT NULL,
	end_at        INTEGER NOT NULL,
	file          TEXT NOT NULL,
	seq           INTEGER NOT NULL,
	modified      INTEGER NOT NULL,
	UNIQUE (collection_id, name),
	UNIQUE (collection_id, uid)
);
CREATE INDEX IF NOT EXISTS dav_objects_seq ON dav_objects(collection_id, seq);
CREATE INDEX IF NOT EXISTS dav_objects_mailbox ON dav_objects(storage_id);
CREATE TABLE IF NOT EXISTS dav_deletions (
	collection_id INTEGER NOT NULL REFERENCES dav_collections(id) ON DELETE CASCADE,
	name          TEXT NOT NULL,
	seq           INTEGER NOT NULL,
	at            INTEGER NOT NULL,
	PRIMARY KEY (collection_id, name)
);
CREATE INDEX IF NOT EXISTS dav_deletions_at ON dav_deletions(at);
`

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
)

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
	ID          int64
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
func SyncToken(epoch, collection, seq int64) string {
	return fmt.Sprintf("urn:ownpanel:sync:%x:%x:%x", epoch, collection, seq)
}

// ParseSyncToken reads a token of SyncToken.
func ParseSyncToken(tok string) (epoch, collection, seq int64, ok bool) {
	rest, found := strings.CutPrefix(tok, "urn:ownpanel:sync:")
	if !found {
		return 0, 0, 0, false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var v [3]int64
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 16, 64)
		if err != nil || n < 0 {
			return 0, 0, 0, false
		}
		v[i] = n
	}
	return v[0], v[1], v[2], true
}

// DAVObject is a calendar object or a vCard.
type DAVObject struct {
	ID           int64
	CollectionID int64
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
func (o DAVObject) ETag() string { return fmt.Sprintf("%x-%x-%x", o.epoch, o.CollectionID, o.Seq) }

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

func (s *Store) collectionDir(id string, coll int64) string {
	return filepath.Join(s.davDir(id), strconv.FormatInt(coll, 10))
}

// davEpoch reads the epoch, creating it the first time.
func (s *Store) davEpoch(ctx context.Context) (int64, error) {
	var e int64
	err := s.rdb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'dav_epoch'`).Scan(&e)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.write(ctx, func(tx *sql.Tx, _ *changes) error {
			var err error
			e, err = s.txEpoch(tx)
			return err
		})
	}
	return e, err
}

func (s *Store) txEpoch(tx *sql.Tx) (int64, error) {
	var e int64
	err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'dav_epoch'`).Scan(&e)
	if errors.Is(err, sql.ErrNoRows) {
		return s.newDAVEpoch(tx)
	}
	return e, err
}

// newDAVEpoch sets an epoch never used before: larger than the last one
// and at least the current time in seconds, so a restored database still
// gets a new one.
func (s *Store) newDAVEpoch(tx *sql.Tx) (int64, error) {
	var last int64
	err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'dav_epoch'`).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	v := max(last+1, s.now().Unix())
	_, err = tx.Exec(`INSERT INTO meta (key, value) VALUES ('dav_epoch', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, v)
	return v, err
}

const collectionColumns = `c.id, c.storage_id, c.kind, c.name, c.display_name, c.color, c.description, c.components, c.seq, c.pruned_seq, c.created`

type scanner interface{ Scan(...any) error }

func scanCollection(r scanner, c *DAVCollection, extra ...any) error {
	var comps string
	var created int64
	dst := append([]any{&c.ID, &c.StorageID, &c.Kind, &c.Name, &c.DisplayName, &c.Color, &c.Description, &comps, &c.Seq, &c.PrunedSeq, &created}, extra...)
	if err := r.Scan(dst...); err != nil {
		return err
	}
	if comps != "" {
		c.Components = strings.Split(comps, ",")
	}
	c.Created = time.Unix(created, 0)
	return nil
}

// DAVCollections lists a mailbox's collections of a kind ("" for all), by
// name, with their object counts and sizes.
func (s *Store) DAVCollections(ctx context.Context, id, kind string) ([]DAVCollection, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	epoch, err := s.davEpoch(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.rdb.QueryContext(ctx, `SELECT `+collectionColumns+`,
		(SELECT count(*) FROM dav_objects o WHERE o.collection_id = c.id),
		(SELECT coalesce(sum(size), 0) FROM dav_objects o WHERE o.collection_id = c.id)
		FROM dav_collections c WHERE c.storage_id = ? AND (? = '' OR c.kind = ?) ORDER BY c.kind, c.name`, id, kind, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DAVCollection
	for rows.Next() {
		var c DAVCollection
		if err := scanCollection(rows, &c, &c.Objects, &c.Bytes); err != nil {
			return nil, err
		}
		c.Epoch = epoch
		out = append(out, c)
	}
	return out, rows.Err()
}

// DAVCollection returns one collection of a mailbox.
func (s *Store) DAVCollection(ctx context.Context, id, kind, name string) (DAVCollection, error) {
	var c DAVCollection
	if err := checkID(id); err != nil {
		return c, err
	}
	epoch, err := s.davEpoch(ctx)
	if err != nil {
		return c, err
	}
	err = scanCollection(s.rdb.QueryRowContext(ctx, `SELECT `+collectionColumns+` FROM dav_collections c WHERE c.storage_id = ? AND c.kind = ? AND c.name = ?`, id, kind, name), &c)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNoCollection
	}
	c.Epoch = epoch
	return c, err
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
	err := s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		return s.createCollection(tx, id, &c)
	})
	if err != nil {
		return c, err
	}
	return s.DAVCollection(ctx, id, c.Kind, c.Name)
}

func (s *Store) createCollection(tx *sql.Tx, id string, c *DAVCollection) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM dav_collections WHERE storage_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n >= MaxDAVCollections {
		return ErrTooManyCollections
	}
	var exists int
	if err := tx.QueryRow(`SELECT count(*) FROM dav_collections WHERE storage_id = ? AND kind = ? AND name = ?`, id, c.Kind, c.Name).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return ErrCollectionExists
	}
	res, err := tx.Exec(`INSERT INTO dav_collections (storage_id, kind, name, display_name, color, description, components, created) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, c.Kind, c.Name, c.DisplayName, c.Color, c.Description, strings.Join(c.Components, ","), s.now().Unix())
	if err != nil {
		return err
	}
	c.ID, err = res.LastInsertId()
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
	var cals, books int
	if err := s.rdb.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM dav_collections WHERE storage_id = ? AND kind = 'calendar'),
		(SELECT count(*) FROM dav_collections WHERE storage_id = ? AND kind = 'addressbook')`, id, id).Scan(&cals, &books); err != nil {
		return err
	}
	if cals > 0 && books > 0 {
		return nil
	}
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		for _, d := range []DAVCollection{
			{Kind: davobj.Calendar, Name: DefaultCalendar, DisplayName: "Calendar"},
			{Kind: davobj.AddressBook, Name: DefaultAddressBook, DisplayName: "Contacts"},
		} {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM dav_collections WHERE storage_id = ? AND kind = ?`, id, d.Kind).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			if err := s.createCollection(tx, id, &d); err != nil && !errors.Is(err, ErrCollectionExists) {
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
func (s *Store) UpdateDAVCollection(ctx context.Context, id string, coll int64, u DAVCollectionUpdate) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		for _, f := range []struct {
			col string
			v   *string
		}{{"display_name", u.DisplayName}, {"color", u.Color}, {"description", u.Description}} {
			if f.v == nil {
				continue
			}
			res, err := tx.Exec(`UPDATE dav_collections SET `+f.col+` = ? WHERE id = ? AND storage_id = ?`, *f.v, coll, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrNoCollection
			}
		}
		return nil
	})
}

// DeleteDAVCollection deletes a collection with all its objects.
func (s *Store) DeleteDAVCollection(ctx context.Context, id string, coll int64) error {
	if err := checkID(id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		var bytes int64
		if err := tx.QueryRow(`SELECT coalesce(sum(size), 0) FROM dav_objects WHERE collection_id = ? AND storage_id = ?`, coll, id).Scan(&bytes); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM dav_collections WHERE id = ? AND storage_id = ?`, coll, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoCollection
		}
		if _, err := tx.Exec(`DELETE FROM dav_objects WHERE collection_id = ?`, coll); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM dav_deletions WHERE collection_id = ?`, coll); err != nil {
			return err
		}
		if err := addUsage(tx, id, -bytes, 0); err != nil {
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

const objectColumns = `id, collection_id, storage_id, name, uid, component, size, start_at, end_at, seq, modified, file`

func scanObject(r scanner, o *DAVObject) error {
	var mod int64
	if err := r.Scan(&o.ID, &o.CollectionID, &o.StorageID, &o.Name, &o.UID, &o.Component, &o.Size, &o.Start, &o.End, &o.Seq, &mod, &o.file); err != nil {
		return err
	}
	o.Modified = time.Unix(mod, 0)
	return nil
}

func (s *Store) queryObjects(ctx context.Context, epoch int64, q string, args ...any) ([]DAVObject, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT `+objectColumns+` FROM dav_objects WHERE `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DAVObject
	for rows.Next() {
		var o DAVObject
		if err := scanObject(rows, &o); err != nil {
			return nil, err
		}
		o.epoch = epoch
		out = append(out, o)
	}
	return out, rows.Err()
}

// DAVObjects lists the objects of a collection by name. With start < end,
// only those whose span overlaps [start, end) (Unix seconds).
func (s *Store) DAVObjects(ctx context.Context, c DAVCollection, start, end int64) ([]DAVObject, error) {
	if start < end {
		return s.queryObjects(ctx, c.Epoch, `collection_id = ? AND start_at < ? AND end_at >= ? ORDER BY name`, c.ID, end, start)
	}
	return s.queryObjects(ctx, c.Epoch, `collection_id = ? ORDER BY name`, c.ID)
}

// DAVObject returns one object of a collection by name.
func (s *Store) DAVObject(ctx context.Context, c DAVCollection, name string) (DAVObject, error) {
	list, err := s.queryObjects(ctx, c.Epoch, `collection_id = ? AND name = ?`, c.ID, name)
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
	list, err := s.queryObjects(ctx, c.Epoch, `collection_id = ? AND uid = ?`, c.ID, uid)
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
	if !fileNameRe.MatchString(o.file) || checkID(o.StorageID) != nil {
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
	err = s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		epoch, err := s.txEpoch(tx)
		if err != nil {
			return err
		}
		var seq int64
		if err := tx.QueryRow(`SELECT seq FROM dav_collections WHERE id = ? AND storage_id = ?`, c.ID, id).Scan(&seq); errors.Is(err, sql.ErrNoRows) {
			return ErrNoCollection
		} else if err != nil {
			return err
		}
		var old DAVObject
		err = scanObject(tx.QueryRow(`SELECT `+objectColumns+` FROM dav_objects WHERE collection_id = ? AND name = ?`, c.ID, name), &old)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		old.epoch = epoch
		if !cond.holds(exists, old.ETag()) {
			return ErrPrecondition
		}
		var other string
		err = tx.QueryRow(`SELECT name FROM dav_objects WHERE collection_id = ? AND uid = ? AND name <> ?`, c.ID, info.UID, name).Scan(&other)
		if err == nil {
			return &UIDConflictError{Name: other}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !exists {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM dav_objects WHERE collection_id = ?`, c.ID).Scan(&n); err != nil {
				return err
			}
			if n >= MaxDAVObjects {
				return ErrTooManyObjects
			}
		}
		if grow := size - old.Size; quota > 0 && grow > 0 {
			var used int64
			if err := tx.QueryRow(`SELECT used_bytes FROM mailboxes WHERE storage_id = ?`, id).Scan(&used); err != nil {
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
		now := s.now().Unix()
		if exists {
			if _, err := tx.Exec(`UPDATE dav_objects SET uid = ?, component = ?, size = ?, start_at = ?, end_at = ?, file = ?, seq = ?, modified = ? WHERE id = ?`,
				info.UID, info.Component, size, info.Start, info.End, file, seq, now, old.ID); err != nil {
				return err
			}
			if err := s.trashFile(ch, filepath.Join(s.collectionDir(id, c.ID), old.file), id); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(`INSERT INTO dav_objects (collection_id, storage_id, name, uid, component, size, start_at, end_at, file, seq, modified) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.ID, id, name, info.UID, info.Component, size, info.Start, info.End, file, seq, now); err != nil {
				return err
			}
			created = true
		}
		if _, err := tx.Exec(`DELETE FROM dav_deletions WHERE collection_id = ? AND name = ?`, c.ID, name); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE dav_collections SET seq = ? WHERE id = ?`, seq, c.ID); err != nil {
			return err
		}
		if err := addUsage(tx, id, size-old.Size, 0); err != nil {
			return err
		}
		obj = DAVObject{CollectionID: c.ID, StorageID: id, Name: name, UID: info.UID, Component: info.Component, Size: size,
			Start: info.Start, End: info.End, Seq: seq, Modified: time.Unix(now, 0), file: file, epoch: epoch}
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
	return s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		epoch, err := s.txEpoch(tx)
		if err != nil {
			return err
		}
		var o DAVObject
		err = scanObject(tx.QueryRow(`SELECT `+objectColumns+` FROM dav_objects WHERE collection_id = ? AND name = ? AND storage_id = ?`, c.ID, name, id), &o)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoObject
		} else if err != nil {
			return err
		}
		o.epoch = epoch
		if !cond.holds(true, o.ETag()) {
			return ErrPrecondition
		}
		var seq int64
		if err := tx.QueryRow(`SELECT seq FROM dav_collections WHERE id = ?`, c.ID).Scan(&seq); err != nil {
			return err
		}
		seq++
		if _, err := tx.Exec(`DELETE FROM dav_objects WHERE id = ?`, o.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO dav_deletions (collection_id, name, seq, at) VALUES (?, ?, ?, ?)
			ON CONFLICT (collection_id, name) DO UPDATE SET seq = excluded.seq, at = excluded.at`, c.ID, name, seq, s.now().Unix()); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE dav_collections SET seq = ? WHERE id = ?`, seq, c.ID); err != nil {
			return err
		}
		if err := addUsage(tx, id, -o.Size, 0); err != nil {
			return err
		}
		return s.trashFile(ch, filepath.Join(s.collectionDir(id, c.ID), o.file), id)
	})
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
	if !ok || epoch != c.Epoch || coll != c.ID || seq > c.Seq || seq < c.PrunedSeq {
		return nil, nil, ErrInvalidSyncToken
	}
	changed, err = s.queryObjects(ctx, c.Epoch, `collection_id = ? AND seq > ? ORDER BY seq`, c.ID, seq)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.rdb.QueryContext(ctx, `SELECT name FROM dav_deletions WHERE collection_id = ? AND seq > ? ORDER BY seq`, c.ID, seq)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, nil, err
		}
		deleted = append(deleted, n)
	}
	return changed, deleted, rows.Err()
}

// PruneDAVDeletions forgets deletions older than DAVDeletionRetention; sync
// tokens from before them are refused from then on.
func (s *Store) PruneDAVDeletions(ctx context.Context) error {
	cutoff := s.now().Add(-DAVDeletionRetention).Unix()
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		if _, err := tx.Exec(`UPDATE dav_collections SET pruned_seq = max(pruned_seq,
			(SELECT max(seq) FROM dav_deletions d WHERE d.collection_id = dav_collections.id AND d.at < ?))
			WHERE id IN (SELECT collection_id FROM dav_deletions WHERE at < ?)`, cutoff, cutoff); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM dav_deletions WHERE at < ?`, cutoff)
		return err
	})
}

// DAVUsage is the size of a mailbox's calendars and address books.
func (s *Store) DAVUsage(ctx context.Context, id string) (int64, error) {
	var n int64
	err := s.rdb.QueryRowContext(ctx, `SELECT coalesce(sum(size), 0) FROM dav_objects WHERE storage_id = ?`, id).Scan(&n)
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
func (s *Store) deleteDAV(tx *sql.Tx, ch *changes, id string) error {
	for _, q := range []string{
		`DELETE FROM dav_deletions WHERE collection_id IN (SELECT id FROM dav_collections WHERE storage_id = ?)`,
		`DELETE FROM dav_objects WHERE storage_id = ?`,
		`DELETE FROM dav_collections WHERE storage_id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return s.trashDir(ch, s.davDir(id), id)
}

// davReport is what reconcileDAV found.
type davReport struct {
	missing, indexed, stray int
}

// reconcileDAV brings the DAV index and files in line, in Reconcile's
// transaction: rows without a file are dropped, files without a row are
// indexed into their collection when they are a valid object whose UID is
// not there yet, other files are removed (never acknowledged to a
// client). It sets a new epoch when anything differed or restored is set.
func (s *Store) reconcileDAV(tx *sql.Tx, restored bool) (davReport, bool, error) {
	var rep davReport
	type row struct {
		id, coll int64
		sid      string
		file     string
	}
	rows, err := tx.Query(`SELECT id, collection_id, storage_id, file FROM dav_objects`)
	if err != nil {
		return rep, false, err
	}
	var missing []row
	known := map[string]bool{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.coll, &r.sid, &r.file); err != nil {
			rows.Close()
			return rep, false, err
		}
		p := filepath.Join(s.collectionDir(r.sid, r.coll), r.file)
		known[p] = true
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, false, err
	}
	for _, r := range missing {
		if _, err := tx.Exec(`DELETE FROM dav_objects WHERE id = ?`, r.id); err != nil {
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
			collID, err := strconv.ParseInt(cd.Name(), 10, 64)
			if !cd.IsDir() || err != nil {
				continue
			}
			var kind, comps string
			err = tx.QueryRow(`SELECT kind, components FROM dav_collections WHERE id = ? AND storage_id = ?`, collID, sid).Scan(&kind, &comps)
			haveColl := err == nil
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
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
				ok, err := s.indexStray(tx, haveColl, sid, collID, kind, comps, p, f.Name())
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
		if _, err := s.newDAVEpoch(tx); err != nil {
			return rep, false, err
		}
		return rep, true, nil
	}
	return rep, false, nil
}

// indexStray indexes a file found without a row, if it can be.
func (s *Store) indexStray(tx *sql.Tx, haveColl bool, sid string, coll int64, kind, comps, path, file string) (bool, error) {
	if !haveColl || !fileNameRe.MatchString(file) {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var info davobj.Info
	ext := ".ics"
	if kind == davobj.Calendar {
		var allowed []string
		if comps != "" {
			allowed = strings.Split(comps, ",")
		}
		info, _, err = davobj.ParseCalendar(data, allowed)
	} else {
		info, _, err = davobj.ParseCard(data)
		ext = ".vcf"
	}
	if err != nil {
		return false, nil
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM dav_objects WHERE collection_id = ? AND uid = ?`, coll, info.UID).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	name := "restored-" + file + ext
	var seq int64
	if err := tx.QueryRow(`UPDATE dav_collections SET seq = seq + 1 WHERE id = ? RETURNING seq`, coll).Scan(&seq); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO dav_objects (collection_id, storage_id, name, uid, component, size, start_at, end_at, file, seq, modified) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		coll, sid, name, info.UID, info.Component, len(data), info.Start, info.End, file, seq, s.now().Unix()); err != nil {
		return false, err
	}
	return true, nil
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
