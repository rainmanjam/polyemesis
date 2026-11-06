package engine

import (
	"slices"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// The expert-mode preview of a RUNNING destination needs the command it is
// running without the expert arguments its row carries, so a candidate edit
// shows the candidate and not the candidate stacked on the last one. It used to
// be recovered by peeling the saved arguments back off the child's live argv
// (ffmpeg.StripExtraArgs), which depended on where DestinationArgs lays them
// out. The engine now keeps what builds the argv and answers directly.
func TestARunningDestinationsBaseCommandOmitsItsExpertArguments(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	row.ExtraOutputArgs = "-muxdelay 0.1"
	plan := planFor(t, e, row)

	argv := &destArgv{
		e: e, row: row, url: "udp://127.0.0.1:20999", graph: newGraphHolder(plan.compiled),
		out: row.URL,
	}
	running := argv.build(row.URL)
	if !slices.Contains(running, "-muxdelay") {
		t.Fatalf("the running command lacks the saved expert argument, so this tests nothing: %q", running)
	}
	base := argv.base()
	if slices.Contains(base, "-muxdelay") {
		t.Errorf("the base command still carries the saved expert argument: %q", base)
	}
	if !slices.Contains(base, "udp://127.0.0.1:20999?fifo_size=32768&overrun_nonfatal=1") {
		t.Errorf("the base command does not read the destination's real relay URL, which is the reason "+
			"the preview starts from the running command: %q", base)
	}
	if len(base) >= len(running) {
		t.Errorf("the base command (%d) is not the running one (%d) minus its extras", len(base), len(running))
	}

	e.mu.Lock()
	e.dests[row.ID] = &destination{
		row: row, argv: argv, proc: supervisor.New(testLogger(), supervisor.Spec{Name: "dest:live"}),
	}
	e.mu.Unlock()
	bin, got, ok := e.DestinationBaseArgv(row.ID)
	if !ok || bin != e.tools.FFmpeg || !slices.Equal(got, base) {
		t.Errorf("DestinationBaseArgv = %q %q %v, want the base command", bin, got, ok)
	}
	if _, _, ok := e.DestinationBaseArgv(row.ID + 1); ok {
		t.Error("a destination that is not running has a base command")
	}
}

// A level applied live moves the holder and not the running argv; stale is the
// comparison that says a respawn is owed, made between the two things the holder
// owns rather than by reading the graph back out of the child's argv.
func TestAHolderIsStaleOnlyWhenTheGraphItBuiltFromHasMoved(t *testing.T) {
	e, store := storeEngine(t)
	unmuted := planFor(t, e, liveRow(t, store, false)).compiled
	muted := planFor(t, e, liveRow(t, store, true)).compiled

	holder := newGraphHolder(unmuted)
	if holder.stale() {
		t.Error("a holder nothing has built a command from is stale")
	}
	_ = holder.argv(func(routing.Result) []string { return nil })
	if holder.stale() {
		t.Error("a holder is stale right after building from its own graph")
	}
	holder.set(muted)
	if !holder.stale() {
		t.Error("a live mute that no command has been built from yet did not read as stale")
	}
	_ = holder.argv(func(routing.Result) []string { return nil })
	if holder.stale() {
		t.Error("a respawn built from the muted graph is still reported stale")
	}
}
