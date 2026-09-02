package engine

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// keyedManagerFixture is managerFixture with the two things it cannot express:
// a database sealed under one secret and opened under another, and a log this
// test can read.
//
// THE MANAGER IS NOT STARTED. warnAboutUnreadableStreamKeys is called directly,
// so the buffer below holds exactly the records this one call produced --
// nothing from a supervisor goroutine, which is what makes an assertion on a
// shared log buffer flaky in this package.
func keyedManagerFixture(t *testing.T, sealWith, openWith *secrets.Box, destNames ...string) (*Manager, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "polyemesis.db")

	sealed, err := db.Open(path, db.WithSecretBox(sealWith))
	if err != nil {
		t.Fatalf("open sealed: %v", err)
	}
	// A destination belongs to a source, so there has to be one to belong to.
	if err := sealed.CreateSource(&db.Source{
		Name: "Main", Enabled: true, Ingest: db.DefaultSettings().Ingest,
	}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	for _, name := range destNames {
		if _, err := sealed.CreateDestination(&db.Destination{
			Name: name, Kind: db.DestRTMP, URL: "rtmp://ingest.example/live",
			StreamKey: "a-live-credential", AudioBitrate: 160, Profile: routing.DefaultProfile(),
		}); err != nil {
			t.Fatalf("CreateDestination(%s): %v", name, err)
		}
	}
	if err := sealed.Close(); err != nil {
		t.Fatalf("close sealed: %v", err)
	}

	store, err := db.Open(path, db.WithSecretBox(openWith))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	tools := &ffmpeg.Tools{FFmpeg: filepath.Join(dir, "no-such-ffmpeg"), FFprobe: filepath.Join(dir, "no-such-ffprobe")}

	var buf bytes.Buffer
	m := NewManager(slog.New(slog.NewTextHandler(&buf, nil)), cfg, store, tools, events.NewBroker())
	t.Cleanup(m.Stop)
	return m, &buf
}

func boxOf(t *testing.T, b byte) *secrets.Box {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{b}, 32))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	return box
}

// A data directory restored WITHOUT its secret.key boots perfectly. A new key
// is minted in silence, the API answers, the destinations are all listed -- and
// every one of them holds a stream key sealed under a secret that is gone. The
// restore reads as a success until the operator tries to go live.
//
// Warning rather than control: an install with one dead destination among
// twenty must still serve the nineteen, and must still serve the screen the
// operator fixes the one on.
func TestBootSaysHowManyDestinationsTheSecretKeyCannotOpen(t *testing.T) {
	m, log := keyedManagerFixture(t, boxOf(t, 0x11), boxOf(t, 0x22), "Twitch main", "YouTube backup")

	m.warnAboutUnreadableStreamKeys()

	got := log.String()
	if !strings.Contains(got, "destinations=2") {
		t.Errorf("the boot log does not say how many destinations will fail to open: %s", got)
	}
	for _, want := range []string{"Twitch main", "YouTube backup"} {
		if !strings.Contains(got, want) {
			t.Errorf("the boot log does not name %q, so the operator has a number and no next step: %s", want, got)
		}
	}
	if !strings.Contains(got, "secret.key") {
		t.Errorf("the boot log does not name the file that went missing: %s", got)
	}
	if !strings.Contains(got, "level=ERROR") {
		t.Errorf(`the warning is below ERROR; "your restore did not restore" is not housekeeping: %s`, got)
	}
	// The credential itself must not be in the log. This whole warning is about
	// keys, and a diagnostic that prints one is worse than the silence it
	// replaced.
	if strings.Contains(got, "a-live-credential") {
		t.Error("the boot warning printed a stream key")
	}
}

// The control: a healthy install must say nothing. A boot line that fires on
// every start is a boot line nobody reads on the start where it is true.
func TestBootIsSilentWhenEveryStreamKeyOpens(t *testing.T) {
	box := boxOf(t, 0x11)
	m, log := keyedManagerFixture(t, box, box, "Twitch main")

	m.warnAboutUnreadableStreamKeys()

	if got := log.String(); strings.Contains(got, "secret key") {
		t.Errorf("a healthy install logged a stranded-key warning: %s", got)
	}
}
