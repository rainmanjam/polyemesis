package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

type dbLedger struct {
	d   *db.DB
	box *secrets.Box
}

// LedgerFor adapts the database to the worker's Ledger.
func LedgerFor(d *db.DB, box *secrets.Box) Ledger { return dbLedger{d: d, box: box} }

func (l dbLedger) Begin(id int64, nonce string) (db.Publication, error) {
	return l.d.BeginPublication(id, nonce)
}
func (l dbLedger) Session(id int64) (string, string, error) {
	return l.d.GetPublicationSession(l.box, id)
}
func (l dbLedger) SaveSession(id int64, nonce, secret, hash string) error {
	return l.d.SetPublicationSession(l.box, id, nonce, secret, hash)
}
func (l dbLedger) ClearSession(id int64, nonce string) error {
	return l.d.ClearPublicationSession(id, nonce)
}
func (l dbLedger) SaveRemote(id int64, nonce, remote string) error {
	return l.d.SetPublicationRemote(id, nonce, remote)
}
func (l dbLedger) StampFinalizing(id int64, nonce string) (bool, error) {
	return l.d.StampPublicationFinalizing(id, nonce)
}
func (l dbLedger) RecordPost(id int64, postID, url, privacy string) error {
	return l.d.RecordPublicationPost(id, postID, url, privacy)
}
func (l dbLedger) Fail(id int64, nonce, cause, detail string) error {
	return l.d.FailPublicationFor(id, nonce, cause, detail)
}
func (l dbLedger) Note(id int64, nonce, note string) error {
	return l.d.SetPublicationNote(id, nonce, note)
}

// JobReader is the slice of the job store the clip resolver needs.
type JobReader interface {
	GetJob(id int64) (*jobs.Job, error)
}

// ClipsFrom resolves a clip.export job to its file, confined to the exports
// directory exactly as the download route confines it.
func ClipsFrom(store JobReader, exportDir string) ClipResolver {
	return func(_ context.Context, clipJobID int64) (File, error) {
		return ResolveClip(store, exportDir, clipJobID)
	}
}

// ResolveClip is ClipsFrom's body, also used by the API to preflight a click.
func ResolveClip(store JobReader, exportDir string, clipJobID int64) (File, error) {
	job, err := store.GetJob(clipJobID)
	if errors.Is(err, db.ErrNotFound) {
		return File{}, ErrClipGone
	}
	if err != nil {
		// A failed read says nothing about the clip; the worker retries it.
		return File{}, fmt.Errorf("could not read the clip's export job: %w", err)
	}
	if job == nil {
		return File{}, ErrClipGone
	}
	if job.Kind != clipper.JobKind || job.State != jobs.StateDone || len(job.Result) == 0 {
		return File{}, ErrNotAClip
	}
	var res clipper.JobResult
	if err := json.Unmarshal(job.Result, &res); err != nil || res.Path == "" {
		return File{}, ErrClipGone
	}
	path, _, err := clipper.ExportPathIn(exportDir, res.Path)
	if err != nil {
		return File{}, err
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return File{}, ErrClipGone
	}
	return File{Path: path, Size: st.Size(), ModTime: st.ModTime().UnixNano(), Seconds: res.Seconds, Vertical: res.Vertical}, nil
}
