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

func (s *Server) publicationView(p db.Publication) publicationView {
	v := publicationView{Publication: p}
	if p.State == db.PublicationConfirmed || p.State == db.PublicationUploading {
		if p.PublishJobID > 0 {
			j, err := s.store.GetJob(p.PublishJobID)
			switch {
			case err != nil || j == nil:
				_ = s.store.FailPublication(p.ID, string(publish.CauseCancelled), "The publish job is gone.")
				p, _ = s.store.GetPublication(p.ID)
				v.Publication = p
			case j.State == jobs.StateCancelled:
				_ = s.store.FailPublication(p.ID, string(publish.CauseCancelled), "Publishing was cancelled.")
				p, _ = s.store.GetPublication(p.ID)
				v.Publication = p
			default:
				stats := s.jobq.Stats()
				jv := s.view(*j, s.snapshot(), stats.Paused, stats.Running < s.concurrency(), nil, time.Now())
				v.Job = &publishJobView{ID: j.ID, State: string(j.State), Progress: j.Progress, Attempts: j.Attempts,
					Blocked: jv.Blocked, Reason: jv.Reason, ETASeconds: jv.ETASeconds}
			}
		}
	}
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
			pv.QuotaUnits = 1600
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
				av.ConnectPath = "/api/v1/oauth/" + id + "/start"
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
	p, _ := principalFrom(r.Context())
	tok, exp := s.publishConfirm.issue(p.userID, plan.hash)
	out := publishPrepared{ConfirmToken: tok, ExpiresAt: exp, Platform: req.Platform, AccountID: req.AccountID,
		AccountName: plan.account.AccountName, Meta: plan.meta,
		PrivacyForced: req.Platform == publish.PlatformTikTok || req.Platform == publish.PlatformX}
	if req.Platform == publish.PlatformYouTube {
		out.QuotaUnits = 1600
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
	p, _ := principalFrom(r.Context())
	if err := s.publishConfirm.redeem(req.ConfirmToken, p.userID, plan.hash); err != nil {
		writeErrorCode(w, http.StatusForbidden, "confirm_required", err.Error())
		return
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
		Tags: plan.meta.Tags, Privacy: plan.meta.Privacy, ContentHash: plan.hash, Nonce: nonce, ConfirmedBy: p.userID,
	})
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
	job, _, err := s.jobq.Submit(jobs.Job{
		Kind: publish.JobKind, Target: fmt.Sprintf("clip-publish:%d", pub.ID), Params: params,
		Priority: jobs.PriorityUser, Unique: true, MaxAttempts: publishMaxAttempts,
	})
	if err != nil {
		_ = s.store.FailPublication(pub.ID, string(publish.CauseUnknown), "The publish job could not be queued.")
		writeStoreError(w, err)
		return
	}
	_ = s.store.SetPublicationJob(pub.ID, nonce, job.ID)
	pub, _ = s.store.GetPublication(pub.ID)
	writeJSON(w, http.StatusAccepted, s.publicationView(pub))
}

func (s *Server) handleGetPublication(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
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
		_ = s.store.FailPublication(p.PublicationID, string(publish.CauseCancelled), "Publishing was cancelled.")
	}
}
