package offload

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// countingReader yields zeros for ever-ish and counts what was taken.
type countingReader struct {
	n, limit int64
}

// oversized answers Get with a declared length and a body of its choosing.
type oversized struct {
	*memStore
	declared int64
	body     *countingReader
}

func noTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".offload-fetch-") {
			t.Fatalf("temporary file %s left behind", e.Name())
		}
	}
}

// Two files that share a basename in different subdirectories get different
// keys, and both objects survive with their own bytes.
func TestTwoFilesSharingABasenameNeverShareAnObject(t *testing.T) {
	f := newFixture(t)
	id, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "c", Provider: "s3", Endpoint: "http://h.test", Bucket: "b",
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Enabled: true, OffloadClips: true,
		PrefixTemplate: "flat/{kind}"})
	f.mems[id] = newMem()
	old := f.now.Add(-time.Hour)
	for _, p := range []string{"clips/clip-20261101-120000.ts", "exports/clip-20261101-120000.ts"} {
		full := filepath.Join(f.dir, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte("bytes of "+p), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(full, old, old)
	}
	f.svc.Pass(context.Background())
	keys := map[string]bool{}
	for _, p := range []string{"clips/clip-20261101-120000.ts", "exports/clip-20261101-120000.ts"} {
		it, ok := f.item(id, p)
		if !ok || it.State != db.OffloadVerified {
			t.Fatalf("%s: %+v", p, it)
		}
		if got := string(f.mems[id].objs[it.Key]); got != "bytes of "+p {
			t.Fatalf("%s: object %s holds %q", p, it.Key, got)
		}
		keys[it.Key] = true
	}
	if len(keys) != 2 {
		t.Fatalf("keys %v", keys)
	}
}

// A rendered key that another file already owns on the target is refused and
// reported on the item; the owner's object is never overwritten.
func TestAKeyAnotherFileOwnsIsRefusedNotOverwritten(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	name := f.segment(5*time.Hour, "new footage", true)
	f.segment(time.Minute, "newest", true)
	recs, _ := f.store.ListRecordings()
	var at time.Time
	for _, r := range recs {
		if r.Filename == name {
			at = r.StartedAt
		}
	}
	key, err := ObjectKey("", db.OffloadKindRecording, nil, at, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutOffloadItem(db.OffloadItem{TargetID: id, RelPath: "elsewhere.mkv", Kind: db.OffloadKindRecording,
		Key: key, State: db.OffloadVerified, SHA256: "x", Bytes: 9}); err != nil {
		t.Fatal(err)
	}
	f.mems[id].objs[key] = []byte("only copy")
	// The owner is a real local file, so the pass keeps its row.
	if err := os.WriteFile(filepath.Join(f.dir, "elsewhere.mkv"), []byte("elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.svc.Pass(context.Background())
	if got := string(f.mems[id].objs[key]); got != "only copy" {
		t.Fatalf("the owner's object was overwritten with %q", got)
	}
	it, ok := f.item(id, name)
	if !ok || it.State != db.OffloadStuck || !strings.Contains(it.LastError, "already held by elsewhere.mkv") {
		t.Fatalf("%+v", it)
	}
	if !f.local(name) {
		t.Fatal("a file refused its key lost its local copy")
	}
	// The database refuses the collision on its own too.
	err = f.store.PutOffloadItem(db.OffloadItem{TargetID: id, RelPath: "third.mkv", Kind: db.OffloadKindRecording, Key: key, State: db.OffloadPending})
	if err == nil {
		t.Fatal("two files stored under one key on one target")
	}
}

// One file whose path cannot be an object key is marked failed with the
// reason; it does not stop the files after it.
func TestABadFilePathFailsThatFileAndNotTheRest(t *testing.T) {
	f := newFixture(t)
	id, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "c", Provider: "s3", Endpoint: "http://h.test", Bucket: "b",
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Enabled: true, OffloadClips: true})
	f.mems[id] = newMem()
	_ = os.MkdirAll(filepath.Join(f.dir, "clips"), 0o755)
	// A backslash is legal in a POSIX file name and never in an object key.
	for i, name := range []string{`bad\name.ts`, "good.ts"} {
		full := filepath.Join(f.dir, "clips", name)
		if err := os.WriteFile(full, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		at := f.now.Add(-time.Duration(3-i) * time.Hour) // the bad one sorts first
		_ = os.Chtimes(full, at, at)
	}
	f.svc.Pass(context.Background())
	bad, ok := f.item(id, `clips/bad\name.ts`)
	if !ok || bad.State != db.OffloadStuck || !strings.Contains(bad.LastError, "invalid file path") {
		t.Fatalf("bad file: %+v ok=%v", bad, ok)
	}
	good, ok := f.item(id, "clips/good.ts")
	if !ok || good.State != db.OffloadVerified {
		t.Fatalf("the good file after it was not uploaded: %+v ok=%v", good, ok)
	}
}

// A bad template skips only its own target.
func TestABadTemplateSkipsOnlyItsTarget(t *testing.T) {
	f := newFixture(t)
	good := f.target()
	bad, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "bad", Provider: "s3", Endpoint: "http://h.test", Bucket: "b",
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Enabled: true, PrefixTemplate: "{nope}"})
	f.mems[bad] = newMem()
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if it, _ := f.item(good, name); it.State != db.OffloadVerified {
		t.Fatalf("good target: %+v", it)
	}
	if f.mems[bad].puts != 0 {
		t.Fatal("a target with an invalid template uploaded")
	}
	// A target-level fault is not smeared across every file as item errors.
	if _, ok := f.item(bad, name); ok {
		t.Fatal("a template error was recorded against a file")
	}
}
