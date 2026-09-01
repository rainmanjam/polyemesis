package supervisor

import (
	"sort"
	"sync"
)

// Teardown counters, and the denominator is the whole point.
//
// WHAT HAPPENED WITHOUT ONE. The escalation at the bottom of stop() logs one
// WARN per occurrence and nothing counts it. On the production host that line
// appeared 53 times over three weeks and nobody saw it, because a log line is
// rung 3 only for whoever decides to go looking, and nobody decides to go
// looking at a system that appears to work. It surfaced when an operator said
// switching ingest mode "felt slow".
//
// THE SUCCESS PATH IS NOT MERELY UNCOUNTED, IT IS UNOBSERVABLE. supervise()
// returns as soon as the context is cancelled, which is BEFORE the "process
// exited" logs further down -- so a teardown that goes perfectly writes nothing
// at all. The journal on that host holds 53 escalations and no clean teardown
// of any kind, not because none happened but because none could be recorded.
//
// That is why an investigation of this bug divided 53 by an unrelated number
// and reported a ratio nobody could have computed. The absent denominator
// misleads in both directions: it hides the problem from whoever is running the
// server, and it invites a wrong number from whoever finally looks. "53
// children were killed" has no scale, and some kills are legitimate because a
// genuinely wedged FFmpeg exists. "53 of 57" is an incident. The difference is
// entirely the denominator, and until now there was nowhere for one to live.
//
// So this counts BOTH outcomes. A device that counts only failures can report
// that failures happened; it can never report that the exception became the
// rule, which is the thing that actually went wrong here.
//
// WHY NOT ON Process. Process.Restarts is the tempting precedent and it is the
// wrong one: it lives and dies with the object it describes. A destination that
// is torn down no longer has a Process, so a per-Process kill counter resets at
// exactly the moment it would have had something to say. These are package
// level and outlive every child they count.
//
// Rung 3 (detection), deliberately. Control would mean a teardown that cannot
// outlive its stop, and every mechanism for that -- PR_SET_PDEATHSIG, cgroup
// kill, the Windows job object -- converts "outlives its stop" into "loses its
// flush", which turns a latency bug into a truncated recording. The escalation
// exists precisely so a wedged child dies; making it die faster is not the fix.

// TeardownStats is one kind's tally. Kills are a subset of Total.
type TeardownStats struct {
	Kind  string `json:"kind"`
	Total int64  `json:"total"`
	Kills int64  `json:"kills"`
}

var (
	teardownMu    sync.Mutex
	teardownTotal = map[string]int64{}
	teardownKills = map[string]int64{}
)

// noteTeardown records one completed stop. killed reports whether it had to be
// escalated to SIGKILL after the grace period expired.
//
// Called from exactly one place -- the select that decides the outcome -- so
// the two counters cannot drift apart. Incrementing them at separate call sites
// is how a denominator quietly stops matching its numerator.
func noteTeardown(kind string, killed bool) {
	if kind == "" {
		kind = "unknown"
	}
	teardownMu.Lock()
	defer teardownMu.Unlock()
	teardownTotal[kind]++
	if killed {
		teardownKills[kind]++
	}
}

// Teardowns returns the tally per kind, sorted by kind so callers rendering it
// get a stable order rather than Go's map iteration.
//
// Process-lifetime counters, not persisted: these describe THIS run of the
// server. A restart zeroes them, which is correct for the question they answer
// ("is teardown working right now") and wrong for "has it ever" -- that one is
// the log's job, and the log has it.
func Teardowns() []TeardownStats {
	teardownMu.Lock()
	defer teardownMu.Unlock()
	out := make([]TeardownStats, 0, len(teardownTotal))
	for kind, total := range teardownTotal {
		out = append(out, TeardownStats{Kind: kind, Total: total, Kills: teardownKills[kind]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// resetTeardownsForTest clears the tally. Package-level state is shared across
// tests in a package that runs them in one process, so a test asserting counts
// has to start from a known floor.
func resetTeardownsForTest() {
	teardownMu.Lock()
	defer teardownMu.Unlock()
	teardownTotal = map[string]int64{}
	teardownKills = map[string]int64{}
}
