//go:build !windows

// POSIX only: the file this test plants has a backslash in its name, which is
// an ordinary character on POSIX and a path separator on Windows -- there no
// file name can reach ObjectKey with a backslash, so the case cannot arise.

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
