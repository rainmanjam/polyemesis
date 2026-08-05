package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// B5. destSpec hashed ExtraInputArgs/ExtraOutputArgs as the raw operator-typed
// strings while destArgs put them through expertArgv -> ffmpeg.SplitArgs.
// Reformatting the whitespace, or re-saving a value SplitArgs refuses and
// expertArgv therefore drops, changed the hash and dropped a live platform
// connection to deliver a byte-identical command line.
//
// Two separate claims, because they can fail separately. First: reformatting
// must not change the COMMAND LINE, which is a property of ffmpeg.SplitArgs.
// Second: the hash must agree with the command line, which is the property that
// was broken -- the hash said "restart" about an argv that had not moved.
//
// Mutation: in destSpecFor, change
// `ExtraInputArgs: expertArgv(log, row, row.ExtraInputArgs, "input")` to
// `ExtraInputArgs: []string{row.ExtraInputArgs}`, which is the pre-parse text
// the hashes used to carry. Observed to fail: every reformatting case reports a
// changed command line.
func TestReformattingAnExpertArgumentDoesNotRestartTheDestination(t *testing.T) {
	compiled := routing.Result{FilterComplex: "anull", OutLabel: "a"}
	e := &Engine{log: testLogger()}

	for _, tc := range []struct {
		name    string
		before  string
		after   string
		wantSig bool // true when the two SHOULD hash differently
	}{
		{"extra spaces between arguments", "-probesize 5M", "-probesize   5M", false},
		{"leading and trailing whitespace", "-probesize 5M", "  -probesize 5M  ", false},
		{"a tab instead of a space", "-probesize 5M", "-probesize\t5M", false},
		{"quoting that parses to the same argv", `-metadata title=x`, `-metadata "title=x"`, false},
		// The other direction, so this is not a test that everything is equal.
		{"a genuinely different value", "-probesize 5M", "-probesize 9M", true},
		{"an argument added", "-probesize 5M", "-probesize 5M -fflags +genpts", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after := backupRow(), backupRow()
			before.ExtraInputArgs, after.ExtraInputArgs = tc.before, tc.after

			sameArgv := equalArgs(
				e.destArgs(before, compiled, "udp://127.0.0.1:1", before.Target()),
				e.destArgs(after, compiled, "udp://127.0.0.1:1", after.Target()))
			if sameArgv == tc.wantSig {
				t.Errorf("the command line changed=%v for %q -> %q, want changed=%v",
					!sameArgv, tc.before, tc.after, tc.wantSig)
			}

			sameSpec := destSpec(before, compiled, "up") == destSpec(after, compiled, "up")
			sameBackup := backupSpecOf(before, compiled, "up") == backupSpecOf(after, compiled, "up")
			if sameSpec != sameArgv {
				t.Errorf("destSpec same=%v for command lines that are same=%v: the hash "+
					"disagrees with the argv it exists to predict, so the destination is "+
					"torn down and rebuilt to deliver the command it is already running",
					sameSpec, sameArgv)
			}
			if sameBackup != sameArgv {
				t.Errorf("backupSpecOf same=%v for command lines that are same=%v",
					sameBackup, sameArgv)
			}
		})
	}
}

// Text the parser refuses never reaches the command line -- expertArgv drops it
// and starts the destination on its generated command. So re-saving it must not
// move the hash either, or the operator's stream is dropped to deliver a change
// that was never applied.
//
// Mutation: in destSpecFor, change
// `ExtraInputArgs: expertArgv(log, row, row.ExtraInputArgs, "input")` to
// `ExtraInputArgs: []string{row.ExtraInputArgs}`. Observed to fail on both
// hashes and on the command line.
func TestUnparseableExpertArgumentsDoNotMoveTheHash(t *testing.T) {
	compiled := routing.Result{FilterComplex: "anull", OutLabel: "a"}
	unterminated := `-metadata title="never closed`
	if _, err := splitOK(unterminated); err == nil {
		t.Fatalf("%q parses; this case no longer tests what it says", unterminated)
	}

	clean := backupRow()
	broken := backupRow()
	broken.ExtraInputArgs = unterminated

	e := &Engine{log: testLogger()}
	if !equalArgs(
		e.destArgs(clean, compiled, "udp://127.0.0.1:1", clean.Target()),
		e.destArgs(broken, compiled, "udp://127.0.0.1:1", broken.Target())) {
		t.Error("text the parser refuses reached the command line")
	}

	if destSpec(clean, compiled, "up") != destSpec(broken, compiled, "up") {
		t.Error("storing unparseable expert arguments restarted the primary to deliver " +
			"a command line that does not contain them")
	}
	if backupSpecOf(clean, compiled, "up") != backupSpecOf(broken, compiled, "up") {
		t.Error("storing unparseable expert arguments restarted the backup for the same nothing")
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// splitOK exists so the unparseable fixture is checked against the real parser
// rather than against a belief about it.
func splitOK(raw string) ([]string, error) { return ffmpeg.SplitArgs(raw) }
