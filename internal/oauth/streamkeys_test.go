package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// TestYouTubeStreamKeysListsTheChannelsKeysAndCreatesNothing is the hazard this
// capability exists around. The key cross-check runs because a destination was
// REFUSED, which is exactly when a careless "find or create" would mint a new
// liveStream -- rotating the key under somebody's encoder to answer a question.
// The only request allowed is the list.
func TestYouTubeStreamKeysListsTheChannelsKeysAndCreatesNothing(t *testing.T) {
	var log []capture
	y := ytIngestStub(t, &log)
	keys, err := y.StreamKeys(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys, []string{"key-abc", "key-second"}) {
		t.Fatalf("keys = %v", keys)
	}
	for _, c := range log {
		if c.Method != http.MethodGet {
			t.Fatalf("the key read sent %s %s; it must only ever list", c.Method, c.Path)
		}
	}
}

// TestTwitchStreamKeysIsTheChannelsOneKey: Twitch has exactly one key per
// channel, read with Get Stream Key, which changes nothing.
func TestTwitchStreamKeysIsTheChannelsOneKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/users":
			_, _ = io.WriteString(w, `{"data":[{"id":"4242","login":"dj","display_name":"DJ"}]}`)
		case "/streams/key":
			_, _ = io.WriteString(w, `{"data":[{"stream_key":"live_4242_current"}]}`)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	keys, err := NewTwitch(WithBaseURL(srv.URL)).StreamKeys(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys, []string{"live_4242_current"}) {
		t.Fatalf("keys = %v", keys)
	}
}

// TestOnlyPlatformsWithAReadOnlyKeyEndpointOfferStreamKeys: Facebook's key IS
// a live video, so reading one means creating one; Kick and the rest publish no
// key endpoint at all. Offering the capability for any of them would mean the
// cross-check either created broadcasts or lied.
func TestOnlyPlatformsWithAReadOnlyKeyEndpointOfferStreamKeys(t *testing.T) {
	want := map[db.Platform]bool{db.PlatformYouTube: true, db.PlatformTwitch: true}
	for p := range NewSet().All() {
		_, ok := NewSet().StreamKeysFor(p)
		if ok != want[p] {
			t.Errorf("%s: StreamKeysFor ok = %v, want %v", p, ok, want[p])
		}
		_, pkgOK := StreamKeysFor(p)
		if pkgOK != ok {
			t.Errorf("%s: package StreamKeysFor = %v, Set twin = %v", p, pkgOK, ok)
		}
	}
}

// TestYouTubeStreamKeysReadsEveryPage: liveStreams.list answers 50 at a time,
// and a channel that has created a stream per destination for a year can have
// more. A key on page two read as "not listed" would put "YouTube's key
// changed" on a destination whose key is fine -- sending the operator to
// rotate a working key, the exact harm a read-only check exists to avoid.
func TestYouTubeStreamKeysReadsEveryPage(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("pageToken") == "p2" {
				_, _ = io.WriteString(w, `{"items":[{"id":"s2","cdn":{"ingestionInfo":{"streamName":"key-on-page-two"}}}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"nextPageToken":"p2","items":[{"id":"s1","cdn":{"ingestionInfo":{"streamName":"key-on-page-one"}}}]}`)
		},
	})
	keys, err := y.StreamKeys(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys, []string{"key-on-page-one", "key-on-page-two"}) {
		t.Fatalf("keys = %v, want both pages", keys)
	}
}
