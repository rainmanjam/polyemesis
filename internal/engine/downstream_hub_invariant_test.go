package engine

// downstreamHub() NEVER RETURNS NIL, AND TWO CALL SITES DEPEND ON THAT.
//
// reconcileClips (engine.go, the clip capture) and the caption path both do:
//
//	port, err := e.allocPort()
//	...
//	hub := e.downstreamHub()
//	url, err := hub.Subscribe(clipSubName, port)
//
// with no nil check. relay.Hub.Subscribe dereferences h.advertise
// immediately, so a nil hub there is a panic in the reconcile loop rather
// than a returned error -- and a panic there takes down the goroutine that
// keeps every destination reconciled.
//
// They are correct today, and the reason is three facts none of which is
// local to them:
//
//  1. downstreamHub() is selectorHub() ?? silenceHub() ?? e.hub.
//  2. New() builds e.hub with relay.New and returns the error if that fails,
//     so a constructed Engine always has one.
//  3. e.hub is never reassigned anywhere in non-test code.
//
// The first two are visible; the third is a property of the whole package and
// is exactly the kind of thing a later change breaks without noticing. Add one
// `e.hub = nil` in a teardown path and those two call sites become a panic,
// with nothing between the edit and the crash.
//
// WHY NOT JUST ADD THE NIL CHECKS. Because a check against a state that cannot
// occur is not free: it tells the next reader the state CAN occur, and they
// then reason about a failover that does not produce it. That misreading cost
// an afternoon here -- #792 shipped with a description claiming the sibling
// branch was reachable during a failover, which it is not. The invariant is
// the honest thing to write down, so it is written down as a test.
//
// Rung 2. Control would mean a type that cannot be nil, which Go does not
// offer for a pointer field without boxing every access.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// assignsEngineHub matches a WRITE to the engine's own hub field.
//
// THE LEADING BOUNDARY IS THE WHOLE POINT. A plain `strings.Contains(s,
// "e.hub")` also matches `e.silence.hub`, because "silenc<e.hub>" contains it
// -- which is how the first version of this test reported two comparison
// lines in silence.go as assignments. A guard whose first run cries wolf is a
// guard that gets deleted.
var assignsEngineHub = regexp.MustCompile(`(^|[^.\w])e\.hub\s*(,[^=]*)?=[^=]`)

func TestDownstreamHubIsNeverNil(t *testing.T) {
	e, _ := storeEngine(t)

	// The ordinary running state, and then the state a failover produces: no
	// selector, no silence tier. The fallthrough to e.hub is what has to hold.
	if got := e.downstreamHub(); got == nil {
		t.Fatal("downstreamHub() is nil on a freshly constructed engine")
	}

	e.mu.Lock()
	e.sel = nil
	e.silence = nil
	e.mu.Unlock()

	if got := e.downstreamHub(); got == nil {
		t.Fatal("downstreamHub() is nil with no selector and no silence tier. " +
			"reconcileClips and the caption path call hub.Subscribe on this " +
			"without a nil check, so this is a panic in the reconcile loop.")
	}
}

// And the property the test above cannot observe at runtime: that nothing
// assigns e.hub after construction. A source check, because the failure is a
// future edit rather than a current state.
//
// SCOPED TO ASSIGNMENT, not to mentions. `e.hub` is read in plenty of places
// and passed around freely; what must not happen is a write.
func TestNothingReassignsTheEngineHub(t *testing.T) {
	// The test binary runs with the package directory as its working
	// directory, so no root-finding is needed to read the package's own source.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}

	checked := 0
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		// Tests DO set it -- TestAPreviewStartWhoseHubVanishesReleasesItsPort
		// nils it deliberately to reach a branch production cannot. That is what
		// a seam is for and is not what this guards.
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		checked++
		for i, line := range strings.Split(string(b), "\n") {
			s := strings.TrimSpace(line)
			if strings.HasPrefix(s, "//") {
				continue
			}
			if assignsEngineHub.MatchString(s) {
				offenders = append(offenders, name+":"+itoa(i+1)+": "+s)
			}
		}
	}

	// POSITIVE CONTROL. A wrong working directory or a bad filter reads no
	// files, and "nothing assigns it" is then true of nothing at all.
	if checked < 10 {
		t.Fatalf("read %d non-test go files in this package; the walk is wrong, so "+
			"this test has not looked at the package it is about", checked)
	}

	if len(offenders) > 0 {
		t.Errorf("e.hub is assigned after construction:\n  %s\n\n"+
			"reconcileClips and the caption path call hub.Subscribe(...) on "+
			"downstreamHub() with no nil check, and downstreamHub() falls through "+
			"to e.hub. Assigning it -- especially to nil in a teardown -- turns "+
			"those into a panic in the reconcile loop. Either keep it immutable "+
			"after New(), or give those two call sites the nil check they "+
			"currently do not need.", strings.Join(offenders, "\n  "))
	}
}
