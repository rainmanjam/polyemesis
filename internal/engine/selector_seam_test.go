package engine

import (
	"log/slog"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The seam ledger, tested by driving a real switch through it.
//
// Issue #126 is a backwards decode timestamp that appears at a failover switch
// in a minority of runs, and every occurrence so far has been recorded as a bare
// count in an acceptance script. The ledger exists to make the next occurrence
// attributable: which feed handed over to which, where each one's timeline was,
// and how long the teardown took.
//
// These tests execute ensureFeed and read what the engine actually logged. They
// deliberately do NOT read selector.go's source text: per issue #107 a test that
// greps for a string passes while the code path it claims to cover is dead, and
// three such tests did exactly that here. The assertions below only pass if
// ensureFeed really called logSeam with the real feeds.

// seamFields splits one slog TextHandler line into its key=value pairs.
//
// strings.Fields is enough because every field this test reads is a number or a
// bare word; `reason` is the only quoted one and nothing here parses it beyond
// checking that it is present.
func seamFields(line string) map[string]string {
	out := map[string]string{}
	for _, tok := range strings.Fields(line) {
		if k, v, ok := strings.Cut(tok, "="); ok {
			out[k] = v
		}
	}
	return out
}

// seamLines returns every ledger line in captured log output.
func seamLines(logged string) []map[string]string {
	var out []map[string]string
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, `msg="feed seam"`) {
			out = append(out, seamFields(line))
		}
	}
	return out
}

func seamFloat(t *testing.T, f map[string]string, key string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(f[key], 64)
	if err != nil {
		t.Fatalf("%s=%q is not a number: %v -- the ledger is parsed by awk in "+
			"scripts/acceptance-failover.sh, so an unparseable field is a broken ledger", key, f[key], err)
	}
	return v
}

func seamUint(t *testing.T, f map[string]string, key string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(f[key], 10, 64)
	if err != nil {
		t.Fatalf("%s=%q is not a generation number: %v", key, f[key], err)
	}
	return v
}

// TestASwitchWritesOneSeamLineNamingBothFeeds is the ledger's whole purpose: a
// switch that happened must be identifiable afterwards from the log alone.
func TestASwitchWritesOneSeamLineNamingBothFeeds(t *testing.T) {
	e := failoverEngine(t)
	var buf syncBuffer
	e.log = slog.New(slog.NewTextHandler(&buf, nil))

	s := failoverOnSettings()
	setSettings(e, s)

	t0 := time.Now()
	e.reconcileSelector(s, wantSelector(s), "")
	hub := e.selectorHub()
	if hub == nil {
		t.Fatal("the selector tier did not start")
	}
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		_ = e.teardownFeed(e.sel.feed)
		_ = hub.Close()
	})

	// THE FIRST FEED IS NOT A SEAM. Nothing has delivered yet, so the tier comes
	// up on the slate; there is no outgoing timeline to join to, and a line for it
	// would put rows in the ledger that the acceptance script would have to filter
	// back out before it could bucket backward steps by switch.
	if got := seamLines(buf.String()); len(got) != 0 {
		t.Fatalf("the first feed logged %d seam line(s); a start is not a handover", len(got))
	}

	// Put the primary on air, so the switch under test is the one the feature
	// exists for and the one #126 was seen at: a live source going quiet.
	e.deliver(sourcePrimary, t0)
	e.step(s, t0)

	e.mu.RLock()
	outFeed := e.sel.feed
	e.mu.RUnlock()
	if outFeed == nil || outFeed.kind != sourcePrimary {
		t.Fatalf("the primary did not go on air (feed %v), so there is no handover to measure", outFeed)
	}

	// The primary goes quiet past the grace period.
	buf.Reset()
	at := t0.Add(20 * time.Second)
	e.step(s, at)

	e.mu.RLock()
	inFeed, active := e.sel.feed, e.sel.active
	e.mu.RUnlock()
	if active != sourceSlate || inFeed == nil {
		t.Fatalf("active source = %q with feed %v; the switch under test did not happen",
			active, inFeed != nil)
	}

	lines := seamLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("one switch wrote %d seam lines, want exactly 1:\n%s", len(lines), buf.String())
	}
	f := lines[0]
	t.Logf("seam: %v", f)

	// The line names the two REAL feeds, not two plausible ones. Every value is
	// compared against the object the engine is holding, which is what makes this
	// a test of the call rather than of the format string.
	if got, want := f["outKind"], string(outFeed.kind); got != want {
		t.Errorf("outKind = %q, want the feed that was torn down (%q)", got, want)
	}
	if got, want := f["inKind"], string(inFeed.kind); got != want {
		t.Errorf("inKind = %q, want the feed that replaced it (%q)", got, want)
	}
	if got, want := seamUint(t, f, "outGen"), outFeed.gen; got != want {
		t.Errorf("outGen = %d, want %d", got, want)
	}
	if got, want := seamUint(t, f, "inGen"), inFeed.gen; got != want {
		t.Errorf("inGen = %d, want %d", got, want)
	}
	if outFeed.gen >= inFeed.gen {
		t.Errorf("the incoming feed's generation %d does not follow the outgoing %d; "+
			"two feeds sharing a number is the ambiguity the counter removes", inFeed.gen, outFeed.gen)
	}

	outOffset := seamFloat(t, f, "outOffset")
	inOffset := seamFloat(t, f, "inOffset")
	if math.Abs(outOffset-outFeed.offset) > 1e-6 {
		t.Errorf("outOffset = %v, want the outgoing feed's own offset %v", outOffset, outFeed.offset)
	}
	if math.Abs(inOffset-inFeed.offset) > 1e-6 {
		t.Errorf("inOffset = %v, want the incoming feed's own offset %v", inOffset, inFeed.offset)
	}

	// predictedStepMs is the falsifiable number the instrumentation was added
	// for, so it is checked against its own three inputs rather than trusted.
	outTimeMs := seamFloat(t, f, "outTimeMs")
	want := (outOffset + outTimeMs/1000 - inOffset) * 1000
	if got := seamFloat(t, f, "predictedStepMs"); math.Abs(got-want) > 1e-3 {
		t.Errorf("predictedStepMs = %v, want %v computed from the same line's own "+
			"outOffset/outTimeMs/inOffset -- a prediction that does not follow from its "+
			"inputs cannot falsify anything", got, want)
	}

	if teardown := seamFloat(t, f, "teardownMs"); teardown < 0 {
		t.Errorf("teardownMs = %v, want the wall time the switch spent stopping the old feed", teardown)
	}
	if f["stopDeadline"] != "false" {
		t.Errorf("stopDeadline = %q; this feed exited on its own, so the ledger must not "+
			"report that it was killed", f["stopDeadline"])
	}
	if f["outProgressDone"] == "" {
		t.Error("outProgressDone is missing; a zero out_time from a child that never emitted " +
			"a final progress block must be distinguishable from one that published nothing")
	}
	if f["reason"] == "" {
		t.Error("reason is missing; a seam nobody can attribute to a decision is half a record")
	}
}

// TestAFailedReplacementIsNotWrittenAsASeam guards the shape of the ledger, not
// just its contents.
//
// A teardown whose replacement never started has no incoming timeline, and the
// obvious way to record that -- the same message with a stand-in value for
// inOffset -- would poison the acceptance script: it buckets every backward step
// by the seam offsets in order, so a row sorting before all the real ones would
// quietly collect steps belonging to each of them. It is a different message, and
// this is the test that keeps it one.
func TestAFailedReplacementIsNotWrittenAsASeam(t *testing.T) {
	e := failoverEngine(t)
	var buf syncBuffer
	e.log = slog.New(slog.NewTextHandler(&buf, nil))

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
		_ = e.teardownFeed(e.sel.feed)
		_ = hub.Close()
	})

	// The backup is a kind the feed layer knows how to build, so ensureFeed does
	// NOT refuse it up front -- it tears the running feed down first and only
	// then discovers there is no backup hub to read. That gap is the case under
	// test, and it is reachable in production whenever the backup listener is
	// gone by the time the switch to it is carried out.
	buf.Reset()
	e.selMu.Lock()
	e.ensureFeed(s, "", sourceBackup, "the backup was chosen", time.Now())
	e.selMu.Unlock()

	e.mu.RLock()
	feed := e.sel.feed
	e.mu.RUnlock()
	if feed != nil {
		t.Fatalf("a backup feed started with no backup hub (%v); the case under test did not happen", feed.kind)
	}
	logged := buf.String()
	if got := seamLines(logged); len(got) != 0 {
		t.Errorf("a switch with no incoming feed was written as a seam: %v\n"+
			"the acceptance script buckets steps by inOffset and would mis-attribute every one of them", got)
	}
	if !strings.Contains(logged, "feed seam incomplete") {
		t.Errorf("the failed switch left no record at all: %s", logged)
	}
}

// TestEveryHandoverInACycleIsLedgered is the half that keeps the test above from
// being satisfied by a ledger that fires once and stops.
//
// A run of the acceptance suite performs several switches, and #126's leading
// explanation is about what is true at EVERY seam rather than at one of them, so
// a ledger that misses handovers cannot settle it.
func TestEveryHandoverInACycleIsLedgered(t *testing.T) {
	e := failoverEngine(t)
	var buf syncBuffer
	e.log = slog.New(slog.NewTextHandler(&buf, nil))

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
		_ = e.teardownFeed(e.sel.feed)
		_ = hub.Close()
	})

	buf.Reset()
	t0 := time.Now()
	// Slate (the tier's cold start) -> primary -> slate -> primary: three
	// handovers, in both directions.
	e.deliver(sourcePrimary, t0)
	e.step(s, t0)
	e.step(s, t0.Add(20*time.Second))
	e.deliver(sourcePrimary, t0.Add(30*time.Second))
	e.step(s, t0.Add(30*time.Second))

	e.mu.RLock()
	active := e.sel.active
	e.mu.RUnlock()
	if active != sourcePrimary {
		t.Fatalf("active source = %q, want the primary back on air", active)
	}

	lines := seamLines(buf.String())
	if len(lines) < 3 {
		t.Fatalf("a down-and-back cycle wrote %d seam lines, want one per handover:\n%s",
			len(lines), buf.String())
	}

	// Generations only ever go forward, and the incoming feed of one seam is the
	// outgoing feed of the next. That chain is what lets the script walk a run's
	// switches in order without guessing from timestamps alone.
	prevIn := uint64(0)
	for i, f := range lines {
		out, in := seamUint(t, f, "outGen"), seamUint(t, f, "inGen")
		if in <= out {
			t.Errorf("seam %d: inGen %d does not follow outGen %d", i, in, out)
		}
		if prevIn != 0 && out != prevIn {
			t.Errorf("seam %d: outGen %d is not the previous seam's inGen %d -- the chain "+
				"of handovers has a hole in it", i, out, prevIn)
		}
		prevIn = in
	}
}
