package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// capture records what a stub platform received, so a test can assert on the
// request body rather than only on the returned result.
type capture struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
}

func recordAll(t *testing.T, log *[]capture, h func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		c := capture{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &c.Body)
		}
		*log = append(*log, c)
		h(w, r)
	}
}

func find(log []capture, method, path string) *capture {
	for i := range log {
		if log[i].Method == method && log[i].Path == path {
			return &log[i]
		}
	}
	return nil
}

// ------------------------------------------------------------- capability set

func TestMetadataCapabilityIsAbsentRatherThanErroringForPlatformsThatCannotDoIt(t *testing.T) {
	tests := []struct {
		name     string
		platform db.Platform
		want     bool
	}{
		{"youtube can push metadata", db.PlatformYouTube, true},
		{"twitch can push metadata", db.PlatformTwitch, true},
		{"kick has no provider at all", db.PlatformKick, false},
		{"an unknown platform is absent", db.Platform("mystery"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := MetadataFor(tc.platform)
			if ok != tc.want {
				t.Fatalf("MetadataFor(%q) = %v, want %v", tc.platform, ok, tc.want)
			}
		})
	}
}

func TestMetadataCapsDescribeWhatEachPlatformActuallyAccepts(t *testing.T) {
	tests := []struct {
		name     string
		platform db.Platform
		field    MetadataField
		want     bool
	}{
		{"youtube takes a title", db.PlatformYouTube, FieldTitle, true},
		{"youtube takes a description", db.PlatformYouTube, FieldDescription, true},
		{"youtube takes a category", db.PlatformYouTube, FieldCategory, true},
		{"twitch takes a title", db.PlatformTwitch, FieldTitle, true},
		{"twitch has no description", db.PlatformTwitch, FieldDescription, false},
		{"twitch takes a category", db.PlatformTwitch, FieldCategory, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mp, ok := MetadataFor(tc.platform)
			if !ok {
				t.Fatalf("MetadataFor(%q) reported no capability", tc.platform)
			}
			if got := mp.MetadataCaps().Accepts(tc.field); got != tc.want {
				t.Fatalf("Accepts(%q) = %v, want %v", tc.field, got, tc.want)
			}
		})
	}
}

func TestMetadataTrimmedAndEmpty(t *testing.T) {
	tests := []struct {
		name      string
		in        Metadata
		wantEmpty bool
		wantTitle string
	}{
		{"all blank is empty", Metadata{}, true, ""},
		{"whitespace only is empty", Metadata{Title: "  \n\t "}, true, ""},
		{"a pasted title is trimmed", Metadata{Title: "  Friday night set\n"}, false, "Friday night set"},
		{"a category alone is enough", Metadata{Category: "Music"}, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Trimmed()
			if got.Empty() != tc.wantEmpty {
				t.Fatalf("Empty() = %v, want %v", got.Empty(), tc.wantEmpty)
			}
			if got.Title != tc.wantTitle {
				t.Fatalf("Title = %q, want %q", got.Title, tc.wantTitle)
			}
		})
	}
}

func TestNormaliseCategoryForgivesHowPeopleActuallyType(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		same bool
	}{
		{"case differs", "Gaming", "gaming", true},
		{"ampersand spelled out", "Science & Technology", "science and technology", true},
		{"double spaces from a paste", "Just  Chatting", "just chatting", true},
		{"leading whitespace", "  Music ", "music", true},
		{"genuinely different names", "Music", "Gaming", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normaliseCategory(tc.a) == normaliseCategory(tc.b); got != tc.same {
				t.Fatalf("normalise(%q)==normalise(%q) = %v, want %v", tc.a, tc.b, got, tc.same)
			}
		})
	}
}

func TestScopeAdviceNamesTheScopeOnlyForAuthorizationFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantScope  bool
		wantSameAs bool
	}{
		{"401 earns the reconnect advice", &statusError{Status: 401, URL: "u", Body: "b"}, true, false},
		{"403 earns it too", &statusError{Status: 403, URL: "u", Body: "b"}, true, false},
		{"a 500 is left alone", &statusError{Status: 500, URL: "u", Body: "b"}, false, true},
		{"a transport error is left alone", io.ErrUnexpectedEOF, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scopeAdvice(tc.err, db.PlatformTwitch, "channel:manage:broadcast")
			if has := strings.Contains(got.Error(), "channel:manage:broadcast"); has != tc.wantScope {
				t.Fatalf("advice %q mentions scope = %v, want %v", got, has, tc.wantScope)
			}
			if same := got == tc.err; same != tc.wantSameAs {
				t.Fatalf("returned the original error = %v, want %v", same, tc.wantSameAs)
			}
		})
	}
}

// ---------------------------------------------------------------- YouTube

// ytStub serves the four YouTube endpoints a push touches.
func ytStub(t *testing.T, log *[]capture, broadcasts string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(recordAll(t, log, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/liveBroadcasts":
			io.WriteString(w, broadcasts)
		case r.Method == http.MethodPut && r.URL.Path == "/liveBroadcasts":
			io.WriteString(w, `{"id":"bcast"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/videoCategories":
			io.WriteString(w, `{"items":[
				{"id":"20","snippet":{"title":"Gaming","assignable":true}},
				{"id":"28","snippet":{"title":"Science & Technology","assignable":true}},
				{"id":"18","snippet":{"title":"Short Movies","assignable":false}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/videos":
			io.WriteString(w, `{"items":[{"snippet":{"title":"old","description":"old desc",
				"categoryId":"1","tags":["keep","me"]}}]}`)
		case r.Method == http.MethodPut && r.URL.Path == "/videos":
			io.WriteString(w, `{"id":"bcast"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	old := ytAPIBase
	ytAPIBase = srv.URL
	t.Cleanup(func() { ytAPIBase = old; srv.Close() })
	return srv
}

func TestYouTubePicksTheBroadcastTheOperatorMeans(t *testing.T) {
	tests := []struct {
		name  string
		items string
		want  string
		err   string
	}{
		{
			name: "the live one beats an earlier scheduled one",
			items: `{"items":[
				{"id":"soon","snippet":{"title":"soon","scheduledStartTime":"2026-01-01T00:00:00Z"},"status":{"lifeCycleStatus":"ready"}},
				{"id":"onair","snippet":{"title":"onair"},"status":{"lifeCycleStatus":"live"}}]}`,
			want: "onair",
		},
		{
			name: "a completed broadcast is never chosen",
			items: `{"items":[
				{"id":"done","snippet":{"title":"done"},"status":{"lifeCycleStatus":"complete"}},
				{"id":"next","snippet":{"title":"next","scheduledStartTime":"2026-07-01T10:00:00Z"},"status":{"lifeCycleStatus":"ready"}}]}`,
			want: "next",
		},
		{
			name: "the soonest upcoming wins",
			items: `{"items":[
				{"id":"later","snippet":{"title":"later","scheduledStartTime":"2026-08-01T10:00:00Z"},"status":{"lifeCycleStatus":"ready"}},
				{"id":"sooner","snippet":{"title":"sooner","scheduledStartTime":"2026-07-01T10:00:00Z"},"status":{"lifeCycleStatus":"ready"}}]}`,
			want: "sooner",
		},
		{
			name:  "nothing schedulable is an actionable error",
			items: `{"items":[{"id":"done","snippet":{"title":"done"},"status":{"lifeCycleStatus":"complete"}}]}`,
			err:   "no live or upcoming broadcast",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var log []capture
			ytStub(t, &log, tc.items)

			got, err := (&YouTube{}).liveBroadcast(context.Background(), "tok")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want one containing %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("liveBroadcast: %v", err)
			}
			if got.ID != tc.want {
				t.Fatalf("chose %q, want %q", got.ID, tc.want)
			}
		})
	}
}

const ytOneUpcoming = `{"items":[{"id":"bcast","snippet":{"title":"old title","description":"old body",
	"scheduledStartTime":"2026-07-01T10:00:00Z"},"status":{"lifeCycleStatus":"ready"}}]}`

func TestYouTubePushWritesTitleDescriptionAndCategory(t *testing.T) {
	var log []capture
	ytStub(t, &log, ytOneUpcoming)

	res, err := (&YouTube{}).PushMetadata(context.Background(), "cid", "tok", "4242", Metadata{
		Title:       "Friday night set",
		Description: "Two hours of house.",
		Category:    "science and technology",
	})
	if err != nil {
		t.Fatalf("PushMetadata: %v", err)
	}

	put := find(log, http.MethodPut, "/liveBroadcasts")
	if put == nil {
		t.Fatal("the broadcast snippet was never updated")
	}
	snip, _ := put.Body["snippet"].(map[string]any)
	if snip["title"] != "Friday night set" || snip["description"] != "Two hours of house." {
		t.Fatalf("snippet = %#v", snip)
	}
	// Dropping scheduledStartTime is how YouTube rejects the whole update.
	if snip["scheduledStartTime"] != "2026-07-01T10:00:00Z" {
		t.Fatalf("scheduledStartTime was not echoed back: %#v", snip)
	}

	vid := find(log, http.MethodPut, "/videos")
	if vid == nil {
		t.Fatal("the category was never written")
	}
	vsnip, _ := vid.Body["snippet"].(map[string]any)
	if vsnip["categoryId"] != "28" {
		t.Fatalf("categoryId = %v, want 28", vsnip["categoryId"])
	}
	// The video update replaces the snippet part, so anything already on the
	// video has to ride along or it is erased.
	tags, _ := vsnip["tags"].([]any)
	if len(tags) != 2 {
		t.Fatalf("existing tags were not preserved: %#v", vsnip["tags"])
	}

	if res.Category != "Science & Technology" {
		t.Fatalf("resolved category = %q", res.Category)
	}
	if len(res.Applied) != 3 {
		t.Fatalf("applied = %v, want all three fields", res.Applied)
	}
}

func TestYouTubePushKeepsExistingValuesForFieldsLeftBlank(t *testing.T) {
	var log []capture
	ytStub(t, &log, ytOneUpcoming)

	res, err := (&YouTube{}).PushMetadata(context.Background(), "cid", "tok", "4242",
		Metadata{Title: "New title"})
	if err != nil {
		t.Fatalf("PushMetadata: %v", err)
	}
	snip, _ := find(log, http.MethodPut, "/liveBroadcasts").Body["snippet"].(map[string]any)
	if snip["description"] != "old body" {
		t.Fatalf("a blank description blanked the broadcast: %#v", snip)
	}
	if len(res.Applied) != 1 || res.Applied[0] != FieldTitle {
		t.Fatalf("applied = %v, want only the title", res.Applied)
	}
	if find(log, http.MethodPut, "/videos") != nil {
		t.Fatal("no category was requested, so the video should not have been touched")
	}
}

func TestYouTubeUnknownCategoryDoesNotLoseTheTitleThatLanded(t *testing.T) {
	var log []capture
	ytStub(t, &log, ytOneUpcoming)

	res, err := (&YouTube{}).PushMetadata(context.Background(), "cid", "tok", "4242",
		Metadata{Title: "New title", Category: "Underwater Basket Weaving"})
	if err != nil {
		t.Fatalf("PushMetadata returned an error for a partial success: %v", err)
	}
	if len(res.Applied) != 1 || res.Applied[0] != FieldTitle {
		t.Fatalf("applied = %v, want the title", res.Applied)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != FieldCategory {
		t.Fatalf("skipped = %v, want the category", res.Skipped)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "Gaming") {
		t.Fatalf("warning should list the valid categories, got %v", res.Warnings)
	}
}

func TestYouTubeRefusalNamesTheScopeToReconnectFor(t *testing.T) {
	var log []capture
	srv := httptest.NewServer(recordAll(t, &log, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, ytOneUpcoming)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"message":"insufficient authentication scopes"}}`)
	}))
	defer srv.Close()
	old := ytAPIBase
	ytAPIBase = srv.URL
	defer func() { ytAPIBase = old }()

	_, err := (&YouTube{}).PushMetadata(context.Background(), "cid", "tok", "4242", Metadata{Title: "x"})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "Settings → Platforms") {
		t.Fatalf("error should tell the operator how to fix it, got %q", err)
	}
}

// ----------------------------------------------------------------- Twitch

func twitchStub(t *testing.T, log *[]capture, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(recordAll(t, log, h))
	old := twitchHelixBase
	twitchHelixBase = srv.URL
	t.Cleanup(func() { twitchHelixBase = old; srv.Close() })
}

func twitchDefault(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/users":
		io.WriteString(w, `{"data":[{"id":"4242","login":"dj","display_name":"DJ"}]}`)
	case r.URL.Path == "/games":
		if r.URL.Query().Get("name") == "Just Chatting" {
			io.WriteString(w, `{"data":[{"id":"509658","name":"Just Chatting"}]}`)
			return
		}
		io.WriteString(w, `{"data":[]}`)
	case r.URL.Path == "/search/categories":
		io.WriteString(w, `{"data":[{"id":"1469308723","name":"Software and Game Development"}]}`)
	case r.Method == http.MethodPatch && r.URL.Path == "/channels":
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestTwitchPushSetsTitleAndGameAndReportsTheMissingDescription(t *testing.T) {
	var log []capture
	twitchStub(t, &log, twitchDefault)

	res, err := (&Twitch{}).PushMetadata(context.Background(), "cid", "tok", "4242", Metadata{
		Title:       "Friday night set",
		Description: "Twitch has nowhere to put this.",
		Category:    "Just Chatting",
	})
	if err != nil {
		t.Fatalf("PushMetadata: %v", err)
	}

	patch := find(log, http.MethodPatch, "/channels")
	if patch == nil {
		t.Fatal("the channel was never modified")
	}
	if patch.Body["title"] != "Friday night set" {
		t.Fatalf("title = %v", patch.Body["title"])
	}
	if patch.Body["game_id"] != "509658" {
		t.Fatalf("game_id = %v, want the id looked up from the name", patch.Body["game_id"])
	}
	if !strings.Contains(patch.Query, "broadcaster_id=4242") {
		t.Fatalf("query = %q, want the broadcaster id", patch.Query)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != FieldDescription {
		t.Fatalf("skipped = %v, want the description", res.Skipped)
	}
	if len(res.Applied) != 2 {
		t.Fatalf("applied = %v, want title and category", res.Applied)
	}
}

func TestTwitchFallsBackToCategorySearchWhenTheExactNameMisses(t *testing.T) {
	var log []capture
	twitchStub(t, &log, twitchDefault)

	res, err := (&Twitch{}).PushMetadata(context.Background(), "cid", "tok", "4242",
		Metadata{Category: "software and game development"})
	if err != nil {
		t.Fatalf("PushMetadata: %v", err)
	}
	if res.Category != "Software and Game Development" {
		t.Fatalf("category = %q", res.Category)
	}
	if find(log, http.MethodGet, "/search/categories") == nil {
		t.Fatal("the search fallback was never tried")
	}
}

func TestTwitchUnknownCategoryStillPushesTheTitle(t *testing.T) {
	var log []capture
	twitchStub(t, &log, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/categories" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[]}`)
			return
		}
		twitchDefault(w, r)
	})

	res, err := (&Twitch{}).PushMetadata(context.Background(), "cid", "tok", "4242",
		Metadata{Title: "Friday night set", Category: "Nonexistent Game"})
	if err != nil {
		t.Fatalf("PushMetadata: %v", err)
	}
	patch := find(log, http.MethodPatch, "/channels")
	if patch == nil {
		t.Fatal("the title should still have been pushed")
	}
	if _, ok := patch.Body["game_id"]; ok {
		t.Fatalf("an unresolved category must not be sent: %#v", patch.Body)
	}
	if len(res.Applied) != 1 || res.Applied[0] != FieldTitle {
		t.Fatalf("applied = %v", res.Applied)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("the operator was never told the category did not exist")
	}
}

func TestTwitchWithNothingItAcceptsMakesNoWrite(t *testing.T) {
	var log []capture
	twitchStub(t, &log, twitchDefault)

	res, err := (&Twitch{}).PushMetadata(context.Background(), "cid", "tok", "4242",
		Metadata{Description: "only a description"})
	if err != nil {
		t.Fatalf("PushMetadata: %v", err)
	}
	if find(log, http.MethodPatch, "/channels") != nil {
		t.Fatal("there was nothing to write, so the channel must not be touched")
	}
	if len(res.Applied) != 0 {
		t.Fatalf("applied = %v, want nothing", res.Applied)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != FieldDescription {
		t.Fatalf("skipped = %v", res.Skipped)
	}
}
