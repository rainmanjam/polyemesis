package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// FFmpeg 9.0 and 9.0.1 are named at startup, in the banner AND in the log.
//
// The banner alone is not enough: a service install runs under systemd, where
// nobody reads stdout and journald holds only what went through the logger. So
// the case checks both channels, on a 9.0.1 install, and then the negative
// control on 9.0.2 -- the release that reverted the scheduler change -- where a
// warning would be noise every boot and teach operators to ignore the banner.
// internal/ffmpeg's TestTheRevertedSchedulerReleasesAreNamedAtStartup owns the
// version matrix; this owns the wiring into reportStartup.
func TestTheBannerAndTheLogNameAnFFmpegReleaseWithTheRevertedSchedulerChange(t *testing.T) {
	provider, err := tlsx.New(tlsx.Options{Mode: tlsx.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	cfg, store, tools := bannerFixture(t)
	cfg.Addr = "127.0.0.1:8080"
	tools.FFmpeg = "/usr/bin/ffmpeg"

	boot := func(version string) (banner, logs string) {
		t.Helper()
		tools.Version = version
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
		banner = captureStdout(t, func() {
			if err := reportStartup(log, cfg, provider, store, tools); err != nil {
				t.Errorf("reportStartup: %v", err)
			}
		})
		return banner, buf.String()
	}

	banner, logs := boot("9.0.1")
	if !strings.Contains(banner, "WARNING: /usr/bin/ffmpeg is FFmpeg 9.0.1") || !strings.Contains(banner, "9d4ca2122") {
		t.Errorf("the banner on FFmpeg 9.0.1 does not name the release and the revert:\n%s", banner)
	}
	if !strings.Contains(logs, "ffmpeg release with a known regression") || !strings.Contains(logs, "9.0.1") {
		t.Errorf("FFmpeg 9.0.1 was not logged, so a service install never sees it in journald:\n%s", logs)
	}

	banner, logs = boot("9.0.2")
	if strings.Contains(banner, "9d4ca2122") || strings.Contains(logs, "known regression") {
		t.Errorf("FFmpeg 9.0.2 carries the revert and must not be warned about.\nbanner:\n%s\nlog:\n%s", banner, logs)
	}
}
