package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

func connectAccount(t *testing.T, store *db.DB, box *secrets.Box, platform db.Platform, name string) int64 {
	t.Helper()
	acct, err := store.UpsertPlatformAccount(box, &db.PlatformAccount{
		Platform:     platform,
		AccountName:  name,
		AccountRef:   name + "-ref",
		AccessToken:  "at",
		RefreshToken: "rt",
	})
	if err != nil {
		t.Fatalf("connect %s: %v", platform, err)
	}
	return acct.ID
}

// pushAndSettle starts a push and polls the job until it finishes, the way the
// composer does.
func pushAndSettle(t *testing.T, h http.Handler, sign func(*http.Request), body any) metadataJob {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, "/api/v1/metadata/push", body)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("push: status %d, body %s", w.Code, w.Body.String())
	}
	var job metadataJob
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode job: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		gr := jsonRequest(t, http.MethodGet, "/api/v1/metadata/push/"+job.ID, nil)
		sign(gr)
		gw := do(t, h, gr)
		if gw.Code != http.StatusOK {
			t.Fatalf("poll: status %d, body %s", gw.Code, gw.Body.String())
		}
		if err := json.Unmarshal(gw.Body.Bytes(), &job); err != nil {
			t.Fatalf("decode poll: %v", err)
		}
		if job.Done {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the push never finished")
	return job
}

func TestMetadataRoutesRequireASession(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"overview", http.MethodGet, "/api/v1/metadata"},
		{"push", http.MethodPost, "/api/v1/metadata/push"},
		{"job", http.MethodGet, "/api/v1/metadata/push/anything"},
		// This list is enumerated by hand, so a route added without a line
		// here is a route nothing checks for auth. broadcast-window reads a
		// connected account's live broadcast state, which is exactly the sort
		// of thing that must not answer an unauthenticated caller.
		{"broadcast window", http.MethodGet, "/api/v1/metadata/broadcast-window"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, jsonRequest(t, tc.method, tc.path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
		})
	}
}

func TestMetadataTargetsOmitPlatformsWithoutTheCapability(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	connectAccount(t, store, s.box, db.PlatformTwitch, "dj")
	// Kick cannot fetch a stream key but can push a title and a category, so it
	// is a legitimate target — the two capabilities are unrelated.
	connectAccount(t, store, s.box, db.PlatformKick, "kicker")
	// A custom destination has no provider at all, so an account for it —
	// however it got there — must be absent rather than present and permanently
	// failing.
	connectAccount(t, store, s.box, db.PlatformCustom, "diy")

	r := jsonRequest(t, http.MethodGet, "/api/v1/metadata", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Targets []metadataTarget `json:"targets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Targets) != 3 {
		t.Fatalf("targets = %d, want the three capable platforms: %#v", len(got.Targets), got.Targets)
	}
	for _, tgt := range got.Targets {
		if tgt.Platform == db.PlatformCustom {
			t.Fatal("a custom destination was offered as a metadata target")
		}
		if len(tgt.Caps.Fields) == 0 {
			t.Fatalf("%s reported no fields, so the composer cannot render it", tgt.Platform)
		}
	}
}

func TestPushMetadataRejectsAnEmptyComposer(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	tests := []struct {
		name string
		body map[string]string
	}{
		{"nothing at all", map[string]string{}},
		{"whitespace only", map[string]string{"title": "   ", "description": "\n"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodPost, "/api/v1/metadata/push", tc.body)
			sign(r)
			w := do(t, h, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
		})
	}
}

// The other side of that rule, and the reason the empty check had to change.
//
// A broadcast-only push carries no title, description or category at all --
// turning the DVR off before going live without retyping a title that is
// already correct. The old check called that empty and refused it, so a
// composer offering broadcast controls would have had a Push button that
// rejected exactly the edit it was built for.
//
// 400 is the failure being guarded against here. Any other status means the
// request got past validation, which is all this test claims: with no
// connected account it still cannot reach a platform.
func TestPushMetadataAcceptsABroadcastOnlyEdit(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodPost, "/api/v1/metadata/push", map[string]any{
		"broadcast": map[string]any{"enableDvr": false},
	})
	sign(r)
	w := do(t, h, r)
	if w.Code == http.StatusBadRequest {
		t.Fatalf("a broadcast-only push was refused as empty: %s", w.Body.String())
	}
}

// false must survive the JSON round trip as "turn it off" rather than
// decoding to the zero value and reading as "not mentioned".
//
// This is the pointer design crossing the wire, and it is the one place it
// could quietly break: a plain bool in the request struct would decode both an
// absent field and an explicit false to the same thing, and the push would
// stop being able to turn a toggle off at all.
func TestAnExplicitFalseSurvivesTheRequestDecode(t *testing.T) {
	var req struct {
		Broadcast oauth.BroadcastSettings `json:"broadcast"`
	}
	if err := json.Unmarshal([]byte(`{"broadcast":{"enableDvr":false}}`), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Broadcast.EnableDvr == nil {
		t.Fatal("an explicit false decoded to nil, so it reads as \"leave it alone\" " +
			"and the operator can never turn the DVR off")
	}
	if *req.Broadcast.EnableDvr {
		t.Error("an explicit false decoded to true")
	}
	if req.Broadcast.Empty() {
		t.Error("a block containing an explicit false reads as empty, so it would never be sent")
	}

	// And the absent case must stay absent.
	var none struct {
		Broadcast oauth.BroadcastSettings `json:"broadcast"`
	}
	if err := json.Unmarshal([]byte(`{"broadcast":{}}`), &none); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if none.Broadcast.EnableDvr != nil {
		t.Error("an omitted field decoded to a value, so untouched settings would be written")
	}
	if !none.Broadcast.Empty() {
		t.Error("an empty block does not read as empty")
	}
}

func TestPushMetadataWithNoConnectedAccountSaysWhereToConnectOne(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodPost, "/api/v1/metadata/push", map[string]string{"title": "Live"})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Settings") {
		t.Fatalf("the error should say where to fix it: %s", w.Body.String())
	}
}

func TestPushMetadataReturnsImmediatelyWithEveryAccountPending(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	connectAccount(t, store, s.box, db.PlatformTwitch, "dj")

	r := jsonRequest(t, http.MethodPost, "/api/v1/metadata/push", map[string]string{"title": "Live tonight"})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}
	var job metadataJob
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if job.ID == "" {
		t.Fatal("the job has no id, so the composer cannot poll it")
	}
	if len(job.Results) != 2 {
		t.Fatalf("results = %d, want one row per account", len(job.Results))
	}
	for _, res := range job.Results {
		if res.State != metaPending {
			t.Fatalf("%s started as %q, want pending", res.Platform, res.State)
		}
	}
}

func TestPushMetadataReportsPerPlatformRatherThanOneBoolean(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	connectAccount(t, store, s.box, db.PlatformTwitch, "dj")

	// No developer credentials are configured, so both fail — but each has to
	// fail in its own row with its own reason.
	job := pushAndSettle(t, h, sign, map[string]string{"title": "Live tonight"})
	if len(job.Results) != 2 {
		t.Fatalf("results = %d, want one row per account", len(job.Results))
	}
	seen := map[db.Platform]metadataOutcome{}
	for _, res := range job.Results {
		if res.State != metaError {
			t.Fatalf("%s = %q, want an error without credentials", res.Platform, res.State)
		}
		if res.Message == "" {
			t.Fatalf("%s failed with no reason", res.Platform)
		}
		if res.FinishedAt == nil {
			t.Fatalf("%s never recorded a finish time", res.Platform)
		}
		seen[res.Platform] = res
	}
	if len(seen) != 2 {
		t.Fatalf("platforms = %v, want both reported separately", seen)
	}
}

// TestPushMetadataCarriesTagsToThePusher closes the gap the whole-branch
// review found: `Tags: req.Tags` in handlePushMetadata's oauth.Metadata
// literal was deletable with the entire suite green, because nothing proved
// the composer's tags survive the trip from the request to the call that
// actually leaves the process. pushMetadataFn is that trip's seam, the same
// shape as ingestForFn one handler over -- oauth.MetadataPusher is a real
// provider (embeds Provider, has no injection point of its own), so this
// captures the argument one line before it would reach one.
func TestPushMetadataCarriesTagsToThePusher(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	connectAccount(t, store, s.box, db.PlatformFacebook, "ada")
	if err := store.PutPlatformCreds(s.box, db.PlatformFacebook, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}

	var (
		called   bool
		captured oauth.Metadata
	)
	s.pushMetadataFn = func(ctx context.Context, pusher oauth.MetadataPusher, clientID, accessToken, accountRef string, m oauth.Metadata) (*oauth.MetadataResult, error) {
		called = true
		captured = m
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldTitle, oauth.FieldTags}}, nil
	}

	job := pushAndSettle(t, h, sign, map[string]any{
		"title": "Live tonight",
		"tags":  []string{"cooking", "live"},
	})
	if !called {
		t.Fatal("pushMetadataFn was never invoked; the push did not reach the seam at all")
	}
	if !reflect.DeepEqual(captured.Tags, []string{"cooking", "live"}) {
		t.Errorf("Tags passed to the pusher = %v, want [cooking live]", captured.Tags)
	}
	if len(job.Results) != 1 || job.Results[0].State != metaOK {
		t.Fatalf("job results = %+v, want one ok result", job.Results)
	}
}

// TestAPushSendsTheStoredComplianceToTheProvider is THE GUARD THIS PLAN EXISTS
// FOR.
//
// oauth.PushCompliance was implemented, unit-tested and documented as shipped,
// and no caller ever invoked it: a YouTube COPPA declaration and a Twitch label
// set could be stored, edited and validated without once reaching a platform.
// Every test of the capability in isolation stayed green through all of it,
// which is precisely why this one drives the real HTTP handler instead. It
// fails the moment the push stops CALLING the capability, whatever the
// capability itself still does.
//
// Mutation to run against it: delete the ComplianceFor branch from pushOne.
// pushComplianceFn is the seam -- the same shape as pushMetadataFn beside it --
// because oauth.CompliancePusher is satisfied only by real providers whose
// HTTP base is unexported, so this is the last observable point before the
// request leaves the process.
func TestAPushSendsTheStoredComplianceToTheProvider(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	kids := true
	stored := db.Compliance{Privacy: db.PrivacyPrivate, MadeForKids: &kids}
	if _, err := store.CreateDestination(&db.Destination{
		Name: "main", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.example/live", StreamKey: "sk-live-1", AccountID: &acctID,
		Compliance: stored,
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	// The metadata half is stubbed only so the row can settle without a network
	// call; nothing about it is under test here.
	s.pushMetadataFn = func(ctx context.Context, pusher oauth.MetadataPusher, clientID, accessToken, accountRef string, m oauth.Metadata) (*oauth.MetadataResult, error) {
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldTitle}}, nil
	}

	var (
		called       bool
		gotCompl     db.Compliance
		gotTarget    oauth.ComplianceTarget
		gotClientID  string
		gotAccessTok string
	)
	s.pushComplianceFn = func(ctx context.Context, pusher oauth.CompliancePusher, clientID, accessToken string,
		tgt oauth.ComplianceTarget, c db.Compliance) (*oauth.MetadataResult, error) {
		called = true
		gotCompl, gotTarget, gotClientID, gotAccessTok = c, tgt, clientID, accessToken
		return &oauth.MetadataResult{
			Applied: []oauth.MetadataField{oauth.FieldPrivacy, oauth.FieldMadeForKids},
		}, nil
	}

	job := pushAndSettle(t, h, sign, map[string]any{"title": "Live tonight"})
	if !called {
		t.Fatal("the push never called PushCompliance: the operator's stored privacy and " +
			"COPPA declaration were saved, validated, and never sent anywhere")
	}
	if !reflect.DeepEqual(gotCompl, stored) {
		t.Errorf("compliance passed to the provider = %+v, want the stored %+v", gotCompl, stored)
	}
	// AccountRef is what Twitch addresses its write with and StreamKey is what
	// Facebook recovers its live video id from, so a target assembled from the
	// wrong halves reaches the wrong channel or broadcast.
	want := oauth.ComplianceTarget{AccountRef: "chan-ref", StreamKey: "sk-live-1"}
	if gotTarget != want {
		t.Errorf("ComplianceTarget = %+v, want %+v", gotTarget, want)
	}
	if gotClientID != "cid" || gotAccessTok != "at" {
		t.Errorf("credentials = (%q, %q), want the account's own (\"cid\", \"at\")", gotClientID, gotAccessTok)
	}

	if len(job.Results) != 1 {
		t.Fatalf("results = %+v, want one row", job.Results)
	}
	// The compliance result has to reach the row the composer renders. Applied
	// fields that the provider reported but the outcome dropped would tell the
	// operator their COPPA declaration did not land when it did.
	applied := map[oauth.MetadataField]bool{}
	for _, f := range job.Results[0].Applied {
		applied[f] = true
	}
	for _, f := range []oauth.MetadataField{oauth.FieldPrivacy, oauth.FieldMadeForKids} {
		if !applied[f] {
			t.Errorf("%q is missing from the reported result %v, so a compliance field that "+
				"was applied is invisible to the operator", f, job.Results[0].Applied)
		}
	}
}

// TestAComplianceConflictIsRefusedBeforeAnythingIsSent pins the ORDER, not the
// status code.
//
// Two destinations on one account asking for different compliance is a refusal
// because the platform has one broadcast to apply them to. A refusal that
// arrives after the job has already started is not a refusal: the operator is
// told nothing was sent while one destination's declaration is already on its
// way. So this asserts that no provider call happened and no job was created,
// and treats the 400 as the least interesting half.
func TestAComplianceConflictIsRefusedBeforeAnythingIsSent(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	for _, d := range []*db.Destination{
		{Name: "main", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
			URL: "rtmp://a.example/live", StreamKey: "sk-main", AccountID: &acctID,
			Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "backup", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
			URL: "rtmp://b.example/live", StreamKey: "sk-backup", AccountID: &acctID,
			Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
	} {
		if _, err := store.CreateDestination(d); err != nil {
			t.Fatalf("create %s: %v", d.Name, err)
		}
	}

	// A channel rather than a bool: these seams are called from the push's own
	// goroutines, so a plain variable read after a 400 would be a data race as
	// well as a flaky assertion.
	calls := make(chan string, 8)
	s.pushMetadataFn = func(ctx context.Context, pusher oauth.MetadataPusher, clientID, accessToken, accountRef string, m oauth.Metadata) (*oauth.MetadataResult, error) {
		calls <- "metadata"
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldTitle}}, nil
	}
	s.pushComplianceFn = func(ctx context.Context, pusher oauth.CompliancePusher, clientID, accessToken string,
		tgt oauth.ComplianceTarget, c db.Compliance) (*oauth.MetadataResult, error) {
		calls <- "compliance"
		return &oauth.MetadataResult{}, nil
	}

	before, _ := metadataRegistry.latest()

	r := jsonRequest(t, http.MethodPost, "/api/v1/metadata/push", map[string]string{"title": "Live tonight"})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	// Both names, for the same reason complianceByAccount reports both: a
	// conflict the operator cannot locate is barely better than silence.
	if !strings.Contains(w.Body.String(), "main") || !strings.Contains(w.Body.String(), "backup") {
		t.Errorf("the refusal does not name both destinations: %s", w.Body.String())
	}

	// Deterministic half: a job added to the registry means work was started
	// for a request that was then answered 400.
	if after, ok := metadataRegistry.latest(); ok && after.ID != before.ID {
		t.Error("a push job was created before the conflict was refused, so the refusal " +
			"came after the work it claims to have prevented")
	}
	// Direct half: nothing may have left the process.
	select {
	case which := <-calls:
		t.Fatalf("a %s request was made for a push that was refused; the operator was told "+
			"nothing was sent while one destination's compliance was already on the wire", which)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestAnUnconfirmedComplianceWriteReachesTheOperatorWithoutAnError guards the
// failure channel that carries NO error, which for Facebook is the only one it
// has.
//
// Every one of Facebook's four PushCompliance failure modes — a Page target, no
// recoverable broadcast id, a refused POST, a read-back that disagrees or
// cannot be read — returns `MetadataResult{Skipped, Warnings}` with a nil
// error. YouTube reports a failed selfDeclaredMadeForKids the same way, and
// Twitch reports a stale label row the same way. The err != nil arm next door
// has a test; this arm had none, so dropping the two appends below left the
// suite green and turned every one of those into an invisible success. An
// operator would be told their privacy change landed when Facebook never
// confirmed it — the exact failure this branch exists to end, one arm to the
// right.
//
// Mutations: drop `res.Skipped = append(res.Skipped, cres.Skipped...)`, drop
// `res.Warnings = append(res.Warnings, cres.Warnings...)`, or both.
func TestAnUnconfirmedComplianceWriteReachesTheOperatorWithoutAnError(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformFacebook, "ada")
	if err := store.PutPlatformCreds(s.box, db.PlatformFacebook, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	if _, err := store.CreateDestination(&db.Destination{
		Name: "page", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://ingest.example/app", StreamKey: "sk-live-1", AccountID: &acctID,
		Compliance: db.Compliance{FacebookPrivacy: db.FBPrivacySelf},
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	s.pushMetadataFn = func(ctx context.Context, pusher oauth.MetadataPusher, clientID, accessToken, accountRef string, m oauth.Metadata) (*oauth.MetadataResult, error) {
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldTitle}}, nil
	}
	// Exactly the shape Facebook returns when the read-back does not confirm:
	// no error, a skipped field, and the reason.
	s.pushComplianceFn = func(ctx context.Context, pusher oauth.CompliancePusher, clientID, accessToken string,
		tgt oauth.ComplianceTarget, c db.Compliance) (*oauth.MetadataResult, error) {
		return &oauth.MetadataResult{
			Skipped:  []oauth.MetadataField{oauth.FieldPrivacy},
			Warnings: []string{"privacy: Facebook did not confirm the change"},
			// The metadata half named no target, so the compliance result's is
			// what the row has to fall back to; without it the row names the
			// account and the operator cannot tell which broadcast was touched.
			Target: "Tonight's broadcast",
		}, nil
	}

	job := pushAndSettle(t, h, sign, map[string]any{"title": "Live tonight"})
	if len(job.Results) != 1 {
		t.Fatalf("results = %+v, want one row", job.Results)
	}
	res := job.Results[0]
	if !strings.Contains(strings.Join(res.Warnings, " "), "did not confirm") {
		t.Errorf("warnings = %v, want the provider's unconfirmed-write reason: a privacy "+
			"change Facebook never confirmed must not be shown as one that landed", res.Warnings)
	}
	skipped := false
	for _, f := range res.Skipped {
		if f == oauth.FieldPrivacy {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("skipped = %v, want privacy: the field the provider refused to confirm is "+
			"the one the operator has to be told about", res.Skipped)
	}
	if res.State != metaPartial {
		t.Errorf("state = %q, want partial: the title landed and the privacy did not, and "+
			"reading this row as a clean success is the whole defect", res.State)
	}
	if res.Target != "Tonight's broadcast" {
		t.Errorf("target = %q, want the broadcast the compliance result named: falling back "+
			"to the account name leaves the operator guessing which broadcast moved", res.Target)
	}
}

// TestAKickTargetIsSkippedOutLoudRatherThanSilently is the absence half of
// oauth.ComplianceFor, and it pins BOTH obligations at once.
//
// Kick has no compliance surface, so the push must not attempt one and must not
// fail the row: an operator whose Kick row turns red for a field Kick does not
// have learns to ignore the colour. But silence is not the alternative. A
// stored setting that goes nowhere and says nothing is the defect this entire
// branch exists to end, and it would be reproduced here in miniature. Skipped
// with a plain reason is the composer's existing way to say "asked for, not
// applicable" — which is why this asserts the state is NOT an error as
// deliberately as it asserts the reason is present.
func TestAKickTargetIsSkippedOutLoudRatherThanSilently(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformKick, "kicker")
	if err := store.PutPlatformCreds(s.box, db.PlatformKick, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	if _, err := store.CreateDestination(&db.Destination{
		Name: "kick", Kind: db.DestRTMP, Platform: db.PlatformKick,
		URL: "rtmp://k.example/live", StreamKey: "sk-kick", AccountID: &acctID,
		Compliance: db.Compliance{Privacy: db.PrivacyPrivate},
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	s.pushMetadataFn = func(ctx context.Context, pusher oauth.MetadataPusher, clientID, accessToken, accountRef string, m oauth.Metadata) (*oauth.MetadataResult, error) {
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldTitle}}, nil
	}
	attempted := false
	s.pushComplianceFn = func(ctx context.Context, pusher oauth.CompliancePusher, clientID, accessToken string,
		tgt oauth.ComplianceTarget, c db.Compliance) (*oauth.MetadataResult, error) {
		attempted = true
		return &oauth.MetadataResult{}, nil
	}

	job := pushAndSettle(t, h, sign, map[string]any{"title": "Live tonight"})
	if attempted {
		t.Error("a compliance write was attempted against Kick, which has no compliance API; " +
			"ComplianceFor's absence is being ignored at the call site")
	}
	if len(job.Results) != 1 {
		t.Fatalf("results = %+v, want one row", job.Results)
	}
	res := job.Results[0]
	if res.State == metaError {
		t.Errorf("state = %q (%s), want anything but an error: a platform without a "+
			"compliance API is not a failure", res.State, res.Message)
	}
	skipped := map[oauth.MetadataField]bool{}
	for _, f := range res.Skipped {
		skipped[f] = true
	}
	if !skipped[oauth.FieldPrivacy] {
		t.Errorf("skipped = %v, want the privacy this destination stored: the operator is "+
			"otherwise left expecting a setting that will never be sent", res.Skipped)
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "no compliance API") {
		t.Errorf("warnings = %v, want one saying Kick has no compliance API, so the "+
			"operator can stop expecting the setting to arrive", res.Warnings)
	}
}

// TestAnAccountWithNoStoredComplianceIsLeftEntirelyAlone holds the spec's
// "empty means leave alone, everywhere" at the one layer that decides it.
//
// Nothing guarded `if !t.Compliance.Empty()`: replacing it with `if true` left
// the suite green while every push to a Kick account emitted a warning about
// compliance settings the operator never made, and YouTube, Twitch and Facebook
// each took a pointless PushCompliance call, saved from the network only by
// their own inner guards. A yellow row on every push for a setting that does
// not exist is how an operator learns to stop reading the rows.
//
// Kick is the target precisely because it has no compliance API: if the guard
// goes, this is the account that produces a visible complaint rather than a
// silent no-op.
func TestAnAccountWithNoStoredComplianceIsLeftEntirelyAlone(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformKick, "kicker")
	if err := store.PutPlatformCreds(s.box, db.PlatformKick, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	// A destination with a stream key and no compliance at all: the ordinary
	// case, and the one that must stay quiet.
	if _, err := store.CreateDestination(&db.Destination{
		Name: "kick", Kind: db.DestRTMP, Platform: db.PlatformKick,
		URL: "rtmp://k.example/live", StreamKey: "sk-kick", AccountID: &acctID,
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	s.pushMetadataFn = func(ctx context.Context, pusher oauth.MetadataPusher, clientID, accessToken, accountRef string, m oauth.Metadata) (*oauth.MetadataResult, error) {
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldTitle}}, nil
	}
	attempted := false
	s.pushComplianceFn = func(ctx context.Context, pusher oauth.CompliancePusher, clientID, accessToken string,
		tgt oauth.ComplianceTarget, c db.Compliance) (*oauth.MetadataResult, error) {
		attempted = true
		return &oauth.MetadataResult{}, nil
	}

	job := pushAndSettle(t, h, sign, map[string]any{"title": "Live tonight"})
	if attempted {
		t.Error("a compliance write was attempted for an account with no compliance stored")
	}
	if len(job.Results) != 1 {
		t.Fatalf("results = %+v, want one row", job.Results)
	}
	res := job.Results[0]
	if res.State != metaOK {
		t.Errorf("state = %q (%s), want ok: nothing was asked for and nothing went wrong",
			res.State, res.Message)
	}
	if strings.Contains(strings.Join(res.Warnings, " "), "compliance") {
		t.Errorf("warnings = %v mention compliance for an account that has none stored; "+
			"an operator warned about a setting they never made stops reading the warnings",
			res.Warnings)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("skipped = %v, want nothing: no compliance field was asked for", res.Skipped)
	}
}

// TestTwoAccountsPushComplianceConcurrently closes a coverage gap rather than a
// defect. runMetadataPush fans out one goroutine per target and the compliance
// write now sits inside it, but every other compliance test settles exactly one
// row, so CI's -race never watched two PushCompliance calls in flight. Each
// target carries its own db.Compliance, whose Labels map came from its own
// destination and is only ever read, so there is nothing shared to corrupt —
// this is the test that would notice if that stopped being true.
func TestTwoAccountsPushComplianceConcurrently(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	yt := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	tw := connectAccount(t, store, s.box, db.PlatformTwitch, "dj")
	for _, p := range []db.Platform{db.PlatformYouTube, db.PlatformTwitch} {
		if err := store.PutPlatformCreds(s.box, p, "cid", "topsecret"); err != nil {
			t.Fatalf("creds %s: %v", p, err)
		}
	}
	for _, d := range []*db.Destination{
		{Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
			URL: "rtmp://a.example/live", StreamKey: "sk-yt", AccountID: &yt,
			Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "tw", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
			URL: "rtmp://b.example/live", StreamKey: "sk-tw", AccountID: &tw,
			Compliance: db.Compliance{Labels: map[string]bool{"Gambling": true}}},
	} {
		if _, err := store.CreateDestination(d); err != nil {
			t.Fatalf("create %s: %v", d.Name, err)
		}
	}

	s.pushMetadataFn = func(ctx context.Context, pusher oauth.MetadataPusher, clientID, accessToken, accountRef string, m oauth.Metadata) (*oauth.MetadataResult, error) {
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldTitle}}, nil
	}
	// Buffered per call, and the goroutines are released together, so the two
	// pushes genuinely overlap rather than queueing behind each other.
	start := make(chan struct{})
	seen := make(chan db.Compliance, 4)
	s.pushComplianceFn = func(ctx context.Context, pusher oauth.CompliancePusher, clientID, accessToken string,
		tgt oauth.ComplianceTarget, c db.Compliance) (*oauth.MetadataResult, error) {
		<-start
		seen <- c
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldPrivacy}}, nil
	}
	close(start)

	job := pushAndSettle(t, h, sign, map[string]any{"title": "Live tonight"})
	if len(job.Results) != 2 {
		t.Fatalf("results = %+v, want one row per account", job.Results)
	}
	close(seen)
	got := map[db.PrivacyStatus]bool{}
	labels := 0
	for c := range seen {
		if c.Privacy != db.PrivacyUnchanged {
			got[c.Privacy] = true
		}
		if len(c.Labels) > 0 {
			labels++
		}
	}
	// Each account's own settings, not one account's applied twice.
	if !got[db.PrivacyPrivate] || labels != 1 {
		t.Errorf("concurrent pushes sent privacy=%v and %d label sets, want each account's "+
			"own compliance exactly once", got, labels)
	}
}

// TestABroadcastFailureIsStillReportedAlongsideAMetadataSuccess is a
// pre-existing bug's guard, not this feature's.
//
// pushOne recorded a failed PushBroadcastSettings on out.Warnings and then, ten
// lines later, ASSIGNED out.Warnings from the metadata result — throwing the
// record away between writing it and showing it. An operator whose DVR toggle
// failed while the title landed was told nothing at all. The two halves have to
// be able to disagree, which is the whole reason this row reports fields rather
// than a boolean.
//
// Mutation: put back `out.Warnings = res.Warnings`.
func TestABroadcastFailureIsStillReportedAlongsideAMetadataSuccess(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}

	s.pushMetadataFn = func(ctx context.Context, pusher oauth.MetadataPusher, clientID, accessToken, accountRef string, m oauth.Metadata) (*oauth.MetadataResult, error) {
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldTitle}}, nil
	}
	var gotSettings oauth.BroadcastSettings
	s.pushBroadcastFn = func(ctx context.Context, pusher broadcastPusher, clientID, accessToken string,
		bs oauth.BroadcastSettings) (*oauth.MetadataResult, error) {
		gotSettings = bs
		return nil, errors.New("youtube said no: the broadcast is already live")
	}

	dvr := false
	job := pushAndSettle(t, h, sign, map[string]any{
		"title":     "Live tonight",
		"broadcast": map[string]any{"enableDvr": dvr},
	})
	if len(job.Results) != 1 {
		t.Fatalf("results = %+v, want one row", job.Results)
	}
	res := job.Results[0]
	if !strings.Contains(strings.Join(res.Warnings, " "), "already live") {
		t.Errorf("warnings = %v, want the broadcast failure's own reason: it was recorded "+
			"and then discarded before the operator could see it", res.Warnings)
	}
	// And the half that worked still reads as having worked.
	if len(res.Applied) == 0 {
		t.Errorf("applied = %v, want the title that landed: a failed broadcast write must "+
			"not erase a metadata write that succeeded", res.Applied)
	}
	// The operator's own settings have to be what reaches the pusher. Sending a
	// zero BroadcastSettings here would be the composer-tags defect again: the
	// call is made, the row reports a result, and the toggle the operator
	// actually moved was never in the request.
	if gotSettings.EnableDvr == nil || *gotSettings.EnableDvr {
		t.Errorf("BroadcastSettings passed to the pusher = %+v, want the explicit "+
			"enableDvr:false the operator sent", gotSettings)
	}
}

// TestAConflictOnAnUnselectedAccountDoesNotBlockThePush is the other direction
// of the refusal, and it is the one that makes the refusal usable.
//
// Compliance conflicts are resolved per account. An operator pushing to one
// account must not be stopped by two destinations disagreeing on a DIFFERENT
// account — they did not select it, they may not be able to see it from the
// composer, and the refusal would name destinations that have nothing to do
// with what they asked for. The conflicting account is still refused when it is
// the one selected; TestAComplianceConflictIsRefusedBeforeAnythingIsSent covers
// that direction over the unfiltered push.
//
// Mutation: refuse whenever any conflict exists anywhere.
func TestAConflictOnAnUnselectedAccountDoesNotBlockThePush(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	clean := connectAccount(t, store, s.box, db.PlatformYouTube, "clean")
	messy := connectAccount(t, store, s.box, db.PlatformTwitch, "messy")
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	for _, d := range []*db.Destination{
		{Name: "tidy", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
			URL: "rtmp://a.example/live", StreamKey: "sk-tidy", AccountID: &clean,
			Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		// Two destinations that disagree, on the account this push never names.
		{Name: "argue-one", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
			URL: "rtmp://b.example/live", StreamKey: "sk-1", AccountID: &messy,
			Compliance: db.Compliance{Labels: map[string]bool{"DrugsIntoxication": true}}},
		{Name: "argue-two", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
			URL: "rtmp://c.example/live", StreamKey: "sk-2", AccountID: &messy,
			Compliance: db.Compliance{Labels: map[string]bool{"Gambling": true}}},
	} {
		if _, err := store.CreateDestination(d); err != nil {
			t.Fatalf("create %s: %v", d.Name, err)
		}
	}

	s.pushMetadataFn = func(ctx context.Context, pusher oauth.MetadataPusher, clientID, accessToken, accountRef string, m oauth.Metadata) (*oauth.MetadataResult, error) {
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldTitle}}, nil
	}
	var pushedFor db.Compliance
	s.pushComplianceFn = func(ctx context.Context, pusher oauth.CompliancePusher, clientID, accessToken string,
		tgt oauth.ComplianceTarget, c db.Compliance) (*oauth.MetadataResult, error) {
		pushedFor = c
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldPrivacy}}, nil
	}

	job := pushAndSettle(t, h, sign, map[string]any{
		"title":      "Live tonight",
		"accountIds": []int64{clean},
	})
	if len(job.Results) != 1 || job.Results[0].AccountID != clean {
		t.Fatalf("results = %+v, want only the selected account", job.Results)
	}
	// The selected account's own compliance still went, unaffected by a
	// disagreement somewhere else entirely.
	if pushedFor.Privacy != db.PrivacyPrivate {
		t.Errorf("compliance pushed = %+v, want the selected account's private setting", pushedFor)
	}
}

// TestAConflictOnTheSelectedAccountStillRefuses is the pair to the test above:
// narrowing a push must not become a way to slip past the refusal. If scoping
// the check to the selected accounts also let a selected conflict through, the
// operator would get exactly the silent one-of-two-declarations write the
// refusal exists to prevent, just by ticking one box.
//
// Two conflicting accounts are selected, not one, because a refusal that stops
// at the first problem sends the operator round the loop again for the second.
// Every conflict, for every account addressed.
func TestAConflictOnTheSelectedAccountStillRefuses(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	clean := connectAccount(t, store, s.box, db.PlatformYouTube, "clean")
	messy := connectAccount(t, store, s.box, db.PlatformTwitch, "messy")
	alsoMessy := connectAccount(t, store, s.box, db.PlatformYouTube, "also-messy")
	for _, d := range []*db.Destination{
		{Name: "tidy", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
			URL: "rtmp://a.example/live", StreamKey: "sk-tidy", AccountID: &clean,
			Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "argue-one", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
			URL: "rtmp://b.example/live", StreamKey: "sk-1", AccountID: &messy,
			Compliance: db.Compliance{Labels: map[string]bool{"DrugsIntoxication": true}}},
		{Name: "argue-two", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
			URL: "rtmp://c.example/live", StreamKey: "sk-2", AccountID: &messy,
			Compliance: db.Compliance{Labels: map[string]bool{"Gambling": true}}},
		{Name: "squabble-one", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
			URL: "rtmp://d.example/live", StreamKey: "sk-3", AccountID: &alsoMessy,
			Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "squabble-two", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
			URL: "rtmp://e.example/live", StreamKey: "sk-4", AccountID: &alsoMessy,
			Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
		// A third disagreeing destination on that same account, so this account
		// alone produces two conflict messages. Reporting only the first per
		// account would otherwise be invisible here.
		{Name: "squabble-three", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
			URL: "rtmp://f.example/live", StreamKey: "sk-5", AccountID: &alsoMessy,
			Compliance: db.Compliance{Privacy: db.PrivacyUnlisted}},
	} {
		if _, err := store.CreateDestination(d); err != nil {
			t.Fatalf("create %s: %v", d.Name, err)
		}
	}

	calls := make(chan string, 8)
	s.pushMetadataFn = func(ctx context.Context, pusher oauth.MetadataPusher, clientID, accessToken, accountRef string, m oauth.Metadata) (*oauth.MetadataResult, error) {
		calls <- "metadata"
		return &oauth.MetadataResult{}, nil
	}
	s.pushComplianceFn = func(ctx context.Context, pusher oauth.CompliancePusher, clientID, accessToken string,
		tgt oauth.ComplianceTarget, c db.Compliance) (*oauth.MetadataResult, error) {
		calls <- "compliance"
		return &oauth.MetadataResult{}, nil
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/metadata/push", map[string]any{
		"title":      "Live tonight",
		"accountIds": []int64{messy, alsoMessy},
	})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	for _, name := range []string{"argue-one", "argue-two", "squabble-one", "squabble-two", "squabble-three"} {
		if !strings.Contains(w.Body.String(), name) {
			t.Errorf("the refusal never names %q, so fixing what it does report still leaves "+
				"the push refused: %s", name, w.Body.String())
		}
	}
	select {
	case which := <-calls:
		t.Fatalf("a %s request was made for a push that was refused", which)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestAFailedComplianceWriteIsReportedRatherThanSwallowed was written because
// the mutation that removes the error branch's whole body left the suite green:
// a compliance write that failed would have been indistinguishable from one
// that succeeded, which is the same silence this branch exists to end, one
// layer in. It also pins the failure as PARTIAL rather than fatal -- the title
// above it may already have landed, and failing the row would send the operator
// back to redo work that took.
func TestAFailedComplianceWriteIsReportedRatherThanSwallowed(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	kids := true
	if _, err := store.CreateDestination(&db.Destination{
		Name: "main", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.example/live", StreamKey: "sk-live-1", AccountID: &acctID,
		Compliance: db.Compliance{Privacy: db.PrivacyPrivate, MadeForKids: &kids},
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	s.pushMetadataFn = func(ctx context.Context, pusher oauth.MetadataPusher, clientID, accessToken, accountRef string, m oauth.Metadata) (*oauth.MetadataResult, error) {
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldTitle}}, nil
	}
	s.pushComplianceFn = func(ctx context.Context, pusher oauth.CompliancePusher, clientID, accessToken string,
		tgt oauth.ComplianceTarget, c db.Compliance) (*oauth.MetadataResult, error) {
		return nil, errors.New("youtube said no: insufficient scope")
	}

	job := pushAndSettle(t, h, sign, map[string]any{"title": "Live tonight"})
	if len(job.Results) != 1 {
		t.Fatalf("results = %+v, want one row", job.Results)
	}
	res := job.Results[0]
	if !strings.Contains(strings.Join(res.Warnings, " "), "insufficient scope") {
		t.Errorf("warnings = %v, want the provider's own reason: a compliance write that "+
			"failed must not read like one that worked", res.Warnings)
	}
	skipped := map[oauth.MetadataField]bool{}
	for _, f := range res.Skipped {
		skipped[f] = true
	}
	// Only what the operator actually set. Naming contentLabels here would
	// report a fault in a Twitch field this YouTube destination never touched.
	for _, f := range []oauth.MetadataField{oauth.FieldPrivacy, oauth.FieldMadeForKids} {
		if !skipped[f] {
			t.Errorf("%q is missing from skipped %v, so the operator is not told which "+
				"declaration failed to land", f, res.Skipped)
		}
	}
	if skipped[oauth.FieldLabels] {
		t.Errorf("skipped = %v names contentLabels, which this destination never set", res.Skipped)
	}
	if res.State != metaPartial {
		t.Errorf("state = %q, want partial: the title landed and the compliance did not, "+
			"and collapsing that to either extreme loses the half that matters", res.State)
	}
}

// TestTheOverviewNeverSerialisesAStreamKey guards the `json:"-"` on
// metadataTarget.StreamKey. The field is on the target only so the compliance
// push can reach Facebook's live video id; the same struct is the body of an
// endpoint the dashboard renders, and a stream key in that response is a
// credential handed to every script on the page. Dropping the tag is a
// one-character change that nothing else notices.
func TestTheOverviewNeverSerialisesAStreamKey(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	if _, err := store.CreateDestination(&db.Destination{
		Name: "main", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.example/live", StreamKey: "sk-live-secret", AccountID: &acctID,
		Compliance: db.Compliance{Privacy: db.PrivacyPrivate},
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	r := jsonRequest(t, http.MethodGet, "/api/v1/metadata", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sk-live-secret") {
		t.Errorf("the metadata overview leaked a stream key into a browser response: %s", w.Body.String())
	}
}

func TestPushMetadataRefusesAnOverlongTitlePerPlatformLimit(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	ytID := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")

	// 120 runes: past YouTube's 100 and under Twitch's 140, which is exactly
	// the case a single global limit would get wrong.
	job := pushAndSettle(t, h, sign, map[string]any{
		"title":      strings.Repeat("x", 120),
		"accountIds": []int64{ytID},
	})
	if len(job.Results) != 1 {
		t.Fatalf("results = %d, want only the selected account", len(job.Results))
	}
	res := job.Results[0]
	if res.State != metaError {
		t.Fatalf("state = %q, want an error", res.State)
	}
	if !strings.Contains(res.Message, "100") || !strings.Contains(res.Message, "120") {
		t.Fatalf("message should name the limit and the length, got %q", res.Message)
	}
}

func TestMetadataOverviewCarriesTheLastPushSoAReloadStillShowsIt(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	connectAccount(t, store, s.box, db.PlatformTwitch, "dj")

	job := pushAndSettle(t, h, sign, map[string]string{"title": "Live tonight"})

	r := jsonRequest(t, http.MethodGet, "/api/v1/metadata", nil)
	sign(r)
	w := do(t, h, r)
	var got struct {
		Last *metadataJob `json:"last"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Last == nil || got.Last.ID != job.ID {
		t.Fatalf("overview did not carry the last push: %#v", got.Last)
	}
}

func TestUnknownMetadataJobIs404(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodGet, "/api/v1/metadata/push/nope", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestPrecheckSkipsUnsupportedFieldsAndRefusesOnlyOverlongOnes(t *testing.T) {
	yt, _ := oauth.MetadataFor(db.PlatformYouTube)
	tw, _ := oauth.MetadataFor(db.PlatformTwitch)

	tests := []struct {
		name        string
		caps        oauth.MetadataCaps
		meta        oauth.Metadata
		wantSkipped []oauth.MetadataField
		wantProblem bool
	}{
		{
			name: "youtube takes all three",
			caps: yt.MetadataCaps(),
			meta: oauth.Metadata{Title: "t", Description: "d", Category: "Gaming"},
		},
		{
			name:        "twitch skips the description it has nowhere to put",
			caps:        tw.MetadataCaps(),
			meta:        oauth.Metadata{Title: "t", Description: "d"},
			wantSkipped: []oauth.MetadataField{oauth.FieldDescription},
		},
		{
			name: "a description left blank is not skipped",
			caps: tw.MetadataCaps(),
			meta: oauth.Metadata{Title: "t"},
		},
		{
			name:        "an overlong title is refused before the wire",
			caps:        yt.MetadataCaps(),
			meta:        oauth.Metadata{Title: strings.Repeat("x", 101)},
			wantProblem: true,
		},
		{
			name: "the same title is fine for twitch",
			caps: tw.MetadataCaps(),
			meta: oauth.Metadata{Title: strings.Repeat("x", 101)},
		},
		{
			name:        "an overlong description is refused",
			caps:        yt.MetadataCaps(),
			meta:        oauth.Metadata{Description: strings.Repeat("x", 5001)},
			wantProblem: true,
		},
		{
			name: "multi-byte runes are counted as characters, not bytes",
			caps: yt.MetadataCaps(),
			meta: oauth.Metadata{Title: strings.Repeat("é", 100)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			skipped, problem := precheck(tc.meta, db.PlatformYouTube, tc.caps)
			if (problem != "") != tc.wantProblem {
				t.Fatalf("problem = %q, wantProblem %v", problem, tc.wantProblem)
			}
			if len(skipped) != len(tc.wantSkipped) {
				t.Fatalf("skipped = %v, want %v", skipped, tc.wantSkipped)
			}
			for i := range skipped {
				if skipped[i] != tc.wantSkipped[i] {
					t.Fatalf("skipped = %v, want %v", skipped, tc.wantSkipped)
				}
			}
		})
	}
}

func TestMergeFieldsUnionsWithoutDuplicating(t *testing.T) {
	tests := []struct {
		name string
		a, b []oauth.MetadataField
		want []oauth.MetadataField
	}{
		{"both empty", nil, nil, nil},
		{
			"the provider repeats what caps already predicted",
			[]oauth.MetadataField{oauth.FieldDescription},
			[]oauth.MetadataField{oauth.FieldDescription},
			[]oauth.MetadataField{oauth.FieldDescription},
		},
		{
			"a runtime skip joins a predicted one",
			[]oauth.MetadataField{oauth.FieldDescription},
			[]oauth.MetadataField{oauth.FieldCategory},
			[]oauth.MetadataField{oauth.FieldDescription, oauth.FieldCategory},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeFields(tc.a, tc.b)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestTwoDestinationsOnOneAccountWithDifferentComplianceAreRefused(t *testing.T) {
	acct := int64(7)
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "main", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "backup", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
	})
	if len(conflicts) == 0 {
		t.Fatal("two destinations asked one broadcast to be two things and it was allowed; " +
			"one of the operator's declarations would be discarded with nothing saying so")
	}
	// BOTH names, because "there is a conflict" the operator cannot locate is
	// barely better than the silence this replaces.
	if !strings.Contains(conflicts[0], "main") || !strings.Contains(conflicts[0], "backup") {
		t.Errorf("conflict %q does not name both destinations", conflicts[0])
	}
	// A refusal that still hands the account a value is not a refusal: the
	// caller has no way to tell "refused" from "resolved to the first one
	// seen" unless the absence of the entry IS the signal.
	if _, ok := got[acct]; ok {
		t.Errorf("account %d is still present in the result despite being reported as a conflict; "+
			"a caller that reads the map without checking conflicts would silently apply it", acct)
	}
}

// TestComplianceByTargetKeepsAConflictingAccountOutOfTheResolvedMap is the
// second line of defence, tested as such.
//
// handlePushMetadata refuses a conflict outright, so this can never be reached
// through the handler and no call-site guard can see it — which is exactly why
// it needs its own. The delete is what makes reading the map without checking
// conflicts fail safe: a caller that skipped the check would otherwise push the
// first destination's settings while reporting the push refused, which looks
// handled and is worse than no detection at all.
func TestComplianceByTargetKeepsAConflictingAccountOutOfTheResolvedMap(t *testing.T) {
	messy, tidy := int64(7), int64(9)
	got, conflicts := complianceByTarget([]db.Destination{
		{Name: "main", AccountID: &messy, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "backup", AccountID: &messy, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
		{Name: "elsewhere", AccountID: &tidy, Compliance: db.Compliance{Privacy: db.PrivacyUnlisted}},
	})

	if _, ok := got[messy]; ok {
		t.Errorf("the conflicting account is still in the resolved map as %+v; a caller that "+
			"read it without checking conflicts would silently apply one of two "+
			"declarations", got[messy])
	}
	if len(conflicts[messy]) == 0 {
		t.Error("the conflict was not reported against the account it is about, so a push " +
			"cannot tell whose problem it is")
	} else if !strings.Contains(conflicts[messy][0], "main") ||
		!strings.Contains(conflicts[messy][0], "backup") {
		t.Errorf("conflict %q does not name both destinations", conflicts[messy][0])
	}

	// The account that never disagreed is untouched by its neighbour's problem.
	if got[tidy].Compliance.Privacy != db.PrivacyUnlisted {
		t.Errorf("resolved privacy for the innocent account = %q, want unlisted",
			got[tidy].Compliance.Privacy)
	}
	if len(conflicts[tidy]) != 0 {
		t.Errorf("the innocent account was reported as conflicting: %v", conflicts[tidy])
	}
}

// TestThreeDisagreeingDestinationsAreAllWithheldAndAllNamed is the case two
// destinations could never show.
//
// The original loop asked the RESULT map whether it had seen this account, and
// removed the entry on conflict. With three disagreeing destinations the third
// therefore found nothing, read as the first one seen, and re-inserted: the
// account came back present, carrying the LAST destination's settings, with one
// conflict message naming only the first two. Present-and-resolved-anyway is
// exactly the "looks handled" outcome the removal exists to prevent, and the
// messier three-destination configuration is the likelier one to hit it.
func TestThreeDisagreeingDestinationsAreAllWithheldAndAllNamed(t *testing.T) {
	acct := int64(7)
	got, conflicts := complianceByTarget([]db.Destination{
		{Name: "a", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "b", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
		{Name: "c", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyUnlisted}},
	})

	if ac, ok := got[acct]; ok {
		t.Errorf("the account is present carrying %q's settings despite three destinations "+
			"disagreeing; a caller reading the map would apply one of them", ac.Destination)
	}
	// Every disagreeing destination, not just the first pair. An operator told
	// only about a-vs-b fixes it, pushes, and is refused over c.
	joined := strings.Join(conflicts[acct], " ")
	for _, name := range []string{`"a"`, `"b"`, `"c"`} {
		if !strings.Contains(joined, name) {
			t.Errorf("destination %s is never named in %v, so fixing what is reported still "+
				"leaves the push refused", name, conflicts[acct])
		}
	}
}

// TestEveryDestinationIsComparedAgainstTheFirstNotItsNeighbour is the guard
// against overcorrecting the above, and against a subtler shape: comparing each
// destination against the one BEFORE it rather than against the first.
//
// Agreement first — three destinations that all agree are ordinary, not three
// conflicts. Then the distinguishing case: with a, a, c the anchor is "a", so
// the message must name "a" and "c". A previous-neighbour comparison would name
// "b" and "c" instead, blaming a destination that agreed with everything and
// leaving the operator editing the wrong row. An all-agreeing fixture cannot
// tell those two implementations apart, which is why this one does not use one.
func TestEveryDestinationIsComparedAgainstTheFirstNotItsNeighbour(t *testing.T) {
	acct := int64(7)

	got, conflicts := complianceByTarget([]db.Destination{
		{Name: "a", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "b", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "c", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
	})
	if len(conflicts[acct]) != 0 {
		t.Errorf("three destinations that all agree were reported as conflicting: %v",
			conflicts[acct])
	}
	if got[acct].Compliance.Privacy != db.PrivacyPrivate {
		t.Errorf("resolved privacy = %q, want private", got[acct].Compliance.Privacy)
	}

	_, conflicts = complianceByTarget([]db.Destination{
		{Name: "a", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "b", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "c", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
	})
	if len(conflicts[acct]) != 1 {
		t.Fatalf("got %d conflicts, want exactly one: only %q disagrees with the anchor",
			len(conflicts[acct]), "c")
	}
	if !strings.HasPrefix(conflicts[acct][0], `"a" and "c"`) {
		t.Errorf("conflict = %q, want it to open with %q: the disagreement is with the first "+
			"destination, and naming %q blames one that agreed with everything",
			conflicts[acct][0], `"a" and "c"`, "b")
	}
}

// TestComplianceByAccountDoesNotReorderItsCallersSlice guards the defensive
// copy. The function sorts by name to make its messages deterministic; doing
// that in place would reorder a slice the caller still owns, which is the kind
// of side effect that is invisible until something downstream depends on store
// order.
func TestComplianceByAccountDoesNotReorderItsCallersSlice(t *testing.T) {
	acct := int64(7)
	dests := []db.Destination{
		{Name: "c", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
		{Name: "a", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
		{Name: "b", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
	}
	complianceByAccount(dests)
	for i, want := range []string{"c", "a", "b"} {
		if dests[i].Name != want {
			t.Fatalf("the caller's slice was reordered to %q...; complianceByAccount sorted "+
				"in place", dests[i].Name)
		}
	}
}

// TestComplianceFieldsNamesOnlyWhatWasSet pins the mapping a failed or skipped
// compliance write reports through. Its call sites are guarded elsewhere; what
// they cannot see is a single clause going missing, and the FacebookPrivacy one
// in particular — a Facebook-only failure would then name no skipped field at
// all, which combined with an unconfirmed write is the whole of Facebook's
// failure reporting gone.
func TestComplianceFieldsNamesOnlyWhatWasSet(t *testing.T) {
	kids := false
	tests := []struct {
		name string
		in   db.Compliance
		want []oauth.MetadataField
	}{
		{"nothing set", db.Compliance{}, nil},
		{"youtube privacy", db.Compliance{Privacy: db.PrivacyPrivate},
			[]oauth.MetadataField{oauth.FieldPrivacy}},
		{"facebook privacy is privacy too", db.Compliance{FacebookPrivacy: db.FBPrivacySelf},
			[]oauth.MetadataField{oauth.FieldPrivacy}},
		{"an explicit false is still a declaration", db.Compliance{MadeForKids: &kids},
			[]oauth.MetadataField{oauth.FieldMadeForKids}},
		{"twitch labels", db.Compliance{Labels: map[string]bool{"Gambling": true}},
			[]oauth.MetadataField{oauth.FieldLabels}},
		{"all of it", db.Compliance{
			Privacy: db.PrivacyPrivate, MadeForKids: &kids,
			Labels: map[string]bool{"Gambling": true}, FacebookPrivacy: db.FBPrivacySelf},
			[]oauth.MetadataField{oauth.FieldPrivacy, oauth.FieldMadeForKids, oauth.FieldLabels}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := complianceFields(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("complianceFields(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestConflictNamesDestinationsInSortedOrderNotStoreOrder(t *testing.T) {
	// Store order is c, a, b -- deliberately not sorted. Without the sort,
	// the first pair compared is (c, a), and the conflict message would name
	// "c" and "a". The function must sort by name first, so the pair actually
	// compared is (a, b) and the message names "a" before "b". Using c/a/b
	// rather than something subtler makes the difference unmistakable: the
	// two possible messages don't share a first name.
	acct := int64(7)
	_, conflicts := complianceByAccount([]db.Destination{
		{Name: "c", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
		{Name: "a", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "b", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
	})
	//
	// Two messages, not one: "a" is the anchor and both "b" and "c" disagree
	// with it, so both are named. This assertion used to demand exactly one,
	// which quietly codified a bug -- the loop compared "c" against a map entry
	// that the "b" conflict had already removed, so "c" read as the first
	// destination seen and was never reported at all. What this test is about
	// is the ORDER, so it checks the first message opens with the sorted pair
	// and leaves the count to
	// TestThreeDisagreeingDestinationsAreAllWithheldAndAllNamed.
	if len(conflicts) == 0 {
		t.Fatal("three destinations, two of them disagreeing with the first, produced no conflict")
	}
	if !strings.HasPrefix(conflicts[0], `"a" and "b"`) {
		t.Errorf("conflict = %q, want it to open with %q (sorted order), not store order", conflicts[0], `"a" and "b"`)
	}
}

func TestTwoDestinationsAgreeingIsNotAConflict(t *testing.T) {
	// The rule refuses disagreement, not duplication. An install with a primary
	// and a backup destination on one account is ordinary.
	acct := int64(7)
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "main", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "backup", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
	})
	if len(conflicts) != 0 {
		t.Fatalf("identical compliance was refused: %v", conflicts)
	}
	if got[acct].Compliance.Privacy != db.PrivacyPrivate {
		t.Errorf("resolved privacy = %q, want private", got[acct].Compliance.Privacy)
	}
}

func TestADestinationWithNoComplianceContributesNothing(t *testing.T) {
	acct := int64(7)
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "configured", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "untouched", AccountID: &acct},
	})
	if len(conflicts) != 0 {
		t.Fatalf("an empty compliance was treated as disagreement: %v", conflicts)
	}
	if got[acct].Compliance.Privacy != db.PrivacyPrivate {
		t.Errorf("resolved privacy = %q, want the configured one", got[acct].Compliance.Privacy)
	}
}

func TestADestinationWithNoAccountIsIgnored(t *testing.T) {
	// A hand-typed destination has no token to push with, so it must
	// contribute nothing to the resolved map and raise no conflict --
	// there is no account for it to conflict over.
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "manual", Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
	})
	if len(got) != 0 {
		t.Errorf("got %v, want no account to receive an unowned destination's compliance", got)
	}
	if len(conflicts) != 0 {
		t.Errorf("got conflicts %v, want none: a destination with no account has nothing to conflict with", conflicts)
	}
}

// A push carrying nothing typed, against a destination that carries stored
// compliance, is accepted rather than refused.
//
// The gate used to answer "is there anything to do" before the targets were
// known, so it could only see the composer. Once compliance started being
// pushed that answer went wrong in the worst direction: an operator correcting a
// COPPA declaration -- a legal obligation, set on the destination and not
// expressible in the composer at all -- was told to enter a title first, and the
// 400 said nothing would happen while something would.
//
// Mutation: restore `if meta.Empty() && req.Broadcast.Empty()` ahead of target
// resolution, or drop the `!anyCompliance(targets)` clause. Either makes this
// fail with a 400.
func TestACompliancePushNeedsNothingTypedInTheComposer(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	kids := true
	if _, err := store.CreateDestination(&db.Destination{
		Name: "main", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.example/live", StreamKey: "sk-live-1", AccountID: &acctID,
		Compliance: db.Compliance{MadeForKids: &kids},
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	called := false
	s.pushComplianceFn = func(ctx context.Context, pusher oauth.CompliancePusher, clientID, accessToken string,
		tgt oauth.ComplianceTarget, c db.Compliance) (*oauth.MetadataResult, error) {
		called = true
		return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldMadeForKids}}, nil
	}

	// Deliberately empty: no title, no description, no category, no broadcast.
	pushAndSettle(t, h, sign, map[string]any{})
	if !called {
		t.Fatal("an empty composer refused a push whose whole purpose was applying a " +
			"stored COPPA declaration; the operator has no other way to send it")
	}
}

// And the refusal still happens when there is genuinely nothing to do, so the
// clause above did not simply open the gate.
//
// Mutation: make anyCompliance always return true. This then fails.
func TestAnEmptyComposerWithNoStoredComplianceIsStillRefused(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	if _, err := store.CreateDestination(&db.Destination{
		Name: "main", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.example/live", StreamKey: "sk-live-1", AccountID: &acctID,
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/metadata/push", map[string]any{})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a push with nothing typed and nothing stored "+
			"has no work to do and should say so (body %s)", w.Code, w.Body.String())
	}
}
