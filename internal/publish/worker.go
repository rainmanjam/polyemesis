package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// Params is a publish job's payload. It carries a ledger row id and the nonce
// minted by the confirmed click -- never a token, a session URL or a file path,
// because anything that may list jobs may read this.
type Params struct {
	PublicationID int64  `json:"publicationId"`
	Nonce         string `json:"nonce"`
}

// Result is what a finished job hands the queue.
type Result struct {
	Platform string `json:"platform"`
	PostID   string `json:"postId"`
	URL      string `json:"url,omitempty"`
	Privacy  string `json:"privacy,omitempty"`
}

// Credential is a fresh access token for one connected account.
type Credential struct {
	AccessToken string
	// Scopes is the grant the platform recorded, as stored. Empty means the
	// platform does not report one, which is not evidence of a missing scope.
	Scopes string
}

// TokenSource returns a usable token for an account, refreshing through the
// same path the rest of the product uses.
type TokenSource func(ctx context.Context, accountID int64) (Credential, error)

// File is a finished clip on disk.
type File struct {
	Path     string
	Size     int64
	Seconds  float64
	Vertical bool
}

// ErrClipGone means the export's file (or its job) no longer exists.
var ErrClipGone = errors.New("the exported clip is no longer on disk")

// ErrNotAClip means the id names something other than a finished clip export.
var ErrNotAClip = errors.New("that is not a finished clip export")

// ClipResolver turns a clip.export job id into its file.
type ClipResolver func(ctx context.Context, clipJobID int64) (File, error)

// Ledger is the durable half, satisfied by LedgerFor(*db.DB, box).
type Ledger interface {
	Begin(id int64, nonce string) (db.Publication, error)
	Session(id int64) (secret, metaHash string, err error)
	SaveSession(id int64, secret, metaHash string) error
	ClearSession(id int64) error
	SaveRemote(id int64, remote string) error
	StampFinalizing(id int64) (bool, error)
	RecordPost(id int64, postID, url, privacy string) error
	Fail(id int64, cause, detail string) error
	Note(id int64, note string) error
}

// Session is the adapter's window onto the ledger for one run.
type Session struct {
	l  Ledger
	id int64
	// Remote is the platform-side id saved by an earlier attempt, if any.
	Remote string
}

// Get returns the stored resumable session and the hash of the metadata it was
// opened with.
func (s *Session) Get() (secret, metaHash string) {
	secret, metaHash, _ = s.l.Session(s.id)
	return secret, metaHash
}

// Save stores a session BEFORE any byte is sent on it.
func (s *Session) Save(secret, metaHash string) error { return s.l.SaveSession(s.id, secret, metaHash) }

// Clear forgets the stored session.
func (s *Session) Clear() error { return s.l.ClearSession(s.id) }

// SaveRemote stores the platform-side id of an unfinished post.
func (s *Session) SaveRemote(remote string) error {
	s.Remote = remote
	return s.l.SaveRemote(s.id, remote)
}

// StampFinalizing records intent to make the one call that cannot be asked
// about afterwards. It reports false when an earlier attempt already did.
func (s *Session) StampFinalizing() (bool, error) { return s.l.StampFinalizing(s.id) }

// Run is everything an adapter needs for one attempt.
type Run struct {
	Pub  db.Publication
	Meta Meta
	Cred Credential
	File File
	Sess *Session
	Rep  jobs.Reporter
}

// Done is an adapter's success.
type Done struct {
	PostID  string
	URL     string
	Privacy string
}

// Adapter publishes to one platform. It must be resumable: given a run whose
// ledger row already holds a session or a remote id, it continues or asks the
// platform instead of starting a second post.
type Adapter interface {
	// Ready checks the grant before any request is made.
	Ready(c Credential) *Failure
	Publish(ctx context.Context, r Run) (Done, error)
}

// WorkerDeps are the worker's collaborators.
type WorkerDeps struct {
	Log      *slog.Logger
	Ledger   Ledger
	Tokens   TokenSource
	Clips    ClipResolver
	Adapters map[string]Adapter
	Gates    Gates
}

// NewWorker adapts publishing to the job queue.
//
// THE FIRST THING IT DOES IS ASK THE LEDGER WHETHER A CLICK ARMED THIS JOB.
// Nothing is read from disk, no token is fetched and no request leaves the
// process before Begin succeeds. That is the whole of "never without a click"
// on the worker's side, and it is why a generic job retry, a replayed job or a
// hand-made row does nothing.
func NewWorker(d WorkerDeps) jobs.Worker {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return jobs.WorkerFunc(func(ctx context.Context, job jobs.Job, rep jobs.Reporter) error {
		var p Params
		if err := json.Unmarshal(job.Params, &p); err != nil || p.PublicationID <= 0 {
			return jobs.Permanent(errors.New("publish: this job does not name a publication"))
		}
		pub, err := d.Ledger.Begin(p.PublicationID, p.Nonce)
		switch {
		case errors.Is(err, db.ErrPublicationPosted):
			// Already on the platform. Success, and nothing is sent.
			rep.SetResult(Result{Platform: pub.Platform, PostID: pub.PlatformPostID, URL: pub.PostURL, Privacy: pub.ActualPrivacy})
			return nil
		case errors.Is(err, db.ErrPublicationNotArmed):
			rep.Logf("not started: no confirmed click armed this publication")
			return jobs.Permanent(err)
		case err != nil:
			return err
		}

		ad, ok := d.Adapters[pub.Platform]
		if !ok || (pub.Platform == PlatformX && !d.Gates.XPublish) {
			return d.giveUp(pub, fail(CauseGated, false, "%s", gateCopy(pub.Platform)))
		}

		meta := Meta{Title: pub.Title, Description: pub.Description, Tags: pub.Tags, Privacy: pub.Privacy}

		cred, err := d.Tokens(ctx, pub.AccountID)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if strings.Contains(strings.ToLower(err.Error()), "reconnect") {
				return d.giveUp(pub, fail(CauseNeedsReconnect, false, "%s", scrubAll(err.Error())))
			}
			return d.settle(job, pub, fail(CauseTokenRejected, true, "%s", scrubAll(err.Error())), cred)
		}
		if f := ad.Ready(cred); f != nil {
			return d.giveUp(pub, f)
		}

		file, err := d.Clips(ctx, pub.ClipJobID)
		if err != nil {
			if errors.Is(err, ErrClipGone) {
				return d.giveUp(pub, fail(CauseClipMissing, false, "%s", err.Error()))
			}
			if errors.Is(err, ErrNotAClip) {
				return d.giveUp(pub, fail(CauseClipRejected, false, "%s", err.Error()))
			}
			return d.settle(job, pub, fail(CauseUnknown, true, "%s", scrubAll(err.Error())), cred)
		}

		sess := &Session{l: d.Ledger, id: pub.ID, Remote: pub.PlatformPostID}
		done, err := ad.Publish(ctx, Run{Pub: pub, Meta: meta, Cred: cred, File: file, Sess: sess, Rep: rep})
		if err != nil {
			if ctx.Err() != nil {
				// Shutdown or cancel: the row stays in flight and the queue
				// decides whether the job comes back.
				return ctx.Err()
			}
			var f *Failure
			if !errors.As(err, &f) {
				f = fail(CauseNetwork, true, "%s", err.Error())
			}
			return d.settle(job, pub, f, cred)
		}
		if err := d.Ledger.RecordPost(pub.ID, done.PostID, done.URL, done.Privacy); err != nil {
			// The platform has the post and the ledger could not say so. A
			// retry would find the stored session or remote id and resolve to
			// the same post, so this is retried, never repeated.
			return fmt.Errorf("publish: the post was made but could not be recorded: %w", err)
		}
		rep.SetResult(Result{Platform: pub.Platform, PostID: done.PostID, URL: done.URL, Privacy: done.Privacy})
		rep.Progress(1)
		log.Info("clip published", "platform", pub.Platform, "publication", pub.ID)
		return nil
	})
}

func gateCopy(platform string) string {
	if platform == PlatformX {
		return XGateCopy
	}
	return "Publishing to " + platform + " is not available in this build."
}

// giveUp ends the publication for a cause a retry cannot fix.
func (d WorkerDeps) giveUp(pub db.Publication, f *Failure) error {
	_ = d.Ledger.Fail(pub.ID, string(f.Cause), f.Detail)
	return jobs.Permanent(f)
}

// settle ends or continues the publication after a failed step: retryable
// failures go back to the queue's backoff until it runs out of attempts.
func (d WorkerDeps) settle(job jobs.Job, pub db.Publication, f *Failure, c Credential) error {
	f.Detail = scrub(f.Detail, c.AccessToken)
	if !f.Retry {
		return d.giveUp(pub, f)
	}
	if job.Attempts >= job.MaxAttempts {
		_ = d.Ledger.Fail(pub.ID, string(f.Cause), f.Detail)
		return f
	}
	_ = d.Ledger.Note(pub.ID, "will retry: "+f.Detail)
	return f
}

func scrub(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) >= 6 {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	return s
}

func scrubAll(s string) string { return strings.TrimSpace(s) }
