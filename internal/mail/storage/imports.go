package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

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

const importCols = `id, storage_id, kind, state, error, host, port, username, file_name, file_size, received, data, created, started, finished`

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

func scanImport(sc interface{ Scan(...any) error }) (config.ImportJob, error) {
	var j config.ImportJob
	var data string
	var created, started, finished int64
	if err := sc.Scan(&j.ID, &j.StorageID, &j.Kind, &j.State, &j.Error, &j.Host, &j.Port, &j.Username,
		&j.FileName, &j.FileSize, &j.Received, &data, &created, &started, &finished); err != nil {
		return j, err
	}
	var d importData
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return j, err
	}
	j.Choices, j.Check, j.Folders = d.Choices, d.Check, d.Folders
	if j.Folders == nil {
		j.Folders = []config.ImportFolderProgress{}
	}
	j.Messages, j.Copied, j.Skipped, j.Failed, j.Bytes, j.Position, j.Scans = d.Messages, d.Copied, d.Skipped, d.Failed, d.Bytes, d.Position, d.Scans
	j.Created, j.Started, j.Finished = timeOrZero(created), timeOrZero(started), timeOrZero(finished)
	return j, nil
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
	j.Created = s.now().UTC().Truncate(time.Second)
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		res, err := tx.Exec(`INSERT INTO imports (storage_id, kind, state, error, host, port, username, file_name, file_size, received, data, created)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.StorageID, j.Kind, j.State, j.Error, j.Host, j.Port, j.Username, j.FileName, j.FileSize, j.Received, data, j.Created.Unix())
		if err != nil {
			return err
		}
		j.ID, err = res.LastInsertId()
		return err
	})
}

// SaveImport stores the state, counts and times of a job. ErrNoImport if
// the job is gone (its mailbox was deleted).
func (s *Store) SaveImport(ctx context.Context, j *config.ImportJob) error {
	data, err := encodeImportData(j)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		res, err := tx.Exec(`UPDATE imports SET state = ?, error = ?, received = ?, data = ?, started = ?, finished = ?
			WHERE id = ? AND storage_id = ?`,
			j.State, j.Error, j.Received, data, unixOrZero(j.Started), unixOrZero(j.Finished), j.ID, j.StorageID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoImport
		}
		return nil
	})
}

// Import returns a job of a mailbox.
func (s *Store) Import(ctx context.Context, storageID string, id int64) (config.ImportJob, error) {
	j, err := scanImport(s.rdb.QueryRowContext(ctx, `SELECT `+importCols+` FROM imports WHERE id = ? AND storage_id = ?`, id, storageID))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNoImport
	}
	return j, err
}

// Imports returns the jobs of a mailbox, newest first.
func (s *Store) Imports(ctx context.Context, storageID string) ([]config.ImportJob, error) {
	return s.queryImports(ctx, `SELECT `+importCols+` FROM imports WHERE storage_id = ? ORDER BY id DESC`, storageID)
}

// ImportsIn returns the jobs of all mailboxes in the given states, oldest
// first.
func (s *Store) ImportsIn(ctx context.Context, states ...string) ([]config.ImportJob, error) {
	if len(states) == 0 {
		return nil, nil
	}
	args := make([]any, len(states))
	for i, st := range states {
		args[i] = st
	}
	q := `SELECT ` + importCols + ` FROM imports WHERE state IN (?` + strings.Repeat(", ?", len(states)-1) + `) ORDER BY id`
	return s.queryImports(ctx, q, args...)
}

func (s *Store) queryImports(ctx context.Context, q string, args ...any) ([]config.ImportJob, error) {
	rows, err := s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []config.ImportJob{}
	for rows.Next() {
		j, err := scanImport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ImportIDs returns the ids of every job (to find spool files without one).
func (s *Store) ImportIDs(ctx context.Context) (map[int64]string, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT id, state FROM imports`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// DeleteImport removes a job with its failures.
func (s *Store) DeleteImport(ctx context.Context, storageID string, id int64) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		_, err := tx.Exec(`DELETE FROM imports WHERE id = ? AND storage_id = ?`, id, storageID)
		return err
	})
}

// PruneImports removes finished jobs that ended before cutoff and returns
// their ids.
func (s *Store) PruneImports(ctx context.Context, cutoff time.Time) ([]int64, error) {
	var ids []int64
	err := s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		rows, err := tx.Query(`DELETE FROM imports WHERE state IN (?, ?, ?) AND finished > 0 AND finished < ? RETURNING id`,
			config.ImportDone, config.ImportFailed, config.ImportCancelled, cutoff.Unix())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

// AddImportFailure records a message a job did not copy; beyond
// config.MaxImportFailures entries nothing more is recorded.
func (s *Store) AddImportFailure(ctx context.Context, id int64, f config.ImportFailure) error {
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM import_failures WHERE import_id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n >= config.MaxImportFailures {
			return nil
		}
		_, err := tx.Exec(`INSERT INTO import_failures (import_id, folder, date, subject, reason) VALUES (?, ?, ?, ?, ?)`,
			id, f.Folder, unixOrZero(f.Date), f.Subject, f.Reason)
		return err
	})
}

// ImportFailures returns the recorded failures of a job, in order.
func (s *Store) ImportFailures(ctx context.Context, id int64) ([]config.ImportFailure, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT folder, date, subject, reason FROM import_failures WHERE import_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []config.ImportFailure{}
	for rows.Next() {
		var f config.ImportFailure
		var date int64
		if err := rows.Scan(&f.Folder, &date, &f.Subject, &f.Reason); err != nil {
			return nil, err
		}
		f.Date = timeOrZero(date)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ImportSeen tells whether a message with this key was imported into
// folder of the mailbox before, or into any folder when folder is "".
func (s *Store) ImportSeen(ctx context.Context, storageID, folder string, key []byte) (bool, error) {
	var n int
	var err error
	if folder == "" {
		err = s.rdb.QueryRowContext(ctx, `SELECT count(*) FROM import_seen WHERE storage_id = ? AND key = ?`, storageID, key).Scan(&n)
	} else {
		err = s.rdb.QueryRowContext(ctx, `SELECT count(*) FROM import_seen WHERE storage_id = ? AND folder = ? AND key = ?`, storageID, folder, key).Scan(&n)
	}
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
	return s.write(ctx, func(tx *sql.Tx, _ *changes) error {
		if key != nil {
			if _, err := tx.Exec(`INSERT INTO import_seen (storage_id, folder, key) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
				storageID, folder, key); err != nil {
				return err
			}
		}
		if wm != nil {
			if _, err := tx.Exec(`INSERT INTO import_watermarks (storage_id, host, username, folder, uid_validity, last_uid)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (storage_id, host, username, folder) DO UPDATE SET uid_validity = excluded.uid_validity, last_uid = excluded.last_uid`,
				storageID, wm.Host, wm.Username, wm.Folder, wm.UIDValidity, wm.LastUID); err != nil {
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
	err := s.rdb.QueryRowContext(ctx, `SELECT uid_validity, last_uid FROM import_watermarks
		WHERE storage_id = ? AND host = ? AND username = ? AND folder = ?`, storageID, host, username, folder).
		Scan(&wm.UIDValidity, &wm.LastUID)
	if errors.Is(err, sql.ErrNoRows) {
		return wm, nil
	}
	return wm, err
}

// SetSpecialUse gives a folder a special use (import of folders that
// have one at the source), unless another folder of the mailbox has it.
func (s *Store) SetSpecialUse(ctx context.Context, storageID, name, use string) error {
	name, err := NormalizeFolderName(name)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, ch *changes) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM folders WHERE storage_id = ? AND special_use = ?`, storageID, use).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		if _, err := tx.Exec(`UPDATE folders SET special_use = ? WHERE storage_id = ? AND name = ?`, use, storageID, name); err != nil {
			return err
		}
		ch.add(storageID, Change{Kind: FoldersChanged, Folder: name})
		return nil
	})
}
