package engine

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// "STARTED" USED TO MEAN "SPAWNED" AND READ AS "PUBLISHING".
//
// e.log.Info("destination started", ...) sat on the instruction after
// proc.Start(), which reports only that a child process exists. FFmpeg fails
// after that all the time -- "Error initializing filters!", "Could not open
// encoder before EOF", "Nothing was written into output file" -- and each of
// those left that line standing as the last word on the destination's health.
//
// It cost a full investigation: five passes of debugging aimed at the receiving
// end, because the sending end was reporting success. On a real install it is
// worse -- an operator sees a started destination, a clean console and no
// error, while the platform gets nothing. #675.
//
// So there are now two claims, and they say different things. This pins the
// second one: "publishing" is emitted from real progress, once per run.

func newMediaLogger(t *testing.T) (*Engine, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	// A buffer the engine's supervisor goroutines also write to would make any
	// assertion here flaky; this Engine spawns nothing, so the only writer is
	// the handler under test.
	e := &Engine{log: slog.New(slog.NewTextHandler(&buf, nil))}
	return e, &buf
}

func TestPublishingIsNotAnnouncedUntilMediaMoves(t *testing.T) {
	e, buf := newMediaLogger(t)
	onProgress := e.firstMediaLogger("dest-a", db.DestRTMP)

	// FFmpeg bound and waiting: progress blocks arrive, OutTimeMS stays zero.
	// This is precisely the state a destination is in when its filter graph
	// failed and nothing will ever be written.
	for range 5 {
		onProgress(ffmpeg.Progress{OutTimeMS: 0})
	}

	if strings.Contains(buf.String(), "destination publishing") {
		t.Errorf("announced publishing for a destination that has moved no media:\n%s", buf.String())
	}
}

func TestPublishingIsAnnouncedOnceWhenMediaMoves(t *testing.T) {
	e, buf := newMediaLogger(t)
	onProgress := e.firstMediaLogger("dest-a", db.DestRTMP)

	onProgress(ffmpeg.Progress{OutTimeMS: 40})
	onProgress(ffmpeg.Progress{OutTimeMS: 80})
	onProgress(ffmpeg.Progress{OutTimeMS: 120})

	if n := strings.Count(buf.String(), "destination publishing"); n != 1 {
		t.Errorf("announced publishing %d times for one run, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "dest-a") {
		t.Errorf("the line does not name the destination: %s", buf.String())
	}
}

func TestPublishingIsAnnouncedAgainAfterAReconnect(t *testing.T) {
	// THE CASE A sync.Once WOULD GET WRONG, and the reason this is not one.
	//
	// The handler outlives a restart because the supervisor keeps its Spec
	// across them. A once-per-process announcement would report the first
	// publish and then stay silent through every reconnect after it -- which is
	// exactly when an operator most needs to hear that the stream came back.
	e, buf := newMediaLogger(t)
	onProgress := e.firstMediaLogger("dest-a", db.DestRTMP)

	onProgress(ffmpeg.Progress{OutTimeMS: 40})
	onProgress(ffmpeg.Progress{OutTimeMS: 5_000})

	// A fresh FFmpeg starts its output clock near zero again.
	onProgress(ffmpeg.Progress{OutTimeMS: 40})
	onProgress(ffmpeg.Progress{OutTimeMS: 80})

	if n := strings.Count(buf.String(), "destination publishing"); n != 2 {
		t.Errorf("announced publishing %d times across two runs, want 2:\n%s", n, buf.String())
	}
}
