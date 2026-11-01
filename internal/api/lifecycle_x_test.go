package api

// The X half of the broadcast lifecycle coordinator. X's broadcast is OPENED on
// the stream source (NOT_STARTED, invisible), PUBLISHED when bytes are arriving,
// ENDED when the operator switches the destination off, and RENEWED when the
// operator goes on air again after a finished show. None of the YouTube tests in
// lifecycle_test.go cover those words, so this file covers them.

import (
	"context"
	"encoding/json"
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
	// createStatus, when non-zero, makes create fail with that HTTP status.
	createStatus int
	// seq numbers broadcasts so a second show gets a different id.
	seq int
}

func (f *xcFake) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
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
			if f.createStatus != 0 {
				w.WriteHeader(f.createStatus)
				_, _ = io.WriteString(w, `{"errors":[{"title":"Unauthorized","detail":"not permitted","status":`+
					`401}]}`)
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
			_, _ = io.WriteString(w, `{"data":{"id":"`+f.id+`","state":"`+f.state+`","source_id":"`+
				xcFakeKey+`","title":""}}`)
		case r.Method == http.MethodPut && path == "/2/users/42/broadcasts/"+f.id+"/state":
			var body struct {
				State string `json:"state"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
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
func TestXFinishedBroadcastWhileEnabledRenewsRatherThanFaults(t *testing.T) {
	for _, s := range []string{oauth.XStateEnded, oauth.XStateTimedOut, oauth.XStateCanceled} {
		plan := planLifecycle(true, &oauth.BroadcastLifecycleState{Status: s, StreamActive: boolPtr(true)})
		if !plan.Renew || plan.Fault != "" {
			t.Errorf("%s while enabled: plan = %+v, want Renew with no fault", s, plan)
		}
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
	fake := &xcFake{active: true, state: oauth.XStateEnded, id: "bc-1", seq: 1}
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
