package api

// The broadcast lifecycle coordinator.
//
// The platform is a real HTTP server speaking the documented wire -- Google's
// error envelope, items[] of broadcast and stream resources -- rather than a
// stubbed interface, for the reason internal/oauth's own tests give: a fake that
// agrees with the code proves only that the code agrees with itself. It also
// means every assertion about what was SENT is an assertion about bytes that
// left the process, which is the only level at which "it never ends a live
// broadcast" is worth claiming.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// ------------------------------------------------------------------- fixtures

// The three YouTube Data API paths this capability touches. Spelled out rather
// than imported because internal/oauth keeps them unexported; if one moves, the
// stub's own guard below fails on an unexpected path rather than quietly
// answering 404 to a call the coordinator makes.
const (
	ytPathBroadcasts = "/liveBroadcasts"
	ytPathStreams    = "/liveStreams"
	ytPathTransition = "/liveBroadcasts/transition"
)

// ytFake is a YouTube whose answers a test controls and whose requests a test
// can read back.
type ytFake struct {
	mu sync.Mutex
	// status is the broadcast's lifeCycleStatus.
	status string
	// streamStatus is the BOUND stream's status; "active" is the only value
	// that satisfies the transition precondition.
	streamStatus string
	monitor      *bool
	// refuseReason, when set, makes every transition fail with that Google
	// reason. refuseCode is the HTTP status it arrives as.
	refuseReason string
	refuseCode   int
	// sent records every broadcastStatus that reached the wire, in order. It is
	// the evidence for the invariant this file exists to protect.
	sent []string
	// reads counts state reads, so a test can assert the coordinator is not
	// spending quota on a broadcast it has nothing to do to.
	reads int
	// stateErr makes the state read itself fail.
	stateErr bool
}

func (f *ytFake) snapshot() (sent []string, reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...), f.reads
}

func (f *ytFake) set(mutate func(*ytFake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mutate(f)
}

func (f *ytFake) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case ytPathBroadcasts:
			f.reads++
			if f.stateErr {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":500,"message":"backend"}}`))
				return
			}
			monitor := "null"
			if f.monitor != nil {
				monitor = "false"
				if *f.monitor {
					monitor = "true"
				}
			}
			fmt.Fprintf(w, `{"items":[{"id":%q,"snippet":{"title":"show"},`+
				`"status":{"lifeCycleStatus":%q},`+
				`"contentDetails":{"boundStreamId":"stream-1",`+
				`"monitorStream":{"enableMonitorStream":%s}}}]}`,
				"bcast-1", f.status, monitor)
		case ytPathStreams:
			fmt.Fprintf(w, `{"items":[{"status":{"streamStatus":%q}}]}`, f.streamStatus)
		case ytPathTransition:
			to := r.URL.Query().Get("broadcastStatus")
			f.sent = append(f.sent, to)
			if f.refuseReason != "" {
				code := f.refuseCode
				if code == 0 {
					code = http.StatusForbidden
				}
				w.WriteHeader(code)
				fmt.Fprintf(w, `{"error":{"code":%d,"message":"no","errors":`+
					`[{"message":"no","domain":"youtube.liveBroadcast","reason":%q}]}}`,
					code, f.refuseReason)
				return
			}
			f.status = to
			fmt.Fprintf(w, `{"id":"bcast-1","status":{"lifeCycleStatus":%q}}`, to)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// fakeLifecycleStore is db.UpdateLifecycle's contract, in memory: it writes the
// lifecycle column and DISCARDS everything else, so a test cannot accidentally
// come to depend on the coordinator writing something the real store would
// throw away.
type fakeLifecycleStore struct {
	mu   sync.Mutex
	rows map[int64]*db.Destination
	// hideFromList omits rows from ListDestinations WITHOUT deleting them,
	// which is what a scoped or paged query would do to a row that still
	// exists. GetDestination still finds them, which is the whole point.
	hideFromList map[int64]bool
	// beforeUpdate runs inside the "transaction", before apply, so a test can
	// move the world exactly where a real race would move it.
	beforeUpdate func(id int64, cur *db.Destination)
}

// GetDestination answers what the real store answers: the row, or
// db.ErrNotFound. endOrphan uses it to CONFIRM a deletion rather than infer one
// from a listing, so a test can make the listing lie -- which is the whole
// hazard -- and watch the confirmation refuse to act on it.
func (f *fakeLifecycleStore) GetDestination(id int64) (*db.Destination, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, db.ErrNotFound
}

func (f *fakeLifecycleStore) ListDestinations() ([]*db.Destination, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*db.Destination, 0, len(f.rows))
	for id, r := range f.rows {
		if f.hideFromList[id] {
			continue
		}
		cp := *r
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeLifecycleStore) UpdateLifecycle(id int64, apply func(*db.Destination) bool) (*db.Destination, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	cur := *row
	if f.beforeUpdate != nil {
		f.beforeUpdate(id, &cur)
		stored := *row
		stored.Enabled = cur.Enabled
		f.rows[id] = &stored
		row = &stored
	}
	if !apply(&cur) {
		return nil, db.ErrLifecycleSkipped
	}
	// ONLY the lifecycle column, exactly as db.UpdateLifecycle does.
	updated := *row
	updated.Lifecycle = cur.Lifecycle
	f.rows[id] = &updated
	cp := updated
	return &cp, nil
}

func (f *fakeLifecycleStore) get(id int64) db.Destination {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.rows[id]
}

func (f *fakeLifecycleStore) remove(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, id)
}

func (f *fakeLifecycleStore) setEnabled(id int64, enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *f.rows[id]
	cp.Enabled = enabled
	f.rows[id] = &cp
}

func lifecycleTestRow(enabled bool, phase string) *db.Destination {
	acct := int64(9)
	src := int64(1)
	return &db.Destination{
		ID: 3, Name: "yt main", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		AccountID: &acct, SourceID: &src, Enabled: enabled,
		Lifecycle: db.BroadcastControl{BroadcastID: "bcast-1", Phase: phase},
	}
}

// lifecycleFixture wires a coordinator to the fake platform and the fake store.
func lifecycleFixture(t *testing.T, yt *ytFake, rows ...*db.Destination) (*lifecycleCoordinator, *fakeLifecycleStore, *[]lifecycleFault) {
	t.Helper()
	srv := httptest.NewServer(yt.handler(t))
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
			return &db.PlatformAccount{ID: 9, Platform: db.PlatformYouTube, AccessToken: "tok"}, nil
		},
		func(f lifecycleFault) { faults = append(faults, f) },
	)
	return c, store, &faults
}

// -------------------------------------------------------------- THE INVARIANT

// THE ONE THAT MATTERS. A transition failure never stops the broadcast.
//
// This is the only rule in the coordinator whose violation could take a show off
// the air, so it is asserted at the wire: whatever the platform answers, and
// however many times it answers it, the string "complete" must never leave this
// process for a destination the operator has enabled.
//
// The refusals below are every one internal/oauth classifies plus an
// unclassified 500, because the tempting place to write a cleanup is inside a
// branch that handles one of them -- and a test that only exercised the happy
// refusal would pass while `complete` was being sent for channelFull.
//
// WHY THE RULE IS NOT MERELY CAUTIOUS: YouTube requires an ACTIVE INGEST to
// transition. Stopping the stream because a transition failed destroys the one
// condition under which a retry could ever succeed, so the "safe" cleanup is the
// action that makes the failure permanent.
//
// MUTATION OBSERVED RED. In handleRefusal, add an end-on-failure -- e.g. after
// noteFailure, call c.end(...) with oauth.PhaseComplete, or simply have advance
// send oauth.PhaseComplete when TransitionBroadcast returns an error. The
// failure message is the one below, naming the transition that reached the wire.
func TestAFailedTransitionNeverEndsTheBroadcastOfAnEnabledDestination(t *testing.T) {
	refusals := []struct {
		name   string
		reason string
		code   int
	}{
		{"no bytes are arriving yet", "errorStreamInactive", http.StatusForbidden},
		{"the machine will not go from here to there", "invalidTransition", http.StatusForbidden},
		{"the channel is at its concurrent broadcast ceiling", "concurrentBroadcastsExceedLimit", http.StatusForbidden},
		{"polyemesis's own shared ingest ceiling", "sharedIngestionBroadcastsExceedLimit", http.StatusForbidden},
		{"the platform wants us to slow down", "userRequestsExceedRateLimit", http.StatusForbidden},
		{"a transient platform error", "errorExecutingTransition", http.StatusForbidden},
		{"an unclassified failure", "somethingNobodyDocumented", http.StatusInternalServerError},
	}

	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			active := true
			monitorOff := false
			yt := &ytFake{
				status: "testing", streamStatus: "active",
				monitor: &monitorOff, refuseReason: tc.reason, refuseCode: tc.code,
			}
			// ENABLED: the operator wants this destination running, which is
			// what a destination that is delivering always looks like.
			c, store, faults := lifecycleFixture(t, yt, lifecycleTestRow(true, "testing"))
			_ = active

			// Well past the give-up budget, because a cleanup written as a
			// last resort would fire at the end rather than at the start.
			for i := 0; i < lifecycleGiveUpAfter+5; i++ {
				c.sweepOnce(context.Background(), sweepEverything)
			}

			sent, _ := yt.snapshot()
			for _, to := range sent {
				if to == string(oauth.PhaseComplete) {
					t.Fatalf("the coordinator sent a %q transition for an ENABLED destination "+
						"after the platform refused with %q. A completed broadcast cannot "+
						"return to live, so this permanently destroys a show that was still "+
						"being delivered -- and it destroys the active ingest that is the "+
						"only condition under which a retry could have succeeded. "+
						"transitions sent: %v",
						oauth.PhaseComplete, tc.reason, sent)
				}
			}
			if got := store.get(3); !got.Enabled {
				t.Fatal("the destination was disabled: the coordinator has reached the " +
					"operator's run/stop intent, which db.UpdateLifecycle exists to make " +
					"impossible")
			}
			// And the failure was not swallowed: escalation is the whole of the
			// permitted response, so its absence would mean a broadcast quietly
			// stuck with nobody told.
			if tc.reason != "errorStreamInactive" && len(*faults) == 0 {
				t.Errorf("nothing was escalated for %q; a fault that stops nothing must at "+
					"least tell somebody", tc.reason)
			}
			if tc.reason == "errorStreamInactive" && len(*faults) != 0 {
				t.Errorf("%q was escalated; it is the ordinary state of every broadcast "+
					"before its encoder connects, and paging somebody for it teaches them "+
					"to ignore this alert", tc.reason)
			}
		})
	}
}

// A crashed process is not an operator ending a show.
//
// A completed YouTube broadcast cannot return to live, so ending on a crash
// destroys a show the operator would otherwise have got back for free: the
// supervisor reconnects to the same key and the same bound stream, and the
// broadcast is still live. The platform's own answer for a stream that never
// comes back is enableAutoStop, which knows more about the ingest than this
// process does.
//
// MUTATION OBSERVED RED: add lifecycleReasonStopped to the wake list in
// Observe. The edge is then accepted, and -- combined with any future branch
// that treats "we were woken about this" as intent -- the crash becomes an end.
func TestACrashedDestinationIsNeverEndedOnTheStrengthOfItsCrash(t *testing.T) {
	monitorOff := false
	yt := &ytFake{status: "live", streamStatus: "inactive", monitor: &monitorOff}
	c, _, _ := lifecycleFixture(t, yt, lifecycleTestRow(true, "live"))

	crash := hooks.Event{
		Trigger:     hooks.TriggerDestinationDown,
		Reason:      lifecycleReasonStopped,
		Destination: &hooks.DestinationRef{ID: 3, Name: "yt main", Platform: "youtube"},
	}
	c.Observe(crash)
	select {
	case id := <-c.wake:
		t.Fatalf("a crash woke the coordinator (destination %d). The next thing that happens "+
			"to a crashed destination is that it reconnects; anything that reads this edge "+
			"as an intent to end is reading a fault as a decision", id)
	default:
	}

	// And the sweep, which runs anyway, still sends nothing that could end it:
	// the row is enabled, because a crash does not change what the operator
	// asked for.
	c.sweepOnce(context.Background(), sweepEverything)
	sent, _ := yt.snapshot()
	for _, to := range sent {
		if to == string(oauth.PhaseComplete) {
			t.Fatalf("an FFmpeg crash ended the broadcast; transitions sent: %v", sent)
		}
	}
}

// The END policy is a switch on three free-text strings produced in another
// package. This drives the real hooks.Watcher through the three transitions and
// fails if a spelling here stops matching what it emits.
//
// Without this, renaming "disabled" to "disabled by operator" in
// internal/hooks/watch.go would compile, pass every test in that package, and
// silently stop every broadcast from ever being ended -- with no error anywhere.
func TestTheDownReasonsAreTheOnesTheWatcherActuallyEmits(t *testing.T) {
	w := hooks.NewWatcher(hooks.SourceRef{ID: 1}, hooks.WatchConfig{DestinationDownAfter: 0})
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	up := func(at time.Time) alerts.Snapshot {
		return alerts.Snapshot{At: at, Destinations: []alerts.DestState{
			{ID: 3, Name: "yt", Platform: "youtube", Enabled: true, Running: true},
		}}
	}

	reasonOf := func(evs []hooks.Event) string {
		t.Helper()
		for _, ev := range evs {
			if ev.Trigger == hooks.TriggerDestinationDown {
				return ev.Reason
			}
		}
		t.Fatal("no destination.down event was produced")
		return ""
	}

	// A process fault: still enabled, no longer running.
	w.Observe(up(base))
	got := reasonOf(w.Observe(alerts.Snapshot{At: base.Add(time.Second), Destinations: []alerts.DestState{
		{ID: 3, Name: "yt", Platform: "youtube", Enabled: true, Running: false},
	}}))
	if got != lifecycleReasonStopped {
		t.Errorf("a crash reads as %q, but this package switches on %q -- a crash would now "+
			"be treated as whatever the default branch does", got, lifecycleReasonStopped)
	}

	// The operator switching it off.
	w2 := hooks.NewWatcher(hooks.SourceRef{ID: 1}, hooks.WatchConfig{DestinationDownAfter: 0})
	w2.Observe(up(base))
	got = reasonOf(w2.Observe(alerts.Snapshot{At: base.Add(time.Second), Destinations: []alerts.DestState{
		{ID: 3, Name: "yt", Platform: "youtube", Enabled: false, Running: false},
	}}))
	if got != lifecycleReasonDisabled {
		t.Errorf("a deliberate disable reads as %q, but this package switches on %q -- no "+
			"broadcast would ever be ended", got, lifecycleReasonDisabled)
	}

	// The row being deleted.
	w3 := hooks.NewWatcher(hooks.SourceRef{ID: 1}, hooks.WatchConfig{DestinationDownAfter: 0})
	w3.Observe(up(base))
	got = reasonOf(w3.Observe(alerts.Snapshot{At: base.Add(time.Second)}))
	if got != lifecycleReasonRemoved {
		t.Errorf("a deleted destination reads as %q, but this package switches on %q",
			got, lifecycleReasonRemoved)
	}
}

// ------------------------------------------------------------------- the plan

// The whole state machine, as a table. Pure because the policy has to be
// readable in one place -- the alternative is seven HTTP fixtures whose meaning
// is spread over the lines that build them.
func TestPlanLifecycle(t *testing.T) {
	yes, no := true, false
	state := func(status string, active, monitor *bool) *oauth.BroadcastLifecycleState {
		return &oauth.BroadcastLifecycleState{
			BroadcastID: "b", Status: status, BoundStreamID: "s",
			StreamStatus: "active", StreamActive: active, MonitorStream: monitor,
		}
	}

	tests := []struct {
		name     string
		enabled  bool
		st       *oauth.BroadcastLifecycleState
		wantSend oauth.BroadcastPhase
		wantEnd  bool
		fault    bool
	}{
		{"already live: idempotency by asking, so nothing is spent",
			true, state("live", &yes, &yes), "", false, false},
		{"testing with bytes arriving goes live",
			true, state("testing", &yes, &yes), oauth.PhaseLive, false, false},
		{"ready with a monitor stream and bytes goes to testing first",
			true, state("ready", &yes, &yes), oauth.PhaseTesting, false, false},
		{"created with NO monitor stream goes straight live",
			true, state("created", &yes, &no), oauth.PhaseLive, false, false},
		{"created with an unknown monitor stream still tries, loudly",
			true, state("created", &yes, nil), oauth.PhaseLive, false, false},
		{"no bytes yet: wait rather than spend a transition to be refused",
			true, state("ready", &no, &yes), "", false, false},
		{"bytes unknown is not bytes arriving",
			true, state("ready", nil, &yes), "", false, false},
		{"mid-transition: the platform is already moving it",
			true, state("liveStarting", &yes, &yes), "", false, false},
		{"a completed broadcast cannot return, so this is terminal",
			true, state("complete", &yes, &yes), "", false, true},
		{"a revoked broadcast is terminal too",
			true, state("revoked", &yes, &yes), "", false, true},

		{"disabled and live: END, the only branch that can",
			false, state("live", &yes, &yes), oauth.PhaseComplete, true, false},
		{"disabled and testing: END, it was on the monitor",
			false, state("testing", &yes, &yes), oauth.PhaseComplete, true, false},
		{"disabled mid-transition: END",
			false, state("liveStarting", &yes, &yes), oauth.PhaseComplete, true, false},
		{"disabled and never aired: nothing to end",
			false, state("created", &no, &yes), "", false, false},
		{"disabled and already complete: nothing to end",
			false, state("complete", &yes, &yes), "", false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planLifecycle(tc.enabled, tc.st)
			if got.Send != tc.wantSend {
				t.Fatalf("Send = %q, want %q", got.Send, tc.wantSend)
			}
			if (got.Fault != "") != tc.fault {
				t.Fatalf("Fault = %q, want a fault: %v", got.Fault, tc.fault)
			}
			if tc.enabled && got.Send == oauth.PhaseComplete {
				t.Fatal("an ENABLED destination planned an end. Every enabled branch may " +
					"send testing or live and nothing else; this is the structural half of " +
					"the rule that a coordinator cannot stop a show")
			}
			if got.Send == oauth.PhaseComplete && !tc.wantEnd {
				t.Fatal("an end was planned where none was wanted")
			}
		})
	}
}

// No enabled input can ever produce an end, whatever the platform says --
// including words the platform has never sent and words no build recognises.
//
// The table above enumerates; this quantifies. It is the assertion that survives
// a future platform, a future phase name, and a garbled response.
func TestNoEnabledDestinationCanEverPlanAnEnd(t *testing.T) {
	yes, no := true, false
	phases := []string{
		"created", "ready", "testing", "testStarting", "live", "liveStarting",
		"complete", "revoked",
		// Things YouTube has never sent, a different platform might, and a
		// corrupted response could.
		"", "LIVE", "ended", "stopping", "abandoned", "deleted", "🙂",
	}
	for _, phase := range phases {
		for _, active := range []*bool{nil, &yes, &no} {
			for _, monitor := range []*bool{nil, &yes, &no} {
				plan := planLifecycle(true, &oauth.BroadcastLifecycleState{
					BroadcastID: "b", Status: phase, BoundStreamID: "s",
					StreamActive: active, MonitorStream: monitor,
				})
				if plan.Send == oauth.PhaseComplete {
					t.Fatalf("phase %q (active=%v monitor=%v) planned an END for a "+
						"destination the operator has enabled", phase, active, monitor)
				}
			}
		}
	}
}

// ------------------------------------------------------------------ the sweeps

// The END an operator asked for, end to end.
func TestDisablingADestinationEndsItsBroadcast(t *testing.T) {
	monitorOn := true
	yt := &ytFake{status: "live", streamStatus: "active", monitor: &monitorOn}
	c, store, _ := lifecycleFixture(t, yt, lifecycleTestRow(false, "live"))

	c.sweepOnce(context.Background(), sweepEverything)

	sent, _ := yt.snapshot()
	if len(sent) != 1 || sent[0] != string(oauth.PhaseComplete) {
		t.Fatalf("transitions sent = %v, want exactly one %q", sent, oauth.PhaseComplete)
	}
	if got := store.get(3).Lifecycle.Phase; !strings.EqualFold(got, "complete") {
		t.Errorf("recorded phase = %q, want the platform's word for finished", got)
	}
}

// The compare-and-set, exercised where it actually matters: the operator changes
// their mind while the coordinator is away asking the platform two questions.
//
// MUTATION OBSERVED RED: drop the `if cur.Enabled { return false }` guard from
// end(). The transition then goes out and a destination that is delivering
// loses its broadcast for good.
func TestReEnablingDuringTheStateReadCancelsTheEnd(t *testing.T) {
	monitorOn := true
	yt := &ytFake{status: "live", streamStatus: "active", monitor: &monitorOn}
	c, store, _ := lifecycleFixture(t, yt, lifecycleTestRow(false, "live"))

	// The operator presses the switch back on in the window between the state
	// read and the claim -- which is exactly where db.UpdateLifecycle re-reads.
	store.beforeUpdate = func(id int64, cur *db.Destination) { cur.Enabled = true }

	c.sweepOnce(context.Background(), sweepEverything)

	sent, _ := yt.snapshot()
	for _, to := range sent {
		if to == string(oauth.PhaseComplete) {
			t.Fatalf("the end was sent for a destination that had just been re-enabled; "+
				"transitions sent: %v", sent)
		}
	}
}

// A destination that has never been through the coordinator is not its business.
//
// This is the whole upgrade story: every row in every existing install decodes
// to a zero lifecycle block, and a disabled one of those must not have its
// broadcast ended by a daemon that has never confirmed anything about it.
func TestADisabledRowWithNoRecordedPhaseIsLeftAlone(t *testing.T) {
	yt := &ytFake{status: "live", streamStatus: "active"}
	row := lifecycleTestRow(false, "")
	// Only the announcement mirror, as an upgraded row has.
	row.Lifecycle = db.BroadcastControl{}
	row.Facebook.BroadcastID = "bcast-1"
	c, _, _ := lifecycleFixture(t, yt, row)

	c.sweepOnce(context.Background(), sweepEverything)

	sent, reads := yt.snapshot()
	if len(sent) != 0 {
		t.Fatalf("transitions sent = %v, want none", sent)
	}
	if reads != 0 {
		t.Errorf("the platform was asked %d times about a destination this daemon has never "+
			"confirmed anything about", reads)
	}
}

// A settled broadcast costs nothing.
//
// BroadcastState is TWO API calls. At a fifteen-second tick, re-reading a
// confirmed-live broadcast is over eleven thousand quota units a day per
// destination against YouTube's default ten thousand -- so a coordinator that
// asked "just to be sure" would run every install out of quota by
// mid-afternoon and then be unable to END anything either.
func TestAConfirmedLiveBroadcastIsNotAskedAboutAgain(t *testing.T) {
	yt := &ytFake{status: "live", streamStatus: "active"}
	c, _, _ := lifecycleFixture(t, yt, lifecycleTestRow(true, "live"))

	for i := 0; i < 10; i++ {
		c.sweepOnce(context.Background(), sweepEverything)
	}
	sent, reads := yt.snapshot()
	if reads != 0 || len(sent) != 0 {
		t.Fatalf("a settled broadcast cost %d state reads and %v transitions", reads, sent)
	}
}

// A deleted row still gets its broadcast ended, from the mirrored tuple.
//
// The "removed" event carries only {id, name, platform}: the account id and the
// broadcast id are in a row that no longer exists. Without the mirror there is
// nothing left that can name the broadcast, and the only backstop is the
// platform's own automatic stop.
func TestDeletingADestinationEndsItsBroadcast(t *testing.T) {
	monitorOn := true
	yt := &ytFake{status: "live", streamStatus: "active", monitor: &monitorOn}
	c, store, _ := lifecycleFixture(t, yt, lifecycleTestRow(true, "live"))

	// One sweep to adopt it, then the row goes away.
	c.sweepOnce(context.Background(), sweepEverything)
	store.remove(3)
	c.sweepOnce(context.Background(), sweepEverything)

	sent, _ := yt.snapshot()
	if len(sent) != 1 || sent[0] != string(oauth.PhaseComplete) {
		t.Fatalf("transitions sent = %v, want exactly one %q for the deleted destination",
			sent, oauth.PhaseComplete)
	}
}

// Boot reconciliation: a daemon that was stopped while a destination was
// disabled comes back and finds the broadcast still on air.
func TestTheFirstSweepEndsABroadcastLeftOnAirByAStoppedDaemon(t *testing.T) {
	monitorOn := true
	yt := &ytFake{status: "live", streamStatus: "active", monitor: &monitorOn}
	// Exactly what a restart finds: a disabled row holding a live phase.
	c, _, _ := lifecycleFixture(t, yt, lifecycleTestRow(false, "live"))

	// The loop performs ONE SWEEP BEFORE ITS FIRST TICK, and that sweep is boot
	// reconciliation. Asserted through the loop rather than by calling
	// sweepOnce, because the property under test is the ordering inside the
	// loop: with the sweep after the ticker, this end would wait a full
	// lifecycleTick and, more importantly, the loop's behaviour after a restart
	// would differ from its behaviour in steady state.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); c.lifecycleLoop(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	var sent []string
	for time.Now().Before(deadline) {
		if sent, _ = yt.snapshot(); len(sent) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if len(sent) != 1 || sent[0] != string(oauth.PhaseComplete) {
		t.Fatalf("the boot sweep sent %v, want one %q well inside one %s tick -- without a "+
			"sweep before the first tick a broadcast survives every restart until the "+
			"platform's automatic stop closes it", sent, oauth.PhaseComplete, lifecycleTick)
	}
}

// The shutdown drain ends what was asked for and starts nothing.
//
// A clean stop produces no sweep, so without this an operator who disables a
// destination and immediately stops the daemon leaves a broadcast on air. And a
// box on its way down must never put one in FRONT of an audience it is about to
// stop feeding.
func TestTheDrainEndsButNeverGoesLive(t *testing.T) {
	monitorOff := false
	yt := &ytFake{status: "testing", streamStatus: "active", monitor: &monitorOff}
	going := lifecycleTestRow(true, "testing")
	ending := lifecycleTestRow(false, "live")
	ending.ID, ending.Name = 4, "yt second"
	c, _, _ := lifecycleFixture(t, yt, going, ending)

	c.drain(context.Background())

	sent, _ := yt.snapshot()
	for _, to := range sent {
		if to == string(oauth.PhaseLive) || to == string(oauth.PhaseTesting) {
			t.Errorf("the drain put a broadcast on air on the way down; sent: %v", sent)
		}
	}
	found := false
	for _, to := range sent {
		if to == string(oauth.PhaseComplete) {
			found = true
		}
	}
	if !found {
		t.Errorf("the drain ended nothing; a destination disabled seconds before shutdown "+
			"keeps its broadcast on air until the platform's automatic stop. sent: %v", sent)
	}
}

// A fault holds after the budget rather than escalating into an action, and the
// stream is never touched on the way there.
func TestAHeldFaultStopsCallingAndLeavesTheStreamAlone(t *testing.T) {
	monitorOff := false
	yt := &ytFake{
		status: "testing", streamStatus: "active", monitor: &monitorOff,
		refuseReason: "concurrentBroadcastsExceedLimit",
	}
	c, store, faults := lifecycleFixture(t, yt, lifecycleTestRow(true, "testing"))

	for i := 0; i < lifecycleGiveUpAfter+10; i++ {
		c.sweepOnce(context.Background(), sweepEverything)
	}
	sent, _ := yt.snapshot()
	if len(sent) > lifecycleGiveUpAfter {
		t.Errorf("%d transitions were sent past a give-up budget of %d; the coordinator is "+
			"hammering a platform that has already said no", len(sent), lifecycleGiveUpAfter)
	}
	if len(*faults) != 1 {
		t.Errorf("escalated %d times for one unbroken run of the same failure; an operator "+
			"who cannot resolve this without acting would get one alert every fifteen "+
			"seconds", len(*faults))
	}
	got := store.get(3)
	if !got.Enabled {
		t.Fatal("the destination was disabled by a held fault")
	}
	if got.Lifecycle.Fault == "" {
		t.Error("no fault was recorded on the row, so the destination card says nothing " +
			"about a broadcast that will not start")
	}
	// The two ceilings must not read the same. Telling somebody to stop another
	// broadcast when they have hit polyemesis's own shared-ingest limit sends
	// them to fix something that is not their fault and will not help.
	channelFull := got.Lifecycle.Fault
	yt2 := &ytFake{
		status: "testing", streamStatus: "active", monitor: &monitorOff,
		refuseReason: "sharedIngestionBroadcastsExceedLimit",
	}
	c2, store2, _ := lifecycleFixture(t, yt2, lifecycleTestRow(true, "testing"))
	c2.sweepOnce(context.Background(), sweepEverything)
	if shared := store2.get(3).Lifecycle.Fault; shared == channelFull {
		t.Error("the channel ceiling and the shared-ingest ceiling produced the same " +
			"sentence; they need different actions from the operator")
	}
}

// An UP edge clears a stale give-up, so a destination that starts delivering
// again is not kept off the air by bookkeeping.
func TestComingBackUpClearsAHeldFault(t *testing.T) {
	monitorOff := false
	yt := &ytFake{
		status: "testing", streamStatus: "active", monitor: &monitorOff,
		refuseReason: "errorExecutingTransition",
	}
	c, store, _ := lifecycleFixture(t, yt, lifecycleTestRow(true, "testing"))
	for i := 0; i < lifecycleGiveUpAfter; i++ {
		c.sweepOnce(context.Background(), sweepEverything)
	}
	if store.get(3).Lifecycle.Attempts < lifecycleGiveUpAfter {
		t.Fatalf("the fixture did not reach the give-up budget: %+v", store.get(3).Lifecycle)
	}

	yt.set(func(f *ytFake) { f.refuseReason = "" })
	c.Observe(hooks.Event{
		Trigger:     hooks.TriggerDestinationUp,
		Destination: &hooks.DestinationRef{ID: 3, Name: "yt main", Platform: "youtube"},
	})
	c.sweepOnce(context.Background(), sweepEverything)

	if got := store.get(3); got.Lifecycle.Fault != "" || !strings.EqualFold(got.Lifecycle.Phase, "live") {
		t.Fatalf("a destination that came back up was left held: %+v", got.Lifecycle)
	}
}

// The engine's observe gate. An install with no lifecycle destination must not
// start paying for a status snapshot every two seconds.
func TestWantedIsOffUntilThereIsSomethingToDrive(t *testing.T) {
	yt := &ytFake{status: "live", streamStatus: "active"}
	plain := lifecycleTestRow(true, "")
	plain.Platform, plain.Lifecycle = db.PlatformTwitch, db.BroadcastControl{}
	c, store, _ := lifecycleFixture(t, yt, plain)

	c.sweepOnce(context.Background(), sweepEverything)
	if c.Wanted() {
		t.Fatal("an install with no lifecycle destination asked the engine to build a " +
			"status snapshot every two seconds")
	}

	store.mu.Lock()
	store.rows[3] = lifecycleTestRow(true, "live")
	store.mu.Unlock()
	c.sweepOnce(context.Background(), sweepEverything)
	if !c.Wanted() {
		t.Fatal("a destination with a broadcast to drive did not turn the engine's edges on")
	}
}

// A destination pointed at a platform that cannot be commanded must not keep a
// phase recorded against the platform it left.
func TestSwitchingPlatformClearsTheRecordedBroadcastState(t *testing.T) {
	yt := &ytFake{status: "live", streamStatus: "active"}
	row := lifecycleTestRow(true, "live")
	row.Platform = db.PlatformTwitch
	c, store, _ := lifecycleFixture(t, yt, row)

	c.sweepOnce(context.Background(), sweepEverything)

	if got := store.get(3).Lifecycle; !got.Empty() {
		t.Fatalf("lifecycle = %+v, want it cleared: a phase recorded against a YouTube "+
			"broadcast must never be attributed to a Twitch destination", got)
	}
}

// A state read that fails is a fault, never an action.
func TestAnUnreadableStateChangesNothing(t *testing.T) {
	yt := &ytFake{status: "live", streamStatus: "active", stateErr: true}
	c, store, faults := lifecycleFixture(t, yt, lifecycleTestRow(false, "live"))

	c.sweepOnce(context.Background(), sweepEverything)

	sent, _ := yt.snapshot()
	if len(sent) != 0 {
		t.Fatalf("a transition was sent on a state read that failed: %v", sent)
	}
	if len(*faults) != 1 {
		t.Errorf("escalations = %d, want 1", len(*faults))
	}
	if got := store.get(3).Lifecycle.Phase; !strings.EqualFold(got, "live") {
		t.Errorf("phase = %q, want the last thing the platform actually said; a failed read "+
			"must never overwrite a known phase with a guess", got)
	}
}

// ------------------------------------------------------------------- the guard

// The coordinator holds no handle on anything that runs a process.
//
// This is the first of the three structural reasons a transition failure cannot
// stop a stream, and unlike the other two it is a property of a FILE rather than
// of a function -- so it is checked by reading the file. lifecycle_wiring.go
// exists precisely so that the escalation path, which does need the manager, is
// somewhere else.
//
// MUTATION OBSERVED RED: add `s.mgr.Reconcile()` (or any engine reference) to
// internal/api/lifecycle.go.
func TestTheLifecycleCoordinatorCannotReachAnythingThatRunsAProcess(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "lifecycle.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse lifecycle.go: %v", err)
	}
	for _, imp := range f.Imports {
		if strings.Contains(imp.Path.Value, "internal/engine") ||
			strings.Contains(imp.Path.Value, "internal/supervisor") ||
			strings.Contains(imp.Path.Value, "internal/ffmpeg") {
			t.Errorf("lifecycle.go imports %s. The coordinator must hold nothing that can "+
				"start, stop or reconfigure an output; the escalation wiring that needs the "+
				"manager lives in lifecycle_wiring.go for exactly this reason",
				imp.Path.Value)
		}
	}

	// Identifiers that are how a process gets stopped in this codebase. Checked
	// as selector names, so `s.mgr`, `.Reconcile()` and `.Stop()` are all caught
	// wherever they appear.
	forbidden := map[string]string{
		"mgr":                   "the engine manager",
		"Reconcile":             "a reconcile, which tears destinations down and brings them up",
		"SetDestinationEnabled": "the operator's run/stop intent",
		"teardownDest":          "a destination teardown",
		"Engines":               "the running engines",
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if why, bad := forbidden[sel.Sel.Name]; bad {
			t.Errorf("lifecycle.go reaches %q -- %s. A transition failure must never be able "+
				"to stop a stream, and the first line of that defence is that this file "+
				"holds nothing that could", sel.Sel.Name, why)
		}
		return true
	})
}

// The escalation payload carries no credential. A broadcast id is not a secret
// -- it is in the public watch URL -- but a stream key is, and a fault built
// from a row is one careless field away from carrying one.
func TestALifecycleFaultCarriesNoCredential(t *testing.T) {
	monitorOff := false
	yt := &ytFake{
		status: "testing", streamStatus: "active", monitor: &monitorOff,
		refuseReason: "concurrentBroadcastsExceedLimit",
	}
	row := lifecycleTestRow(true, "testing")
	row.StreamKey = "super-secret-stream-key"
	row.URL = "rtmp://a.rtmp.youtube.com/live2"
	c, _, faults := lifecycleFixture(t, yt, row)

	c.sweepOnce(context.Background(), sweepEverything)

	if len(*faults) == 0 {
		t.Fatal("nothing was escalated")
	}
	blob, err := json.Marshal((*faults)[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), row.StreamKey) {
		t.Fatalf("a lifecycle fault carried the stream key: %s", blob)
	}
}

// The alert itself never carries a secret to the wire.
//
// The same assertion audit_test.go makes about every audit event, made here
// because a broadcast fault is an OPERATIONAL event and cannot join that list --
// an audit event must set no coalescing key and this one must set one. The
// planted secret goes in Detail, which is the one field of this event that is
// not written by this process: the classified refusals produce fixed sentences,
// but the unclassified branch formats the underlying error, and a platform error
// body is text nobody here chose.
func TestABroadcastFaultAlertNeverCarriesASecret(t *testing.T) {
	ev := auditBroadcastFault(lifecycleFault{
		DestinationID: 3, Destination: "yt main", Platform: "youtube",
		BroadcastID: "bcast-1",
		Detail:      "the platform refused: rtmps://live.twitch.tv/app/" + plantedSecret,
	})
	if ev.Key == "" {
		t.Error("no coalescing key: a destination refusing every fifteen seconds would " +
			"produce two hundred and forty messages an hour")
	}
	for _, format := range []alerts.Format{alerts.FormatJSON, alerts.FormatDiscord, alerts.FormatSlack} {
		rule := alerts.Rule{ID: 1, Name: "ops", Enabled: true,
			URL: "https://example.test/hook", Format: format}
		body, _, err := alerts.Encode(alerts.Delivery{
			Rule:  rule.Normalized(),
			Items: []alerts.Item{{Event: ev.Redacted(), Count: 1}},
		})
		if err != nil {
			t.Fatalf("Encode(%s): %v", format, err)
		}
		if strings.Contains(string(body), plantedSecret) {
			t.Fatalf("a %s broadcast fault carried a secret to the wire:\n%s", format, body)
		}
		if !strings.Contains(string(body), alerts.Mask) {
			t.Errorf("a %s broadcast fault dropped the text entirely instead of masking it; "+
				"the operator still needs to know their broadcast will not start:\n%s",
				format, body)
		}
	}
}

// A store failure is survivable and silent about nothing important.
func TestASweepSurvivesAStoreThatWillNotAnswer(t *testing.T) {
	c := newLifecycleCoordinator(slog.New(slog.NewTextHandler(io.Discard, nil)),
		brokenLifecycleStore{}, oauth.Set{},
		func(context.Context, int64) (*db.PlatformAccount, error) { return nil, nil },
		nil)
	c.sweepOnce(context.Background(), sweepEverything)
	if c.Wanted() {
		t.Error("a sweep that read nothing still claimed there was something to drive")
	}
}

type brokenLifecycleStore struct{}

func (brokenLifecycleStore) ListDestinations() ([]*db.Destination, error) {
	return nil, errors.New("database is away")
}

func (brokenLifecycleStore) UpdateLifecycle(int64, func(*db.Destination) bool) (*db.Destination, error) {
	return nil, errors.New("database is away")
}

func (brokenLifecycleStore) GetDestination(int64) (*db.Destination, error) {
	return nil, errors.New("database is away")
}

// ------------------------------------------------- FINISHED BUSINESS IS FREE

// A DISABLED ROW WHOSE BROADCAST IS ALREADY OVER MUST COST NOTHING, FOR EVER.
//
// complete and revoked are terminal -- YouTube documents no transition out of
// either, which is the same fact that stops this coordinator sending complete
// on a crash. So there is nothing left to do to such a row, and asking anyway
// costs two API calls (the broadcast and its bound stream) every fifteen
// seconds until the daemon dies. That is over eleven thousand units a day
// against a default allocation of ten thousand, from a row nobody will ever act
// on -- one of them is enough to exhaust the whole install's quota and leave
// the coordinator unable to end anything that matters.
//
// It also pins the observe loop on: Wanted() is true while anything is tracked,
// so a dead row keeps the engine building snapshots it has no consumer for.
func TestASettledTerminalRowIsForgottenRatherThanPolledForEver(t *testing.T) {
	for _, phase := range []string{phaseComplete, phaseRevoked} {
		t.Run(phase, func(t *testing.T) {
			row := lifecycleTestRow(false, phase)
			yt := &ytFake{status: phase, streamStatus: "inactive"}
			c, _, _ := lifecycleFixture(t, yt, row)

			for i := 0; i < 10; i++ {
				c.sweepOnce(context.Background(), sweepEverything)
			}

			_, reads := yt.snapshot()
			if reads != 0 {
				t.Errorf("%d state reads across ten sweeps of a finished, disabled row. "+
					"At two calls each on a fifteen-second tick this never stops, and one "+
					"such row exhausts the install's daily quota on its own.", reads)
			}
			if c.Wanted() {
				t.Error("a finished row still holds the observe loop on, so the engine keeps " +
					"building snapshots for a consumer that will never act again")
			}
		})
	}
}

// The disabled path is exempt from the enabled hold on purpose -- a pending end
// must still land -- but "not held" was implemented as "retried every fifteen
// seconds for ever". A permanently failing state read (a revoked token, a
// broadcast deleted in Studio) then retries until the process dies, while the
// failure log says "giving up for now", which was untrue.
//
// The bound is deliberately looser than the enabled one: a broadcast left live
// is worse than one left un-started, so this tries considerably harder before
// it stops.
func TestTheDisabledPathGivesUpEventuallyRatherThanRetryingForEver(t *testing.T) {
	row := lifecycleTestRow(false, phaseLive)
	// Attempts already past the disabled bound: the row has been failing for a
	// long time and nothing has changed.
	row.Lifecycle.Attempts = lifecycleGiveUpAfter * 2
	yt := &ytFake{status: phaseLive, streamStatus: "active"}
	c, _, _ := lifecycleFixture(t, yt, row)

	for i := 0; i < 5; i++ {
		c.sweepOnce(context.Background(), sweepEverything)
	}

	sent, reads := yt.snapshot()
	if reads != 0 || len(sent) != 0 {
		t.Errorf("a disabled row past its give-up bound is still calling the platform "+
			"(%d reads, %d transitions). The log says it gave up; it had not.", reads, len(sent))
	}
}

// A LISTING THAT LIES MUST NOT END A BROADCAST.
//
// endOrphan fires for a destination absent from ListDestinations, which is an
// inference about a QUERY rather than a fact about a row. It holds only while
// that listing is unfiltered and whole-table — true today, enforced by nothing.
// Scope the query later, to one source or to enabled rows or to a page, and
// every live broadcast outside the scope looks deleted and gets completed:
// silent, permanent on YouTube, and arriving as "why did half my broadcasts
// end".
//
// So the deletion is confirmed rather than inferred. This test makes the
// listing lie in exactly that way and asserts nothing is sent.
func TestABroadcastIsNotEndedBecauseAListingWasIncomplete(t *testing.T) {
	row := lifecycleTestRow(true, phaseLive)
	yt := &ytFake{status: phaseLive, streamStatus: "active"}
	c, store, _ := lifecycleFixture(t, yt, row)

	// Track it the way a live destination is tracked.
	c.sweepOnce(context.Background(), sweepEverything)

	// Now make ListDestinations omit it WITHOUT deleting it — precisely what a
	// scoped or paged query would do.
	store.mu.Lock()
	hidden := store.rows[row.ID]
	store.hideFromList = map[int64]bool{row.ID: true}
	store.mu.Unlock()
	if hidden == nil {
		t.Fatal("fixture row vanished")
	}

	before, _ := yt.snapshot()
	c.sweepOnce(context.Background(), sweepEverything)
	after, _ := yt.snapshot()

	for _, sent := range after[len(before):] {
		if sent == string(oauth.PhaseComplete) {
			t.Fatalf("a broadcast was ENDED because the destination was missing from a "+
				"listing, though the row still exists. On YouTube that is permanent. "+
				"sent=%v", after)
		}
	}
}
