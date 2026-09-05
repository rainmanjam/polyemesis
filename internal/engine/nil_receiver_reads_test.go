package engine

import (
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// A nil *Engine is what the API holds on an install with no source, so which
// methods answer one and which refuse it is a CONTRACT, not an accident of
// whichever field each happens to touch first.
//
// The two halves are equally load-bearing. A read that panics is a 500 on the
// first screen an operator ever sees. A MUTATION that answers is worse: it is a
// 200 OK for something that did not happen -- Reconcile reporting success
// having reconciled nothing, PreviewRequested acknowledging a preview nobody
// started. So this table pins both directions, and a nil-safety pass that
// wanders past the reads fails here rather than in production.
//
// Derived from the method set rather than hand-listed: every argumentless
// exported method must appear below, so adding one without deciding what it
// does with no engine is a build-time argument with this file.
//
// Methods that take arguments are out of scope. Every one of them is a
// mutation or a lookup the API guards at the boundary, and calling them here
// would mean inventing arguments whose validity is the thing under test
// somewhere else.
var nilEngineAnswers = map[string]bool{
	// The zero-source read contract.
	"Status":      true,
	"SourceInfo":  true,
	"SourceKnown": true,
	"Levels":      true,
	"Processes":   true,
	"Alerts":      true,
	// The delivery budget the manager pushed in. Zero is the honest answer on
	// an install with no programme: nobody pushed one, so nothing is clamped
	// and the alerts package default is what a delivery would use. Answering
	// rather than refusing keeps it in step with Alerts, which is the object it
	// describes.
	"AlertRetry": true,
	"Loudness":   true,
	// The Meters page's switch, which cannot assert a state it has not been
	// told. An install with no engine has no analyser tier running, so `false`
	// is the true answer rather than a placeholder -- and the page draws the
	// switch off rather than 500ing on the first screen.
	"LoudnessMonitorEnabled": true,
	"ClipBuffer":             true,
	"Failover":               true,
	"SourceID":               true,
	// Not a guard of its own: GPUBusy is Status plus a fold, so it inherits the
	// answer. Recorded rather than left to be rediscovered, because it is the
	// one method here whose behaviour is decided in another file.
	"GPUBusy": true,

	// Refusals. Three different reasons, all deliberate:
	//
	// MUTATIONS -- Reconcile, PreviewRequested, Stop. MUST NOT #2: a nil-safe
	// mutation is a success report for work that never happened.
	"Reconcile":        false,
	"PreviewRequested": false,
	"Stop":             false,
	// COMMAND-LINE INPUTS -- Source discards the "known" bit that SourceKnown
	// keeps, and every caller of it compiles a filtergraph from what it gets
	// back. Answering with the placeholder layout would hand an operator six
	// tracks that do not exist, labelled as their routing. The API refuses
	// those routes instead.
	"Source": false,
	// PIPELINE HANDLES -- there is no relay, no sampler, no playout muxer and
	// no supervised recorder on an install with no programme, and a nil one
	// only moves the panic to the caller's next line. The API reads the
	// install-wide equivalents off the manager instead; see Server.tools,
	// Server.hostSystem and Server.recordings.
	"BackupHub":  false,
	"Hooks":      false,
	"Hub":        false,
	"Monitor":    false,
	"Playout":    false,
	"Recordings": false,
	"Tools":      false,
	// STATE THAT IS NOT THIS ENGINE'S TO INVENT -- the settings snapshot is the
	// store's (the API reads it there), and the rest describe children that
	// only exist while a programme is running.
	"Clips":        false,
	"ClipUsage":    false,
	"IngestLive":   false,
	"OutputLive":   false,
	"LastReload":   false,
	"LiveCaptions": false,
	"Renditions":   false,
	"Settings":     false,
	"Silence":      false,
	"SourceName":   false,
}

// argumentlessEngineMethods is every exported method on *Engine that takes
// nothing but its receiver.
func argumentlessEngineMethods() []reflect.Method {
	t := reflect.TypeOf((*Engine)(nil))
	var out []reflect.Method
	for i := range t.NumMethod() {
		m := t.Method(i)
		// NumIn counts the receiver, so one argument means none of its own.
		if m.Type.NumIn() == 1 {
			out = append(out, m)
		}
	}
	return out
}

func TestEveryArgumentlessEngineMethodSaysWhatANilReceiverGets(t *testing.T) {
	methods := argumentlessEngineMethods()
	if len(methods) < 20 {
		t.Fatalf("only %d argumentless methods found on *Engine; reflection is not "+
			"seeing the method set and every assertion in this file is vacuous", len(methods))
	}
	seen := make(map[string]bool, len(methods))
	for _, m := range methods {
		seen[m.Name] = true
		if _, ok := nilEngineAnswers[m.Name]; !ok {
			t.Errorf("Engine.%s is not in nilEngineAnswers: say whether an install with "+
				"no source gets an answer from it or a refusal. A read answers; a mutation "+
				"must not.", m.Name)
		}
	}
	for name := range nilEngineAnswers {
		if !seen[name] {
			t.Errorf("nilEngineAnswers still classifies Engine.%s, which no longer exists "+
				"or now takes arguments", name)
		}
	}
}

// calledOnNil reports whether the method panicked.
func calledOnNil(m reflect.Method) (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	m.Func.Call([]reflect.Value{reflect.ValueOf((*Engine)(nil))})
	return false
}

func TestANilEngineAnswersTheReadsAndRefusesEverythingElse(t *testing.T) {
	for _, m := range argumentlessEngineMethods() {
		want, classified := nilEngineAnswers[m.Name]
		if !classified {
			continue // the test above is the one that reports this
		}
		panicked := calledOnNil(m)
		switch {
		case want && panicked:
			t.Errorf("Engine.%s panicked on a nil receiver. It is a zero-source read: "+
				"an install with no programme reaches it on every request, and a panic "+
				"there is a 500 on the page the operator recovers through.", m.Name)
		case !want && !panicked:
			t.Errorf("Engine.%s answered a nil receiver. Nothing in the pipeline exists "+
				"to answer for, so whatever it returned is invented -- and if it writes, "+
				"the caller has just been told work happened that did not.", m.Name)
		}
	}
}

// The values, not merely the absence of a panic. An accessor that answered with
// something meaningless would pass the table above and still put a fabricated
// number on a dashboard.
func TestWhatANilEngineActuallyReports(t *testing.T) {
	var e *Engine

	st := e.Status()
	if st.Renditions == nil || st.Destinations == nil {
		t.Errorf("Status returned nil slices (%v, %v); ui/src/lib/types.ts declares both "+
			"non-nullable, so a null is a type lie the dashboard walks into",
			st.Renditions, st.Destinations)
	}
	if len(st.Renditions) != 0 || len(st.Destinations) != 0 {
		t.Errorf("Status invented rows with no engine: %v %v", st.Renditions, st.Destinations)
	}
	if st.Ingest != nil || st.Recorder != nil || st.Failover != nil {
		t.Errorf("Status reported a process on an install that runs none: %+v", st)
	}

	if got := e.SourceID(); got != 0 {
		t.Errorf("SourceID = %d with no engine, want 0: any other value addresses a row", got)
	}
	if info := e.SourceInfo(); info.ID != 0 || info.Probed {
		t.Errorf("SourceInfo = %+v, want the unprobed zero value", info)
	}
	if _, known := e.SourceKnown(); known {
		t.Error("SourceKnown reported a MEASURED layout with no engine to have probed one")
	}
	if src, _ := e.SourceKnown(); len(src.Tracks) == 0 {
		t.Error("SourceKnown returned no layout at all; the routing editor renders this, " +
			"and the placeholder is what an unprobed engine has always handed it")
	}
	if reports := e.Loudness(); reports == nil {
		t.Error("Loudness returned nil rather than an empty report set")
	}
	if e.Failover() != nil {
		t.Error("Failover reported a selector tier on an install with no engine")
	}
	// Scheduler is no longer here to ask: there is ONE runner and it belongs to
	// the Manager, because `schedules` has no source_id and a timetable is a
	// property of the box. See Manager.Scheduler and #526.
	if e.Alerts() != nil {
		t.Error("Alerts handed back a notifier that cannot exist; its callers " +
			"test for nil and refuse, which is how the test-send route avoids " +
			"reporting \"sent\" for a webhook nobody sent")
	}
	if _, at := e.Levels(); !at.IsZero() {
		t.Errorf("Levels reported a measurement time of %v with nothing metering", at)
	}
	if c := e.ClipBuffer(); c.Enabled || c.Running {
		t.Errorf("ClipBuffer = %+v, want the off card", c)
	}
	if len(e.Processes()) != 0 {
		t.Error("Processes listed a supervised child with no engine to supervise one")
	}
}

// The wire shape, because that is what the UI parses. A Go slice being empty
// and its JSON being [] are the same thing only while the field has no
// omitempty, and this is the assertion that notices if one is ever added.
func TestANilEngineStatusSerialisesBothSlicesAsEmptyArrays(t *testing.T) {
	var e *Engine
	b, err := json.Marshal(e.Status())
	if err != nil {
		t.Fatalf("marshal Status: %v", err)
	}
	body := string(b)
	for _, want := range []string{`"renditions":[]`, `"destinations":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("zero-source Status did not carry %s: %s", want, body)
		}
	}
	for _, bad := range []string{`"renditions":null`, `"destinations":null`} {
		if strings.Contains(body, bad) {
			t.Errorf("zero-source Status carried %s, which the TypeScript type says "+
				"cannot happen: %s", bad, body)
		}
	}
}

// panicValueOnNil returns what a method panicked WITH, rather than only whether
// it panicked.
func panicValueOnNil(m reflect.Method) (v any) {
	defer func() { v = recover() }()
	m.Func.Call([]reflect.Value{reflect.ValueOf((*Engine)(nil))})
	return nil
}

// THE REFUSALS MUST BE SOFTWARE PANICS, NOT HARDWARE FAULTS. #440.
//
// The test above asks whether each method panics, and a nil dereference panics
// too -- so it passed for the entire time these methods were faulting rather
// than refusing, and could never have told the two apart.
//
// The difference is not cosmetic on Windows. A hardware nil check raises a real
// EXCEPTION_ACCESS_VIOLATION, and Go's recovery from one writes below the
// goroutine's stack into the adjacent heap span: golang/go#81238, open. The
// damage surfaces later and elsewhere -- `found pointer to free object`,
// `s.allocCount != s.nelems`, a fault inside the collector -- which is fifteen
// Windows CI aborts in 1,607 runs, always in this package, never on Unix,
// because Unix delivers signals on a signal stack and never on the goroutine's.
// This test's own sibling above is what took the fault, on every run.
//
// A runtime.Error here means the guard is gone and the hardware path is back.
// That is invisible in every other way: the method still panics, the contract
// test still passes, and CI goes on corrupting a heap roughly once in ninety
// runs on whichever host draws the short straw.
func TestEveryRefusalIsASoftwarePanicRatherThanANilDereference(t *testing.T) {
	var checked int
	for _, m := range argumentlessEngineMethods() {
		answers, classified := nilEngineAnswers[m.Name]
		if !classified || answers {
			continue
		}
		checked++
		v := panicValueOnNil(m)
		if v == nil {
			t.Errorf("%s did not panic on a nil receiver at all", m.Name)
			continue
		}
		if _, isRuntime := v.(runtime.Error); isRuntime {
			t.Errorf("%s panicked with a runtime.Error (%v), which means it "+
				"DEREFERENCED the nil receiver rather than refusing it. On Windows that "+
				"is an EXCEPTION_ACCESS_VIOLATION whose recovery writes below the "+
				"goroutine stack and corrupts the heap (golang/go#81238). Add "+
				"e.requireEngine(%q) as the method's first statement.", m.Name, v, m.Name)
			continue
		}
		s, ok := v.(string)
		if !ok || !strings.Contains(s, m.Name) {
			t.Errorf("%s panicked with %#v; want a string naming the method, so an "+
				"operator reading a 500 knows which call refused", m.Name, v)
		}
	}
	// A floor, because a classification table that stopped matching the method
	// set would make every assertion above run zero times and report success.
	if checked < 15 {
		t.Fatalf("only %d refusing methods were checked; nilEngineAnswers has "+
			"drifted from the method set and this test is now vacuous", checked)
	}
}
