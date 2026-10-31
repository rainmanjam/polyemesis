package ffmpeg

import (
	"strings"
	"testing"
)

// FFmpeg 9.0 and 9.0.1 shipped "fftools/ffmpeg_sched: allow throttling decoder
// outputs" (03dfac5630), and 9.0.2 reverted it (9d4ca2122) because it misorders
// output, ends streams early and runs memory up. polyemesis's destination is
// exactly the shape that hits a scheduler: one input, several audio decoders,
// one filtergraph. Both releases clear the 6.0 floor, so without this nothing
// tells an operator on one of them why a destination drops out.
//
// A warning and not a refusal: a refusal takes down an install that may be
// working, over a regression that depends on the input.
func TestTheRevertedSchedulerReleasesAreNamedAtStartup(t *testing.T) {
	warned := []string{
		"9.0",                            // the release tarball, Homebrew at the time
		"9.0.1",                          // Homebrew, Arch
		"n9.0.1",                         // a build of the tag
		"7:9.0.1-1",                      // a Debian epoch:version-revision
		"9.0.1-1",                        // a distro revision
		"n9.0-3-gdeadbeef12",             // a release-branch snapshot after 9.0
		"n9.0.1-11-ge47273f4d9-20260831", // BtbN's dated build of the branch
	}
	for _, v := range warned {
		tl := &Tools{FFmpeg: "/usr/bin/ffmpeg", Version: v}
		got := tl.KnownRegressionWarning()
		if got == "" {
			t.Errorf("version %q: no warning; 9.0.0 and 9.0.1 carry the scheduler regression", v)
			continue
		}
		for _, want := range []string{v, "9.0.2", "9d4ca2122"} {
			if !strings.Contains(got, want) {
				t.Errorf("version %q: the warning does not mention %q:\n%s", v, want, got)
			}
		}
	}

	clean := []string{
		"9.0.2", "n9.0.2", "n9.0.2-17-g2a571b6068-20260930", "9.0.3", "9.0.10",
		"9.1", "10.0", "8.1.2", "n8.1.2-34-g9b6c8969e0-20260806",
		"6.1.1-3ubuntu5", "N-121000-gabcdef0123", "",
	}
	for _, v := range clean {
		tl := &Tools{FFmpeg: "/usr/bin/ffmpeg", Version: v}
		if got := tl.KnownRegressionWarning(); got != "" {
			t.Errorf("version %q: warned, but it does not carry the regression:\n%s", v, got)
		}
	}
}
