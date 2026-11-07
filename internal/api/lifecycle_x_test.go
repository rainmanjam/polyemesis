package api

// The X half of the broadcast lifecycle coordinator. X's Go Live (POST /2/broadcasts)
// creates AND publishes in one call and is public the moment it returns, so the
// coordinator "opens" a destination by recording a pending handle (nothing exists on
// X), takes the source live only once bytes are arriving (and records the REAL
// broadcast id then), ENDS the broadcast when the operator switches the destination
// off, and RENEWS when the operator goes on air again after a finished show. None of
// the YouTube tests in lifecycle_test.go cover those words, so this file covers them.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// xcFakeKey is the stream source key the fake X hands out. It is built at runtime
// so the fixture is not shaped like a real credential, and the leak test looks
// for this exact string in everything the coordinator records.
var xcFakeKey = strings.Join([]string{"polyemesis", "fixture", "source", "07"}, "")

// xcSourceID is the fake source's id: 19 digits, and NOT the stream key. xcPending is the handle the coordinator holds
// for it until the source is taken live; it is oauth's "pending:" + the source id, hard-coded here so a drift in that
// format fails these tests.
const (
	xcSourceID = "7100000000000000001"
	xcPending  = "pending:" + xcSourceID
)

// xcOldRoute matches the routes X removes on 2027-01-01. The fake fails the test on any request to one.
var xcOldRoute = regexp.MustCompile(`^/2/users/[^/]+/(sources|broadcasts)(/|$)`)

// xcFake is the X Livestream API as the coordinator sees it, on the /2/sources and /2/broadcasts routes: one account,
// one source, and a broadcast whose state the test moves by hand.
type xcFake struct {
	mu sync.Mutex
	// state is the current broadcast's X state word. Empty means none exists yet.
	state string
	id    string
	// active is whether the source is receiving bytes (is_stream_active).
	active bool
	// opened records the source id of every go-live (POST /2/broadcasts) call, in order.
	opened []string
	// published and ended count the writes that reached the wire: new broadcasts taken live, and ENDs.
	published int
	ended     int
	// publishBodies records the raw body of every go-live call, so a test can read the title X was sent.
	publishBodies []map[string]any
	// createStatus, when non-zero, makes the stream-key read (the one call opening a destination makes that X can
	// refuse) fail with that HTTP status.
	createStatus int
	// createDetail, when set, is the problem detail a refused read carries.
	createDetail string
	// creates counts every stream-key read, refused or not: one per attempt to open a destination.
	creates int
	// liveStatus, when non-zero, makes a go-live call fail with that HTTP status. liveBody is the problem body; empty
	// means the busy-source 409.
	liveStatus int
	liveBody   string
	// listLag makes GET /2/broadcasts leave out a live broadcast, as a list that has not caught up would.
	listLag bool
	// sourceDeleted makes GET /2/sources answer with no sources, as after the source was deleted on X.
	sourceDeleted bool
	// srcID and key override the source id and stream key the fake holds (empty means xcSourceID and xcFakeKey), so a
	// test can replace the source as a reconnect would.
	srcID, key string
	// createdMS is created_at_ms of the live broadcast the list reports; zero means long ago (somebody else's).
	createdMS int64
	// failReads makes GET /2/sources and GET /2/broadcasts answer 500.
	failReads bool
	// skew is how far X's clock (its Date header, and the created_at_ms it stamps) is from the real one.
	skew time.Duration
	// noDate suppresses the Date header, so X's clock cannot be read.
	noDate bool
	// dropLive makes POST /2/broadcasts create the broadcast and then drop the connection: the answer is lost.
	dropLive bool
	// liveCalls counts every go-live call, refused or not.
	liveCalls int
	// seq numbers broadcasts so a second show gets a different id.
	seq int
	// limited makes every call answer 429 with a reset a minute ahead; requests counts every call that reached X.
	limited  bool
	requests int
	// sourceGone makes a broadcast read carry no source_id, so whether the encoder is sending is unknown.
	sourceGone bool
}

func (f *xcFake) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Date", time.Now().Add(f.skew).UTC().Format(http.TimeFormat))
		if f.noDate {
			w.Header()["Date"] = nil
		}
		f.requests++
		if f.limited {
			w.Header().Set("x-rate-limit-reset", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"title":"Too Many Requests","detail":"slow down"}`)
			return
		}
		path := r.URL.Path
		if xcOldRoute.MatchString(path) {
			t.Errorf("request %s %s hit a user-scoped route X removes on 2027-01-01", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch {
		case path == "/2/users/me":
			_, _ = io.WriteString(w, `{"data":{"id":"42","username":"streamer"}}`)
		case r.Method == http.MethodGet && (path == "/2/sources" || path == "/2/broadcasts") && f.failReads:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"title":"Internal Server Error"}`)
		case r.Method == http.MethodGet && path == "/2/sources":
			active := "false"
			if f.active {
				active = "true"
			}
			if f.sourceDeleted {
				_, _ = io.WriteString(w, `{"data":[],"meta":{"result_count":0}}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"`+f.sourceID()+`","name":"polyemesis","is_stream_active":`+active+
				`}],"meta":{"result_count":1}}`)
		case r.Method == http.MethodGet && path == "/2/broadcasts":
			live := (f.state == oauth.XStateRunning || f.state == oauth.XStatePrePublished) && !f.listLag
			if !live {
				_, _ = io.WriteString(w, `{"data":[],"meta":{"result_count":0}}`)
				return
			}
			created := f.createdMS
			if created == 0 {
				created = 1000 // long ago: not started by this run
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"`+f.id+`","broadcast_id":"`+f.id+`","state":"`+f.state+
				`","source_id":"`+xcSourceID+`","title":"","created_at_ms":"`+strconv.FormatInt(created, 10)+`"}],"meta":{"result_count":1}}`)
		case r.Method == http.MethodGet && path == "/2/sources/"+f.sourceID()+"/stream_key":
			f.creates++
			if f.createStatus != 0 {
				detail := "not permitted"
				if f.createDetail != "" {
					detail = f.createDetail
				}
				w.WriteHeader(f.createStatus)
				_, _ = io.WriteString(w, `{"errors":[{"title":"Unauthorized","detail":"`+detail+`","status":`+
					`401}],"detail":"`+detail+`"}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":{"id":"`+f.sourceID()+`","stream_key":"`+f.streamKey()+`"}}`)
		case r.Method == http.MethodPost && path == "/2/broadcasts":
			f.liveCalls++
			if f.liveStatus != 0 {
				w.WriteHeader(f.liveStatus)
				body := f.liveBody
				if body == "" {
					body = `{"type":"https://api.x.com/2/problems/conflict","title":"Conflict",` +
						`"detail":"Another broadcast is live on this source.","status":409}`
				}
				_, _ = io.WriteString(w, body)
				return
			}
			var raw map[string]any
			_ = json.NewDecoder(r.Body).Decode(&raw)
			for k := range raw {
				switch k {
				case "source_id", "title", "locale", "chat_option", "should_not_tweet":
				default:
					t.Errorf("go-live carried %q, a field POST /2/broadcasts rejects", k)
				}
			}
			sid, _ := raw["source_id"].(string)
			if sid != xcSourceID {
				t.Errorf("go-live source_id = %q, want the source id %s (not the stream key)", sid, xcSourceID)
			}
			f.opened = append(f.opened, sid)
			f.publishBodies = append(f.publishBodies, raw)
			// X returns the broadcast already live on the source (200), unchanged. Only a finished or absent
			// broadcast makes way for a new one (201).
			if f.state == oauth.XStateRunning || f.state == oauth.XStatePrePublished {
				_, _ = io.WriteString(w, xcBroadcastJSON(f.id, f.state))
				return
			}
			f.seq++
			f.id = "bc-" + string(rune('0'+f.seq))
			f.state = oauth.XStateRunning
			f.published++
			f.createdMS = time.Now().Add(f.skew).UnixMilli()
			if f.dropLive {
				panic(http.ErrAbortHandler) // created, and the answer never arrives
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, xcBroadcastJSON(f.id, f.state))
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/2/broadcasts/"):
			id := strings.TrimPrefix(path, "/2/broadcasts/")
			if strings.HasPrefix(id, "pending") {
				t.Errorf("the pending handle %q was sent to X as a broadcast id", id)
			}
			if id != f.id {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"errors":[{"title":"Not Found","detail":"no such broadcast"}]}`)
				return
			}
			src := xcSourceID
			if f.sourceGone {
				src = ""
			}
			created := f.createdMS
			if created == 0 {
				created = 1000
			}
			_, _ = io.WriteString(w, `{"data":{"id":"`+f.id+`","state":"`+f.state+`","source_id":"`+
				src+`","title":"","created_at_ms":"`+strconv.FormatInt(created, 10)+`"}}`)
		case r.Method == http.MethodPut && path == "/2/broadcasts/"+f.id+"/state":
			var raw map[string]any
			_ = json.NewDecoder(r.Body).Decode(&raw)
			if len(raw) != 1 || raw["state"] != "END" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"errors":[{"title":"Bad Request","detail":"only END is supported"}]}`)
				return
			}
			f.ended++
			f.state = oauth.XStateEnded
			_, _ = io.WriteString(w, `{"data":{"id":"`+f.id+`","state":"ENDED"}}`)
		default:
			t.Errorf("unexpected X request %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (f *xcFake) sourceID() string {
	if f.srcID != "" {
		return f.srcID
	}
	return xcSourceID
}

func (f *xcFake) streamKey() string {
	if f.key != "" {
		return f.key
	}
	return xcFakeKey
}

func (f *xcFake) createCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates
}

func xcBroadcastJSON(id, state string) string {
	return `{"data":{"id":"` + id + `","broadcast_id":"` + id + `","state":"` + state +
		`","share_url":"https://x.com/i/broadcasts/` + id + `"}}`
}

func (f *xcFake) set(mutate func(*xcFake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mutate(f)
}

func (f *xcFake) snapshot() (opened []string, published, ended int, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opened...), f.published, f.ended, f.state
}

// xcRow is an X destination on the stream source. It is enabled, which is the
// operator's intent to be on air, and it carries the minted source key.
func xcRow(enabled bool, lc db.BroadcastControl) *db.Destination {
	acct := int64(9)
	src := int64(1)
	return &db.Destination{
		ID: 5, Name: "x main", Kind: db.DestRTMP, Platform: db.PlatformX,
		AccountID: &acct, SourceID: &src, Enabled: enabled, StreamKey: xcFakeKey,
		Lifecycle: lc,
	}
}

// xcFixture wires the coordinator to the fake X with the Livestream gate open.
// The gate rides the provider set, so it is passed to NewSet.
func xcFixture(t *testing.T, fake *xcFake, gateOpen bool, rows ...*db.Destination) (*lifecycleCoordinator, *fakeLifecycleStore, *[]lifecycleFault) {
	t.Helper()
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)

	store := &fakeLifecycleStore{rows: map[int64]*db.Destination{}}
	for _, r := range rows {
		cp := *r
		store.rows[r.ID] = &cp
	}
	var faults []lifecycleFault
	c := newLifecycleCoordinator(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		store,
		oauth.NewSet(oauth.WithBaseURL(srv.URL), oauth.WithXLivestream(gateOpen)),
		func(context.Context, int64) (*db.PlatformAccount, error) {
			return &db.PlatformAccount{ID: 9, Platform: db.PlatformX, AccessToken: "tok"}, nil
		},
		func(f lifecycleFault) { faults = append(faults, f) },
	)
	return c, store, &faults
}

// xcSweep runs one sweep over the stored row, the way the 15-second loop does.
func xcSweep(t *testing.T, c *lifecycleCoordinator, store *fakeLifecycleStore) *db.Destination {
	t.Helper()
	store.mu.Lock()
	cp := *store.rows[5]
	store.mu.Unlock()
	c.considerRow(context.Background(), &cp, sweepEverything)
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.rows[5]
}

// -------------------------------------------------------------- THE PLANNER

// Every documented X state is planned, and none of them falls into the
// unknown-phase path that escalates. Enabled and disabled are both covered.
//
// The fault this prevents is not a hypothetical one: X's words are upper case, and
// before this file the coordinator treated anything it did not recognise as a
// fault on every sweep.
func TestXEveryDocumentedStateIsPlannedAndNoneIsUnknown(t *testing.T) {
	states := []string{oauth.XStateNotStarted, oauth.XStatePrePublished, oauth.XStateRunning,
		oauth.XStateEnded, oauth.XStateTimedOut, oauth.XStateCanceled}
	for _, enabled := range []bool{true, false} {
		for _, s := range states {
			for _, active := range []*bool{nil, boolPtr(false), boolPtr(true)} {
				st := &oauth.BroadcastLifecycleState{Status: s, StreamActive: active}
				plan := planLifecycle(enabled, st)
				if plan.Unknown {
					t.Errorf("enabled=%v state %s active=%v: planned as unknown (%q), which escalates", enabled, s, active, plan.Wait)
				}
				if plan.Send == oauth.PhaseTesting {
					t.Errorf("enabled=%v state %s: sent testing, which X has no state for", enabled, s)
				}
			}
		}
	}
}

// A NOT_STARTED broadcast goes live only once bytes arrive. Publishing an empty
// source would put an empty show in front of an audience.
func TestXNotStartedWaitsForBytesBeforeItGoesLive(t *testing.T) {
	off := planLifecycle(true, &oauth.BroadcastLifecycleState{Status: oauth.XStateNotStarted, StreamActive: boolPtr(false)})
	if off.Send != "" || off.Wait == "" {
		t.Errorf("no bytes: plan = %+v, want nothing sent and a reason to wait", off)
	}
	unknown := planLifecycle(true, &oauth.BroadcastLifecycleState{Status: oauth.XStateNotStarted})
	if unknown.Send != "" {
		t.Errorf("unknown ingest: plan = %+v, want nothing sent", unknown)
	}
	on := planLifecycle(true, &oauth.BroadcastLifecycleState{Status: oauth.XStateNotStarted, StreamActive: boolPtr(true)})
	if on.Send != oauth.PhaseLive {
		t.Errorf("bytes arriving: Send = %q, want live", on.Send)
	}
}

// A finished X broadcast on an enabled destination is a second show, not a
// fault. The plan asks for a renewal; the YouTube behaviour (fault) is unchanged.
//
// Only once the encoder has stopped: an END with bytes still arriving is the
// operator's emergency stop in the X app, and renewing then would publish a new
// public broadcast within a sweep. CANCELED is never renewed by itself.
func TestXFinishedBroadcastWhileEnabledRenewsRatherThanFaults(t *testing.T) {
	for _, s := range []string{oauth.XStateEnded, oauth.XStateTimedOut} {
		plan := planLifecycle(true, &oauth.BroadcastLifecycleState{Status: s, StreamActive: boolPtr(false)})
		if !plan.Renew || plan.Fault != "" {
			t.Errorf("%s while enabled, encoder stopped: plan = %+v, want Renew with no fault", s, plan)
		}
		for _, active := range []*bool{boolPtr(true), nil} {
			plan := planLifecycle(true, &oauth.BroadcastLifecycleState{Status: s, StreamActive: active})
			if plan.Renew || plan.Send != "" || plan.Wait == "" {
				t.Errorf("%s while bytes may still arrive (%v): plan = %+v, want a wait, no renewal", s, active, plan)
			}
		}
	}
	plan := planLifecycle(true, &oauth.BroadcastLifecycleState{Status: oauth.XStateCanceled, StreamActive: boolPtr(false)})
	if plan.Renew || plan.Fault == "" {
		t.Errorf("canceled: plan = %+v, want a fault and no renewal", plan)
	}
}

// A RUNNING broadcast on a destination the operator has switched off is ended.
// The ended and the never-aired broadcasts are left alone.
func TestXRunningBroadcastWhileDisabledIsEnded(t *testing.T) {
	plan := planLifecycle(false, &oauth.BroadcastLifecycleState{Status: oauth.XStateRunning})
	if plan.Send != oauth.PhaseComplete {
		t.Errorf("running and disabled: Send = %q, want complete", plan.Send)
	}
	for _, s := range []string{oauth.XStateNotStarted, oauth.XStatePrePublished, oauth.XStateEnded, oauth.XStateCanceled} {
		if p := planLifecycle(false, &oauth.BroadcastLifecycleState{Status: s}); p.Send != "" {
			t.Errorf("%s and disabled: Send = %q, want nothing", s, p.Send)
		}
	}
}

// PRE_PUBLISHED is X's own transition. Nothing is sent while it is in progress,
// because a second PUBLISH would be refused.
func TestXPrePublishedIsLeftAloneWhileXIsMovingIt(t *testing.T) {
	plan := planLifecycle(true, &oauth.BroadcastLifecycleState{Status: oauth.XStatePrePublished, StreamActive: boolPtr(true)})
	if plan.Send != "" || plan.Wait == "" {
		t.Errorf("pre-published: plan = %+v, want nothing sent", plan)
	}
}

// The recorded phase is the coordinator's own word, so the live-recheck budget
// and isTerminalPhase work for X without any X-specific branch.
func TestXRecordedPhaseUsesTheCoordinatorsWords(t *testing.T) {
	want := map[string]string{
		oauth.XStateNotStarted: phaseCreated,
		oauth.XStateRunning:    phaseLive,
		oauth.XStateEnded:      phaseComplete,
		oauth.XStateTimedOut:   phaseRevoked,
		oauth.XStateCanceled:   phaseRevoked,
	}
	for s, phase := range want {
		if got := xLifecyclePhase(s); got != phase {
			t.Errorf("xLifecyclePhase(%s) = %q, want %q", s, got, phase)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// --------------------------------------------------------- THE COORDINATOR

// The whole first show, end to end, through considerRow: the row has no
// broadcast, so the coordinator opens the destination. Opening creates NOTHING on
// X (Go Live is public the moment it returns, and ends as TIMED_OUT after five
// minutes without video), so it records a pending handle and sends no go-live
// while the source is silent. When bytes arrive, the next sweep takes the source
// live in one call, and the recorded id becomes the REAL broadcast id.
func TestXCoordinatorOpensOnEnableThenPublishesWhenBytesArrive(t *testing.T) {
	fake := &xcFake{}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{}))

	row := xcSweep(t, c, store)
	opened, published, _, state := fake.snapshot()
	if len(opened) != 0 || published != 0 || state != "" {
		t.Fatalf("first sweep: go-live calls %v, published %d, state %q; want nothing created on X", opened, published, state)
	}
	if lifecycleBroadcastID(row) != xcPending || row.Lifecycle.Phase != phaseCreated {
		t.Errorf("recorded = %+v, want the pending handle %s, created", row.Lifecycle, xcPending)
	}

	// Silent sweeps must not call Go Live, however many there are.
	for range 3 {
		xcSweep(t, c, store)
	}
	if opened, _, _, _ = fake.snapshot(); len(opened) != 0 {
		t.Fatalf("a silent source was taken live: %v", opened)
	}

	fake.set(func(f *xcFake) { f.active = true })
	row = xcSweep(t, c, store)
	opened, published, _, state = fake.snapshot()
	if len(opened) != 1 || published != 1 || state != oauth.XStateRunning {
		t.Errorf("bytes arriving: go-live calls %v, published %d, state %s; want exactly one go-live to RUNNING", opened, published, state)
	}
	if row.Lifecycle.BroadcastID != "bc-1" {
		t.Errorf("recorded id = %q, want the real broadcast id bc-1 and not the pending handle", row.Lifecycle.BroadcastID)
	}
	if row.Lifecycle.Phase != phaseLive || len(*faults) != 0 {
		t.Errorf("after go-live: phase %q faults %v; want live with no faults", row.Lifecycle.Phase, *faults)
	}
	// The next sweep reads the real broadcast: already live, nothing more is sent.
	xcSweep(t, c, store)
	if opened, _, _, _ = fake.snapshot(); len(opened) != 1 {
		t.Errorf("a live broadcast was taken live again: %v", opened)
	}
}

// A destination recorded by an earlier version holds a REAL NOT_STARTED broadcast id made on the removed create route.
// The new routes cannot publish it, so its source is read and taken live, and the recorded id becomes the new broadcast.
func TestXRecordedNotStartedBroadcastIsTakenLiveOnItsSource(t *testing.T) {
	fake := &xcFake{active: true, state: oauth.XStateNotStarted, id: "old-1", seq: 0}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{BroadcastID: "old-1", Phase: phaseCreated}))
	row := xcSweep(t, c, store)
	opened, _, _, _ := fake.snapshot()
	if len(opened) != 1 || opened[0] != xcSourceID {
		t.Fatalf("go-live calls = %v, want one on the source %s", opened, xcSourceID)
	}
	if row.Lifecycle.Phase != phaseLive || len(*faults) != 0 {
		t.Errorf("recorded = %+v faults %v, want live", row.Lifecycle, *faults)
	}
	if row.Lifecycle.BroadcastID == "old-1" || row.Lifecycle.BroadcastID != "bc-1" {
		t.Errorf("recorded id = %q, want it moved off the old broadcast onto the new one (bc-1)", row.Lifecycle.BroadcastID)
	}
}

// A go-live X refuses (409: another broadcast is live on the source, or the source cannot take video yet) keeps the
// pending handle, counts toward the budget, and names X's reason; the source is not taken live behind the operator's back.
func TestXRefusedGoLiveKeepsThePendingHandleAndEscalates(t *testing.T) {
	fake := &xcFake{active: true, liveStatus: http.StatusConflict}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	var row *db.Destination
	for range lifecycleGiveUpAfter + 1 {
		row = xcSweep(t, c, store)
	}
	if lifecycleBroadcastID(row) != xcPending {
		t.Errorf("recorded id = %q after a refused go-live, want the pending handle kept", lifecycleBroadcastID(row))
	}
	if len(*faults) == 0 {
		t.Fatal("a refused go-live never escalated")
	}
	if !strings.Contains(row.Lifecycle.Fault, "Another broadcast is live on this source") {
		t.Errorf("fault = %q, want X's own reason", row.Lifecycle.Fault)
	}
}

// THE SECOND SHOW. The first show ended while the destination was still enabled
// (X's own timeout). The next sweep must open a NEW broadcast and clear the old
// lifecycle block, and it must not seed from the Facebook mirror, which still
// names the ended broadcast.
func TestXSecondShowOpensANewBroadcastAndForgetsTheEndedOne(t *testing.T) {
	fake := &xcFake{active: false, state: oauth.XStateEnded, id: "bc-1", seq: 1}
	stale := xcRow(true, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseLive, Attempts: 3, Fault: "old"})
	stale.Facebook.BroadcastID = "bc-1"
	c, store, faults := xcFixture(t, fake, true, stale)

	row := xcSweep(t, c, store)
	if opened, _, _, _ := fake.snapshot(); len(opened) != 0 {
		t.Fatalf("second show went live %v before any bytes, want a pending handle only", opened)
	}
	if lifecycleBroadcastID(row) != xcPending {
		t.Errorf("recorded broadcast = %q, want the pending handle for the next show, not the ended one", lifecycleBroadcastID(row))
	}
	if row.Lifecycle.Fault != "" || row.Lifecycle.Attempts != 0 {
		t.Errorf("old fault or attempts carried into the new show: %+v", row.Lifecycle)
	}
	if len(*faults) != 0 {
		t.Errorf("renewal raised faults: %v", *faults)
	}
}

// Switching the destination off while it is on air ends the broadcast, through
// the same END call YouTube uses.
func TestXDisablingWhileRunningEndsTheBroadcast(t *testing.T) {
	fake := &xcFake{active: true, state: oauth.XStateRunning, id: "bc-1", seq: 1}
	c, store, _ := xcFixture(t, fake, true, xcRow(false, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseLive}))

	xcSweep(t, c, store)
	_, _, ended, state := fake.snapshot()
	if ended != 1 || state != oauth.XStateEnded {
		t.Errorf("disabled while running: ended %d, state %s; want one END", ended, state)
	}
}

// A closed Livestream gate opens nothing and raises nothing. The account stays
// connected for sign-in, and the gate state is what the operator sees.
func TestXClosedGateOpensNothingAndRaisesNoFault(t *testing.T) {
	fake := &xcFake{active: true}
	c, store, faults := xcFixture(t, fake, false, xcRow(true, db.BroadcastControl{}))

	for i := 0; i < 3; i++ {
		xcSweep(t, c, store)
	}
	opened, _, _, _ := fake.snapshot()
	if len(opened) != 0 || fake.createCalls() != 0 {
		t.Errorf("closed gate opened %d broadcasts and read %d keys", len(opened), fake.createCalls())
	}
	if len(*faults) != 0 {
		t.Errorf("closed gate raised faults: %v", *faults)
	}
	if row := store.rows[5]; lifecycleBroadcastID(row) != "" {
		t.Errorf("closed gate recorded %q", lifecycleBroadcastID(row))
	}
}

// A refused create escalates after the usual attempts, and the escalation names
// the failure without the stream source key. The key is the secret that lets
// anyone broadcast on the source.
func TestXRefusedCreateEscalatesWithoutLeakingTheSourceKey(t *testing.T) {
	fake := &xcFake{active: true, createStatus: http.StatusUnauthorized}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{}))

	for i := 0; i < lifecycleGiveUpAfter+2; i++ {
		xcSweep(t, c, store)
	}
	if len(*faults) == 0 {
		t.Fatal("a refused create never escalated")
	}
	for _, f := range *faults {
		if strings.Contains(f.Detail, xcFakeKey) {
			t.Errorf("fault leaks the source key: %+v", f)
		}
	}
	row := store.rows[5]
	if strings.Contains(row.Lifecycle.Fault, xcFakeKey) {
		t.Errorf("recorded fault leaks the source key: %q", row.Lifecycle.Fault)
	}
}

// SEC-X1: X's problem detail can echo the source_id it was sent, and the source_id IS
// the stream key. A create refused with the key in its detail must leave the key in no
// fault, no recorded row and no log line. The fixture key has no credential shape, so
// this is the exact-literal scrub doing the work, not the pattern matcher.
func TestXRefusedCreateWhoseDetailEchoesTheKeyLeaksItNowhere(t *testing.T) {
	fake := &xcFake{active: true, createStatus: http.StatusBadRequest, createDetail: "source " + xcFakeKey + " is not yours"}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{}))
	var logs bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&logs, nil))

	xcSweep(t, c, store)

	if len(*faults) == 0 {
		t.Fatal("the refused create raised nothing, so this test examined nothing")
	}
	for _, f := range *faults {
		if strings.Contains(f.Detail, xcFakeKey) {
			t.Errorf("the escalated fault carries the source key: %q", f.Detail)
		}
	}
	if got := store.rows[5].Lifecycle.Fault; got == "" || strings.Contains(got, xcFakeKey) {
		t.Errorf("recorded fault = %q; want a fault without the source key", got)
	}
	if strings.Contains(logs.String(), xcFakeKey) {
		t.Errorf("the log carries the source key:\n%s", logs.String())
	}
}

// SEC-X2: a refused create is held after lifecycleGiveUpAfter sweeps, like every other
// fault this coordinator counts. Before, the no-broadcast branch called the platform on
// every sweep for ever. The hold is released by an UP edge, which arrives through Observe
// and must survive the untrack that branch does first.
func TestXRefusedCreateIsHeldAfterTheBudgetAndReleasedByAnUpEdge(t *testing.T) {
	fake := &xcFake{active: true, createStatus: http.StatusUnauthorized}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{}))

	for i := 0; i < lifecycleGiveUpAfter+5; i++ {
		xcSweep(t, c, store)
	}
	if n := fake.createCalls(); n != lifecycleGiveUpAfter {
		t.Fatalf("%d create calls after %d sweeps; want the hold to stop them at %d",
			n, lifecycleGiveUpAfter+5, lifecycleGiveUpAfter)
	}

	c.Observe(hooks.Event{Trigger: hooks.TriggerDestinationUp, Destination: &hooks.DestinationRef{ID: 5}})
	xcSweep(t, c, store)
	if n := fake.createCalls(); n != lifecycleGiveUpAfter+1 {
		t.Errorf("%d create calls after an UP edge; want one more attempt (%d)", n, lifecycleGiveUpAfter+1)
	}
}

// The other release: an operator who switches the destination off and on again gets a
// fresh budget. Without it a held X row nobody tracks could stay held for good, because
// an untracked row opens no observe gate and so may never see an UP edge.
func TestXHeldCreateIsReleasedWhenTheOperatorDisablesTheDestination(t *testing.T) {
	fake := &xcFake{active: true, createStatus: http.StatusUnauthorized}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{Attempts: lifecycleGiveUpAfter, Fault: "refused"}))
	c.noteRearm(5) // held in this process just now, so the re-arm interval is running

	xcSweep(t, c, store)
	if n := fake.createCalls(); n != 0 {
		t.Fatalf("a held row made %d create calls", n)
	}
	store.mu.Lock()
	store.rows[5].Enabled = false
	store.mu.Unlock()
	row := xcSweep(t, c, store)
	if row.Lifecycle.Attempts != 0 || row.Lifecycle.Fault != "" {
		t.Errorf("disabling left the hold in place: %+v", row.Lifecycle)
	}
}

// ------------------------------------------------- THE GO-LIVE TITLE AND THE CHAT TELL

// xcLiveReady is a destination that is open (a pending handle) while bytes arrive, so the next sweep takes it live.
func xcLiveReady(t *testing.T, title string) (*lifecycleCoordinator, *fakeLifecycleStore, *xcFake) {
	t.Helper()
	fake := &xcFake{active: true}
	row := xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated})
	row.GoLiveTitle = title
	c, store, _ := xcFixture(t, fake, true, row)
	return c, store, fake
}

// TestXPublishCarriesTheDestinationsGoLiveTitle: X takes a title only when the broadcast goes live, so the coordinator's
// go-live is the one chance to send it. It used to go through the generic transition, which always sent an empty
// publish and left every X broadcast untitled.
//
// MUTATION: the title left out of the XPublish in advance. Observed: FAIL, the body carried no title.
func TestXPublishCarriesTheDestinationsGoLiveTitle(t *testing.T) {
	c, store, fake := xcLiveReady(t, "Sunday practice, live")
	row := xcSweep(t, c, store)
	if row.Lifecycle.Phase != phaseLive {
		t.Fatalf("phase after publish = %q, want live", row.Lifecycle.Phase)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.publishBodies) != 1 {
		t.Fatalf("PUBLISH calls = %d, want 1", len(fake.publishBodies))
	}
	if got := fake.publishBodies[0]["title"]; got != "Sunday practice, live" {
		t.Errorf("PUBLISH title = %v, want the destination's go-live title", got)
	}
}

// TestXPublishWithNoTitleSendsNoTitleField: an empty title is left out, so X keeps whatever it has rather than being
// sent an empty string it might take as "no title".
func TestXPublishWithNoTitleSendsNoTitleField(t *testing.T) {
	c, store, fake := xcLiveReady(t, "")
	xcSweep(t, c, store)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.publishBodies) != 1 {
		t.Fatalf("PUBLISH calls = %d, want 1", len(fake.publishBodies))
	}
	if _, ok := fake.publishBodies[0]["title"]; ok {
		t.Errorf("PUBLISH body %v carries a title field for an untitled destination", fake.publishBodies[0])
	}
}

// TestXPublishUsesTheTitleSourceWhenOneIsWired: the server wires a title source that prefers the title of a
// recurring show starting now over the stored one. The coordinator sends whatever that source answers.
func TestXPublishUsesTheTitleSourceWhenOneIsWired(t *testing.T) {
	c, store, fake := xcLiveReady(t, "stored title")
	c.goLiveTitle = func(d *db.Destination) string { return "Show 12: " + d.Name }
	xcSweep(t, c, store)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.publishBodies[0]["title"]; got != "Show 12: x main" {
		t.Errorf("PUBLISH title = %v, want the wired source's answer", got)
	}
}

// TestXPublishAndEndTellTheChatAdapterWhichBroadcastIsLive: the coordinator knows the new broadcast id the moment
// X accepts the PUBLISH, so it tells the account's chat adapter instead of leaving it to the adapter's poll. The END
// clears it, so chat sends stop addressing a broadcast that is over.
//
// MUTATION: the pointChat call after a publish removed. Observed: FAIL, no tell recorded.
func TestXPublishAndEndTellTheChatAdapterWhichBroadcastIsLive(t *testing.T) {
	c, store, fake := xcLiveReady(t, "")
	type tell struct {
		account int64
		id      string
	}
	var tells []tell
	c.pointChat = func(account int64, id string) { tells = append(tells, tell{account, id}) }

	xcSweep(t, c, store)
	if len(tells) != 1 || tells[0] != (tell{9, "bc-1"}) {
		t.Fatalf("tells after publish = %v, want account 9 told bc-1", tells)
	}

	store.mu.Lock()
	store.rows[5].Enabled = false
	store.mu.Unlock()
	xcSweep(t, c, store)
	if _, _, ended, _ := fake.snapshot(); ended != 1 {
		t.Fatalf("disabling did not end the broadcast")
	}
	if len(tells) != 2 || tells[1] != (tell{9, ""}) {
		t.Errorf("tells after end = %v, want account 9 cleared", tells)
	}
}

// TestXRenewClearsTheChatTell: a broadcast X ended on its own (TIMED_OUT, or ended in X's tooling) is renewed rather
// than ended by polyemesis, so the renewal is where the adapter learns the old id is over.
func TestXRenewClearsTheChatTell(t *testing.T) {
	fake := &xcFake{active: false, state: oauth.XStateTimedOut, id: "bc-1", seq: 1}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseLive}))
	var told []string
	c.pointChat = func(_ int64, id string) { told = append(told, id) }
	xcSweep(t, c, store)
	if len(told) == 0 || told[0] != "" {
		t.Errorf("tells on renewal = %q, want the old broadcast cleared first", told)
	}
}

// THE EMERGENCY STOP. The operator ends the X broadcast in the X app while the
// encoder is still pushing. Nothing is reopened or published while the bytes keep
// coming; once the encoder stops, the next show gets a new broadcast.
func TestXBroadcastEndedWhileTheEncoderStillSendsIsNotReopenedUntilItStops(t *testing.T) {
	fake := &xcFake{active: true, state: oauth.XStateEnded, id: "bc-1", seq: 1}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseLive}))

	for range 3 {
		xcSweep(t, c, store)
	}
	opened, published, _, _ := fake.snapshot()
	if len(opened) != 0 || published != 0 || fake.createCalls() != 0 {
		t.Fatalf("ended with bytes still arriving: go-live %v, published %d, key reads %d; want nothing reopened",
			opened, published, fake.createCalls())
	}
	fake.set(func(f *xcFake) { f.active = false })
	if row := xcSweep(t, c, store); lifecycleBroadcastID(row) != xcPending {
		t.Fatalf("after the encoder stopped: recorded %q, want the next show's pending handle", lifecycleBroadcastID(row))
	}
}

// A CANCELED broadcast is a fault, not a renewal, so a cancel-and-reopen cycle
// cannot post an announcement on every turn. Switching the destination off
// forgets it, so switching it on again opens a new one.
func TestXCanceledBroadcastIsAFaultReleasedBySwitchingTheDestinationOffAndOn(t *testing.T) {
	fake := &xcFake{state: oauth.XStateCanceled, id: "bc-1", seq: 1}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseLive}))

	for range 3 {
		xcSweep(t, c, store)
	}
	if opened, _, _, _ := fake.snapshot(); len(opened) != 0 || fake.createCalls() != 0 {
		t.Fatalf("a canceled broadcast was reopened: %v (key reads %d)", opened, fake.createCalls())
	}
	if len(*faults) == 0 {
		t.Fatal("a canceled broadcast raised no fault")
	}
	store.mu.Lock()
	store.rows[5].Enabled = false
	store.mu.Unlock()
	if row := xcSweep(t, c, store); lifecycleBroadcastID(row) != "" || row.Lifecycle.Fault != "" {
		t.Fatalf("switched off: lifecycle = %+v, want the canceled broadcast forgotten", row.Lifecycle)
	}
	store.mu.Lock()
	store.rows[5].Enabled = true
	store.mu.Unlock()
	if row := xcSweep(t, c, store); lifecycleBroadcastID(row) != xcPending {
		t.Fatalf("switched on again: recorded %q, want a new pending handle", lifecycleBroadcastID(row))
	}
}

// open() records a pending handle for every enabled X destination before any bytes
// arrive. Switched off before it aired, it cannot become anything without this
// coordinator's go-live, so nothing asks X about it.
func TestADisabledXDestinationWhoseBroadcastNeverAiredIsNotReadEverySweep(t *testing.T) {
	fake := &xcFake{}
	c, store, _ := xcFixture(t, fake, true, xcRow(false, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	xcSweep(t, c, store) // the one read after the switch-off
	fake.mu.Lock()
	first := fake.requests
	fake.mu.Unlock()
	for range 5 {
		xcSweep(t, c, store)
	}
	fake.mu.Lock()
	n := fake.requests
	fake.mu.Unlock()
	if n != first {
		t.Fatalf("X was asked %d more times about a never-aired broadcast on a disabled destination, want 0", n-first)
	}
}

// The one read after the switch-off is what catches a go-live whose record was lost: the row still holds the
// pending handle, X has the broadcast RUNNING on that source, and the read finds it, adopts its real id and ends it.
func TestADisabledXRowStillPendingButRunningOnXIsStillEnded(t *testing.T) {
	fake := &xcFake{state: oauth.XStateRunning, id: "bc-1", seq: 1, active: true, createdMS: time.Now().UnixMilli()}
	c, store, _ := xcFixture(t, fake, true, xcRow(false, db.BroadcastControl{PendingSource: xcSourceID,
		GoLiveAttemptMS: time.Now().Add(-time.Second).UnixMilli(), Phase: phaseCreated}))
	for range 3 {
		xcSweep(t, c, store)
	}
	if _, _, ended, state := fake.snapshot(); ended != 1 || state != oauth.XStateEnded {
		t.Fatalf("ended %d (X state %s), want the broadcast behind the pending handle ended exactly once", ended, state)
	}
}

// The old recorded-"created" shape still ends too: a REAL id from an earlier version whose publish record was lost.
func TestADisabledXRowRecordedAsCreatedButRunningIsStillEnded(t *testing.T) {
	fake := &xcFake{state: oauth.XStateRunning, id: "bc-1", seq: 1, active: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(false, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseCreated}))
	xcSweep(t, c, store)
	if _, _, ended, _ := fake.snapshot(); ended != 1 {
		t.Fatalf("ended %d, want the running broadcast ended", ended)
	}
}

// A destination enabled again with the pending handle while X is RUNNING adopts the broadcast, records its real id
// and never sends a second go-live.
func TestAnEnabledXRowStillPendingButRunningOnXAdoptsTheBroadcast(t *testing.T) {
	fake := &xcFake{state: oauth.XStateRunning, id: "bc-1", seq: 1, active: true, createdMS: time.Now().UnixMilli()}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID,
		GoLiveAttemptMS: time.Now().Add(-time.Second).UnixMilli(), Phase: phaseCreated}))
	row := xcSweep(t, c, store)
	if row.Lifecycle.BroadcastID != "bc-1" || row.Lifecycle.PendingSource != "" || row.Lifecycle.Phase != phaseLive {
		t.Errorf("recorded = %+v, want the real id bc-1, live, and the pending source cleared", row.Lifecycle)
	}
	if opened, _, _, _ := fake.snapshot(); len(opened) != 0 {
		t.Errorf("a second go-live was sent: %v", opened)
	}
}

// DELETING THE DESTINATION RIGHT AFTER GO-LIVE. The target was tracked before the sweep went live, with the pending
// handle and phase created, so an orphan pass in the next sweep would have let go of a live broadcast. advance
// re-tracks it as the real id, live, so the deletion ends it.
func TestXDeletingTheDestinationRightAfterGoLiveEndsTheBroadcast(t *testing.T) {
	fake := &xcFake{active: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	c.sweepOnce(context.Background(), sweepEverything) // goes live: bc-1 RUNNING
	if _, published, _, _ := fake.snapshot(); published != 1 {
		t.Fatalf("setup: published %d, want the go-live to have happened", published)
	}
	store.mu.Lock()
	delete(store.rows, 5)
	store.mu.Unlock()
	for range 3 {
		c.sweepOnce(context.Background(), sweepEverything)
	}
	if _, _, ended, state := fake.snapshot(); ended != 1 || state != oauth.XStateEnded {
		t.Fatalf("ended %d (X state %s): deleting the destination right after go-live left it on air", ended, state)
	}
}

// If the write that records the go-live fails, the broadcast is still tracked as live under its real id, so deleting
// the row ends it instead of stranding it on air.
func TestXAFailedRecordAfterGoLiveStillLeavesTheBroadcastEndable(t *testing.T) {
	fake := &xcFake{active: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	store.failUpdatesAfterGoLive = true // the marker is written; the record of the real id is not
	c.sweepOnce(context.Background(), sweepEverything)
	if _, published, _, _ := fake.snapshot(); published != 1 {
		t.Fatalf("setup: published %d", published)
	}
	store.mu.Lock()
	delete(store.rows, 5)
	store.failUpdatesAfterGoLive = false
	store.mu.Unlock()
	for range 3 {
		c.sweepOnce(context.Background(), sweepEverything)
	}
	if _, _, ended, _ := fake.snapshot(); ended != 1 {
		t.Fatalf("ended %d, want the broadcast whose record failed ended once the row was deleted", ended)
	}
}

// A pending handle whose source is gone from X can never go live. It is a fault the operator sees at once, in words
// (see also TestXDeletedSourceIsForgottenAndTheDestinationReopensOnANewSource), and the destination is held after the
// budget like every other fault: the dead source is forgotten, so what keeps failing is opening one, which says to
// reconnect.
func TestXPendingSourceDeletedOnXRaisesAFault(t *testing.T) {
	fake := &xcFake{sourceDeleted: true}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	row := xcSweep(t, c, store)
	if len(*faults) == 0 || !strings.Contains(row.Lifecycle.Fault, "no longer has the stream source") {
		t.Fatalf("faults %d, fault %q: a source gone from X must be raised in words", len(*faults), row.Lifecycle.Fault)
	}
	for range lifecycleGiveUpAfter + 5 {
		row = xcSweep(t, c, store)
	}
	if row.Lifecycle.Fault == "" || strings.Contains(row.Lifecycle.Fault, "pending:") {
		t.Errorf("fault %q after the budget, want a visible fault without the internal handle", row.Lifecycle.Fault)
	}
	if row.Lifecycle.Attempts != lifecycleGiveUpAfter {
		t.Errorf("attempts %d, want the destination held at the give-up count", row.Lifecycle.Attempts)
	}
}

// An account X will not let go live (403) is terminal: one call, held at the give-up count, raised once in words.
func TestXIneligibleAccountIsATerminalFaultNotRetried(t *testing.T) {
	fake := &xcFake{active: true, liveStatus: http.StatusForbidden,
		liveBody: `{"type":"about:blank","title":"Forbidden","detail":"This account is not eligible to go live.","status":403}`}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	var row *db.Destination
	for range lifecycleGiveUpAfter + 5 {
		row = xcSweep(t, c, store)
	}
	if fake.liveCalls != 1 {
		t.Errorf("%d go-live calls, want exactly 1: the refusal is terminal", fake.liveCalls)
	}
	if len(*faults) != 1 || row.Lifecycle.Attempts != lifecycleGiveUpAfter {
		t.Errorf("faults %d attempts %d, want one fault held at the give-up count", len(*faults), row.Lifecycle.Attempts)
	}
	for _, want := range []string{"allowed to broadcast on X", "not eligible to go live"} {
		if !strings.Contains(row.Lifecycle.Fault, want) {
			t.Errorf("fault %q does not say %q", row.Lifecycle.Fault, want)
		}
	}
	if strings.Contains(row.Lifecycle.Fault, "pending:") {
		t.Errorf("the fault shows the internal handle: %q", row.Lifecycle.Fault)
	}
}

// A 409 that says the source cannot take video yet is the expected wait: it is retried every sweep and never spends
// the give-up budget or raises anything.
func TestXSourceNotReceivingVideoYetDoesNotSpendTheGiveUpBudget(t *testing.T) {
	fake := &xcFake{active: true, liveStatus: http.StatusConflict,
		liveBody: `{"type":"https://api.x.com/2/problems/conflict","title":"Conflict","detail":"The source cannot take video until its encoder connects.","status":409}`}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	var row *db.Destination
	for range lifecycleGiveUpAfter + 5 {
		row = xcSweep(t, c, store)
	}
	if row.Lifecycle.Attempts != 0 || row.Lifecycle.Fault != "" || len(*faults) != 0 {
		t.Errorf("attempts %d fault %q faults %d, want the wait to cost nothing", row.Lifecycle.Attempts, row.Lifecycle.Fault, len(*faults))
	}
	if fake.liveCalls != lifecycleGiveUpAfter+5 {
		t.Errorf("%d go-live calls, want one per sweep: the wait is retried", fake.liveCalls)
	}
}

// A go-live X answers 200 (the source already had a live broadcast) is adopted, and the coordinator says so at INFO.
func TestXGoLiveAnswered200AdoptsTheLiveBroadcastAndLogsIt(t *testing.T) {
	// The list is lagging, so the read cannot find the live broadcast and the go-live is what answers.
	fake := &xcFake{active: true, state: oauth.XStateRunning, id: "bc-9", seq: 9, listLag: true, createdMS: time.Now().UnixMilli()}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	var logs bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	row := xcSweep(t, c, store)
	if row.Lifecycle.BroadcastID != "bc-9" {
		t.Errorf("recorded id = %q, want the adopted broadcast bc-9", row.Lifecycle.BroadcastID)
	}
	if !strings.Contains(logs.String(), "already had a live broadcast") {
		t.Errorf("the adoption was not logged at INFO:\n%s", logs.String())
	}
}

// The handle never reaches an operator: not the stored broadcastId, not a fault, not an escalation, not an event.
func TestXPendingHandleNeverReachesTheOperator(t *testing.T) {
	fake := &xcFake{active: true, liveStatus: http.StatusConflict}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{}))
	var logs bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&logs, nil))
	var phases []broadcastPhase
	c.announce = func(p broadcastPhase) { phases = append(phases, p) }
	for range 3 {
		xcSweep(t, c, store)
	}
	row := store.rows[5]
	if row.Lifecycle.BroadcastID != "" || row.Lifecycle.PendingSource != xcSourceID {
		t.Errorf("recorded = %+v, want broadcastId empty and the source kept as pendingSource", row.Lifecycle)
	}
	if row.ActiveBroadcastID() != "" {
		t.Errorf("ActiveBroadcastID() = %q while pending, want empty", row.ActiveBroadcastID())
	}
	if strings.Contains(row.Lifecycle.Fault, "pending:") {
		t.Errorf("fault shows the handle: %q", row.Lifecycle.Fault)
	}
	for _, f := range *faults {
		if strings.Contains(f.Detail, "pending:") || strings.Contains(f.BroadcastID, "pending:") {
			t.Errorf("escalation shows the handle: %+v", f)
		}
	}
	for _, p := range phases {
		if strings.Contains(p.BroadcastID, "pending:") {
			t.Errorf("event shows the handle: %+v", p)
		}
	}
}

// X publishes no Livestream rate limits. A 429 is X asking for a pause: the
// provider holds its calls until the reset, and the coordinator records why but
// does not count it toward the give-up budget.
func TestAnXRateLimitIsNotCountedAgainstTheGiveUpBudget(t *testing.T) {
	fake := &xcFake{limited: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{}))
	var row *db.Destination
	for range lifecycleGiveUpAfter + 5 {
		row = xcSweep(t, c, store)
	}
	if row.Lifecycle.Attempts != 0 {
		t.Fatalf("attempts = %d after rate-limit answers, want 0", row.Lifecycle.Attempts)
	}
	if !strings.Contains(row.Lifecycle.Fault, "rate-limiting") {
		t.Fatalf("fault = %q, want the rate limit named", row.Lifecycle.Fault)
	}
	fake.mu.Lock()
	n := fake.requests
	fake.mu.Unlock()
	if n != 1 {
		t.Fatalf("X was asked %d times during the hold, want 1", n)
	}
}

// An ended broadcast whose answer cannot say whether the encoder is sending (no
// source on the read) waits, as one with bytes still arriving does, and an UP
// edge releases it: the ingest stopped and came back, which is the next show.
func TestXEndedBroadcastWithAnUnknownEncoderWaitsUntilAnUpEdge(t *testing.T) {
	fake := &xcFake{state: oauth.XStateEnded, id: "bc-1", seq: 1, sourceGone: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseLive}))
	for range 3 {
		xcSweep(t, c, store)
	}
	if fake.createCalls() != 0 {
		t.Fatalf("opened with the encoder's state unknown (%d key reads), want a wait", fake.createCalls())
	}
	c.Observe(hooks.Event{Trigger: hooks.TriggerDestinationUp, Destination: &hooks.DestinationRef{ID: 5}})
	if row := xcSweep(t, c, store); lifecycleBroadcastID(row) != xcPending {
		t.Fatalf("after an UP edge recorded %q, want the next show's pending handle", lifecycleBroadcastID(row))
	}
}

// A DELETED X DESTINATION WHOSE BROADCAST X IS STILL PUBLISHING IS ENDED ONCE IT RUNS (w3h tail, #28). The PUBLISH
// was sent, X answered PRE_PUBLISHED, and the row was deleted before X moved the broadcast to RUNNING.
// endableFromPhase did not count "prepublished" as confirmed, and the read that still said PRE_PUBLISHED planned
// "never aired", so endOrphan let go of the broadcast both ways and it went RUNNING with nothing left to end it.
// It is now kept and read again, and the END goes out on the sweep that sees it RUNNING.
//
// MUTATION, internal/api/lifecycle.go, in endOrphan: delete the XStatePrePublished keep-tracking branch. Observed:
// FAIL -- ended 0 after the broadcast went RUNNING. And separately in endableFromPhase: drop phasePrePublished.
// Observed: FAIL, the same way.
func TestADeletedXDestinationWhoseBroadcastIsStillPublishingIsEndedOnceItRuns(t *testing.T) {
	fake := &xcFake{active: true, state: oauth.XStatePrePublished, id: "bc-1", seq: 1}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{BroadcastID: "bc-1", Phase: phasePrePublished}))

	c.sweepOnce(context.Background(), sweepEverything)
	store.mu.Lock()
	delete(store.rows, 5)
	store.mu.Unlock()

	// Still PRE_PUBLISHED: nothing can be ended yet, and the broadcast must not be let go.
	c.sweepOnce(context.Background(), sweepEverything)
	if _, _, ended, _ := fake.snapshot(); ended != 0 {
		t.Fatalf("an END was sent to a broadcast X was still publishing: %d", ended)
	}

	fake.set(func(f *xcFake) { f.state = oauth.XStateRunning })
	c.sweepOnce(context.Background(), sweepEverything)
	if _, _, ended, state := fake.snapshot(); ended != 1 || state != oauth.XStateEnded {
		t.Fatalf("ended %d (state %s) after the deleted destination's broadcast went RUNNING; want one END", ended, state)
	}
}

// Deleting an X destination whose broadcast X is still publishing asks for confirmation, as deleting one that is
// live does: endOrphan will end it, so the delete is a request to end a show.
func TestAPrePublishedXBroadcastIsEndableSoItsDeleteNeedsConfirmation(t *testing.T) {
	if !endableFromPhase(phasePrePublished) {
		t.Fatal("a broadcast this process published is not endable, so its delete would neither confirm nor end it")
	}
}

// ----------------------------------------------------- round 2: review of the review

// WRONG-BROADCAST ADOPTION. The source carries a broadcast polyemesis did not start (created long before). A go-live
// that X refuses as "another broadcast is live" must not be followed by adopting that broadcast on the next read,
// which would clear the fault and let a switch-off END somebody else's show.
func TestXNeverAdoptsABroadcastItDidNotStartAfterABusyRefusal(t *testing.T) {
	fake := &xcFake{active: true, state: oauth.XStateRunning, id: "other-1", seq: 1, listLag: false, createdMS: 0, liveStatus: http.StatusConflict}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	var row *db.Destination
	for range 4 {
		row = xcSweep(t, c, store)
	}
	if row.Lifecycle.BroadcastID != "" || row.Lifecycle.PendingSource != xcSourceID {
		t.Fatalf("recorded = %+v, want the pending source kept and nothing adopted", row.Lifecycle)
	}
	if len(*faults) == 0 || !strings.Contains(row.Lifecycle.Fault, "another broadcast is already live") {
		t.Errorf("faults %d fault %q, want the busy source raised", len(*faults), row.Lifecycle.Fault)
	}
	// Switched off, the other broadcast is still not ended: it was never ours.
	store.mu.Lock()
	store.rows[5].Enabled = false
	store.mu.Unlock()
	for range 3 {
		xcSweep(t, c, store)
	}
	if _, _, ended, _ := fake.snapshot(); ended != 0 {
		t.Fatalf("ended %d: polyemesis ended a broadcast it did not start", ended)
	}
}

// A 200 for a broadcast created BEFORE this attempt is somebody else's: refused, not adopted, never ended.
func TestXGoLiveAnswered200ForAForeignBroadcastIsRefusedNotAdopted(t *testing.T) {
	fake := &xcFake{active: true, state: oauth.XStateRunning, id: "other-1", seq: 1, listLag: true}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	row := xcSweep(t, c, store)
	if row.Lifecycle.BroadcastID != "" || len(*faults) == 0 || !strings.Contains(row.Lifecycle.Fault, "not started by polyemesis") {
		t.Fatalf("recorded %+v faults %d, want a refusal saying the broadcast was not started by polyemesis", row.Lifecycle, len(*faults))
	}
}

// The attempt is stamped before the go-live is sent and kept when the answer is lost, so the next read can adopt what
// that attempt made; a definite refusal clears it, because nothing was created.
func TestXGoLiveAttemptIsStampedKeptOnALostAnswerAndClearedOnARefusal(t *testing.T) {
	fake := &xcFake{active: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	store.failUpdatesAfterGoLive = true
	before := time.Now().UnixMilli()
	xcSweep(t, c, store) // goes live; the record of the real id fails
	row := store.rows[5]
	if row.Lifecycle.GoLiveSentMS < before || row.Lifecycle.PendingSource != xcSourceID || row.Lifecycle.BroadcastID != "" {
		t.Fatalf("recorded = %+v, want the pending source with the in-flight marker at or after %d", row.Lifecycle, before)
	}
	if d := row.Lifecycle.GoLiveAttemptMS - time.Now().UnixMilli(); d < -3000 || d > 3000 {
		t.Fatalf("attempt in X's time = %d, want it within a few seconds of now (X's clock has one-second resolution)", row.Lifecycle.GoLiveAttemptMS)
	}

	fake2 := &xcFake{active: true, liveStatus: http.StatusConflict}
	c2, store2, _ := xcFixture(t, fake2, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	row2 := xcSweep(t, c2, store2)
	if row2.Lifecycle.GoLiveAttemptMS != 0 || row2.Lifecycle.GoLiveSentMS != 0 {
		t.Errorf("markers = %+v after a definite refusal, want them cleared: nothing was created", row2.Lifecycle)
	}
}

// PROTECTION DOWNGRADE. After a go-live whose record failed, the next sweep re-tracks from the durable row, which still
// says pending/created. That must not overwrite the verified live target: with the state read failing and the row then
// deleted, the broadcast is still ended.
func TestXTrackingNeverMovesBackwardsFromAVerifiedLiveBroadcast(t *testing.T) {
	fake := &xcFake{active: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	store.failUpdatesAfterGoLive = true
	c.sweepOnce(context.Background(), sweepEverything) // live as bc-1; the record fails
	if _, published, _, _ := fake.snapshot(); published != 1 {
		t.Fatalf("setup: published %d", published)
	}
	store.mu.Lock()
	store.failUpdatesAfterGoLive = false
	store.mu.Unlock()
	fake.set(func(f *xcFake) { f.failReads = true })
	c.sweepOnce(context.Background(), sweepEverything) // re-tracks from the durable row; the state read fails
	store.mu.Lock()
	delete(store.rows, 5)
	store.mu.Unlock()
	fake.set(func(f *xcFake) { f.failReads = false })
	for range 3 {
		c.sweepOnce(context.Background(), sweepEverything)
	}
	if _, _, ended, _ := fake.snapshot(); ended != 1 {
		t.Fatalf("ended %d: a failed read downgraded the live target and the deleted row's broadcast was dropped", ended)
	}
}

// A renewal is not a downgrade: once X ended the broadcast and the next show is opened, the old live target is gone, so
// deleting the row sends no END to the finished broadcast.
func TestXRenewalForgetsTheEndedLiveTarget(t *testing.T) {
	fake := &xcFake{active: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	xcSweep(t, c, store) // live bc-1
	fake.set(func(f *xcFake) { f.state, f.active = oauth.XStateEnded, false })
	xcSweep(t, c, store) // ended, encoder stopped: renewed to a pending handle
	store.mu.Lock()
	delete(store.rows, 5)
	store.mu.Unlock()
	for range 3 {
		c.sweepOnce(context.Background(), sweepEverything)
	}
	if _, _, ended, _ := fake.snapshot(); ended != 0 {
		t.Fatalf("ended %d: an END was sent to a broadcast that had already finished", ended)
	}
}

// DELETED SOURCE. The fault is raised, the dead source is forgotten so the destination is opened again, and once the
// account is reconnected (the key refresh writes the new source's key) the next sweep opens on the NEW source and the
// fault clears.
func TestXDeletedSourceIsForgottenAndTheDestinationReopensOnANewSource(t *testing.T) {
	fake := &xcFake{sourceDeleted: true}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	row := xcSweep(t, c, store)
	if row.Lifecycle.PendingSource != "" || row.Lifecycle.BroadcastID != "" {
		t.Fatalf("recorded = %+v, want the dead source forgotten", row.Lifecycle)
	}
	if len(*faults) == 0 || !strings.Contains(row.Lifecycle.Fault, "no longer has the stream source") {
		t.Fatalf("faults %d fault %q, want the deleted source raised", len(*faults), row.Lifecycle.Fault)
	}
	// Still no usable source: opening cannot succeed, the fault stays visible and nothing is pending.
	row = xcSweep(t, c, store)
	if row.Lifecycle.PendingSource != "" || row.Lifecycle.Fault == "" {
		t.Fatalf("recorded = %+v, want the fault still visible while no source exists", row.Lifecycle)
	}
	// Reconnect: a new source, a new key stored on the destination.
	const newSrc, newKey = "7100000000000000002", "freshkey2"
	fake.set(func(f *xcFake) { f.sourceDeleted, f.srcID, f.key = false, newSrc, newKey })
	store.mu.Lock()
	store.rows[5].StreamKey = newKey
	store.mu.Unlock()
	var last *db.Destination
	for range 2 {
		last = xcSweep(t, c, store)
	}
	if last.Lifecycle.PendingSource != newSrc || last.Lifecycle.Fault != "" {
		t.Fatalf("recorded = %+v, want the destination reopened on the new source %s with the fault cleared", last.Lifecycle, newSrc)
	}
}

// RECONNECT resets the pending state of the account's X destinations, but never touches one with a real broadcast.
func TestReconnectResetsPendingXLifecycleButNotALiveBroadcast(t *testing.T) {
	acct := int64(9)
	pendingRow := xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, GoLiveAttemptMS: 5, Phase: phaseCreated, Fault: "old", Attempts: 3})
	liveRow := xcRow(true, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseLive})
	liveRow.ID, liveRow.Name = 6, "x live"
	otherAcct := int64(10)
	otherRow := xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated})
	otherRow.ID, otherRow.Name, otherRow.AccountID = 7, "x other", &otherAcct
	store := &fakeLifecycleStore{rows: map[int64]*db.Destination{5: pendingRow, 6: liveRow, 7: otherRow}}
	if n := resetXPendingLifecycle(store, acct); n != 1 {
		t.Fatalf("reset %d destinations, want exactly the one pending row of the account", n)
	}
	if got := store.rows[5].Lifecycle; !got.Empty() {
		t.Errorf("pending row = %+v, want the pending state cleared", got)
	}
	if store.rows[6].Lifecycle.BroadcastID != "bc-1" {
		t.Errorf("a live broadcast was reset: %+v", store.rows[6].Lifecycle)
	}
	if store.rows[7].Lifecycle.PendingSource == "" {
		t.Errorf("another account's destination was reset")
	}
}

// The coordinator's logs never show the handle: the source is its own key, and broadcast is only ever a real id.
func TestXLogsNeverShowThePendingHandle(t *testing.T) {
	fake := &xcFake{active: true, liveStatus: http.StatusConflict}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{}))
	var logs bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	for range 4 {
		xcSweep(t, c, store)
	}
	if strings.Contains(logs.String(), "pending:") {
		t.Errorf("a log line shows the internal handle:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "pending_source="+xcSourceID) {
		t.Errorf("no log line names the pending source under its own key:\n%s", logs.String())
	}
}

// A missing-scopes 403 is terminal and tells the operator to reconnect with the scopes.
func TestXMissingScopesIsATerminalFaultThatSaysReconnect(t *testing.T) {
	fake := &xcFake{active: true, liveStatus: http.StatusForbidden,
		liveBody: `{"type":"https://api.x.com/2/problems/missing-scopes","title":"Forbidden","detail":"Token lacks broadcast.write.","status":403}`}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	var row *db.Destination
	for range lifecycleGiveUpAfter + 3 {
		row = xcSweep(t, c, store)
	}
	if fake.liveCalls != 1 || len(*faults) != 1 {
		t.Errorf("go-live calls %d faults %d, want one call and one fault: the refusal is terminal", fake.liveCalls, len(*faults))
	}
	for _, want := range []string{"Reconnect", "broadcast.read", "broadcast.write"} {
		if !strings.Contains(row.Lifecycle.Fault, want) {
			t.Errorf("fault %q does not say %q", row.Lifecycle.Fault, want)
		}
	}
}

// ------------------------------------------------------------ round 3: clocks, races, re-arm

// X's clock, not ours. The go-live attempt is recorded in X's time (our clock plus the offset from X's Date header), so
// a polyemesis clock ten minutes either way still adopts the broadcast its own lost go-live made, without a second POST.
func TestXAdoptsItsOwnBroadcastWhateverTheLocalClockSays(t *testing.T) {
	for _, skew := range []time.Duration{10 * time.Minute, -10 * time.Minute, 0} {
		t.Run(skew.String(), func(t *testing.T) {
			fake := &xcFake{active: true, skew: skew, dropLive: true}
			c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
			row := xcSweep(t, c, store) // goes live; the answer is lost
			if row.Lifecycle.GoLiveSentMS == 0 || row.Lifecycle.BroadcastID != "" {
				t.Fatalf("recorded = %+v, want the go-live marked as unresolved", row.Lifecycle)
			}
			fake.set(func(f *xcFake) { f.dropLive = false })
			row = xcSweep(t, c, store)
			if row.Lifecycle.BroadcastID != "bc-1" || row.Lifecycle.GoLiveSentMS != 0 || row.Lifecycle.Phase != phaseLive {
				t.Fatalf("recorded = %+v, want the broadcast its own go-live made adopted as bc-1", row.Lifecycle)
			}
			if fake.liveCalls != 1 {
				t.Errorf("%d go-live calls, want 1: the lost answer is resolved by reading, not by sending again", fake.liveCalls)
			}
		})
	}
}

// A broadcast on the source that X created minutes AFTER the attempt, or before it, is somebody else's: never adopted,
// never ended, and the operator is told.
func TestXForeignBroadcastInsideOrOutsideTheWindowIsNeverAdoptedOrEnded(t *testing.T) {
	for _, tc := range []struct {
		name string
		by   time.Duration // created_at_ms relative to the attempt
	}{{"two minutes after the attempt", 2 * time.Minute}, {"two minutes before the attempt", -2 * time.Minute}} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			fake := &xcFake{active: true, state: oauth.XStateRunning, id: "other-1", seq: 1, createdMS: now.Add(tc.by).UnixMilli()}
			c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID,
				GoLiveAttemptMS: now.UnixMilli(), GoLiveSentMS: now.UnixMilli(), Phase: phaseCreated}))
			var row *db.Destination
			for range 3 {
				row = xcSweep(t, c, store)
			}
			if row.Lifecycle.BroadcastID != "" || len(*faults) == 0 || !strings.Contains(row.Lifecycle.Fault, "not started by polyemesis") {
				t.Fatalf("recorded %+v faults %d, want it refused as not started by polyemesis", row.Lifecycle, len(*faults))
			}
			store.mu.Lock()
			store.rows[5].Enabled = false
			store.mu.Unlock()
			for range 3 {
				xcSweep(t, c, store)
			}
			if _, _, ended, _ := fake.snapshot(); ended != 0 {
				t.Fatalf("ended %d: polyemesis ended a broadcast it did not start", ended)
			}
		})
	}
}

// With no reading of X's clock the attempt is not recorded in X's time, and a 200 for a live broadcast cannot be
// confirmed as ours: refused with that wording, never adopted, never ended.
func TestXWithNoReadingOfXsClockNothingIsAdopted(t *testing.T) {
	fake := &xcFake{active: true, noDate: true, state: oauth.XStateRunning, id: "other-1", seq: 1, listLag: true,
		createdMS: time.Now().UnixMilli()}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	row := xcSweep(t, c, store)
	if row.Lifecycle.BroadcastID != "" || len(*faults) == 0 || !strings.Contains(row.Lifecycle.Fault, "could not confirm the broadcast on this source was started by polyemesis") {
		t.Fatalf("recorded %+v faults %d, want the unconfirmed broadcast refused in words", row.Lifecycle, len(*faults))
	}
	store.mu.Lock()
	store.rows[5].Enabled = false
	store.mu.Unlock()
	for range 3 {
		xcSweep(t, c, store)
	}
	if _, _, ended, _ := fake.snapshot(); ended != 0 {
		t.Fatalf("ended %d: an unconfirmed broadcast was ended", ended)
	}
}

// RACE (a): a reconnect does not reset a row whose go-live is in flight or unresolved; the sweep resolves it.
func TestReconnectLeavesARowWithAnUnresolvedGoLiveToTheSweep(t *testing.T) {
	acct := int64(9)
	row := xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, GoLiveSentMS: 5, GoLiveAttemptMS: 5, Phase: phaseCreated, Fault: "x"})
	store := &fakeLifecycleStore{rows: map[int64]*db.Destination{5: row}}
	if n := resetXPendingLifecycle(store, acct); n != 0 {
		t.Fatalf("reset %d rows, want the unresolved one left alone", n)
	}
	if got := store.rows[5].Lifecycle; got.PendingSource != xcSourceID || got.GoLiveSentMS != 5 {
		t.Errorf("row = %+v, want it untouched", got)
	}
	// Decided again inside the write: a go-live stamped between the listing and the write is also left alone.
	row2 := xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated, Fault: "x"})
	store2 := &fakeLifecycleStore{rows: map[int64]*db.Destination{5: row2}}
	store2.onUpdate = map[int]func(*db.Destination){1: func(r *db.Destination) { r.Lifecycle.GoLiveSentMS = 7 }}
	if n := resetXPendingLifecycle(store2, acct); n != 0 || store2.rows[5].Lifecycle.GoLiveSentMS != 7 || store2.rows[5].Lifecycle.PendingSource == "" {
		t.Errorf("reset %d rows / row %+v, want a go-live stamped during the reset to win", n, store2.rows[5].Lifecycle)
	}
}

// RACE (b): the row is reset between the sweep's read and its stamp. The stamp is refused (older generation), so the
// go-live is NOT sent for a source that was just reset.
func TestXStampRefusedByAResetSendsNoGoLive(t *testing.T) {
	fake := &xcFake{active: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	store.onUpdate = map[int]func(*db.Destination){1: func(r *db.Destination) {
		r.Lifecycle = db.BroadcastControl{Generation: r.Lifecycle.Generation + 1} // a reconnect reset it
	}}
	c.sweepOnce(context.Background(), sweepEverything)
	if fake.liveCalls != 0 {
		t.Fatalf("%d go-live calls, want none: the row was reset under the sweep", fake.liveCalls)
	}
}

// RACE (b): a late FAILURE record from a sweep that read the older row cannot put back a source that was cleared.
func TestXLateFailureRecordCannotRestoreAClearedSource(t *testing.T) {
	fake := &xcFake{active: true, liveStatus: http.StatusConflict,
		liveBody: `{"type":"https://api.x.com/2/problems/conflict","title":"Conflict","status":409,"detail":"Broadcast creation is temporarily blocked."}`}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	// Write 1 is the stamp. Write 2 is the failure record, and the source is cleared just before it.
	store.onUpdate = map[int]func(*db.Destination){2: func(r *db.Destination) {
		r.Lifecycle = db.BroadcastControl{Generation: r.Lifecycle.Generation + 1}
	}}
	c.sweepOnce(context.Background(), sweepEverything)
	if got := store.rows[5].Lifecycle; got.PendingSource != "" || got.Fault != "" || got.Attempts != 0 {
		t.Fatalf("row = %+v, want the cleared state kept: a late result must not restore the source or the fault", got)
	}
}

// RACE (c): a go-live whose answer was lost, then the row deleted. The target is kept as maybe-live; the read finds the
// broadcast inside the window (even after the list lagged once) and ENDS the real id.
func TestXDeletedRowAfterALostGoLiveStillEndsWhatItMade(t *testing.T) {
	fake := &xcFake{active: true, dropLive: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	c.sweepOnce(context.Background(), sweepEverything) // goes live, answer lost
	if _, published, _, _ := fake.snapshot(); published != 1 {
		t.Fatalf("setup: published %d", published)
	}
	store.mu.Lock()
	delete(store.rows, 5)
	store.mu.Unlock()
	fake.set(func(f *xcFake) { f.dropLive, f.listLag = false, true })
	c.sweepOnce(context.Background(), sweepEverything) // the list has not caught up: kept, not dropped
	if _, _, ended, _ := fake.snapshot(); ended != 0 {
		t.Fatalf("ended %d before the list showed the broadcast", ended)
	}
	fake.set(func(f *xcFake) { f.listLag = false })
	for range 3 {
		c.sweepOnce(context.Background(), sweepEverything)
	}
	if _, _, ended, state := fake.snapshot(); ended != 1 || state != oauth.XStateEnded {
		t.Fatalf("ended %d (X state %s): the broadcast a lost go-live made was dropped when the row was deleted", ended, state)
	}
}

// RACE (c): a switched-off destination whose go-live answer was lost also ends it, and keeps reading until it shows up.
func TestXSwitchOffAfterALostGoLiveKeepsLookingThenEnds(t *testing.T) {
	fake := &xcFake{active: true, dropLive: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	xcSweep(t, c, store) // goes live, answer lost
	store.mu.Lock()
	store.rows[5].Enabled = false
	store.mu.Unlock()
	fake.set(func(f *xcFake) { f.dropLive, f.listLag = false, true })
	xcSweep(t, c, store) // list lags: this must not make the row quiet
	xcSweep(t, c, store)
	fake.set(func(f *xcFake) { f.listLag = false })
	xcSweep(t, c, store)
	if _, _, ended, _ := fake.snapshot(); ended != 1 {
		t.Fatalf("ended %d, want the broadcast ended once the list showed it", ended)
	}
}

// RACE (c): a source that left X with a go-live unresolved clears the row (so it can open again) but keeps the tracked
// maybe-live target, so nothing that may be on air is forgotten.
func TestXSourceGoneKeepsTheMaybeLiveTarget(t *testing.T) {
	fake := &xcFake{sourceDeleted: true}
	now := time.Now().UnixMilli()
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID,
		GoLiveAttemptMS: now, GoLiveSentMS: now, Phase: phaseCreated}))
	row := xcSweep(t, c, store)
	if row.Lifecycle.PendingSource != "" || row.Lifecycle.GoLiveSentMS != 0 || row.Lifecycle.Generation != 1 {
		t.Fatalf("recorded = %+v, want the pending state cleared and the generation bumped", row.Lifecycle)
	}
	c.mu.Lock()
	tgt, ok := c.tracked[5]
	c.mu.Unlock()
	if !ok || !tgt.maybeLive {
		t.Fatalf("tracked = %+v ok=%v, want the maybe-live target kept", tgt, ok)
	}
}

// UNKNOWN 409. A conflict X does not explain is counted, and its exact detail is shown. When the budget is used the
// destination is not blocked for ever: every two minutes one try is made, and the first one X accepts goes live with no
// manual toggle. A terminal refusal is never retried.
func TestXUnknownConflictIsRetriedEveryTwoMinutesAfterTheBudgetAndGoesLive(t *testing.T) {
	const detail = "Broadcast creation is temporarily blocked."
	fake := &xcFake{active: true, liveStatus: http.StatusConflict,
		liveBody: `{"type":"https://api.x.com/2/problems/conflict","title":"Conflict","status":409,"detail":"` + detail + `"}`}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	clock := time.Now()
	c.now = func() time.Time { return clock }
	var row *db.Destination
	for range lifecycleGiveUpAfter {
		row = xcSweep(t, c, store)
	}
	if row.Lifecycle.Attempts != lifecycleGiveUpAfter || row.Lifecycle.Terminal {
		t.Fatalf("recorded = %+v, want the budget used and the fault not terminal", row.Lifecycle)
	}
	if !strings.Contains(row.Lifecycle.Fault, detail) || len(*faults) == 0 {
		t.Fatalf("fault %q faults %d, want X's exact words in the fault and the fault raised", row.Lifecycle.Fault, len(*faults))
	}
	calls := fake.liveCalls
	fake.set(func(f *xcFake) { f.liveStatus = 0 }) // X accepts from now on
	xcSweep(t, c, store)
	if fake.liveCalls != calls {
		t.Fatalf("a held destination made a go-live call before the interval: %d -> %d", calls, fake.liveCalls)
	}
	clock = clock.Add(lifecycleRearmEvery + time.Second)
	row = xcSweep(t, c, store)
	if row.Lifecycle.BroadcastID != "bc-1" || row.Lifecycle.Fault != "" || row.Lifecycle.Attempts != 0 {
		t.Fatalf("recorded = %+v, want it live as bc-1 with the fault cleared, with no manual toggle", row.Lifecycle)
	}
}

func TestXTerminalRefusalIsNeverReArmed(t *testing.T) {
	fake := &xcFake{active: true, liveStatus: http.StatusForbidden,
		liveBody: `{"type":"about:blank","title":"Forbidden","detail":"This account is not eligible to go live.","status":403}`}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	clock := time.Now()
	c.now = func() time.Time { return clock }
	xcSweep(t, c, store)
	for range 3 {
		clock = clock.Add(10 * time.Minute)
		xcSweep(t, c, store)
	}
	if fake.liveCalls != 1 {
		t.Fatalf("%d go-live calls, want 1: a terminal refusal is not retried automatically", fake.liveCalls)
	}
	if !store.rows[5].Lifecycle.Terminal {
		t.Errorf("recorded = %+v, want the fault marked terminal", store.rows[5].Lifecycle)
	}
}

// ------------------------------------------------------------ round 4 (the last)

// NO GO-LIVE WITHOUT A DURABLE MARKER. If the write that records the attempt fails for ANY reason (a database error,
// a deleted row), nothing is sent: a broadcast polyemesis could not record is one it could not find or end later.
func TestXNoGoLiveIsSentWhenTheMarkerCannotBeWritten(t *testing.T) {
	fake := &xcFake{active: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	store.failUpdates = true
	for range 3 {
		c.sweepOnce(context.Background(), sweepEverything)
	}
	if fake.liveCalls != 0 {
		t.Fatalf("%d go-live calls with the marker unwritable, want none", fake.liveCalls)
	}
	// Once the database answers again the go-live goes out, so nothing was lost by waiting.
	store.mu.Lock()
	store.failUpdates = false
	store.mu.Unlock()
	xcSweep(t, c, store)
	if fake.liveCalls != 1 {
		t.Fatalf("%d go-live calls after the write recovered, want 1", fake.liveCalls)
	}
}

// The source is gone from X's list but the broadcast listing shows OUR RUNNING broadcast (inside the window): the read
// returns it, so the END proceeds on a switch-off and on a delete instead of the read turning into "source gone" and the
// maybe-live target being discarded.
func TestXSourceGoneWithOurBroadcastRunningStillEnds(t *testing.T) {
	for _, mode := range []string{"switched off", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UnixMilli()
			fake := &xcFake{sourceDeleted: true, state: oauth.XStateRunning, id: "bc-1", seq: 1, createdMS: now}
			row := xcRow(mode != "switched off", db.BroadcastControl{PendingSource: xcSourceID,
				GoLiveAttemptMS: now - 1000, GoLiveSentMS: now - 1000, Phase: phaseCreated})
			c, store, _ := xcFixture(t, fake, true, row)
			if mode == "deleted" {
				c.sweepOnce(context.Background(), sweepEverything) // tracks the row (enabled): the source is gone but ours is live
				store.mu.Lock()
				delete(store.rows, 5)
				store.mu.Unlock()
			}
			for range 4 {
				c.sweepOnce(context.Background(), sweepEverything)
			}
			if _, _, ended, _ := fake.snapshot(); ended != 1 {
				t.Fatalf("ended %d: the broadcast listing showed our RUNNING broadcast and it was not ended (%s)", ended, mode)
			}
		})
	}
}

// WHERE TRACKING GIVES UP ON A MAYBE-LIVE BROADCAST IT SAYS SO. No new machinery: a durable fault on the row (so it
// survives a restart and shows on the destination), or an escalation when the row is gone.
const xcLeftLive = "polyemesis may have left a broadcast live on X"

func TestXGivingUpOnAMaybeLiveBroadcastRaisesADurableFault(t *testing.T) {
	now := time.Now()
	fake := &xcFake{} // nothing on the list, ever: a lost answer whose broadcast never shows
	c, store, faults := xcFixture(t, fake, true, xcRow(false, db.BroadcastControl{PendingSource: xcSourceID,
		GoLiveAttemptMS: now.UnixMilli(), GoLiveSentMS: now.UnixMilli(), Phase: phaseCreated}))
	clock := now
	c.now = func() time.Time { return clock }
	xcSweep(t, c, store) // the one read after the switch-off, inside the window: nothing found yet
	if store.rows[5].Lifecycle.Fault != "" {
		t.Fatalf("a fault inside the window: %q", store.rows[5].Lifecycle.Fault)
	}
	clock = now.Add(goLiveUnresolvedFor + time.Minute) // the window has passed and the row would go quiet
	for range 3 {
		xcSweep(t, c, store)
	}
	row := store.rows[5]
	if !strings.Contains(row.Lifecycle.Fault, xcLeftLive) || !strings.Contains(row.Lifecycle.Fault, "producer tooling") || !row.Lifecycle.Terminal {
		t.Fatalf("fault %q terminal %v, want the left-live fault on the row", row.Lifecycle.Fault, row.Lifecycle.Terminal)
	}
	if len(*faults) != 1 || !strings.Contains((*faults)[0].Detail, xcLeftLive) {
		t.Errorf("escalations = %+v, want exactly one, naming the possible live broadcast", *faults)
	}
}

func TestXGivingUpOnADeletedMaybeLiveBroadcastEscalates(t *testing.T) {
	now := time.Now()
	fake := &xcFake{active: true, dropLive: true}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{PendingSource: xcSourceID, Phase: phaseCreated}))
	clock := now
	c.now = func() time.Time { return clock }
	c.sweepOnce(context.Background(), sweepEverything)                      // go-live sent, answer lost
	fake.set(func(f *xcFake) { f.dropLive, f.state, f.id = false, "", "" }) // and nothing ever appears on the list
	store.mu.Lock()
	delete(store.rows, 5)
	store.mu.Unlock()
	clock = now.Add(goLiveUnresolvedFor + time.Minute)
	for range 3 {
		c.sweepOnce(context.Background(), sweepEverything)
	}
	n := 0
	for _, f := range *faults {
		if strings.Contains(f.Detail, xcLeftLive) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("escalations = %+v, want exactly one saying polyemesis may have left a broadcast live on X", *faults)
	}
}

// The give-up fault holds like every other terminal fault: set at the give-up count, so a maybe-live row that is
// switched off and on again with the encoder sending does NOT send another go-live until the fault is cleared.
func TestXLeftLiveFaultHoldsAReEnabledRowUntilTheFaultIsCleared(t *testing.T) {
	now := time.Now()
	fake := &xcFake{active: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(false, db.BroadcastControl{PendingSource: xcSourceID,
		GoLiveAttemptMS: now.UnixMilli(), GoLiveSentMS: now.UnixMilli(), Phase: phaseCreated}))
	clock := now
	c.now = func() time.Time { return clock }
	xcSweep(t, c, store)
	clock = now.Add(goLiveUnresolvedFor + time.Minute)
	for range 2 {
		xcSweep(t, c, store)
	}
	lc := store.rows[5].Lifecycle
	if lc.Fault != leftLiveFault || !lc.Terminal || lc.Attempts != lifecycleGiveUpAfter {
		t.Fatalf("recorded = %+v, want the left-live fault, terminal, at the give-up count", lc)
	}
	store.mu.Lock()
	store.rows[5].Enabled = true
	store.mu.Unlock()
	for range 3 {
		clock = clock.Add(10 * time.Minute) // far past any re-arm interval: a terminal fault is not re-armed either
		xcSweep(t, c, store)
	}
	if fake.liveCalls != 0 {
		t.Fatalf("%d go-live calls for a re-enabled row holding the left-live fault, want none", fake.liveCalls)
	}
}

// If the write that records the give-up fault fails, nothing is alerted and the target stays tracked, so the next sweep
// tries again; the fault is raised exactly once when the write succeeds.
func TestXLeftLiveFaultWriteFailureKeepsTrackingAndDoesNotAlert(t *testing.T) {
	now := time.Now()
	fake := &xcFake{}
	c, store, faults := xcFixture(t, fake, true, xcRow(false, db.BroadcastControl{PendingSource: xcSourceID,
		GoLiveAttemptMS: now.UnixMilli(), GoLiveSentMS: now.UnixMilli(), Phase: phaseCreated}))
	clock := now
	c.now = func() time.Time { return clock }
	xcSweep(t, c, store) // inside the window: reads once, goes quiet
	clock = now.Add(goLiveUnresolvedFor + time.Minute)
	store.mu.Lock()
	store.failUpdates = true
	store.mu.Unlock()
	for range 3 {
		xcSweep(t, c, store)
	}
	if len(*faults) != 0 {
		t.Fatalf("escalations = %+v with the fault unwritable, want none", *faults)
	}
	c.mu.Lock()
	_, tracked := c.tracked[5]
	c.mu.Unlock()
	if !tracked || store.rows[5].Lifecycle.Fault != "" {
		t.Fatalf("tracked=%v fault=%q, want the target kept and no fault recorded yet", tracked, store.rows[5].Lifecycle.Fault)
	}
	store.mu.Lock()
	store.failUpdates = false
	store.mu.Unlock()
	for range 3 {
		xcSweep(t, c, store)
	}
	if store.rows[5].Lifecycle.Fault != leftLiveFault || len(*faults) != 1 {
		t.Fatalf("fault %q escalations %d, want the fault recorded and raised exactly once after the write recovered",
			store.rows[5].Lifecycle.Fault, len(*faults))
	}
}
