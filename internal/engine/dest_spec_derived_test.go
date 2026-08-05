package engine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// D1. destSpec and backupSpecOf were two hand-written lists over the same
// ffmpeg.DestSpec inputs, sitting 264 lines apart. Adding a field to DestSpec
// needed three edits, and missing the third was silent and asymmetric: the
// primary picked the setting up, the backup kept the old command line, and the
// two feeds the platform received differed with nothing reporting it.
//
// The drift had already started. compiled.OutLabel is a destArgs input and was
// in NEITHER hash -- latent only because it currently co-varies with
// FilterComplex, which is exactly how this class stays hidden.
//
// Mutation: in destArgvSig, replace `s.RelayURL = ""` with
// `s = ffmpeg.DestSpec{Kind: s.Kind, Target: s.Target, FilterComplex: s.FilterComplex}`
// -- the hand-written subset this replaced, which is what the old hashes were.
// Observed to fail on both hashes. (A mutation inside destSpecFor cannot be
// hash-only any more, which is the point of the change: it would move the
// command line too, and the first assertion below says so.)
func TestTheAudioOutLabelReachesBothRestartHashes(t *testing.T) {
	row := backupRow()
	// Same filter string, different output label. That is the pair the old
	// hashes could not tell apart, and the pair that produces two different
	// command lines.
	a := routing.Result{FilterComplex: "[0:a:0]anull[aout]", OutLabel: "[aout]"}
	b := routing.Result{FilterComplex: "[0:a:0]anull[aout]", OutLabel: "[other]"}

	e := &Engine{log: testLogger()}
	argvA := strings.Join(e.destArgs(row, a, "udp://127.0.0.1:1", row.Target()), " ")
	argvB := strings.Join(e.destArgs(row, b, "udp://127.0.0.1:1", row.Target()), " ")
	if argvA == argvB {
		t.Error("the graph's output label does not reach the command line at all; " +
			"there is nothing here for a hash to predict")
	}

	if (destSpec(row, a, "up") == destSpec(row, b, "up")) != (argvA == argvB) {
		t.Error("the primary's hash disagrees with the argv about the graph's output " +
			"label, so changing it is stored and never applied to the running process")
	}
	if (backupSpecOf(row, a, "up") == backupSpecOf(row, b, "up")) != (argvA == argvB) {
		t.Error("the backup's hash disagrees with the argv about the graph's output " +
			"label, so the two feeds the platform receives would differ with nothing " +
			"reporting it")
	}
}

// The general form of the same thing, and the guard that makes the NEXT field
// added to ffmpeg.DestSpec safe rather than the last one.
//
// Every field of the built spec has to move the signature, because every field
// of it is on the command line. RelayURL is the deliberate exception: it is the
// allocated port, new on every start, so hashing it would make a destination's
// signature differ from itself and nothing would ever be left running.
//
// Mutation: replace destArgvSig's body with a hand-written subset --
// `return string(s.Kind) + s.Target + s.FilterComplex`. Observed to fail,
// naming eleven fields.
func TestEveryFieldOfTheCommandLineMovesTheSignature(t *testing.T) {
	base := destSpecFor(testLogger(), backupRow(),
		routing.Result{FilterComplex: "anull", OutLabel: "a"},
		"udp://127.0.0.1:1", "rtmp://live.example/app/key")
	baseSig := destArgvSig(base)

	leaves := leafPaths(reflect.TypeOf(base), "")
	if len(leaves) < 10 {
		t.Fatalf("found %d leaf fields on ffmpeg.DestSpec, want at least 10; the "+
			"walker is broken, which would make this guard pass by finding nothing", len(leaves))
	}

	for _, path := range leaves {
		t.Run(path, func(t *testing.T) {
			bumped := base
			setLeaf(reflect.ValueOf(&bumped).Elem(), path)
			got := destArgvSig(bumped)
			if path == "RelayURL" {
				if got != baseSig {
					t.Error("RelayURL moved the signature: the relay port is new on " +
						"every start, so every destination's hash would differ from itself")
				}
				return
			}
			if got == baseSig {
				t.Errorf("%s is on the command line and not in the signature, so "+
					"changing it is stored and silently never applied -- and the "+
					"primary and the backup drift apart", path)
			}
		})
	}
}

// leafPaths lists every field of a struct, descending into nested structs, so
// Transport and Audio are covered field by field rather than as one opaque
// value.
func leafPaths(t reflect.Type, prefix string) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		name := prefix + f.Name
		if f.Type.Kind() == reflect.Struct {
			out = append(out, leafPaths(f.Type, name+".")...)
			continue
		}
		out = append(out, name)
	}
	return out
}

// setLeaf gives the named field a value distinct from the one it holds.
func setLeaf(v reflect.Value, path string) {
	for _, name := range strings.Split(path, ".") {
		v = v.FieldByName(name)
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "-changed")
	case reflect.Int:
		v.SetInt(v.Int() + 7)
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Slice:
		v.Set(reflect.ValueOf([]string{"-changed"}))
	default:
		panic("setLeaf: unhandled kind " + v.Kind().String() + " at " + path)
	}
}

// The two verdicts stay separate even though the inputs are now shared. This is
// the property the unification could most easily have destroyed: derive both
// from one value and it is one line's carelessness to derive one from the
// other, at which point enabling redundancy drops the stream it protects.
//
// Mutation: in backupSpecOf, replace `backupTarget(row)` with `row.Target()`.
// Observed to fail -- the backup then hashes identically for a rotated backup
// key.
func TestTheSharedInputsDidNotMergeTheTwoVerdicts(t *testing.T) {
	compiled := routing.Result{FilterComplex: "anull", OutLabel: "a"}
	before, after := backupRow(), backupRow()
	after.BackupStreamKey = "rotated"

	if destSpec(before, compiled, "up") != destSpec(after, compiled, "up") {
		t.Error("rotating the BACKUP key moved the primary's hash, so the primary " +
			"would be dropped to deliver redundancy it already had")
	}
	if backupSpecOf(before, compiled, "up") == backupSpecOf(after, compiled, "up") {
		t.Error("rotating the backup key did not move the backup's hash, so it would " +
			"keep publishing to an endpoint that no longer exists")
	}
	if destSpec(before, compiled, "up") == backupSpecOf(before, compiled, "up") {
		t.Error("the two hashes are the same value; they are not two verdicts any more")
	}
}

// Nothing above would notice if the argv builder and the hash stopped reading
// the same description. This pins the seam itself.
func TestTheArgvAndTheHashReadTheSameSpec(t *testing.T) {
	row := backupRow()
	compiled := routing.Result{FilterComplex: "anull", OutLabel: "a"}
	e := &Engine{log: testLogger()}

	spec := destSpecFor(e.log, row, compiled, "udp://127.0.0.1:1", row.Target())
	fromSpec := ffmpeg.DestinationArgs(spec)
	fromEngine := e.destArgs(row, compiled, "udp://127.0.0.1:1", row.Target())
	if !equalArgs(fromSpec, fromEngine) {
		t.Fatalf("destArgs does not render destSpecFor's value:\n got %v\nwant %v",
			fromEngine, fromSpec)
	}
	// And the hash is taken from that same value, with only the relay cleared.
	spec.RelayURL = ""
	if destArgvSig(spec) != destArgvSig(destSpecFor(nil, row, compiled, "", row.Target())) {
		t.Error("the spec the hash is taken from differs from the one the argv is " +
			"rendered from; the two can drift again")
	}
}
