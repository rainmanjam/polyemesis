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
// THE PREVIOUS VERSION OF THIS TEST WAS THE DEFECT IT CLAIMED TO EXCLUDE. It
// said "every field of the command line moves the signature", mutated each
// field of ffmpeg.DestSpec, and then checked destArgvSig ALONE -- never that
// the field reached DestinationArgs. Against a signature taken over the whole
// struct that assertion cannot fail whatever the argv builder does, so it
// passed while demonstrating the opposite of its premise, and it certified as
// "on the command line" fields that are not:
//
//   - CopyVideo. DestinationArgs never reads it, for any kind; `-c:v copy` is
//     unconditional.
//   - Audio.Codec on RTMP. Opus is refused on a container that cannot carry it,
//     so choosing it dropped a live connection to the platform and respawned the
//     identical command line. That is B5 -- a restart for a change the child
//     cannot observe -- reintroduced through the unified hash.
//
// Those two are the ones the audit named. Running the rule found two more of
// exactly the same kind: Transport.NoDurationFilesize is an FLV flag and reaches
// the argv for rtmp alone, and VideoDelayMS cannot reach a DestAudio command
// because that kind carries no video at all. Ten field/kind pairs in total moved
// the old hash without moving the command line, which is why the fix is
// structural rather than a list of exclusions.
//
// So the rule asserted here is the real one and it runs in both directions: the
// signature moves if and ONLY if the command line moves. Both failures are
// worth naming. A field on the argv and not in the hash is stored and silently
// never applied, and the primary and the backup drift apart. A field in the hash
// and not on the argv drops a live stream to change nothing.
//
// Per DestKind, because reaching the argv is kind-dependent, and every one of
// the four instances above was invisible without that: Audio.Codec is on the
// command line for srt and file and not for rtmp or audio, and all four of those
// are correct.
//
// Mutation: restore destArgvSig's previous body, `return fmt.Sprintf("%#v", s)`
// -- the whole-struct hash this replaced. Observed to fail on ten field/kind
// pairs: CopyVideo on all four kinds, Audio.Codec on rtmp and audio,
// Transport.NoDurationFilesize on srt, file and audio, and VideoDelayMS on
// audio. Second mutation, for the other direction: replace the body with the
// hand-written subset the struct hash itself replaced,
// `return string(s.Kind) + s.Target + s.FilterComplex`. Observed to fail on 42
// field/kind pairs.
func TestTheSignatureMovesExactlyWhenTheCommandLineDoes(t *testing.T) {
	base := destSpecFor(testLogger(), backupRow(),
		routing.Result{FilterComplex: "anull", OutLabel: "a"},
		"udp://127.0.0.1:1", "rtmp://live.example/app/key")

	leaves := leafPaths(reflect.TypeOf(base), "")
	if len(leaves) < 10 {
		t.Fatalf("found %d leaf fields on ffmpeg.DestSpec, want at least 10; the "+
			"walker is broken, which would make this guard pass by finding nothing", len(leaves))
	}

	// A bumper that produced a value the builder normalises away would make
	// every case vacuously "does not reach the argv, does not move the hash".
	// Counted and asserted below.
	reached := 0
	for _, kind := range destKinds {
		for _, path := range leaves {
			t.Run(string(kind)+"/"+path, func(t *testing.T) {
				before := base
				before.Kind = kind
				after := before
				bumpLeaf(reflect.ValueOf(&after).Elem(), path, kind)

				onArgv := !equalArgs(ffmpeg.DestinationArgs(before), ffmpeg.DestinationArgs(after))
				inSig := destArgvSig(before) != destArgvSig(after)

				if path == "RelayURL" {
					// The one field that is on the command line and deliberately
					// out of the hash: it is the allocated port, new on every
					// start, so hashing it would make every destination's
					// signature differ from itself and nothing would ever be
					// left running.
					if inSig {
						t.Error("RelayURL moved the signature; every destination would " +
							"be restarted by every reconcile, for ever")
					}
					return
				}
				if onArgv {
					reached++
				}
				if onArgv && !inSig {
					t.Errorf("%s changes the %s command line and not the signature, so "+
						"changing it is stored and silently never applied -- and the "+
						"primary and the backup drift apart", path, kind)
				}
				if !onArgv && inSig {
					t.Errorf("%s does not change the %s command line and yet moves the "+
						"signature, so editing it drops a live connection to the platform "+
						"and respawns the identical command. That is B5", path, kind)
				}
			})
		}
	}
	if reached < 10 {
		t.Errorf("only %d field/kind combinations changed the command line at all; "+
			"the bumper is producing values the argv builder normalises away, which "+
			"would make this guard pass by comparing nothing", reached)
	}
}

// The two live instances the rewritten guard above was written for, spelled out
// so a reader meets them as behaviour rather than as a table entry.
//
// Mutation: restore destArgvSig's previous body, `return fmt.Sprintf("%#v", s)`.
// Observed to fail on both.
func TestAChangeTheCommandLineCannotSeeDoesNotRestartTheStream(t *testing.T) {
	compiled := routing.Result{FilterComplex: "anull", OutLabel: "a"}
	row := backupRow()

	// CopyVideo is documented as "always true in v1 and here to make the
	// guarantee explicit and testable". DestinationArgs does not read it at
	// all -- `-c:v copy` is unconditional -- and both construction sites
	// hardcode true, so the cost was latent. The class was not.
	spec := destSpecFor(testLogger(), row, compiled, "", row.Target())
	off := spec
	off.CopyVideo = false
	if !equalArgs(ffmpeg.DestinationArgs(spec), ffmpeg.DestinationArgs(off)) {
		t.Fatal("DestinationArgs now reads CopyVideo, so this test no longer describes " +
			"the code; the general guard above is the one to trust")
	}
	if destArgvSig(spec) != destArgvSig(off) {
		t.Error("CopyVideo moved the signature although the command line is identical")
	}

	// Opus on RTMP. FFmpeg cannot carry Opus in FLV, so the builder refuses it
	// and emits AAC -- the same command line either way. Restarting for it took
	// the destination off air to deliver nothing.
	aac := spec
	aac.Kind, aac.Audio.Codec = ffmpeg.DestRTMP, ""
	opus := aac
	opus.Audio.Codec = ffmpeg.AudioCodecOpus
	if !equalArgs(ffmpeg.DestinationArgs(aac), ffmpeg.DestinationArgs(opus)) {
		t.Fatal("RTMP now renders Opus differently; this case has changed")
	}
	if destArgvSig(aac) != destArgvSig(opus) {
		t.Error("choosing Opus on an RTMP destination moved the signature although " +
			"the command line is identical: a live stream dropped to change nothing")
	}

	// And the same choice on SRT, which CAN carry it, must still restart.
	srtAAC := aac
	srtAAC.Kind = ffmpeg.DestSRT
	srtOpus := srtAAC
	srtOpus.Audio.Codec = ffmpeg.AudioCodecOpus
	if equalArgs(ffmpeg.DestinationArgs(srtAAC), ffmpeg.DestinationArgs(srtOpus)) {
		t.Fatal("SRT no longer renders Opus differently; this case has changed")
	}
	if destArgvSig(srtAAC) == destArgvSig(srtOpus) {
		t.Error("choosing Opus on an SRT destination did not move the signature, so " +
			"the codec is stored and the destination goes on publishing AAC")
	}
}

// destKinds is every transport a destination can be, because whether a field
// reaches the command line depends on which one it is.
var destKinds = []ffmpeg.DestKind{
	ffmpeg.DestRTMP, ffmpeg.DestSRT, ffmpeg.DestFile, ffmpeg.DestAudio,
}

// bumpLeaf gives the named field a value distinct from the one it holds AND
// meaningful to the argv builder. setLeaf's generic "+7" and "-changed" are
// enough for most fields, but two of them are read as enumerations: an
// unrecognised codec falls back to AAC and an unrecognised kind renders as
// nothing in particular, so the generic bump would compare a spec against
// itself and prove nothing.
func bumpLeaf(v reflect.Value, path string, kind ffmpeg.DestKind) {
	switch path {
	case "Kind":
		next := ffmpeg.DestRTMP
		if kind == ffmpeg.DestRTMP {
			next = ffmpeg.DestSRT
		}
		v.FieldByName("Kind").SetString(string(next))
	case "Audio.Codec":
		v.FieldByName("Audio").FieldByName("Codec").SetString(ffmpeg.AudioCodecOpus)
	default:
		setLeaf(v, path)
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
		// Per element type, because DestSpec now carries both []string (the
		// expert arguments) and []int (AudioTracks). A single []string bump
		// panicked on the second one the moment it was added, which is the
		// walker doing its job: an unhandled field would otherwise be a field
		// this guard silently stopped covering.
		switch v.Type().Elem().Kind() {
		case reflect.Int:
			v.Set(reflect.ValueOf([]int{3}))
		default:
			v.Set(reflect.ValueOf([]string{"-changed"}))
		}
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
