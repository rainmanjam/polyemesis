package engine

import (
	"bytes"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// ---------------------------------------------------------------- log capture

// syncBuffer is a goroutine-safe stand-in for bytes.Buffer, for tests that
// wire a buffer-backed logger into an engine and then read it back.
//
// A bare bytes.Buffer is documented as unsafe for concurrent use, and several
// selector tests do exactly that: they assign a *bytes.Buffer-backed
// slog.Logger to e.log and then call reconcileSelector/startFeed, which spawn
// a real supervisor goroutine (internal/supervisor.(*Process).supervise).
// That goroutine logs asynchronously -- e.g. a Warn when a process stalls --
// straight into the same buffer the test goroutine is calling Reset()/String()
// on. `go test -race` caught it as two concurrent writers to one
// bytes.Buffer; it is intermittent because it only fires when the supervisor
// goroutine's log write and the test's Reset/String happen to overlap, so it
// can pass locally for many runs and still be a real, load-bearing bug.
//
// Every place a test needs to read back what the engine logged, use this
// instead of a bare bytes.Buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// setSettings writes the engine's live settings the way production does: under
// the lock.
//
// Assigning e.settings directly RACES the status publisher. Engine.Settings()
// reads that field under e.mu.RLock(), and it is called from the supervisor's
// own goroutine via setState -> onState -> publishStatus -> Status ->
// SourceInfo. The engine is correct; a test that reaches past its mutex is not.
//
// This was reported by the race detector on a Dependabot PR that bumped an
// unrelated dependency -- the bump shifted timing enough to make a latent race
// fire, and the failure looked like the dependency's fault. It was ours.
func setSettings(e *Engine, s db.Settings) {
	e.mu.Lock()
	e.settings = s
	e.mu.Unlock()
}

// ---------------------------------------------------------------- the choice

func failoverChoice(cur sourceKind, mut func(*sourceChoice)) sourceChoice {
	now := time.Unix(1_700_000_000, 0)
	c := sourceChoice{
		now:           now,
		cur:           cur,
		grace:         5 * time.Second,
		slateEnabled:  true,
		backupEnabled: true,
		returnStable:  30 * time.Second,
	}
	mut(&c)
	return c
}

// delivering marks a source as having sent bytes `ago` and as having been
// unbroken for `run`.
func delivering(now time.Time, ago, run time.Duration) liveness {
	return liveness{rx: 1, at: now.Add(-ago), since: now.Add(-run)}
}

// The whole feature is this function. Every row is a moment an operator would
// have to explain to a client, so the reason string matters as much as the
// answer.
func TestChooseSourceSwitchesOnDeliveryNotOnProcessState(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	tests := []struct {
		name string
		cur  sourceKind
		mut  func(*sourceChoice)
		want sourceKind
	}{
		{
			name: "a delivering primary is left alone",
			cur:  sourcePrimary,
			mut: func(c *sourceChoice) {
				c.primary = delivering(now, time.Second, time.Minute)
			},
			want: sourcePrimary,
		},
		{
			name: "a primary quiet for less than the grace period is not a failure yet",
			cur:  sourcePrimary,
			mut: func(c *sourceChoice) {
				c.primary = delivering(now, 4*time.Second, time.Minute)
				c.backup = delivering(now, time.Second, time.Minute)
			},
			want: sourcePrimary,
		},
		{
			name: "a primary quiet past the grace period gives way to a live backup",
			cur:  sourcePrimary,
			mut: func(c *sourceChoice) {
				c.primary = delivering(now, 6*time.Second, time.Minute)
				c.backup = delivering(now, time.Second, time.Minute)
			},
			want: sourceBackup,
		},
		{
			name: "with no backup on air the slate takes over",
			cur:  sourcePrimary,
			mut: func(c *sourceChoice) {
				c.primary = delivering(now, 6*time.Second, time.Minute)
			},
			want: sourceSlate,
		},
		{
			name: "a configured but silent backup is not a source",
			cur:  sourcePrimary,
			mut: func(c *sourceChoice) {
				c.primary = delivering(now, 6*time.Second, time.Minute)
				c.backup = delivering(now, time.Hour, time.Hour)
			},
			want: sourceSlate,
		},
		{
			// Fail open. Switching to nothing would take the destinations off
			// the air for the whole outage; parked on the primary they start
			// carrying the stream the instant an encoder arrives.
			name: "with nowhere to go the feed stays parked on the primary",
			cur:  sourcePrimary,
			mut: func(c *sourceChoice) {
				c.primary = delivering(now, time.Hour, time.Hour)
				c.slateEnabled = false
				c.backupEnabled = false
			},
			want: sourcePrimary,
		},
		{
			name: "the default return mode leaves the backup on air when the primary comes back",
			cur:  sourceBackup,
			mut: func(c *sourceChoice) {
				c.backup = delivering(now, time.Second, time.Hour)
				c.primary = delivering(now, time.Second, time.Hour)
			},
			want: sourceBackup,
		},
		{
			name: "an automatic return waits for the primary to be steady",
			cur:  sourceBackup,
			mut: func(c *sourceChoice) {
				c.autoReturn = true
				c.backup = delivering(now, time.Second, time.Hour)
				c.primary = delivering(now, time.Second, 5*time.Second)
			},
			want: sourceBackup,
		},
		{
			name: "an automatic return goes back once the primary has been steady long enough",
			cur:  sourceBackup,
			mut: func(c *sourceChoice) {
				c.autoReturn = true
				c.backup = delivering(now, time.Second, time.Hour)
				c.primary = delivering(now, time.Second, time.Hour)
			},
			want: sourcePrimary,
		},
		{
			// Manual return means "do not flap", not "never recover".
			name: "a dead backup returns to a live primary even in manual mode",
			cur:  sourceBackup,
			mut: func(c *sourceChoice) {
				c.backup = delivering(now, time.Minute, time.Hour)
				c.primary = delivering(now, time.Second, time.Second)
			},
			want: sourcePrimary,
		},
		{
			name: "both ingests gone falls through to the slate",
			cur:  sourceBackup,
			mut: func(c *sourceChoice) {
				c.backup = delivering(now, time.Minute, time.Hour)
				c.primary = delivering(now, time.Minute, time.Hour)
			},
			want: sourceSlate,
		},
		{
			// A slate is a holding pattern, never a destination: the return is
			// immediate and does not consult the return mode.
			name: "the slate hands back to the primary the moment it delivers",
			cur:  sourceSlate,
			mut: func(c *sourceChoice) {
				c.primary = delivering(now, time.Second, time.Second)
			},
			want: sourcePrimary,
		},
		{
			name: "the slate hands to the backup when only the backup is delivering",
			cur:  sourceSlate,
			mut: func(c *sourceChoice) {
				c.backup = delivering(now, time.Second, time.Second)
			},
			want: sourceBackup,
		},
		{
			name: "a slate switched off mid-outage falls back to the primary feed",
			cur:  sourceSlate,
			mut:  func(c *sourceChoice) { c.slateEnabled = false },
			want: sourcePrimary,
		},
		{
			name: "a fresh tier with nothing delivering shows the slate",
			cur:  sourceNone,
			mut:  func(c *sourceChoice) {},
			want: sourceSlate,
		},
		{
			name: "an operator's pin outranks the detector",
			cur:  sourcePrimary,
			mut: func(c *sourceChoice) {
				c.pinned = sourceSlate
				c.primary = delivering(now, time.Second, time.Hour)
			},
			want: sourceSlate,
		},
		{
			// A pin that outlived its source would strand the broadcast on a
			// dead input, which is the opposite of what a manual override is
			// reached for.
			name: "a pin to a source that has died is ignored",
			cur:  sourceSlate,
			mut: func(c *sourceChoice) {
				c.pinned = sourceBackup
				c.backup = delivering(now, time.Hour, time.Hour)
				c.primary = delivering(now, time.Second, time.Second)
			},
			want: sourcePrimary,
		},
		{
			name: "a pin to a backup that is switched off is ignored",
			cur:  sourceSlate,
			mut: func(c *sourceChoice) {
				c.pinned = sourceBackup
				c.backupEnabled = false
				c.backup = delivering(now, time.Second, time.Hour)
				c.primary = delivering(now, time.Second, time.Second)
			},
			want: sourcePrimary,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := chooseSource(failoverChoice(tt.cur, tt.mut))

			if got != tt.want {
				t.Fatalf("chooseSource() = %q, want %q", got, tt.want)
			}
			// A switch nobody can explain is the failure this feature is
			// judged on, so every change of source has to carry a reason.
			if got != tt.cur && reason == "" {
				t.Errorf("switch from %q to %q carries no reason", tt.cur, got)
			}
			if got == tt.cur && tt.cur != sourceNone && reason != "" {
				t.Errorf("staying on %q reported a switch reason %q", tt.cur, reason)
			}
		})
	}
}

// A source that has never delivered a byte is not live, however long the engine
// has been up. Reading a zero counter as "fine" would leave the tier waiting on
// an encoder that never arrives instead of showing the slate.
func TestLivenessNeedsBytesNotUptime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var l liveness

	if l.alive(now, 5*time.Second) {
		t.Error("a source with no bytes reads as live")
	}
	l.sample(0, now, 5*time.Second)
	if l.alive(now, 5*time.Second) {
		t.Error("an unchanged byte counter reads as live")
	}
	l.sample(1316, now, 5*time.Second)
	if !l.alive(now, 5*time.Second) {
		t.Error("a source that just delivered reads as dead")
	}
	if !l.alive(now.Add(4*time.Second), 5*time.Second) {
		t.Error("a source goes dead inside its grace period")
	}
	if l.alive(now.Add(6*time.Second), 5*time.Second) {
		t.Error("a source stays live past its grace period")
	}
}

// The stability window an automatic return waits on has to measure the CURRENT
// run of delivery, not the first byte ever seen, or a source that flapped an
// hour ago would look rock solid the instant it came back.
func TestLivenessRestartsTheStableRunAfterAGap(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	grace := 5 * time.Second
	var l liveness

	l.sample(1, now, grace)
	l.sample(2, now.Add(time.Second), grace)
	if got := l.stableFor(now.Add(time.Second)); got != time.Second {
		t.Errorf("stableFor() = %v during an unbroken run, want 1s", got)
	}

	after := now.Add(time.Hour)
	l.sample(3, after, grace)
	if got := l.stableFor(after); got != 0 {
		t.Errorf("stableFor() = %v on the first byte after a gap, want 0", got)
	}
}

// -------------------------------------------------------------- restart hash

// The tier's whole purpose in one assertion: what a consumer folds into its
// restart hash must not move when the source behind the selector does.
func TestTheRestartHashDoesNotMoveWhenTheSourceDoes(t *testing.T) {
	row := testDestination(1, nil)
	compiled := routing.Result{FilterComplex: "[0:a:0]anull[out]", OutLabel: "[out]"}
	rend := testRendition(1, "1080p60")

	on := upstreamSig("on", "")
	// A failover is exactly the case where the silence signature can change
	// underneath a live destination: the probe goes blank when the primary
	// stops. With the tier running that must make no difference at all.
	afterProbeCleared := upstreamSig("on", "some-silence-signature")

	if on != afterProbeCleared {
		t.Fatal("the selector's signature moved with the silence tier; every destination would restart on a failover")
	}
	if destSpec(row, compiled, on) != destSpec(row, compiled, afterProbeCleared) {
		t.Error("a destination's restart hash moved across a source switch")
	}
	if renditionSig(rend, 60, on, "") != renditionSig(rend, 60, afterProbeCleared, "") {
		t.Error("a rendition's restart hash moved across a source switch")
	}
}

// With failover off nothing may change: the signature every existing consumer
// is running with has to be byte-identical to the one it had before this tier
// existed, or enabling nothing would still restart every destination.
func TestFailoverOffLeavesEveryRestartHashExactlyAsItWas(t *testing.T) {
	if got := wantSelector(db.DefaultSettings()); got != "" {
		t.Fatalf("wantSelector(defaults) = %q, want the tier off", got)
	}
	tests := []string{"", "a-silence-signature"}
	for _, silence := range tests {
		if got := upstreamSig("", silence); got != silence {
			t.Errorf("upstreamSig(\"\", %q) = %q, want the silence signature unchanged", silence, got)
		}
	}
}

// -------------------------------------------------------------------- feeds

// The offset is the only thing standing between a switch and a timestamp that
// jumps into the past, which platforms answer by dropping the connection.
func TestRelayFeedArgsCopyEverythingAndCarryTheTimelineForward(t *testing.T) {
	args := relayFeedArgs("udp://127.0.0.1:21001", "udp://127.0.0.1:21002", 12.5)

	joined := args
	want := [][2]string{
		{"-map", "0"},
		{"-c", "copy"},
		{"-output_ts_offset", "12.500"},
		{"-f", "mpegts"},
	}
	for _, pair := range want {
		i := slices.Index(joined, pair[0])
		if i < 0 || i+1 >= len(joined) || joined[i+1] != pair[1] {
			t.Errorf("missing %s %s in %v", pair[0], pair[1], joined)
		}
	}
	// An output option bound after the target attaches to nothing at all.
	if at, target := slices.Index(joined, "-output_ts_offset"), len(joined)-1; at >= target {
		t.Errorf("-output_ts_offset at %d is not before the output target at %d", at, target)
	}
	if got := joined[len(joined)-1]; got != ffmpeg.RelayOutputURL("udp://127.0.0.1:21002") {
		t.Errorf("output target = %q, want the selector hub", got)
	}
	// The copy contract: a feed that re-encoded would make the selector a
	// second place video is degraded.
	if slices.Contains(joined, "-c:v") || slices.Contains(joined, "libx264") {
		t.Errorf("the feed encodes video: %v", joined)
	}
}

// The slate has to look like the ingest it replaced or a copying destination
// sees a format change it may refuse.
func TestSlateSpecFollowsTheProbedIngestRatherThanAForm(t *testing.T) {
	e := &Engine{
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		sourceState: sourceState{videoInfo: &ffmpeg.VideoStream{Width: 1920, Height: 1080, FrameRate: 50}},
	}
	s := db.DefaultSettings()
	s.Failover.Slate.Color = "0x101014"

	spec, fallback := e.slateSpec(s, "udp://127.0.0.1:21005", 7)

	if spec.Width != 1920 || spec.Height != 1080 || spec.FPS != 50 {
		t.Errorf("slate geometry = %dx%d@%v, want the probed 1920x1080@50",
			spec.Width, spec.Height, spec.FPS)
	}
	if spec.TimestampOffsetSeconds != 7 {
		t.Errorf("slate offset = %v, want 7", spec.TimestampOffsetSeconds)
	}
	if spec.Color != "0x101014" {
		t.Errorf("slate colour = %q, want the configured one", spec.Color)
	}
	if fallback != "" {
		t.Errorf("an unconfigured encoder reported a fallback: %q", fallback)
	}

	// Unprobed: there is nothing to match, and SlateArgs' own defaults take
	// over. Stated here because it is the case a copying destination sees as a
	// resolution change.
	e.videoInfo = nil
	if spec, _ := e.slateSpec(s, "udp://127.0.0.1:21005", 0); spec.Width != 0 || spec.FPS != 0 {
		t.Errorf("an unprobed slate invented geometry: %dx%d@%v", spec.Width, spec.Height, spec.FPS)
	}
}

// A slate image is operator input that becomes an FFmpeg argument, so it is
// confined to the data directory exactly as a file:// pull source is. A path
// that escapes is dropped in favour of a flat colour rather than refused: a
// standby source that will not start is worth nothing.
func TestSlateImageIsConfinedAndFallsBackToColour(t *testing.T) {
	e := &Engine{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e.cfg.DataDir = filepath.Join("/var", "lib", "polyemesis")

	tests := []struct {
		name string
		path string
		want string
	}{
		{"a relative path resolves under the data directory", "slate.png",
			filepath.Join("/var", "lib", "polyemesis", "slate.png")},
		{"a traversal is dropped", "../../etc/shadow", ""},
		{"an absolute path is dropped", "/etc/shadow", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := db.DefaultSettings()
			s.Failover.Slate.ImagePath = tt.path

			spec, _ := e.slateSpec(s, "udp://127.0.0.1:21005", 0)

			if spec.ImagePath != tt.want {
				t.Errorf("ImagePath = %q, want %q", spec.ImagePath, tt.want)
			}
		})
	}
}

// ------------------------------------------------------- the measured switch

// failoverEngine is the smallest engine the tier needs: real hubs and a real
// store, because a switch subscribes, allocates and publishes for real.
//
// The FFmpeg binary deliberately does not exist. What is being measured is what
// the switch does to the DESTINATIONS, and every one of those effects — the
// subscription, the process identity, the restart count — happens whether or
// not the child ever execs.
func failoverEngine(t *testing.T) *Engine {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub, err := relay.New(log, 0)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	store := dbtest.OpenAt(t, filepath.Join(t.TempDir(), "test.db"))

	return &Engine{log: log, store: store, bus: events.NewBroker(), hub: hub, alloc: relay.NewPortAllocator(relayPortBase+relayPortSpan, 64), tools: &ffmpeg.Tools{FFmpeg: "polyemesis-no-such-binary"}, dests: map[int64]*destination{}, rends: map[int64]*rendition{}, playProcs: map[string]*supervisor.Process{}, sourceState: sourceState{source: routing.DefaultSource()}}
}

func failoverOnSettings() db.Settings {
	s := db.DefaultSettings()
	s.Failover.Enabled = true
	s.Failover.GraceSeconds = 5
	s.Failover.Slate.Enabled = true
	return s
}

// step runs the DECISION at a chosen instant, from whatever liveness the tier
// is already carrying.
//
// NOT FOR ACQUISITION. The production sweep is sweepSelector, and step skips
// the first half of it: sampleSources, which is where each candidate's liveness
// comes from a HUB's byte counter. A test built on step therefore cannot see
// the sweep reading the wrong hub, cannot see a counter that goes backwards,
// and cannot see a source that stopped delivering -- it can only see what
// chooseSource does with numbers the test wrote itself. That blind spot is
// exactly how a wrong-hub failover regression could have shipped with the whole
// package green; see engine_gap_sweep_test.go, which calls sweepSelector and
// pushes real datagrams through relay.Hub.Deliver instead.
//
// Use it for what it is good for: the decision's own branches, at instants a
// wall clock could not produce.
func (e *Engine) step(s db.Settings, now time.Time) {
	e.selMu.Lock()
	defer e.selMu.Unlock()
	e.applySourceChoice(s, "", now)
}

// waitUntil polls a condition rather than sleeping a guessed interval, so the
// test is neither flaky on a loaded machine nor slow on an idle one.
func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// deliver marks one source as having just delivered bytes, by writing the
// liveness the sweep would have sampled.
//
// NOT FOR ACQUISITION, for the same reason as step: it writes the ANSWER
// sampleSources is supposed to compute, so it bypasses the question of which
// hub that answer is read from. A test that pairs deliver with step never
// executes a byte counter at all. To assert that a source is or is not
// delivering, put real datagrams on the real hub with relay.Hub.Deliver and
// call e.sweepSelector(now).
func (e *Engine) deliver(k sourceKind, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.sel.live[k]
	l.sample(l.rx+1316, now, time.Second)
}

// This is the feature, measured rather than asserted: a destination must come
// out of a primary-down -> slate -> primary-back cycle as the SAME process,
// with the SAME restart count, still subscribed to the SAME relay. If it
// restarts, the platform connection drops and the slate has prevented nothing.
func TestADestinationRidesAPrimaryDownSlateBackCycleWithoutRestarting(t *testing.T) {
	e := failoverEngine(t)
	s := failoverOnSettings()
	setSettings(e, s)

	e.reconcileSelector(s, wantSelector(s), "")
	hub := e.selectorHub()
	if hub == nil {
		t.Fatal("the selector tier did not start")
	}
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		e.teardownFeed(e.sel.feed)
		_ = hub.Close()
	})

	// One destination, wired exactly as startDest wires it.
	proc := supervisor.New(e.log, supervisor.Spec{
		Name: "dest:1", Kind: "destination", Bin: "polyemesis-no-such-binary",
	})
	port, err := e.alloc.Allocate()
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	hub.Subscribe("dest:1", port)
	dest := &destination{
		row: &db.Destination{ID: 1, Name: "twitch"}, proc: proc,
		port: port, subName: "dest:1", hub: hub, spec: "unchanged",
	}
	// Under e.mu: the selector's sweep goroutine is already running and reads
	// e.dests, so an unguarded write here is a genuine race -- one that only
	// shows up when the sweep happens to land in the same moment, which is why
	// it surfaced on CI rather than locally.
	e.mu.Lock()
	e.dests[1] = dest
	e.mu.Unlock()
	before := proc.Status()

	t0 := time.Now()
	type phase struct {
		name string
		at   time.Time
		live bool
		want sourceKind
	}
	phases := []phase{
		{"the primary is delivering", t0, true, sourcePrimary},
		{"the primary has been quiet past the grace period", t0.Add(20 * time.Second), false, sourceSlate},
		{"the primary is back", t0.Add(30 * time.Second), true, sourcePrimary},
	}

	var offsets []float64
	var feeds []*supervisor.Process
	for _, p := range phases {
		if p.live {
			e.deliver(sourcePrimary, p.at)
		}
		e.step(s, p.at)

		e.mu.RLock()
		active, feed := e.sel.active, e.sel.feed
		e.mu.RUnlock()
		if active != p.want {
			t.Fatalf("%s: active source = %q, want %q", p.name, active, p.want)
		}
		if feed == nil {
			t.Fatalf("%s: no feed is running", p.name)
		}
		offsets = append(offsets, feed.offset)
		feeds = append(feeds, feed.proc)

		// The measurement, taken at every phase rather than only at the end.
		e.mu.RLock()
		stillThere := e.dests[1] == dest
		e.mu.RUnlock()
		if !stillThere || dest.proc != proc {
			t.Fatalf("%s: the destination was replaced", p.name)
		}
		if got := proc.Status(); got.Restarts != before.Restarts {
			t.Fatalf("%s: destination restarts = %d, want %d",
				p.name, got.Restarts, before.Restarts)
		}
		if dest.hub != hub {
			t.Fatalf("%s: the destination was moved to another relay", p.name)
		}
		if !slices.Contains(hub.Subscribers(), "dest:1") {
			t.Fatalf("%s: the destination lost its subscription", p.name)
		}
	}

	// Three different feeds did the work, which is what makes the assertions
	// above mean something: the source really did change under the destination.
	if feeds[0] == feeds[1] || feeds[1] == feeds[2] {
		t.Error("the feed process was not replaced across the cycle")
	}
	// PTS continuity: each feed publishes further along one shared timeline, so
	// a switch is a forward step rather than a jump into the past.
	for i := 1; i < len(offsets); i++ {
		if offsets[i] < offsets[i-1] {
			t.Errorf("feed %d starts at offset %v, behind the previous feed's %v",
				i, offsets[i], offsets[i-1])
		}
	}
	e.mu.RLock()
	switches, reason := e.sel.switches, e.sel.reason
	e.mu.RUnlock()
	if switches < 3 {
		t.Errorf("switches = %d, want at least the three the cycle performed", switches)
	}
	if reason == "" {
		t.Error("the last switch left no reason for an operator to read")
	}
}

// A feed that exited is rebuilt with a CURRENT offset. Left to the supervisor's
// own restart it would come back with the offset it was born with and publish
// its second life in the past, which is the failure the whole timeline
// arrangement exists to avoid.
func TestARespawnedFeedDoesNotRewindTheTimeline(t *testing.T) {
	e := failoverEngine(t)
	s := failoverOnSettings()
	setSettings(e, s)

	e.reconcileSelector(s, wantSelector(s), "")
	if e.selectorHub() == nil {
		t.Fatal("the selector tier did not start")
	}
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		e.teardownFeed(e.sel.feed)
		_ = e.sel.hub.Close()
	})

	t0 := time.Now()
	e.deliver(sourcePrimary, t0)
	e.step(s, t0)

	e.mu.RLock()
	first := e.sel.feed
	e.mu.RUnlock()
	if first == nil {
		t.Fatal("no feed is running")
	}
	// The binary does not exist, so the spawn fails and leaves exactly the
	// state a crashed feed leaves behind.
	waitUntil(t, func() bool { return !feedRunning(first) }, "the feed to go down")

	later := t0.Add(time.Minute)
	e.deliver(sourcePrimary, later)
	e.step(s, later)

	e.mu.RLock()
	second := e.sel.feed
	e.mu.RUnlock()
	if second == nil || second == first {
		t.Fatal("a dead feed was not respawned")
	}
	if second.offset <= first.offset {
		t.Errorf("respawned feed offset = %v, want it ahead of the first feed's %v",
			second.offset, first.offset)
	}
}

// Switching the feature off has to leave the pipeline exactly as it was before
// it was ever switched on. A leftover tier would go on being consulted — and a
// selector that is present but has no hub refuses every destination.
func TestTurningFailoverOffLeavesNothingBehind(t *testing.T) {
	e := failoverEngine(t)
	on := failoverOnSettings()
	setSettings(e, on)

	e.reconcileSelector(on, wantSelector(on), "")
	if e.selectorHub() == nil {
		t.Fatal("the selector tier did not start")
	}

	off := db.DefaultSettings()
	setSettings(e, off)
	e.reconcileSelector(off, wantSelector(off), "")

	if h := e.selectorHub(); h != nil {
		t.Error("the selector hub outlived the setting that created it")
	}
	if err := e.selectorProblem(); err != nil {
		t.Errorf("a destination is refused with failover off: %v", err)
	}
	if e.Failover() != nil {
		t.Error("the dashboard still shows a failover tier")
	}
	// A passthrough destination is back on the ingest hub, byte-for-byte the
	// arrangement that predates this tier.
	hub, err := e.upstreamHub(&db.Destination{ID: 1})
	if err != nil {
		t.Fatalf("upstreamHub: %v", err)
	}
	if hub != e.hub {
		t.Error("a passthrough destination does not read the ingest hub again")
	}
}

// A manual override is what makes the default return mode usable: with the
// backup on air and the primary healthy, nothing moves until an operator says
// so.
func TestAnOperatorCanPutASourceOnAirByHand(t *testing.T) {
	e := failoverEngine(t)
	s := failoverOnSettings()
	setSettings(e, s)

	e.reconcileSelector(s, wantSelector(s), "")
	if e.selectorHub() == nil {
		t.Fatal("the selector tier did not start")
	}
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		e.teardownFeed(e.sel.feed)
		_ = e.sel.hub.Close()
	})

	now := time.Now()
	e.deliver(sourcePrimary, now)
	e.step(s, now)
	if got := e.Failover().Active; got != sourcePrimary {
		t.Fatalf("active source = %q, want the primary", got)
	}

	if err := e.SwitchSource("slate"); err != nil {
		t.Fatalf("SwitchSource: %v", err)
	}
	st := e.Failover()
	if st.Active != sourceSlate {
		t.Errorf("active source = %q after a manual switch, want the slate", st.Active)
	}
	if st.Pinned != sourceSlate {
		t.Errorf("pinned = %q, want the operator's choice to be visible", st.Pinned)
	}

	if err := e.SwitchSource("auto"); err != nil {
		t.Fatalf("SwitchSource(auto): %v", err)
	}
	e.deliver(sourcePrimary, now.Add(time.Second))
	e.step(s, now.Add(time.Second))
	if got := e.Failover().Active; got != sourcePrimary {
		t.Errorf("active source = %q after handing back to automatic, want the primary", got)
	}
	if err := e.SwitchSource("nonsense"); err == nil {
		t.Error("an unknown source name was accepted")
	}
}

// ------------------------------------------------------------------ settings

// The tier is opt-in, and an upgrade must not switch it on.
func TestFailoverDefaultsAreOffAndValid(t *testing.T) {
	s := db.DefaultSettings()

	if s.Failover.Enabled {
		t.Error("failover is enabled by default; an upgrade would change a running pipeline")
	}
	if s.Failover.Return != db.FailoverReturnManual {
		t.Errorf("default return mode = %q, want manual so a recovering encoder cannot flap a broadcast",
			s.Failover.Return)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("default settings do not validate: %v", err)
	}
}

func TestFailoverValidationRefusesWhatCannotStart(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(*db.Settings)
		wantErr bool
	}{
		{
			// Ports are gone, so the collision that used to be possible is
			// not. Both primary and backup arrive on the one SRT listener and
			// are told apart by token.
			name: "an SRT backup alongside an SRT primary is fine",
			mut: func(s *db.Settings) {
				s.Failover.Enabled = true
				s.Failover.Backup.Enabled = true
				s.Failover.Backup.Mode = db.IngestSRT
			},
			wantErr: false,
		},
		{
			// The last exclusivity, now gone. It existed because there was one
			// RTMP listener with no token routing on it, so a primary and a
			// backup could not both have it. internal/rtmpserver routes by
			// token, and the standby is reached at "<token>.backup" on the same
			// socket -- exactly as it already was over SRT.
			name: "an RTMP backup behind an RTMP primary is fine",
			mut: func(s *db.Settings) {
				s.Ingest.Mode = db.IngestRTMP
				s.Ingest.RTMP.App = "live"
				s.Failover.Enabled = true
				s.Failover.Backup.Enabled = true
				s.Failover.Backup.Mode = db.IngestRTMP
				s.Failover.Backup.RTMP.App = "live"
			},
			wantErr: false,
		},
		{
			// An RTMP backup is fine when the primary is not using RTMP.
			name: "an RTMP backup behind an SRT primary is fine",
			mut: func(s *db.Settings) {
				s.Failover.Enabled = true
				s.Failover.Backup.Enabled = true
				s.Failover.Backup.Mode = db.IngestRTMP
				s.Failover.Backup.RTMP.App = "live"
			},
			wantErr: false,
		},
		{
			name:    "a grace period of zero is a unit mistake",
			mut:     func(s *db.Settings) { s.Failover.GraceSeconds = 0 },
			wantErr: true,
		},
		{
			name:    "an unknown return mode",
			mut:     func(s *db.Settings) { s.Failover.Return = "sometimes" },
			wantErr: true,
		},
		{
			name:    "a slate image outside the data directory",
			mut:     func(s *db.Settings) { s.Failover.Slate.ImagePath = "../../etc/shadow" },
			wantErr: true,
		},
		{
			name:    "a slate image inside it",
			mut:     func(s *db.Settings) { s.Failover.Slate.ImagePath = "branding/slate.png" },
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := db.DefaultSettings()
			tt.mut(&s)

			err := s.Validate()

			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
