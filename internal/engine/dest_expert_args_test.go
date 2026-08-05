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
// The argv is compared as well as the hash, so the test cannot pass by both
// sides being wrong in the same direction.
//
// Mutation: in expertArgvSig, replace the body with `return raw`. Observed to
// fail on both the primary's and the backup's hash for every reformatting.
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
				t.Fatalf("the two command lines are same=%v, which contradicts the case; "+
					"the fixture is wrong, not the code", sameArgv)
			}

			sameSpec := destSpec(before, compiled, "up") == destSpec(after, compiled, "up")
			sameBackup := backupSpecOf(before, compiled, "up") == backupSpecOf(after, compiled, "up")
			if sameSpec == tc.wantSig {
				t.Errorf("destSpec same=%v for command lines that are same=%v: the hash "+
					"disagrees with the argv it exists to predict", sameSpec, sameArgv)
			}
			if sameBackup == tc.wantSig {
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
// Mutation: in expertArgvSig, replace the body with `return raw`. Observed to
// fail.
func TestUnparseableExpertArgumentsDoNotMoveTheHash(t *testing.T) {
	compiled := routing.Result{FilterComplex: "anull", OutLabel: "a"}
	unterminated := `-metadata title="never closed`
	if _, err := splitOK(unterminated); err == nil {
		t.Fatalf("%q parses; this case no longer tests what it says", unterminated)
	}

	clean := backupRow()
	broken := backupRow()
	broken.ExtraInputArgs = unterminated

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
