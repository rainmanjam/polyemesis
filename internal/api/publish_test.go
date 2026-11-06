package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/platform"
	"github.com/rainmanjam/polyemesis/internal/publish"
)

const ytUploadScope = "https://www.googleapis.com/auth/youtube.upload"

// pubFixture is a server with a queue (no workers: nothing runs), one finished
// vertical clip export on disk, and one connected YouTube account whose grant
// allows uploads.
type pubFixture struct {
	h    http.Handler
	sign func(*http.Request)
	srv  *Server
	q    *jobs.Queue
	clip int64
	yt   int64
}

func newPubFixture(t *testing.T) *pubFixture {
	t.Helper()
	h, sign, srv, q := playlistJobServer(t)
	dir := srv.clipExportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "short-1.mp4")
	if err := os.WriteFile(path, []byte("MP4BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	job := seedDoneClipExport(t, srv.store, path)
	raw, _ := json.Marshal(clipper.JobResult{Path: path, Mode: clipper.ModeFast, Seconds: 20, Vertical: true})
	if err := srv.store.SetJobResult(job.ID, raw, time.Now()); err != nil {
		t.Fatal(err)
	}
	f := &pubFixture{h: h, sign: sign, srv: srv, q: q, clip: job.ID}
	f.yt = f.account(t, db.PlatformYouTube, "My Channel", ytUploadScope)
	return f
}

func (f *pubFixture) account(t *testing.T, p db.Platform, name, scopes string) int64 {
	t.Helper()
	a, err := f.srv.store.UpsertPlatformAccount(f.srv.box, &db.PlatformAccount{
		Platform: p, AccountName: name, AccountRef: name + "-ref", AccessToken: "placeholder-access",
		RefreshToken: "placeholder-refresh", ExpiresAt: time.Now().Add(time.Hour), Scopes: scopes, ScopeVer: 1})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func (f *pubFixture) form(platform string, account int64) map[string]any {
	return map[string]any{"clipJobId": f.clip, "platform": platform, "accountId": account,
		"title": "My short", "description": "words", "tags": []string{"fun"}, "privacy": "private"}
}

func (f *pubFixture) prepare(t *testing.T, form map[string]any, want int) []byte {
	return send(t, f.h, f.sign, http.MethodPost, "/api/v1/publish/prepare", form, want)
}

// click does what the console does: prepare, then Publish with the token.
func (f *pubFixture) click(t *testing.T, form map[string]any, want int) []byte {
	t.Helper()
	var prep struct {
		ConfirmToken string `json:"confirmToken"`
	}
	decodeInto(t, f.prepare(t, form, http.StatusOK), &prep)
	body := map[string]any{}
	for k, v := range form {
		body[k] = v
	}
	body["confirmToken"] = prep.ConfirmToken
	return send(t, f.h, f.sign, http.MethodPost, "/api/v1/publish", body, want)
}

func (f *pubFixture) publishJobs(t *testing.T) []jobs.Job {
	return jobsOfKind(t, f.srv.store, publish.JobKind)
}

func (f *pubFixture) rows(t *testing.T) []db.Publication {
	t.Helper()
	rows, err := f.srv.store.ListPublicationsForClip(f.clip)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// ---------------------------------------------------------------- the click

// NO TOKEN OF ANY SCOPE CAN REACH ANY PUBLISH ROUTE. This is the API half of
// "never without a click": a script, an MCP client or a leaked admin token has
// no way to start, prepare or even look at a publish.
func TestNoAPITokenScopeReachesAPublishRoute(t *testing.T) {
	f := newPubFixture(t)
	for _, scope := range []string{db.ScopeAdmin, db.ScopeRead, db.ScopeMCPAdmin, db.ScopeMCPRead, db.ScopeOBS} {
		tok := createScopedToken(t, f.h, f.sign, "pub-"+scope, scope)
		for _, rt := range []struct{ method, path string }{
			{"POST", "/api/v1/publish/prepare"}, {"POST", "/api/v1/publish"},
			{"GET", "/api/v1/publish/targets?clip=" + strconv.FormatInt(f.clip, 10)}, {"GET", "/api/v1/publish/1"},
		} {
			r := jsonRequest(t, rt.method, rt.path, f.form("youtube", f.yt))
			bearer(tok)(r)
			w := do(t, f.h, r)
			if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
				t.Errorf("%s token: %s %s answered %d, want 401/403", scope, rt.method, rt.path, w.Code)
			}
		}
	}
	if n := len(f.publishJobs(t)); n != 0 || len(f.rows(t)) != 0 {
		t.Fatalf("a token reached the publish path: %d jobs, %d rows", n, len(f.rows(t)))
	}
}

func TestPublishWithoutAConfirmationQueuesNothing(t *testing.T) {
	f := newPubFixture(t)
	form := f.form("youtube", f.yt)
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/publish", form, http.StatusForbidden) // no token at all
	form["confirmToken"] = "made-up"
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/publish", form, http.StatusForbidden)
	if len(f.publishJobs(t)) != 0 || len(f.rows(t)) != 0 {
		t.Fatalf("an unconfirmed publish left a job or a row")
	}
}

func TestAConfirmationOnlyCoversTheFormItWasMintedFor(t *testing.T) {
	f := newPubFixture(t)
	var prep struct {
		ConfirmToken string `json:"confirmToken"`
	}
	decodeInto(t, f.prepare(t, f.form("youtube", f.yt), http.StatusOK), &prep)
	edited := f.form("youtube", f.yt)
	edited["title"] = "A different title the person never reviewed"
	edited["confirmToken"] = prep.ConfirmToken
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/publish", edited, http.StatusForbidden)
	if len(f.publishJobs(t)) != 0 {
		t.Fatal("edited content was published on a stale confirmation")
	}
	// The mismatch burnt the token: even the original form cannot use it now.
	orig := f.form("youtube", f.yt)
	orig["confirmToken"] = prep.ConfirmToken
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/publish", orig, http.StatusForbidden)
}

func TestAConfirmationWorksOnce(t *testing.T) {
	f := newPubFixture(t)
	var prep struct {
		ConfirmToken string `json:"confirmToken"`
	}
	decodeInto(t, f.prepare(t, f.form("youtube", f.yt), http.StatusOK), &prep)
	form := f.form("youtube", f.yt)
	form["confirmToken"] = prep.ConfirmToken
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/publish", form, http.StatusAccepted)
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/publish", form, http.StatusForbidden)
}

func TestAConfirmedClickQueuesOneJobThatCarriesNoSecret(t *testing.T) {
	f := newPubFixture(t)
	body := f.click(t, f.form("youtube", f.yt), http.StatusAccepted)
	var v publicationView
	decodeInto(t, body, &v)
	if v.State != db.PublicationConfirmed || v.IdemKey == "" || v.Privacy != "private" {
		t.Fatalf("view = %+v", v)
	}
	list := f.publishJobs(t)
	if len(list) != 1 {
		t.Fatalf("jobs = %d, want 1", len(list))
	}
	params := string(list[0].Params)
	for _, banned := range []string{"placeholder-access", "placeholder-refresh", "http", "token", "session", "path"} {
		if strings.Contains(strings.ToLower(params), banned) {
			t.Errorf("job params carry %q: %s", banned, params)
		}
	}
	if strings.Contains(string(body), "nonce") {
		t.Errorf("the response exposes the nonce: %s", body)
	}
}

// A second click -- a double click, a second tab -- converges on the same post.
func TestASecondClickOnTheSameTargetQueuesNothingNew(t *testing.T) {
	f := newPubFixture(t)
	f.click(t, f.form("youtube", f.yt), http.StatusAccepted)
	f.click(t, f.form("youtube", f.yt), http.StatusOK)
	if len(f.publishJobs(t)) != 1 || len(f.rows(t)) != 1 {
		t.Fatalf("jobs=%d rows=%d, want 1/1", len(f.publishJobs(t)), len(f.rows(t)))
	}
}

func TestAClipAlreadyPostedIsNeverQueuedAgain(t *testing.T) {
	f := newPubFixture(t)
	var v publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusAccepted), &v)
	if err := f.srv.store.RecordPublicationPost(v.ID, "vid9", "https://youtu.be/vid9", "private"); err != nil {
		t.Fatal(err)
	}
	var again publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusOK), &again)
	if again.State != db.PublicationPosted || again.PlatformPostID != "vid9" {
		t.Fatalf("view = %+v", again)
	}
	if len(f.publishJobs(t)) != 1 {
		t.Fatalf("a posted clip queued another job")
	}
}

// A job cannot be re-run from the jobs page; only a new Publish click re-arms a
// failed one, and the old job's nonce stays dead.
func TestAFailedPublishNeedsANewClickAndTheJobsPageCannotRetryIt(t *testing.T) {
	f := newPubFixture(t)
	var v publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusAccepted), &v)
	old := f.publishJobs(t)[0]
	if err := f.srv.store.FailPublication(v.ID, "quota_exceeded", "out of quota"); err != nil {
		t.Fatal(err)
	}
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(old.ID, 10)+"/retry", nil, http.StatusConflict)

	var rearmed publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusAccepted), &rearmed)
	if rearmed.ID != v.ID || rearmed.State != db.PublicationConfirmed {
		t.Fatalf("rearmed = %+v", rearmed)
	}
	var oldParams publish.Params
	_ = json.Unmarshal(old.Params, &oldParams)
	if _, err := f.srv.store.BeginPublication(v.ID, oldParams.Nonce); err == nil {
		t.Fatal("the first click's nonce still arms the publication after a re-arm")
	}
}

func TestCancellingAPublishJobRevokesTheClick(t *testing.T) {
	f := newPubFixture(t)
	var v publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusAccepted), &v)
	job := f.publishJobs(t)[0]
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(job.ID, 10)+"/cancel", nil, http.StatusOK)
	row, _ := f.srv.store.GetPublication(v.ID)
	if row.State != db.PublicationFailed || row.FailCause != string(publish.CauseCancelled) {
		t.Fatalf("row after cancel = %+v", row)
	}
}

func TestThePublishKindCannotBeSubmittedFromTheLibrary(t *testing.T) {
	f := newPubFixture(t)
	r := jsonRequest(t, http.MethodPost, "/api/v1/library/recordings/1/jobs/"+string(publish.JobKind), map[string]any{})
	f.sign(r)
	if w := do(t, f.h, r); w.Code < 400 {
		t.Fatalf("library submit of a publish answered %d", w.Code)
	}
	if len(f.publishJobs(t)) != 0 {
		t.Fatal("a publish job was created without a click")
	}
}

func TestNoMCPToolIsNamedForPublishing(t *testing.T) {
	for _, tool := range mcpCatalogue() {
		n := strings.ToLower(tool.name)
		if strings.Contains(n, "publish") || strings.Contains(n, "upload") || strings.Contains(n, "post_clip") {
			t.Errorf("MCP tool %q looks like it publishes; clip publishing is console-click only", tool.name)
		}
	}
}

// ------------------------------------------------------------------ platforms

func TestTikTokIsForcedPrivateAtEveryLayer(t *testing.T) {
	f := newPubFixture(t)
	tt := f.account(t, db.PlatformTikTok, "creator", "user.info.basic,video.publish")
	form := f.form("tiktok", tt)
	form["privacy"] = "PUBLIC_TO_EVERYONE"
	var prep struct {
		Meta          publish.Meta `json:"meta"`
		PrivacyForced bool         `json:"privacyForced"`
	}
	decodeInto(t, f.prepare(t, form, http.StatusOK), &prep)
	if prep.Meta.Privacy != "SELF_ONLY" || !prep.PrivacyForced {
		t.Fatalf("prepare = %+v", prep)
	}
	var v publicationView
	decodeInto(t, f.click(t, form, http.StatusAccepted), &v)
	if v.Privacy != "SELF_ONLY" {
		t.Fatalf("stored privacy = %q", v.Privacy)
	}
	var targets publishTargetsView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, "/api/v1/publish/targets?clip="+strconv.FormatInt(f.clip, 10), nil, http.StatusOK), &targets)
	for _, p := range targets.Platforms {
		if p.ID == "tiktok" && (!p.Privacy.Forced || len(p.Privacy.Options) != 1 || p.Privacy.Options[0] != "SELF_ONLY") {
			t.Fatalf("tiktok privacy view = %+v", p.Privacy)
		}
	}
}

func TestXIsGatedAndQueuesNothing(t *testing.T) {
	f := newPubFixture(t)
	xa := f.account(t, db.PlatformX, "xuser", "tweet.write media.write users.read")
	form := f.form("x", xa)
	var e apiError
	decodeInto(t, f.prepare(t, form, http.StatusForbidden), &e)
	if e.Code != "gated" {
		t.Fatalf("code = %q", e.Code)
	}
	form["confirmToken"] = "anything"
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/publish", form, http.StatusForbidden)
	var targets publishTargetsView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, "/api/v1/publish/targets?clip="+strconv.FormatInt(f.clip, 10), nil, http.StatusOK), &targets)
	for _, p := range targets.Platforms {
		if p.ID == "x" && (p.Enabled || p.GatedReason == "") {
			t.Fatalf("x target = %+v, want disabled with a reason", p)
		}
	}
	if len(f.publishJobs(t)) != 0 || len(f.rows(t)) != 0 {
		t.Fatal("a gated platform left a job or row")
	}
}

func TestAGrantWithoutUploadAsksForAReconnect(t *testing.T) {
	f := newPubFixture(t)
	weak := f.account(t, db.PlatformYouTube, "Read Only", "https://www.googleapis.com/auth/youtube.readonly")
	var e apiError
	decodeInto(t, f.prepare(t, f.form("youtube", weak), http.StatusConflict), &e)
	if e.Code != "needs_reconnect" {
		t.Fatalf("code = %q", e.Code)
	}
	var targets publishTargetsView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, "/api/v1/publish/targets?clip="+strconv.FormatInt(f.clip, 10), nil, http.StatusOK), &targets)
	found := false
	for _, p := range targets.Platforms {
		for _, a := range p.Accounts {
			if a.ID == weak {
				found = a.NeedsReconnect && a.ConnectPath == "/api/v1/oauth/youtube/start"
			}
		}
	}
	if !found {
		t.Fatal("the read-only account was not offered a reconnect")
	}
}

func TestAnAccountConnectedBeforeUploadsExistedDoesNotNeedAReconnect(t *testing.T) {
	f := newPubFixture(t)
	old := f.account(t, db.PlatformYouTube, "Old Grant", "https://www.googleapis.com/auth/youtube")
	f.prepare(t, f.form("youtube", old), http.StatusOK)
}

func TestTargetsPrefillTheFormFromTheClip(t *testing.T) {
	f := newPubFixture(t)
	var targets publishTargetsView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, "/api/v1/publish/targets?clip="+strconv.FormatInt(f.clip, 10), nil, http.StatusOK), &targets)
	if targets.Prefill.Title != "short-1" || !targets.Vertical {
		t.Fatalf("prefill = %+v vertical=%v", targets.Prefill, targets.Vertical)
	}
	if len(targets.Prefill.Tags) != 1 || targets.Prefill.Tags[0] != "Shorts" {
		t.Fatalf("tags = %v", targets.Prefill.Tags)
	}
	if len(targets.Platforms) != 3 {
		t.Fatalf("platforms = %d", len(targets.Platforms))
	}
}

func TestPublishRefusesAClipThatIsNotAFinishedExport(t *testing.T) {
	f := newPubFixture(t)
	form := f.form("youtube", f.yt)
	form["clipJobId"] = 99999
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/publish/prepare", form, http.StatusNotFound)
	other, err := f.srv.store.EnqueueJob(jobs.Job{Kind: "transcribe", Target: "recording:1"})
	if err != nil {
		t.Fatal(err)
	}
	form["clipJobId"] = other.ID
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/publish/prepare", form, http.StatusNotFound)
}

func TestPublishRejectsBadFormsBeforeAnyToken(t *testing.T) {
	f := newPubFixture(t)
	bad := f.form("youtube", f.yt)
	bad["title"] = ""
	f.prepare(t, bad, http.StatusBadRequest)
	bad = f.form("myspace", f.yt)
	f.prepare(t, bad, http.StatusBadRequest)
	other := f.form("youtube", 424242)
	f.prepare(t, other, http.StatusNotFound)
}

func TestAStatusReadReconcilesACancelledJobIntoAFailure(t *testing.T) {
	f := newPubFixture(t)
	var v publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusAccepted), &v)
	job := f.publishJobs(t)[0]
	// A cancel that bypassed the handler (the queue was driven directly).
	if err := f.q.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	var got publicationView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, "/api/v1/publish/"+strconv.FormatInt(v.ID, 10), nil, http.StatusOK), &got)
	if got.State != db.PublicationFailed || got.Reason == nil || got.Reason.Cause != publish.CauseCancelled {
		t.Fatalf("status = %+v", got)
	}
}

func TestPublishTokensHandsTheWorkerTheStoredGrant(t *testing.T) {
	f := newPubFixture(t)
	c, err := f.srv.PublishTokens()(t.Context(), f.yt)
	if err != nil || c.AccessToken != "placeholder-access" || c.Scopes != ytUploadScope {
		t.Fatalf("credential = %+v, %v", c, err)
	}
	if _, err := f.srv.PublishTokens()(t.Context(), 424242); err == nil {
		t.Fatal("an unknown account produced a credential")
	}
}

// A job that ends WITHOUT finishing its row (a crash on the last attempt, a ledger
// write that failed) must not strand the publication: every later click would
// find it "in flight" and queue nothing, and the jobs page cannot retry it.
func TestARowWhoseJobFailedUnderItIsReconciledAndAnewClickRearmsIt(t *testing.T) {
	f := newPubFixture(t)
	var v publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusAccepted), &v)
	old := f.publishJobs(t)[0]
	if err := f.srv.store.FinishJob(old.ID, jobs.StateFailed, "the server stopped on the last attempt", time.Now()); err != nil {
		t.Fatal(err)
	}
	var got publicationView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, "/api/v1/publish/"+strconv.FormatInt(v.ID, 10), nil, http.StatusOK), &got)
	if got.State != db.PublicationFailed || got.Reason == nil || !strings.Contains(got.Reason.Line, "last attempt") {
		t.Fatalf("status = %+v", got)
	}
	var again publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusAccepted), &again)
	if again.ID != v.ID || again.State != db.PublicationConfirmed || len(f.publishJobs(t)) != 2 {
		t.Fatalf("rearm = %+v, jobs = %d", again, len(f.publishJobs(t)))
	}
}

// The same repair happens at the click even if nobody looked at the status.
func TestAClickReconcilesAStrandedRowBeforeClaiming(t *testing.T) {
	f := newPubFixture(t)
	var v publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusAccepted), &v)
	if err := f.srv.store.FinishJob(f.publishJobs(t)[0].ID, jobs.StateDone, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	f.click(t, f.form("youtube", f.yt), http.StatusAccepted)
	if len(f.publishJobs(t)) != 2 {
		t.Fatalf("a stranded row was not re-armed by the click")
	}
}

func TestARowThatWasNeverQueuedIsNotLeftArmedForever(t *testing.T) {
	f := newPubFixture(t)
	p, _, err := f.srv.store.ClaimPublication(db.Publication{ClipJobID: f.clip, Platform: "youtube",
		AccountID: f.yt, Title: "t", Nonce: "n", Privacy: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.store.SQL().Exec(`UPDATE clip_publications SET updated_ms = ? WHERE id = ?`,
		time.Now().Add(-time.Hour).UnixMilli(), p.ID); err != nil {
		t.Fatal(err)
	}
	var got publicationView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, "/api/v1/publish/"+strconv.FormatInt(p.ID, 10), nil, http.StatusOK), &got)
	if got.State != db.PublicationFailed {
		t.Fatalf("state = %q", got.State)
	}
}

// outcome_unknown means the last request may have landed. A click cannot re-arm it
// unless the person says they looked at the account.
func TestAnOutcomeUnknownFailureNeedsTheAcknowledgementToBeReArmed(t *testing.T) {
	f := newPubFixture(t)
	var v publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusAccepted), &v)
	if err := f.srv.store.FailPublication(v.ID, string(publish.CauseOutcomeUnknown), "look at the account"); err != nil {
		t.Fatal(err)
	}
	var e apiError
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusConflict), &e)
	if e.Code != "acknowledge_outcome" || len(f.publishJobs(t)) != 1 {
		t.Fatalf("code = %q, jobs = %d", e.Code, len(f.publishJobs(t)))
	}
	ack := f.form("youtube", f.yt)
	ack["acknowledgeOutcome"] = true
	f.click(t, ack, http.StatusAccepted)
	if len(f.publishJobs(t)) != 2 {
		t.Fatalf("an acknowledged re-arm did not queue a job")
	}
}

// A job that is live but was never linked to its row (a crash between Submit and
// the link) is found by its target and linked, not failed as "never queued".
func TestALiveJobThatWasNeverLinkedIsLinkedNotFailed(t *testing.T) {
	f := newPubFixture(t)
	row, _, err := f.srv.store.ClaimPublication(db.Publication{ClipJobID: f.clip, Platform: "youtube",
		AccountID: f.yt, Title: "t", Nonce: "nonce-x", Privacy: "private"})
	if err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(publish.Params{PublicationID: row.ID, Nonce: "nonce-x"})
	job, _, err := f.q.Submit(jobs.Job{Kind: publish.JobKind, Target: publishJobTarget(row.ID), Params: params, Unique: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.store.SQL().Exec(`UPDATE clip_publications SET updated_ms = ? WHERE id = ?`,
		time.Now().Add(-time.Hour).UnixMilli(), row.ID); err != nil {
		t.Fatal(err)
	}
	var got publicationView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, "/api/v1/publish/"+strconv.FormatInt(row.ID, 10), nil, http.StatusOK), &got)
	if got.State == db.PublicationFailed || got.PublishJobID != job.ID {
		t.Fatalf("view = %+v, want it linked to job %d and still in flight", got, job.ID)
	}
}

func TestAnUnreadableJobDoesNotFailALivePublication(t *testing.T) {
	f := newPubFixture(t)
	var v publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusAccepted), &v)
	row, _ := f.srv.store.GetPublication(v.ID)
	// A job id that does not parse as a row is a not-found; a read error must not
	// be treated the same way. Closing the store would break every other call too,
	// so the distinction is pinned on reconcile's own contract: a missing job fails
	// the row, an existing one does not.
	if got, j := f.srv.reconcilePublication(row); got.State == db.PublicationFailed || j == nil {
		t.Fatalf("a live job was reconciled away: %+v", got)
	}
}

// The quota a YouTube upload will spend is shown to the operator before they
// click. The literal is videos.insert's published price, and it must equal the
// registry value the upload actually charges.
func TestTargetsQuoteTheUploadCostTheRegistryCharges(t *testing.T) {
	f := newPubFixture(t)
	var targets publishTargetsView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, "/api/v1/publish/targets?clip="+strconv.FormatInt(f.clip, 10), nil, http.StatusOK), &targets)
	for _, p := range targets.Platforms {
		if p.ID == "youtube" {
			want := 1600
			if p.QuotaUnits != want {
				t.Fatalf("quotaUnits = %d, want %d", p.QuotaUnits, want)
			}
			if got := platform.YouTubeCostUploadVideo; got != want {
				t.Fatalf("the registry charges %d for an upload and the API quotes %d", got, want)
			}
			return
		}
	}
	t.Fatal("no youtube target")
}
