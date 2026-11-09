package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const evStreams = `{"items":[
 {"id":"stream-77","snippet":{"title":"mine"},"status":{"streamStatus":"inactive"},"cdn":{"ingestionType":"rtmp","ingestionInfo":{"streamName":"key-abc","ingestionAddress":"rtmp://a/live2"}}},
 {"id":"stream-88","snippet":{"title":"busy"},"status":{"streamStatus":"active"},"cdn":{"ingestionType":"rtmp","ingestionInfo":{"streamName":"key-other","ingestionAddress":"rtmp://a/live2"}}},
 {"id":"stream-99","snippet":{"title":"idle"},"status":{"streamStatus":"inactive"},"cdn":{"ingestionType":"rtmp","ingestionInfo":{"streamName":"key-idle","ingestionAddress":"rtmp://a/live2"}}}]}`

func evJSON(id, status, bound string, autoStart bool) string {
	b, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"id": id, "snippet": map[string]any{"title": "Event " + id, "liveChatId": "chat-" + id, "scheduledStartTime": "2030-01-01T18:00:00Z", "channelId": "UCchannel"},
		"status":         map[string]any{"lifeCycleStatus": status, "privacyStatus": "unlisted"},
		"contentDetails": map[string]any{"boundStreamId": bound, "enableAutoStart": autoStart, "enableAutoStop": false, "enableDvr": true, "monitorStream": map[string]any{"enableMonitorStream": false}}}}})
	return string(b)
}

// evStub answers GET /liveBroadcasts?id=... with the event, and the active read with nothing.
func evStub(t *testing.T, log *[]capture, event string, extra map[string]http.HandlerFunc) *YouTube {
	h := map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, evStreams) },
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("id") != "" {
				io.WriteString(w, event)
				return
			}
			io.WriteString(w, `{"items":[]}`)
		},
	}
	for k, v := range extra {
		h[k] = v
	}
	return lnStub(t, log, h)
}

func noPost(t *testing.T, log []capture, path string) {
	t.Helper()
	for _, c := range log {
		if c.Method == http.MethodPost && c.Path == path {
			t.Errorf("a POST %s was sent", path)
		}
	}
}

func TestOpenLiveNowUsesAPickedEventBindsItAndCreatesNothing(t *testing.T) {
	var log []capture
	y := evStub(t, &log, evJSON("ev-1", "ready", "", false), nil)
	r, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, EventID: "ev-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Existing || r.BroadcastID != "ev-1" || r.LiveChatID != "chat-ev-1" || r.StreamID != "stream-77" {
		t.Errorf("result = %+v, want the picked event, its own chat, on the destination's stream", r)
	}
	noPost(t, log, ytBroadcastsPath)
	if bind := find(log, http.MethodPost, ytBindPath); bind == nil || mustQuery(t, bind).Get("streamId") != "stream-77" {
		t.Errorf("the picked event was not bound to the destination's stream: %v", callsOf(log))
	}
}

func TestOpenLiveNowDoesNotRebindAnEventAlreadyOnTheStream(t *testing.T) {
	var log []capture
	y := evStub(t, &log, evJSON("ev-1", "ready", "stream-77", true), nil)
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, EventID: "ev-1"}); err != nil {
		t.Fatal(err)
	}
	noPost(t, log, ytBindPath)
	noPost(t, log, ytBroadcastsPath)
}

func TestAnEventOnAnotherIdleStreamIsMovedOnlyOnConfirmAndNeverOffAnActiveStream(t *testing.T) {
	var log []capture
	y := evStub(t, &log, evJSON("ev-1", "ready", "stream-99", true), nil)
	_, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, EventID: "ev-1"})
	if !errors.Is(err, ErrEventRebindNeeded) {
		t.Fatalf("err = %v, want a confirmation to be required", err)
	}
	noPost(t, log, ytBindPath)
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, EventID: "ev-1", RebindEvent: true}); err != nil {
		t.Fatalf("a confirmed rebind off an idle stream: %v", err)
	}
	if find(log, http.MethodPost, ytBindPath) == nil {
		t.Error("the confirmed rebind sent no bind")
	}

	log = nil
	y = evStub(t, &log, evJSON("ev-2", "ready", "stream-88", true), nil)
	_, err = y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, EventID: "ev-2", RebindEvent: true})
	if !errors.Is(err, ErrEventStreamActive) {
		t.Fatalf("err = %v, want a refusal to move an event off an active stream even when confirmed", err)
	}
	noPost(t, log, ytBindPath)
}

// A finished event is never reused: the open creates a broadcast instead.
func TestAFinishedOrVanishedPickedEventFallsBackToCreating(t *testing.T) {
	for name, ev := range map[string]string{"complete": evJSON("ev-1", "complete", "stream-77", true), "vanished": `{"items":[]}`} {
		var log []capture
		y := evStub(t, &log, ev, nil)
		r, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, EventID: "ev-1"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.Existing || r.BroadcastID != "bcast-9" {
			t.Errorf("%s: result = %+v, want a new broadcast", name, r)
		}
		if find(log, http.MethodPost, ytBroadcastsPath) == nil {
			t.Errorf("%s: nothing was created", name)
		}
	}
}

func TestCreateFieldsReachTheInsertAndMadeForKidsIsAlwaysExplicit(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, nil)
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey}); err != nil {
		t.Fatal(err)
	}
	ins := find(log, http.MethodPost, ytBroadcastsPath)
	st, _ := ins.Body["status"].(map[string]any)
	if v, ok := st["selfDeclaredMadeForKids"]; !ok || v != false {
		t.Errorf("selfDeclaredMadeForKids = %v (present %v), want an explicit false by default", v, ok)
	}
	if _, ok := ins.Body["snippet"].(map[string]any)["description"]; ok {
		t.Error("an empty description was sent")
	}
	cd := ins.Body["contentDetails"].(map[string]any)
	for _, k := range []string{"latencyPreference", "enableDvr", "projection"} {
		if _, ok := cd[k]; ok {
			t.Errorf("contentDetails carries %s although none was set", k)
		}
	}

	log = nil
	y = lnStub(t, &log, nil)
	yes, no := true, false
	_, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey,
		EventFields: EventFields{Description: "About the show", MadeForKids: &yes, Latency: "low", EnableDvr: &no, Projection360: true}})
	if err != nil {
		t.Fatal(err)
	}
	ins = find(log, http.MethodPost, ytBroadcastsPath)
	st, _ = ins.Body["status"].(map[string]any)
	cd = ins.Body["contentDetails"].(map[string]any)
	snip := ins.Body["snippet"].(map[string]any)
	if snip["description"] != "About the show" || st["selfDeclaredMadeForKids"] != true ||
		cd["latencyPreference"] != "low" || cd["enableDvr"] != false || cd["projection"] != "360" {
		t.Errorf("insert = %v", ins.Body)
	}
	if cd["enableAutoStart"] != true || cd["enableAutoStop"] != false {
		t.Errorf("the new fields displaced auto-start/auto-stop: %v", cd)
	}
}

func TestACategoryIsAppliedAfterTheCreateAndItsFailureIsOnlyAWarning(t *testing.T) {
	cats := `{"items":[{"id":"20","snippet":{"title":"Gaming","assignable":true}}]}`
	var log []capture
	y := lnStub(t, &log, map[string]http.HandlerFunc{
		"GET /videoCategories": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, cats) },
		"GET /videos": func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"items":[{"snippet":{"title":"t","description":"","categoryId":"22"}}]}`)
		},
		"PUT /videos": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) },
	})
	r, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, Category: "Gaming"})
	if err != nil || len(r.Warnings) != 0 {
		t.Fatalf("err %v warnings %v", err, r)
	}
	put := find(log, http.MethodPut, "/videos")
	if put == nil || put.Body["snippet"].(map[string]any)["categoryId"] != "20" {
		t.Errorf("the category was not written to the video: %v", callsOf(log))
	}

	log = nil
	y = lnStub(t, &log, map[string]http.HandlerFunc{
		"GET /videoCategories": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, cats) },
	})
	r, err = y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, Category: "Nonsense"})
	if err != nil {
		t.Fatalf("a bad category failed the open: %v", err)
	}
	if len(r.Warnings) != 1 || r.BroadcastID != "bcast-9" {
		t.Errorf("result = %+v, want the broadcast and one warning", r)
	}
}

func TestListEventsAsksForUpcomingAndActiveSeparatelyAndNeverReturnsAKey(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, evStreams) },
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("broadcastStatus") {
			case "upcoming":
				io.WriteString(w, evJSON("ev-up", "ready", "stream-77", false))
			case "active":
				io.WriteString(w, strings.Replace(evJSON("ev-live", "live", "stream-88", true), `"items":[`, `"items":[{"id":"ev-done","status":{"lifeCycleStatus":"complete"},"snippet":{},"contentDetails":{}},`, 1))
			default:
				t.Errorf("unexpected query %s", r.URL.RawQuery)
			}
		},
	})
	evs, err := y.ListEvents(context.Background(), "tok", []string{"key-abc"})
	if err != nil {
		t.Fatal(err)
	}
	var qs []string
	for _, c := range log {
		if c.Path == ytBroadcastsPath {
			q := mustQuery(t, &c)
			if q.Get("mine") != "" {
				t.Errorf("mine sent beside broadcastStatus, which YouTube refuses: %s", c.Query)
			}
			qs = append(qs, q.Get("broadcastStatus"))
		}
	}
	if strings.Join(qs, ",") != "upcoming,active" {
		t.Errorf("broadcastStatus queries = %v", qs)
	}
	if len(evs) != 2 {
		t.Fatalf("events = %+v, want the upcoming and the live one, not the complete one", evs)
	}
	up := evs[0]
	if up.ID != "ev-up" || up.EnableAutoStart || up.EnableAutoStop || up.StreamActive == nil || *up.StreamActive ||
		len(up.BoundToHeld) != 1 || up.BoundToHeld[0] != 0 || up.Privacy != "unlisted" {
		t.Errorf("upcoming event = %+v", up)
	}
	if evs[1].StreamActive == nil || !*evs[1].StreamActive {
		t.Errorf("the live event's stream should read active: %+v", evs[1])
	}
	raw, _ := json.Marshal(evs)
	for _, leak := range []string{"key-abc", "key-other", "streamName", "ingestionAddress", "rtmp://"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the event list carries %q: %s", leak, raw)
		}
	}
}

func TestSetEventAutoStartIsAReadThenWriteThatCarriesTheOtherFieldsAndRefusesALiveEvent(t *testing.T) {
	var log []capture
	y := evStub(t, &log, evJSON("ev-1", "ready", "stream-77", false), map[string]http.HandlerFunc{
		"GET /channels":           func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, ytStubChannel) },
		"PUT " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) },
	})
	if err := y.SetEventAutoStart(context.Background(), "tok", "ev-1"); err != nil {
		t.Fatal(err)
	}
	put := find(log, http.MethodPut, ytBroadcastsPath)
	cd, _ := put.Body["contentDetails"].(map[string]any)
	if put == nil || cd["enableAutoStart"] != true {
		t.Fatalf("auto-start was not turned on: %v", log)
	}
	if cd["enableAutoStop"] != false || cd["enableDvr"] != true {
		t.Errorf("the update reset the other toggles: %v", cd)
	}
	if put.Body["snippet"].(map[string]any)["title"] != "Event ev-1" {
		t.Errorf("the update dropped the title: %v", put.Body["snippet"])
	}

	log = nil
	y = evStub(t, &log, evJSON("ev-2", "live", "stream-77", false), nil)
	if err := y.SetEventAutoStart(context.Background(), "tok", "ev-2"); err == nil {
		t.Error("auto-start was changed on a live event")
	}
	if find(log, http.MethodPut, ytBroadcastsPath) != nil {
		t.Error("a write was sent for a live event")
	}
}

// A scheduled show carries the destination's create-time fields, and one with none is created exactly as before.
func TestAScheduledShowCarriesTheCreateFieldsAndAnUnsetOneIsUnchanged(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, nil)
	at := ytTestTime()
	if _, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel", IngestOptions{ScheduledFor: at}); err != nil {
		t.Fatal(err)
	}
	ins := find(log, http.MethodPost, ytBroadcastsPath)
	if q := mustQuery(t, ins); q.Get("part") != "snippet,status" {
		t.Errorf("an unset show's part = %q, want it unchanged", q.Get("part"))
	}
	if _, ok := ins.Body["status"].(map[string]any)["selfDeclaredMadeForKids"]; ok {
		t.Error("made-for-kids was sent for a destination that never set it")
	}

	log = nil
	y = ytScheduleStub(t, &log, nil)
	yes, no := true, false
	_, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel", IngestOptions{ScheduledFor: at,
		YouTubeEvent: EventFields{Description: "About", MadeForKids: &yes, Latency: "ultraLow", EnableDvr: &no, Projection360: true}})
	if err != nil {
		t.Fatal(err)
	}
	ins = find(log, http.MethodPost, ytBroadcastsPath)
	if q := mustQuery(t, ins); q.Get("part") != "snippet,status,contentDetails" {
		t.Errorf("part = %q, want contentDetails named because the body carries it", q.Get("part"))
	}
	cd := ins.Body["contentDetails"].(map[string]any)
	st := ins.Body["status"].(map[string]any)
	if ins.Body["snippet"].(map[string]any)["description"] != "About" || st["selfDeclaredMadeForKids"] != true ||
		cd["latencyPreference"] != "ultraLow" || cd["enableDvr"] != false || cd["projection"] != "360" {
		t.Errorf("insert = %v", ins.Body)
	}
}

func ytTestTime() time.Time { return time.Date(2030, 1, 1, 18, 0, 0, 0, time.UTC) }

func TestOpenLiveNowReusesItsOwnEarlierUnrecordedBroadcastInsteadOfMakingAnother(t *testing.T) {
	start := time.Now().Add(90 * time.Second).UTC().Format(time.RFC3339)
	own := `{"items":[{"id":"left-over","snippet":{"title":"Friday","scheduledStartTime":"` + start + `","liveChatId":"chat-lo"},"status":{"lifeCycleStatus":"created"},` +
		`"contentDetails":{"boundStreamId":"","enableAutoStart":true,"enableAutoStop":false}}]}`
	var log []capture
	y := lnStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("broadcastStatus") == "upcoming" {
				io.WriteString(w, own)
				return
			}
			io.WriteString(w, `{"items":[]}`)
		},
	})
	r, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, Title: "Friday", AttemptAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if r.BroadcastID != "left-over" || r.LiveChatID != "chat-lo" || r.Existing {
		t.Errorf("result = %+v, want the earlier attempt's broadcast reused", r)
	}
	noPost(t, log, ytBroadcastsPath)
	if bind := find(log, http.MethodPost, ytBindPath); bind == nil || mustQuery(t, bind).Get("id") != "left-over" {
		t.Errorf("the reused broadcast was not bound: %v", callsOf(log))
	}
}

// A broadcast with another title, or auto-stop on, or bound to another stream, is somebody else's and is not reused.
func TestOpenLiveNowDoesNotReuseABroadcastThatIsNotItsOwn(t *testing.T) {
	start := time.Now().Add(90 * time.Second).UTC().Format(time.RFC3339)
	for name, item := range map[string]string{
		"other title":    `"title":"Other","scheduledStartTime":"` + start + `"},"status":{"lifeCycleStatus":"created"},"contentDetails":{"enableAutoStart":true,"enableAutoStop":false}`,
		"auto-stop on":   `"title":"Friday","scheduledStartTime":"` + start + `"},"status":{"lifeCycleStatus":"created"},"contentDetails":{"enableAutoStart":true,"enableAutoStop":true}`,
		"other stream":   `"title":"Friday","scheduledStartTime":"` + start + `"},"status":{"lifeCycleStatus":"created"},"contentDetails":{"boundStreamId":"stream-88","enableAutoStart":true,"enableAutoStop":false}`,
		"start long ago": `"title":"Friday","scheduledStartTime":"2020-01-01T00:00:00Z"},"status":{"lifeCycleStatus":"created"},"contentDetails":{"enableAutoStart":true,"enableAutoStop":false}`,
	} {
		var log []capture
		y := lnStub(t, &log, map[string]http.HandlerFunc{
			"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("broadcastStatus") == "upcoming" {
					io.WriteString(w, `{"items":[{"id":"x-1","snippet":{`+item+`}]}`)
					return
				}
				io.WriteString(w, `{"items":[]}`)
			},
		})
		r, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, Title: "Friday", AttemptAt: time.Now()})
		if err != nil || r.BroadcastID != "bcast-9" {
			t.Errorf("%s: result %+v err %v, want a new broadcast", name, r, err)
		}
	}
}

func TestDeleteUnairedBroadcastDeletesOnlyACreatedOrReadyOne(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, map[string]http.HandlerFunc{
		"DELETE " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) },
	})
	for _, st := range []string{"live", "testing", "complete", "liveStarting", ""} {
		if err := y.DeleteUnairedBroadcast(context.Background(), "tok", "b-1", st); err == nil {
			t.Errorf("status %q: a broadcast that may have aired was deleted", st)
		}
	}
	if find(log, http.MethodDelete, ytBroadcastsPath) != nil {
		t.Fatal("a delete was sent for a broadcast that may have aired")
	}
	for _, st := range []string{"created", "ready"} {
		if err := y.DeleteUnairedBroadcast(context.Background(), "tok", "b-1", st); err != nil {
			t.Errorf("status %q: %v", st, err)
		}
	}
}

// Without the caller's proof of an earlier attempt, even a perfect lookalike is never adopted; and a reused broadcast
// whose bind is refused is left alone, not deleted.
func TestAnUpcomingLookalikeIsAdoptedOnlyWithProofAndNeverDeletedOnABindRefusal(t *testing.T) {
	start := time.Now().Add(90 * time.Second).UTC().Format(time.RFC3339)
	look := `{"items":[{"id":"studio-1","snippet":{"title":"Friday","scheduledStartTime":"` + start + `"},"status":{"lifeCycleStatus":"created"},` +
		`"contentDetails":{"boundStreamId":"","enableAutoStart":true,"enableAutoStop":false}}]}`
	handlers := func(bindStatus int) map[string]http.HandlerFunc {
		h := map[string]http.HandlerFunc{
			"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("broadcastStatus") == "upcoming" {
					io.WriteString(w, look)
					return
				}
				io.WriteString(w, `{"items":[]}`)
			},
			"DELETE " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) },
		}
		if bindStatus != 0 {
			h["POST "+ytBindPath] = func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(bindStatus)
				io.WriteString(w, `{"error":{"errors":[{"reason":"liveBroadcastBindingNotAllowed"}]}}`)
			}
		}
		return h
	}
	var log []capture
	y := lnStub(t, &log, handlers(0))
	r, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, Title: "Friday"})
	if err != nil || r.BroadcastID != "bcast-9" || r.Reused {
		t.Errorf("without MayReuse: result %+v err %v, want a fresh broadcast and the lookalike untouched", r, err)
	}

	log = nil
	y = lnStub(t, &log, handlers(http.StatusForbidden))
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, Title: "Friday", AttemptAt: time.Now()}); err == nil {
		t.Fatal("a refused bind was not an error")
	}
	if find(log, http.MethodDelete, ytBroadcastsPath) != nil {
		t.Error("a reused broadcast was deleted after a refused bind")
	}
}

// The marker's time is what ties a lookalike to the attempt: the right start is taken, a start from another moment is
// not, and BeforeInsert runs once per insert and can stop it.
func TestReuseIsTiedToTheMarkersTimeAndBeforeInsertGatesTheInsert(t *testing.T) {
	at := time.Now().Add(-3 * time.Minute)
	lookalike := func(start time.Time) map[string]http.HandlerFunc {
		return map[string]http.HandlerFunc{"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("broadcastStatus") == "upcoming" {
				io.WriteString(w, `{"items":[{"id":"x-1","snippet":{"title":"Friday","scheduledStartTime":"`+start.UTC().Format(time.RFC3339)+`"},`+
					`"status":{"lifeCycleStatus":"created"},"contentDetails":{"enableAutoStart":true,"enableAutoStop":false}}]}`)
				return
			}
			io.WriteString(w, `{"items":[]}`)
		}}
	}
	var log []capture
	y := lnStub(t, &log, lookalike(at.Add(2*time.Minute+20*time.Second)))
	if r, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, Title: "Friday", AttemptAt: at}); err != nil || r.BroadcastID != "x-1" {
		t.Errorf("the lookalike at marker+lead was not reused: %+v %v", r, err)
	}
	log = nil
	y = lnStub(t, &log, lookalike(at.Add(9*time.Minute)))
	r, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, Title: "Friday", AttemptAt: at})
	if err != nil || r.BroadcastID != "bcast-9" {
		t.Errorf("a lookalike nine minutes after the marker was reused: %+v %v", r, err)
	}

	log = nil
	y = lnStub(t, &log, nil)
	stamps := 0
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey,
		BeforeInsert: func() error { stamps++; return nil }}); err != nil || stamps != 1 {
		t.Errorf("BeforeInsert ran %d times (err %v), want once before the insert", stamps, err)
	}
	log = nil
	y = lnStub(t, &log, nil)
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey,
		BeforeInsert: func() error { return errors.New("store down") }}); err == nil {
		t.Error("an insert went ahead although its marker could not be written")
	}
	noPost(t, log, ytBroadcastsPath)
}

func TestBroadcastStateSaysNotListedWithATypedError(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, map[string]http.HandlerFunc{"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"items":[]}`) }})
	if _, err := y.BroadcastState(context.Background(), "tok", "gone-1"); !errors.Is(err, ErrBroadcastNotListed) {
		t.Errorf("err = %v, want ErrBroadcastNotListed", err)
	}
}

func TestBroadcastStateCarriesTheLiveChatId(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, map[string]http.HandlerFunc{"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"items":[{"id":"b-1","snippet":{"title":"t","liveChatId":"chat-b1"},"status":{"lifeCycleStatus":"live"},"contentDetails":{}}]}`)
	}})
	st, err := y.BroadcastState(context.Background(), "tok", "b-1")
	if err != nil || st.LiveChatID != "chat-b1" {
		t.Errorf("state = %+v %v, want the liveChatId", st, err)
	}
}
