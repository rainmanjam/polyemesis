package oauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// YouTube's scheduled broadcasts, asserted at the WIRE rather than at the
// return value.
//
// Every trap this feature has produces a request that SUCCEEDS while doing the
// wrong thing, so a test that only checked the returned Broadcast would pass
// against a create that named the wrong parts, a list that sent two mutually
// exclusive filters, or a bind that removed the binding it was supposed to make.
// The stubs below therefore answer plausibly and the assertions are on the
// query string and the body.
//
// Fixtures are shaped like the DOCUMENTED responses -- liveStreams.list returns
// items[] of stream resources with cdn.ingestionInfo, liveBroadcasts.insert
// returns a broadcast resource -- rather than like the structs that decode them.
// youtube_stats_test.go records why: a fake that agrees with the code proves
// only that the code agrees with itself.

const (
	ytStubChannel = `{"items":[{"id":"UCchannel","snippet":{"title":"Night Owl"}}]}`
	ytStubStreams = `{"items":[{"id":"stream-77","snippet":{"title":"polyemesis"},
		"cdn":{"ingestionType":"rtmp","ingestionInfo":{
			"streamName":"key-abc","ingestionAddress":"rtmp://a.example/live2",
			"backupIngestionAddress":"rtmp://b.example/live2"}}}]}`
)

// ytScheduleStub serves the endpoints a schedule touches and records every
// request. handlers may override any path; anything unhandled fails the test
// loudly rather than 404ing quietly, because a call this file did not expect is
// exactly the thing it is looking for.
func ytScheduleStub(t *testing.T, log *[]capture, handlers map[string]http.HandlerFunc) *YouTube {
	t.Helper()
	srv := httptest.NewServer(recordAll(t, log, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if h, ok := handlers[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /channels":
			io.WriteString(w, ytStubChannel)
		case "GET " + ytStreamsPath:
			io.WriteString(w, ytStubStreams)
		case "POST " + ytBroadcastsPath:
			io.WriteString(w, `{"id":"bcast-1","snippet":{"title":"Scheduled stream",
				"scheduledStartTime":"2030-01-01T18:00:00Z"},"status":{"lifeCycleStatus":"created"}}`)
		case "POST " + ytBindPath:
			io.WriteString(w, `{"id":"bcast-1"}`)
		default:
			t.Errorf("unexpected %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return NewYouTube(WithBaseURL(srv.URL))
}

func mustQuery(t *testing.T, c *capture) url.Values {
	t.Helper()
	if c == nil {
		t.Fatal("the call this assertion is about never reached the stub")
	}
	q, err := url.ParseQuery(c.Query)
	if err != nil {
		t.Fatalf("parse query %q: %v", c.Query, err)
	}
	return q
}

// ------------------------------------------------------------- create

// The whole scheduled path, asserted call by call.
//
// MUTATION, internal/oauth/youtube_schedule.go, in createScheduledBroadcast:
// `?part=snippet,status` -> `?part=snippet,status,contentDetails`.
// Observed: FAIL -- part = "snippet,status,contentDetails". That is the
// destructive-by-part trap: a part named without its fields supplied is a part
// reset to defaults, and YouTube answers 200 either way.
func TestYouTubeSchedulesABroadcastAndBindsItToTheChannelsStream(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, nil)

	at := time.Date(2030, 1, 1, 18, 0, 0, 0, time.FixedZone("east", 3*3600))
	b, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel",
		IngestOptions{ScheduledFor: at})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}

	// --- the create
	create := find(log, http.MethodPost, ytBroadcastsPath)
	q := mustQuery(t, create)
	if got := q.Get("part"); got != "snippet,status" {
		t.Errorf("insert part = %q, want exactly the parts the body carries", got)
	}
	snip, _ := create.Body["snippet"].(map[string]any)
	if snip == nil {
		t.Fatalf("the create sent no snippet: %+v", create.Body)
	}
	// UTC, not the caller's zone: 18:00+03:00 is 15:00Z, and a test that passed
	// a UTC time in would not notice a create that forwarded the wall clock.
	if got := snip["scheduledStartTime"]; got != "2030-01-01T15:00:00Z" {
		t.Errorf("scheduledStartTime = %v, want the RFC 3339 UTC instant", got)
	}
	if got, _ := snip["title"].(string); strings.TrimSpace(got) == "" {
		t.Error("the create sent no snippet.title, which liveBroadcasts.insert requires")
	}
	status, _ := create.Body["status"].(map[string]any)
	if status == nil || status["privacyStatus"] != "public" {
		t.Errorf("status = %+v, want privacyStatus public -- a private scheduled "+
			"broadcast announces the show to nobody", create.Body["status"])
	}
	if _, sent := create.Body["contentDetails"]; sent {
		t.Error("the create sent contentDetails; auto-start decides when a show goes " +
			"live and that belongs to the go-live round, not to scheduling")
	}

	// --- the bind
	bind := find(log, http.MethodPost, ytBindPath)
	bq := mustQuery(t, bind)
	if bq.Get("id") != "bcast-1" {
		t.Errorf("bind id = %q, want the broadcast the create returned", bq.Get("id"))
	}
	// The one that silently does the opposite: streamId is documented as
	// optional because OMITTING it removes an existing binding.
	if bq.Get("streamId") != "stream-77" {
		t.Errorf("bind streamId = %q, want the reusable stream's id -- an absent "+
			"streamId is the documented spelling of \"remove the binding\"", bq.Get("streamId"))
	}
	if bind.Body != nil {
		t.Errorf("bind sent a body (%+v); its whole input is in the query string", bind.Body)
	}

	// --- what the caller gets back
	if b.ID != "bcast-1" {
		t.Errorf("Broadcast.ID = %q, want the created broadcast id -- without it nothing "+
			"can move or end this show", b.ID)
	}
	if b.Ingest.Key != "key-abc" || b.Ingest.URL != "rtmp://a.example/live2" {
		t.Errorf("Ingest = %+v, want the reusable stream's own address and key", b.Ingest)
	}
	if b.Target != "UCchannel" {
		t.Errorf("Target = %q, want the channel this was created on", b.Target)
	}
}

// The live-now path must stay EXACTLY what it was before YouTube became a
// TargetedProvider, because internal/api routes every go-live through IngestFor
// the moment the capability exists. A create here would put a second broadcast
// on the channel every time somebody pressed "refresh key", and would have to
// invent a scheduledStartTime for a show starting now to do it.
//
// MUTATION, internal/oauth/youtube_schedule.go, in IngestFor: delete the
// `if opts.ScheduledFor.IsZero() { return b, nil }` early return.
// Observed: FAIL -- liveBroadcasts.insert was called for a live-now go-live.
func TestYouTubeCreatesNoBroadcastForALiveNowGoLive(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, nil)

	b, err := y.IngestFor(context.Background(), "cid", "tok", "", IngestOptions{})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	if c := find(log, http.MethodPost, ytBroadcastsPath); c != nil {
		t.Errorf("a live-now go-live created a broadcast: %+v", c)
	}
	if c := find(log, http.MethodPost, ytBindPath); c != nil {
		t.Errorf("a live-now go-live bound a broadcast: %+v", c)
	}
	if b.ID != "" {
		t.Errorf("Broadcast.ID = %q, want empty -- internal/api reads a non-empty id as "+
			"a broadcast object it should record", b.ID)
	}
	if b.Ingest.Key != "key-abc" {
		t.Errorf("Ingest.Key = %q, want the same key Provider.Ingest returns", b.Ingest.Key)
	}
	// Populating Backups would write Destination.BackupURL on the next key
	// refresh, which is a go-live behaviour change dressed as a scheduling one.
	if len(b.Backups) != 0 {
		t.Errorf("Backups = %+v, want none for now", b.Backups)
	}
}

// Ingest and IngestFor must read the SAME stream. They used to be one function;
// splitting them is what let the scheduled path see the stream id, and two
// lookups that disagreed would leave the encoder publishing to one stream while
// the broadcast was bound to another -- with both calls answering 200.
func TestYouTubeIngestAndIngestForAgreeOnTheStream(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, nil)
	ctx := context.Background()

	ing, err := y.Ingest(ctx, "cid", "tok")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	b, err := y.IngestFor(ctx, "cid", "tok", "", IngestOptions{})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	if *ing != b.Ingest {
		t.Errorf("Ingest returned %+v and IngestFor returned %+v", *ing, b.Ingest)
	}
}

// A bind that fails leaves a PUBLIC event page on the channel that nothing in
// this database names, and the caller's next sweep will make another beside it.
// The id is the only thing that lets an operator find the first one.
func TestYouTubeNamesTheOrphanWhenTheBindFails(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"POST " + ytBindPath: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":{"errors":[{"reason":"liveBroadcastBindingNotAllowed"}]}}`)
		},
	})

	b, err := y.IngestFor(context.Background(), "cid", "tok", "",
		IngestOptions{ScheduledFor: time.Unix(1893456000, 0)})
	if err == nil {
		t.Fatalf("IngestFor returned %+v for a broadcast that was never bound; the "+
			"encoder would publish to a stream the broadcast cannot see", b)
	}
	if b != nil {
		t.Errorf("IngestFor returned a Broadcast alongside the error: %+v", b)
	}
	if !strings.Contains(err.Error(), "bcast-1") {
		t.Errorf("the error does not name the broadcast that was left behind: %v", err)
	}
}

// A create that answers 200 with no id has put something on the channel that
// nothing can address. Returning it as an ordinary Broadcast would read as "no
// broadcast was made".
func TestYouTubeRefusesACreateThatReturnsNoID(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"POST " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"snippet":{"title":"Scheduled stream"}}`)
		},
	})
	if _, err := y.IngestFor(context.Background(), "cid", "tok", "",
		IngestOptions{ScheduledFor: time.Unix(1893456000, 0)}); err == nil {
		t.Fatal("IngestFor accepted a create that returned no broadcast id")
	}
	if c := find(log, http.MethodPost, ytBindPath); c != nil {
		t.Errorf("bind was called with no broadcast id: %+v", c)
	}
}

// The documented refusals, turned into advice that names NO NUMBER. YouTube
// documents that concurrent broadcasts are capped and never documents at what,
// so an advice string carrying a figure would be this repository's
// most-repeated defect shipped in the friendliest possible wrapper.
func TestYouTubeCreateAdviceExplainsTheRefusalWithoutInventingALimit(t *testing.T) {
	tests := []struct {
		name       string
		reason     string
		wantPhrase string
	}{
		{
			name:       "an ineligible channel is told what to enable",
			reason:     "liveStreamingNotEnabled",
			wantPhrase: "not enabled for live streaming",
		},
		{
			name:       "a refused start time does not blame a polyemesis limit",
			reason:     "invalidScheduledStartTime",
			wantPhrase: "publishes none to enforce",
		},
		{
			name:       "the concurrency cap is reported as unpublished",
			reason:     "concurrentBroadcastsExceedLimit",
			wantPhrase: "does not publish what that number is",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var log []capture
			y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
				"POST " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusForbidden)
					io.WriteString(w, `{"error":{"errors":[{"reason":"`+tc.reason+`"}]}}`)
				},
			})
			_, err := y.IngestFor(context.Background(), "cid", "tok", "",
				IngestOptions{ScheduledFor: time.Unix(1893456000, 0)})
			if err == nil {
				t.Fatal("a refused create came back as success")
			}
			if !strings.Contains(err.Error(), tc.wantPhrase) {
				t.Errorf("advice does not contain %q: %v", tc.wantPhrase, err)
			}

			// NO DIGIT IN THE ADVICE ITSELF. Asserted against a bare error
			// rather than against the one above, because that one is wrapped
			// around a real HTTP failure whose text legitimately carries a
			// status code and a stub port. What must carry no number is the
			// sentence polyemesis wrote: every limit named here is one YouTube
			// declines to state, and a figure in this text would be one
			// somebody made up.
			//
			// "RFC 3339" is removed before scanning and is the ONLY allowance.
			// It names a wire format, which is a fact about what polyemesis
			// sends; every other number in this text would be a claim about
			// what YouTube permits. Spelled as a subtraction rather than as a
			// looser pattern so that adding a second allowance is a visible
			// edit to this list.
			bare := ytBroadcastCreateAdvice(errors.New(tc.reason))
			scanned := strings.ReplaceAll(bare.Error(), "RFC 3339", "RFC")
			if strings.ContainsAny(scanned, "0123456789") {
				t.Errorf("the advice contains a digit, and none of these limits is "+
					"published: %v", bare)
			}
		})
	}
}

// ------------------------------------------------------------- horizon

// YouTube publishes NO scheduling bound, and the representation must say that
// rather than pick a number.
//
// MUTATION, internal/oauth/youtube_schedule.go:
// `func (y *YouTube) ScheduleHorizon() time.Duration { return ScheduleHorizonUnbounded }`
// -> `... { return 30 * 24 * time.Hour }`.
// Observed: FAIL -- "horizon = 720h0m0s", and then twice more, once per
// occurrence past the guess: "an occurrence 9600h0m0s ahead was refused before
// the call" and the same at 175200h0m0s. In production that refusal is a SKIP
// rather than an error, so the show would simply never have been announced and
// nothing anywhere would have said why.
func TestYouTubesScheduleHorizonAssertsNoBoundYouTubeNeverPublished(t *testing.T) {
	sb, ok := ScheduledBroadcastsFor(db.PlatformYouTube)
	if !ok {
		t.Fatal("YouTube has no scheduled-broadcast capability")
	}
	if got := sb.ScheduleHorizon(); got != ScheduleHorizonUnbounded {
		t.Errorf("horizon = %v, want the unbounded sentinel -- YouTube documents no "+
			"limit on scheduledStartTime, and a number here is one nobody published", got)
	}

	// The property that matters is what the CALLER does with it, spelled the way
	// preannounce.go and automation.go spell it. A sentinel that failed this
	// would be a horizon of zero wearing a good name.
	now := time.Now()
	for _, ahead := range []time.Duration{
		time.Minute,
		8 * 24 * time.Hour, // past Facebook's documented seven days
		400 * 24 * time.Hour,
		20 * 365 * 24 * time.Hour,
	} {
		if now.Add(ahead).Sub(now) > sb.ScheduleHorizon() {
			t.Errorf("an occurrence %v ahead was refused before the call; YouTube "+
				"publishes no bound for polyemesis to enforce on its behalf", ahead)
		}
	}
}

// ---------------------------------------------------------- reschedule

const ytStubExistingBroadcast = `{"items":[{"id":"bcast-1",
	"snippet":{"title":"Friday night set","description":"come along",
		"channelId":"UCchannel","scheduledStartTime":"2030-01-01T18:00:00Z"},
	"status":{"lifeCycleStatus":"ready"},
	"contentDetails":{"enableDvr":true,"enableAutoStart":true,"enableAutoStop":false,
		"monitorStream":{"enableMonitorStream":true,"broadcastStreamDelayMs":30000}}}]}`

// The move reads the broadcast back and writes every field of every part.
//
// MUTATION, internal/oauth/youtube_schedule.go, in RescheduleBroadcast: build
// the update body directly as {"id":..., "snippet":{"scheduledStartTime":...}}
// instead of going through writeBroadcastParts.
// Observed: FAIL -- title, description and the monitorStream fields all absent
// from the PUT. YouTube answers 200 to that and blanks them, which is trap 2:
// liveBroadcasts is destructive BY PART.
func TestYouTubeRescheduleCarriesEveryFieldOfEveryPartItSends(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, ytStubExistingBroadcast)
		},
		"PUT " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"id":"bcast-1"}`)
		},
	})

	at := time.Date(2030, 2, 3, 9, 30, 0, 0, time.UTC)
	if err := y.RescheduleBroadcast(context.Background(), "tok", "bcast-1", at); err != nil {
		t.Fatalf("RescheduleBroadcast: %v", err)
	}

	// --- the read
	read := find(log, http.MethodGet, ytBroadcastsPath)
	rq := mustQuery(t, read)
	if rq.Get("id") != "bcast-1" {
		t.Errorf("read id = %q, want the broadcast being moved", rq.Get("id"))
	}
	// "Filters (specify exactly one of the following parameters)" over
	// broadcastStatus, id and mine. Adding one to look stricter makes the
	// request INVALID, not safer.
	for _, exclusive := range []string{"broadcastStatus", "mine"} {
		if _, sent := rq[exclusive]; sent {
			t.Errorf("the read sent %s alongside id; liveBroadcasts.list documents these "+
				"as mutually exclusive filters", exclusive)
		}
	}
	if _, sent := rq["broadcastType"]; sent {
		t.Error("the read sent broadcastType, which is documented for requests using " +
			"mine or broadcastStatus and applies to neither of them here")
	}
	for _, part := range []string{"snippet", "contentDetails", "status"} {
		if !strings.Contains(rq.Get("part"), part) {
			t.Errorf("the read did not ask for %s, so the write cannot carry it back "+
				"unchanged; part = %q", part, rq.Get("part"))
		}
	}

	// --- the write
	put := find(log, http.MethodPut, ytBroadcastsPath)
	if put == nil {
		t.Fatalf("no liveBroadcasts.update reached the stub; calls were %+v", log)
	}
	if put.Body["id"] != "bcast-1" {
		t.Errorf("update id = %v, want bcast-1", put.Body["id"])
	}
	snip, _ := put.Body["snippet"].(map[string]any)
	if snip == nil {
		t.Fatalf("the update sent no snippet: %+v", put.Body)
	}
	if got := snip["scheduledStartTime"]; got != "2030-02-03T09:30:00Z" {
		t.Errorf("scheduledStartTime = %v, want the new instant", got)
	}
	if snip["title"] != "Friday night set" || snip["description"] != "come along" {
		t.Errorf("the update dropped a snippet field it did not change: %+v — YouTube "+
			"reverts every field of a part it is sent without", snip)
	}
	cd, _ := put.Body["contentDetails"].(map[string]any)
	if cd == nil {
		t.Fatalf("the update sent no contentDetails, which every liveBroadcasts.update "+
			"requires: %+v", put.Body)
	}
	mon, _ := cd["monitorStream"].(map[string]any)
	if mon == nil || mon["enableMonitorStream"] != true || mon["broadcastStreamDelayMs"] != float64(30000) {
		t.Errorf("monitorStream = %+v, want the broadcast's current values carried "+
			"through; these two are required on every update", cd["monitorStream"])
	}
	if cd["enableDvr"] != true || cd["enableAutoStart"] != true || cd["enableAutoStop"] != false {
		t.Errorf("contentDetails toggles = %+v, want the current values — sending a "+
			"CHANGED value is what YouTube refuses once a broadcast leaves created/ready", cd)
	}
}

// An empty id is refused BEFORE any call, and the reason is specific: an empty
// id on a list filter is not "no results", it is an unfiltered list, and the
// update that followed would move whatever came back first.
func TestYouTubeRescheduleRefusesAnEmptyBroadcastIDWithoutCallingAnything(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, nil)
	for _, id := range []string{"", "   "} {
		if err := y.RescheduleBroadcast(context.Background(), "tok", id, time.Now()); err == nil {
			t.Errorf("RescheduleBroadcast(%q) succeeded", id)
		}
	}
	if len(log) != 0 {
		t.Errorf("an empty broadcast id reached the network: %+v", log)
	}
}

// A broadcast that YouTube no longer lists is an event page deleted from under
// the schedule. It must not be mistaken for the first item of an empty list.
func TestYouTubeRescheduleFailsRatherThanMovingSomebodyElsesBroadcast(t *testing.T) {
	tests := []struct {
		name  string
		items string
	}{
		{"nothing came back", `{"items":[]}`},
		{"something else came back", `{"items":[{"id":"other","snippet":{"title":"not ours"}}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var log []capture
			y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
				"GET " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
					io.WriteString(w, tc.items)
				},
			})
			err := y.RescheduleBroadcast(context.Background(), "tok", "bcast-1", time.Now())
			if err == nil {
				t.Fatal("RescheduleBroadcast succeeded against a broadcast that was not there")
			}
			if c := find(log, http.MethodPut, ytBroadcastsPath); c != nil {
				t.Errorf("an update was sent anyway: %+v", c)
			}
		})
	}
}

// The ownership guard. liveBroadcasts.list documents "owned by the
// authenticated user" exactly once, in the `mine` row, and nothing scopes the
// `id` filter to the caller -- so a stored id can point at a channel this token
// does not own, which is what an operator reconnecting a different Google
// account to the same destination leaves behind.
//
// MUTATION, internal/oauth/youtube_schedule.go, in RescheduleBroadcast: delete
// the snippet.channelId comparison.
// Observed: FAIL -- "RescheduleBroadcast moved a broadcast on a channel this
// token does not own", with the PUT recorded against UCsomeoneelse's broadcast.
// In production YouTube answers that with a bare 403, which reads as a scope
// problem rather than as the wrong account being connected.
func TestYouTubeRescheduleWillNotMoveABroadcastOnAnotherChannel(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, strings.Replace(ytStubExistingBroadcast,
				`"channelId":"UCchannel"`, `"channelId":"UCsomeoneelse"`, 1))
		},
		// Answers 200 so that a guard-less build fails on the assertions below
		// rather than on the stub's own 404. The claim is that the write is not
		// SENT, not that it would have been refused.
		"PUT " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"id":"bcast-1"}`)
		},
	})
	err := y.RescheduleBroadcast(context.Background(), "tok", "bcast-1", time.Now())
	if err == nil {
		t.Fatal("RescheduleBroadcast moved a broadcast on a channel this token does not own")
	}
	if !strings.Contains(err.Error(), "UCsomeoneelse") || !strings.Contains(err.Error(), "UCchannel") {
		t.Errorf("the error names neither channel: %v", err)
	}
	if c := find(log, http.MethodPut, ytBroadcastsPath); c != nil {
		t.Errorf("the update was sent anyway: %+v", c)
	}
}

// ------------------------------------------------------------- targets

// One channel, reported as one target rather than as none. AccountFor refuses a
// ref naming a different channel, because every YouTube call is scoped to
// whatever channel the token belongs to -- there is no addressing parameter to
// get wrong, so the mismatch would publish the next show to the new channel
// while every screen still named the old one.
func TestYouTubeTargetsIsTheOneChannelTheTokenOwns(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, nil)
	ctx := context.Background()

	got, err := y.Targets(ctx, "cid", "tok")
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	if len(got) != 1 || got[0].Ref != "UCchannel" || got[0].Name != "Night Owl" {
		t.Fatalf("Targets = %+v, want the single connected channel", got)
	}
	if got[0].Kind != "channel" {
		t.Errorf("Kind = %q, want YouTube's own word for what this is", got[0].Kind)
	}

	if _, err := y.AccountFor(ctx, "cid", "tok", ""); err != nil {
		t.Errorf("AccountFor with the default ref: %v", err)
	}
	if _, err := y.AccountFor(ctx, "cid", "tok", "UCchannel"); err != nil {
		t.Errorf("AccountFor with the matching ref: %v", err)
	}
	err = func() error { _, e := y.AccountFor(ctx, "cid", "tok", "UCsomeoneelse"); return e }()
	if err == nil {
		t.Fatal("AccountFor accepted a ref for a channel this token does not own")
	}
	if !strings.Contains(err.Error(), "UCchannel") || !strings.Contains(err.Error(), "UCsomeoneelse") {
		t.Errorf("the error names neither channel, so an operator cannot tell which is "+
			"which: %v", err)
	}
}

// A destination pointed at the wrong channel must not get as far as creating a
// broadcast on the right one.
func TestYouTubeIngestForRefusesAMismatchedChannelBeforeCreatingAnything(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, nil)
	if _, err := y.IngestFor(context.Background(), "cid", "tok", "UCsomeoneelse",
		IngestOptions{ScheduledFor: time.Unix(1893456000, 0)}); err == nil {
		t.Fatal("IngestFor created a broadcast for a mismatched channel ref")
	}
	if c := find(log, http.MethodPost, ytBroadcastsPath); c != nil {
		t.Errorf("a broadcast was created anyway: %+v", c)
	}
}
