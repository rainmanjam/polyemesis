package main

// Tests for the bounded stop-poll that replaced three `time.Sleep(3s)` calls,
// issue #195.
//
// WHY A TEST FOR A SMOKE TEST
//
// scripts/smoketest.go is driven by ci.yml on ubuntu, macos AND windows, and
// nothing else in the repository executes it. The sleep it replaces could not
// be wrong in a way anybody would notice: it was followed by a Kill with no
// assertion on death, so a too-short sleep surfaced as a confusing failure in
// verify() two steps later, on one OS, in a step whose log is long.
//
// A wait that reports "still running 10s after /stop" is only worth having if
// it fires when that is true and does not fire when it is false. Those are the
// three cases below. The first is also the reason the change is worth making at
// all: the poll returns as soon as the processes are gone, where the sleep paid
// three seconds every run on every matrix OS.
//
// This file compiles against smoketest.go because it is the only Go file in
// scripts/ without a `//go:build ignore` tag -- every other main there is a
// `go run` driver and is excluded from the package.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// statusServer serves /api/v1/status from a function of the sample number, so a
// test can describe a state that CHANGES without any timing in it.
func statusServer(t *testing.T, dests func(sample int64) []map[string]any) (samples *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"destinations": dests(n.Add(1)),
		})
	}))
	t.Cleanup(srv.Close)

	oldBase, oldClient, oldInterval := base, client, stopPollInterval
	base = srv.URL + "/api/v1"
	client = srv.Client()
	// Fast enough that these tests assert on ORDER and CONTENT, never on
	// duration -- except case 1, whose ceiling is 40x the interval.
	stopPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { base, client, stopPollInterval = oldBase, oldClient, oldInterval })

	return &n
}

func dest(id any, name, state string) map[string]any {
	return map[string]any{
		"id": id, "name": name,
		"process": map[string]any{"state": state},
	}
}

// 1. The whole point of the change: the wait ENDS when the processes end,
// rather than paying a fixed budget every run.
//
// Mutation: `if len(still) == 0 {` -> `if len(still) == 0 &&
// !time.Now().Before(deadline) {` in waitStopped, so it notices they stopped
// but still waits out the whole budget.
// Observed to fail with "waitStopped burned its whole 2s budget on destinations
// that stopped after 3 samples".
func TestWaitStoppedReturnsAsSoonAsTheDestinationsAreGone(t *testing.T) {
	statusServer(t, func(sample int64) []map[string]any {
		state := "running"
		if sample >= 3 {
			state = "exited"
		}
		return []map[string]any{dest(1.0, "A", state), dest(2.0, "B", state)}
	})

	started := time.Now()
	still := waitStopped([]string{"1", "2"}, 2*time.Second)
	elapsed := time.Since(started)

	if len(still) != 0 {
		t.Fatalf("waitStopped reported %v still running on a server that stopped them", still)
	}
	// A ceiling of 200ms against a 2s budget: 40 poll intervals of headroom, so
	// this measures "it did not wait for the deadline" and not "the machine is
	// fast". The sleep it replaces would have been 3s here.
	if elapsed > 200*time.Millisecond {
		t.Errorf("waitStopped burned %s of its 2s budget on destinations that stopped after "+
			"3 samples: the poll is not ending early, so it costs the same fixed price on "+
			"every run of every matrix OS that the sleep did", elapsed.Round(time.Millisecond))
	}
}

// 2. The diagnostic half. A destination that never stops must be NAMED and the
// wait must be bounded -- the failure mode of an unbounded one is a CI step that
// hangs until the job timeout kills it and loses its log, which is issue #179
// exactly.
//
// Mutation: `return still` -> `return nil` at the deadline branch of
// waitStopped.
// Observed to fail with "waitStopped reported nothing still running while the
// server never stopped anything".
func TestWaitStoppedNamesADestinationThatNeverStops(t *testing.T) {
	statusServer(t, func(int64) []map[string]any {
		return []map[string]any{dest(1.0, "A", "running"), dest(2.0, "B", "exited")}
	})

	started := time.Now()
	still := waitStopped([]string{"1", "2"}, 300*time.Millisecond)
	elapsed := time.Since(started)

	if len(still) != 1 || still[0] != "1" {
		t.Errorf("waitStopped reported %v; destination 1 never left the running state and "+
			"destination 2 did, so exactly one of them should be named", still)
	}
	if elapsed > 5*time.Second {
		t.Errorf("waitStopped ran %s against a 300ms ceiling: an unbounded wait in a CI step "+
			"is what burns a job timeout and loses the log with it", elapsed)
	}
}

// 3. Only the destinations that were STOPPED are watched.
//
// The E-RTMP and SRT phases stop two destinations each while the earlier
// phases' destinations are still in /status -- and in phase 1's case still
// deliberately running. A wait that watched every row would never finish, and
// would then blame the destination it had just stopped.
//
// Mutation: delete the `if !want[id] { continue }` filter in runningAmong.
// Observed to fail with "waitStopped reported [1] still running; it is watching
// destinations nobody stopped".
func TestWaitStoppedIgnoresDestinationsNobodyStopped(t *testing.T) {
	statusServer(t, func(int64) []map[string]any {
		return []map[string]any{
			dest(1.0, "A-from-an-earlier-phase", "running"),
			dest(3.0, "C", "exited"),
			dest(4.0, "D", "exited"),
		}
	})

	still := waitStopped([]string{"3", "4"}, 300*time.Millisecond)
	if len(still) != 0 {
		t.Errorf("waitStopped reported %v still running; it is watching destinations nobody "+
			"stopped, so every phase after the first would wait out its whole ceiling and "+
			"then accuse the wrong destination", still)
	}
}
