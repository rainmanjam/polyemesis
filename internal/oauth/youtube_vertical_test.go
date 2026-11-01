package oauth

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// THE VERTICAL HALF IS A STREAM THE HORIZONTAL BROADCAST MUST NEVER BIND TO, AND THE
// FIRST-RTMP FALLBACK MUST NEVER ADOPT IT (#7, YouTube paired vertical).
//
// The #7 spike found that the API cannot attach a vertical companion to a broadcast.
// Two things follow. The vertical stream is created on its own, outside streamFor, so
// it never comes back as "the shared stream". And streamFor's fallback, which takes the
// first RTMP stream on the channel, would otherwise adopt the vertical stream and bind
// the horizontal broadcast to it. Every test below guards one of those two outcomes.
//
// Two mechanisms keep the vertical stream out of the fallback. The title marker covers
// the zero-options path, which has nowhere to carry an exclusion list. ExcludeStreamIDs
// covers callers that do pass options, and it survives a rename in Studio, which the
// title marker does not.

// ytVerticalStub is a channel holding the horizontal stream (stream-77) and a vertical
// stream (stream-vert) in that order. The vertical one is listed FIRST on purpose: the
// fallback picks the first RTMP stream, so an implementation that forgets the guard
// adopts it, and the bind assertion catches the mistake.
const ytVerticalStub = `{"items":[
	{"id":"stream-vert","snippet":{"title":"polyemesis - Show (vertical)"},
		"cdn":{"ingestionType":"rtmp","ingestionInfo":{
			"streamName":"key-vert","ingestionAddress":"rtmp://v.example/live2"}}},
	{"id":"stream-77","snippet":{"title":"polyemesis - Show"},
		"cdn":{"ingestionType":"rtmp","ingestionInfo":{
			"streamName":"key-abc","ingestionAddress":"rtmp://a.example/live2"}}}
]}`

// ytVerticalOnlyStub has nothing but the vertical stream. Any fallback that adopts it
// would bind the horizontal broadcast to the vertical ingest, so the test expects a
// create instead.
const ytVerticalOnlyStub = `{"items":[
	{"id":"stream-vert","snippet":{"title":"polyemesis - Show (vertical)"},
		"cdn":{"ingestionType":"rtmp","ingestionInfo":{
			"streamName":"key-vert","ingestionAddress":"rtmp://v.example/live2"}}}
]}`

// A stream renamed in Studio drops the marker. Only ExcludeStreamIDs can still see it.
const ytRenamedVerticalStub = `{"items":[
	{"id":"stream-vert","snippet":{"title":"my vertical feed"},
		"cdn":{"ingestionType":"rtmp","ingestionInfo":{
			"streamName":"key-vert","ingestionAddress":"rtmp://v.example/live2"}}},
	{"id":"stream-77","snippet":{"title":"polyemesis - Show"},
		"cdn":{"ingestionType":"rtmp","ingestionInfo":{
			"streamName":"key-abc","ingestionAddress":"rtmp://a.example/live2"}}}
]}`

// ytVerticalCreated is what liveStreams.insert answers for a vertical create.
const ytVerticalCreated = `{"id":"stream-new-vert","snippet":{"title":"polyemesis - Show (vertical)"},
	"cdn":{"ingestionType":"rtmp","ingestionInfo":{
		"streamName":"key-new-vert","ingestionAddress":"rtmp://v2.example/live2"}}}`

// TestTheFirstRTMPFallbackNeverAdoptsAVerticalStream pins the zero-options path, which is
// what reusableStream and Ingest use. The vertical stream is listed first, so only the
// title marker keeps it out.
func TestTheFirstRTMPFallbackNeverAdoptsAVerticalStream(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, ytVerticalStub) },
	})
	s, err := y.reusableStream(context.Background(), "tok")
	if err != nil {
		t.Fatalf("reusableStream: %v", err)
	}
	if s.ID != "stream-77" {
		t.Fatalf("fallback returned %q; a vertical stream was adopted, and the horizontal broadcast would bind to it", s.ID)
	}
}

// TestTheFallbackCreatesAStreamRatherThanAdoptingTheOnlyVerticalOne pins the case where
// the vertical stream is the only RTMP stream on the channel. The fallback must create a
// new stream, not hand the vertical one back.
func TestTheFallbackCreatesAStreamRatherThanAdoptingTheOnlyVerticalOne(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, ytVerticalOnlyStub) },
		"POST " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, ytVerticalCreated)
		},
	})
	s, err := y.reusableStream(context.Background(), "tok")
	if err != nil {
		t.Fatalf("reusableStream: %v", err)
	}
	if s.ID == "stream-vert" {
		t.Fatal("the only RTMP stream on the channel is the vertical one, and it was handed back as the horizontal ingest")
	}
	if findCapture(log, "POST", ytStreamsPath) == nil {
		t.Fatal("no stream was created; the fallback should have provisioned a new one rather than adopt the vertical stream")
	}
}

// TestAStreamRenamedAwayFromTheMarkerIsStillExcludedWhenItsIDIsKnown pins the second
// mechanism. A renamed vertical stream has no marker, and ExcludeStreamIDs keeps it out.
func TestAStreamRenamedAwayFromTheMarkerIsStillExcludedWhenItsIDIsKnown(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, ytRenamedVerticalStub) },
	})
	s, err := y.streamFor(context.Background(), "tok", IngestOptions{ExcludeStreamIDs: []string{"stream-vert"}})
	if err != nil {
		t.Fatalf("streamFor: %v", err)
	}
	if s.ID != "stream-77" {
		t.Fatalf("streamFor returned %q; the renamed vertical stream was not excluded by id", s.ID)
	}
}

// TestTheHorizontalBroadcastBindsOnlyToTheHorizontalStream is the bind guard. The
// scheduled path takes its stream from streamFor and binds the broadcast to it. With the
// vertical stream listed first, the bind streamId must still be the horizontal one.
func TestTheHorizontalBroadcastBindsOnlyToTheHorizontalStream(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, ytVerticalStub) },
	})
	_, err := y.IngestFor(context.Background(), "client", "tok", "", IngestOptions{
		ScheduledFor: time.Date(2030, 1, 1, 18, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	bind := findCapture(log, "POST", ytBindPath)
	if bind == nil {
		t.Fatal("the scheduled broadcast was never bound to a stream")
	}
	if got := mustQuery(t, bind).Get("streamId"); got == "stream-vert" {
		t.Fatal("liveBroadcasts.bind was called with the VERTICAL stream id; the horizontal broadcast must never bind to it")
	}
}

// TestCreateVerticalStreamMakesItsOwnStreamAndNeverReusesTheShared pins the creation path.
// It must POST a new liveStream titled with the marker, and it must not list or adopt the
// channel's existing streams first. Listing would be how it ends up with the shared key.
func TestCreateVerticalStreamMakesItsOwnStreamAndNeverReusesTheShared(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"POST " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, ytVerticalCreated) },
	})
	v, err := y.CreateVerticalStream(context.Background(), "tok", "Show")
	if err != nil {
		t.Fatalf("CreateVerticalStream: %v", err)
	}
	if v.ID != "stream-new-vert" {
		t.Fatalf("vertical stream id = %q, want the newly created one", v.ID)
	}
	if findCapture(log, "GET", ytStreamsPath) != nil {
		t.Fatal("CreateVerticalStream listed the channel's streams; it must create its own, not reuse one")
	}
	post := findCapture(log, "POST", ytStreamsPath)
	if post == nil {
		t.Fatal("no liveStreams.insert was sent")
	}
	snip, _ := post.Body["snippet"].(map[string]any)
	title, _ := snip["title"].(string)
	if !strings.HasSuffix(title, ytVerticalMarker) {
		t.Fatalf("created title %q does not carry the vertical marker %q", title, ytVerticalMarker)
	}
}

// TestTheVerticalMarkerSurvivesTheHundredTwentyEightRuneCut pins the naming rule. The
// label is what gets cut, never the marker. A cut that dropped the marker would make the
// stream look like a horizontal one to the fallback.
func TestTheVerticalMarkerSurvivesTheHundredTwentyEightRuneCut(t *testing.T) {
	title := ytVerticalStreamTitle(strings.Repeat("x", 300))
	if got := len([]rune(title)); got > ytStreamTitleMax {
		t.Fatalf("title is %d runes, over the %d limit", got, ytStreamTitleMax)
	}
	if !strings.HasSuffix(title, ytVerticalMarker) {
		t.Fatalf("a long label cut the marker off: %q", title)
	}
	if !isVerticalStreamTitle(title) {
		t.Fatalf("isVerticalStreamTitle does not recognise the title it produced: %q", title)
	}
}

// TestTheVerticalHealthReadNeverCarriesAStreamKey pins the status shape. The health read
// is shown in the UI and written to logs, so its type must not hold a key field. The
// vertical key arrives only through CreateVerticalStream's Ingest.
func TestTheVerticalHealthReadNeverCarriesAStreamKey(t *testing.T) {
	typ := reflect.TypeOf(VerticalStreamHealth{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "key") || strings.Contains(name, "streamname") {
			t.Fatalf("VerticalStreamHealth has field %q; a status read must never carry the key", typ.Field(i).Name)
		}
	}
}

// TestVerticalHealthReadsStreamStatusAndHealthByID pins the sub-row source. The health comes
// from liveStreams status, looked up by the vertical stream's id.
func TestVerticalHealthReadsStreamStatusAndHealthByID(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"items":[{"id":"stream-vert","status":{"streamStatus":"error",
				"healthStatus":{"status":"bad"}}}]}`)
		},
	})
	h, err := y.VerticalStreamHealth(context.Background(), "tok", "stream-vert")
	if err != nil {
		t.Fatalf("VerticalStreamHealth: %v", err)
	}
	if h.StreamStatus != "error" || h.HealthStatus != "bad" {
		t.Fatalf("health = %+v, want streamStatus error and healthStatus bad", h)
	}
	get := findCapture(log, "GET", ytStreamsPath)
	if get == nil || mustQuery(t, get).Get("id") != "stream-vert" {
		t.Fatal("the health read did not look the vertical stream up by its id")
	}
}

// TestCreateVerticalStreamRefusesAStreamWithNoID (review #29): a vertical stream with a key and no id cannot be
// read for health, excluded from the fallback by id, or found again. Accepting it would store a pair the sub-row can
// never read. The refusal must not echo the key it just received.
func TestCreateVerticalStreamRefusesAStreamWithNoID(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"POST " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"snippet":{"title":"polyemesis - Show (vertical)"},
				"cdn":{"ingestionType":"rtmp","ingestionInfo":{"streamName":"key-noid-vert","ingestionAddress":"rtmp://v2.example/live2"}}}`)
		},
	})
	v, err := y.CreateVerticalStream(context.Background(), "tok", "Show")
	if err == nil {
		t.Fatalf("CreateVerticalStream accepted a stream with no id: %+v", v)
	}
	if strings.Contains(err.Error(), "key-noid-vert") {
		t.Errorf("the refusal echoed the stream key: %v", err)
	}
}
