package supervisor

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// The census answers a question nothing in this package could answer before:
// WHAT HAVE WE ACTUALLY SPAWNED?
//
// Every child was reachable only through the one field holding its Process --
// e.meters, e.preview, e.recorder, e.ingest, an entry in the destinations map.
// That is fine while the field holds. When it does not, the child is not
// leaked in the ordinary sense of "we still have a reference we forgot to
// free": it is UNREACHABLE. Nothing can signal it, nothing can report it, and
// nothing can even count it. The only instrument that sees it is `ps` on the
// host, run by a person who already suspects.
//
// #631 is exactly that shape and says so in its own words: an ffmpeg with
// `ppid` pointing at the LIVE server, "reaped by this teardown, so the host is
// left clean -- but nothing in polyemesis did it, and outside systemd nothing
// would have." No SIGKILL escalation was logged, because no escalation
// happened; nothing held the handle to escalate with.
//
// WHY THIS IS KEYED ON THE OS CHILD RATHER THAN ON THE Process. Registering a
// Process when it is constructed would inherit the same blind spot: a Process
// nobody holds is still a Process the census holds, so a lost handle would look
// identical to a live one. The entries here are created where a pid comes into
// existence and removed where cmd.Wait() returns, so what the census counts is
// what the operating system counts. A child whose supervisor has been dropped
// on the floor still appears, which is the entire point.
//
// PACKAGE-LEVEL, deliberately, for the same reason. The pid table belongs to
// the OS process, and threading ownership through an Engine would mean a leaked
// child is invisible exactly when its owner is the thing that went missing.
//
// This is a DETECTION device, not a control: it does not stop the mistake, and
// it does not reap anything. It makes a mistake that was previously invisible
// from inside the program answerable from inside the program -- which is what
// turns "somebody noticed ffmpeg on the host three weeks later" into a test
// that fails in seconds.

// Child is one live OS child, as the census sees it.
type Child struct {
	// PID is the child's own pid, not its process group.
	PID int
	// Name and Kind are the Spec's, so a report names the thing an operator
	// recognises ("meters", "dest:studio-a") rather than a number.
	Name string
	Kind string
	// Since is when the child was spawned, so a report can say how long it has
	// outlived whatever should have reaped it.
	Since time.Time
}

func (c Child) String() string {
	return fmt.Sprintf("%s (%s) pid %d, up %s", c.Name, c.Kind, c.PID, time.Since(c.Since).Round(time.Millisecond))
}

var census struct {
	mu   sync.Mutex
	live map[int]Child
}

// enrol records a child that has just been spawned. Called with the pid from a
// successful cmd.Start(), because before that there is nothing to enrol and
// after a failed start there never was.
func enrol(pid int, name, kind string) {
	if pid <= 0 {
		return
	}
	census.mu.Lock()
	defer census.mu.Unlock()
	if census.live == nil {
		census.live = make(map[int]Child)
	}
	census.live[pid] = Child{PID: pid, Name: name, Kind: kind, Since: time.Now()}
}

// discharge records that a child has been reaped. Called where cmd.Wait()
// returns, which is the only moment the pid is genuinely gone -- signalling it
// is not, because a child that ignores SIGTERM is still very much a child.
func discharge(pid int) {
	if pid <= 0 {
		return
	}
	census.mu.Lock()
	defer census.mu.Unlock()
	delete(census.live, pid)
}

// Live returns every child this process has spawned and not yet reaped, oldest
// first, so a caller reporting them leads with the one that has been wrong for
// longest.
//
// Exported because the callers that need it are outside this package: the
// engine's shutdown, which can now assert it reaped what it started, and the
// tests that would otherwise have to shell out to ps.
func Live() []Child {
	census.mu.Lock()
	out := make([]Child, 0, len(census.live))
	for _, c := range census.live {
		out = append(out, c)
	}
	census.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Since.Equal(out[j].Since) {
			return out[i].PID < out[j].PID
		}
		return out[i].Since.Before(out[j].Since)
	})
	return out
}

// LiveCount is Live() for a caller that only wants the number, without building
// the slice on a hot path.
func LiveCount() int {
	census.mu.Lock()
	defer census.mu.Unlock()
	return len(census.live)
}
