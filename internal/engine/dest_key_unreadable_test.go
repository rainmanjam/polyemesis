package engine

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// A destination whose stored stream key will not decrypt on this machine --
// the key file was not restored with the database, or the database was moved to
// a different host. internal/db refuses to hand out the key and refuses to
// report the destination as enabled, which is the fail-closed half; this is the
// other half, and without it the operator gets no explanation at all.
//
// The card would otherwise be a perfectly healthy looking destination that is
// simply switched off: no process, no error, no compile failure, nothing to
// distinguish it from one the operator turned off themselves last week. That
// reading sends somebody hunting through routing profiles and platform
// settings for a problem that is a missing file in the data directory.
//
// Driven through e.Status(), which is what the API and the WebSocket push both
// call, rather than through the store alone. A test of scanDestination would
// pass while the dashboard went on showing nothing, and that is exactly the
// shape of test this repo has been burned by.
//
// Mutation: delete the `if row.KeyUnreadable != ""` block from status.go.
// Observed to fail with "its card carries no warning at all". Restored from a
// file backup; git diff --stat empty.
//
// Mutation: in internal/db's scanDestination, stop setting KeyUnreadable (leave
// the disable and the blanking). Observed to fail on the same assertion, which
// is what says the two halves are both load-bearing.
func TestADestinationWhoseKeyCannotBeReadSaysSoOnItsCard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polyemesis.db")

	written, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	// A different key file, which is what a machine that generated its own has.
	present, err := secrets.New(bytes.Repeat([]byte{0x5c}, 32))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}

	// Written by the install that HAD the key.
	sealed := dbtest.OpenAt(t, path, db.WithSecretBox(written))
	lost, err := sealed.CreateDestination(&db.Destination{
		Name: "the one with the lost key", Kind: db.DestRTMP,
		URL: "rtmp://ingest.example/live", StreamKey: "live-key-9000",
		Enabled: true, AudioBitrate: 160, Profile: routing.DefaultProfile(),
	})
	if err != nil {
		t.Fatalf("CreateDestination(sealed): %v", err)
	}
	// THE CONTROL, and it is not optional: without it a status layer that
	// simply warned on every destination would pass this test.
	fine, err := sealed.CreateDestination(&db.Destination{
		Name: "archive", Kind: db.DestFile, URL: "archive.mkv",
		Enabled: false, AudioBitrate: 160, Profile: routing.DefaultProfile(),
	})
	if err != nil {
		t.Fatalf("CreateDestination(control): %v", err)
	}
	if err := sealed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Read by the install that does not.
	e, _ := engineOnKeylessStore(t, dir, path, present)

	byID := map[int64]DestStatus{}
	for _, ds := range e.Status().Destinations {
		byID[ds.ID] = ds
	}

	got, ok := byID[lost.ID]
	if !ok {
		t.Fatal("the destination whose key cannot be read is missing from the status " +
			"snapshot entirely: an operator cannot fix what the dashboard does not show")
	}
	if got.Name != "the one with the lost key" {
		t.Errorf("the card lost its name: %q", got.Name)
	}
	if got.Enabled {
		t.Error("the card reports the destination as enabled: nothing may publish " +
			"with a stream key that could not be read")
	}
	var reason string
	for _, w := range got.Warnings {
		if strings.Contains(w, "could not be read on this machine") {
			reason = w
		}
	}
	if reason == "" {
		t.Fatalf("its card carries no warning at all, only %q. The destination is "+
			"off with no explanation, which reads as somebody having switched it "+
			"off rather than as a missing key file.", got.Warnings)
	}
	if !strings.Contains(reason, "re-enter") {
		t.Errorf("the warning is %q, which does not tell the operator what to do", reason)
	}
	// It must never carry the credential itself: DestStatus reaches a RETAINED
	// MQTT topic, which is delivered to every future subscriber.
	if strings.Contains(reason, "live-key-9000") {
		t.Error("the warning contains the stream key")
	}

	control, ok := byID[fine.ID]
	if !ok {
		t.Fatal("the control destination is missing from the status snapshot")
	}
	for _, w := range control.Warnings {
		if strings.Contains(w, "could not be read on this machine") {
			t.Errorf("a destination with no stream key at all is warned about too: %q. "+
				"The flag is being sprayed across the dashboard rather than naming "+
				"the destination that has the problem.", w)
		}
	}
}

// engineOnKeylessStore is storeEngine with the store opened at a caller-chosen
// path and key. Separate rather than a parameter on storeEngine because every
// other caller of that helper wants neither, and this one needs to open a
// database some earlier handle already wrote.
func engineOnKeylessStore(t *testing.T, dir, path string, box *secrets.Box) (*Engine, *db.DB) {
	t.Helper()
	store, err := db.Open(path, db.WithSecretBox(box))
	if err != nil {
		t.Fatalf("reopen %s: %v", path, err)
	}
	t.Cleanup(func() { store.Close() })

	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	tools := &ffmpeg.Tools{
		FFmpeg:  filepath.Join(dir, "no-such-ffmpeg"),
		FFprobe: filepath.Join(dir, "no-such-ffprobe"),
	}
	id, err := store.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	e, err := New(testLogger(), cfg, store, tools, events.NewBroker(), id, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(e.Stop)
	return e, store
}
