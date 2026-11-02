package offload

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/objstore"
	"github.com/rainmanjam/polyemesis/internal/recording"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// Obviously fake, low-entropy credentials (gitleaks scans every commit).
const (
	fakeKeyID  = "fake-access-key"
	fakeSecret = "fake-secret-value-not-real"
)

// memStore is an in-memory bucket that can be told to misbehave.
type memStore struct {
	mu      sync.Mutex
	objs    map[string][]byte
	meta    map[string]map[string]string
	putErr  error
	headErr error
	// corrupt flips a byte of every stored object, so the stored copy is not
	// the bytes that were sent -- what verification exists to catch.
	corrupt bool
	// lieETag reports a plausible-looking MD5 ETag that is not the body's.
	lieETag bool
	puts    int
}

func newMem() *memStore {
	return &memStore{objs: map[string][]byte{}, meta: map[string]map[string]string{}}
}

func (m *memStore) Put(_ context.Context, key string, body io.ReadSeeker, size int64, sh, mb string, meta map[string]string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	if m.putErr != nil {
		return "", m.putErr
	}
	b, _ := io.ReadAll(body)
	if int64(len(b)) != size {
		return "", errors.New("short body")
	}
	if m.corrupt && len(b) > 0 {
		b = append([]byte(nil), b...)
		b[0] ^= 0xff
	}
	m.objs[key] = b
	m.meta[key] = meta
	return m.etag(key), nil
}

func (m *memStore) etag(key string) string {
	if m.lieETag {
		return strings.Repeat("ab", 16)
	}
	s := md5.Sum(m.objs[key])
	return hex.EncodeToString(s[:])
}

func (m *memStore) Head(_ context.Context, key string) (objstore.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.headErr != nil {
		return objstore.ObjectInfo{}, m.headErr
	}
	b, ok := m.objs[key]
	if !ok {
		return objstore.ObjectInfo{}, &objstore.Error{Op: "HeadObject", StatusCode: 404}
	}
	return objstore.ObjectInfo{Size: int64(len(b)), ETag: m.etag(key), Meta: m.meta[key]}, nil
}

func (m *memStore) Get(_ context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, objstore.ObjectInfo{}, &objstore.Error{Op: "GetObject", StatusCode: 404}
	}
	return io.NopCloser(bytes.NewReader(b)), objstore.ObjectInfo{Size: int64(len(b))}, nil
}

func (m *memStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}

type fixture struct {
	t     *testing.T
	store *db.DB
	rec   *recording.Manager
	dir   string
	svc   *Service
	now   time.Time
	mems  map[int64]*memStore
	logs  *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := dbtest.Open(t)
	dir := t.TempDir()
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	f := &fixture{t: t, store: store, dir: dir, now: time.Now(), mems: map[int64]*memStore{}, logs: logs}
	f.rec = recording.New(log, store, dir, nil)
	f.svc = New(log, store, box, f.rec, nil,
		WithClock(func() time.Time { return f.now }),
		WithClientFactory(func(tg db.StorageTarget) (Client, error) {
			m, ok := f.mems[tg.ID]
			if !ok {
				m = newMem()
				f.mems[tg.ID] = m
			}
			return m, nil
		}))
	return f
}

func (f *fixture) target(mode string, keep int) int64 {
	f.t.Helper()
	id, err := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "t" + mode, Provider: "minio", Endpoint: "http://h.test",
		Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: mode, KeepLocalHours: keep, Enabled: true})
	if err != nil {
		f.t.Fatal(err)
	}
	f.mems[id] = newMem()
	return id
}

// segment writes a finished, measured recording that started age ago and was
// last modified well outside the settle window.
func (f *fixture) segment(age time.Duration, body string, measured bool) string {
	f.t.Helper()
	at := f.now.Add(-age)
	name := "rec-" + at.Format("20060102-150405") + ".mkv"
	path := filepath.Join(f.dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	mt := f.now.Add(-age + time.Minute)
	if mt.After(f.now) {
		mt = f.now
	}
	if err := os.Chtimes(path, mt, mt); err != nil {
		f.t.Fatal(err)
	}
	dur := int64(0)
	if measured {
		dur = 60000
	}
	if err := f.store.UpsertRecording(&db.Recording{Filename: name, StartedAt: at, Bytes: int64(len(body)), DurationMS: dur}); err != nil {
		f.t.Fatal(err)
	}
	return name
}

func (f *fixture) item(target int64, rel string) (db.OffloadItem, bool) {
	f.t.Helper()
	its, err := f.store.OffloadItemsFor(rel)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, it := range its {
		if it.TargetID == target {
			return it, true
		}
	}
	return db.OffloadItem{}, false
}

func (f *fixture) local(name string) bool {
	_, err := os.Stat(filepath.Join(f.dir, name))
	return err == nil
}

func TestMirrorUploadsAFinishedSegmentVerifiesItAndKeepsTheLocalFile(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMirror, 0)
	old := f.segment(5*time.Hour, "old segment", true)
	f.segment(time.Minute, "newest, still being written", true)
	f.svc.Pass(context.Background())

	it, ok := f.item(id, old)
	if !ok || it.State != db.OffloadVerified {
		t.Fatalf("item %+v ok=%v, want verified", it, ok)
	}
	if !strings.HasPrefix(it.Key, "polyemesis/recordings/") || !strings.HasSuffix(it.Key, "/"+old) {
		t.Fatalf("key %q", it.Key)
	}
	if got := f.mems[id].objs[it.Key]; string(got) != "old segment" {
		t.Fatalf("stored %q", got)
	}
	if !f.local(old) {
		t.Fatal("mirror mode deleted the local file")
	}
}

func TestTheFileBeingWrittenIsNeverUploaded(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMirror, 0)
	f.segment(10*time.Hour, "finished", true)
	unmeasured := f.segment(5*time.Hour, "not yet probed", false)
	newest := f.segment(3*time.Hour, "newest by start time", true)
	f.svc.Pass(context.Background())
	for _, name := range []string{unmeasured, newest} {
		if _, ok := f.item(id, name); ok {
			t.Errorf("%s was offered for upload", name)
		}
	}
	// And a finished segment touched inside the settle window waits.
	recent := f.segment(8*time.Hour, "just rewritten", true)
	_ = os.Chtimes(filepath.Join(f.dir, recent), f.now, f.now)
	f.svc.Pass(context.Background())
	if _, ok := f.item(id, recent); ok {
		t.Error("a file modified inside the settle window was uploaded")
	}
}

func TestAChecksumMismatchIsAFailureAndNeverEvicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*memStore)
	}{
		{"stored bytes differ", func(m *memStore) { m.corrupt = true }},
		{"etag is not the md5 sent", func(m *memStore) { m.lieETag = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			id := f.target(db.OffloadMove, 0)
			tc.set(f.mems[id])
			name := f.segment(5*time.Hour, "precious footage", true)
			f.segment(time.Minute, "newest", true)
			f.svc.Pass(context.Background())
			it, ok := f.item(id, name)
			if !ok || it.State != db.OffloadFailed {
				t.Fatalf("state %+v, want failed", it)
			}
			if !strings.Contains(it.LastError, "checksum mismatch") {
				t.Fatalf("error %q does not name the mismatch", it.LastError)
			}
			if !it.NextAttemptAt.After(f.now) {
				t.Fatal("no retry was scheduled")
			}
			if !f.local(name) || it.Evicted {
				t.Fatal("a recording whose upload did not verify was deleted")
			}
		})
	}
}

func TestADownloadVerifyCatchesAStoreThatLiesInHead(t *testing.T) {
	f := newFixture(t)
	id, err := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "dl", Provider: "s3", Endpoint: "http://h.test", Bucket: "b",
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMove, VerifyDownload: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m := newMem()
	f.mems[id] = m
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	// Head reports the right size and an ETag that is not hex MD5 (as SSE-KMS
	// targets do), so only the read-back can see the corruption.
	m.corrupt = true
	m.lieETag = false
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return nonMD5ETag{m}, nil }
	f.svc.Pass(context.Background())
	it, _ := f.item(id, name)
	if it.State != db.OffloadFailed || !f.local(name) {
		t.Fatalf("state %s local=%v; a corrupt copy passed download verification", it.State, f.local(name))
	}
}

type nonMD5ETag struct{ *memStore }

func (n nonMD5ETag) Head(ctx context.Context, key string) (objstore.ObjectInfo, error) {
	info, err := n.memStore.Head(ctx, key)
	info.ETag = "kms-opaque-etag"
	delete(info.Meta, "sha256")
	return info, err
}

func TestMoveModeDeletesTheLocalFileOnlyAfterAVerifiedUpload(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMove, 0)
	f.mems[id].putErr = &objstore.Error{Op: "PutObject", StatusCode: 503}
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)

	f.svc.Pass(context.Background())
	if !f.local(name) {
		t.Fatal("deleted before any upload succeeded")
	}
	it, _ := f.item(id, name)
	if it.State != db.OffloadFailed || it.Attempts != 1 {
		t.Fatalf("%+v", it)
	}

	// Not retried before its backoff elapses.
	f.mems[id].putErr = nil
	f.svc.Pass(context.Background())
	if f.mems[id].puts != 1 {
		t.Fatalf("retried before the backoff: %d puts", f.mems[id].puts)
	}

	f.now = f.now.Add(2 * time.Hour)
	f.svc.Pass(context.Background())
	it, _ = f.item(id, name)
	if it.State != db.OffloadVerified || !it.Evicted {
		t.Fatalf("after retry %+v", it)
	}
	if f.local(name) {
		t.Fatal("verified move-mode upload left the local file")
	}
	// The Library row survives the next scan.
	if _, err := f.rec.Scan(); err != nil {
		t.Fatal(err)
	}
	recs, _ := f.store.ListRecordings()
	found := false
	for _, r := range recs {
		found = found || r.Filename == name
	}
	if !found {
		t.Fatal("the evicted recording dropped out of the index")
	}
}

func TestMoveModeKeepsTheLastNHoursLocal(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMove, 6)
	young := f.segment(4*time.Hour, "young", true)
	old := f.segment(9*time.Hour, "old", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	for _, n := range []string{young, old} {
		if it, _ := f.item(id, n); it.State != db.OffloadVerified {
			t.Fatalf("%s not verified: %+v", n, it)
		}
	}
	if !f.local(young) {
		t.Error("a segment inside keepLocalHours was evicted")
	}
	if f.local(old) {
		t.Error("a segment past keepLocalHours stayed local")
	}
}

func TestMoveWaitsForEveryEnabledTarget(t *testing.T) {
	f := newFixture(t)
	f.target(db.OffloadMove, 0)
	mirror := f.target(db.OffloadMirror, 0)
	f.mems[mirror].putErr = &objstore.Error{Op: "PutObject", StatusCode: 500}
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if !f.local(name) {
		t.Fatal("evicted while one enabled target had no verified copy")
	}
}

func TestAMissingRemoteCopyAtEvictionTimeKeepsTheFile(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMove, 6)
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background()) // uploads, inside keep window
	it, _ := f.item(id, name)
	if it.State != db.OffloadVerified || !f.local(name) {
		t.Fatalf("setup: %+v", it)
	}
	// Someone emptied the bucket; then the keep window passes.
	f.mems[id].objs = map[string][]byte{}
	f.now = f.now.Add(2 * time.Hour)
	f.svc.Pass(context.Background())
	if !f.local(name) {
		t.Fatal("evicted although the remote copy was gone")
	}
}

func TestFetchBackRestoresTheVerifiedBytesAndPinsThemLocal(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMove, 0)
	name := f.segment(5*time.Hour, "footage to clip", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if f.local(name) || !f.svc.Evicted(name) {
		t.Fatal("setup: not evicted")
	}
	if err := f.svc.FetchNow(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil || string(b) != "footage to clip" {
		t.Fatalf("fetched %q %v", b, err)
	}
	if f.svc.Evicted(name) {
		t.Fatal("still marked evicted after fetch")
	}
	// Pinned: the next pass does not evict it again straight away.
	f.svc.Pass(context.Background())
	if !f.local(name) {
		t.Fatal("re-evicted a freshly fetched recording")
	}
	it, _ := f.item(id, name)
	if it.FetchedAt.IsZero() {
		t.Fatal("fetch time not recorded")
	}
}

func TestFetchBackRefusesACorruptCopy(t *testing.T) {
	f := newFixture(t)
	id := f.target(db.OffloadMove, 0)
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	it, _ := f.item(id, name)
	f.mems[id].objs[it.Key] = []byte("tampered")
	err := f.svc.FetchNow(context.Background(), name)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err %v, want checksum mismatch", err)
	}
	if f.local(name) {
		t.Fatal("a corrupt fetch was renamed into place")
	}
	entries, _ := os.ReadDir(f.dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".offload-fetch-") {
			t.Fatalf("temporary file %s left behind", e.Name())
		}
	}
}

func TestFetchIsAsyncAndReportsDone(t *testing.T) {
	f := newFixture(t)
	f.target(db.OffloadMove, 0)
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	done := make(chan struct{}, 1)
	f.svc.onChange = func() { done <- struct{}{} }
	st, err := f.svc.Fetch(name)
	if err != nil || st.State != FetchFetching {
		t.Fatalf("%+v %v", st, err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("fetch never finished")
	}
	if st, _ := f.svc.FetchStatus(name); st.State != FetchDone {
		t.Fatalf("%+v", st)
	}
	if _, err := f.svc.Fetch(name); !errors.Is(err, ErrNotEvicted) {
		t.Fatalf("fetch of a local file: %v", err)
	}
}

func TestExtrasFollowThePerTargetToggles(t *testing.T) {
	f := newFixture(t)
	plain := f.target(db.OffloadMirror, 0)
	all, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "all", Provider: "r2", Endpoint: "http://h.test", Bucket: "b",
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMirror, Enabled: true,
		OffloadStems: true, OffloadTranscripts: true, OffloadClips: true})
	f.mems[all] = newMem()
	name := f.segment(5*time.Hour, "master", true)
	f.segment(time.Minute, "newest", true)
	base := strings.TrimSuffix(name, ".mkv")
	old := f.now.Add(-time.Hour)
	for _, p := range []string{"stems/" + base + "-mic.flac", "transcripts/" + base + ".json", "clips/clip-20261101-120000.ts"} {
		full := filepath.Join(f.dir, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(full, old, old)
	}
	f.svc.Pass(context.Background())
	for _, p := range []string{"stems/" + base + "-mic.flac", "transcripts/" + base + ".json", "clips/clip-20261101-120000.ts"} {
		if _, ok := f.item(plain, p); ok {
			t.Errorf("%s offloaded to a target that did not ask for it", p)
		}
		if it, ok := f.item(all, p); !ok || it.State != db.OffloadVerified {
			t.Errorf("%s not offloaded to the target that asked: %+v", p, it)
		}
	}
	if it, _ := f.item(all, "stems/"+base+"-mic.flac"); !strings.Contains(it.Key, "/stems/") {
		t.Errorf("stem key %q", it.Key)
	}
}

// Credentials never reach a log line, an item's stored error, or the probe's
// report -- driven through the real SigV4 client against a server that
// refuses every request.
func TestCredentialsNeverReachLogsOrStoredErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `<Error><Code>InvalidAccessKeyId</Code><Message>The key you gave is not known</Message></Error>`)
	}))
	defer srv.Close()
	f := newFixture(t)
	id, err := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "real", Provider: "minio", Endpoint: srv.URL, Bucket: "b",
		PathStyle: true, AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Mode: db.OffloadMove, Enabled: true,
		AllowPrivateTarget: true})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.newClient = NewClient
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	it, _ := f.item(id, name)
	if it.State != db.OffloadFailed || !strings.Contains(it.LastError, "InvalidAccessKeyId") {
		t.Fatalf("%+v", it)
	}
	tg, _ := f.store.GetStorageTarget(f.svc.box, id)
	res := Probe(context.Background(), tg, nil)
	if res.OK || res.FailedStep != "put" {
		t.Fatalf("probe %+v", res)
	}
	for where, s := range map[string]string{"log": f.logs.String(), "item": it.LastError, "probe": res.Steps[0].Error} {
		if strings.Contains(s, fakeSecret) {
			t.Errorf("secret leaked into %s: %q", where, s)
		}
	}
	if !f.local(name) {
		t.Fatal("refused upload evicted the file")
	}
}

func TestTheSSRFGuardRefusesALoopbackTargetUnlessItOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	tg := db.StorageTarget{Endpoint: srv.URL, Bucket: "b", PathStyle: true, AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret}
	res := Probe(context.Background(), tg, nil)
	if res.OK || res.FailedStep != "put" || !strings.Contains(res.Steps[0].Error, "non-public") {
		t.Fatalf("loopback reached without opt-in: %+v", res)
	}
	tg.AllowPrivateTarget = true
	if res := Probe(context.Background(), tg, nil); res.FailedStep == "put" {
		t.Fatalf("opted-in target refused: %+v", res)
	}
}

func TestProbeNamesTheStepThatFailed(t *testing.T) {
	tg := db.StorageTarget{Name: "p"}
	m := newMem()
	ok := Probe(context.Background(), tg, func(db.StorageTarget) (Client, error) { return m, nil })
	if !ok.OK || len(ok.Steps) != 3 || len(m.objs) != 0 {
		t.Fatalf("%+v objs=%d", ok, len(m.objs))
	}
	m.corrupt = true
	bad := Probe(context.Background(), tg, func(db.StorageTarget) (Client, error) { return m, nil })
	if bad.FailedStep != "get" || !bad.Steps[2].OK {
		t.Fatalf("%+v (delete must still clean up after a failed get)", bad)
	}
	m.corrupt = false
	m.putErr = errors.New("denied")
	p := Probe(context.Background(), tg, func(db.StorageTarget) (Client, error) { return m, nil })
	if p.FailedStep != "put" || !p.Steps[1].Skipped || !p.Steps[2].Skipped {
		t.Fatalf("%+v", p)
	}
}

func TestPrefixTemplates(t *testing.T) {
	src := int64(3)
	at := time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC)
	k, err := ObjectKey("vods/{source}/{yyyy}-{mm}-{dd}/{kind}", db.OffloadKindRecording, &src, at, "rec.mkv")
	if err != nil || k != "vods/s3/2026-11-01/recordings/rec.mkv" {
		t.Fatalf("%q %v", k, err)
	}
	if k, _ := ObjectKey("", db.OffloadKindStem, nil, at, "a.flac"); k != "polyemesis/stems/2026/11/01/a.flac" {
		t.Fatalf("default %q", k)
	}
	for _, bad := range []string{"/abs", "a/../b", "{nope}", "a b", "{kind", "x}"} {
		if ValidatePrefixTemplate(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEvictableRule(t *testing.T) {
	now := time.Now()
	v := db.OffloadItem{State: db.OffloadVerified, SHA256: "x", Bytes: 10, VerifiedAt: now.Add(-time.Hour)}
	if !Evictable(v, 10, now.Add(-2*time.Hour), now) {
		t.Fatal("a verified, unchanged item is evictable")
	}
	cases := map[string]func(db.OffloadItem) (db.OffloadItem, int64, time.Time){
		"failed": func(i db.OffloadItem) (db.OffloadItem, int64, time.Time) {
			i.State = db.OffloadFailed
			return i, 10, now.Add(-2 * time.Hour)
		},
		"uploading": func(i db.OffloadItem) (db.OffloadItem, int64, time.Time) {
			i.State = db.OffloadUploading
			return i, 10, now.Add(-2 * time.Hour)
		},
		"size changed":  func(i db.OffloadItem) (db.OffloadItem, int64, time.Time) { return i, 11, now.Add(-2 * time.Hour) },
		"touched after": func(i db.OffloadItem) (db.OffloadItem, int64, time.Time) { return i, 10, now },
		"no digest": func(i db.OffloadItem) (db.OffloadItem, int64, time.Time) {
			i.SHA256 = ""
			return i, 10, now.Add(-2 * time.Hour)
		},
		"recently fetched": func(i db.OffloadItem) (db.OffloadItem, int64, time.Time) {
			i.FetchedAt = now.Add(-time.Hour)
			return i, 10, now.Add(-2 * time.Hour)
		},
	}
	for name, mut := range cases {
		it, size, mt := mut(v)
		if Evictable(it, size, mt, now) {
			t.Errorf("%s: evictable", name)
		}
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	if d := backoff(1, true); d < backoffBase || d > backoffBase*6/5+time.Second {
		t.Fatalf("first %v", d)
	}
	if d := backoff(4, true); d < 4*backoffBase {
		t.Fatalf("fourth %v", d)
	}
	if d := backoff(40, true); d > backoffCap*6/5+time.Second {
		t.Fatalf("capped %v", d)
	}
	if backoff(1, false) != backoffCap {
		t.Fatal("a permanent refusal waits the cap")
	}
	_ = base64.StdEncoding
}
