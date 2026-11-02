package offload

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/objstore"
)

// movedFixture: one move-mode target (keep 0), one old finished recording and
// the newest one, uploaded and verified but not yet evicted (keep window big).
func movedFixture(t *testing.T) (*fixture, int64, string) {
	t.Helper()
	f := newFixture(t)
	id := f.target(db.OffloadMove, 1000)
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if it, _ := f.item(id, name); it.State != db.OffloadVerified || it.Evicted {
		t.Fatalf("setup %+v", it)
	}
	tg, _ := f.store.GetStorageTarget(f.svc.box, id)
	tg.KeepLocalHours, tg.SecretAccessKey = 0, ""
	if err := f.store.UpdateStorageTarget(f.svc.box, tg); err != nil {
		t.Fatal(err)
	}
	return f, id, name
}

// #2: a file marked evicted that is still on disk (a crash between the mark
// and the unlink) is repaired -- the mark is cleared -- not left as a row that
// claims a bucket copy is the only one while the file sits here.
func TestAnEvictionInterruptedBeforeTheUnlinkIsRepaired(t *testing.T) {
	f, id, name := movedFixture(t)
	tg, _ := f.store.GetStorageTarget(f.svc.box, id)
	tg.KeepLocalHours, tg.SecretAccessKey = 1000, ""
	_ = f.store.UpdateStorageTarget(f.svc.box, tg)
	if err := f.store.BeginEvict(name, nil); err != nil {
		t.Fatal(err)
	}
	f.svc.Pass(context.Background())
	if !f.local(name) || f.svc.Evicted(name) {
		t.Fatalf("local=%v evicted=%v; the stale mark was not cleared", f.local(name), f.svc.Evicted(name))
	}
}

// #2: evictOne never marks a file evicted that it did not remove itself, and
// undoes the mark when the file changed or vanished after the check.
func TestEvictOneUndoesTheMarkWhenTheFileIsNotAsChecked(t *testing.T) {
	f, _, name := movedFixture(t)
	path := filepath.Join(f.dir, name)
	fi, _ := os.Stat(path)
	cand := candidate{rel: name, kind: db.OffloadKindRecording, size: fi.Size(), mtime: fi.ModTime()}
	ids := []int64{1}

	_ = os.WriteFile(path, []byte("rewritten after the check"), 0o644)
	if f.svc.evictOne(cand, ids) || !f.local(name) || f.svc.Evicted(name) {
		t.Fatal("evicted a file that changed after it was checked")
	}
	_ = os.Remove(path)
	if f.svc.evictOne(cand, ids) || f.svc.Evicted(name) {
		t.Fatal("a file somebody else removed was marked evicted")
	}
}

// #3 / #4 / #5: the gate re-reads the enabled targets in its own transaction.
func TestBeginEvictRefusesUnlessEveryEnabledTargetIsVerified(t *testing.T) {
	f, a, name := movedFixture(t)
	b, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "b", Provider: "s3", Endpoint: "http://h.test",
		Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMirror, Enabled: true})
	if err := f.store.BeginEvict(name, []int64{a}); !errors.Is(err, db.ErrNotCovered) {
		t.Fatalf("evicted while target b (enabled, no row) holds nothing: %v", err)
	}
	_ = f.store.PutOffloadItem(db.OffloadItem{TargetID: b, RelPath: name, Kind: db.OffloadKindRecording, Key: "kb",
		State: db.OffloadVerified, SHA256: "x", VerifiedAt: time.Now()})
	if err := f.store.BeginEvict(name, []int64{a}); !errors.Is(err, db.ErrNotCovered) {
		t.Fatal("the checked set is not the enabled set, and the gate let it through")
	}
	// Target b deleted between the caller's checks and the gate.
	if err := f.store.DeleteStorageTarget(b, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.store.BeginEvict(name, []int64{a, b}); !errors.Is(err, db.ErrNotCovered) {
		t.Fatal("a target deleted mid-pass was still counted as holding a copy")
	}
	if err := f.store.BeginEvict(name, []int64{a}); err != nil {
		t.Fatalf("the real enabled set, all verified: %v", err)
	}
}

// #5 / #15: an enabled target that cannot vouch for a copy -- no secret, or a
// client that will not build -- blocks eviction, and does not panic.
func TestAnUnusableEnabledTargetBlocksEviction(t *testing.T) {
	for _, tc := range []string{"no secret", "client fails"} {
		t.Run(tc, func(t *testing.T) {
			f, _, name := movedFixture(t)
			id, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "x", Provider: "s3", Endpoint: "http://h.test",
				Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMirror, Enabled: true})
			_ = f.store.PutOffloadItem(db.OffloadItem{TargetID: id, RelPath: name, Kind: db.OffloadKindRecording, Key: "kx",
				State: db.OffloadVerified, SHA256: "x", Bytes: 7, VerifiedAt: f.now})
			if tc == "no secret" {
				_, _ = f.store.SQL().Exec(`UPDATE storage_targets SET secret_enc = NULL WHERE id = ?`, id)
			} else {
				good := f.svc.newClient
				f.svc.newClient = func(t db.StorageTarget) (Client, error) {
					if t.ID == id {
						return nil, errors.New("will not build")
					}
					return good(t)
				}
			}
			f.svc.Pass(context.Background())
			if !f.local(name) || f.svc.Evicted(name) {
				t.Fatal("evicted although an enabled target could not vouch for its copy")
			}
		})
	}
}

// #6: a stored object of the right size but without (or with another)
// SHA-256 is not evidence; the file stays and the row goes back to pending.
func TestAReplacedObjectOfTheSameSizeBlocksEvictionAndReuploads(t *testing.T) {
	f, id, name := movedFixture(t)
	it, _ := f.item(id, name)
	m := f.mems[id]
	m.objs[it.Key] = []byte("FOOTAGE") // same length, other bytes
	delete(m.meta, it.Key)
	f.svc.Pass(context.Background())
	if !f.local(name) || f.svc.Evicted(name) {
		t.Fatal("evicted on size alone")
	}
	if it, _ := f.item(id, name); it.State == db.OffloadVerified {
		// the same pass may already have re-uploaded it; what matters is that
		// it was not left "verified" against the replaced object
		if string(m.objs[it.Key]) != "footage" {
			t.Fatalf("row still verified against a replaced object: %+v", it)
		}
	}
}

// #9: a recording something is reading is not evicted.
func TestARecordingInUseIsNotEvicted(t *testing.T) {
	f, _, name := movedFixture(t)
	unpin := f.svc.Pin(name)
	f.svc.Pass(context.Background())
	if !f.local(name) {
		t.Fatal("evicted a pinned recording")
	}
	unpin()
	f.svc.SetInUse(func(n string) bool { return n == name })
	f.svc.Pass(context.Background())
	if !f.local(name) {
		t.Fatal("evicted a recording a job is reading")
	}
	f.svc.SetInUse(nil)
	f.svc.Pass(context.Background())
	if f.local(name) {
		t.Fatal("not evicted once nothing used it")
	}
}

// #12: the stems of a master already moved off the disk are still uploaded,
// for example to a target added afterwards.
func TestStemsOfAnEvictedMasterAreStillOffloaded(t *testing.T) {
	f := newFixture(t)
	f.target(db.OffloadMove, 0)
	name := f.segment(5*time.Hour, "master", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if !f.svc.Evicted(name) {
		t.Fatal("setup: not evicted")
	}
	stem := "stems/" + strings.TrimSuffix(name, ".mkv") + "-mic.flac"
	full := filepath.Join(f.dir, filepath.FromSlash(stem))
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	_ = os.WriteFile(full, []byte("stem"), 0o644)
	old := f.now.Add(-time.Hour)
	_ = os.Chtimes(full, old, old)
	late, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "late", Provider: "s3", Endpoint: "http://h.test",
		Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMirror, Enabled: true, OffloadStems: true})
	f.mems[late] = newMem()
	f.svc.Pass(context.Background())
	if it, ok := f.item(late, stem); !ok || it.State != db.OffloadVerified {
		t.Fatalf("stem of an evicted master: %+v %v", it, ok)
	}
}

// #13: Retry now pressed after a pass has read the items is not overwritten
// by that pass's stale copy of the attempt count, and its kick is not dropped.
func TestRetryNowDuringAPassIsNotOverwrittenByIt(t *testing.T) {
	f := newFixture(t)
	id, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "t", Provider: "s3", Endpoint: "http://h.test",
		Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMirror, Enabled: true, MaxAttempts: 7})
	m := newMem()
	m.putErr = &objstore.Error{Op: "PutObject", StatusCode: 503}
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	_ = f.store.PutOffloadItem(db.OffloadItem{TargetID: id, RelPath: name, Kind: db.OffloadKindRecording,
		Key: "polyemesis/recordings/x/" + name, State: db.OffloadFailed, Attempts: 6})
	var once sync.Once
	// The factory runs after the pass has read the items: the operator's
	// Retry now lands exactly in that window.
	f.svc.newClient = func(db.StorageTarget) (Client, error) {
		once.Do(func() {
			if n, err := f.svc.Retry(id, ""); err != nil || n != 1 {
				t.Errorf("retry %d %v", n, err)
			}
		})
		return m, nil
	}
	f.svc.Pass(context.Background())
	it, _ := f.item(id, name)
	if it.State == db.OffloadStuck || it.Attempts != 1 {
		t.Fatalf("after a retry mid-pass: %+v (want failed, attempts 1)", it)
	}
	if m.puts == 0 {
		t.Fatal("the retried item was not attempted")
	}
}

// #17: an SSE-KMS or SSE-C object's ETag is not its MD5; an upload is not
// failed for that.
type kmsStore struct{ *memStore }

func (k kmsStore) Head(ctx context.Context, key string) (objstore.ObjectInfo, error) {
	info, err := k.memStore.Head(ctx, key)
	info.ETag = strings.Repeat("9f", 16)
	info.OpaqueETag = true
	return info, err
}

func TestAnSSEKMSObjectIsVerifiedWithoutComparingItsETag(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMirror, 0)
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return kmsStore{f.mems[id]}, nil }
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if it, _ := f.item(id, name); it.State != db.OffloadVerified {
		t.Fatalf("%+v", it)
	}
	// And the same opaque ETag WITHOUT the encryption header is a mismatch.
	f2 := newFixture(t)
	id2 := f2.target(db.OffloadMirror, 0)
	f2.mems[id2].lieETag = true
	n2 := f2.segment(5*time.Hour, "footage", true)
	f2.segment(time.Minute, "newest", true)
	f2.svc.Pass(context.Background())
	if it, _ := f2.item(id2, n2); it.State == db.OffloadVerified {
		t.Fatal("an unexplained wrong ETag verified")
	}
}

// #20: a long session's fetch runs a bounded number of transfers at once.
type countingGets struct {
	*memStore
	cur, max atomic.Int32
}

func (c *countingGets) GetFrom(ctx context.Context, key string, off int64) (io.ReadCloser, objstore.ObjectInfo, bool, error) {
	n := c.cur.Add(1)
	for {
		m := c.max.Load()
		if n <= m || c.max.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	defer c.cur.Add(-1)
	return c.memStore.GetFrom(ctx, key, off)
}

func TestFetchingManySegmentsRunsAtMostTwoAtOnce(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMove, 0)
	var names []string
	for i := 0; i < 5; i++ {
		names = append(names, f.segment(time.Duration(10+i)*time.Hour, "footage "+string(rune('a'+i)), true))
	}
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	cg := &countingGets{memStore: f.mems[id]}
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return cg, nil }
	done := make(chan struct{}, 8)
	f.svc.onChange = func() { done <- struct{}{} }
	for _, n := range names {
		if _, err := f.svc.Fetch(n); err != nil {
			t.Fatal(err)
		}
	}
	for range names {
		<-done
	}
	if m := cg.max.Load(); m > maxConcurrentFetches || m == 0 {
		t.Fatalf("%d fetches ran at once", m)
	}
}

// #22: a connection dropped after the last byte is a complete download, not
// a retry for bytes=<size>- that answers 416.
func TestADropAfterTheLastByteIsAComplete(t *testing.T) {
	f := newFixture(t)
	f.svc.fetchBackoff = time.Millisecond
	id := f.target(db.OffloadMove, 0)
	body := bigBody(64)
	name := f.segment(5*time.Hour, body, true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	f.mems[id].getCuts = []int64{64}
	if err := f.svc.FetchNow(context.Background(), name); err != nil {
		t.Fatalf("complete download failed: %v", err)
	}
}

// #23: finished fetch states are pruned after their TTL.
func TestFinishedFetchStatesArePruned(t *testing.T) {
	f := newFixture(t)
	f.svc.fetches["old.mkv"] = &FetchState{State: FetchDone, Since: f.now.Add(-2 * fetchStateTTL)}
	f.svc.fetches["recent.mkv"] = &FetchState{State: FetchFailed, Since: f.now}
	f.svc.fetchMu.Lock()
	f.svc.pruneFetchesLocked()
	f.svc.fetchMu.Unlock()
	if _, ok := f.svc.fetches["old.mkv"]; ok {
		t.Fatal("not pruned")
	}
	if _, ok := f.svc.fetches["recent.mkv"]; !ok {
		t.Fatal("pruned too early")
	}
}

// #7: a file the recorder created under the name meanwhile is never
// overwritten by a fetch-back.
func TestFetchBackNeverReplacesAFileThatAppeared(t *testing.T) {
	f := newFixture(t)
	f.target(db.OffloadMove, 0)
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	path := filepath.Join(f.dir, name)
	_ = os.WriteFile(path, []byte("written by somebody else"), 0o644)
	if err := f.svc.FetchNow(context.Background(), name); err == nil {
		t.Fatal("fetch-back succeeded over an existing file")
	}
	if b, _ := os.ReadFile(path); string(b) != "written by somebody else" {
		t.Fatalf("existing file overwritten: %q", b)
	}
	noTempFiles(t, f.dir)
}
