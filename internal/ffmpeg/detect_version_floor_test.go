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
// appended "(unrecognised version string; assuming >= 6.0)" -- the floor then -- to the version and
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
	if strings.Contains(err.Error(), "apt install ffmpeg") {
		t.Errorf("the refusal recommends apt, which is where 6.1.1 and 7.1.5 come from: %v", err)
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
//
// AT THE BOUNDARY, not far below it. The case used to be "4", which a floor of
// 6 refused and a floor of 7 or 8 refuses too -- so it could not tell which
// floor was in force. 7 is the newest major the floor exists to keep out, and
// it is the one an operator on Debian 13's 7.1.5 would reach for.
func TestTheEscapeHatchCannotBeUsedToRunAnOldBuild(t *testing.T) {
	t.Setenv(AssumeMajorEnv, "7")
	bin := fakeVersionOnlyFFmpeg(t, "ffmpeg version SOMEVENDOR-BUILD")
	_, err := Detect(context.Background(), bin, filepath.Join(filepath.Dir(bin), "ffprobe"))
	if err == nil {
		t.Fatal("POLYEMESIS_FFMPEG_ASSUME_MAJOR=7 got past a floor of 8")
	}
	// The refusal has to say what to do, not only that the answer was wrong.
	if !strings.Contains(err.Error(), "8.0 or newer") {
		t.Errorf("the refusal does not name the floor: %v", err)
	}
}

// And the floor itself is admitted through the same hatch: 8 is a claim the
// operator is allowed to make.
func TestTheEscapeHatchAdmitsAClaimOfExactlyTheFloor(t *testing.T) {
	t.Setenv(AssumeMajorEnv, "8")
	bin := fakeVersionOnlyFFmpeg(t, "ffmpeg version SOMEVENDOR-BUILD")
	tools, err := Detect(context.Background(), bin, filepath.Join(filepath.Dir(bin), "ffprobe"))
	if err != nil {
		t.Fatalf("POLYEMESIS_FFMPEG_ASSUME_MAJOR=8 was refused: %v", err)
	}
	if tools.Major != MinMajorVersion {
		t.Errorf("Major = %d, want %d", tools.Major, MinMajorVersion)
	}
}

// A typo must not read as "unset" and drop the operator back at the original
// refusal with no idea why their answer was ignored.
func TestATypoInTheEscapeHatchSaysSo(t *testing.T) {
	t.Setenv(AssumeMajorEnv, "8.1")
	bin := fakeVersionOnlyFFmpeg(t, "ffmpeg version SOMEVENDOR-BUILD")
	_, err := Detect(context.Background(), bin, filepath.Join(filepath.Dir(bin), "ffprobe"))
	if err == nil {
		t.Fatal("a malformed value was accepted")
	}
	if !strings.Contains(err.Error(), "8.1") {
		t.Errorf("the error does not quote what was set: %v", err)
	}
}

// The parseable cases decide on their own number, and are pinned AT THE
// BOUNDARY so the fail-closed branch cannot quietly grow to cover them and the
// floor cannot drift without a test noticing.
//
// The refused builds are the ones operators really have: Ubuntu 24.04's stock
// 6.1.1 and Debian 13's 7.1.5. Both cleared the old 6.0 floor and neither can
// carry Enhanced RTMP multitrack (two-track FLV), which is why the floor is 8.
func TestAReadableVersionStillDecidesOnItsOwnNumber(t *testing.T) {
	t.Setenv(AssumeMajorEnv, "")
	for _, banner := range []string{
		"ffmpeg version 4.4.2-0ubuntu0.22.04.1 Copyright (c) 2000-2021",
		"ffmpeg version 6.1.1-3ubuntu5 Copyright (c) 2000-2023",
		"ffmpeg version 7.1.5-0+deb13u1 Copyright (c) 2000-2025",
		"ffmpeg version n7.1.3 Copyright (c) 2000-2025",
	} {
		bin := fakeVersionOnlyFFmpeg(t, banner)
		_, err := Detect(context.Background(), bin, filepath.Join(filepath.Dir(bin), "ffprobe"))
		if err == nil {
			t.Errorf("%q was admitted below the 8.0 floor", banner)
			continue
		}
		// THE REFUSAL IS THE OPERATOR'S ONLY INSTRUCTIONS. It has to name the
		// floor and every way past it, because this is read at a terminal by
		// someone whose distribution just handed them the build being refused.
		for _, want := range []string{"8.0 or newer", "install.sh", "Docker"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: the refusal does not mention %q: %v", banner, want, err)
			}
		}
		// And it must not send them back to the package manager that gave them
		// the old build in the first place.
		if strings.Contains(err.Error(), "apt install ffmpeg") {
			t.Errorf("%q: the refusal recommends apt, which is where 6.1.1 and 7.1.5 come from: %v", banner, err)
		}
	}

	for _, c := range []struct {
		banner       string
		major, minor int
	}{
		{"ffmpeg version 8.0.1-3ubuntu2 Copyright (c) 2000-2025", 8, 0},
		{"ffmpeg version n8.1.3-9-g29e619e767-20260930 Copyright (c) 2000-2026", 8, 1},
		{"ffmpeg version 9.0.2 Copyright (c) 2000-2026", 9, 0},
	} {
		bin := fakeVersionOnlyFFmpeg(t, c.banner)
		tools, err := Detect(context.Background(), bin, filepath.Join(filepath.Dir(bin), "ffprobe"))
		if err != nil {
			t.Errorf("%q was refused: %v", c.banner, err)
			continue
		}
		if tools.Major != c.major || tools.Minor != c.minor {
			t.Errorf("%q: version = %d.%d, want %d.%d", c.banner, tools.Major, tools.Minor, c.major, c.minor)
		}
	}
}
