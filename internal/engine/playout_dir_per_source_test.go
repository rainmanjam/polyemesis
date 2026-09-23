package engine

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// Two programmes must not package playout into the same directory.
//
// Every engine runs its own playout manager, and a variant's muxer writes
// <dir>/<variant name>/. While every engine was handed cfg.PlayoutDir() itself,
// two programmes with the default "main" variant both wrote /data/playout/main/:
// their segments and playlists overwrote each other, each engine's teardown
// cleared the other's live window, and playout.sourceId -- which picks the
// engine whose handler serves -- changed nothing, because every handler served
// the same files. Reproduced live with two SRT sources of different sizes: the
// newest segment flipped between the two sizes every few samples.
//
// The directories must also not NEST: the sweeper walks its root recursively,
// so a programme whose directory sat inside another's would have its live
// window pruned under the other's storage limit.
//
// Mutation: hand engine.New's playout.Deps cfg.PlayoutDir() again. Observed to
// fail with "both package into".
func TestEachSourcePackagesPlayoutIntoItsOwnDirectory(t *testing.T) {
	one, store := storeEngine(t)

	src := &db.Source{Name: "Studio B", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	two, err := New(testLogger(), one.cfg, store, one.tools, events.NewBroker(), src.ID, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(two.Stop)

	a, b := one.Playout().Dir(), two.Playout().Dir()
	if a == b {
		t.Fatalf("programmes %d and %d both package into %s: the second muxer overwrites "+
			"the first's segments and playout.sourceId has nothing to choose between",
			one.sourceID, two.sourceID, a)
	}
	within := func(child, parent string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if within(a, b) || within(b, a) {
		t.Fatalf("playout directories %s and %s nest, so one sweeper prunes the other's window", a, b)
	}
	// Under the shared root, so the installer's permissions and the disk
	// accounting that already knows about it still reach both.
	for _, d := range []string{a, b} {
		if filepath.Dir(d) != one.cfg.PlayoutDir() {
			t.Errorf("playout directory %s is not directly under %s", d, one.cfg.PlayoutDir())
		}
	}
}
