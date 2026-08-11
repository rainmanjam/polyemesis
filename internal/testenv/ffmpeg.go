package testenv

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// RequireFFmpegEnv names the variable that turns "this machine has no ffprobe"
// from an environment fact into a defect.
//
// #187, recurring defect #2: a suite that passes for the wrong reason. Every
// test that proves the upload probe gate works begins by looking ffprobe up on
// PATH and skipping if it is not there. That is right on a laptop -- the gate is
// not a property of the code when there is nothing to gate with -- and wrong in
// CI, where .github/workflows/ci.yml installs ffmpeg and ffprobe on every job
// and NOTHING asserted that the tests then ran. Delete the install step during a
// speed-up or a runner-image change and the whole upload gate goes unverified
// while the package still prints ok.
//
// So CI sets this, and in CI the absence is a failure that names the binary.
const RequireFFmpegEnv = "POLYEMESIS_REQUIRE_FFMPEG"

// FFmpegRequired reports whether a missing FFmpeg-family binary must fail rather
// than skip.
//
// Anything Go's ParseBool accepts counts, plus the empty string counting as
// false, so that `POLYEMESIS_REQUIRE_FFMPEG=` in a shell profile does not
// quietly arm it. A value that is set but unparseable counts as REQUIRED: the
// only reason to type this variable at all is to demand the binaries, and the
// fail-closed direction for a gate is on.
func FFmpegRequired() bool {
	v, ok := os.LookupEnv(RequireFFmpegEnv)
	if !ok || strings.TrimSpace(v) == "" {
		return false
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return true
	}
	return b
}

// FFmpegBinary resolves an FFmpeg-family binary on PATH and decides, in the one
// place, whether its absence is an environment fact or a defect.
//
// skipWhy is the message used when it is an environment fact; it should say what
// the reader loses, not merely that something is missing. When
// RequireFFmpegEnv is armed there is no skip and the failure NAMES the binary,
// because "ffprobe" is the thing an operator has to go and install and a message
// that says "a dependency is missing" sends them to read the test.
//
// Capability skips are deliberately NOT routed through here. An ffmpeg that
// cannot mux h264/aac is a build difference and still an environment fact even
// in CI; only the binary's outright absence is a defect this can speak to.
func FFmpegBinary(t testing.TB, name, skipWhy string) string {
	t.Helper()
	bin, err := exec.LookPath(name)
	if err == nil {
		return bin
	}
	if FFmpegRequired() {
		t.Fatalf("%s is not on PATH: %v\n"+
			"%s=%q is set, which means this environment has undertaken to provide "+
			"%s and has not. This test is not skipped here on purpose -- a skip "+
			"would print ok and leave the behaviour it names unverified, which is "+
			"the whole of #187. Install %s, or unset %s if you are not CI.",
			name, err, RequireFFmpegEnv, os.Getenv(RequireFFmpegEnv), name, name, RequireFFmpegEnv)
	}
	t.Skip(skipWhy)
	return ""
}
