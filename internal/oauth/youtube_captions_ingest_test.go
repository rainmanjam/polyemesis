package oauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A CAPTION INGEST URL IS A CREDENTIAL, AND IT IS ONLY DOCUMENTED ON ONE CALL.
//
// liveStreams.insert returns contentDetails.closedCaptionsIngestionUrl when the
// request asks for the contentDetails part. The list and update parts do not
// document that field, so a URL read back from a reused stream is undocumented
// (docs-youtube C2). A caption-enabled YouTube destination therefore gets its
// own stream, created with contentDetails requested, and keeps the URL the insert
// returned. The tests below pin three things: the insert asks for the part, the
// list never does, and a caption destination without a stored URL never adopts a
// stream whose URL it cannot know.

// fixtureCaptionURL is a signed-looking ingest URL. The signature is assembled at
// runtime so no literal secret-shaped value is checked in.
func fixtureCaptionURL() string {
	return "https://upload.youtube.com/closedcaption?cid=fixture&sig=" + "fixture-" + "sig"
}

// ytCaptionCreatedStream is what liveStreams.insert answers with when the request
// asked for contentDetails: the same third stream as ytStubCreatedStream, now
// carrying the caption ingest URL.
func ytCaptionCreatedStream(url string) string {
	return `{"id":"stream-99","snippet":{"title":"polyemesis - Third show"},
	"cdn":{"ingestionType":"rtmp","ingestionInfo":{
		"streamName":"key-new","ingestionAddress":"rtmp://e.example/live2"}},
	"contentDetails":{"closedCaptionsIngestionUrl":"` + url + `"}}`
}

// ytCaptionStub is the two-stream channel plus an insert that returns the caption
// URL, so the create path can be checked end to end without a network.
func ytCaptionStub(t *testing.T, log *[]capture) *YouTube {
	t.Helper()
	return ytScheduleStub(t, log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, ytStubTwoStreams)
		},
		"POST " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, ytCaptionCreatedStream(fixtureCaptionURL()))
		},
	})
}

// streamRequests returns the captured liveStreams requests with the given method.
func streamRequests(log []capture, method string) []capture {
	var out []capture
	for _, c := range log {
		if c.Method == method && c.Path == ytStreamsPath {
			out = append(out, c)
		}
	}
	return out
}

// TestYouTubeCaptionStreamAsksInsertForItsIngestURLAndKeepsIt pins the capture.
// A caption destination with no stored URL gets a new stream, the insert names
// the contentDetails part, and the URL the insert returned comes back on the
// broadcast so it can be stored.
func TestYouTubeCaptionStreamAsksInsertForItsIngestURLAndKeepsIt(t *testing.T) {
	var log []capture
	y := ytCaptionStub(t, &log)

	b, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel",
		IngestOptions{CaptionPOST: true, IngestLabel: "Captioned show"})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	posts := streamRequests(log, "POST")
	if len(posts) != 1 {
		t.Fatalf("liveStreams inserts = %d, want exactly 1 (the destination's own stream)", len(posts))
	}
	if !strings.Contains(posts[0].Query, "contentDetails") {
		t.Fatalf("insert query %q does not request the contentDetails part, so no caption URL comes back", posts[0].Query)
	}
	if b.Ingest.Key != "key-new" {
		t.Fatalf("key = %q, want the newly created stream's key key-new", b.Ingest.Key)
	}
	if b.CaptionIngestURL != fixtureCaptionURL() {
		t.Fatalf("CaptionIngestURL was not carried out of the insert")
	}
}

// TestYouTubeStreamListNeverAsksForContentDetails pins the other half of the
// rule: the list call stays on id, snippet and cdn, because the documented list
// part set does not cover the caption URL. The check runs on the caption path
// (where a list still happens before the create) and on the ordinary path.
func TestYouTubeStreamListNeverAsksForContentDetails(t *testing.T) {
	for _, opts := range []IngestOptions{
		{},
		{CaptionPOST: true},
		{CaptionPOST: true, HeldKey: "key-abc", CaptionIngestURL: fixtureCaptionURL()},
	} {
		var log []capture
		y := ytCaptionStub(t, &log)
		if _, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel", opts); err != nil {
			t.Fatalf("IngestFor(%+v): %v", opts, err)
		}
		for _, c := range streamRequests(log, "GET") {
			if strings.Contains(c.Query, "contentDetails") {
				t.Fatalf("list query %q asks for contentDetails (opts %+v); the caption URL is not documented on list", c.Query, opts)
			}
		}
	}
}

// TestYouTubeCaptionDestinationKeepsItsHeldStreamWhenItStoredTheURL pins reuse:
// once the URL is stored with the held key, the destination keeps its stream and
// does not create a second one. The stored URL is what comes back.
func TestYouTubeCaptionDestinationKeepsItsHeldStreamWhenItStoredTheURL(t *testing.T) {
	var log []capture
	y := ytCaptionStub(t, &log)

	b, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel",
		IngestOptions{CaptionPOST: true, HeldKey: "key-abc", CaptionIngestURL: fixtureCaptionURL()})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	if n := len(streamRequests(log, "POST")); n != 0 {
		t.Fatalf("liveStreams inserts = %d, want 0: the held key and its stored URL should be reused", n)
	}
	if b.Ingest.Key != "key-abc" {
		t.Fatalf("key = %q, want the held key key-abc", b.Ingest.Key)
	}
	if b.CaptionIngestURL != fixtureCaptionURL() {
		t.Fatalf("CaptionIngestURL = %q, want the stored URL carried out", b.CaptionIngestURL)
	}
}

// TestYouTubeCaptionDestinationWithoutStoredURLRotatesRatherThanGuess pins the
// rotation. A destination holding a key but no stored caption URL cannot know the
// URL for the stream it holds, so it gets a new stream and a new key. This is the
// one place a caption destination's key moves, and the operator is told so. A
// shared stream is never adopted on this path either, since its URL is unknown.
func TestYouTubeCaptionDestinationWithoutStoredURLRotatesRatherThanGuess(t *testing.T) {
	cases := []IngestOptions{
		{CaptionPOST: true, HeldKey: "key-abc"},
		{CaptionPOST: true},
	}
	for _, opts := range cases {
		var log []capture
		y := ytCaptionStub(t, &log)
		b, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel", opts)
		if err != nil {
			t.Fatalf("IngestFor(%+v): %v", opts, err)
		}
		if n := len(streamRequests(log, "POST")); n != 1 {
			t.Fatalf("opts %+v: liveStreams inserts = %d, want 1 (rotate to a stream with a known URL)", opts, n)
		}
		if b.Ingest.Key != "key-new" {
			t.Fatalf("opts %+v: key = %q, want the new stream key-new, not an adopted one", opts, b.Ingest.Key)
		}
	}
}
