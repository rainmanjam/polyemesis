// Package ffmpeg detects the system FFmpeg and builds every command line
// polyemesis spawns.
//
// The builders are pure functions from a spec struct to an argument slice.
// Nothing here runs a process — that is the supervisor's job — which is what
// lets the command lines be unit-tested exhaustively.
package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MinMajorVersion is the oldest FFmpeg we accept. 6.0 is the floor because
// polyemesis relies on behaviour that is only reliable from 6.x onwards:
// stable multi-track MPEG-TS mapping, the modern channel-layout API used by
// pan/amerge, and `-progress` output that includes the fields we parse.
const MinMajorVersion = 6

// Tools is a validated pair of binaries.
type Tools struct {
	FFmpeg     string `json:"ffmpeg"`
	FFprobe    string `json:"ffprobe"`
	Version    string `json:"version"`
	Major      int    `json:"major"`
	Minor      int    `json:"minor"`
	HasLibSRT  bool   `json:"hasLibsrt"`
	HasLibX264 bool   `json:"hasLibx264"`
}

// ErrNotFound signals a missing binary, as opposed to an unusable one.
var ErrNotFound = errors.New("ffmpeg not found")

var versionRe = regexp.MustCompile(`ffmpeg version (\S+)`)
var numRe = regexp.MustCompile(`^n?(\d+)\.(\d+)`)

// Detect locates ffmpeg and ffprobe and verifies they are new enough.
//
// ffmpegPath/ffprobePath may be empty, in which case $PATH is searched.
// Failure messages name the actual problem and the actual fix, because this
// is the first thing a new user hits and a vague error here costs them an
// afternoon.
func Detect(ctx context.Context, ffmpegPath, ffprobePath string) (*Tools, error) {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}

	binFFmpeg, err := exec.LookPath(ffmpegPath)
	if err != nil {
		return nil, fmt.Errorf("%w: could not find %q on PATH. Install FFmpeg %d.0 or newer "+
			"(macOS: brew install ffmpeg; Debian/Ubuntu: apt install ffmpeg), "+
			"or set ffmpeg.binary in config.yaml to its full path",
			ErrNotFound, ffmpegPath, MinMajorVersion)
	}
	binFFprobe, err := exec.LookPath(ffprobePath)
	if err != nil {
		return nil, fmt.Errorf("%w: found ffmpeg at %s but could not find %q on PATH. "+
			"ffprobe ships with FFmpeg; if you built it yourself, make sure ffprobe was installed too, "+
			"or set ffmpeg.probe in config.yaml",
			ErrNotFound, binFFmpeg, ffprobePath)
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, binFFmpeg, "-hide_banner", "-version").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("running %s -version failed: %w\n%s", binFFmpeg, err, truncate(string(out), 400))
	}
	text := string(out)

	t := &Tools{FFmpeg: binFFmpeg, FFprobe: binFFprobe}
	if m := versionRe.FindStringSubmatch(text); m != nil {
		t.Version = m[1]
	}
	// Distro builds report things like "6.1.1-3ubuntu5" and git builds report
	// "N-113518-gd6a4b1e" or "n7.0". Only the leading numeric pair matters,
	// and a build we cannot parse is not automatically a build we should
	// refuse — see below.
	if m := numRe.FindStringSubmatch(t.Version); m != nil {
		t.Major, _ = strconv.Atoi(m[1])
		t.Minor, _ = strconv.Atoi(m[2])
	}

	t.HasLibSRT = strings.Contains(text, "--enable-libsrt")
	t.HasLibX264 = strings.Contains(text, "--enable-libx264")

	if t.Major > 0 && t.Major < MinMajorVersion {
		return nil, fmt.Errorf(
			"%s is FFmpeg %s, but polyemesis requires %d.0 or newer. "+
				"Multi-track MPEG-TS routing is not reliable on older builds. "+
				"Upgrade FFmpeg, or point ffmpeg.binary in config.yaml at a newer build",
			binFFmpeg, t.Version, MinMajorVersion)
	}
	if t.Major == 0 {
		// An unrecognised version string is almost always a git/nightly build,
		// which is newer than 6.0, not older. Refusing to start would be the
		// wrong call; the protocol probe below is the real capability check.
		t.Version = strings.TrimSpace(t.Version) + " (unrecognised version string; assuming >= 6.0)"
	}

	if err := t.checkSRT(ctx); err != nil {
		return t, err
	}
	return t, nil
}

// checkSRT confirms the SRT protocol is actually compiled in. The configure
// line is a hint; asking FFmpeg for its protocol list is the truth, and SRT is
// the primary ingest so a missing one must fail loudly at startup rather than
// at first stream.
func (t *Tools) checkSRT(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, t.FFmpeg, "-hide_banner", "-protocols").CombinedOutput()
	if err != nil {
		return nil // non-fatal: some builds restrict -protocols
	}
	if strings.Contains(string(out), "srt") {
		t.HasLibSRT = true
		return nil
	}
	t.HasLibSRT = false
	return fmt.Errorf(
		"%s (FFmpeg %s) was built without SRT support, which polyemesis uses for multi-track ingest. "+
			"Install a build with --enable-libsrt (macOS: brew install ffmpeg; "+
			"Debian/Ubuntu: apt install ffmpeg from a recent release), "+
			"or switch the ingest to RTMP in Settings once the server is running",
		t.FFmpeg, t.Version)
}

// ProbeVersion is a small helper for the /api/v1/system endpoint.
func (t *Tools) String() string {
	return fmt.Sprintf("ffmpeg %s (%s)", t.Version, t.FFmpeg)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// scanLines is a small shared helper for reading process output line by line.
func scanLines(r interface{ Read([]byte) (int, error) }, fn func(string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
	return sc.Err()
}
