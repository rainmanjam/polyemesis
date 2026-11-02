package oauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The audience sampler's cheap YouTube reads (PR #950 review, item 3): one
// liveBroadcasts.list to find the live video, then one videos.list per tick
// against the cached id. Never channels.list: the caller already holds the
// channel id.
func youtubeViewersStub(t *testing.T, routes map[string]string) (*YouTube, func(string) int) {
	t.Helper()
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		body, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewYouTube(WithBaseURL(srv.URL)), func(p string) int { mu.Lock(); defer mu.Unlock(); return hits[p] }
}

func TestYouTubeActiveBroadcastIsOneCallAndKeepsToItsChannel(t *testing.T) {
	y, hits := youtubeViewersStub(t, map[string]string{
		ytBroadcastsPath: `{"items":[{"id":"someone-else","snippet":{"channelId":"UCother","title":"Not ours"}},` +
			`{"id":"vid1","snippet":{"channelId":"UCours","title":"Friday set"}}]}`,
	})
	id, title, err := y.ActiveBroadcast(context.Background(), "tok", "UCours")
	if err != nil || id != "vid1" || title != "Friday set" {
		t.Fatalf("ActiveBroadcast = %q %q %v, want our own broadcast", id, title, err)
	}
	if hits("/channels") != 0 || hits(ytBroadcastsPath) != 1 {
		t.Fatalf("calls: channels=%d broadcasts=%d, want only one liveBroadcasts.list", hits("/channels"), hits(ytBroadcastsPath))
	}
	none, _ := youtubeViewersStub(t, map[string]string{ytBroadcastsPath: `{"items":[]}`})
	if id, _, err := none.ActiveBroadcast(context.Background(), "tok", "UCours"); id != "" || err != nil {
		t.Fatalf("no live broadcast = %q %v, want empty and no error", id, err)
	}
}

func TestYouTubeLiveViewersReadsOneVideo(t *testing.T) {
	cases := []struct {
		name, body          string
		found, ended, count bool
		viewers             int
	}{
		{"live with a count", `{"items":[{"liveStreamingDetails":{"concurrentViewers":"42"}}]}`, true, false, true, 42},
		{"live with the count hidden", `{"items":[{"liveStreamingDetails":{}}]}`, true, false, false, 0},
		{"ended", `{"items":[{"liveStreamingDetails":{"actualEndTime":"2026-11-01T20:00:00Z"}}]}`, true, true, false, 0},
		{"gone", `{"items":[]}`, false, false, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			y, hits := youtubeViewersStub(t, map[string]string{ytVideosPath: c.body})
			v, err := y.LiveViewers(context.Background(), "tok", "vid1")
			if err != nil {
				t.Fatal(err)
			}
			if v.Found != c.found || v.Ended != c.ended || (v.Viewers != nil) != c.count {
				t.Fatalf("got %+v", v)
			}
			if c.count && *v.Viewers != c.viewers {
				t.Fatalf("viewers = %d", *v.Viewers)
			}
			if hits(ytVideosPath) != 1 || hits("/channels") != 0 {
				t.Fatal("more than one videos.list, or a channels.list")
			}
		})
	}
}

// TestAPlatformRefusalCarriesItsKind (item 5): a quota refusal and a rate
// limit are typed, so the sampler says "quota" and backs off instead of
// calling either a hidden count.
func TestAPlatformRefusalCarriesItsKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/quota":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"quotaExceeded"}],"code":403}}`))
		case "/limited":
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	t.Cleanup(srv.Close)
	var out any
	err := getJSON(context.Background(), srv.URL+"/quota", "tok", nil, &out)
	var se *StatusError
	if !errors.As(err, &se) || !se.QuotaExceeded || se.Status != http.StatusForbidden {
		t.Fatalf("quota refusal = %#v", err)
	}
	if !strings.HasPrefix(err.Error(), "the platform refused the request (403)") {
		t.Fatalf("the message changed: %q", err.Error())
	}
	err = getJSON(context.Background(), srv.URL+"/limited", "tok", nil, &out)
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests || se.RetryAfter != 30*time.Second || se.QuotaExceeded {
		t.Fatalf("rate limit = %#v", err)
	}
}

// TestTheMeterChargesYouTubeDataAPICallsOnly (item 2): every YouTube Data
// API request this package sends is charged to the shared allowance -- a list
// one unit, a write fifty -- unless the caller already charged it.
func TestTheMeterChargesYouTubeDataAPICallsOnly(t *testing.T) {
	prev := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	})}
	t.Cleanup(func() { httpClient = prev })
	var mu sync.Mutex
	spent := 0
	SetYouTubeMeter(func(cost int) { mu.Lock(); spent += cost; mu.Unlock() })
	t.Cleanup(func() { SetYouTubeMeter(nil) })

	ctx := context.Background()
	var out any
	_ = getJSON(ctx, "https://www.googleapis.com/youtube/v3/videos?part=snippet", "tok", nil, &out)
	_ = postJSON(ctx, "https://www.googleapis.com/youtube/v3/liveBroadcasts/transition", "tok", map[string]any{}, nil, nil)
	_ = getJSON(ctx, "https://api.twitch.tv/helix/streams", "tok", nil, &out)
	_ = getJSON(WithQuotaCharged(ctx), "https://www.googleapis.com/youtube/v3/videos", "tok", nil, &out)
	mu.Lock()
	defer mu.Unlock()
	if spent != 51 {
		t.Fatalf("metered %d units, want 51 (a list and a write; not Twitch, not a pre-charged read)", spent)
	}
}

// TestTheMeterChargesTheUploadHostToo (R4): thumbnails.set lives under
// /upload/youtube/v3/ and is a 50-unit write.
func TestTheMeterChargesTheUploadHostToo(t *testing.T) {
	cases := []struct {
		method, url string
		cost        int
		ok          bool
	}{
		{http.MethodPost, "https://www.googleapis.com/upload/youtube/v3/thumbnails/set?videoId=v", 50, true},
		{http.MethodPost, "https://www.googleapis.com/upload/youtube/v3/videos?part=snippet", 1600, true},
		{http.MethodGet, "https://www.googleapis.com/youtube/v3/videos", 1, true},
		{http.MethodGet, "https://www.googleapis.com/drive/v3/files", 0, false},
	}
	for _, c := range cases {
		r, _ := http.NewRequest(c.method, c.url, nil)
		if cost, ok := youTubeCost(r); cost != c.cost || ok != c.ok {
			t.Errorf("%s %s = %d %v, want %d %v", c.method, c.url, cost, ok, c.cost, c.ok)
		}
	}
}
