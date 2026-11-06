package api

// Clip publishing (Wave 4 #5c): send a finished clip export or short to
// YouTube, TikTok or (gated, off) X.
//
// NOTHING HERE PUBLISHES ON ITS OWN, AND THERE IS NO SWITCH THAT WOULD MAKE IT.
// What stands between a request and a platform:
//
//   - Every route is in the requireSession group. An API token -- admin, mcp or
//     read -- cannot reach any of them, and no MCP tool exists for them.
//   - POST /publish needs a confirmation token that POST /publish/prepare minted
//     for exactly the form that is being submitted, for the same signed-in
//     person, once, within ten minutes (publish_confirm.go). The console calls
//     prepare when the form is reviewable and publish when the Publish button
//     is clicked.
//   - The click is recorded as a ledger row carrying a nonce that the queued
//     job carries too. The worker refuses a job whose nonce does not match a row
//     that is armed (internal/publish, db.BeginPublication), so a replayed,
//     retried or hand-made job does nothing.
//   - Generic job retry is refused for this kind, and cancelling the job revokes
//     the click.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/platform"
	"github.com/rainmanjam/polyemesis/internal/publish"
)

// publishMaxAttempts is deliberately more generous than the queue default: a
// platform that is busy for a minute is the ordinary reason an upload retries.
const publishMaxAttempts = 5

// PublishTokens is the worker's TokenSource, backed by the same refresh path
// every other platform call uses. Exported because the worker is registered in
// cmd/polyemesis before the server exists; see publish.TokenGate.
func (s *Server) PublishTokens() publish.TokenSource {
	return func(ctx context.Context, accountID int64) (publish.Credential, error) {
		acct, err := s.tokenFor(ctx, accountID)
		if errors.Is(err, oauth.ErrReconnectRequired) {
			// Typed for the worker, which ends the publish with
			// needs_reconnect instead of retrying a grant that is gone.
			return publish.Credential{}, fmt.Errorf("%w: %v", publish.ErrReconnectRequired, err)
		}
		if err != nil {
			return publish.Credential{}, err
		}
		return publish.Credential{AccessToken: acct.AccessToken, Scopes: acct.Scopes}, nil
	}
}

// ------------------------------------------------------------------ request

type publishRequest struct {
	ClipJobID    int64    `json:"clipJobId"`
	Platform     string   `json:"platform"`
	AccountID    int64    `json:"accountId"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Tags         []string `json:"tags"`
	Privacy      string   `json:"privacy"`
	ConfirmToken string   `json:"confirmToken,omitempty"`
	// AcknowledgeOutcome is the person saying they looked at the platform after
	// an outcome_unknown failure and the post is not there. Without it that
	// failure cannot be re-armed: the earlier request may have landed.
	AcknowledgeOutcome bool `json:"acknowledgeOutcome,omitempty"`
}

// plan is everything a request resolves to, checked.
type publishPlan struct {
	req     publishRequest
	meta    publish.Meta
	account db.PlatformAccount
	file    publish.File
	hash    string
}

type publishError struct {
	status int
	code   string
	msg    string
}

func (e *publishError) Error() string { return e.msg }

func (s *Server) writePublishError(w http.ResponseWriter, err error) {
	var pe *publishError
	if errors.As(err, &pe) {
		writeErrorCode(w, pe.status, pe.code, pe.msg)
		return
	}
	writeStoreError(w, err)
}

// planPublish validates a request against the clip, the platform and the
// account. It reads; it never writes.
func (s *Server) planPublish(req publishRequest) (*publishPlan, error) {
	if s.jobq == nil {
		return nil, &publishError{http.StatusServiceUnavailable, "no_queue", jobsUnavailable}
	}
	switch req.Platform {
	case publish.PlatformYouTube, publish.PlatformTikTok, publish.PlatformX:
	default:
		return nil, &publishError{http.StatusBadRequest, "unknown_platform", "unknown platform"}
	}
	if req.Platform == publish.PlatformX && !s.publishGates.XPublish {
		return nil, &publishError{http.StatusForbidden, "gated", publish.XGateCopy}
	}
	if req.ClipJobID <= 0 || req.AccountID <= 0 {
		return nil, &publishError{http.StatusBadRequest, "invalid", "choose a clip and a connected account"}
	}
	meta, err := publish.Normalize(req.Platform, publish.Meta{
		Title: req.Title, Description: req.Description, Tags: req.Tags, Privacy: req.Privacy})
	if err != nil {
		return nil, &publishError{http.StatusBadRequest, "invalid", strings.TrimPrefix(err.Error(), "invalid publish request: ")}
	}
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		return nil, err
	}
	var acct *db.PlatformAccount
	for i := range accts {
		if accts[i].ID == req.AccountID {
			acct = &accts[i]
		}
	}
	if acct == nil || string(acct.Platform) != req.Platform {
		return nil, &publishError{http.StatusNotFound, "no_account", "that account is not connected to " + req.Platform}
	}
	if f := publish.GrantFailure(req.Platform, acct.Scopes); f != nil {
		return nil, &publishError{http.StatusConflict, string(publish.CauseNeedsReconnect), f.Detail}
	}
	file, err := publish.ResolveClip(s.store, s.clipExportDir(), req.ClipJobID)
	if err != nil {
		code := "clip_missing"
		if errors.Is(err, publish.ErrNotAClip) {
			code = "not_a_clip"
		}
		return nil, &publishError{http.StatusNotFound, code, err.Error()}
	}
	if req.Platform == publish.PlatformTikTok && file.Seconds > 0 && file.Seconds > 10*60 {
		return nil, &publishError{http.StatusBadRequest, "clip_rejected", "TikTok posts from the API are limited to a few minutes; trim the clip first"}
	}
	meta.Title = strings.TrimSpace(meta.Title)
	b, _ := json.Marshal([]any{req.ClipJobID, req.Platform, req.AccountID, meta})
	sum := sha256.Sum256(b)
	return &publishPlan{req: req, meta: meta, account: *acct, file: file,
		hash: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

// sessionUser is the signed-in person a request is from. The routes sit behind
// requireSession, so this never fails there; it is checked anyway so that a route
// ever mounted without that middleware refuses to mint or spend a confirmation as
// user 0.
func sessionUser(w http.ResponseWriter, r *http.Request) (int64, bool) {
	p, ok := principalFrom(r.Context())
	if !ok || p.token != nil || p.userID <= 0 {
		writeError(w, http.StatusUnauthorized, "sign in to do this")
		return 0, false
	}
	return p.userID, true
}

// ----------------------------------------------------------------- the views

type publishJobView struct {
	ID         int64   `json:"id"`
	State      string  `json:"state"`
	Progress   float64 `json:"progress"`
	Attempts   int     `json:"attempts"`
	Blocked    bool    `json:"blocked,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	ETASeconds float64 `json:"etaSeconds,omitempty"`
}

type publicationView struct {
	db.Publication
	// Reason is why a failed publication failed, in the same shape as a
	// destination's failure reason (cause + the line it rests on).
	Reason *publish.Reason `json:"reason,omitempty"`
	Job    *publishJobView `json:"job,omitempty"`
}

// publishNeverQueuedAfter is how long a row may sit armed with no job before it
// is treated as never having been queued (a crash between the claim and the
// submit).
const publishNeverQueuedAfter = 2 * time.Minute

// reconcilePublication makes a row's state agree with its job. A row that is
// still in flight while its job has ended is stuck: nothing will advance it, and
// without this every later click would find it "in flight" and queue nothing.
// Failing it is safe by construction -- a new click re-arms the row, and the
// stored session or remote id makes the next attempt resume or ask the platform
// rather than post again. It returns the job it looked at, when it is still live.
func (s *Server) reconcilePublication(p db.Publication) (db.Publication, *jobs.Job) {
	if p.State != db.PublicationConfirmed && p.State != db.PublicationUploading {
		return p, nil
	}
	fail := func(cause publish.Cause, detail string) (db.Publication, *jobs.Job) {
		// Fenced on the nonce this snapshot was read with: if a click re-armed the
		// row since, this stale look must not fail the new run.
		if err := s.store.FailPublicationFor(p.ID, p.Nonce, string(cause), detail); err != nil &&
			!errors.Is(err, db.ErrPublicationNotArmed) {
			s.log.Warn("could not fail a stranded publication", "publication", p.ID, "err", err)
		}
		p, _ = s.store.GetPublication(p.ID)
		return p, nil
	}
	if p.PublishJobID == 0 {
		// Submit and the link back to the row are separate writes. A live job that
		// was never linked (a crash between them) is found by its target and linked
		// now; only a row with no job at all, for long enough, is called unqueued.
		j, err := s.store.FindActiveJob(publish.JobKind, publishJobTarget(p.ID))
		if err != nil {
			s.log.Warn("could not look for a publication's job", "publication", p.ID, "err", err)
			return p, nil
		}
		if j != nil {
			if err := s.store.SetPublicationJob(p.ID, nonceOf(j), j.ID); err != nil {
				s.log.Warn("could not link a publication to its job", "publication", p.ID, "err", err)
			}
			p.PublishJobID = j.ID
			return p, j
		}
		if time.Since(p.UpdatedAt) > publishNeverQueuedAfter {
			return fail(publish.CauseUnknown, "The publish job was never queued. Click Publish to try again.")
		}
		return p, nil
	}
	j, err := s.store.GetJob(p.PublishJobID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return fail(publish.CauseCancelled, "The publish job is gone.")
	case err != nil || j == nil:
		// A read that failed says nothing about the job. Leave the row alone: a
		// worker may be mid-upload, and failing it here could let a click post again.
		s.log.Warn("could not read a publication's job", "publication", p.ID, "err", err)
		return p, nil
	case j.State == jobs.StateCancelled:
		return fail(publish.CauseCancelled, "Publishing was cancelled.")
	case j.State == jobs.StateFailed:
		// The worker records its own failures; this is a job that ended without
		// one (a crash on the last attempt, a ledger write that failed).
		detail := strings.TrimSpace(publish.Redact(j.Error))
		if detail == "" {
			detail = "The publish job ended without finishing."
		}
		return fail(publish.CauseUnknown, detail)
	case j.State == jobs.StateDone:
		return fail(publish.CauseUnknown, "The publish job finished without recording a post.")
	}
	return p, j
}

// publishJobTarget is the queue target a publication's job is submitted with.
func publishJobTarget(id int64) string { return fmt.Sprintf("clip-publish:%d", id) }

// nonceOf reads the nonce a publish job was queued with.
func nonceOf(j *jobs.Job) string {
	var p publish.Params
	_ = json.Unmarshal(j.Params, &p)
	return p.Nonce
}

func (s *Server) publicationView(p db.Publication) publicationView {
	p, j := s.reconcilePublication(p)
	v := publicationView{Publication: p}
	if j != nil {
		stats := s.jobq.Stats()
		jv := s.view(*j, s.snapshot(), stats.Paused, stats.Running < s.concurrency(), nil, time.Now())
		v.Job = &publishJobView{ID: j.ID, State: string(j.State), Progress: j.Progress, Attempts: j.Attempts,
			Blocked: jv.Blocked, Reason: jv.Reason, ETASeconds: jv.ETASeconds}
	}
	// The last redaction boundary: whatever was stored, a link or a token pair
	// never reaches the browser.
	v.FailDetail = publish.Redact(v.FailDetail)
	if v.State == db.PublicationFailed {
		v.Reason = &publish.Reason{Cause: publish.Cause(v.FailCause), Line: v.FailDetail}
	}
	return v
}

// ------------------------------------------------------------------- targets

type publishAccountView struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// NeedsReconnect is true when the stored grant lacks the permission to
	// post. The console shows "Reconnect to allow uploads" and sends the person
	// through the ordinary OAuth connect for ConnectPath.
	NeedsReconnect bool   `json:"needsReconnect,omitempty"`
	ConnectPath    string `json:"connectPath,omitempty"`
}

type publishPrivacyView struct {
	Default string   `json:"default"`
	Options []string `json:"options"`
	// Forced means there is no choice: the level is not editable.
	Forced bool `json:"forced"`
}

type publishPlatformView struct {
	ID          string `json:"id"`
	Enabled     bool   `json:"enabled"`
	GatedReason string `json:"gatedReason,omitempty"`
	// Configured is false until the operator has saved developer credentials,
	// which is what the connect step needs.
	Configured bool                 `json:"configured"`
	Privacy    publishPrivacyView   `json:"privacy"`
	Accounts   []publishAccountView `json:"accounts"`
	// QuotaUnits is the platform allowance one publish spends, when it has one.
	QuotaUnits int `json:"quotaUnits,omitempty"`
}

type publishTargetsView struct {
	ClipJobID int64                 `json:"clipJobId"`
	Platforms []publishPlatformView `json:"platforms"`
	// Prefill is the form's starting point, taken from the clip.
	Prefill     publish.Meta      `json:"prefill"`
	Seconds     float64           `json:"seconds"`
	Vertical    bool              `json:"vertical"`
	Publication []publicationView `json:"publications"`
}

// handlePublishTargets lists where a clip can go and what is already posted.
func (s *Server) handlePublishTargets(w http.ResponseWriter, r *http.Request) {
	clipJobID, err := parseInt64(r.URL.Query().Get("clip"))
	if err != nil || clipJobID <= 0 {
		writeError(w, http.StatusBadRequest, "name the clip with ?clip=<export job id>")
		return
	}
	file, ferr := publish.ResolveClip(s.store, s.clipExportDir(), clipJobID)
	if ferr != nil {
		code := "clip_missing"
		if errors.Is(ferr, publish.ErrNotAClip) {
			code = "not_a_clip"
		}
		writeErrorCode(w, http.StatusNotFound, code, ferr.Error())
		return
	}
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	creds := map[db.Platform]bool{}
	if list, err := s.store.ListPlatformCreds(); err == nil {
		for _, c := range list {
			creds[c.Platform] = true
		}
	}
	out := publishTargetsView{ClipJobID: clipJobID, Seconds: file.Seconds, Vertical: file.Vertical}
	out.Prefill = s.publishPrefill(clipJobID, file)
	for _, id := range []string{publish.PlatformYouTube, publish.PlatformTikTok, publish.PlatformX} {
		pv := publishPlatformView{ID: id, Enabled: true, Configured: creds[db.Platform(id)], Accounts: []publishAccountView{}}
		switch id {
		case publish.PlatformYouTube:
			pv.Privacy = publishPrivacyView{Default: publish.PrivacyPrivate,
				Options: []string{publish.PrivacyPrivate, publish.PrivacyUnlisted, publish.PrivacyPublic}}
			pv.QuotaUnits = platform.YouTubeCostUploadVideo
		case publish.PlatformTikTok:
			pv.Privacy = publishPrivacyView{Default: publish.TikTokPrivacy, Options: []string{publish.TikTokPrivacy}, Forced: true}
		case publish.PlatformX:
			pv.Privacy = publishPrivacyView{Default: publish.PrivacyPublic, Options: []string{publish.PrivacyPublic}, Forced: true}
			if !s.publishGates.XPublish {
				pv.Enabled = false
				pv.GatedReason = publish.XGateCopy
			}
		}
		for _, a := range accts {
			if string(a.Platform) != id {
				continue
			}
			av := publishAccountView{ID: a.ID, Name: a.AccountName}
			if publish.GrantFailure(id, a.Scopes) != nil {
				av.NeedsReconnect = true
				av.ConnectPath = oauthStartPath(id)
			}
			pv.Accounts = append(pv.Accounts, av)
		}
		out.Platforms = append(out.Platforms, pv)
	}
	pubs, err := s.store.ListPublicationsForClip(clipJobID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out.Publication = []publicationView{}
	for _, p := range pubs {
		out.Publication = append(out.Publication, s.publicationView(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscan(strings.TrimSpace(s), &n)
	return n, err
}

// publishPrefill takes the title from the clip request, the description from
// the highlight it was accepted from, and a Shorts tag for a vertical file.
func (s *Server) publishPrefill(clipJobID int64, file publish.File) publish.Meta {
	m := publish.Meta{Tags: []string{}}
	job, err := s.store.GetJob(clipJobID)
	if err != nil || job == nil {
		return m
	}
	var params clipper.JobParams
	if json.Unmarshal(job.Params, &params) == nil {
		m.Title = strings.TrimSpace(params.Request.Title)
		if m.Title == "" && params.Request.OutPath != "" {
			base := filepath.Base(params.Request.OutPath)
			m.Title = strings.TrimSuffix(base, filepath.Ext(base))
		}
	}
	if m.Title == "" {
		m.Title = strings.TrimSuffix(filepath.Base(file.Path), filepath.Ext(file.Path))
	}
	if rec, ok := jobs.ParseRecordingTarget(job.Target); ok {
		if hs, err := s.store.ListHighlights(rec); err == nil {
			for _, h := range hs {
				if h.JobID == job.ID && strings.TrimSpace(h.Excerpt) != "" {
					m.Description = strings.TrimSpace(h.Excerpt)
				}
			}
		}
	}
	if file.Vertical {
		m.Tags = append(m.Tags, "Shorts")
	}
	return m
}

// ------------------------------------------------------------------- prepare

type publishPrepared struct {
	ConfirmToken  string           `json:"confirmToken"`
	ExpiresAt     time.Time        `json:"expiresAt"`
	Platform      string           `json:"platform"`
	AccountID     int64            `json:"accountId"`
	AccountName   string           `json:"accountName"`
	Meta          publish.Meta     `json:"meta"`
	PrivacyForced bool             `json:"privacyForced"`
	QuotaUnits    int              `json:"quotaUnits,omitempty"`
	Existing      *publicationView `json:"existing,omitempty"`
}

// handlePreparePublish checks the form and mints the confirmation token for it.
// It sends nothing anywhere.
func (s *Server) handlePreparePublish(w http.ResponseWriter, r *http.Request) {
	var req publishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.ConfirmToken = ""
	plan, err := s.planPublish(req)
	if err != nil {
		s.writePublishError(w, err)
		return
	}
	uid, ok := sessionUser(w, r)
	if !ok {
		return
	}
	tok, exp := s.publishConfirm.issue(uid, plan.hash)
	out := publishPrepared{ConfirmToken: tok, ExpiresAt: exp, Platform: req.Platform, AccountID: req.AccountID,
		AccountName: plan.account.AccountName, Meta: plan.meta,
		PrivacyForced: req.Platform == publish.PlatformTikTok || req.Platform == publish.PlatformX}
	if req.Platform == publish.PlatformYouTube {
		out.QuotaUnits = platform.YouTubeCostUploadVideo
	}
	if pubs, err := s.store.ListPublicationsForClip(req.ClipJobID); err == nil {
		for _, ex := range pubs {
			if ex.Platform == req.Platform && ex.AccountID == req.AccountID {
				v := s.publicationView(ex)
				out.Existing = &v
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ------------------------------------------------------------------- publish

// handlePublish is the click. It redeems the confirmation token, claims the
// ledger row and queues the job -- and does nothing else.
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	var req publishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	plan, err := s.planPublish(req)
	if err != nil {
		s.writePublishError(w, err)
		return
	}
	uid, ok := sessionUser(w, r)
	if !ok {
		return
	}
	if err := s.publishConfirm.redeem(req.ConfirmToken, uid, plan.hash); err != nil {
		writeErrorCode(w, http.StatusForbidden, "confirm_required", err.Error())
		return
	}
	// A row stuck in flight (its job ended without finishing it) is failed first,
	// so this click re-arms it instead of being folded into a job that is gone.
	if rows, err := s.store.ListPublicationsForClip(req.ClipJobID); err == nil {
		for _, ex := range rows {
			if ex.Platform != req.Platform || ex.AccountID != req.AccountID {
				continue
			}
			s.reconcilePublication(ex)
		}
	}
	nonceRaw := make([]byte, 24)
	if _, err := rand.Read(nonceRaw); err != nil {
		writeError(w, http.StatusInternalServerError, "could not mint a confirmation")
		return
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)

	pub, outcome, err := s.store.ClaimPublication(db.Publication{
		ClipJobID: req.ClipJobID, Platform: req.Platform, AccountID: req.AccountID,
		AccountName: plan.account.AccountName, Title: plan.meta.Title, Description: plan.meta.Description,
		Tags: plan.meta.Tags, Privacy: plan.meta.Privacy, ContentHash: plan.hash, Nonce: nonce, ConfirmedBy: uid,
		AcknowledgeOutcome: req.AcknowledgeOutcome,
	})
	if errors.Is(err, db.ErrOutcomeUnacknowledged) {
		// The last request may have landed. Re-arming without the person having
		// looked is how a lost response becomes a second post.
		writeErrorCode(w, http.StatusConflict, "acknowledge_outcome", err.Error())
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	switch outcome {
	case db.ClaimPosted, db.ClaimInFlight:
		// Already made, or already on its way: queue nothing, so a second click,
		// a second tab and a retry all converge on one post.
		writeJSON(w, http.StatusOK, s.publicationView(pub))
		return
	}

	params, _ := json.Marshal(publish.Params{PublicationID: pub.ID, Nonce: nonce})
	submit := func() (*jobs.Job, bool, error) {
		return s.jobq.Submit(jobs.Job{
			Kind: publish.JobKind, Target: publishJobTarget(pub.ID), Params: params,
			Priority: jobs.PriorityUser, Unique: true, MaxAttempts: publishMaxAttempts,
		})
	}
	job, created, err := submit()
	if err == nil && !created && nonceOf(job) != nonce {
		// Unique folded this click into a job that is still active under an OLDER
		// click's nonce -- it would run, find the nonce dead, and do nothing, and
		// this click would never publish. Retire it and queue this click's own.
		if cerr := s.jobq.Cancel(job.ID); cerr != nil {
			s.log.Warn("could not retire a stale publish job", "job", job.ID, "err", cerr)
		}
		job, created, err = submit()
		if err == nil && !created && nonceOf(job) != nonce {
			err = errors.New("an earlier publish job is still holding this target; try again in a moment")
		}
	}
	if err != nil {
		if ferr := s.store.FailPublication(pub.ID, string(publish.CauseUnknown), "The publish job could not be queued."); ferr != nil {
			s.log.Warn("could not fail a publication whose job was not queued", "publication", pub.ID, "err", ferr)
		}
		writeStoreError(w, err)
		return
	}
	if err := s.store.SetPublicationJob(pub.ID, nonce, job.ID); err != nil {
		// Not fatal: a status read links a live job to its row by its target.
		s.log.Warn("could not link a publication to its job", "publication", pub.ID, "err", err)
	}
	pub, _ = s.store.GetPublication(pub.ID)
	writeJSON(w, http.StatusAccepted, s.publicationView(pub))
}

func (s *Server) handleGetPublication(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidID)
		return
	}
	p, err := s.store.GetPublication(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.publicationView(p))
}

// rejectPublishJobRetry refuses a generic retry of a publish job. The job is
// the continuation of one click; running it again after it stopped is a second
// publish, and that needs a second click on Publish.
func (s *Server) rejectPublishJobRetry(w http.ResponseWriter, id int64) bool {
	j, err := s.store.GetJob(id)
	if err != nil || j == nil || j.Kind != publish.JobKind {
		return false
	}
	writeErrorCode(w, http.StatusConflict, "publish_needs_click",
		"A publish cannot be retried from the jobs page. Open the clip and click Publish again.")
	return true
}

// revokePublishOnCancel turns a cancelled publish job into a failed publication,
// so cancelling cannot be undone by anything but a new click.
func (s *Server) revokePublishOnCancel(id int64) {
	j, err := s.store.GetJob(id)
	if err != nil || j == nil || j.Kind != publish.JobKind {
		return
	}
	var p publish.Params
	if json.Unmarshal(j.Params, &p) == nil && p.PublicationID > 0 {
		if err := s.store.FailPublicationFor(p.PublicationID, p.Nonce, string(publish.CauseCancelled), "Publishing was cancelled."); err != nil &&
			!errors.Is(err, db.ErrPublicationNotArmed) {
			// The job is already cancelled; the status read fails the row if this
			// write did not land.
			s.log.Warn("could not revoke a cancelled publication", "publication", p.PublicationID, "err", err)
		}
	}
}
