package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeVersionOnlyFFmpeg writes a stub ffmpeg (and an ffprobe beside it) that prints banner
// on -version and nothing on anything else, and returns its path.
//
// A SHELL STUB, because the branch under test reads a real process's output and
// the only way to control that output is to be the process. The probes Detect
// runs afterwards (-protocols, -encoders, -filters) all fail silently by
// design, so a stub that prints nothing for them exercises the real path.
func fakeVersionOnlyFFmpeg(t *testing.T, banner string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a /bin/sh script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"-version\" ]; then\n    printf '%s\\n' " +
		"\"" + banner + "\"\n    exit 0\n  fi\ndone\nexit 1\n"
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "ffmpeg")
}

// TestAnUnreadableVersionIsRefusedAndNamed is the finding. The floor read
// "if t.Major > 0 && t.Major < MinMajorVersion" and then, for Major == 0,
// appended "(unrecognised version string; assuming >= 6.0)" to the version and
// carried on. So the check only refused versions it could already parse: a
// binary whose version string it could not read was admitted on a guess, and
// the guess is wrong exactly when it matters -- a hand-built 4.x or 5.x, which
// accepts every argument this package writes and then fails hours later, on
// air, on multi-track mapping.
func TestAnUnreadableVersionIsRefusedAndNamed(t *testing.T) {
	t.Setenv(AssumeMajorEnv, "")
	const banner = "ffmpeg version SOMEVENDOR-BUILD Copyright (c) 2000-2019 the FFmpeg developers"
	bin := fakeVersionOnlyFFmpeg(t, banner)

	tools, err := Detect(context.Background(), bin, filepath.Join(filepath.Dir(bin), "ffprobe"))
	if err == nil {
		t.Fatalf("Detect admitted a build of unknown version: %+v", tools)
	}
	// NAMING THE STRING IS HALF THE DEVICE. "cannot read the version" sends an
	// operator to the wrong place; the string they can go and check themselves
	// sends them to the right one.
	if !strings.Contains(err.Error(), "SOMEVENDOR-BUILD") {
		t.Errorf("the refusal does not name the string it could not parse: %v", err)
	}
	if !strings.Contains(err.Error(), AssumeMajorEnv) {
		t.Errorf("the refusal does not name the way out: %v", err)
	}
}

// A version string with no "ffmpeg version" line at all still has to name
// something an operator can look at, rather than quoting the empty string.
func TestAnUnrecognisableBannerIsQuotedAsWhatWasActuallyPrinted(t *testing.T) {
	t.Setenv(AssumeMajorEnv, "")
	bin := fakeVersionOnlyFFmpeg(t, "this is not an ffmpeg banner at all")
	_, err := Detect(context.Background(), bin, filepath.Join(filepath.Dir(bin), "ffprobe"))
	if err == nil {
		t.Fatal("Detect admitted a binary that printed no version line")
	}
	if !strings.Contains(err.Error(), "this is not an ffmpeg banner at all") {
		t.Errorf("the refusal quotes nothing useful: %v", err)
	}
}

// The escape hatch exists because refusing to start is the one failure an
// operator cannot fix from inside the product, and a master nightly really is
// newer than the floor.
func TestTheOperatorCanStateTheVersionThemselves(t *testing.T) {
	t.Setenv(AssumeMajorEnv, "8")
	bin := fakeVersionOnlyFFmpeg(t, "ffmpeg version N-113518-gd6a4b1e Copyright (c) 2000-2026 the FFmpeg developers")
	tools, err := Detect(context.Background(), bin, filepath.Join(filepath.Dir(bin), "ffprobe"))
	if err != nil {
		t.Fatalf("Detect refused a build the operator vouched for: %v", err)
	}
	if tools.Major != 8 {
		t.Errorf("Major = %d, want 8", tools.Major)
	}
	// The version string must still say the number was asserted, not measured.
	if !strings.Contains(tools.Version, AssumeMajorEnv) {
		t.Errorf("Version = %q, which reads as if the version was detected", tools.Version)
	}
}

// It is a claim, not an override: it cannot be used to get under the floor.
func TestTheEscapeHatchCannotBeUsedToRunAnOldBuild(t *testing.T) {
	t.Setenv(AssumeMajorEnv, "4")
	bin := fakeVersionOnlyFFmpeg(t, "ffmpeg version SOMEVENDOR-BUILD")
	if _, err := Detect(context.Background(), bin, filepath.Join(filepath.Dir(bin), "ffprobe")); err == nil {
		t.Fatal("POLYEMESIS_FFMPEG_ASSUME_MAJOR=4 got past a floor of 6")
	}
}

// A typo must not read as "unset" and drop the operator back at the original
// refusal with no idea why their answer was ignored.
func TestATypoInTheEscapeHatchSaysSo(t *testing.T) {
	t.Setenv(AssumeMajorEnv, "7.1")
	bin := fakeVersionOnlyFFmpeg(t, "ffmpeg version SOMEVENDOR-BUILD")
	_, err := Detect(context.Background(), bin, filepath.Join(filepath.Dir(bin), "ffprobe"))
	if err == nil {
		t.Fatal("a malformed value was accepted")
	}
	if !strings.Contains(err.Error(), "7.1") {
		t.Errorf("the error does not quote what was set: %v", err)
	}
}

// The parseable cases are unchanged, and pinned so the fail-closed branch
// cannot quietly grow to cover them.
func TestAReadableVersionStillDecidesOnItsOwnNumber(t *testing.T) {
	t.Setenv(AssumeMajorEnv, "")
	old := fakeVersionOnlyFFmpeg(t, "ffmpeg version 4.4.2-0ubuntu0.22.04.1 Copyright (c) 2000-2021")
	if _, err := Detect(context.Background(), old, filepath.Join(filepath.Dir(old), "ffprobe")); err == nil {
		t.Error("FFmpeg 4.4 was admitted")
	}
	ok := fakeVersionOnlyFFmpeg(t, "ffmpeg version 6.1.1-3ubuntu5 Copyright (c) 2000-2023")
	tools, err := Detect(context.Background(), ok, filepath.Join(filepath.Dir(ok), "ffprobe"))
	if err != nil {
		t.Fatalf("FFmpeg 6.1.1 was refused: %v", err)
	}
	if tools.Major != 6 || tools.Minor != 1 {
		t.Errorf("version = %d.%d, want 6.1", tools.Major, tools.Minor)
	}
}
