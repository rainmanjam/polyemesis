package api

import (
	"encoding/json"
	"net/http"
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
