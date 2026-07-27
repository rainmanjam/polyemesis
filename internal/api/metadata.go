package api

// The go-live composer's server half.
//
// One title, description and category, pushed to every connected account whose
// platform can accept them. Two properties drive the whole shape of this file:
//
//   - Partial failure is the normal case. YouTube can succeed while Twitch's
//     token is stale, or a title can land while a category name turns out not
//     to exist. So the unit of reporting is one account, with the fields that
//     applied and the reason for the ones that did not — never a single
//     boolean over the whole push.
//   - A platform API can take twenty seconds. The dashboard cannot wait on it,
//     so the push is a job: POST starts it and returns immediately with every
//     account pending, and the composer polls until each row settles.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// metadataState is one account's row in the result list.
type metadataState string

const (
	metaPending metadataState = "pending"
	metaOK      metadataState = "ok"
	// metaPartial is a real outcome, not a rounding of "ok": the title landed
	// and the category did not, and the operator has to know which.
	metaPartial metadataState = "partial"
	metaError   metadataState = "error"
)

// metadataPushTimeout bounds one account's push. Generous, because YouTube's
// broadcast list plus a category lookup plus two writes is four round trips,
// and a push that gives up early looks identical to a push that failed.
const metadataPushTimeout = 60 * time.Second

// metadataJobRetention is how many finished pushes stay readable. The composer
// only ever polls the one it started; the rest are kept so a reloaded page can
// still show what the last push did.
const metadataJobRetention = 20

// metadataTarget is a connected account this push can write to.
type metadataTarget struct {
	AccountID   int64              `json:"accountId"`
	Platform    db.Platform        `json:"platform"`
	AccountName string             `json:"accountName"`
	Caps        oauth.MetadataCaps `json:"caps"`
}

// metadataOutcome is one account's result.
type metadataOutcome struct {
	AccountID   int64       `json:"accountId"`
	Platform    db.Platform `json:"platform"`
	AccountName string      `json:"accountName"`

	State metadataState `json:"state"`
	// Message is empty on a clean success and is otherwise the whole
	// explanation, written for someone who has thirty seconds before going
	// live.
	Message  string                `json:"message,omitempty"`
	Applied  []oauth.MetadataField `json:"applied"`
	Skipped  []oauth.MetadataField `json:"skipped,omitempty"`
	Target   string                `json:"target,omitempty"`
	Category string                `json:"category,omitempty"`
	Warnings []string              `json:"warnings,omitempty"`

	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// metadataJob is one press of "Push".
type metadataJob struct {
	ID         string            `json:"id"`
	Metadata   oauth.Metadata    `json:"metadata"`
	StartedAt  time.Time         `json:"startedAt"`
	FinishedAt *time.Time        `json:"finishedAt,omitempty"`
	Done       bool              `json:"done"`
	Results    []metadataOutcome `json:"results"`
}

// metadataJobs is the in-process job registry. Nothing here outlives the
// process on purpose: a metadata push is a fire-and-report action whose lasting
// effect lives on the platforms, not in our database, and a restart mid-push
// leaves nothing to reconcile.
type metadataJobs struct {
	mu    sync.Mutex
	byID  map[string]*metadataJob
	order []string
	last  string
}

var metadataRegistry = &metadataJobs{byID: map[string]*metadataJob{}}

func (j *metadataJobs) add(job *metadataJob) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.byID[job.ID] = job
	j.order = append(j.order, job.ID)
	j.last = job.ID
	for len(j.order) > metadataJobRetention {
		delete(j.byID, j.order[0])
		j.order = j.order[1:]
	}
}

// with runs fn under the lock. Every mutation of a live job goes through here,
// because the worker goroutines write results while an HTTP handler is
// encoding them.
func (j *metadataJobs) with(id string, fn func(*metadataJob)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if job, ok := j.byID[id]; ok {
		fn(job)
	}
}

// snapshot returns a copy safe to hand to the JSON encoder outside the lock.
func (j *metadataJobs) snapshot(id string) (metadataJob, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.byID[id]
	if !ok {
		return metadataJob{}, false
	}
	out := *job
	out.Results = append([]metadataOutcome(nil), job.Results...)
	return out, true
}

func (j *metadataJobs) latest() (metadataJob, bool) {
	j.mu.Lock()
	id := j.last
	j.mu.Unlock()
	if id == "" {
		return metadataJob{}, false
	}
	return j.snapshot(id)
}

// ------------------------------------------------------------------ handlers

// handleMetadataOverview answers "what can I push to, and what happened last
// time" in one read, so the composer renders complete on first paint.
func (s *Server) handleMetadataOverview(w http.ResponseWriter, r *http.Request) {
	targets, err := s.metadataTargets()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	body := map[string]any{"targets": targets}
	if last, ok := metadataRegistry.latest(); ok {
		body["last"] = last
	}
	writeJSON(w, http.StatusOK, body)
}

// metadataTargets lists connected accounts on platforms that implement the
// capability. A platform that cannot do this is absent rather than present and
// failing — see oauth.MetadataFor.
func (s *Server) metadataTargets() ([]metadataTarget, error) {
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		return nil, err
	}
	out := []metadataTarget{}
	for _, a := range accts {
		mp, ok := oauth.MetadataFor(a.Platform)
		if !ok {
			continue
		}
		out = append(out, metadataTarget{
			AccountID:   a.ID,
			Platform:    a.Platform,
			AccountName: a.AccountName,
			Caps:        mp.MetadataCaps(),
		})
	}
	return out, nil
}

// handlePushMetadata starts a push and returns 202 with every account pending.
// It never waits on a platform: one slow API would otherwise hold the
// dashboard's request open for the length of the slowest of them.
func (s *Server) handlePushMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title       string  `json:"title"`
		Description string  `json:"description"`
		Category    string  `json:"category"`
		AccountIDs  []int64 `json:"accountIds"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	meta := oauth.Metadata{
		Title:       req.Title,
		Description: req.Description,
		Category:    req.Category,
	}.Trimmed()
	if meta.Empty() {
		writeError(w, http.StatusBadRequest,
			"enter a title, a description or a category before pushing")
		return
	}

	targets, err := s.metadataTargets()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if len(req.AccountIDs) > 0 {
		want := map[int64]bool{}
		for _, id := range req.AccountIDs {
			want[id] = true
		}
		filtered := targets[:0]
		for _, t := range targets {
			if want[t.AccountID] {
				filtered = append(filtered, t)
			}
		}
		targets = filtered
	}
	if len(targets) == 0 {
		writeError(w, http.StatusPreconditionFailed,
			"no connected account can receive stream metadata. Connect a YouTube or Twitch "+
				"account in Settings → Platforms first.")
		return
	}

	id, err := auth.RandomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	job := &metadataJob{
		ID:        id,
		Metadata:  meta,
		StartedAt: time.Now(),
		Results:   make([]metadataOutcome, len(targets)),
	}
	for i, t := range targets {
		job.Results[i] = metadataOutcome{
			AccountID:   t.AccountID,
			Platform:    t.Platform,
			AccountName: t.AccountName,
			State:       metaPending,
			Applied:     []oauth.MetadataField{},
		}
	}
	metadataRegistry.add(job)

	// Detached from the request context on purpose: the response returns in
	// milliseconds and would otherwise cancel the very work it just started.
	go s.runMetadataPush(job.ID, meta, targets)

	snap, _ := metadataRegistry.snapshot(job.ID)
	writeJSON(w, http.StatusAccepted, snap)
}

func (s *Server) handleMetadataJob(w http.ResponseWriter, r *http.Request) {
	snap, ok := metadataRegistry.snapshot(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such metadata push")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// precheck answers what this platform will not take before a single byte goes
// over the wire, and returns the fields it will silently ignore plus the one
// problem worth refusing over.
//
// A field the platform does not have is skipped, not failed: the composer
// already said Twitch has no description, and colouring it red would train the
// operator to ignore the colour. A documented character limit, on the other
// hand, is arithmetic rather than a probe of anything, so checking it here
// costs nothing and beats a platform 400 that blames the request body.
func precheck(meta oauth.Metadata, platform db.Platform, caps oauth.MetadataCaps) (skipped []oauth.MetadataField, problem string) {
	for _, f := range []struct {
		field oauth.MetadataField
		want  string
	}{
		{oauth.FieldTitle, meta.Title},
		{oauth.FieldDescription, meta.Description},
		{oauth.FieldCategory, meta.Category},
	} {
		if f.want != "" && !caps.Accepts(f.field) {
			skipped = append(skipped, f.field)
		}
	}

	if n := utf8.RuneCountInString(meta.Title); caps.TitleMax > 0 && n > caps.TitleMax {
		return skipped, fmt.Sprintf("%s titles are limited to %d characters; yours is %d.",
			platform, caps.TitleMax, n)
	}
	if n := utf8.RuneCountInString(meta.Description); caps.DescriptionMax > 0 && n > caps.DescriptionMax {
		return skipped, fmt.Sprintf("%s descriptions are limited to %d characters; yours is %d.",
			platform, caps.DescriptionMax, n)
	}
	return skipped, ""
}

// mergeFields unions two field lists, preserving the order of the first.
func mergeFields(a, b []oauth.MetadataField) []oauth.MetadataField {
	seen := make(map[oauth.MetadataField]bool, len(a)+len(b))
	out := make([]oauth.MetadataField, 0, len(a)+len(b))
	for _, f := range append(append([]oauth.MetadataField{}, a...), b...) {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func fieldList(fields []oauth.MetadataField) string {
	if len(fields) == 0 {
		return "nothing it accepts"
	}
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = string(f)
	}
	return strings.Join(parts, ", ")
}

// ------------------------------------------------------------------- worker

// runMetadataPush pushes to every target concurrently. Concurrency is the
// whole point: two platforms that each take fifteen seconds should cost
// fifteen seconds, and a wedged one must not delay the report for a healthy one.
func (s *Server) runMetadataPush(jobID string, meta oauth.Metadata, targets []metadataTarget) {
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t metadataTarget) {
			defer wg.Done()
			out := s.pushOne(meta, t)
			now := time.Now()
			out.FinishedAt = &now
			metadataRegistry.with(jobID, func(j *metadataJob) { j.Results[i] = out })
		}(i, t)
	}
	wg.Wait()

	now := time.Now()
	metadataRegistry.with(jobID, func(j *metadataJob) {
		j.Done = true
		j.FinishedAt = &now
	})
}

// pushOne is one account's whole story: refresh the token, call the platform,
// and turn whatever came back into a row a human can act on.
func (s *Server) pushOne(meta oauth.Metadata, t metadataTarget) metadataOutcome {
	out := metadataOutcome{
		AccountID:   t.AccountID,
		Platform:    t.Platform,
		AccountName: t.AccountName,
		Applied:     []oauth.MetadataField{},
	}

	skipped, tooLong := precheck(meta, t.Platform, t.Caps)
	out.Skipped = skipped
	if tooLong != "" {
		out.State = metaError
		out.Message = tooLong
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), metadataPushTimeout)
	defer cancel()

	acct, err := s.tokenFor(ctx, t.AccountID)
	if err != nil {
		out.State = metaError
		out.Message = err.Error()
		return out
	}
	creds, err := s.store.GetPlatformCreds(s.box, t.Platform)
	if err != nil {
		out.State = metaError
		out.Message = fmt.Sprintf("developer credentials for %s are missing; add them in Settings → Platform credentials.", t.Platform)
		return out
	}
	pusher, ok := oauth.MetadataFor(t.Platform)
	if !ok {
		out.State = metaError
		out.Message = fmt.Sprintf("%s cannot receive stream metadata", t.Platform)
		return out
	}

	res, err := pusher.PushMetadata(ctx, creds.ClientID, acct.AccessToken, acct.AccountRef, meta)
	if err != nil {
		out.State = metaError
		out.Message = err.Error()
		s.log.Warn("metadata push failed", "platform", t.Platform, "account", t.AccountName, "err", err)
		return out
	}

	out.Applied = res.Applied
	if out.Applied == nil {
		out.Applied = []oauth.MetadataField{}
	}
	// The provider reports the same unsupported fields we predicted from its
	// caps, so merge rather than concatenate.
	out.Skipped = mergeFields(out.Skipped, res.Skipped)
	// A platform that edits the channel itself has no sub-object to name, so
	// the row falls back to naming the account it wrote to.
	out.Target = res.Target
	if out.Target == "" {
		out.Target = t.AccountName
	}
	out.Category = res.Category
	out.Warnings = res.Warnings

	switch {
	case len(out.Applied) == 0:
		out.State = metaError
		out.Message = fmt.Sprintf("%s took none of what you entered (%s).",
			t.Platform, fieldList(out.Skipped))
	case len(out.Warnings) > 0 || len(out.Skipped) > 0:
		out.State = metaPartial
	default:
		out.State = metaOK
	}
	s.log.Info("metadata pushed", "platform", t.Platform, "account", t.AccountName,
		"state", out.State, "applied", len(out.Applied))
	return out
}
