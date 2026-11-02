package offload

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/objstore"
)

// ---- 1. stuck state, alert hook, Retry now ----

func TestConsecutiveFailuresMakeAnItemStuckOnceAndRetryNowFreesIt(t *testing.T) {
	f := newFixture(t)
	id, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "flaky", Provider: "s3", Endpoint: "http://h.test",
		Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMirror, Enabled: true, MaxAttempts: 3})
	m := newMem()
	f.mems[id] = m
	m.putErr = &objstore.Error{Op: "PutObject", StatusCode: 503}
	var raised []StuckEvent
	f.svc.SetStuckHook(func(ev StuckEvent) { raised = append(raised, ev) })
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)

	for i := 0; i < 6; i++ {
		f.svc.Pass(context.Background())
		f.now = f.now.Add(2 * time.Hour) // past any backoff
	}
	it, _ := f.item(id, name)
	if it.State != db.OffloadStuck || it.Attempts != 3 {
		t.Fatalf("after 3 failures: %+v", it)
	}
	if m.puts != 3 {
		t.Fatalf("%d puts; a stuck item must not be retried automatically", m.puts)
	}
	if len(raised) != 1 || raised[0].RelPath != name || raised[0].TargetName != "flaky" || raised[0].Attempts != 3 {
		t.Fatalf("stuck raised %+v; want exactly once, on the transition", raised)
	}

	m.putErr = nil
	n, err := f.svc.Retry(id, "")
	if err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	f.svc.Pass(context.Background())
	if it, _ := f.item(id, name); it.State != db.OffloadVerified {
		t.Fatalf("after Retry now: %+v", it)
	}
}

func TestARefusedCredentialIsStuckAtOnce(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMirror, 0)
	f.mems[id].putErr = &objstore.Error{Op: "PutObject", StatusCode: 403, Code: "AccessDenied"}
	raised := 0
	f.svc.SetStuckHook(func(StuckEvent) { raised++ })
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if it, _ := f.item(id, name); it.State != db.OffloadStuck || it.Attempts != 1 || raised != 1 {
		t.Fatalf("%+v raised=%d", it, raised)
	}
	// Retry for one file, on every target.
	if n, _ := f.svc.Retry(0, name); n != 1 {
		t.Fatalf("per-file retry moved %d", n)
	}
	if it, _ := f.item(id, name); it.State != db.OffloadPending || it.Attempts != 0 {
		t.Fatalf("after retry %+v", it)
	}
}

// ---- 2. multipart ----

func bigBody(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return string(b)
}

func TestALargeFileGoesUpInPartsAndVerifiesTheCompositeETag(t *testing.T) {
	f := newFixture(t)
	f.svc.mpThreshold, f.svc.partSize, f.svc.partBackoff = 100, 40, time.Millisecond
	id := f.target(db.OffloadMove, 0)
	m := f.mems[id]
	m.partErrs[2] = 2 // part 2 fails twice, then lands: retried with backoff
	body := bigBody(130)
	name := f.segment(5*time.Hour, body, true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	it, _ := f.item(id, name)
	if it.State != db.OffloadVerified || !it.Evicted {
		t.Fatalf("%+v", it)
	}
	if string(m.objs[it.Key]) != body || !strings.HasSuffix(m.etags[it.Key], "-4") {
		t.Fatalf("object %d bytes etag %q", len(m.objs[it.Key]), m.etags[it.Key])
	}
	if m.puts != 0 || m.partPuts != 6 {
		t.Fatalf("single puts %d, part puts %d (4 parts + 2 retries)", m.puts, m.partPuts)
	}
	if rows, _ := f.store.ListOffloadMultipart(0); len(rows) != 0 {
		t.Fatalf("progress rows left after completion: %+v", rows)
	}
}

func TestAMultipartObjectWhoseBytesDifferIsNotVerified(t *testing.T) {
	f := newFixture(t)
	f.svc.mpThreshold, f.svc.partSize, f.svc.partBackoff = 100, 40, time.Millisecond
	id := f.target(db.OffloadMove, 0)
	f.mems[id].corrupt = true
	name := f.segment(5*time.Hour, bigBody(130), true)
	f.segment(time.Minute, "newest", true)
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return nonMD5ETag{f.mems[id]}, nil }
	tg, _ := f.store.GetStorageTarget(f.svc.box, id)
	tg.VerifyDownload = true
	tg.SecretAccessKey = ""
	_ = f.store.UpdateStorageTarget(f.svc.box, tg)
	f.svc.Pass(context.Background())
	if it, _ := f.item(id, name); it.State == db.OffloadVerified || !f.local(name) {
		t.Fatalf("corrupt multipart object verified: %+v", it)
	}
}

// A part failure ends the attempt with the finished parts recorded; the next
// attempt -- in a NEW service, as after a restart -- sends only the rest.
func TestAMultipartUploadResumesAcrossARestart(t *testing.T) {
	f := newFixture(t)
	f.svc.mpThreshold, f.svc.partSize, f.svc.partTries, f.svc.partBackoff = 100, 40, 1, time.Millisecond
	id := f.target(db.OffloadMirror, 0)
	m := f.mems[id]
	m.partErrs[3] = 1
	name := f.segment(5*time.Hour, bigBody(130), true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	rows, _ := f.store.ListOffloadMultipart(id)
	if len(rows) != 1 || len(rows[0].Parts) != 2 {
		t.Fatalf("progress after a failed part: %+v", rows)
	}
	sentBefore := m.partPuts

	again := New(slog.New(slog.NewTextHandler(io.Discard, nil)), f.store, f.svc.box, f.rec, nil,
		WithClock(func() time.Time { return f.now.Add(2 * time.Hour) }),
		WithClientFactory(func(db.StorageTarget) (Client, error) { return m, nil }),
		WithMultipart(100, 40), WithRetryPacing(time.Millisecond))
	again.Recover(context.Background())
	if rows, _ := f.store.ListOffloadMultipart(id); len(rows) != 1 {
		t.Fatal("the startup sweep aborted an upload it should resume")
	}
	again.Pass(context.Background())
	if it, _ := f.item(id, name); it.State != db.OffloadVerified {
		t.Fatalf("%+v", it)
	}
	if sent := m.partPuts - sentBefore; sent != 2 {
		t.Fatalf("resume sent %d parts; parts 1 and 2 were already there, so want 2", sent)
	}
	if len(m.aborted) != 0 {
		t.Fatalf("aborted %v", m.aborted)
	}
}

func TestGivingUpAbortsTheMultipartUpload(t *testing.T) {
	f := newFixture(t)
	f.svc.mpThreshold, f.svc.partSize, f.svc.partTries = 100, 40, 1
	id, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "x", Provider: "s3", Endpoint: "http://h.test",
		Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMirror, Enabled: true, MaxAttempts: 1})
	m := newMem()
	f.mems[id] = m
	m.partErrs[2] = 99
	f.segment(5*time.Hour, bigBody(130), true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if len(m.aborted) != 1 {
		t.Fatalf("aborted %v; a stuck multipart upload must be abandoned on the service", m.aborted)
	}
	if rows, _ := f.store.ListOffloadMultipart(0); len(rows) != 0 {
		t.Fatalf("rows %+v", rows)
	}
}

func TestAbortTargetAndTheStartupSweepAbandonOrphans(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMirror, 0)
	m := f.mems[id]
	tg, _ := f.store.GetStorageTarget(f.svc.box, id)
	// An orphan: its file is gone.
	upID, _ := m.CreateMultipart(context.Background(), "k/gone.mkv", nil)
	_ = f.store.PutOffloadMultipart(db.OffloadMultipart{TargetID: id, RelPath: "gone.mkv", Key: "k/gone.mkv", UploadID: upID, PartSize: 40, Bytes: 100, SHA256: "x"})
	f.svc.Recover(context.Background())
	if len(m.aborted) != 1 {
		t.Fatalf("startup sweep aborted %v", m.aborted)
	}
	upID2, _ := m.CreateMultipart(context.Background(), "k/live.mkv", nil)
	_ = f.store.PutOffloadMultipart(db.OffloadMultipart{TargetID: id, RelPath: "live.mkv", Key: "k/live.mkv", UploadID: upID2, PartSize: 40, Bytes: 100, SHA256: "x"})
	f.svc.AbortTarget(context.Background(), tg)
	if len(m.aborted) != 2 {
		t.Fatalf("AbortTarget aborted %v", m.aborted)
	}
}

// ---- 3. fetch-back retries, resumed by Range ----

func TestFetchBackResumesFromTheBytesAlreadyReceived(t *testing.T) {
	f := newFixture(t)
	f.svc.fetchBackoff = time.Millisecond
	id := f.target(db.OffloadMove, 0)
	m := f.mems[id]
	body := bigBody(90)
	name := f.segment(5*time.Hour, body, true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	m.getCuts = []int64{30, 25} // two dropped connections
	if err := f.svc.FetchNow(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, name)); string(b) != body {
		t.Fatalf("fetched %d bytes, not the original", len(b))
	}
	if len(m.getOffsets) != 3 || m.getOffsets[0] != 0 || m.getOffsets[1] != 30 || m.getOffsets[2] != 55 {
		t.Fatalf("offsets %v; each retry must resume where the last stopped", m.getOffsets)
	}
}

func TestFetchBackGivesUpAfterThreeAttemptsAndLeavesNothing(t *testing.T) {
	f := newFixture(t)
	f.svc.fetchBackoff = time.Millisecond
	id := f.target(db.OffloadMove, 0)
	m := f.mems[id]
	name := f.segment(5*time.Hour, bigBody(90), true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	m.getCuts = []int64{10, 10, 10, 10}
	err := f.svc.FetchNow(context.Background(), name)
	if err == nil || len(m.getOffsets) != 3 {
		t.Fatalf("err %v after %d attempts", err, len(m.getOffsets))
	}
	if f.local(name) {
		t.Fatal("a failed fetch landed")
	}
	noTempFiles(t, f.dir)
	// The async path reports it, and a second request may try again.
	done := make(chan struct{}, 4)
	f.svc.onChange = func() { done <- struct{}{} }
	m.getCuts = []int64{10, 10, 10}
	m.getOffsets = nil
	_, _ = f.svc.Fetch(name)
	<-done
	if st, _ := f.svc.FetchStatus(name); st.State != FetchFailed || st.Error == "" {
		t.Fatalf("%+v", st)
	}
	m.getCuts = nil
	if st, err := f.svc.Fetch(name); err != nil || st.State != FetchFetching {
		t.Fatalf("retry after failure: %+v %v", st, err)
	}
	<-done
	if !f.local(name) {
		t.Fatal("retry did not fetch")
	}
}

// ---- 4. restart safety ----

// blockingStore holds Put and GetFrom open until the context ends, the way a
// transfer in flight at shutdown is.
type blockingStore struct {
	*memStore
	started chan struct{}
	once    sync.Once
}

func (b *blockingStore) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sh, mb string, meta map[string]string) (string, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return "", ctx.Err()
}

type slowBody struct {
	ctx  context.Context
	sent bool
}

func (s *slowBody) Read(p []byte) (int, error) {
	if !s.sent {
		s.sent = true
		return copy(p, "partial"), nil
	}
	<-s.ctx.Done()
	return 0, s.ctx.Err()
}
func (s *slowBody) Close() error { return nil }

func (b *blockingStore) GetFrom(ctx context.Context, key string, off int64) (io.ReadCloser, objstore.ObjectInfo, bool, error) {
	_, info, _, err := b.memStore.GetFrom(ctx, key, off)
	b.once.Do(func() { close(b.started) })
	return &slowBody{ctx: ctx}, info, false, err
}

func TestAnUploadCutOffByShutdownComesBackRetryable(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMove, 0)
	bs := &blockingStore{memStore: f.mems[id], started: make(chan struct{})}
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return bs, nil }
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.svc.Pass(ctx); close(done) }()
	<-bs.started
	cancel()
	<-done
	it, _ := f.item(id, name)
	if it.State != db.OffloadFailed || it.Attempts != 0 || !it.NextAttemptAt.IsZero() || !f.local(name) {
		t.Fatalf("after shutdown mid-upload: %+v local=%v", it, f.local(name))
	}

	// A process that died without even that write leaves "uploading"; the
	// next one's startup recovery makes it retryable and the pass sends it.
	it.State = db.OffloadUploading
	_ = f.store.PutOffloadItem(it)
	next := New(slog.New(slog.NewTextHandler(io.Discard, nil)), f.store, f.svc.box, f.rec, nil,
		WithClock(func() time.Time { return f.now }),
		WithClientFactory(func(db.StorageTarget) (Client, error) { return f.mems[id], nil }))
	next.Recover(context.Background())
	if it, _ := f.item(id, name); it.State != db.OffloadFailed || !it.NextAttemptAt.IsZero() {
		t.Fatalf("recovered %+v", it)
	}
	next.Pass(context.Background())
	if it, _ := f.item(id, name); it.State != db.OffloadVerified {
		t.Fatalf("after recovery pass %+v", it)
	}
}

func TestAFetchCutOffByShutdownNeverWritesTheFinalPath(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMove, 0)
	name := f.segment(5*time.Hour, "footage to clip", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	bs := &blockingStore{memStore: f.mems[id], started: make(chan struct{})}
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return bs, nil }
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- f.svc.FetchNow(ctx, name) }()
	<-bs.started
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if f.local(name) {
		t.Fatal("a cancelled fetch left a file under the final name")
	}
	noTempFiles(t, f.dir)
	if !f.svc.Evicted(name) {
		t.Fatal("a cancelled fetch cleared the evicted mark")
	}
	// A temp file a killed process left behind is swept at startup.
	_ = os.WriteFile(filepath.Join(f.dir, ".offload-fetch-123"), bytes.Repeat([]byte("x"), 10), 0o644)
	f.svc.Recover(context.Background())
	noTempFiles(t, f.dir)
}
