package api

// The X half of the broadcast lifecycle coordinator. X's broadcast is OPENED on
// the stream source (NOT_STARTED, invisible), PUBLISHED when bytes are arriving,
// ENDED when the operator switches the destination off, and RENEWED when the
// operator goes on air again after a finished show. None of the YouTube tests in
// lifecycle_test.go cover those words, so this file covers them.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// xcFake is the X Livestream API as the coordinator sees it: one account, one
// source, and a broadcast whose state the test moves by hand.
type xcFake struct {
	mu sync.Mutex
	// state is the current broadcast's X state word. Empty means none exists yet.
	state string
	id    string
	// active is whether the source is receiving bytes (is_stream_active).
	active bool
	// opened records every source key a create call carried, in order.
	opened []string
	// published and ended count the writes that reached the wire.
	published int
	ended     int
	// publishBodies records the raw body of every PUBLISH, so a test can read the title X was sent.
	publishBodies []map[string]any
	// createStatus, when non-zero, makes create fail with that HTTP status.
	createStatus int
	// createDetail, when set, is the problem detail a refused create carries.
	createDetail string
	// creates counts every create call, refused or not.
	creates int
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
		f.requests++
		if f.limited {
			w.Header().Set("x-rate-limit-reset", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"title":"Too Many Requests","detail":"slow down"}`)
			return
		}
		path := r.URL.Path
		switch {
		case path == "/2/users/me":
			_, _ = io.WriteString(w, `{"data":{"id":"42","username":"streamer"}}`)
		case strings.HasSuffix(path, "/sources"):
			active := "false"
			if f.active {
				active = "true"
			}
			_, _ = io.WriteString(w, `{"data":{"sources":[{"id":"src-1","name":"polyemesis",`+
				`"rtmp_stream_key":"`+xcFakeKey+`","is_stream_active":`+active+`}]}}`)
		case r.Method == http.MethodPost && path == "/2/users/42/broadcasts":
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
			var body struct {
				SourceID string `json:"source_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.opened = append(f.opened, body.SourceID)
			// X returns the broadcast already running on the source rather than a
			// second one. Only a finished broadcast makes way for a new one.
			if f.state == oauth.XStateNotStarted || f.state == oauth.XStateRunning || f.state == oauth.XStatePrePublished {
				_, _ = io.WriteString(w, xcBroadcastJSON(f.id, f.state))
				return
			}
			f.seq++
			f.id = "bc-" + string(rune('0'+f.seq))
			f.state = oauth.XStateNotStarted
			_, _ = io.WriteString(w, xcBroadcastJSON(f.id, f.state))
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/2/broadcasts/"):
			id := strings.TrimPrefix(path, "/2/broadcasts/")
			if id != f.id {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"errors":[{"title":"Not Found","detail":"no such broadcast"}]}`)
				return
			}
			src := xcFakeKey
			if f.sourceGone {
				src = ""
			}
			_, _ = io.WriteString(w, `{"data":{"id":"`+f.id+`","state":"`+f.state+`","source_id":"`+
				src+`","title":""}}`)
		case r.Method == http.MethodPut && path == "/2/users/42/broadcasts/"+f.id+"/state":
			var raw map[string]any
			_ = json.NewDecoder(r.Body).Decode(&raw)
			f.publishBodies = append(f.publishBodies, raw)
			body := struct{ State string }{}
			if st, ok := raw["state"].(string); ok {
				body.State = st
			}
			if body.State != "PUBLISH" || f.state != oauth.XStateNotStarted {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"errors":[{"title":"Bad Request","detail":"cannot publish"}]}`)
				return
			}
			f.published++
			f.state = oauth.XStateRunning
			_, _ = io.WriteString(w, xcBroadcastJSON(f.id, f.state))
		case r.Method == http.MethodPut && path == "/2/broadcasts/"+f.id+"/state":
			f.ended++
			f.state = oauth.XStateEnded
			_, _ = io.WriteString(w, `{"data":{"state":"ENDED"}}`)
		default:
			t.Errorf("unexpected X request %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (f *xcFake) createCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates
}

func xcBroadcastJSON(id, state string) string {
	return `{"data":{"broadcast":{"id":"` + id + `","state":"` + state + `"},"share_url":"https://x.com/i/broadcasts/` +
		id + `","video_access":{}}}`
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
// The gate is read from the environment when the provider set is built, so it
// is set here, before NewSet.
func xcFixture(t *testing.T, fake *xcFake, gateOpen bool, rows ...*db.Destination) (*lifecycleCoordinator, *fakeLifecycleStore, *[]lifecycleFault) {
	t.Helper()
	if gateOpen {
		t.Setenv("POLYEMESIS_X_LIVESTREAM", "1")
	} else {
		t.Setenv("POLYEMESIS_X_LIVESTREAM", "")
	}
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
		oauth.NewSet(oauth.WithBaseURL(srv.URL)),
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
// broadcast, so the coordinator opens one on the stream source. The broadcast is
// NOT_STARTED and nothing is published while the source is silent. When bytes
// arrive, the next sweep publishes it, and the recorded phase is live.
func TestXCoordinatorOpensOnEnableThenPublishesWhenBytesArrive(t *testing.T) {
	fake := &xcFake{}
	c, store, faults := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{}))

	row := xcSweep(t, c, store)
	opened, published, _, state := fake.snapshot()
	if len(opened) != 1 || opened[0] != xcFakeKey {
		t.Fatalf("first sweep opened %v, want one broadcast on the minted source", opened)
	}
	if published != 0 || state != oauth.XStateNotStarted {
		t.Errorf("silent source: published %d, state %s; want nothing public", published, state)
	}
	if row.Lifecycle.BroadcastID != "bc-1" || row.Lifecycle.Phase != phaseCreated {
		t.Errorf("recorded = %+v, want bc-1 created", row.Lifecycle)
	}

	fake.set(func(f *xcFake) { f.active = true })
	row = xcSweep(t, c, store)
	_, published, _, state = fake.snapshot()
	if published != 1 || state != oauth.XStateRunning {
		t.Errorf("bytes arriving: published %d, state %s; want one publish to RUNNING", published, state)
	}
	if row.Lifecycle.Phase != phaseLive || len(*faults) != 0 {
		t.Errorf("after publish: phase %q faults %v; want live with no faults", row.Lifecycle.Phase, *faults)
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
	opened, _, _, _ := fake.snapshot()
	if len(opened) != 1 {
		t.Fatalf("second show opened %d broadcasts, want exactly one new one", len(opened))
	}
	if row.Lifecycle.BroadcastID == "bc-1" || row.Lifecycle.BroadcastID == "" {
		t.Errorf("recorded broadcast = %q, want a new id, not the ended one", row.Lifecycle.BroadcastID)
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
	if len(opened) != 0 {
		t.Errorf("closed gate opened %d broadcasts", len(opened))
	}
	if len(*faults) != 0 {
		t.Errorf("closed gate raised faults: %v", *faults)
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

// xcLiveReady is a destination whose broadcast is open and NOT_STARTED while bytes arrive, so the next sweep
// publishes it.
func xcLiveReady(t *testing.T, title string) (*lifecycleCoordinator, *fakeLifecycleStore, *xcFake) {
	t.Helper()
	fake := &xcFake{active: true, state: oauth.XStateNotStarted, id: "bc-1", seq: 1}
	row := xcRow(true, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseCreated})
	row.GoLiveTitle = title
	c, store, _ := xcFixture(t, fake, true, row)
	return c, store, fake
}

// TestXPublishCarriesTheDestinationsGoLiveTitle: X takes a title only on PUBLISH, so the coordinator's PUBLISH is
// the one chance to send it. It used to go through the generic transition, which always sent an empty publish and
// left every X broadcast untitled.
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
	if len(opened) != 0 || published != 0 {
		t.Fatalf("ended with bytes still arriving: opened %v, published %d; want nothing reopened", opened, published)
	}
	fake.set(func(f *xcFake) { f.active = false })
	xcSweep(t, c, store)
	if opened, _, _, _ := fake.snapshot(); len(opened) != 1 {
		t.Fatalf("after the encoder stopped: opened %v, want the next show's broadcast", opened)
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
	if opened, _, _, _ := fake.snapshot(); len(opened) != 0 {
		t.Fatalf("a canceled broadcast was reopened: %v", opened)
	}
	if len(*faults) == 0 {
		t.Fatal("a canceled broadcast raised no fault")
	}
	store.mu.Lock()
	store.rows[5].Enabled = false
	store.mu.Unlock()
	if row := xcSweep(t, c, store); row.Lifecycle.BroadcastID != "" || row.Lifecycle.Fault != "" {
		t.Fatalf("switched off: lifecycle = %+v, want the canceled broadcast forgotten", row.Lifecycle)
	}
	store.mu.Lock()
	store.rows[5].Enabled = true
	store.mu.Unlock()
	xcSweep(t, c, store)
	if opened, _, _, _ := fake.snapshot(); len(opened) != 1 {
		t.Fatalf("switched on again: opened %v, want one new broadcast", opened)
	}
}

// open() records a broadcast for every enabled X destination before any bytes
// arrive. Switched off before it aired, that broadcast cannot become anything
// without this coordinator's PUBLISH, so nothing asks X about it.
func TestADisabledXDestinationWhoseBroadcastNeverAiredIsNotReadEverySweep(t *testing.T) {
	fake := &xcFake{state: oauth.XStateNotStarted, id: "bc-1", seq: 1}
	c, store, _ := xcFixture(t, fake, true, xcRow(false, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseCreated}))
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

// The one read after the switch-off is what catches a publish whose record was
// lost: the row says "created", X says RUNNING, and the broadcast is ended.
func TestADisabledXRowRecordedAsCreatedButRunningIsStillEnded(t *testing.T) {
	fake := &xcFake{state: oauth.XStateRunning, id: "bc-1", seq: 1, active: true}
	c, store, _ := xcFixture(t, fake, true, xcRow(false, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseCreated}))
	xcSweep(t, c, store)
	if _, _, ended, _ := fake.snapshot(); ended != 1 {
		t.Fatalf("ended %d, want the running broadcast ended", ended)
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
	if opened, _, _, _ := fake.snapshot(); len(opened) != 0 {
		t.Fatalf("opened %v with the encoder's state unknown, want a wait", opened)
	}
	c.Observe(hooks.Event{Trigger: hooks.TriggerDestinationUp, Destination: &hooks.DestinationRef{ID: 5}})
	xcSweep(t, c, store)
	if opened, _, _, _ := fake.snapshot(); len(opened) != 1 {
		t.Fatalf("after an UP edge opened %v, want the next show's broadcast", opened)
	}
}
