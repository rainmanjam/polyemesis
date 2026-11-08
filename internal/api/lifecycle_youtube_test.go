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

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

const ynKey = "yn-secret-stream-key"

type ynBroadcast struct {
	status, bound, chat string
	privacy, title      string
	autoStart, autoStop any
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
				if q.Get("id") != "" && q.Get("id") != id {
					continue
				}
				items = append(items, fmt.Sprintf(`{"id":%q,"snippet":{"title":%q,"liveChatId":%q},"status":{"lifeCycleStatus":%q},`+
					`"contentDetails":{"boundStreamId":%q,"monitorStream":{"enableMonitorStream":false}}}`,
					id, b.title, b.chat, b.status, b.bound))
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
				autoStart: body["contentDetails"]["enableAutoStart"], autoStop: body["contentDetails"]["enableAutoStop"]}
			fmt.Fprintf(w, `{"id":%q,"snippet":{"liveChatId":%q},"status":{"lifeCycleStatus":"created"}}`, id, f.bcasts[id].chat)
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
	want := []string{"GET /liveStreams", "GET /liveBroadcasts", "POST /liveBroadcasts", "POST /liveBroadcasts/bind"}
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
