package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/danielgtmn/ablage"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// importsDir holds the spool files of import uploads. Unlike tmp/ it is
// not emptied at start: a file job continues from its spool after a
// restart (the import runner removes spools without a job).
const importsDir = "imports"

// ErrNoImport: no such import job (or its mailbox was deleted).
var ErrNoImport = errors.New("no such import job")

// ImportsDir is the directory for import spool files, on the same file
// system as the messages.
func (s *Store) ImportsDir() string { return filepath.Join(s.dir, importsDir) }

// importData is the part of a job kept as JSON.
type importData struct {
	Choices  config.ImportChoices          `json:"choices"`
	Check    *config.ImportCheck           `json:"check,omitempty"`
	Folders  []config.ImportFolderProgress `json:"folders,omitempty"`
	Messages int                           `json:"messages,omitempty"`
	Copied   int                           `json:"copied,omitempty"`
	Skipped  int                           `json:"skipped,omitempty"`
	Failed   int                           `json:"failed,omitempty"`
	Bytes    int64                         `json:"bytes,omitempty"`
	Position int64                         `json:"position,omitempty"`
	Scans    int                           `json:"scans,omitempty"`
}

func importJob(r importRec) (config.ImportJob, error) {
	j := config.ImportJob{ID: r.ID, StorageID: r.StorageID, Kind: r.Kind, State: r.State, Error: r.Error, Host: r.Host, Port: int(r.Port),
		Username: r.Username, FileName: r.FileName, FileSize: r.FileSize, Received: r.Received}
	var d importData
	if err := json.Unmarshal([]byte(r.Data), &d); err != nil {
		return j, err
	}
	j.Choices, j.Check, j.Folders = d.Choices, d.Check, d.Folders
	if j.Folders == nil {
		j.Folders = []config.ImportFolderProgress{}
	}
	j.Messages, j.Copied, j.Skipped, j.Failed, j.Bytes, j.Position, j.Scans = d.Messages, d.Copied, d.Skipped, d.Failed, d.Bytes, d.Position, d.Scans
	j.Created, j.Started, j.Finished = utcOrZero(r.Created), utcOrZero(r.Started), utcOrZero(r.Finished)
	return j, nil
}

func importJobs(list []importRec) ([]config.ImportJob, error) {
	out := []config.ImportJob{}
	for _, r := range list {
		j, err := importJob(r)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

func encodeImportData(j *config.ImportJob) (string, error) {
	b, err := json.Marshal(importData{
		Choices: j.Choices, Check: j.Check, Folders: j.Folders,
		Messages: j.Messages, Copied: j.Copied, Skipped: j.Skipped, Failed: j.Failed, Bytes: j.Bytes, Position: j.Position,
		Scans: j.Scans,
	})
	return string(b), err
}

// CreateImport records a new job of a mailbox (whose default folders are
// created if needed) and sets its ID and creation time.
func (s *Store) CreateImport(ctx context.Context, j *config.ImportJob) error {
	if err := s.ensure(ctx, j.StorageID); err != nil {
		return err
	}
	data, err := encodeImportData(j)
	if err != nil {
		return err
	}
	j.Created = sec(s.now())
	return s.write(ctx, func(tx *repos, _ *changes) error {
		r := importRec{StorageID: j.StorageID, Kind: j.Kind, State: j.State, Error: j.Error, Host: j.Host, Port: int64(j.Port), Username: j.Username,
			FileName: j.FileName, FileSize: j.FileSize, Received: j.Received, Data: data, Created: j.Created}
		if err := tx.imports.Create(ctx, &r); err != nil {
			return err
		}
		j.ID = r.ID
		return nil
	})
}

// SaveImport stores the state, counts and times of a job. ErrNoImport if
// the job is gone (its mailbox was deleted).
func (s *Store) SaveImport(ctx context.Context, j *config.ImportJob) error {
	data, err := encodeImportData(j)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *repos, _ *changes) error {
		n, err := tx.imports.UpdateWhere(ctx, ablage.And(ablage.Eq("id", j.ID), ablage.Eq("storage_id", j.StorageID)), ablage.Row{
			"state": j.State, "error": j.Error, "received": j.Received, "data": data, "started": secOrZero(j.Started), "finished": secOrZero(j.Finished),
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNoImport
		}
		return nil
	})
}

// Import returns a job of a mailbox.
func (s *Store) Import(ctx context.Context, storageID, id string) (config.ImportJob, error) {
	r, err := s.r.imports.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("id", id), ablage.Eq("storage_id", storageID))})
	if errors.Is(err, ablage.ErrNotFound) {
		return config.ImportJob{}, ErrNoImport
	}
	if err != nil {
		return config.ImportJob{}, err
	}
	return importJob(r)
}

// Imports returns the jobs of a mailbox, newest first.
func (s *Store) Imports(ctx context.Context, storageID string) ([]config.ImportJob, error) {
	list, err := s.r.imports.Find(ctx, ablage.Query{Where: ablage.Eq("storage_id", storageID), OrderBy: []ablage.Order{ablage.Desc("created"), ablage.Desc("id")}})
	if err != nil {
		return nil, err
	}
	return importJobs(list)
}

// ImportsIn returns the jobs of all mailboxes in the given states, oldest
// first.
func (s *Store) ImportsIn(ctx context.Context, states ...string) ([]config.ImportJob, error) {
	if len(states) == 0 {
		return nil, nil
	}
	list, err := s.r.imports.Find(ctx, ablage.Query{Where: ablage.In("state", states...), OrderBy: []ablage.Order{ablage.Asc("created")}})
	if err != nil {
		return nil, err
	}
	return importJobs(list)
}

// ImportIDs returns the ids of every job with its state (to find spool
// files without one).
func (s *Store) ImportIDs(ctx context.Context) (map[string]string, error) {
	list, err := s.r.imports.Find(ctx, ablage.Query{})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range list {
		out[r.ID] = r.State
	}
	return out, nil
}

// DeleteImport removes a job with its failures.
func (s *Store) DeleteImport(ctx context.Context, storageID, id string) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		_, err := tx.imports.DeleteWhere(ctx, ablage.And(ablage.Eq("id", id), ablage.Eq("storage_id", storageID)))
		return err
	})
}

// PruneImports removes finished jobs that ended before cutoff and returns
// their ids.
func (s *Store) PruneImports(ctx context.Context, cutoff time.Time) ([]string, error) {
	var ids []string
	err := s.write(ctx, func(tx *repos, _ *changes) error {
		ids = nil
		list, err := tx.imports.Find(ctx, ablage.Query{Where: ablage.And(
			ablage.In("state", config.ImportDone, config.ImportFailed, config.ImportCancelled),
			ablage.Gt("finished", time.Time{}), ablage.Lt("finished", sec(cutoff)))})
		if err != nil || len(list) == 0 {
			return err
		}
		for _, r := range list {
			ids = append(ids, r.ID)
		}
		_, err = tx.imports.DeleteWhere(ctx, ablage.In("id", ids...))
		return err
	})
	return ids, err
}

// AddImportFailure records a message a job did not copy; beyond
// config.MaxImportFailures entries nothing more is recorded.
func (s *Store) AddImportFailure(ctx context.Context, id string, f config.ImportFailure) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		n, err := tx.importFailures.Count(ctx, ablage.Eq("import_id", id))
		if err != nil {
			return err
		}
		if n >= config.MaxImportFailures {
			return nil
		}
		last, err := tx.importFailures.Aggregate(ctx, ablage.Aggregation{Where: ablage.Eq("import_id", id), Values: []ablage.Agg{ablage.Max("n", "n")}})
		if err != nil {
			return err
		}
		next := int64(0)
		if v, ok := last[0]["n"].(int64); ok {
			next = v + 1
		}
		return tx.importFailures.Create(ctx, &importFailureRec{ImportID: id, N: next, Folder: f.Folder, Date: secOrZero(f.Date), Subject: f.Subject, Reason: f.Reason})
	})
}

// ImportFailures returns the recorded failures of a job, in order.
func (s *Store) ImportFailures(ctx context.Context, id string) ([]config.ImportFailure, error) {
	list, err := s.r.importFailures.Find(ctx, ablage.Query{Where: ablage.Eq("import_id", id), OrderBy: []ablage.Order{ablage.Asc("n")}})
	if err != nil {
		return nil, err
	}
	out := []config.ImportFailure{}
	for _, r := range list {
		out = append(out, config.ImportFailure{Folder: r.Folder, Date: utcOrZero(r.Date), Subject: r.Subject, Reason: r.Reason})
	}
	return out, nil
}

// ImportSeen tells whether a message with this key was imported into
// folder of the mailbox before, or into any folder when folder is "".
func (s *Store) ImportSeen(ctx context.Context, storageID, folder string, key []byte) (bool, error) {
	where := ablage.And(ablage.Eq("storage_id", storageID), ablage.Eq("key", key))
	if folder != "" {
		where = ablage.And(where, ablage.Eq("folder", folder))
	}
	n, err := s.r.importSeen.Count(ctx, where)
	return n > 0, err
}

// Watermark is the highest UID copied from a folder of another IMAP
// account, valid as long as the folder's UIDVALIDITY stays the same.
type Watermark struct {
	Host, Username, Folder string
	UIDValidity, LastUID   uint32
}

// RecordImported notes the key of a message imported into folder (when
// key is set) and moves the watermark (when wm is set), in one
// transaction.
func (s *Store) RecordImported(ctx context.Context, storageID, folder string, key []byte, wm *Watermark) error {
	return s.write(ctx, func(tx *repos, _ *changes) error {
		if key != nil {
			err := tx.importSeen.Create(ctx, &importSeenRec{StorageID: storageID, Folder: folder, Key: key})
			if err != nil && !errors.Is(err, ablage.ErrDuplicate) {
				return err
			}
		}
		if wm != nil {
			if err := tx.importMarks.UpsertOn(ctx, &importWatermarkRec{StorageID: storageID, Host: wm.Host, Username: wm.Username, Folder: wm.Folder,
				UIDValidity: int64(wm.UIDValidity), LastUID: int64(wm.LastUID)}, "storage_id", "host", "username", "folder"); err != nil {
				return err
			}
		}
		return nil
	})
}

// ImportWatermark returns the watermark of a source folder (zero values
// if there is none).
func (s *Store) ImportWatermark(ctx context.Context, storageID, host, username, folder string) (Watermark, error) {
	wm := Watermark{Host: host, Username: username, Folder: folder}
	r, err := s.r.importMarks.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("storage_id", storageID), ablage.Eq("host", host),
		ablage.Eq("username", username), ablage.Eq("folder", folder))})
	if errors.Is(err, ablage.ErrNotFound) {
		return wm, nil
	}
	wm.UIDValidity, wm.LastUID = uint32(r.UIDValidity), uint32(r.LastUID)
	return wm, err
}

// SetSpecialUse gives a folder a special use (import of folders that
// have one at the source), unless another folder of the mailbox has it.
func (s *Store) SetSpecialUse(ctx context.Context, storageID, name, use string) error {
	name, err := NormalizeFolderName(name)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *repos, ch *changes) error {
		n, err := tx.folders.Count(ctx, ablage.And(ablage.Eq("storage_id", storageID), ablage.Eq("special_use", use)))
		if err != nil || n > 0 {
			return err
		}
		if _, err := tx.folders.UpdateWhere(ctx, ablage.And(ablage.Eq("storage_id", storageID), ablage.Eq("name", name)), ablage.Row{"special_use": use}); err != nil {
			return err
		}
		ch.add(storageID, Change{Kind: FoldersChanged, Folder: name})
		return nil
	})
}
