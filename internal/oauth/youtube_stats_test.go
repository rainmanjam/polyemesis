package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// YouTube's viewer stats, which cost two calls and can half-fail.
//
// Fixtures here are shaped like the documented responses -- liveBroadcasts.list
// returns items[] of broadcast resources, videos.list returns items[] whose
// liveStreamingDetails may or may not carry concurrentViewers -- rather than
// like the structs that decode them. kick.go's stats section records what the
// other habit costs: a fake that agrees with the code proves only that the code
// agrees with itself.
func youtubeStatsStub(t *testing.T, broadcasts, videos http.HandlerFunc) *YouTube {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/channels":
			_, _ = w.Write([]byte(`{"items":[{"id":"UCchannel","snippet":{"title":"Night Owl"}}]}`))
		case ytBroadcastsPath:
			broadcasts(w, r)
		case ytVideosPath:
			if videos == nil {
				t.Error("videos.list was called for a channel with no live broadcast")
				return
			}
			videos(w, r)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return NewYouTube(WithBaseURL(srv.URL))
}

func TestYouTubeStatsJoinsTheBroadcastToItsVideoWithoutInventingACount(t *testing.T) {
	viewers := func(n int) *int { return &n }
	const liveBroadcast = `{"items":[{"id":"vid123","snippet":{"channelId":"UCchannel",` +
		`"title":"Friday night set","actualStartTime":"2026-08-16T20:00:00Z"}}]}`

	tests := []struct {
		name        string
		broadcasts  string
		bStatus     int
		videos      string
		vStatus     int
		noVideoCall bool
		wantLive    bool
		wantViewers *int
		wantTitle   string
		wantErr     bool
	}{
		{
			// The documented wire form: Google serialises 64-bit values as
			// quoted strings, so this is what a real response looks like.
			name:        "a live broadcast with a quoted count reports it",
			broadcasts:  liveBroadcast,
			videos:      `{"items":[{"liveStreamingDetails":{"concurrentViewers":"1312","actualStartTime":"2026-08-16T20:00:00Z"}}]}`,
			wantLive:    true,
			wantViewers: viewers(1312),
			wantTitle:   "Friday night set",
		},
		{
			// The type the reference states -- "concurrentViewers: unsigned
			// long". Both spellings must decode, because betting on one fails
			// the WHOLE response and takes liveness down with the count.
			name:        "a bare numeric count decodes just as readily",
			broadcasts:  liveBroadcast,
			videos:      `{"items":[{"liveStreamingDetails":{"concurrentViewers":1312}}]}`,
			wantLive:    true,
			wantViewers: viewers(1312),
			wantTitle:   "Friday night set",
		},
		{
			// The case the pointer exists for. YouTube omits the key when the
			// owner has hidden the count, when nobody is watching, and after
			// the broadcast ends -- three states, one absent key, and reporting
			// zero would be a number polyemesis made up for all three.
			name:        "an absent count is not a count of zero",
			broadcasts:  liveBroadcast,
			videos:      `{"items":[{"liveStreamingDetails":{"actualStartTime":"2026-08-16T20:00:00Z"}}]}`,
			wantLive:    true,
			wantViewers: nil,
			wantTitle:   "Friday night set",
		},
		{
			name:        "a genuine zero survives, because YouTube said zero",
			broadcasts:  liveBroadcast,
			videos:      `{"items":[{"liveStreamingDetails":{"concurrentViewers":"0"}}]}`,
			wantLive:    true,
			wantViewers: viewers(0),
			wantTitle:   "Friday night set",
		},
		{
			name:        "an idle channel is an answer, and never asks about a video",
			broadcasts:  `{"items":[]}`,
			noVideoCall: true,
			wantLive:    false,
		},
		{
			// broadcastStatus=active is not documented as owner-scoped: "owned
			// by the authenticated user" appears once on that page, in the mine
			// row, and mine cannot be combined with broadcastStatus. Reporting
			// a stranger's audience as yours is the failure this prevents.
			name: "a broadcast belonging to another channel is ignored",
			broadcasts: `{"items":[{"id":"someoneelse","snippet":{"channelId":"UCsomebodyelse",` +
				`"title":"not yours"}}]}`,
			noVideoCall: true,
			wantLive:    false,
		},
		{
			// The half-failure that matters: liveness was already established
			// by the first call, so losing the second costs the number and not
			// the answer. Erroring here would throw away a correct liveness
			// read to report the failure of an advisory one.
			name:       "a refused videos.list costs the count, not the liveness",
			broadcasts: liveBroadcast,
			vStatus:    http.StatusForbidden,
			videos:     `{"error":{"code":403,"errors":[{"reason":"quotaExceeded"}]}}`,
			wantLive:   true,
			wantTitle:  "Friday night set",
		},
		{
			name:        "a refused liveBroadcasts.list is a real error",
			broadcasts:  `{"error":{"code":403,"errors":[{"reason":"insufficientLivePermissions"}]}}`,
			bStatus:     http.StatusForbidden,
			noVideoCall: true,
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var videos http.HandlerFunc
			if !tc.noVideoCall {
				videos = func(w http.ResponseWriter, r *http.Request) {
					if got := r.URL.Query().Get("part"); got != "liveStreamingDetails" {
						t.Errorf("part = %q, want liveStreamingDetails", got)
					}
					// The whole join: the broadcast id IS the video id.
					if got := r.URL.Query().Get("id"); got != "vid123" {
						t.Errorf("id = %q, want the broadcast id used as the video id", got)
					}
					if tc.vStatus != 0 {
						w.WriteHeader(tc.vStatus)
					}
					_, _ = w.Write([]byte(tc.videos))
				}
			}
			y := youtubeStatsStub(t, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if got := q.Get("broadcastStatus"); got != "active" {
					t.Errorf("broadcastStatus = %q, want active", got)
				}
				// The default is "event", which returns only scheduled event
				// broadcasts and would report a persistent live channel as dark.
				if got := q.Get("broadcastType"); got != "all" {
					t.Errorf("broadcastType = %q, want all -- the default is event", got)
				}
				// Mutually exclusive with broadcastStatus: "Filters (specify
				// exactly one of the following parameters)".
				if q.Get("mine") != "" {
					t.Error("mine was sent alongside broadcastStatus, which the filter group forbids")
				}
				if tc.bStatus != 0 {
					w.WriteHeader(tc.bStatus)
				}
				_, _ = w.Write([]byte(tc.broadcasts))
			}, videos)

			got, err := y.Stats(context.Background(), "cid", "tok")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Stats: %v", err)
			}
			if got.Live != tc.wantLive {
				t.Errorf("Live = %v, want %v", got.Live, tc.wantLive)
			}
			switch {
			case tc.wantViewers == nil && got.ViewerCount != nil:
				t.Errorf("ViewerCount = %d, want nil -- no count reported at all", *got.ViewerCount)
			case tc.wantViewers != nil && got.ViewerCount == nil:
				t.Errorf("ViewerCount was not reported, want %d", *tc.wantViewers)
			case tc.wantViewers != nil && *got.ViewerCount != *tc.wantViewers:
				t.Errorf("ViewerCount = %d, want %d", *got.ViewerCount, *tc.wantViewers)
			}
			if got.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tc.wantTitle)
			}
		})
	}
}
