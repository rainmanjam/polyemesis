package offload

import (
	"context"
	"io"
	"log/slog"
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
		Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Enabled: true, MaxAttempts: 3})
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
	// Retry kicked a pass of its own; a Pass called while it runs defers to
	// it and returns at once, so wait for the kick before reading the row.
	f.settle()
	if it, _ := f.item(id, name); it.State != db.OffloadVerified {
		t.Fatalf("after Retry now: %+v", it)
	}
}

func TestARefusedCredentialIsStuckAtOnce(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	f.mems[id].putErr = &objstore.Error{Op: "PutObject", StatusCode: 403, Code: "AccessDenied"}
	raised := 0
	f.svc.SetStuckHook(func(StuckEvent) { raised++ })
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if it, _ := f.item(id, name); it.State != db.OffloadStuck || it.Attempts != 1 || raised != 1 {
		t.Fatalf("%+v raised=%d", it, raised)
	}
	// Retry for one file, on every target. A cancelled base context parks the
	// pass Retry kicks, so the reset row is read before anything re-attempts it.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	f.svc.baseCtx = cancelled
	if n, _ := f.svc.Retry(0, name); n != 1 {
		t.Fatalf("per-file retry moved %d", n)
	}
	if it, _ := f.item(id, name); it.State != db.OffloadPending || it.Attempts != 0 {
		t.Fatalf("after retry %+v", it)
	}
	f.settle()
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
	id := f.target()
	m := f.mems[id]
	m.partErrs[2] = 2 // part 2 fails twice, then lands: retried with backoff
	body := bigBody(130)
	name := f.segment(5*time.Hour, body, true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	it, _ := f.item(id, name)
	if it.State != db.OffloadVerified || !f.local(name) {
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
	id := f.target()
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
	id := f.target()
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
		Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Enabled: true, MaxAttempts: 1})
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
	id := f.target()
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

// Probe for the flake: Retry's kick pass is asynchronous. While it is running,
// a second Pass is deferred to it (documented on Pass), not run inline, so
// returning from Pass proves nothing until the kick has drained.
func TestPassWhileTheKickPassRunsDefersToItAndSettleWaitsForIt(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	m := f.mems[id]
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	if err := f.store.PutOffloadItem(db.OffloadItem{TargetID: id, RelPath: name, Kind: db.OffloadKindRecording,
		Key: "polyemesis/recordings/x/" + name, State: db.OffloadStuck, Attempts: 3}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.svc.newClient = func(db.StorageTarget) (Client, error) {
		once.Do(func() { close(entered); <-release })
		return m, nil
	}
	if n, err := f.svc.Retry(id, ""); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	<-entered // the kick pass now holds the pass lock, parked before any upload
	// A file that appears now was not in the kick pass's scan: only the extra
	// round the deferred Pass asked for can find it.
	late := f.segment(6*time.Hour, "late footage", true)
	f.svc.Pass(context.Background())
	if it, _ := f.item(id, name); it.State != db.OffloadPending || it.Attempts != 0 {
		t.Fatalf("a Pass during the kick pass must defer to it: %+v", it)
	}
	settled := make(chan struct{})
	go func() { f.settle(); close(settled) }()
	select {
	case <-settled:
		t.Fatal("settle returned while the kick pass was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-settled
	for _, n := range []string{name, late} {
		if it, _ := f.item(id, n); it.State != db.OffloadVerified {
			t.Fatalf("the deferred ask was dropped for %s: %+v", n, it)
		}
	}
}
