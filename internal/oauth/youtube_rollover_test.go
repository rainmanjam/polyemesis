package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// YouTube's planned rollover, tested against a fake that behaves like the channel: a broadcast has a lifecycle
// status, a bound stream and a monitor setting; a transition is refused by name when the state does not allow it;
// a stream is active or not. A fake that agrees with the code proves only that the code agrees with itself, so
// the refusals below are the real Google envelopes and the states are the documented ones.

// rolloverFake is the channel. It records every write so a test can assert what was sent and, just as important,
// what was not.
type rolloverFake struct {
	t  *testing.T
	mu sync.Mutex

	broadcasts map[string]*fakeBroadcast
	streams    map[string]string // stream id -> streamStatus
	nextID     int

	// refuse maps "broadcastID/target" to a Google refusal reason, for every attempt. A test sets it to make a
	// transition fail for good.
	refuse map[string]string
	// refuseQueue maps "broadcastID/target" to reasons that refuse the next attempts in order, then stop. It is
	// how a test makes the second attempt behave differently from the first.
	refuseQueue map[string][]string
	// autoStart, when true, makes a broadcast bound with enableAutoStart go live by itself on bind.
	autoStart bool
	// refuseInsert, when set, is returned for the next liveBroadcasts.insert.
	refuseInsert string
	// refuseDelete makes DELETE fail, so a test can reach the "could not remove either" path.
	refuseDelete bool

	// liveThen500 maps "broadcastID/target" to a transition that takes effect and then answers 500, the way a
	// transition that YouTube carried out can still fail on the wire. fail500 answers 500 and changes nothing.
	liveThen500 map[string]bool
	fail500     map[string]bool
	// readsBeforeFail, when set for an id, lets that many liveBroadcasts reads of it succeed and fails every one
	// after, so a test can make the re-read after a failed transition fail.
	readsBeforeFail map[string]int
	reads           map[string]int

	writes []string // "METHOD path" for every write, in order
}

type fakeBroadcast struct {
	id, title, description, privacy, status, streamID string
	monitor, autoStart                                bool
	scheduledStart                                    string
	// captions is contentDetails.closedCaptionsType and delayMs the monitor stream's delay, both as the
	// broadcast was created with them.
	captions string
	delayMs  int
}

func newRolloverFake(t *testing.T) *rolloverFake {
	return &rolloverFake{
		t:               t,
		broadcasts:      map[string]*fakeBroadcast{},
		streams:         map[string]string{},
		refuse:          map[string]string{},
		refuseQueue:     map[string][]string{},
		liveThen500:     map[string]bool{},
		fail500:         map[string]bool{},
		readsBeforeFail: map[string]int{},
		reads:           map[string]int{},
	}
}

// liveOld seeds the broadcast a rollover rolls over: live on the horizontal stream, with a public title and a
// description, and the vertical companion stream present on the channel, so a test can assert it is never used.
func (f *rolloverFake) liveOld() {
	f.broadcasts["old-1"] = &fakeBroadcast{
		id: "old-1", title: "Thursday show", description: "Weekly show notes", privacy: "unlisted",
		status: "live", streamID: "stream-h", monitor: false, autoStart: true,
	}
	f.streams["stream-h"] = "active"
	f.streams["stream-v"] = "active" // the #7 vertical stream: present, and never to be bound
}

func (f *rolloverFake) client() *YouTube {
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.t.Cleanup(srv.Close)
	return NewYouTube(WithBaseURL(srv.URL))
}

func (f *rolloverFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == ytBroadcastsPath:
		id := q.Get("id")
		f.reads[id]++
		if n, ok := f.readsBeforeFail[id]; ok && f.reads[id] > n {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.listBroadcasts(w, id)
	case r.Method == http.MethodPost && r.URL.Path == ytBroadcastsPath:
		f.writes = append(f.writes, "POST /liveBroadcasts")
		f.insert(w, r)
	case r.Method == http.MethodPost && r.URL.Path == ytBindPath:
		f.writes = append(f.writes, "POST /liveBroadcasts/bind")
		f.bind(w, q.Get("id"), q.Get("streamId"))
	case r.Method == http.MethodPost && r.URL.Path == ytTransitionPath:
		// The target and the id go into the record, so a test can assert which broadcast was moved to what, and in
		// which order.
		f.writes = append(f.writes, "POST /liveBroadcasts/transition "+q.Get("broadcastStatus")+" "+q.Get("id"))
		f.transition(w, q.Get("id"), q.Get("broadcastStatus"))
	case r.Method == http.MethodDelete && r.URL.Path == ytBroadcastsPath:
		f.writes = append(f.writes, "DELETE /liveBroadcasts")
		f.remove(w, q.Get("id"))
	case r.Method == http.MethodGet && r.URL.Path == ytStreamsPath:
		if q.Get("id") == "" {
			// The ingest path (streamFor) lists streams without an id. A rollover must never reach it.
			f.t.Errorf("a rollover listed the channel's streams without an id: that is the first-rtmp fallback, which can adopt the vertical stream")
		}
		f.streamStatus(w, q.Get("id"))
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *rolloverFake) listBroadcasts(w http.ResponseWriter, id string) {
	type item struct {
		ID      string `json:"id"`
		Snippet struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"snippet"`
		Status struct {
			LifeCycleStatus string `json:"lifeCycleStatus"`
			PrivacyStatus   string `json:"privacyStatus"`
		} `json:"status"`
		ContentDetails struct {
			BoundStreamID      string `json:"boundStreamId,omitempty"`
			EnableAutoStart    bool   `json:"enableAutoStart"`
			ClosedCaptionsType string `json:"closedCaptionsType,omitempty"`
			MonitorStream      struct {
				EnableMonitorStream    bool `json:"enableMonitorStream"`
				BroadcastStreamDelayMs int  `json:"broadcastStreamDelayMs"`
			} `json:"monitorStream"`
		} `json:"contentDetails"`
	}
	var items []item
	if b, ok := f.broadcasts[id]; ok {
		var it item
		it.ID = b.id
		it.Snippet.Title, it.Snippet.Description = b.title, b.description
		it.Status.LifeCycleStatus, it.Status.PrivacyStatus = b.status, b.privacy
		it.ContentDetails.BoundStreamID = b.streamID
		it.ContentDetails.EnableAutoStart = b.autoStart
		it.ContentDetails.MonitorStream.EnableMonitorStream = b.monitor
		it.ContentDetails.MonitorStream.BroadcastStreamDelayMs = b.delayMs
		it.ContentDetails.ClosedCaptionsType = b.captions
		items = append(items, it)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

func (f *rolloverFake) insert(w http.ResponseWriter, r *http.Request) {
	if f.refuseInsert != "" {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(ytTransitionRefusal(f.refuseInsert, "refused")))
		f.refuseInsert = ""
		return
	}
	// YouTube reads the monitor flag only from the nested contentDetails.monitorStream object. A flat
	// contentDetails.enableMonitorStream key is ignored, so the replacement would keep the default monitor
	// stream on. The fake therefore reads the nested key and fails the test when the flat key is sent.
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Errorf("insert body: %v", err)
	}
	var body struct {
		Snippet struct {
			Title              string `json:"title"`
			Description        string `json:"description"`
			ScheduledStartTime string `json:"scheduledStartTime"`
		} `json:"snippet"`
		Status struct {
			PrivacyStatus string `json:"privacyStatus"`
		} `json:"status"`
		ContentDetails struct {
			EnableAutoStart    bool   `json:"enableAutoStart"`
			ClosedCaptionsType string `json:"closedCaptionsType"`
			MonitorStream      *struct {
				EnableMonitorStream    *bool `json:"enableMonitorStream"`
				BroadcastStreamDelayMs int   `json:"broadcastStreamDelayMs"`
			} `json:"monitorStream"`
		} `json:"contentDetails"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		f.t.Errorf("insert body: %v", err)
	}
	var flat struct {
		ContentDetails map[string]json.RawMessage `json:"contentDetails"`
	}
	_ = json.Unmarshal(raw, &flat)
	if _, ok := flat.ContentDetails["enableMonitorStream"]; ok {
		f.t.Errorf("the insert body sent a flat contentDetails.enableMonitorStream, which YouTube ignores")
	}
	monitor, delay := false, 0
	if body.ContentDetails.MonitorStream == nil || body.ContentDetails.MonitorStream.EnableMonitorStream == nil {
		f.t.Errorf("the insert body must nest enableMonitorStream under contentDetails.monitorStream")
	} else {
		monitor = *body.ContentDetails.MonitorStream.EnableMonitorStream
		delay = body.ContentDetails.MonitorStream.BroadcastStreamDelayMs
	}
	f.nextID++
	id := fmt.Sprintf("new-%d", f.nextID)
	f.broadcasts[id] = &fakeBroadcast{
		id: id, title: body.Snippet.Title, description: body.Snippet.Description, privacy: body.Status.PrivacyStatus,
		status: "created", monitor: monitor, autoStart: body.ContentDetails.EnableAutoStart,
		scheduledStart: body.Snippet.ScheduledStartTime,
		captions:       body.ContentDetails.ClosedCaptionsType, delayMs: delay,
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
}

func (f *rolloverFake) bind(w http.ResponseWriter, id, streamID string) {
	b, ok := f.broadcasts[id]
	if !ok || streamID == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if streamID == "stream-v" {
		f.t.Errorf("a rollover bound broadcast %s to the vertical stream stream-v; the vertical stream must never be a rollover's ingest", id)
	}
	b.streamID = streamID
	if b.autoStart && f.autoStart && f.streams[streamID] == "active" {
		b.status = "live"
	}
	_, _ = w.Write([]byte(`{}`))
}

func (f *rolloverFake) transition(w http.ResponseWriter, id, target string) {
	b, ok := f.broadcasts[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	key := id + "/" + target
	if f.fail500[key] {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if f.liveThen500[key] {
		b.status = target
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	reason, bad := f.refuse[key]
	if q := f.refuseQueue[key]; len(q) > 0 {
		reason, bad = q[0], true
		f.refuseQueue[key] = q[1:]
	}
	if bad {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(ytTransitionRefusal(reason, "refused by the fake")))
		return
	}
	switch target {
	case "testing":
		// Testing needs the monitor stream (docs-youtube B22). The fake moves straight to testing; the real
		// channel passes through testStarting first.
		if !b.monitor {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(ytTransitionRefusal("invalidTransition", "no monitor stream")))
			return
		}
		b.status = "testing"
	case "live":
		if b.monitor && b.status != "testing" {
			// An enabled monitor stream forces testing before live (B14).
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(ytTransitionRefusal("invalidTransition", "testing first")))
			return
		}
		if b.status == "live" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(ytTransitionRefusal("redundantTransition", "already live")))
			return
		}
		if f.streams[b.streamID] != "active" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(ytTransitionRefusal("errorStreamInactive", "the bound stream is inactive")))
			return
		}
		b.status = "live"
	case "complete":
		if b.status == "complete" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(ytTransitionRefusal("redundantTransition", "already complete")))
			return
		}
		b.status = "complete"
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_, _ = fmt.Fprintf(w, `{"id":%q,"status":{"lifeCycleStatus":%q}}`, id, b.status)
}

func (f *rolloverFake) remove(w http.ResponseWriter, id string) {
	if f.refuseDelete {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	delete(f.broadcasts, id)
	w.WriteHeader(http.StatusNoContent)
}

func (f *rolloverFake) streamStatus(w http.ResponseWriter, id string) {
	status, ok := f.streams[id]
	var items []map[string]any
	if ok {
		items = append(items, map[string]any{"id": id, "status": map[string]any{"streamStatus": status}})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

// wrote reports whether a write with this method and path reached the fake. Reads are not writes, and are not
// recorded.
func (f *rolloverFake) wrote(prefix string) int {
	n := 0
	for _, w := range f.writes {
		if strings.HasPrefix(w, prefix) {
			n++
		}
	}
	return n
}

func rolloverReq(now time.Time) RolloverRequest {
	return RolloverRequest{OldBroadcastID: "old-1", Now: now}
}

// TestARolloverCreatesBindsAndTakesTheReplacementLiveBeforeEndingTheOld is the happy path, and the order it is
// asserted in is the rule. The old broadcast must not be ended until the new one is live, and the new one must be
// bound to the old one's horizontal stream, never the vertical one.
func TestARolloverCreatesBindsAndTakesTheReplacementLiveBeforeEndingTheOld(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	y := f.client()
	now := time.Date(2026, 10, 31, 21, 0, 0, 0, time.UTC)

	out, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(now))
	if err != nil {
		t.Fatalf("rollover: %v", err)
	}
	if out.NewBroadcastID == "" || out.NewBroadcastID == "old-1" {
		t.Fatalf("no new broadcast id: %+v", out)
	}
	if !out.OldEnded || out.Gap || len(out.Warnings) != 0 {
		t.Errorf("outcome = %+v, want the old ended, no gap, no warnings", out)
	}
	newB := f.broadcasts[out.NewBroadcastID]
	if newB == nil || newB.status != "live" || newB.streamID != "stream-h" {
		t.Errorf("replacement = %+v, want live and bound to the horizontal stream", newB)
	}
	if old := f.broadcasts["old-1"]; old == nil || old.status != "complete" {
		t.Errorf("old broadcast = %+v, want complete", old)
	}
	// The order is the rule: create, bind, take the new one live, and only then end the old one. Ending first would
	// open a gap the channel did not need, and taking the new one live first is what keeps the feed on air.
	want := []string{
		"POST /liveBroadcasts",
		"POST /liveBroadcasts/bind",
		"POST /liveBroadcasts/transition live new-1",
		"POST /liveBroadcasts/transition complete old-1",
	}
	if strings.Join(f.writes, " | ") != strings.Join(want, " | ") {
		t.Fatalf("writes = %v, want %v", f.writes, want)
	}
}

// TestTheReplacementCarriesTheMonitorSettingAndAutoStartOfTheOldBroadcast checks the create body. The monitor stream
// must be off, because an enabled monitor stream forces a testing stage the rollover skips, and auto-start must match
// the old broadcast because it cannot change once the stream is active.
func TestTheReplacementCarriesTheMonitorSettingAndAutoStartOfTheOldBroadcast(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	y := f.client()
	out, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	if err != nil {
		t.Fatalf("rollover: %v", err)
	}
	newB := f.broadcasts[out.NewBroadcastID]
	if newB.monitor {
		t.Errorf("the replacement enabled its monitor stream, which forces a testing stage")
	}
	if !newB.autoStart {
		t.Errorf("the replacement did not carry the old broadcast's enableAutoStart")
	}
	if newB.privacy != "unlisted" {
		t.Errorf("privacy = %q, want the old broadcast's unlisted", newB.privacy)
	}
	if newB.title != "Thursday show" || newB.description != "Weekly show notes" {
		t.Errorf("title/description not copied from the live broadcast: %q / %q", newB.title, newB.description)
	}
}

// TestTheCallerChoosesTheTitleAndDescriptionWhenGiven checks the template path: a title and description the caller
// supplies win over the copy, and an empty field copies the live broadcast.
func TestTheCallerChoosesTheTitleAndDescriptionWhenGiven(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	y := f.client()
	req := rolloverReq(time.Now())
	req.Title = "Thursday show 2026-11-01"
	req.Privacy = "PUBLIC"
	out, err := y.RollOverBroadcast(context.Background(), "tok", req)
	if err != nil {
		t.Fatalf("rollover: %v", err)
	}
	newB := f.broadcasts[out.NewBroadcastID]
	if newB.title != "Thursday show 2026-11-01" {
		t.Errorf("title = %q, want the caller's", newB.title)
	}
	if newB.description != "Weekly show notes" {
		t.Errorf("description = %q, want the live broadcast's, since the caller gave none", newB.description)
	}
	if newB.privacy != "public" {
		t.Errorf("privacy = %q, want the caller's public, lower-cased", newB.privacy)
	}
}

// TestANonLiveBroadcastIsNotRolledOverAndNothingIsWritten covers the refusal before any write. A broadcast that is
// not on air has nothing to replace, so no create, bind or transition may be sent.
func TestANonLiveBroadcastIsNotRolledOverAndNothingIsWritten(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.broadcasts["old-1"].status = "complete"
	y := f.client()
	_, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	var re *RolloverError
	if !errors.As(err, &re) || re.Stage != "read" {
		t.Fatalf("err = %v, want a read-stage refusal", err)
	}
	if n := len(f.writes); n != 0 {
		t.Errorf("a non-live broadcast caused %d writes: %v", n, f.writes)
	}
}

// TestABroadcastWithNoStreamIsNotRolledOver covers a broadcast with no bound ingest. A replacement would have nothing
// to join, and the one correct fallback, streamFor, is exactly the path that can adopt the vertical stream.
func TestABroadcastWithNoStreamIsNotRolledOver(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.broadcasts["old-1"].streamID = ""
	y := f.client()
	_, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	if err == nil {
		t.Fatal("a broadcast with no bound stream was rolled over")
	}
	if n := len(f.writes); n != 0 {
		t.Errorf("writes = %v, want none", f.writes)
	}
}

// TestAStreamInactiveRefusalLeavesTheOldBroadcastOnAirAndRemovesTheReplacement is the rule the file comment names
// first: a failed rollover never stops the old broadcast. The replacement, which nobody asked for, is removed.
func TestAStreamInactiveRefusalLeavesTheOldBroadcastOnAirAndRemovesTheReplacement(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.refuse["new-1/live"] = "errorStreamInactive"
	y := f.client()
	_, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	var re *RolloverError
	if !errors.As(err, &re) || re.Stage != "start" || !re.OldStillLive {
		t.Fatalf("err = %#v, want a start-stage failure with the old broadcast still live", err)
	}
	if old := f.broadcasts["old-1"]; old.status != "live" {
		t.Errorf("the old broadcast is %q after a refused rollover; it must stay live", old.status)
	}
	if _, still := f.broadcasts["new-1"]; still {
		t.Errorf("the replacement was left behind as an event page nobody is watching")
	}
	if f.wrote("POST /liveBroadcasts/transition") != 1 {
		t.Errorf("transitions = %d, want the one refused attempt and no end of the old broadcast", f.wrote("POST /liveBroadcasts/transition"))
	}
}

// TestAChannelThatWillNotCarryTwoLiveBroadcastsEndsTheOldFirstAndReportsTheGap covers the fallback: when the channel
// refuses two live broadcasts, the old one ends and the new one goes live straight after. The gap is reported.
func TestAChannelThatWillNotCarryTwoLiveBroadcastsEndsTheOldFirstAndReportsTheGap(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.refuseQueue["new-1/live"] = []string{"concurrentBroadcastsExceedLimit"}
	y := f.client()
	out, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	if err != nil {
		t.Fatalf("rollover: %v", err)
	}
	if !out.Gap || !out.OldEnded {
		t.Errorf("outcome = %+v, want a reported gap with the old ended", out)
	}
	if f.broadcasts["old-1"].status != "complete" {
		t.Errorf("old broadcast = %q, want complete", f.broadcasts["old-1"].status)
	}
}

// TestAReplacementThatWillNotStartAfterTheGapIsKeptAsReady covers the bad end of the gap path directly. The old broadcast
// is ended, the replacement is refused going live, and the replacement must still exist in its ready state, with the
// error naming it, so the coordinator can take it live later.
func TestAReplacementThatWillNotStartAfterTheGapIsKeptAsReady(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.refuseQueue["new-1/live"] = []string{"concurrentBroadcastsExceedLimit", "errorExecutingTransition"}
	y := f.client()
	_, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	var re *RolloverError
	if !errors.As(err, &re) || re.Stage != "start" || re.OldStillLive {
		t.Fatalf("err = %#v, want a start-stage failure with the old broadcast ended", err)
	}
	if re.NewBroadcastID == "" {
		t.Fatalf("the failure does not name the replacement, so the coordinator cannot find it")
	}
	if _, still := f.broadcasts[re.NewBroadcastID]; !still {
		t.Errorf("the replacement %s was removed; it must be kept so it can be taken live", re.NewBroadcastID)
	}
}

// TestAnOldBroadcastThatCannotBeEndedIsWarnedAboutAndTheRolloverStillCounts covers a failure after the new broadcast
// is live. The rollover succeeded. The old broadcast is still on air, and the operator is told, without the rollover
// being called a failure.
func TestAnOldBroadcastThatCannotBeEndedIsWarnedAboutAndTheRolloverStillCounts(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.refuse["old-1/complete"] = "errorExecutingTransition"
	y := f.client()
	out, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	if err != nil {
		t.Fatalf("the replacement is live, so the rollover must not be an error: %v", err)
	}
	if out.OldEnded || len(out.Warnings) != 1 {
		t.Errorf("outcome = %+v, want the old not ended and one warning", out)
	}
	if f.broadcasts[out.NewBroadcastID].status != "live" {
		t.Errorf("the replacement is %q, want live", f.broadcasts[out.NewBroadcastID].status)
	}
}

// TestAutoStartThatAlreadyBroughtTheReplacementLiveIsNotTransitionedAgain covers the unverified auto-start case from
// the other side: when the replacement is already live after bind, no second live transition is sent.
func TestAutoStartThatAlreadyBroughtTheReplacementLiveIsNotTransitionedAgain(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.autoStart = true
	y := f.client()
	out, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	if err != nil {
		t.Fatalf("rollover: %v", err)
	}
	if !out.OldEnded {
		t.Errorf("outcome = %+v, want the old ended", out)
	}
	// The replacement was already live, so no live transition may reach it. Only the old broadcast is ended.
	want := []string{
		"POST /liveBroadcasts",
		"POST /liveBroadcasts/bind",
		"POST /liveBroadcasts/transition complete old-1",
	}
	if strings.Join(f.writes, " | ") != strings.Join(want, " | ") {
		t.Errorf("writes = %v, want %v: a live transition was sent to a replacement that was already live", f.writes, want)
	}
}

// TestAFailedCreateLeavesTheOldBroadcastOnAir covers the first write. A refused create must leave the old broadcast
// running and report the create stage.
func TestAFailedCreateLeavesTheOldBroadcastOnAir(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.refuseInsert = "liveStreamingNotEnabled"
	y := f.client()
	_, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	var re *RolloverError
	if !errors.As(err, &re) || re.Stage != "create" || !re.OldStillLive {
		t.Fatalf("err = %#v, want a create-stage failure with the old broadcast still live", err)
	}
	if f.broadcasts["old-1"].status != "live" {
		t.Errorf("the old broadcast is %q, want live", f.broadcasts["old-1"].status)
	}
}

// TestAReplacementThatCannotBeRemovedIsNamedSoItCanBeDeletedByHand covers the double failure: the replacement
// will not start and will not delete. The error must name it, since that id is the only way anyone finds the page.
func TestAReplacementThatCannotBeRemovedIsNamedSoItCanBeDeletedByHand(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.refuse["new-1/live"] = "errorStreamInactive"
	f.refuseDelete = true
	y := f.client()
	_, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	var re *RolloverError
	if !errors.As(err, &re) || re.NewBroadcastID != "new-1" {
		t.Fatalf("err = %#v, want the replacement new-1 named", err)
	}
	if !strings.Contains(re.Error(), "YouTube Studio") {
		t.Errorf("the error does not say where to delete the page: %v", re)
	}
}

// TestTheSessionStartIsThePlatformsOwnActualStartTime checks what the margin counts from. The platform's actual
// start is read from the broadcast, so a daemon that began watching a show late still counts the cap from when the
// show really went live. A broadcast that has no actual start yet reports zero, and nothing is counted.
func TestTheSessionStartIsThePlatformsOwnActualStartTime(t *testing.T) {
	actual := time.Date(2026, 10, 31, 18, 5, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ytBroadcastsPath || r.URL.Query().Get("id") != "old-1" {
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = fmt.Fprintf(w, `{"items":[{"id":"old-1","snippet":{"actualStartTime":%q}}]}`, actual.Format(time.RFC3339))
	}))
	defer srv.Close()
	y := NewYouTube(WithBaseURL(srv.URL))
	got, err := y.RolloverSessionStart(context.Background(), "tok", "old-1")
	if err != nil {
		t.Fatalf("session start: %v", err)
	}
	if !got.Equal(actual) {
		t.Errorf("session start = %s, want the platform's actual start %s", got, actual)
	}
}

// TestASessionWithNoActualStartYetReportsZero keeps an unstarted broadcast from being counted from a made-up moment.
func TestASessionWithNoActualStartYetReportsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"old-1","snippet":{}}]}`))
	}))
	defer srv.Close()
	y := NewYouTube(WithBaseURL(srv.URL))
	got, err := y.RolloverSessionStart(context.Background(), "tok", "old-1")
	if err != nil {
		t.Fatalf("session start: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("session start = %s, want zero until YouTube reports one", got)
	}
}

// TestAReplacementWhoseSourcePrivacyIsUnknownStaysPrivate: when the source read returns no privacyStatus, the
// replacement used to fall back to public. A show that was private or unlisted would then start its next session
// with a public event page, and nothing in the rollover ever corrects that before go-live. The rollover must fail
// closed to private when it cannot read the privacy it is copying.
//
// MUTATION, internal/oauth/youtube_rollover.go in RollOverBroadcast: restore the fallback to ytScheduledPrivacy.
// Observed: FAIL -- privacy = "public".
func TestAReplacementWhoseSourcePrivacyIsUnknownStaysPrivate(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.broadcasts["old-1"].privacy = ""
	y := f.client()
	out, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	if err != nil {
		t.Fatalf("rollover: %v", err)
	}
	if got := f.broadcasts[out.NewBroadcastID].privacy; got != "private" {
		t.Errorf("privacy = %q, want private: an unreadable source privacy must not widen to public", got)
	}
}

// TestAReplacementThatWentLiveDespiteAFailedTransitionIsNotDeleted (review #48): a transition YouTube carried out can
// still answer 500. Deleting the replacement on that answer would delete a broadcast that is on air, which is the
// one thing this file must never do. The state is re-read first, and a replacement that is live is kept and the
// rollover carries on to end the old one.
func TestAReplacementThatWentLiveDespiteAFailedTransitionIsNotDeleted(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.liveThen500["new-1/live"] = true
	y := f.client()
	out, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	if err != nil {
		t.Fatalf("RollOverBroadcast: %v; the replacement is live, so the rollover counts", err)
	}
	if f.wrote("DELETE /liveBroadcasts") != 0 {
		t.Errorf("the live replacement was deleted after a 500 that YouTube had in fact carried out")
	}
	if out.NewBroadcastID != "new-1" || !out.OldEnded {
		t.Errorf("outcome = %+v, want new-1 live and the old broadcast ended", out)
	}
}

// TestAReplacementThatDidNotGoLiveAfterAFailedTransitionIsStillRemoved is the other side of #48: when the re-read
// says the replacement is not on air, it is the unwatched event page it always was, and it is removed.
func TestAReplacementThatDidNotGoLiveAfterAFailedTransitionIsStillRemoved(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.fail500["new-1/live"] = true
	y := f.client()
	_, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	var re *RolloverError
	if !errors.As(err, &re) || re.Stage != "start" || !re.OldStillLive {
		t.Fatalf("err = %#v, want a start-stage failure with the old broadcast still live", err)
	}
	if f.wrote("DELETE /liveBroadcasts") != 1 {
		t.Errorf("deletes = %d, want the replacement removed once", f.wrote("DELETE /liveBroadcasts"))
	}
}

// TestAReplacementWhoseStateCannotBeReReadIsKeptAndNamed (#48): if the re-read fails too, nothing here knows whether
// the replacement is on air, so it is not deleted. The error names it, so a person can check it.
func TestAReplacementWhoseStateCannotBeReReadIsKeptAndNamed(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	f.fail500["new-1/live"] = true
	f.readsBeforeFail["new-1"] = 1 // the pre-transition read succeeds, the re-read fails
	y := f.client()
	_, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	var re *RolloverError
	if !errors.As(err, &re) || re.Stage != "start" || re.NewBroadcastID != "new-1" || !re.OldStillLive {
		t.Fatalf("err = %#v, want a start-stage failure naming new-1 with the old broadcast still live", err)
	}
	if f.wrote("DELETE /liveBroadcasts") != 0 {
		t.Errorf("a replacement whose state is unknown was deleted; it may be on air")
	}
}

// TestTheReplacementIsScheduledSlightlyAheadOfNow (review #61): YouTube's insert wants a scheduledStartTime in the
// future (docs-youtube B3), and req.Now is already in the past by the time the request lands. The replacement is
// scheduled ytRolloverStartLead ahead. PROVISIONAL: the size of the lead is a choice, not a documented number.
func TestTheReplacementIsScheduledSlightlyAheadOfNow(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	y := f.client()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if _, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(now)); err != nil {
		t.Fatalf("RollOverBroadcast: %v", err)
	}
	got, err := time.Parse(time.RFC3339, f.broadcasts["new-1"].scheduledStart)
	if err != nil {
		t.Fatalf("scheduledStartTime %q: %v", f.broadcasts["new-1"].scheduledStart, err)
	}
	if !got.Equal(now.Add(ytRolloverStartLead)) || !got.After(now) {
		t.Errorf("scheduledStartTime = %v, want %v (now plus the lead)", got, now.Add(ytRolloverStartLead))
	}
}

// A broadcast taking captions by HTTP POST must hand them to its replacement. closedCaptionsType cannot change once
// the replacement is testing or live (docs-youtube B13), so the replacement is created with it, with the monitor
// stream on and the old delay, and taken through testing, which that monitor stream forces, before live. Before
// this, the replacement went live with captions off and every POST to the ingestion URL showed nothing.
func TestACaptionedBroadcastRollsOverToACaptionedReplacementThroughTesting(t *testing.T) {
	f := newRolloverFake(t)
	f.liveOld()
	old := f.broadcasts["old-1"]
	old.monitor, old.captions, old.delayMs = true, ytClosedCaptionsHTTPPost, 60000
	y := f.client()
	y.testingWait = []time.Duration{0, 0}

	out, err := y.RollOverBroadcast(context.Background(), "tok", rolloverReq(time.Now()))
	if err != nil {
		t.Fatalf("rollover: %v", err)
	}
	newB := f.broadcasts[out.NewBroadcastID]
	if newB.captions != ytClosedCaptionsHTTPPost || !newB.monitor || newB.delayMs != 60000 {
		t.Fatalf("replacement captions=%q monitor=%v delay=%d, want HTTP POST, monitor on and the old 60 s delay",
			newB.captions, newB.monitor, newB.delayMs)
	}
	if newB.status != "live" || !out.OldEnded {
		t.Fatalf("replacement %+v, outcome %+v: want the replacement live and the old ended", newB, out)
	}
	want := []string{
		"POST /liveBroadcasts",
		"POST /liveBroadcasts/bind",
		"POST /liveBroadcasts/transition testing new-1",
		"POST /liveBroadcasts/transition live new-1",
		"POST /liveBroadcasts/transition complete old-1",
	}
	if strings.Join(f.writes, " | ") != strings.Join(want, " | ") {
		t.Fatalf("writes = %v, want %v", f.writes, want)
	}
}
