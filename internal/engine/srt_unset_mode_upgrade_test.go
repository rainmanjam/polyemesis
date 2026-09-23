package engine

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// A source the console created on 0.10.0 still takes its SRT encoder after
// the upgrade.
//
// On 0.10.0 the create form's {name} stored a source with ingest mode "", and
// the shared SRT port admitted it: measured in Docker against the v0.10.0
// image, "srt publisher connected ... source=console-made". The port then
// learned to admit only SRT-mode sources (TestTheSRTListenerAdmitsOnlySRTModeSources),
// which is right for rtmp and pull and, on its own, cut every such source off
// on the first boot after an upgrade -- the encoder dials and is refused, and
// nothing in the UI says why.
//
// So this is the upgrade, not a unit: the row is written with the exact blob
// v0.10.0 wrote, the database is closed and opened again the way a new binary
// opens it, and a real SRT caller dials the real listener with the source's
// token as its streamid.
//
// Mutation: remove MigrateUnsetSourceIngestMode from db.Open. Observed to fail
// with "refused an SRT publish".
func TestAnUpgradedConsoleSourceStillAdmitsSRT(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polyemesis.db")

	// The old install.
	old := dbtest.OpenAt(t, path)
	const streamID = "console-made-source-streamid"
	if _, err := old.SQL().Exec(
		`INSERT INTO sources (name, enabled, ingest, token, position, created_at, updated_at)
		 VALUES ('console-made', 1, ?, ?, 1, 1700000000, 1700000000)`,
		`{"mode":"","srt":{"passphrase":"","latencyMs":200},"rtmp":{"app":"live","streamKey":""},`+
			`"pull":{"url":"","reconnectDelayMaxSeconds":30,"rtspTransport":"tcp"}}`, streamID); err != nil {
		t.Fatalf("insert the v0.10.0 source: %v", err)
	}
	st, err := old.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	st.Listeners.RTMPPort = freeTCPPort(t)
	if err := old.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close the old install: %v", err)
	}

	// The upgrade boot.
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("the upgrade boot could not open the database: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	tools := &ffmpeg.Tools{FFmpeg: filepath.Join(dir, "no-such-ffmpeg"), FFprobe: filepath.Join(dir, "no-such-ffprobe")}
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, store, tools, events.NewBroker())
	t.Cleanup(m.Stop)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !m.ListenerBound(db.IngestSRT) {
		t.Fatal("control: the SRT listener did not bind, so a refusal below would prove nothing")
	}

	c := srt.DefaultConfig()
	c.StreamId = streamID
	c.ConnectionTimeout = 3 * time.Second
	conn, err := srt.Dial("srt", "127.0.0.1:"+strconv.Itoa(st.Listeners.SRTPort), c)
	if err != nil {
		t.Fatalf("the shared SRT port refused an SRT publish into a source the console created "+
			"on 0.10.0 (%v); that encoder was connected before the upgrade", err)
	}
	conn.Close()
}
