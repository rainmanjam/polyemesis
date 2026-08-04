package api

import (
	"context"
	"encoding/json"
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
	_, conflicts := complianceByAccount([]db.Destination{
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
	// A hand-typed destination has no token to push with.
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "manual", Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
	})
	if len(conflicts) != 0 || len(got) != 0 {
		t.Errorf("got %v / %v, want both empty", got, conflicts)
	}
}
