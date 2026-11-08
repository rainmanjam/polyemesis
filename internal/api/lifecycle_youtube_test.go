package api

// YouTube's live-now broadcast through the coordinator, against a fake YouTube that keeps broadcasts.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

const ynKey = "yn-secret-stream-key"

type ynBroadcast struct {
	status, bound, chat string
	privacy, title      string
	autoStart, autoStop any
	kids, dvr           any
	latency, projection any
	desc                any
}

// ynFake is YouTube as a destination sees it: one reusable stream holding ynKey, and the broadcasts made on it.
type ynFake struct {
	mu          sync.Mutex
	listedKey   string // the key the stream carries; "" removes the stream
	streamState string // streamStatus of the bound stream
	bcasts      map[string]*ynBroadcast
	seq         int
	calls       []string // "METHOD path" in order
	inserts     int
	transitions []string // broadcastStatus values sent
	refuseTrans string   // reason to refuse a transition with, once
	deletes     []string // ids deleted
	dropInsert  bool     // the insert creates the broadcast and then the answer never arrives
	onInsert    func()   // runs when an insert lands, before the answer
	quotaSpent  bool     // every call answers 403 quotaExceeded
}

func newYnFake() *ynFake {
	return &ynFake{listedKey: ynKey, streamState: "active", bcasts: map[string]*ynBroadcast{}}
}

func (f *ynFake) snapshot() (calls []string, inserts int, trans []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), f.inserts, append([]string(nil), f.transitions...)
}

func (f *ynFake) set(m func(*ynFake)) { f.mu.Lock(); defer f.mu.Unlock(); m(f) }

func (f *ynFake) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		if f.quotaSpent {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":403,"errors":[{"reason":"quotaExceeded"}]}}`)
			return
		}
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/liveStreams":
			if f.listedKey == "" {
				_, _ = io.WriteString(w, `{"items":[]}`)
				return
			}
			fmt.Fprintf(w, `{"items":[{"id":"stream-77","snippet":{"title":"polyemesis"},"status":{"streamStatus":%q},`+
				`"cdn":{"ingestionType":"rtmp","ingestionInfo":{"streamName":%q,"ingestionAddress":"rtmp://a/live2"}}}]}`,
				f.streamState, f.listedKey)
		case r.Method == http.MethodGet && r.URL.Path == "/liveBroadcasts":
			var items []string
			for id, b := range f.bcasts {
				if q.Get("broadcastStatus") == "active" && b.status != "live" && b.status != "testing" {
					continue
				}
				if q.Get("broadcastStatus") == "upcoming" && b.status != "created" && b.status != "ready" {
					continue
				}
				if q.Get("id") != "" && q.Get("id") != id {
					continue
				}
				items = append(items, fmt.Sprintf(`{"id":%q,"snippet":{"title":%q,"liveChatId":%q,"scheduledStartTime":%q},"status":{"lifeCycleStatus":%q},`+
					`"contentDetails":{"boundStreamId":%q,"enableAutoStart":%v,"enableAutoStop":%v,"monitorStream":{"enableMonitorStream":false}}}`,
					id, b.title, b.chat, time.Now().Add(2*time.Minute).UTC().Format(time.RFC3339), b.status, b.bound, b.autoStart == true, b.autoStop == true))
			}
			fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
		case r.Method == http.MethodPost && r.URL.Path == "/liveBroadcasts":
			var body map[string]map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.seq++
			f.inserts++
			id := fmt.Sprintf("bcast-%d", f.seq)
			f.bcasts[id] = &ynBroadcast{status: "created", chat: fmt.Sprintf("chat-%d", f.seq),
				privacy: fmt.Sprint(body["status"]["privacyStatus"]), title: fmt.Sprint(body["snippet"]["title"]),
				autoStart: body["contentDetails"]["enableAutoStart"], autoStop: body["contentDetails"]["enableAutoStop"],
				kids: body["status"]["selfDeclaredMadeForKids"], dvr: body["contentDetails"]["enableDvr"],
				latency: body["contentDetails"]["latencyPreference"], projection: body["contentDetails"]["projection"],
				desc: body["snippet"]["description"]}
			if f.onInsert != nil {
				f.onInsert()
			}
			if f.dropInsert {
				panic(http.ErrAbortHandler)
			}
			fmt.Fprintf(w, `{"id":%q,"snippet":{"liveChatId":%q},"status":{"lifeCycleStatus":"created"}}`, id, f.bcasts[id].chat)
		case r.Method == http.MethodDelete && r.URL.Path == "/liveBroadcasts":
			f.deletes = append(f.deletes, q.Get("id"))
			delete(f.bcasts, q.Get("id"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/liveBroadcasts/bind":
			if b := f.bcasts[q.Get("id")]; b != nil {
				b.bound = q.Get("streamId")
			}
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodPost && r.URL.Path == "/liveBroadcasts/transition":
			to := q.Get("broadcastStatus")
			f.transitions = append(f.transitions, to)
			if f.refuseTrans != "" {
				reason := f.refuseTrans
				f.refuseTrans = ""
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprintf(w, `{"error":{"code":403,"errors":[{"domain":"youtube.liveBroadcast","reason":%q}]}}`, reason)
				return
			}
			if b := f.bcasts[q.Get("id")]; b != nil {
				b.status = to
			}
			fmt.Fprintf(w, `{"id":%q,"status":{"lifeCycleStatus":%q}}`, q.Get("id"), to)
		default:
			t.Errorf("unexpected %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

type ynChat struct {
	mu   sync.Mutex
	sent []string
}

func (c *ynChat) point(_ int64, id string) { c.mu.Lock(); c.sent = append(c.sent, id); c.mu.Unlock() }
func (c *ynChat) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sent...)
}

func ynRow(enabled bool) *db.Destination {
	acct, src := int64(9), int64(1)
	return &db.Destination{
		ID: 3, Name: "yt main", Kind: db.DestRTMP, Platform: db.PlatformYouTube, StreamKey: ynKey,
		AccountID: &acct, SourceID: &src, Enabled: enabled,
	}
}

func ynFixture(t *testing.T, f *ynFake, rows ...*db.Destination) (*lifecycleCoordinator, *fakeLifecycleStore, *[]lifecycleFault, *ynChat) {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	store := &fakeLifecycleStore{rows: map[int64]*db.Destination{}}
	for _, r := range rows {
		cp := *r
		store.rows[r.ID] = &cp
	}
	var faults []lifecycleFault
	chat := &ynChat{}
	c := newLifecycleCoordinator(slog.New(slog.NewTextHandler(io.Discard, nil)), store,
		oauth.NewSet(oauth.WithBaseURL(srv.URL)),
		func(context.Context, int64) (*db.PlatformAccount, error) {
			return &db.PlatformAccount{ID: 9, Platform: db.PlatformYouTube, AccessToken: "tok"}, nil
		},
		func(fl lifecycleFault) { faults = append(faults, fl) })
	c.pointYouTubeChat = chat.point
	return c, store, &faults, chat
}

func sweep(c *lifecycleCoordinator, n int) {
	for i := 0; i < n; i++ {
		c.sweepOnce(context.Background(), sweepEverything)
	}
}

// An enabled YouTube destination with an account and no broadcast gets its own: list, active read, insert, bind, in
// that order, with the row recording it as opened by polyemesis, chat pointed at the insert's liveChatId, and
// nothing sent that could go live before video arrives.
func TestAnEnabledYouTubeDestinationOpensItsOwnBroadcastAndPointsChatAtIt(t *testing.T) {
	f := newYnFake()
	c, store, faults, chat := ynFixture(t, f, ynRow(true))
	sweep(c, 1)

	calls, inserts, trans := f.snapshot()
	want := []string{"GET /liveStreams", "GET /liveBroadcasts", "GET /liveBroadcasts", "POST /liveBroadcasts", "POST /liveBroadcasts/bind"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if inserts != 1 || len(trans) != 0 {
		t.Errorf("inserts=%d transitions=%v, want one insert and nothing sent towards live (auto-start does that)", inserts, trans)
	}
	b := f.bcasts["bcast-1"]
	if b.autoStart != true || b.autoStop != false || b.privacy != "public" || b.title != "yt main" || b.bound != "stream-77" {
		t.Errorf("broadcast = %+v, want auto-start on, auto-stop off, public, titled by the destination, bound", *b)
	}
	got := store.get(3)
	if got.Lifecycle.BroadcastID != "bcast-1" || !got.Lifecycle.Opened || got.Lifecycle.Phase != "created" || got.Lifecycle.Fault != "" {
		t.Errorf("lifecycle = %+v", got.Lifecycle)
	}
	if ids := chat.all(); len(ids) != 1 || ids[0] != "chat-1" {
		t.Errorf("chat was pointed at %v, want the insert response's liveChatId", ids)
	}
	if len(*faults) != 0 {
		t.Errorf("faults = %v", *faults)
	}
}

func TestAYouTubeBroadcastCarriesTheDestinationsTitlePrivacyAndCaptionSettings(t *testing.T) {
	f := newYnFake()
	row := ynRow(true)
	row.GoLiveTitle = "Friday night show"
	row.Compliance.Privacy = db.PrivacyPrivate
	c, _, _, _ := ynFixture(t, f, row)
	sweep(c, 1)
	b := f.bcasts["bcast-1"]
	if b == nil || b.title != "Friday night show" || b.privacy != "private" {
		t.Errorf("broadcast = %+v, want the go-live title and the private override", b)
	}
}

// Destinations polyemesis must not touch: a pasted key with no account, and one switched to Studio's Stream now.
func TestManualKeyAndStudioStreamNowYouTubeDestinationsAreLeftAlone(t *testing.T) {
	f := newYnFake()
	noAcct := ynRow(true)
	noAcct.AccountID = nil
	studio := ynRow(true)
	studio.ID = 4
	studio.Compliance.YouTube = &db.YouTubeBroadcastControls{StudioStreamNow: true}
	c, store, _, _ := ynFixture(t, f, noAcct, studio)
	sweep(c, 3)
	if calls, _, _ := f.snapshot(); len(calls) != 0 {
		t.Errorf("calls = %v, want none for a manual-key or Stream-now destination", calls)
	}
	if !store.get(3).Lifecycle.Empty() || !store.get(4).Lifecycle.Empty() {
		t.Error("the rows were written to")
	}
}

// A key the channel does not list is a fault that names the destination, never a new stream or a new key.
func TestAnUnlistedKeyIsAFaultAndNothingIsCreatedOrRotated(t *testing.T) {
	f := newYnFake()
	f.listedKey = ""
	c, store, faults, _ := ynFixture(t, f, ynRow(true))
	sweep(c, 2)
	calls, inserts, _ := f.snapshot()
	for _, call := range calls {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("%s was sent for a key the channel does not list", call)
		}
	}
	if inserts != 0 {
		t.Error("a broadcast was made on a stream that is not the destination's")
	}
	got := store.get(3)
	if got.Lifecycle.Fault == "" || got.Lifecycle.Attempts != 2 || strings.Contains(got.Lifecycle.Fault, ynKey) {
		t.Errorf("lifecycle = %+v, want a counted fault that does not carry the key", got.Lifecycle)
	}
	if len(*faults) != 1 || strings.Contains((*faults)[0].Detail, ynKey) {
		t.Errorf("faults = %+v, want one escalation without the key", *faults)
	}
}

// The first sweep after an upgrade finds destinations already on air through Studio. They get no second
// broadcast and no alert, and after the first sweep no more calls.
func TestAStreamAlreadyLiveInStudioIsLeftAloneQuietlyAndNotPolledAgain(t *testing.T) {
	f := newYnFake()
	f.bcasts["studio-1"] = &ynBroadcast{status: "live", bound: "stream-77", chat: "chat-s"}
	c, store, faults, _ := ynFixture(t, f, ynRow(true))
	sweep(c, 1)
	_, inserts, _ := f.snapshot()
	if inserts != 0 {
		t.Fatal("a second broadcast was made beside the live one")
	}
	got := store.get(3)
	if !strings.Contains(got.Lifecycle.Fault, "studio-1") || got.Lifecycle.Attempts < lifecycleGiveUpAfter {
		t.Errorf("lifecycle = %+v, want the sentence on the row and the destination held", got.Lifecycle)
	}
	if len(*faults) != 0 {
		t.Errorf("escalated %v: a destination that was already live must not alert at upgrade", *faults)
	}
	before, _, _ := f.snapshot()
	sweep(c, 5)
	after, _, _ := f.snapshot()
	if len(after) != len(before) {
		t.Errorf("a held destination kept calling YouTube: %d more calls", len(after)-len(before))
	}
}

// Quota: an unaffordable open sends nothing, and YouTube saying the allowance is spent pauses without burning the
// attempts budget.
func TestASpentQuotaPausesTheOpenWithoutBurningTheBudget(t *testing.T) {
	f := newYnFake()
	c, store, faults, _ := ynFixture(t, f, ynRow(true))
	afford := false
	c.quotaAfford = func(cost int) bool {
		if cost != ytOpenUnits {
			t.Errorf("the open priced itself at %d units, want %d", cost, ytOpenUnits)
		}
		return afford
	}
	paused := 0
	c.quotaPause = func() { paused++ }
	sweep(c, 3)
	if calls, _, _ := f.snapshot(); len(calls) != 0 {
		t.Errorf("calls = %v, want none while the allowance cannot pay", calls)
	}
	if got := store.get(3); got.Lifecycle.Attempts != 0 || !strings.Contains(got.Lifecycle.Fault, "quota") {
		t.Errorf("lifecycle = %+v, want a quota sentence and no attempts burned", got.Lifecycle)
	}
	afford = true
	f.set(func(f *ynFake) { f.quotaSpent = true })
	sweep(c, 3)
	if paused == 0 {
		t.Error("YouTube said the quota is spent and the install's allowance was not paused")
	}
	if got := store.get(3); got.Lifecycle.Attempts != 0 {
		t.Errorf("attempts = %d: a spent allowance must not burn the give-up budget", got.Lifecycle.Attempts)
	}
	if len(*faults) != 0 {
		t.Errorf("a quota pause alerted: %v", *faults)
	}
}

// Switching the destination off ends the broadcast polyemesis opened (complete), tells chat, and a reconnect gap
// never does: the fake only ever sees `complete` after the destination was disabled.
func TestSwitchingTheDestinationOffEndsItsOwnBroadcastAndClearsChat(t *testing.T) {
	f := newYnFake()
	c, store, _, chat := ynFixture(t, f, ynRow(true))
	sweep(c, 1) // opens bcast-1
	f.set(func(f *ynFake) { f.bcasts["bcast-1"].status = "live" })
	// Still enabled: a sweep must not complete it, however long the encoder was away.
	c.forgetLiveRecheck(3)
	sweep(c, 2)
	if _, _, trans := f.snapshot(); len(trans) != 0 {
		t.Fatalf("transitions %v sent while the destination was on", trans)
	}
	store.mu.Lock()
	store.rows[3].Enabled = false
	store.mu.Unlock()
	sweep(c, 1)
	if _, _, trans := f.snapshot(); len(trans) != 1 || trans[0] != "complete" {
		t.Fatalf("transitions = %v, want one complete", trans)
	}
	if ids := chat.all(); len(ids) < 2 || ids[len(ids)-1] != "" {
		t.Errorf("chat pointers = %v, want the chat cleared after the end", ids)
	}
}

// When the opened broadcast completes while the destination is still on (Studio's End), the next one is opened.
func TestAFinishedOwnBroadcastIsRenewedWhileTheDestinationIsStillOn(t *testing.T) {
	f := newYnFake()
	c, store, _, chat := ynFixture(t, f, ynRow(true))
	sweep(c, 1)
	f.set(func(f *ynFake) { f.bcasts["bcast-1"].status = "complete" })
	c.forgetLiveRecheck(3)
	sweep(c, 1)
	got := store.get(3)
	if got.Lifecycle.BroadcastID != "bcast-2" || !got.Lifecycle.Opened {
		t.Fatalf("lifecycle = %+v, want the next broadcast recorded", got.Lifecycle)
	}
	if ids := chat.all(); ids[len(ids)-1] != "chat-2" {
		t.Errorf("chat pointers = %v, want the new broadcast's chat last", ids)
	}
}

// An ANNOUNCED broadcast that finishes is still a fault, as before: only polyemesis's own is renewed.
func TestAFinishedAnnouncedBroadcastIsStillAFaultNotARenewal(t *testing.T) {
	f := newYnFake()
	f.bcasts["ann-1"] = &ynBroadcast{status: "complete", bound: "stream-77"}
	row := ynRow(true)
	row.Facebook.BroadcastID = "ann-1"
	row.Lifecycle = db.BroadcastControl{BroadcastID: "ann-1", Phase: "live"} // adopted from the mirror, never Opened
	c, store, _, _ := ynFixture(t, f, row)
	sweep(c, 1)
	if _, inserts, _ := f.snapshot(); inserts != 0 {
		t.Error("an announced broadcast was renewed")
	}
	if got := store.get(3); got.Lifecycle.Fault == "" || !got.Lifecycle.Terminal {
		t.Errorf("lifecycle = %+v, want the completed-broadcast fault", got.Lifecycle)
	}
}

// A renewal that fails keeps the finished broadcast's id on the row. Wiping it would let the announcement mirror's
// id come back through ActiveBroadcastID.
func TestAFailedRenewalKeepsTheFinishedIdAndNeverAdoptsTheAnnouncementMirror(t *testing.T) {
	f := newYnFake()
	c, store, _, _ := ynFixture(t, f, ynRow(true))
	sweep(c, 1)
	store.mu.Lock()
	store.rows[3].Facebook.BroadcastID = "stale-announced" // a schedule's announcement mirror appears later
	store.mu.Unlock()
	f.set(func(f *ynFake) { f.bcasts["bcast-1"].status = "complete"; f.listedKey = "" })
	c.forgetLiveRecheck(3)
	sweep(c, 3)
	got := store.get(3)
	if got.Lifecycle.BroadcastID != "bcast-1" || !got.Lifecycle.Opened {
		t.Errorf("lifecycle = %+v, want bcast-1 kept after the failed renewal", got.Lifecycle)
	}
	if got.Lifecycle.Fault == "" || got.Lifecycle.Attempts == 0 {
		t.Errorf("lifecycle = %+v, want the failure counted", got.Lifecycle)
	}
	if _, inserts, _ := f.snapshot(); inserts != 1 {
		t.Errorf("inserts = %d, want only the first", inserts)
	}
}

// YouTube moving the broadcast on its own (auto-start) while polyemesis also sends: a refusal that the next read
// shows was only a race clears the fault and the count.
func TestAnInvalidTransitionAutoStartCausedClearsOnTheNextRead(t *testing.T) {
	f := newYnFake()
	c, store, _, _ := ynFixture(t, f, ynRow(true))
	sweep(c, 1)
	f.set(func(f *ynFake) { f.refuseTrans = "invalidTransition" })
	sweep(c, 1) // created + active stream: polyemesis sends live, YouTube refuses
	if got := store.get(3); got.Lifecycle.Attempts != 1 {
		t.Fatalf("lifecycle = %+v, want the refusal counted once", got.Lifecycle)
	}
	f.set(func(f *ynFake) { f.bcasts["bcast-1"].status = "live" }) // auto-start got there first
	sweep(c, 1)
	if got := store.get(3); got.Lifecycle.Attempts != 0 || got.Lifecycle.Fault != "" || got.Lifecycle.Phase != "live" {
		t.Errorf("lifecycle = %+v, want a clean live row", got.Lifecycle)
	}
}

// A picked event with auto-start OFF still goes live: polyemesis does the testing/live transition itself once video
// flows, and records the event as its own so a switch-off ends it. The event's own flags are not changed.
func TestAPickedEventWithAutoStartOffIsTakenLiveByPolyemesisAndNothingIsCreated(t *testing.T) {
	f := newYnFake()
	f.bcasts["ev-1"] = &ynBroadcast{status: "ready", chat: "chat-ev", title: "Friday", autoStart: false}
	row := ynRow(true)
	row.Compliance.YouTube = &db.YouTubeBroadcastControls{EventID: "ev-1"}
	c, store, _, chat := ynFixture(t, f, row)
	sweep(c, 1)
	if _, inserts, _ := f.snapshot(); inserts != 0 {
		t.Fatal("a broadcast was created although an event was picked")
	}
	got := store.get(3)
	if got.Lifecycle.BroadcastID != "ev-1" || !got.Lifecycle.Opened {
		t.Fatalf("lifecycle = %+v, want the picked event recorded as polyemesis's own", got.Lifecycle)
	}
	if f.bcasts["ev-1"].bound != "stream-77" {
		t.Errorf("the event was not bound to the destination's stream: %+v", *f.bcasts["ev-1"])
	}
	if ids := chat.all(); len(ids) != 1 || ids[0] != "chat-ev" {
		t.Errorf("chat pointers = %v, want the event's own chat", ids)
	}
	sweep(c, 1) // video is flowing (stream active): the coordinator moves it
	if _, _, trans := f.snapshot(); len(trans) == 0 || trans[len(trans)-1] != "live" {
		t.Errorf("transitions = %v, want polyemesis to take the auto-start-off event live", trans)
	}
	if f.bcasts["ev-1"].autoStart != false {
		t.Error("the event's own auto-start flag was changed")
	}
}

// After a picked event completes, the destination falls back to creating broadcasts and never reuses it.
func TestAfterThePickedEventCompletesTheDestinationFallsBackToCreating(t *testing.T) {
	f := newYnFake()
	f.bcasts["ev-1"] = &ynBroadcast{status: "ready", chat: "chat-ev"}
	row := ynRow(true)
	row.Compliance.YouTube = &db.YouTubeBroadcastControls{EventID: "ev-1"}
	c, store, _, _ := ynFixture(t, f, row)
	sweep(c, 1)
	f.set(func(f *ynFake) { f.bcasts["ev-1"].status = "complete" })
	c.forgetLiveRecheck(3)
	sweep(c, 1)
	got := store.get(3)
	if got.Lifecycle.BroadcastID == "ev-1" || got.Lifecycle.BroadcastID == "" {
		t.Fatalf("lifecycle = %+v, want a new broadcast and the finished event left behind", got.Lifecycle)
	}
	if _, inserts, _ := f.snapshot(); inserts != 1 {
		t.Errorf("inserts = %d, want one new broadcast", inserts)
	}
}

// The create fields come from the destination: made-for-kids from compliance, latency and DVR from the controls.
func TestTheDestinationsCreateFieldsReachTheInsert(t *testing.T) {
	f := newYnFake()
	kids := true
	dvr := false
	row := ynRow(true)
	row.Compliance.MadeForKids = &kids
	row.Compliance.YouTube = &db.YouTubeBroadcastControls{
		LatencyPreference: "low", EnableDvr: &dvr, Description: "About", Projection360: true}
	// the fake records the insert body's status and details via ynBroadcast; reuse its title/privacy plus a probe
	c, _, _, _ := ynFixture(t, f, row)
	sweep(c, 1)
	b := f.bcasts["bcast-1"]
	if b == nil || b.kids != true || b.latency != "low" || b.dvr != false || b.projection != "360" || b.desc != "About" {
		t.Errorf("broadcast = %+v, want the destination's fields on the insert", b)
	}
}

// A scheduled show's options come from the same stored settings, and only for YouTube.
func TestAScheduledShowGetsTheDestinationsCreateFieldsOnlyOnYouTube(t *testing.T) {
	kids := false
	dvr := true
	row := ynRow(true)
	row.Compliance.MadeForKids = &kids
	row.Compliance.YouTube = &db.YouTubeBroadcastControls{Description: "D", LatencyPreference: "low", EnableDvr: &dvr, Projection360: true}
	got := ingestOptionsFor(row, time.Time{}).YouTubeEvent
	if got.Description != "D" || got.Latency != "low" || got.EnableDvr == nil || !got.Projection360 || got.MadeForKids == nil {
		t.Errorf("event fields = %+v", got)
	}
	row.Platform = db.PlatformTwitch
	if f := ingestOptionsFor(row, time.Time{}).YouTubeEvent; f != (oauth.EventFields{}) {
		t.Errorf("a non-YouTube destination produced event fields: %+v", f)
	}
}

// A broadcast polyemesis opened that never went live, on a destination then switched off, is read ONCE after the
// switch-off and then left alone. Without that bound each 15 s sweep spends two quota units for the rest of the
// daemon's life (the arithmetic lifecycle.go's skipStateRead spells out), and re-enabling must still find it.
func TestAnOpenedBroadcastThatNeverAiredIsNotReadForEverAfterTheSwitchOff(t *testing.T) {
	f := newYnFake()
	c, store, _, _ := ynFixture(t, f, ynRow(true))
	sweep(c, 1) // opens bcast-1, phase created
	store.mu.Lock()
	store.rows[3].Enabled = false
	store.mu.Unlock()
	sweep(c, 2)
	before, _, trans := f.snapshot()
	sweep(c, 50)
	after, _, _ := f.snapshot()
	if len(after) != len(before) {
		t.Errorf("%d more YouTube calls over 50 sweeps of a switched-off, never-aired broadcast", len(after)-len(before))
	}
	if len(trans) != 0 {
		t.Errorf("transitions = %v: a never-aired broadcast has nothing to end", trans)
	}
	// Switching it on again reuses the broadcast: no second insert.
	store.mu.Lock()
	store.rows[3].Enabled = true
	store.mu.Unlock()
	sweep(c, 1)
	if _, inserts, _ := f.snapshot(); inserts != 1 {
		t.Errorf("inserts = %d after re-enabling, want the same broadcast reused", inserts)
	}
	if store.get(3).Lifecycle.BroadcastID != "bcast-1" {
		t.Errorf("lifecycle = %+v", store.get(3).Lifecycle)
	}
}

// Auto-start getting there first must not alert: the refusal is a race, and the next read clears it.
func TestAnAutoStartRaceRefusalRaisesNoAlertOnAShowThatIsLive(t *testing.T) {
	f := newYnFake()
	c, _, faults, _ := ynFixture(t, f, ynRow(true))
	sweep(c, 1)
	f.set(func(f *ynFake) { f.refuseTrans = "invalidTransition" })
	sweep(c, 1)
	f.set(func(f *ynFake) { f.bcasts["bcast-1"].status = "live" })
	sweep(c, 1)
	if len(*faults) != 0 {
		t.Errorf("faults = %+v: an auto-start race alerted on a show that is live", *faults)
	}
}

func (f *ynFake) deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deletes...)
}

// ITEM 1. A broadcast polyemesis opened that has been ended on a switched-off destination is forgotten, so the
// announcement mirror drives the next scheduled show instead of the finished id.
func TestAnEndedOwnBroadcastIsForgottenSoTheAnnouncementMirrorDrivesTheNextShow(t *testing.T) {
	f := newYnFake()
	c, store, _, _ := ynFixture(t, f, ynRow(true))
	sweep(c, 1)
	f.set(func(f *ynFake) { f.bcasts["bcast-1"].status = "live" })
	c.forgetLiveRecheck(3)
	sweep(c, 1)
	store.mu.Lock()
	store.rows[3].Enabled = false
	store.rows[3].Facebook.BroadcastID = "ann-1" // the next scheduled show's announcement
	store.mu.Unlock()
	sweep(c, 3) // end, then the terminal row is let go
	got := store.get(3)
	if got.Lifecycle.BroadcastID != "" || got.Lifecycle.Opened {
		t.Fatalf("lifecycle = %+v, want the ended broadcast forgotten", got.Lifecycle)
	}
	if got.ActiveBroadcastID() != "ann-1" {
		t.Errorf("active broadcast = %q, want the announced show", got.ActiveBroadcastID())
	}
}

// After a scheduled show has completed, switching the destination on again opens a live-now broadcast instead of
// stopping at the completed-broadcast fault.
func TestAdHocGoLiveAfterACompletedScheduledShowOpensALiveNowBroadcast(t *testing.T) {
	f := newYnFake()
	f.bcasts["ann-1"] = &ynBroadcast{status: "complete", bound: "stream-77"}
	row := ynRow(true)
	row.Facebook.BroadcastID = "ann-1"
	c, store, _, _ := ynFixture(t, f, row)
	sweep(c, 1)
	got := store.get(3)
	if _, inserts, _ := f.snapshot(); inserts != 1 || got.Lifecycle.BroadcastID != "bcast-1" || !got.Lifecycle.Opened {
		t.Errorf("inserts=%d lifecycle=%+v, want a live-now broadcast after the completed scheduled one", inserts, got.Lifecycle)
	}
}

// ITEM 2. Switching to Studio's Stream now stops polyemesis driving the broadcast it opened.
func TestSwitchingToStudioStreamNowReleasesTheBroadcastPolyemesisOpened(t *testing.T) {
	f := newYnFake()
	c, store, _, chat := ynFixture(t, f, ynRow(true))
	sweep(c, 1)
	store.mu.Lock()
	store.rows[3].Compliance.YouTube = &db.YouTubeBroadcastControls{StudioStreamNow: true}
	store.mu.Unlock()
	sweep(c, 3)
	if _, _, trans := f.snapshot(); len(trans) != 0 {
		t.Errorf("transitions %v sent for a destination that now uses Stream now", trans)
	}
	if got := store.get(3); got.Lifecycle.BroadcastID != "" || got.Lifecycle.Opened {
		t.Errorf("lifecycle = %+v, want the record cleared", got.Lifecycle)
	}
	if d := f.deleted(); len(d) != 1 || d[0] != "bcast-1" {
		t.Errorf("deleted = %v, want the never-aired broadcast polyemesis created removed", d)
	}
	if ids := chat.all(); ids[len(ids)-1] != "" {
		t.Errorf("chat pointers = %v, want chat released", ids)
	}
}

func TestSwitchingToStudioStreamNowLeavesALiveBroadcastAloneAndForgetsIt(t *testing.T) {
	f := newYnFake()
	c, store, _, _ := ynFixture(t, f, ynRow(true))
	sweep(c, 1)
	f.set(func(f *ynFake) { f.bcasts["bcast-1"].status = "live" })
	store.mu.Lock()
	store.rows[3].Compliance.YouTube = &db.YouTubeBroadcastControls{StudioStreamNow: true}
	store.mu.Unlock()
	sweep(c, 3)
	if d := f.deleted(); len(d) != 0 {
		t.Errorf("deleted %v: a broadcast that may be live is never deleted", d)
	}
	if _, _, trans := f.snapshot(); len(trans) != 0 {
		t.Errorf("transitions %v", trans)
	}
	if got := store.get(3); got.Lifecycle.BroadcastID != "" {
		t.Errorf("lifecycle = %+v", got.Lifecycle)
	}
}

// ITEM 3. A broadcast polyemesis CREATED that never aired is deleted on switch-off; an adopted event never is.
func TestACreatedBroadcastThatNeverAiredIsDeletedOnSwitchOffAndAnAdoptedEventNeverIs(t *testing.T) {
	f := newYnFake()
	c, store, _, _ := ynFixture(t, f, ynRow(true))
	sweep(c, 1)
	store.mu.Lock()
	store.rows[3].Enabled = false
	store.mu.Unlock()
	sweep(c, 5)
	if d := f.deleted(); len(d) != 1 || d[0] != "bcast-1" {
		t.Errorf("deleted = %v, want the never-aired broadcast removed once", d)
	}
	if got := store.get(3); got.Lifecycle.BroadcastID != "" {
		t.Errorf("lifecycle = %+v, want it cleared", got.Lifecycle)
	}
	before, _, _ := f.snapshot()
	sweep(c, 20)
	if after, _, _ := f.snapshot(); len(after) != len(before) {
		t.Error("a deleted broadcast kept being asked about")
	}

	f2 := newYnFake()
	f2.bcasts["ev-1"] = &ynBroadcast{status: "ready", chat: "chat-ev"}
	row := ynRow(true)
	row.Compliance.YouTube = &db.YouTubeBroadcastControls{EventID: "ev-1"}
	c2, store2, _, _ := ynFixture(t, f2, row)
	sweep(c2, 1)
	store2.mu.Lock()
	store2.rows[3].Enabled = false
	store2.mu.Unlock()
	sweep(c2, 5)
	if d := f2.deleted(); len(d) != 0 {
		t.Errorf("deleted %v: a picked event must never be deleted", d)
	}
}

// ITEM 4a. If the record of a just-created broadcast cannot be written, the broadcast is deleted rather than orphaned.
func TestABroadcastWhoseRecordCannotBeWrittenIsDeleted(t *testing.T) {
	f := newYnFake()
	c, store, _, _ := ynFixture(t, f, ynRow(true))
	f.set(func(f *ynFake) { f.onInsert = func() { store.mu.Lock(); store.failUpdates = true; store.mu.Unlock() } })
	sweep(c, 1)
	if d := f.deleted(); len(d) != 1 || d[0] != "bcast-1" {
		t.Errorf("deleted = %v, want the unrecorded broadcast removed", d)
	}
}

// ITEM 4b. An insert whose answer never arrives still created the broadcast; the next attempt finds and reuses it
// instead of making another.
func TestAnInsertWhoseAnswerWasLostIsReusedNotRepeated(t *testing.T) {
	f := newYnFake()
	c, store, _, _ := ynFixture(t, f, ynRow(true))
	f.set(func(f *ynFake) { f.dropInsert = true })
	sweep(c, 1)
	f.set(func(f *ynFake) { f.dropInsert = false })
	sweep(c, 1)
	if _, inserts, _ := f.snapshot(); inserts != 1 {
		t.Errorf("inserts = %d, want the lost insert reused", inserts)
	}
	got := store.get(3)
	if got.Lifecycle.BroadcastID != "bcast-1" || f.bcasts["bcast-1"].bound != "stream-77" {
		t.Errorf("lifecycle=%+v bound=%q, want bcast-1 recorded and bound", got.Lifecycle, f.bcasts["bcast-1"].bound)
	}
}

// ITEM 5. A broadcast waiting for video is read on the slow cadence, not every sweep.
func TestABroadcastWaitingForVideoIsReadOnTheSlowCadence(t *testing.T) {
	f := newYnFake()
	f.streamState = "inactive"
	c, _, _, _ := ynFixture(t, f, ynRow(true))
	sweep(c, 1)
	start, _, _ := f.snapshot()
	const sweepsPerHour = 240
	sweep(c, sweepsPerHour)
	end, _, _ := f.snapshot()
	calls := len(end) - len(start)
	if limit := (sweepsPerHour/lifecycleLiveRecheckEvery + 2) * 2; calls > limit {
		t.Errorf("%d YouTube calls in an hour of waiting for video, want at most %d (every %d sweeps)", calls, limit, lifecycleLiveRecheckEvery)
	}
}
