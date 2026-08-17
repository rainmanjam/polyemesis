package oauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A STREAM PER DESTINATION, AND THE TESTS THAT SAY WHOSE KEY MUST NOT MOVE.
//
// YouTube counts concurrent broadcasts per stream key as well as per channel,
// and the per-key ceiling is the smaller one -- so every destination handed the
// same key counts as one ingestion source and the install is capped there. The
// fix is a liveStream per destination beyond the first, driven by
// IngestOptions.DedicatedIngest.
//
// EVERY ASSERTION BELOW IS ABOUT WHICH STREAM CAME BACK, AND HALF OF THEM ARE
// ABOUT A CREATE THAT MUST NOT HAPPEN. A stream key that changes under a
// destination somebody is already publishing with is the one outcome this
// change is not allowed to have: it is pasted into an encoder, and rotating it
// breaks a working configuration for a feature the operator did not ask for.
// A test that only read the returned key would pass against a provider that
// minted a new stream on every refresh, because a fresh key is a perfectly
// plausible-looking key -- so these read the REQUESTS too, and the absence of a
// POST to /liveStreams is the assertion that carries the hazard.

// Two streams on one channel: the reusable one every destination shares today,
// and a second one this feature would have created for a later destination.
// Shaped like the documented liveStreams.list response rather than like the
// struct that decodes it, for the reason youtube_stats_test.go gives.
const ytStubTwoStreams = `{"items":[
	{"id":"stream-77","snippet":{"title":"polyemesis"},
		"cdn":{"ingestionType":"rtmp","ingestionInfo":{
			"streamName":"key-abc","ingestionAddress":"rtmp://a.example/live2",
			"backupIngestionAddress":"rtmp://b.example/live2"}}},
	{"id":"stream-88","snippet":{"title":"polyemesis - Second show"},
		"cdn":{"ingestionType":"rtmp","ingestionInfo":{
			"streamName":"key-second","ingestionAddress":"rtmp://c.example/live2",
			"backupIngestionAddress":"rtmp://d.example/live2"}}}
]}`

// ytStubCreatedStream is what liveStreams.insert answers with: a THIRD stream,
// whose key matches neither of the two already on the channel. That is what
// lets an assertion tell "it created one" apart from "it reused one" without
// reading the request log.
const ytStubCreatedStream = `{"id":"stream-99","snippet":{"title":"polyemesis - Third show"},
	"cdn":{"ingestionType":"rtmp","ingestionInfo":{
		"streamName":"key-new","ingestionAddress":"rtmp://e.example/live2"}}}`

// ytIngestStub is ytScheduleStub with a channel that already has two streams
// and a create that answers with a third.
func ytIngestStub(t *testing.T, log *[]capture) *YouTube {
	t.Helper()
	return ytScheduleStub(t, log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, ytStubTwoStreams)
		},
		"POST " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, ytStubCreatedStream)
		},
	})
}

// HAZARD 1, THE WHOLE OF IT: a destination that already holds a key keeps that
// key, whichever stream it is and whatever the caller decided about dedication.
//
// The two rows are the two installs this change could break. The first is the
// destination holding the channel's shared stream -- the one an operator's
// Studio-scheduled events are bound to -- and it must keep holding it even
// after its neighbours have moved off onto their own streams. The second is a
// destination that already has its own dedicated stream, refreshed again: the
// obvious wrong implementation ("dedicated means create") mints a second stream
// for it every time the button is pressed, and the key in its encoder dies.
//
// MUTATION, internal/oauth/youtube.go, in streamFor: disable the opts.HeldKey
// block (`if false && opts.HeldKey != ""`). Observed: FAIL on the second and
// third rows -- key = "key-new" for both, with a POST to /liveStreams that the
// destination never asked for.
//
// The FIRST row survives that mutation, and saying so is more useful than
// hiding it: with no held-key match it falls through to the shared-stream
// branch, which returns the same stream by another route. That row is here
// because it pins the shared-stream holder's key against a future change to
// EITHER branch, and its own hazard -- being promoted to dedicated when a
// neighbour is deleted -- is the row below it.
func TestYouTubeKeepsTheStreamADestinationAlreadyHolds(t *testing.T) {
	tests := []struct {
		name      string
		opts      IngestOptions
		wantKey   string
		wantURL   string
		wantAbout string
	}{{
		name:      "the destination holding the channel's shared stream keeps it",
		opts:      IngestOptions{HeldKey: "key-abc"},
		wantKey:   "key-abc",
		wantURL:   "rtmp://a.example/live2",
		wantAbout: "the reusable stream this destination has been publishing with",
	}, {
		name: "and keeps it even once the caller has decided it should be dedicated",
		// The case a later destination's arrival creates: the first destination
		// is still first, but a caller that changed its mind -- or a row whose
		// id ordering shifted -- must not be able to move an established key.
		opts:      IngestOptions{HeldKey: "key-abc", DedicatedIngest: true, IngestLabel: "First show"},
		wantKey:   "key-abc",
		wantURL:   "rtmp://a.example/live2",
		wantAbout: "the key already in this destination's encoder",
	}, {
		name:      "a destination with its own stream is not given a second one",
		opts:      IngestOptions{HeldKey: "key-second", DedicatedIngest: true, IngestLabel: "Second show"},
		wantKey:   "key-second",
		wantURL:   "rtmp://c.example/live2",
		wantAbout: "the dedicated stream this destination already owns",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var log []capture
			y := ytIngestStub(t, &log)

			b, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel", tc.opts)
			if err != nil {
				t.Fatalf("IngestFor: %v", err)
			}
			if b.Ingest.Key != tc.wantKey {
				t.Errorf("key = %q, want %q -- %s", b.Ingest.Key, tc.wantKey, tc.wantAbout)
			}
			if b.Ingest.URL != tc.wantURL {
				t.Errorf("url = %q, want %q", b.Ingest.URL, tc.wantURL)
			}
			// The assertion that carries the hazard. A create here is a rotation
			// even when the returned key happens to look right, because the
			// stream it made is a real stream on the operator's channel.
			if c := find(log, http.MethodPost, ytStreamsPath); c != nil {
				t.Errorf("a refresh for a destination that already holds a key created "+
					"another stream (%+v); the key in its encoder would stop working", c)
			}
		})
	}
}

// The first destination on an account keeps TODAY'S BEHAVIOUR EXACTLY: the
// channel's existing reusable stream, and no new stream on the channel.
//
// This is the case the whole design is arranged around. An operator with one
// YouTube destination did not ask for any of this, and handing them a freshly
// minted key their Studio-scheduled events are not bound to would break a setup
// that works today.
//
// MUTATION, internal/oauth/youtube.go, in streamFor: drop the
// `if !opts.DedicatedIngest` guard so every caller creates. Observed: FAIL --
// "key = key-new, want key-abc".
func TestYouTubeFirstDestinationStillGetsTheChannelsExistingStream(t *testing.T) {
	var log []capture
	y := ytIngestStub(t, &log)

	b, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel", IngestOptions{
		IngestLabel: "First show",
	})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	if b.Ingest.Key != "key-abc" || b.Ingest.URL != "rtmp://a.example/live2" {
		t.Errorf("Ingest = %+v, want the channel's existing reusable stream", b.Ingest)
	}
	if c := find(log, http.MethodPost, ytStreamsPath); c != nil {
		t.Errorf("a first destination provisioned a new stream (%+v); its key would not "+
			"be the one the operator's scheduled events are bound to", c)
	}
}

// The fix itself: a destination the caller nominated as NOT first, holding no
// key yet, gets a stream of its own -- which is what makes it a separate
// ingestion source rather than a co-tenant of the shared one.
//
// MUTATION, internal/oauth/youtube.go, in streamFor: change
// `if !opts.DedicatedIngest` to `if true`. Observed: FAIL -- "key = key-abc",
// the shared stream, which is the defect this change exists to remove.
func TestYouTubeGivesALaterDestinationItsOwnStream(t *testing.T) {
	var log []capture
	y := ytIngestStub(t, &log)

	b, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel", IngestOptions{
		DedicatedIngest: true,
		IngestLabel:     "Third show",
	})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	if b.Ingest.Key != "key-new" || b.Ingest.URL != "rtmp://e.example/live2" {
		t.Errorf("Ingest = %+v, want the newly created stream's own address and key", b.Ingest)
	}
	for _, shared := range []string{"key-abc", "key-second"} {
		if b.Ingest.Key == shared {
			t.Fatalf("this destination was handed %q, a key another destination is "+
				"already publishing with", shared)
		}
	}

	create := find(log, http.MethodPost, ytStreamsPath)
	if create == nil {
		t.Fatalf("no stream was created; calls were %+v", log)
	}
	snip, _ := create.Body["snippet"].(map[string]any)
	if snip == nil {
		t.Fatalf("the create sent no snippet: %+v", create.Body)
	}
	// HAZARD 4: these appear in YouTube Studio, and five of them called
	// "polyemesis" is a channel whose operator cannot tell which stream feeds
	// which show.
	title, _ := snip["title"].(string)
	if !strings.Contains(title, "Third show") {
		t.Errorf("title = %q, want the destination's own name in it -- an operator "+
			"reading YouTube Studio has nothing else to go on", title)
	}
	// cdn.resolution and cdn.frameRate must be set together or omitted
	// together, and variable requires variable: the paired refusals
	// resolutionRequired/frameRateRequired are quoted in the evidence file.
	cdn, _ := create.Body["cdn"].(map[string]any)
	if cdn == nil || cdn["ingestionType"] != "rtmp" ||
		cdn["resolution"] != "variable" || cdn["frameRate"] != "variable" {
		t.Errorf("cdn = %+v, want the same variable/variable RTMP stream the shared "+
			"one is; a dedicated stream that refused what OBS sends is not a fix", cdn)
	}
}

// A dedicated stream is only worth having if the BROADCAST is bound to it. The
// bind is a separate call taking a streamId, so a create that provisioned a new
// stream and then bound the show to the shared one would leave every
// destination counting against the same ingestion source again -- and every
// call would succeed.
//
// MUTATION, internal/oauth/youtube_schedule.go, in IngestFor: revert
// `y.streamFor(ctx, accessToken, opts)` to `y.reusableStream(ctx, accessToken)`.
// Observed: FAIL -- "bind streamId = stream-77", the shared stream.
func TestYouTubeBindsAScheduledBroadcastToTheDestinationsOwnStream(t *testing.T) {
	var log []capture
	y := ytIngestStub(t, &log)

	b, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel", IngestOptions{
		DedicatedIngest: true,
		IngestLabel:     "Third show",
		ScheduledFor:    time.Date(2030, 1, 1, 18, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	bind := find(log, http.MethodPost, ytBindPath)
	q := mustQuery(t, bind)
	if q.Get("streamId") != "stream-99" {
		t.Errorf("bind streamId = %q, want stream-99 -- the stream this destination "+
			"was just given, not the one its neighbours publish to", q.Get("streamId"))
	}
	if b.Ingest.Key != "key-new" {
		t.Errorf("Ingest.Key = %q, want the created stream's key; the encoder and the "+
			"broadcast must be pointed at the same stream", b.Ingest.Key)
	}
}

// A held key the channel no longer lists: the stream was deleted in YouTube
// Studio, or the account was reconnected to a different channel. The key in the
// encoder is already dead either way, so re-provisioning is the only outcome
// available -- and the one thing that must NOT happen is the destination
// falling back onto a stream one of its neighbours is publishing with, which
// would be the concurrency defect re-created by an error path.
func TestYouTubeReprovisionsWhenTheHeldStreamIsGone(t *testing.T) {
	var log []capture
	y := ytIngestStub(t, &log)

	b, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel", IngestOptions{
		HeldKey:         "key-deleted-in-studio",
		DedicatedIngest: true,
		IngestLabel:     "Third show",
	})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	if b.Ingest.Key != "key-new" {
		t.Errorf("key = %q, want a newly provisioned one -- the held stream is gone and "+
			"the alternative is publishing to somebody else's", b.Ingest.Key)
	}
}

// Provider.Ingest has nowhere to carry an option, so it must keep doing what it
// always did. It is what a platform with no target capability goes through, and
// internal/api's fallback builds a Broadcast around it.
//
// MUTATION, internal/oauth/youtube.go: change reusableStream to pass
// `IngestOptions{DedicatedIngest: true}`. Observed: FAIL -- Ingest created a
// stream and returned key-new.
func TestYouTubeProviderIngestStillReadsTheSharedStream(t *testing.T) {
	var log []capture
	y := ytIngestStub(t, &log)

	ing, err := y.Ingest(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if ing.Key != "key-abc" {
		t.Errorf("Ingest.Key = %q, want the channel's reusable stream", ing.Key)
	}
	if c := find(log, http.MethodPost, ytStreamsPath); c != nil {
		t.Errorf("Provider.Ingest created a stream: %+v", c)
	}
}

// The title an operator reads in YouTube Studio.
//
// The 128 is DOCUMENTED -- "Title 1-128 chars" in the liveStreams create
// caveats of docs/evidence/platform-lifecycle-apis-2026-08-16.md -- which is
// what separates it from the concurrency numbers in the same file, which are a
// support transcript and are enforced nowhere.
func TestYouTubeStreamTitleNamesTheDestinationAndFitsTheDocumentedLimit(t *testing.T) {
	if got := ytStreamTitle(""); got != "polyemesis" {
		t.Errorf("ytStreamTitle(\"\") = %q, want the bare base -- a stream created "+
			"without a destination behind it has no name to borrow", got)
	}
	if got := ytStreamTitle("  Second show  "); got != "polyemesis - Second show" {
		t.Errorf("ytStreamTitle = %q, want the base and the destination's trimmed name", got)
	}

	// Over-long, and in a script where one character is three bytes: a cut by
	// byte would send a title ending in half a character.
	long := strings.Repeat("夜", 200)
	got := ytStreamTitle(long)
	if n := len([]rune(got)); n != ytStreamTitleMax {
		t.Errorf("length = %d runes, want %d -- YouTube documents a 128-character "+
			"ceiling on snippet.title", n, ytStreamTitleMax)
	}
	if !strings.HasPrefix(got, "polyemesis - ") {
		t.Errorf("title = %q, want the label cut rather than the base", got)
	}
	if !strings.ContainsRune(got, '夜') || strings.ContainsRune(got, '�') {
		t.Errorf("title = %q, want whole characters -- the cut is by rune", got)
	}
}
