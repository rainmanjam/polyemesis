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
	"reflect"
	"sort"
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

	// Compliance is the obligation metadata resolved from this account's
	// destinations -- see complianceByAccount. Zero when no destination on this
	// account set any, which means "leave it alone" rather than "clear it".
	Compliance db.Compliance `json:"compliance,omitempty"`
	// StreamKey comes from the destination the compliance was resolved from,
	// because Facebook recovers its live video id from the key rather than from
	// anything the account knows. Never serialised: this response reaches a
	// browser, and a stream key in it is a credential in a page.
	StreamKey string `json:"-"`
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
	// Conflicts are ignored here on purpose: this endpoint reports what CAN be
	// pushed to, and a disagreement between two destinations does not remove an
	// account from that list. The push itself is where it has to be refused.
	targets, _, err := s.metadataTargets()
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
// capability, each carrying the compliance resolved from its destinations. A
// platform that cannot do this is absent rather than present and failing — see
// oauth.MetadataFor.
//
// The conflicts return is not advisory. Two destinations on one account asking
// for different compliance have no answer, so complianceByAccount deletes that
// account from the map and names both destinations here; a caller that pushes
// anyway sends one of them with nothing saying which.
func (s *Server) metadataTargets() ([]metadataTarget, []string, error) {
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		return nil, nil, err
	}
	dests, err := s.store.ListDestinations()
	if err != nil {
		return nil, nil, err
	}
	flat := make([]db.Destination, 0, len(dests))
	for _, d := range dests {
		flat = append(flat, *d)
	}
	compliance, conflicts := complianceByAccount(flat)

	out := []metadataTarget{}
	for _, a := range accts {
		mp, ok := oauth.MetadataFor(a.Platform)
		if !ok {
			continue
		}
		// A conflicting account is absent from the map, so this reads the zero
		// Compliance -- "touch nothing" -- rather than the first destination's.
		// That is a second line of defence, not the refusal: handlePushMetadata
		// still has to check conflicts, because a push that quietly writes
		// nothing looks exactly like a push that worked.
		ac := compliance[a.ID]
		out = append(out, metadataTarget{
			AccountID:   a.ID,
			Platform:    a.Platform,
			AccountName: a.AccountName,
			Caps:        mp.MetadataCaps(),
			Compliance:  ac.Compliance,
			StreamKey:   ac.StreamKey,
		})
	}
	return out, conflicts, nil
}

// handlePushMetadata starts a push and returns 202 with every account pending.
// It never waits on a platform: one slow API would otherwise hold the
// dashboard's request open for the length of the slowest of them.
func (s *Server) handlePushMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Category    string `json:"category"`
		// Tags are words, resolved by whichever provider's PushMetadata knows
		// how to turn them into that platform's own id space -- Facebook's
		// content_tags, for one. Distinct from Broadcast.Tags below: that one
		// travels through PushBroadcastSettings, which only YouTube and Kick
		// implement, so a provider with no broadcast resource -- Facebook --
		// would never see a tag typed into that field.
		Tags       []string `json:"tags,omitempty"`
		AccountIDs []int64  `json:"accountIds"`
		// Broadcast is the YouTube-only settings that live on a different
		// resource: tags, the scheduled start, and the contentDetails toggles.
		// Every field is a pointer, so an omitted one means "leave it alone"
		// and an explicit false means "turn it off". See
		// oauth/youtube_broadcast.go.
		Broadcast oauth.BroadcastSettings `json:"broadcast"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	meta := oauth.Metadata{
		Title:       req.Title,
		Description: req.Description,
		Category:    req.Category,
		Tags:        req.Tags,
	}.Trimmed()
	// Either half is enough to be worth pushing. A broadcast-only push is a
	// real thing an operator does -- turning the DVR off before going live
	// without retyping a title that is already correct.
	if meta.Empty() && req.Broadcast.Empty() {
		writeError(w, http.StatusBadRequest,
			"enter a title, a description, a category or a broadcast setting before pushing")
		return
	}

	targets, conflicts, err := s.metadataTargets()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Before the job exists, before a token is read, before anything leaves the
	// process. A conflict refused later would be a refusal reported to the
	// operator while one destination's compliance was already on the wire.
	if len(conflicts) > 0 {
		writeError(w, http.StatusBadRequest, strings.Join(conflicts, " "))
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
	go s.runMetadataPush(job.ID, meta, req.Broadcast, targets)

	snap, _ := metadataRegistry.snapshot(job.ID)
	writeJSON(w, http.StatusAccepted, snap)
}

// broadcastWindower reports what is still editable on an account's current
// broadcast. Optional for the same reason broadcastPusher is.
type broadcastWindower interface {
	BroadcastWindow(ctx context.Context, accessToken string) (*oauth.BroadcastWindow, error)
}

// broadcastWindowRow is one account's answer, including the ones that failed.
//
// A failure is a row rather than an error on the whole response: an operator
// with two YouTube accounts and an expired token on one still needs the
// controls enabled for the other.
type broadcastWindowRow struct {
	AccountID   int64       `json:"accountId"`
	Platform    db.Platform `json:"platform"`
	AccountName string      `json:"accountName"`
	// Window is nil when this account could not be read, or when the platform
	// has no broadcast resource at all.
	Window *oauth.BroadcastWindow `json:"window,omitempty"`
	// Supported is false for a platform with no broadcast concept, which the
	// composer shows differently from an error: Twitch is not broken, it
	// simply has no DVR toggle.
	Supported bool   `json:"supported"`
	Error     string `json:"error,omitempty"`
}

// handleBroadcastWindow answers "what can still be changed" BEFORE the operator
// edits anything.
//
// This is the whole point of the disable-when-locked design: YouTube freezes
// the contentDetails toggles once a broadcast leaves created/ready, and an
// operator who only discovers that from a 403 discovers it mid-broadcast.
//
// It is deliberately NOT on any hot path. Each row is a live API call, so this
// is fetched when the composer opens rather than polled, and a failure here
// disables nothing -- the write still happens and the 403 is still the
// authority.
func (s *Server) handleBroadcastWindow(w http.ResponseWriter, r *http.Request) {
	// Conflicts are not this endpoint's business either: what is still editable
	// on a broadcast does not depend on which compliance values were stored.
	targets, _, err := s.metadataTargets()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	rows := make([]broadcastWindowRow, 0, len(targets))
	for _, t := range targets {
		row := broadcastWindowRow{
			AccountID:   t.AccountID,
			Platform:    t.Platform,
			AccountName: t.AccountName,
		}
		pusher, ok := oauth.MetadataFor(t.Platform)
		if !ok {
			rows = append(rows, row)
			continue
		}
		wr, ok := pusher.(broadcastWindower)
		if !ok {
			// Not an error. This platform has no broadcast resource.
			rows = append(rows, row)
			continue
		}
		row.Supported = true

		ctx, cancel := context.WithTimeout(r.Context(), metadataPushTimeout)
		acct, err := s.tokenFor(ctx, t.AccountID)
		if err != nil {
			row.Error = err.Error()
			cancel()
			rows = append(rows, row)
			continue
		}
		win, err := wr.BroadcastWindow(ctx, acct.AccessToken)
		cancel()
		if err != nil {
			// A channel with no upcoming broadcast lands here, which is an
			// ordinary state rather than a fault: the operator has not
			// scheduled one yet.
			row.Error = err.Error()
			rows = append(rows, row)
			continue
		}
		row.Window = win
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": rows})
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

// accountCompliance is one account's resolved compliance, and the destination
// it came from so a message can name it.
type accountCompliance struct {
	Compliance  db.Compliance
	StreamKey   string
	Destination string
}

// complianceByAccount resolves per-destination compliance onto the accounts a
// push actually addresses, and reports disagreements rather than resolving
// them.
//
// The mismatch is real and not incidental: compliance is stored per
// DESTINATION, a push is per ACCOUNT, and a compliance write targets whatever
// the token owns -- YouTube's takes no account reference at all. So two
// destinations on one account with different values are asking one broadcast to
// be two things, and picking one would discard a COPPA declaration with nothing
// anywhere saying so.
//
// Destinations with no account are skipped: a hand-typed key has no token to
// push with. Destinations with empty compliance contribute nothing and never
// conflict, because "not set" is not a disagreement with anything.
func complianceByAccount(dests []db.Destination) (map[int64]accountCompliance, []string) {
	sorted := append([]db.Destination(nil), dests...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	out := map[int64]accountCompliance{}
	var conflicts []string
	for _, d := range sorted {
		if d.AccountID == nil || d.Compliance.Empty() {
			continue
		}
		id := *d.AccountID
		prev, seen := out[id]
		if !seen {
			out[id] = accountCompliance{
				Compliance: d.Compliance, StreamKey: d.StreamKey, Destination: d.Name,
			}
			continue
		}
		if !reflect.DeepEqual(prev.Compliance, d.Compliance) {
			conflicts = append(conflicts, fmt.Sprintf(
				"%q and %q share one connected account but ask for different compliance "+
					"settings, and the platform has only one broadcast to apply them to. "+
					"Make them match, or point one at a different account.",
				prev.Destination, d.Name))
			delete(out, id)
		}
	}
	return out, conflicts
}

// complianceFields names the fields a stored compliance actually asked for, so
// a failed write reports only what the operator set. Naming all three would
// tell someone who never touched COPPA that their COPPA declaration was
// skipped, which is a fault report for a field they do not use.
func complianceFields(c db.Compliance) []oauth.MetadataField {
	var out []oauth.MetadataField
	// FacebookPrivacy joins FieldPrivacy: both are "who may see this", and the
	// composer has one row for it.
	if c.Privacy != db.PrivacyUnchanged || c.FacebookPrivacy != db.FBPrivacyUnchanged {
		out = append(out, oauth.FieldPrivacy)
	}
	if c.MadeForKids != nil {
		out = append(out, oauth.FieldMadeForKids)
	}
	if len(c.Labels) > 0 {
		out = append(out, oauth.FieldLabels)
	}
	return out
}

// ------------------------------------------------------------------- worker

// runMetadataPush pushes to every target concurrently. Concurrency is the
// whole point: two platforms that each take fifteen seconds should cost
// fifteen seconds, and a wedged one must not delay the report for a healthy one.
func (s *Server) runMetadataPush(jobID string, meta oauth.Metadata, bc oauth.BroadcastSettings, targets []metadataTarget) {
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t metadataTarget) {
			defer wg.Done()
			out := s.pushOne(meta, bc, t)
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

// broadcastPusher is the optional half of a metadata provider: the settings
// that live on a broadcast rather than on a channel.
//
// Optional on purpose. Only YouTube has a broadcast resource with a scheduled
// start, a DVR flag and an editing window; putting these on the shared
// MetadataPusher interface would force Twitch and Kick to carry stubs whose
// only behaviour is to refuse.
type broadcastPusher interface {
	PushBroadcastSettings(ctx context.Context, clientID, accessToken string, s oauth.BroadcastSettings) (*oauth.MetadataResult, error)
}

// pushOne is one account's whole story: refresh the token, call the platform,
// and turn whatever came back into a row a human can act on.
func (s *Server) pushOne(meta oauth.Metadata, bc oauth.BroadcastSettings, t metadataTarget) metadataOutcome {
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

	// A broadcast-only push skips this entirely rather than sending an empty
	// Metadata. PushMetadata on an empty block would either write nothing or,
	// worse on a destructive-by-part API, blank the fields it was handed
	// nothing for.
	res := &oauth.MetadataResult{}
	if !meta.Empty() {
		var err error
		res, err = s.pushMetadataFn(ctx, pusher, creds.ClientID, acct.AccessToken, acct.AccountRef, meta)
		if err != nil {
			out.State = metaError
			out.Message = err.Error()
			s.log.Warn("metadata push failed", "platform", t.Platform, "account", t.AccountName, "err", err)
			return out
		}
	}

	// The broadcast settings, where the platform has them. YouTube is the only
	// one today; Twitch has no equivalent and Kick's entire surface is three
	// fields. A type assertion rather than a method on the shared interface,
	// so a platform that never grows these does not carry a stub that returns
	// "unsupported" for every operator who tries.
	if !bc.Empty() {
		if bp, ok := pusher.(broadcastPusher); ok {
			bres, err := bp.PushBroadcastSettings(ctx, creds.ClientID, acct.AccessToken, bc)
			switch {
			case err != nil:
				// Reported, not fatal. The metadata write above may already
				// have landed, and failing the whole row would send the
				// operator back to redo work that took.
				out.Warnings = append(out.Warnings, err.Error())
				out.Skipped = mergeFields(out.Skipped, []oauth.MetadataField{oauth.FieldContentDetails})
			case bres != nil:
				res.Applied = append(res.Applied, bres.Applied...)
				res.Skipped = append(res.Skipped, bres.Skipped...)
				res.Warnings = append(res.Warnings, bres.Warnings...)
				if res.Target == "" {
					res.Target = bres.Target
				}
			}
		} else {
			// Named rather than silently dropped: an operator who set a DVR
			// toggle and saw nothing happen on Twitch deserves to know the
			// platform has no such thing.
			out.Skipped = mergeFields(out.Skipped, []oauth.MetadataField{
				oauth.FieldScheduledStart, oauth.FieldContentDetails, oauth.FieldTags,
			})
		}
	}

	// The compliance write: the operator's stored privacy, COPPA declaration and
	// content labels. This is the call the whole feature was missing -- the
	// capability existed and was tested for a full release while nothing
	// invoked it, so the settings were editable and unsendable at the same time.
	//
	// ComplianceFor rather than a type assertion, and absence rather than an
	// error: Kick has no compliance API, and a red row for a field the platform
	// does not have teaches the operator to ignore red rows.
	if !t.Compliance.Empty() {
		if cp, ok := oauth.ComplianceFor(t.Platform); ok {
			cres, err := s.pushComplianceFn(ctx, cp, creds.ClientID, acct.AccessToken,
				oauth.ComplianceTarget{AccountRef: acct.AccountRef, StreamKey: t.StreamKey},
				t.Compliance)
			switch {
			case err != nil:
				// Reported, not fatal, for the same reason the broadcast half is:
				// the metadata write above may already have landed, and failing
				// the whole row would send the operator back to redo work that
				// took. Merged into res so it survives the assignments below.
				res.Warnings = append(res.Warnings, err.Error())
				res.Skipped = append(res.Skipped, complianceFields(t.Compliance)...)
				s.log.Warn("compliance push failed", "platform", t.Platform,
					"account", t.AccountName, "err", err)
			case cres != nil:
				res.Applied = append(res.Applied, cres.Applied...)
				res.Skipped = append(res.Skipped, cres.Skipped...)
				res.Warnings = append(res.Warnings, cres.Warnings...)
				if res.Target == "" {
					res.Target = cres.Target
				}
			}
		}
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
