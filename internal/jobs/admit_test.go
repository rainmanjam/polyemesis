package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Admission control is the correction to a race the deferral mechanism cannot
// close on its own: the queue wakes on Submit and again the instant a job
// finishes, and both of those land between two governor ticks. Before this
// existed, an acceptance run measured a proxy job starting AND finishing while
// a stream was live, with the governor none the wiser. These tests pin the
// closed window; TestGovernorMayStartAppliesTheSameDecisionTickWould pins that
// it is the same decision, not a second policy.

func TestQueueAsksAdmissionBeforeClaimingNotAfter(t *testing.T) {
	tests := []struct {
		name       string
		admit      func(Kind) bool
		wantRuns   int64
		wantClaims bool
	}{
		{
			name:       "no admission callback at all runs everything, because an ungoverned queue is a queue",
			admit:      nil,
			wantRuns:   1,
			wantClaims: true,
		},
		{
			name:     "a refused kind is never claimed, so the job is not even marked running",
			admit:    func(Kind) bool { return false },
			wantRuns: 0,
		},
		{
			name:       "an admitted kind runs normally",
			admit:      func(Kind) bool { return true },
			wantRuns:   1,
			wantClaims: true,
		},
		{
			name:       "admission is asked per kind, so refusing another kind does not block this one",
			admit:      func(k Kind) bool { return k == "allowed" },
			wantRuns:   1,
			wantClaims: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, st, _ := testQueue(t)
			var runs int64
			mustRegister(t, q, "allowed", 0, func(context.Context, Job, Reporter) error {
				atomic.AddInt64(&runs, 1)
				return nil
			})
			q.SetAdmit(tc.admit)

			job := mustSubmit(t, q, Job{Kind: "allowed", Target: "t"})
			ctx := context.Background()
			q.Tick(ctx)
			waitUntil(t, "the queue to settle", func() bool { return q.Stats().Running == 0 })

			if got := atomic.LoadInt64(&runs); got != tc.wantRuns {
				t.Errorf("worker ran %d times, want %d", got, tc.wantRuns)
			}
			// The refused case must leave the row untouched. A job that was
			// claimed and then put back would have burned an attempt.
			after := getJob(t, st, job.ID)
			if !tc.wantClaims {
				if after.State != StateQueued {
					t.Errorf("a refused job is in state %q, want %q", after.State, StateQueued)
				}
				if after.Attempts != 0 {
					t.Errorf("a refused job burned %d attempt(s); admission must happen before the claim", after.Attempts)
				}
			}
		})
	}
}

// The bug this closes: the job that finishes wakes the queue, which immediately
// claims the next one. Without admission that next job starts however live the
// stream is, because the governor's tick has not come round yet.
func TestARefusedKindDoesNotSlipThroughOnTheWakeAJobsCompletionCauses(t *testing.T) {
	q, _, _ := testQueue(t)
	var (
		allow atomic.Bool
		runs  atomic.Int64
	)
	allow.Store(true)
	mustRegister(t, q, "work", 0, func(context.Context, Job, Reporter) error {
		runs.Add(1)
		// The gate closes while this job is running, which is precisely the
		// moment the old code lost: completion wakes the queue.
		allow.Store(false)
		return nil
	})
	q.SetAdmit(func(Kind) bool { return allow.Load() })

	for i := 0; i < 4; i++ {
		mustSubmit(t, q, Job{Kind: "work", Target: string(rune('a' + i))})
	}
	ctx := context.Background()
	q.Tick(ctx)
	waitUntil(t, "the first job to finish", func() bool { return q.Stats().Running == 0 })
	q.Tick(ctx)
	waitUntil(t, "the queue to settle", func() bool { return q.Stats().Running == 0 })

	if got := runs.Load(); got != 1 {
		t.Errorf("%d jobs ran after the gate closed mid-run, want exactly 1", got)
	}
}

// MayStart must be the SAME decision Tick makes, or an operator reading the
// gates panel would be looking at one policy while another one ran.
func TestGovernorMayStartAppliesTheSameDecisionTickWould(t *testing.T) {
	const kind Kind = "heavy"
	window := Window{TZ: "UTC", StartMinutes: 2 * 60, EndMinutes: 6 * 60}

	tests := []struct {
		name    string
		policy  func(Policy) Policy
		sensors Sensors
		want    bool
	}{
		{
			name:   "an idle machine admits work",
			policy: func(p Policy) Policy { return p },
			want:   true,
		},
		{
			name: "a live ingest refuses it, which is the whole point of the tier",
			policy: func(p Policy) Policy {
				p.YieldToStream = true
				return p
			},
			sensors: Sensors{IngestLive: func() bool { return true }},
			want:    false,
		},
		{
			name: "yieldToStream off admits work even with a stream up",
			policy: func(p Policy) Policy {
				p.YieldToStream = false
				return p
			},
			sensors: Sensors{IngestLive: func() bool { return true }},
			want:    true,
		},
		{
			name: "a kind marked ignoreIngest is admitted through a live stream",
			policy: func(p Policy) Policy {
				p.YieldToStream = true
				p.Kinds[kind] = KindPolicy{Mode: ModeDeferred, IgnoreIngest: true}
				return p
			},
			sensors: Sensors{IngestLive: func() bool { return true }},
			want:    true,
		},
		{
			name: "a scheduled kind outside its window is refused",
			policy: func(p Policy) Policy {
				p.Kinds[kind] = KindPolicy{Mode: ModeScheduled, Windows: []Window{window}}
				return p
			},
			want: false,
		},
		{
			name: "a manual kind is refused until a human releases a job",
			policy: func(p Policy) Policy {
				p.Kinds[kind] = KindPolicy{Mode: ModeManual}
				return p
			},
			want: false,
		},
		{
			name: "a realtime kind is admitted regardless of the stream",
			policy: func(p Policy) Policy {
				p.YieldToStream = true
				p.Kinds[kind] = KindPolicy{Mode: ModeRealtime}
				return p
			},
			sensors: Sensors{IngestLive: func() bool { return true }},
			want:    true,
		},
		{
			name: "a disabled governor admits everything, because a gate you switched off is not a gate",
			policy: func(p Policy) Policy {
				p.Enabled = false
				p.YieldToStream = true
				return p
			},
			sensors: Sensors{IngestLive: func() bool { return true }},
			want:    true,
		},
		{
			name: "a nil ingest sensor admits work rather than blocking on a question nobody can answer",
			policy: func(p Policy) Policy {
				p.YieldToStream = true
				return p
			},
			sensors: Sensors{},
			want:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, _, _, clk := testGovernor(t, tc.policy(governorPolicy()), tc.sensors)
			// Noon UTC on the fake clock, which is outside the 02:00-06:00
			// window every scheduled case above uses.
			snap := g.Tick(clk.Now())
			got := g.MayStart(kind)
			if got != tc.want {
				t.Errorf("MayStart(%q) = %v, want %v (gates %+v)", kind, got, tc.want, snap.Gates)
			}
			// Whatever it said, Tick must have said the same thing about
			// starting. Two answers would mean two policies.
			for _, v := range snap.Verdicts {
				if v.Kind == kind && v.Start != got {
					t.Errorf("Tick said Start=%v for %q but MayStart said %v", v.Start, kind, got)
				}
			}
		})
	}
}

// Release is the "run it anyway" button. Admission is decided per kind because
// that is the only granularity the store claims at, so a released job has to be
// able to drag its kind past the mode gate — and no further.
func TestAReleasedJobGetsItsKindPastTheModeGateButNotPastTheStream(t *testing.T) {
	const kind Kind = "heavy"
	p := governorPolicy()
	p.YieldToStream = true
	p.Kinds[kind] = KindPolicy{Mode: ModeManual}

	live := false
	g, q, _, clk := testGovernor(t, p, Sensors{IngestLive: func() bool { return live }})
	mustRegister(t, q, kind, 0, func(context.Context, Job, Reporter) error { return nil })
	job := mustSubmit(t, q, Job{Kind: kind, Target: "t"})

	g.Tick(clk.Now())
	if g.MayStart(kind) {
		t.Fatal("a manual kind was admitted with nothing released")
	}

	g.Release(job.ID)
	// No tick in between: the queue wakes on its own second, and a release the
	// governor only honoured five seconds later would be a button that appears
	// not to work.
	if !g.MayStart(kind) {
		t.Error("a released job did not get its kind past the manual gate")
	}

	live = true
	if g.MayStart(kind) {
		t.Error("a released job was admitted while the ingest was live; a human clicking run does not change the fact that a broadcast is on")
	}
}

// "Run it anyway" was decorative for the exact case it exists for: a job
// blocked by a mode gate is parked half an hour out, and nothing brought it
// back. An acceptance run caught it — a released job sat queued for a full
// minute and never started.
func TestReleaseBringsAModeDeferredJobForwardButLeavesARetryBackoffAlone(t *testing.T) {
	const kind Kind = "heavy"
	p := governorPolicy()
	p.Kinds[kind] = KindPolicy{Mode: ModeManual}
	p.ManualDeferFor = 30 * time.Minute

	g, q, st, clk := testGovernor(t, p, Sensors{})
	mustRegister(t, q, kind, 0, func(context.Context, Job, Reporter) error { return nil })
	job := mustSubmit(t, q, Job{Kind: kind, Target: "t"})

	g.Tick(clk.Now())
	parked := getJob(t, st, job.ID)
	if parked.State != StateDeferred {
		t.Fatalf("a manual job is in state %q, want %q", parked.State, StateDeferred)
	}
	if !parked.AvailableAt.After(clk.Now().Add(20 * time.Minute)) {
		t.Fatalf("a manual job was parked at %v, expected roughly half an hour out", parked.AvailableAt)
	}

	g.Release(job.ID)
	after := getJob(t, st, job.ID)
	if after.AvailableAt.After(clk.Now()) {
		t.Errorf("a released job is still parked until %v; the button does nothing", after.AvailableAt)
	}

	// The other half of the rule: a QUEUED row with a future AvailableAt is a
	// retry backoff, and releasing must not shorten it.
	backoff := mustSubmit(t, q, Job{Kind: kind, Target: "u"})
	later := clk.Now().Add(10 * time.Minute)
	if err := st.RescheduleJob(backoff.ID, later, "boom", clk.Now()); err != nil {
		t.Fatalf("RescheduleJob: %v", err)
	}
	g.Release(backoff.ID)
	if got := getJob(t, st, backoff.ID).AvailableAt; !got.Equal(later) {
		t.Errorf("a retry backoff moved to %v, want it left at %v", got, later)
	}
}

// The linger exists so a reconnect is not raced by a transcode pouncing on the
// gap, and it has to apply on the claim path too or the gap reopens there.
func TestMayStartHonoursTheIngestLingerAfterTheStreamStops(t *testing.T) {
	p := governorPolicy()
	p.YieldToStream = true
	p.IngestLinger = 30 * time.Second

	live := true
	g, _, _, clk := testGovernor(t, p, Sensors{IngestLive: func() bool { return live }})
	g.Tick(clk.Now())
	if g.MayStart("heavy") {
		t.Fatal("admitted while live")
	}

	live = false
	clk.Advance(10 * time.Second)
	if g.MayStart("heavy") {
		t.Error("admitted 10s into a 30s linger; a reconnect would be racing a transcode")
	}
	clk.Advance(25 * time.Second)
	if !g.MayStart("heavy") {
		t.Error("still refused 35s after the stream stopped, past the 30s linger")
	}
}
