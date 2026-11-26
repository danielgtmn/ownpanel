package storage

import (
	"fmt"
	"time"

	"github.com/danielgtmn/ablage"
)

// The rows of mail.db, one struct per table, kept with ablage repositories.
// Ids are strings: rows from before ablage keep their integer id as text
// ("42"), new rows get ablage ids. Counters clients or the panel see (IMAP
// UIDs and UIDVALIDITY, DAV sequence numbers, the event cursor) are integer
// fields of their own. Times that were Unix seconds before ablage are still
// stored to the second (sec), so comparisons behave as they did.

// metaRow is a named counter: uid_validity, dav_epoch, event_seq.
type metaRow struct {
	ID    string `db:"id,id"`
	Value int64  `db:"value"`
}

// mailboxRow exists once a mailbox stored anything; its id is the storage id.
type mailboxRow struct {
	ID        string    `db:"id,id"`
	Created   time.Time `db:"created"`
	UsedBytes int64     `db:"used_bytes"`
	Messages  int64     `db:"messages"`
}

type folderRec struct {
	ID          string `db:"id,id"`
	StorageID   string `db:"storage_id,ref=mailboxes,cascade"`
	Name        string `db:"name"`
	SpecialUse  string `db:"special_use"`
	UIDValidity int64  `db:"uid_validity"`
	UIDNext     int64  `db:"uid_next"`
	Subscribed  bool   `db:"subscribed"`
}

// messageRec is one message in a folder. The IMAP system flags are bool
// fields, keywords a JSON list (nil when there are none), so counts like
// "unseen" are plain conditions.
type messageRec struct {
	ID           string    `db:"id,id"`
	FolderID     string    `db:"folder_id,ref=folders,cascade"`
	StorageID    string    `db:"storage_id"`
	UID          int64     `db:"uid"`
	File         string    `db:"file"`
	Size         int64     `db:"size"`
	InternalDate time.Time `db:"internal_date"`
	Seen         bool      `db:"seen"`
	Answered     bool      `db:"answered"`
	Flagged      bool      `db:"flagged"`
	Deleted      bool      `db:"deleted"`
	Draft        bool      `db:"draft"`
	Keywords     []string  `db:"keywords"`
	MsgID        string    `db:"msgid"`
}

// sharedSeenRec: reader (a storage id) has seen the message in a shared
// mailbox.
type sharedSeenRec struct {
	ID        string `db:"id,id"`
	MessageID string `db:"message_id,ref=messages,cascade"`
	Reader    string `db:"reader,index"`
}

type bindingRec struct {
	ID      string    `db:"id,id"` // storage id
	Address string    `db:"address"`
	Bound   time.Time `db:"bound"`
}

type blockRec struct {
	ID      string    `db:"id,id"` // storage id
	Blocked bool      `db:"blocked"`
	Reason  string    `db:"reason"`
	At      time.Time `db:"at"`
}

// eventRec: Seq is the panel's cursor (config.Event.ID), allocated from the
// meta counter event_seq, so it never goes back, also after pruning.
type eventRec struct {
	ID        string    `db:"id,id"`
	Seq       int64     `db:"seq,unique"`
	At        time.Time `db:"at,index"`
	Kind      string    `db:"kind"`
	StorageID string    `db:"storage_id"`
	Address   string    `db:"address"`
	Detail    string    `db:"detail"`
	Account   string    `db:"account"` // config.AccountChange as JSON, "" for none
}

type queueRec struct {
	ID        string    `db:"id,id"`
	File      string    `db:"file,unique"`
	Sender    string    `db:"sender"`
	StorageID string    `db:"storage_id"`
	Created   time.Time `db:"created"`
	Size      int64     `db:"size"`
}

// queueRcptRec: Pos keeps the order of the recipients of a message.
type queueRcptRec struct {
	ID        string    `db:"id,id"`
	MessageID string    `db:"message_id,ref=queue,cascade"`
	Pos       int64     `db:"pos"`
	Address   string    `db:"address"`
	Domain    string    `db:"domain"`
	State     int64     `db:"state"`
	Attempts  int64     `db:"attempts"`
	NextTry   time.Time `db:"next_try"`
	LastError string    `db:"last_error"`
}

type sendLogRec struct {
	ID    string    `db:"id,id"`
	Key   string    `db:"key"`
	Value string    `db:"value"`
	At    time.Time `db:"at"`
}

// greylistRec: the id is the triplet key; Passed is nil until it passed.
type greylistRec struct {
	ID     string     `db:"id,id"`
	Net    string     `db:"net"`
	First  time.Time  `db:"first,index"`
	Passed *time.Time `db:"passed"`
}

// importRec: Started and Finished are the zero time until then.
type importRec struct {
	ID        string    `db:"id,id"`
	StorageID string    `db:"storage_id,ref=mailboxes,cascade"`
	Kind      string    `db:"kind"`
	State     string    `db:"state,index"`
	Error     string    `db:"error"`
	Host      string    `db:"host"`
	Port      int64     `db:"port"`
	Username  string    `db:"username"`
	FileName  string    `db:"file_name"`
	FileSize  int64     `db:"file_size"`
	Received  int64     `db:"received"`
	Data      string    `db:"data"` // importData as JSON
	Created   time.Time `db:"created"`
	Started   time.Time `db:"started"`
	Finished  time.Time `db:"finished"`
}

// importFailureRec: N keeps the order of a job's failures.
type importFailureRec struct {
	ID       string    `db:"id,id"`
	ImportID string    `db:"import_id,ref=imports,cascade"`
	N        int64     `db:"n"`
	Folder   string    `db:"folder"`
	Date     time.Time `db:"date"`
	Subject  string    `db:"subject"`
	Reason   string    `db:"reason"`
}

type importSeenRec struct {
	ID        string `db:"id,id"`
	StorageID string `db:"storage_id,ref=mailboxes,cascade"`
	Folder    string `db:"folder"`
	Key       []byte `db:"key"`
}

type importWatermarkRec struct {
	ID          string `db:"id,id"`
	StorageID   string `db:"storage_id,ref=mailboxes,cascade"`
	Host        string `db:"host"`
	Username    string `db:"username"`
	Folder      string `db:"folder"`
	UIDValidity int64  `db:"uid_validity"`
	LastUID     int64  `db:"last_uid"`
}

// overlayRec: the id is the storage id; Data is directory.Overlay as JSON,
// kept as the exact bytes given.
type overlayRec struct {
	ID   string `db:"id,id"`
	Data string `db:"data"`
}

// sessionRec: the id is the hex SHA-256 of the session id.
type sessionRec struct {
	ID         string    `db:"id,id"`
	StorageID  string    `db:"storage_id"`
	Address    string    `db:"address"`
	Credential string    `db:"credential"`
	Created    time.Time `db:"created"`
	LastSeen   time.Time `db:"last_seen"`
}

// davCollectionRec: ids are never reused (old ones are integers, new ones
// ablage ids), so a collection created again never accepts the ETags and
// sync tokens of the one before.
type davCollectionRec struct {
	ID          string    `db:"id,id"`
	StorageID   string    `db:"storage_id"`
	Kind        string    `db:"kind"`
	Name        string    `db:"name"`
	DisplayName string    `db:"display_name"`
	Color       string    `db:"color"`
	Description string    `db:"description"`
	Components  string    `db:"components"`
	Seq         int64     `db:"seq"`
	PrunedSeq   int64     `db:"pruned_seq"`
	Created     time.Time `db:"created"`
}

type davObjectRec struct {
	ID           string    `db:"id,id"`
	CollectionID string    `db:"collection_id,ref=dav_collections,cascade"`
	StorageID    string    `db:"storage_id,index"`
	Name         string    `db:"name"`
	UID          string    `db:"uid"`
	Component    string    `db:"component"`
	Size         int64     `db:"size"`
	StartAt      int64     `db:"start_at"`
	EndAt        int64     `db:"end_at"`
	File         string    `db:"file"`
	Seq          int64     `db:"seq"`
	Modified     time.Time `db:"modified"`
}

type davDeletionRec struct {
	ID           string    `db:"id,id"`
	CollectionID string    `db:"collection_id,ref=dav_collections,cascade"`
	Name         string    `db:"name"`
	Seq          int64     `db:"seq"`
	At           time.Time `db:"at,index"`
}

// sieveScriptRec: ActiveFor holds the storage id while the script is the
// active one and is nil otherwise; its unique index allows one active
// script per mailbox. Size is the content's length in bytes.
type sieveScriptRec struct {
	ID        string    `db:"id,id"`
	StorageID string    `db:"storage_id"`
	Name      string    `db:"name"`
	Content   string    `db:"content"`
	Size      int64     `db:"size"`
	ActiveFor *string   `db:"active_for,unique"`
	Updated   time.Time `db:"updated"`
}

type sieveReplyRec struct {
	ID        string    `db:"id,id"`
	StorageID string    `db:"storage_id"`
	Handle    string    `db:"handle"`
	Sender    string    `db:"sender"`
	Until     time.Time `db:"until,index"`
}

type sieveDuplicateRec struct {
	ID        string    `db:"id,id"`
	StorageID string    `db:"storage_id"`
	Key       string    `db:"key"`
	Until     time.Time `db:"until,index"`
}

// repos are the repositories of one store (the writer, the reader, or a
// transaction of either).
type repos struct {
	meta            *ablage.Repository[metaRow]
	mailboxes       *ablage.Repository[mailboxRow]
	folders         *ablage.Repository[folderRec]
	messages        *ablage.Repository[messageRec]
	sharedSeen      *ablage.Repository[sharedSeenRec]
	bindings        *ablage.Repository[bindingRec]
	blocks          *ablage.Repository[blockRec]
	events          *ablage.Repository[eventRec]
	queue           *ablage.Repository[queueRec]
	queueRcpts      *ablage.Repository[queueRcptRec]
	sendLog         *ablage.Repository[sendLogRec]
	greylist        *ablage.Repository[greylistRec]
	imports         *ablage.Repository[importRec]
	importFailures  *ablage.Repository[importFailureRec]
	importSeen      *ablage.Repository[importSeenRec]
	importMarks     *ablage.Repository[importWatermarkRec]
	overlays        *ablage.Repository[overlayRec]
	sessions        *ablage.Repository[sessionRec]
	davCollections  *ablage.Repository[davCollectionRec]
	davObjects      *ablage.Repository[davObjectRec]
	davDeletions    *ablage.Repository[davDeletionRec]
	sieveScripts    *ablage.Repository[sieveScriptRec]
	sieveReplies    *ablage.Repository[sieveReplyRec]
	sieveDuplicates *ablage.Repository[sieveDuplicateRec]
}

// newRepos declares every table of mail.db on st.
func newRepos(st ablage.Store) (*repos, error) {
	var err error
	r := &repos{
		meta:      mk[metaRow](st, &err, "meta"),
		mailboxes: mk[mailboxRow](st, &err, "mailboxes"),
		folders:   mk[folderRec](st, &err, "folders", ablage.WithUniqueIndex("storage_id", "name")),
		messages: mk[messageRec](st, &err, "messages", ablage.WithUniqueIndex("folder_id", "uid"),
			ablage.WithIndex("storage_id", "file"), ablage.WithIndex("folder_id", "msgid")),
		sharedSeen:     mk[sharedSeenRec](st, &err, "shared_seen", ablage.WithUniqueIndex("message_id", "reader")),
		bindings:       mk[bindingRec](st, &err, "bindings"),
		blocks:         mk[blockRec](st, &err, "blocks"),
		events:         mk[eventRec](st, &err, "events", ablage.WithIndex("storage_id", "kind", "seq")),
		queue:          mk[queueRec](st, &err, "queue"),
		queueRcpts:     mk[queueRcptRec](st, &err, "queue_rcpts", ablage.WithIndex("state", "next_try"), ablage.WithIndex("message_id", "pos")),
		sendLog:        mk[sendLogRec](st, &err, "send_log", ablage.WithIndex("key", "at")),
		greylist:       mk[greylistRec](st, &err, "greylist", ablage.WithIndex("net", "passed")),
		imports:        mk[importRec](st, &err, "imports", ablage.WithIndex("storage_id", "created")),
		importFailures: mk[importFailureRec](st, &err, "import_failures", ablage.WithIndex("import_id", "n")),
		importSeen: mk[importSeenRec](st, &err, "import_seen", ablage.WithUniqueIndex("storage_id", "folder", "key"),
			ablage.WithIndex("storage_id", "key")),
		importMarks:    mk[importWatermarkRec](st, &err, "import_watermarks", ablage.WithUniqueIndex("storage_id", "host", "username", "folder")),
		overlays:       mk[overlayRec](st, &err, "account_overlays"),
		sessions:       mk[sessionRec](st, &err, "account_sessions", ablage.WithIndex("storage_id", "created")),
		davCollections: mk[davCollectionRec](st, &err, "dav_collections", ablage.WithUniqueIndex("storage_id", "kind", "name")),
		davObjects: mk[davObjectRec](st, &err, "dav_objects", ablage.WithUniqueIndex("collection_id", "name"),
			ablage.WithUniqueIndex("collection_id", "uid"), ablage.WithIndex("collection_id", "seq")),
		davDeletions: mk[davDeletionRec](st, &err, "dav_deletions", ablage.WithUniqueIndex("collection_id", "name"),
			ablage.WithIndex("collection_id", "seq")),
		sieveScripts:    mk[sieveScriptRec](st, &err, "sieve_scripts", ablage.WithUniqueIndex("storage_id", "name")),
		sieveReplies:    mk[sieveReplyRec](st, &err, "sieve_replies", ablage.WithUniqueIndex("storage_id", "handle", "sender")),
		sieveDuplicates: mk[sieveDuplicateRec](st, &err, "sieve_duplicates", ablage.WithUniqueIndex("storage_id", "key")),
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// mk makes a repository unless *err is set already, and records its error.
func mk[T any](st ablage.Store, err *error, table string, opts ...ablage.Option) *ablage.Repository[T] {
	if *err != nil {
		return nil
	}
	r, e := ablage.NewRepository[T](st, table, opts...)
	if e != nil {
		*err = fmt.Errorf("table %s: %w", table, e)
	}
	return r
}

// with binds every repository to st (a transaction).
func (r *repos) with(st ablage.Store) *repos {
	return &repos{
		meta:            r.meta.With(st),
		mailboxes:       r.mailboxes.With(st),
		folders:         r.folders.With(st),
		messages:        r.messages.With(st),
		sharedSeen:      r.sharedSeen.With(st),
		bindings:        r.bindings.With(st),
		blocks:          r.blocks.With(st),
		events:          r.events.With(st),
		queue:           r.queue.With(st),
		queueRcpts:      r.queueRcpts.With(st),
		sendLog:         r.sendLog.With(st),
		greylist:        r.greylist.With(st),
		imports:         r.imports.With(st),
		importFailures:  r.importFailures.With(st),
		importSeen:      r.importSeen.With(st),
		importMarks:     r.importMarks.With(st),
		overlays:        r.overlays.With(st),
		sessions:        r.sessions.With(st),
		davCollections:  r.davCollections.With(st),
		davObjects:      r.davObjects.With(st),
		davDeletions:    r.davDeletions.With(st),
		sieveScripts:    r.sieveScripts.With(st),
		sieveReplies:    r.sieveReplies.With(st),
		sieveDuplicates: r.sieveDuplicates.With(st),
	}
}

// sec is t to the second, in UTC: what was stored as Unix seconds before.
func sec(t time.Time) time.Time { return time.Unix(t.Unix(), 0).UTC() }

// local is t (to the second) in the local time zone, as time.Unix returns
// it.
func local(t time.Time) time.Time { return time.Unix(t.Unix(), 0) }

// secOrZero is sec(t), or the zero time for the zero time ("never").
func secOrZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return sec(t)
}

// utcOrZero is the stored t in UTC, or the zero time.
func utcOrZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC()
}
