package offload

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/objstore"
)

// countingReader yields zeros for ever-ish and counts what was taken.
type countingReader struct {
	n, limit int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.n >= c.limit {
		return 0, io.EOF
	}
	if int64(len(p)) > c.limit-c.n {
		p = p[:c.limit-c.n]
	}
	clear(p)
	c.n += int64(len(p))
	return len(p), nil
}

// oversized answers Get with a declared length and a body of its choosing.
type oversized struct {
	*memStore
	declared int64
	body     *countingReader
}

func (o oversized) Get(context.Context, string) (io.ReadCloser, objstore.ObjectInfo, error) {
	return io.NopCloser(o.body), objstore.ObjectInfo{Size: o.declared}, nil
}

func evictedFixture(t *testing.T) (*fixture, string, db.OffloadItem) {
	t.Helper()
	f := newFixture(t)
	id := f.target(db.OffloadMove, 0)
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	it, _ := f.item(id, name)
	if !it.Evicted {
		t.Fatal("setup: not evicted")
	}
	return f, name, it
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

// A body longer than the uploaded length is cut off one byte past it -- the
// volume is never filled ahead of the digest check -- and refused.
func TestFetchBackReadsAtMostOneBytePastTheUploadedLength(t *testing.T) {
	f, name, it := evictedFixture(t)
	body := &countingReader{limit: 8 << 20}
	f.svc.newClient = func(db.StorageTarget) (Client, error) {
		return oversized{memStore: newMem(), declared: it.Bytes, body: body}, nil
	}
	err := f.svc.FetchNow(context.Background(), name)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err %v", err)
	}
	if body.n > it.Bytes+1 {
		t.Fatalf("read %d bytes of an object uploaded as %d", body.n, it.Bytes)
	}
	if f.local(name) {
		t.Fatal("an oversized fetch landed")
	}
	noTempFiles(t, f.dir)
}

// A declared length that is not the uploaded length is refused before a
// single byte is written.
func TestFetchBackRefusesAMismatchedContentLengthBeforeReading(t *testing.T) {
	f, name, it := evictedFixture(t)
	body := &countingReader{limit: 1 << 20}
	f.svc.newClient = func(db.StorageTarget) (Client, error) {
		return oversized{memStore: newMem(), declared: it.Bytes + 1000, body: body}, nil
	}
	if err := f.svc.FetchNow(context.Background(), name); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err %v", err)
	}
	if body.n != 0 {
		t.Fatalf("read %d bytes of an object whose declared length was already wrong", body.n)
	}
	noTempFiles(t, f.dir)
}

// Two files that share a basename in different subdirectories get different
// keys, and both objects survive with their own bytes.
func TestTwoFilesSharingABasenameNeverShareAnObject(t *testing.T) {
	f := newFixture(t)
	id, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "c", Provider: "s3", Endpoint: "http://h.test", Bucket: "b",
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMirror, Enabled: true, OffloadClips: true,
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
	id := f.target(db.OffloadMove, 0)
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
		Key: key, State: db.OffloadVerified, Evicted: true, SHA256: "x", Bytes: 9}); err != nil {
		t.Fatal(err)
	}
	f.mems[id].objs[key] = []byte("only copy")
	f.svc.Pass(context.Background())
	if got := string(f.mems[id].objs[key]); got != "only copy" {
		t.Fatalf("the owner's object was overwritten with %q", got)
	}
	it, ok := f.item(id, name)
	if !ok || it.State != db.OffloadFailed || !strings.Contains(it.LastError, "already held by elsewhere.mkv") {
		t.Fatalf("%+v", it)
	}
	if !f.local(name) {
		t.Fatal("a file refused its key was evicted")
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
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMirror, Enabled: true, OffloadClips: true})
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
	if !ok || bad.State != db.OffloadFailed || !strings.Contains(bad.LastError, "invalid file path") {
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
	good := f.target(db.OffloadMirror, 0)
	bad, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "bad", Provider: "s3", Endpoint: "http://h.test", Bucket: "b",
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMirror, Enabled: true, PrefixTemplate: "{nope}"})
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
