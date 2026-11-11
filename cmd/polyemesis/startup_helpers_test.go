package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// The startup helpers in this file each decide one thing the operator depends
// on: which directory and binaries win over the config file, whether the
// interrupted-poll count reaches the log, whether a plain-HTTP deployment starts
// a :80 helper it must not, and which Listen call serves the API.

func TestApplyPathFlagsOverridesOnlyTheFlagsGiven(t *testing.T) {
	base := func() config.Config {
		return config.Config{DataDir: "/from/file", FFmpeg: config.FFmpeg{Binary: "/file/ffmpeg", Probe: "/file/ffprobe"}}
	}
	tests := []struct {
		name                        string
		dataDir, ffmpeg, ffprobe    string
		wantDir, wantBin, wantProbe string
	}{
		{name: "no flags leaves every file value alone",
			wantDir: "/from/file", wantBin: "/file/ffmpeg", wantProbe: "/file/ffprobe"},
		{name: "data dir flag replaces only the data dir",
			dataDir: "/from/flag", wantDir: "/from/flag", wantBin: "/file/ffmpeg", wantProbe: "/file/ffprobe"},
		{name: "ffmpeg flag replaces only the binary",
			ffmpeg: "/flag/ffmpeg", wantDir: "/from/file", wantBin: "/flag/ffmpeg", wantProbe: "/file/ffprobe"},
		{name: "ffprobe flag replaces only the probe",
			ffprobe: "/flag/ffprobe", wantDir: "/from/file", wantBin: "/file/ffmpeg", wantProbe: "/flag/ffprobe"},
		{name: "every flag replaces its value",
			dataDir: "/d", ffmpeg: "/b", ffprobe: "/p", wantDir: "/d", wantBin: "/b", wantProbe: "/p"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			applyPathFlags(&cfg, tc.dataDir, tc.ffmpeg, tc.ffprobe)
			if cfg.DataDir != tc.wantDir || cfg.FFmpeg.Binary != tc.wantBin || cfg.FFmpeg.Probe != tc.wantProbe {
				t.Errorf("got dir=%q bin=%q probe=%q; want dir=%q bin=%q probe=%q",
					cfg.DataDir, cfg.FFmpeg.Binary, cfg.FFmpeg.Probe, tc.wantDir, tc.wantBin, tc.wantProbe)
			}
		})
	}
}

func TestReportInterruptedPollsLogsTheCountOfPollsMarked(t *testing.T) {
	t.Run("an open poll left by a previous process is reported with its count", func(t *testing.T) {
		store := dbtest.Open(t)
		t0 := time.Unix(1_800_000_000, 0)
		if _, err := store.CreatePoll(db.Poll{Question: "q", Options: []string{"a", "b"}, DurationS: 60,
			State: "open", StartedAt: t0, EndsAt: t0.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		reportInterruptedPolls(slog.New(slog.NewTextHandler(&logs, nil)), store)
		out := logs.String()
		if !strings.Contains(out, "marked interrupted") || !strings.Contains(out, "count=1") {
			t.Errorf("log = %q; want the interrupted line with count=1", out)
		}
	})
	t.Run("no open polls logs nothing", func(t *testing.T) {
		store := dbtest.Open(t)
		var logs bytes.Buffer
		reportInterruptedPolls(slog.New(slog.NewTextHandler(&logs, nil)), store)
		if logs.Len() != 0 {
			t.Errorf("log = %q; want nothing when no poll was open", logs.String())
		}
	})
	t.Run("a store that cannot be written to warns instead of reporting a count", func(t *testing.T) {
		store := dbtest.Open(t)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		reportInterruptedPolls(slog.New(slog.NewTextHandler(&logs, nil)), store)
		out := logs.String()
		if !strings.Contains(out, "could not mark interrupted polls") || strings.Contains(out, "count=") {
			t.Errorf("log = %q; want the warning and no count line", out)
		}
	})
}

func TestStartRedirectHelperIsNilWithoutOwnTLS(t *testing.T) {
	provider, err := tlsx.New(tlsx.Options{Mode: tlsx.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Enabled() {
		t.Fatal("precondition: ModeOff must not terminate TLS")
	}
	// srv is nil on purpose: a helper that touched the server before checking
	// whether it is needed would panic here instead of returning nil.
	var logs bytes.Buffer
	got := startRedirectHelper(slog.New(slog.NewTextHandler(&logs, nil)), config.Config{}, provider, nil)
	if got != nil {
		t.Errorf("startRedirectHelper = %v; want nil when a proxy owns TLS and port 80", got)
	}
	if logs.Len() != 0 {
		t.Errorf("log = %q; want nothing when no helper is started", logs.String())
	}
}

func TestListenAndServePlainWhenTLSIsNotOurs(t *testing.T) {
	provider, err := tlsx.New(tlsx.Options{Mode: tlsx.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	// An invalid port fails in net.Listen before anything is bound, so no
	// listener is left running and the error is the plain ListenAndServe one.
	srv := &http.Server{Addr: "127.0.0.1:-1"}
	err = listenAndServe(srv, provider)
	if err == nil {
		t.Fatal("listenAndServe on an invalid port returned nil")
	}
	if errors.Is(err, http.ErrServerClosed) {
		t.Errorf("err = %v; a listen failure must not look like a clean shutdown", err)
	}
}
