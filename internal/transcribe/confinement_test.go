package transcribe

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

/* THE CONFINEMENT CHECK DID NOT CONFINE.
 *
 * resolveRecording refuses a name with a separator or a "..", joins what is
 * left to the recordings directory, and then tests that the result is inside
 * that directory. That test is the whole confinement -- and it was performed on
 * filepath.Abs, WHICH DOES NOT FOLLOW SYMLINKS.
 *
 * So a symlink sitting in the recordings directory passes every check: its name
 * is a bare filename, it contains no "..", and its absolute path is inside the
 * directory. os.Stat then follows the link to decide it exists and is not a
 * directory, and the transcription job that follows reads through it to
 * whatever it points at. The function returns a path it has certified as
 * confined, for a file that is not.
 */
func TestARecordingSymlinkCannotEscapeTheRecordingsDirectory(t *testing.T) {
	h := newHarness(t, stubWhisperBody)

	outside := filepath.Join(t.TempDir(), "outside.mkv")
	if err := os.WriteFile(outside, []byte("not really an mkv"), 0o600); err != nil {
		t.Fatalf("seed the file to escape to: %v", err)
	}
	symlinkOrSkip(t, outside, filepath.Join(h.recordingsDir, "smuggled.mkv"))

	got, err := h.proc.resolveRecording("smuggled.mkv")
	if err == nil {
		t.Fatalf("resolveRecording certified %q as inside the recordings "+
			"directory, but it is a symlink to %q. filepath.Abs does not follow "+
			"links, so the one check whose job is confinement never saw the "+
			"target.", got, outside)
	}
	if !strings.Contains(err.Error(), "outside the recordings directory") {
		t.Errorf("refused with %q, which does not tell the operator the "+
			"recording left the directory", err)
	}
}

// An ordinary recording must still resolve — including when the recordings
// directory is ITSELF a symlink, which is the usual way an install puts
// recordings on a separate disk.
func TestAnOrdinaryRecordingStillResolvesThroughASymlinkedDirectory(t *testing.T) {
	h := newHarness(t, stubWhisperBody)

	real := filepath.Join(h.recordingsDir, "rec-20260101-120000.mkv")
	if err := os.WriteFile(real, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := h.proc.resolveRecording("rec-20260101-120000.mkv"); err != nil {
		t.Fatalf("an ordinary recording was refused: %v", err)
	}

	// Now reach the same directory through a symlinked root.
	link := filepath.Join(t.TempDir(), "recordings")
	symlinkOrSkip(t, h.recordingsDir, link)
	p := *h.proc
	p.recordingsDir = link
	if _, err := p.resolveRecording("rec-20260101-120000.mkv"); err != nil {
		t.Errorf("a recording under a symlinked recordings directory was refused: "+
			"%v — resolving the file but not the root compares a real path "+
			"against a link and rejects everything", err)
	}
}

// symlinkOrSkip makes a link, or skips because this machine will not.
//
// ONE site for the whole file rather than one per test, which is the shape
// skips.json asks for: a Windows account without the create-symlink privilege
// cannot run either of these, and that is a property of the machine, not of the
// code under test. Anything other than a permission refusal is a real failure
// and is reported as one.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, os.ErrPermission) || runtime.GOOS == "windows" {
			t.Skipf("this machine will not create symlinks: %v", err)
		}
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}
