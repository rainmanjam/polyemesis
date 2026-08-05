package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
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
// actually leaves the process.
//
// It used to replace a pushMetadataFn closure and read the oauth.Metadata that
// arrived. It now runs the real Facebook provider against a stub Graph API and
// reads the content_tags parameter of the POST it made -- which is one step
// further along the same trip, and covers resolveTags turning each word into an
// interest id as well as the words getting that far.
//
// Mutation: delete `Tags: req.Tags` from handlePushMetadata's oauth.Metadata
// literal. Observed FAIL ("no /search lookup was made for the composer's tags").
func TestPushMetadataCarriesTagsToThePusher(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
	sign := login(t, h)

	connectAccount(t, store, s.box, db.PlatformFacebook, "ada")
	if err := store.PutPlatformCreds(s.box, db.PlatformFacebook, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}

	job := pushAndSettle(t, h, sign, map[string]any{
		"title": "Live tonight",
		"tags":  []string{"cooking", "live"},
	})

	// One ad-interest lookup per word, which is where a tag that never left the
	// composer stops being visible at all.
	var looked []string
	for _, c := range stub.calls() {
		if c.Path == "/search" {
			looked = append(looked, c.Query.Get("q"))
		}
	}
	if !reflect.DeepEqual(looked, []string{"cooking", "live"}) {
		t.Fatalf("Facebook was asked to resolve %v, want [cooking live]: no /search lookup "+
			"was made for the composer's tags, so they never reached the provider", looked)
	}
	// And the resolved ids reached the write. A lookup that happens and a
	// content_tags parameter that is dropped afterwards are different bugs.
	write := stub.first(http.MethodPost, "/"+stub.fbLiveID)
	if write == nil {
		t.Fatalf("no live video edit reached Facebook; the push made %v", stub.calls())
	}
	if got := write.Query.Get("content_tags"); got != `["interest-cooking","interest-live"]` {
		t.Errorf("content_tags = %q, want the two resolved interest ids", got)
	}
	if got := write.Query.Get("title"); got != "Live tonight" {
		t.Errorf("title = %q, want the composer's own", got)
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
// Both accounts are here because a ComplianceTarget assembled from the wrong
// halves reaches the wrong place, and the two halves are observable on
// different platforms: Twitch addresses its write with AccountRef, and every
// call carries the account's own bearer token. YouTube's two endpoints are the
// COPPA half -- privacyStatus and selfDeclaredMadeForKids live on two different
// resources, and anyone who assumes symmetry writes a call that returns 200 and
// changes nothing.
//
// Mutation: delete the ComplianceFor branch from pushOne. Observed FAIL ("no
// privacy write reached YouTube").
func TestAPushSendsTheStoredComplianceToTheProvider(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
	sign := login(t, h)

	ytID := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	twID := connectAccount(t, store, s.box, db.PlatformTwitch, "dj")
	for _, p := range []db.Platform{db.PlatformYouTube, db.PlatformTwitch} {
		if err := store.PutPlatformCreds(s.box, p, "cid", "topsecret"); err != nil {
			t.Fatalf("creds %s: %v", p, err)
		}
	}
	kids := true
	for _, d := range []*db.Destination{
		{Name: "main", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
			URL: "rtmp://a.example/live", StreamKey: "sk-live-1", AccountID: &ytID,
			Compliance: db.Compliance{Privacy: db.PrivacyPrivate, MadeForKids: &kids}},
		{Name: "twitch", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
			URL: "rtmp://b.example/live", StreamKey: "sk-live-2", AccountID: &twID,
			Compliance: db.Compliance{Labels: map[string]bool{"Gambling": true}}},
	} {
		if _, err := store.CreateDestination(d); err != nil {
			t.Fatalf("create %s: %v", d.Name, err)
		}
	}

	job := pushAndSettle(t, h, sign, map[string]any{"title": "Live tonight"})

	// privacyStatus is on the BROADCAST, part=status.
	privacy := stubCallWith(stub, http.MethodPut, "/liveBroadcasts", "part", "status")
	if privacy == nil {
		t.Fatalf("no privacy write reached YouTube: the operator's stored privacy and "+
			"COPPA declaration were saved, validated, and never sent anywhere. "+
			"The push made %v", stub.calls())
	}
	if got := nestedString(privacy.Body, "status", "privacyStatus"); got != "private" {
		t.Errorf("privacyStatus = %q, want the stored \"private\" (body %+v)", got, privacy.Body)
	}
	// selfDeclaredMadeForKids is absent from liveBroadcasts.update's settable
	// list, so it has to go through videos.update against the same id. A push
	// that sent it to the broadcast would get a 200 and change nothing.
	kidsCall := stubCallWith(stub, http.MethodPut, "/videos", "part", "status")
	if kidsCall == nil {
		t.Fatalf("no made-for-kids write reached YouTube; the push made %v", stub.calls())
	}
	if got := nestedAny(kidsCall.Body, "status", "selfDeclaredMadeForKids"); got != true {
		t.Errorf("selfDeclaredMadeForKids = %v, want the stored true (body %+v)", got, kidsCall.Body)
	}
	// The account's own token, on the account's own write.
	if privacy.Auth != "Bearer at" {
		t.Errorf("the privacy write carried %q, want the account's own bearer token", privacy.Auth)
	}

	// And Twitch's half: AccountRef is what addresses the write, so a target
	// assembled from the wrong halves reaches the wrong channel.
	var labels *stubCall
	for _, c := range stub.matching(http.MethodPatch, "/channels") {
		if c.Body["content_classification_labels"] != nil {
			found := c
			labels = &found
		}
	}
	if labels == nil {
		t.Fatalf("no label write reached Twitch at all; the push made %v", stub.calls())
	}
	if got := labels.Query.Get("broadcaster_id"); got != "dj-ref" {
		t.Errorf("the label write addressed broadcaster_id=%q, want the account's own ref: "+
			"a ComplianceTarget assembled from the wrong halves reaches the wrong channel", got)
	}

	if len(job.Results) != 2 {
		t.Fatalf("results = %+v, want one row per account", job.Results)
	}
	// The compliance result has to reach the row the composer renders. Applied
	// fields that the provider reported but the outcome dropped would tell the
	// operator their COPPA declaration did not land when it did.
	applied := map[oauth.MetadataField]bool{}
	for _, row := range job.Results {
		for _, f := range row.Applied {
			applied[f] = true
		}
	}
	for _, f := range []oauth.MetadataField{oauth.FieldPrivacy, oauth.FieldMadeForKids, oauth.FieldLabels} {
		if !applied[f] {
			t.Errorf("%q is missing from the reported results %+v, so a compliance field that "+
				"was applied is invisible to the operator", f, job.Results)
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
//
// Mutations: move the `if len(problems) > 0` refusal to AFTER
// `go s.runMetadataPush(...)`. Observed FAIL, on both halves -- "a push job was
// created before the conflict was refused" and a liveBroadcasts.update on the
// wire for a request answered 400. Or delete the refusal outright: observed
// FAIL ("status = 202, want 400").
func TestAComplianceConflictIsRefusedBeforeAnythingIsSent(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
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
	// Direct half: nothing may have left the process. The stub records every
	// request any provider makes, so this is now a question about the wire
	// rather than about a closure that stood in for it, and the wait is what
	// makes it an assertion rather than a race -- a push that started would
	// have reached YouTube well inside it.
	time.Sleep(500 * time.Millisecond)
	if made := stub.calls(); len(made) != 0 {
		t.Fatalf("%v was sent for a push that was refused; the operator was told "+
			"nothing was sent while one destination's compliance was already on the wire", made)
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
// Mutation: drop `res.Skipped = append(res.Skipped, cres.Skipped...)` and
// `res.Warnings = append(res.Warnings, cres.Warnings...)` from pushOne's
// `case cres != nil`. Observed FAIL ("warnings = []", "skipped = []",
// "state = ok, want partial").
func TestAnUnconfirmedComplianceWriteReachesTheOperatorWithoutAnError(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformFacebook, "ada")
	if err := store.PutPlatformCreds(s.box, db.PlatformFacebook, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	// A NUMERIC stream key, because Facebook's key IS the live video id and
	// ComplianceTarget.StreamKey is the only place PushCompliance can recover
	// it from. A target assembled from the wrong halves lands on no broadcast
	// at all, and this is where that shows.
	if _, err := store.CreateDestination(&db.Destination{
		Name: "page", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://ingest.example/app", StreamKey: "1234567890", AccountID: &acctID,
		Compliance: db.Compliance{FacebookPrivacy: db.FBPrivacySelf},
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	job := pushAndSettle(t, h, sign, map[string]any{"title": "Live tonight"})

	// The write went to the broadcast the stream key names. Graph accepts it
	// and the read-back disagrees, which is the whole failure channel: no
	// error, a skipped field and the reason.
	if wrote := stub.first(http.MethodPost, "/1234567890"); wrote == nil {
		t.Fatalf("no privacy write reached the live video the stream key names; "+
			"the push made %v", stub.calls())
	} else if got := wrote.Query.Get("privacy"); got != `{"value":"SELF"}` {
		t.Errorf("privacy = %q, want the destination's stored SELF", got)
	}

	if len(job.Results) != 1 {
		t.Fatalf("results = %+v, want one row", job.Results)
	}
	res := job.Results[0]
	if !strings.Contains(strings.Join(res.Warnings, " "), "not confirmed") {
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
	// And the row names the broadcast rather than the account, so the operator
	// can tell which one was touched. The compliance result's own Target is
	// covered by TestACompliancePushNeedsNothingTypedInTheComposer, which is
	// the push where the metadata half names nothing at all.
	if res.Target == "ada" {
		t.Errorf("target = %q -- falling back to the account name leaves the operator "+
			"guessing which broadcast moved", res.Target)
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
//
// Mutation: delete the two appends from pushOne's else branch -- the Skipped
// fields and the "has no compliance API" warning. Observed FAIL ("skipped = []",
// "warnings = []").
func TestAKickTargetIsSkippedOutLoudRatherThanSilently(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
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

	job := pushAndSettle(t, h, sign, map[string]any{"title": "Live tonight"})

	// Kick's own title write and nothing else. ComplianceFor answers false for
	// Kick, so a second request here would mean the call site resolved
	// compliance through some other platform's provider and wrote the
	// operator's declaration somewhere it does not belong.
	for _, c := range stub.calls() {
		if c.Path != "/public/v1/channels" {
			t.Errorf("%v was sent for a Kick account, which has no compliance API; "+
				"ComplianceFor's absence is being ignored at the call site", c)
		}
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
//
// Mutation: replace `if !t.Compliance.Empty()` in pushOne with `if true`.
// Observed FAIL ("state = partial, want ok" and a warning about compliance
// settings this account never stored).
func TestAnAccountWithNoStoredComplianceIsLeftEntirelyAlone(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
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

	job := pushAndSettle(t, h, sign, map[string]any{"title": "Live tonight"})

	for _, c := range stub.calls() {
		if c.Path != "/public/v1/channels" {
			t.Errorf("%v was sent for an account with no compliance stored", c)
		}
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
//
// The overlap has to be MEASURED, which is what this used to miss. It built a
// `start` channel and closed it before the push began, so both stubs ran
// straight through and two sequential workers satisfied every assertion —
// removing `go` from runMetadataPush's fan-out left it green, and the -race
// coverage it claimed did not exist. It now records the peak number of stubs in
// flight and asserts it reached two.
//
// The rendezvous is bounded rather than a bare barrier: a sequential fan-out
// would block on an unbounded one forever, and a test that hangs reports
// nothing. The wait is only ever paid by a run that is already failing.
//
// Mutation: drop the `go` from `go func(i int, t metadataTarget)` in
// runMetadataPush. Observed FAIL ("compliance pushes never overlapped: peak 1
// in flight").
func TestTwoAccountsPushComplianceConcurrently(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
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

	// Each compliance request announces itself, then waits for the other to
	// arrive. Both are only released once both are inside the stub, which is
	// what makes the overlap real instead of assumed -- and `peak` is the
	// measurement, so a sequential fan-out is a failure with a number in it
	// rather than a hang.
	//
	// The predicate has to tell a compliance write from a metadata one:
	// Twitch's two writes are both PATCH /channels, and only the compliance
	// one carries content_classification_labels.
	const targets = 2
	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	both := make(chan struct{})
	var once sync.Once
	stub.setOnCall(func(c stubCall) {
		isCompliance := (c.Path == "/liveBroadcasts" && c.Query.Get("part") == "status") ||
			(c.Path == "/channels" && c.Body["content_classification_labels"] != nil)
		if !isCompliance {
			return
		}
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		arrived := inFlight
		mu.Unlock()

		if arrived == targets {
			once.Do(func() { close(both) })
		}
		select {
		case <-both:
		case <-time.After(2 * time.Second):
			// Only reachable when the pushes are serialised; the assertion on
			// peak below is what reports it. Two of these have to fit inside
			// pushAndSettle's own 10s deadline, or the failure comes back as
			// "the push never finished" instead of naming the missing overlap.
		}

		mu.Lock()
		inFlight--
		mu.Unlock()
	})

	job := pushAndSettle(t, h, sign, map[string]any{"title": "Live tonight"})
	mu.Lock()
	gotPeak := peak
	mu.Unlock()
	if gotPeak != targets {
		t.Fatalf("compliance pushes never overlapped: peak %d in flight, want %d. "+
			"The point of this test is to give -race two PushCompliance calls at "+
			"once; with a serialised fan-out it watches nothing.", gotPeak, targets)
	}
	if len(job.Results) != 2 {
		t.Fatalf("results = %+v, want one row per account", job.Results)
	}
	// Each account's own settings, not one account's applied twice.
	privacy := stubCallWith(stub, http.MethodPut, "/liveBroadcasts", "part", "status")
	if privacy == nil || nestedString(privacy.Body, "status", "privacyStatus") != "private" {
		t.Errorf("YouTube did not receive its own stored privacy: %+v", privacy)
	}
	labels := 0
	for _, c := range stub.matching(http.MethodPatch, "/channels") {
		if c.Body["content_classification_labels"] != nil {
			labels++
		}
	}
	if labels != 1 {
		t.Errorf("%d label writes reached Twitch, want exactly the one account that "+
			"stored labels", labels)
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
// Mutation: put back `out.Warnings = res.Warnings` in place of the append.
// Observed FAIL ("warnings = [], want the broadcast failure's own reason").
func TestABroadcastFailureIsStillReportedAlongsideAMetadataSuccess(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
	sign := login(t, h)

	connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}

	// Only the broadcast write fails. The title write goes to the same path
	// with a different `part`, so refusing by path alone would fail both and
	// prove nothing about the two halves disagreeing.
	stub.setReject(func(c stubCall) string {
		if c.Method == http.MethodPut && c.Path == "/liveBroadcasts" &&
			strings.Contains(c.Query.Get("part"), "contentDetails") {
			return "youtube said no: the broadcast is already live"
		}
		return ""
	})

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
	// The operator's own settings have to be what reaches the platform. Sending
	// a zero BroadcastSettings here would be the composer-tags defect again:
	// the call is made, the row reports a result, and the toggle the operator
	// actually moved was never in the request.
	write := stubCallWith(stub, http.MethodPut, "/liveBroadcasts", "part", "snippet,contentDetails")
	if write == nil {
		t.Fatalf("no broadcast write reached YouTube at all; the push made %v", stub.calls())
	}
	if got := nestedAny(write.Body, "contentDetails", "enableDvr"); got != false {
		t.Errorf("enableDvr = %v, want the explicit false the operator sent (body %+v)",
			got, write.Body)
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
// Mutation: range over `conflicts` instead of over `targets` when collecting
// problems, which refuses whenever any conflict exists anywhere. Observed FAIL
// ("push: status 400" naming two destinations this push never addressed).
func TestAConflictOnAnUnselectedAccountDoesNotBlockThePush(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
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

	job := pushAndSettle(t, h, sign, map[string]any{
		"title":      "Live tonight",
		"accountIds": []int64{clean},
	})
	if len(job.Results) != 1 || job.Results[0].AccountID != clean {
		t.Fatalf("results = %+v, want only the selected account", job.Results)
	}
	// The selected account's own compliance still went, unaffected by a
	// disagreement somewhere else entirely.
	privacy := stubCallWith(stub, http.MethodPut, "/liveBroadcasts", "part", "status")
	if privacy == nil || nestedString(privacy.Body, "status", "privacyStatus") != "private" {
		t.Errorf("the selected account's private setting never reached YouTube; "+
			"the push made %v", stub.calls())
	}
	// And nothing was sent for the account that was not selected.
	if n := len(stub.matching(http.MethodPatch, "/channels")); n != 0 {
		t.Errorf("%d writes reached the Twitch account this push never named", n)
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
//
// Mutation: delete the `if len(problems) > 0` refusal from handlePushMetadata.
// Observed FAIL ("status = 202, want 400").
//
// Note what the wire assertion below can and cannot see here: this test seeds
// no developer credentials, so pushOne fails at GetPlatformCreds before any
// provider is reached. The stub is the belt to the 400's braces, not the guard
// itself -- TestAComplianceConflictIsRefusedBeforeAnythingIsSent is the one
// that proves nothing left the process, and it does seed them.
func TestAConflictOnTheSelectedAccountStillRefuses(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
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
	time.Sleep(500 * time.Millisecond)
	if made := stub.calls(); len(made) != 0 {
		t.Fatalf("%v was sent for a push that was refused", made)
	}
}

// TestAFailedComplianceWriteIsReportedRatherThanSwallowed was written because
// the mutation that removes the error branch's whole body left the suite green:
// a compliance write that failed would have been indistinguishable from one
// that succeeded, which is the same silence this branch exists to end, one
// layer in. It also pins the failure as PARTIAL rather than fatal -- the title
// above it may already have landed, and failing the row would send the operator
// back to redo work that took.
//
// Mutation: delete the body of pushOne's `case err != nil` on the compliance
// write. Observed FAIL ("warnings = []", both declarations missing from
// skipped, "state = ok, want partial").
func TestAFailedComplianceWriteIsReportedRatherThanSwallowed(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
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

	// The privacy write alone refuses. The title write is the same path with a
	// different `part`, and it has to keep working: the point of the row being
	// PARTIAL is that one half landed and the other did not.
	stub.setReject(func(c stubCall) string {
		if c.Method == http.MethodPut && c.Path == "/liveBroadcasts" && c.Query.Get("part") == "status" {
			return "youtube said no: insufficient scope"
		}
		return ""
	})

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
	s, h, store, stub := stubbedServer(t, config.Config{})
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

	// Deliberately empty: no title, no description, no category, no broadcast.
	job := pushAndSettle(t, h, sign, map[string]any{})
	if stubCallWith(stub, http.MethodPut, "/videos", "part", "status") == nil {
		t.Fatalf("an empty composer refused a push whose whole purpose was applying a "+
			"stored COPPA declaration; the operator has no other way to send it. "+
			"The push made %v", stub.calls())
	}
	// This is the one push where the metadata half names nothing, so the row's
	// target can only have come from the compliance result. Without that
	// fallback the row names the account and the operator cannot tell which
	// broadcast the declaration landed on.
	if len(job.Results) != 1 {
		t.Fatalf("results = %+v, want one row", job.Results)
	}
	if job.Results[0].Target != "Tonight's broadcast" {
		t.Errorf("target = %q, want the broadcast the compliance result named", job.Results[0].Target)
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
