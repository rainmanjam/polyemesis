package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/clips"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// Deleting a clip while the buffer is off goes through no capturer, and used to
// remove the file alone: the clip's record (clip-….ts.meta.json) stayed behind,
// invisible to the listing and to retention, so nothing ever cleaned it up.
func TestDeleteClipWithTheBufferOffRemovesTheClipsRecordToo(t *testing.T) {
	dir := t.TempDir()
	name := "clip-20260101-000000.ts"
	for _, f := range []string{name, name + ".meta.json"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{bus: events.NewBroker(), clipCfg: clips.Config{Dir: dir}}

	if err := e.DeleteClip(name); err != nil {
		t.Fatalf("DeleteClip: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("left behind after a delete with the buffer off: %v", entries)
	}
}
