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
	"strconv"
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
	mu    sync.Mutex
	objs  map[string][]byte
	etags map[string]string
	meta  map[string]map[string]string
	// multipart
	uploads  map[string]map[int][]byte
	nextID   int
	partErrs map[int]int // part number -> failures still to inject
	partPuts int
	aborted  []string
	// getCuts makes the next GetFrom calls stop after this many bytes with an
	// error; getOffsets records the offset each GetFrom asked for.
	getCuts    []int64
	getOffsets []int64
	putErr     error
	headErr    error
	// corrupt flips a byte of every stored object, so the stored copy is not
	// the bytes that were sent -- what verification exists to catch.
	corrupt bool
	// lieETag reports a plausible-looking MD5 ETag that is not the body's.
	lieETag bool
	puts    int
}

func newMem() *memStore {
	return &memStore{objs: map[string][]byte{}, meta: map[string]map[string]string{}, etags: map[string]string{},
		uploads: map[string]map[int][]byte{}, partErrs: map[int]int{}}
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
	delete(m.etags, key)
	return m.etag(key), nil
}

func (m *memStore) etag(key string) string {
	if m.lieETag {
		return strings.Repeat("ab", 16)
	}
	if e, ok := m.etags[key]; ok {
		return e
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

type cutReader struct {
	r    io.Reader
	left int64
}

func (c *cutReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errors.New("connection reset by the fake")
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func (m *memStore) GetFrom(_ context.Context, key string, off int64) (io.ReadCloser, objstore.ObjectInfo, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getOffsets = append(m.getOffsets, off)
	b, ok := m.objs[key]
	if !ok {
		return nil, objstore.ObjectInfo{}, false, &objstore.Error{Op: "GetObject", StatusCode: 404}
	}
	if off > 0 && off >= int64(len(b)) {
		return nil, objstore.ObjectInfo{}, false, &objstore.Error{Op: "GetObject", StatusCode: 416, Code: "InvalidRange"}
	}
	var r io.Reader = bytes.NewReader(b[off:])
	if len(m.getCuts) > 0 {
		r = &cutReader{r: r, left: m.getCuts[0]}
		m.getCuts = m.getCuts[1:]
	}
	return io.NopCloser(r), objstore.ObjectInfo{Size: int64(len(b))}, off > 0, nil
}

func (m *memStore) CreateMultipart(_ context.Context, key string, meta map[string]string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := key + "#" + strconv.Itoa(m.nextID)
	m.uploads[id] = map[int][]byte{}
	m.meta[key] = meta
	return id, nil
}

func (m *memStore) UploadPart(_ context.Context, _ string, id string, n int, body io.ReadSeeker, size int64, _, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.partPuts++
	if m.partErrs[n] > 0 {
		m.partErrs[n]--
		return "", &objstore.Error{Op: "UploadPart", StatusCode: 503}
	}
	up, ok := m.uploads[id]
	if !ok {
		return "", &objstore.Error{Op: "UploadPart", StatusCode: 404, Code: "NoSuchUpload"}
	}
	b, _ := io.ReadAll(body)
	if int64(len(b)) != size {
		return "", errors.New("short part")
	}
	up[n] = b
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:]), nil
}

func (m *memStore) CompleteMultipart(_ context.Context, key, id string, parts []objstore.Part) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	up, ok := m.uploads[id]
	if !ok {
		return "", &objstore.Error{Op: "CompleteMultipartUpload", StatusCode: 404, Code: "NoSuchUpload"}
	}
	var all []byte
	h := md5.New()
	for _, p := range parts {
		b := up[p.Number]
		all = append(all, b...)
		sum := md5.Sum(b)
		h.Write(sum[:])
	}
	if m.corrupt && len(all) > 0 {
		all[0] ^= 0xff
	}
	m.objs[key] = all
	m.etags[key] = hex.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(parts))
	delete(m.uploads, id)
	return m.etags[key], nil
}

func (m *memStore) AbortMultipart(_ context.Context, _ string, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aborted = append(m.aborted, id)
	delete(m.uploads, id)
	return nil
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
	n     int
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

func (f *fixture) target() int64 {
	f.t.Helper()
	f.n++
	id, err := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "t" + strconv.Itoa(f.n), Provider: "minio",
		Endpoint: "http://h.test", Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Enabled: true})
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
	id := f.target()
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
	id := f.target()
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

func TestAChecksumMismatchIsAFailureAndIsRetried(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*memStore)
	}{
		{"stored bytes differ", func(m *memStore) { m.corrupt = true }},
		{"etag is not the md5 sent", func(m *memStore) { m.lieETag = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			id := f.target()
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
			if !f.local(name) {
				t.Fatal("a recording whose upload did not verify was deleted")
			}
		})
	}
}

func TestADownloadVerifyCatchesAStoreThatLiesInHead(t *testing.T) {
	f := newFixture(t)
	id, err := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "dl", Provider: "s3", Endpoint: "http://h.test", Bucket: "b",
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, VerifyDownload: true, Enabled: true})
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

func TestExtrasFollowThePerTargetToggles(t *testing.T) {
	f := newFixture(t)
	plain := f.target()
	all, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "all", Provider: "r2", Endpoint: "http://h.test", Bucket: "b",
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Enabled: true,
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
		PathStyle: true, AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Enabled: true,
		AllowPrivateTarget: true})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.newClient = NewClient
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	it, _ := f.item(id, name)
	if it.State != db.OffloadStuck || !strings.Contains(it.LastError, "InvalidAccessKeyId") {
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
		t.Fatal("a refused upload touched the local file")
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

func TestBackoffGrowsAndCaps(t *testing.T) {
	if d := retryDelay(1, true); d < backoffBase || d > backoffBase*6/5+time.Second {
		t.Fatalf("first %v", d)
	}
	if d := retryDelay(4, true); d < 4*backoffBase {
		t.Fatalf("fourth %v", d)
	}
	if d := retryDelay(40, true); d > backoffCap*6/5+time.Second {
		t.Fatalf("capped %v", d)
	}
	if retryDelay(1, false) != backoffCap {
		t.Fatal("a permanent refusal waits the cap")
	}
	_ = base64.StdEncoding
}

// settle waits for the passes Kick started (Retry now starts one), so a test
// reads the outcome of the pass rather than racing it.
func (f *fixture) settle() { f.svc.kicks.Wait() }

// An unmounted recordings volume made every stat say "not exist", and prune
// wiped the whole ledger: a full re-upload when the volume returned, and the
// failed/stuck state Retry acts on was gone.
func TestPruneKeepsTheLedgerWhenTheVolumeIsGone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		unmnt func(f *fixture, names []string)
	}{
		{"directory missing", func(f *fixture, _ []string) {
			if err := os.Rename(f.dir, f.dir+".away"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Rename(f.dir+".away", f.dir) })
		}},
		// The mountpoint left behind, with a live recorder writing a new
		// segment into it: not empty, and still not the volume.
		{"bare mountpoint with a live segment", func(f *fixture, _ []string) {
			entries, err := os.ReadDir(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if err := os.RemoveAll(filepath.Join(f.dir, e.Name())); err != nil {
					t.Fatal(err)
				}
			}
			f.segment(30*time.Second, "written into the bare mountpoint", true)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			id := f.target()
			names := []string{
				f.segment(5*time.Hour, "first", true),
				f.segment(4*time.Hour, "second", true),
			}
			f.segment(time.Minute, "live", true)
			f.svc.Pass(context.Background())
			// A second pass, as the next tick would: the ledger now tracks files that
			// are all present, which is what confirms the volume (HoldForMissing).
			f.svc.Pass(context.Background())
			for _, n := range names {
				if _, ok := f.item(id, n); !ok {
					t.Fatalf("%s not tracked after the first pass", n)
				}
			}
			tc.unmnt(f, names)
			f.svc.Pass(context.Background())
			for _, n := range names {
				if _, ok := f.item(id, n); !ok {
					t.Errorf("%s: ledger row pruned while the volume was gone", n)
				}
			}
		})
	}
}

// One file removed out of several is an ordinary delete, and its row goes.
func TestPruneStillDropsASingleRemovedFile(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	var names []string
	for i := 5; i >= 2; i-- {
		names = append(names, f.segment(time.Duration(i)*time.Hour, "seg"+strconv.Itoa(i), true))
	}
	f.segment(time.Minute, "live", true)
	f.svc.Pass(context.Background())
	// A second pass, as the next tick would: the ledger now tracks files that
	// are all present, which is what confirms the volume (HoldForMissing).
	f.svc.Pass(context.Background())
	if err := os.Remove(filepath.Join(f.dir, names[0])); err != nil {
		t.Fatal(err)
	}
	f.svc.Pass(context.Background())
	if _, ok := f.item(id, names[0]); ok {
		t.Errorf("row for removed %s kept", names[0])
	}
	if _, ok := f.item(id, names[1]); !ok {
		t.Errorf("row for %s dropped", names[1])
	}
}

// The hold must not latch: on a confirmed volume, every tracked file going
// (retention can take them all) prunes every row.
func TestPruneDropsEverythingGoneFromAConfirmedVolume(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	names := []string{f.segment(5*time.Hour, "a", true), f.segment(4*time.Hour, "b", true)}
	f.segment(time.Minute, "live", true)
	f.svc.Pass(context.Background())
	// A second pass, as the next tick would: the ledger now tracks files that
	// are all present, which is what confirms the volume (HoldForMissing).
	f.svc.Pass(context.Background())
	for _, n := range names {
		if err := os.Remove(filepath.Join(f.dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.Pass(context.Background())
	for _, n := range names {
		if _, ok := f.item(id, n); ok {
			t.Errorf("row for removed %s kept: the hold latched on a confirmed volume", n)
		}
	}
}

// due decides, per known item, whether this pass attempts it: verified and
// unchanged, stuck, or failed and still backing off are left alone; a verified
// file that changed since is reset to pending and sent again.
func TestDueSkipsSettledItemsAndResendsAChangedFile(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &Service{now: func() time.Time { return now }}
	cand := candidate{size: 100, mtime: now.Add(-time.Hour)}
	cases := []struct {
		name      string
		it        db.OffloadItem
		cand      candidate
		want      bool
		wantState string
	}{
		{"verified unchanged", db.OffloadItem{State: db.OffloadVerified, Bytes: 100, VerifiedAt: now}, cand, false, db.OffloadVerified},
		{"verified, size changed", db.OffloadItem{State: db.OffloadVerified, Bytes: 99, VerifiedAt: now, Attempts: 3}, cand, true, db.OffloadPending},
		{"verified, modified since", db.OffloadItem{State: db.OffloadVerified, Bytes: 100, VerifiedAt: now.Add(-2 * time.Hour)}, cand, true, db.OffloadPending},
		{"stuck", db.OffloadItem{State: db.OffloadStuck}, cand, false, db.OffloadStuck},
		{"failed, backing off", db.OffloadItem{State: db.OffloadFailed, NextAttemptAt: now.Add(time.Minute)}, cand, false, db.OffloadFailed},
		{"failed, due", db.OffloadItem{State: db.OffloadFailed, NextAttemptAt: now.Add(-time.Minute)}, cand, true, db.OffloadFailed},
		{"pending", db.OffloadItem{State: db.OffloadPending}, cand, true, db.OffloadPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it := c.it
			if got := s.due(&it, c.cand); got != c.want {
				t.Errorf("due = %v, want %v", got, c.want)
			}
			if it.State != c.wantState {
				t.Errorf("state = %v, want %v", it.State, c.wantState)
			}
			if c.wantState == db.OffloadPending && c.it.State == db.OffloadVerified && (it.Attempts != 0 || !it.NextAttemptAt.IsZero()) {
				t.Errorf("a changed file kept its attempts: %+v", it)
			}
		})
	}
}

// A recording's saved chat (recording.writeChatSidecars) rides along with it:
// no storage target has a switch for it, so it goes under the recording kind.
func TestSavedChatTravelsWithItsRecording(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	old := f.segment(5*time.Hour, "old segment", true)
	live := f.segment(time.Minute, "newest, still being written", true)
	base := strings.TrimSuffix(old, ".mkv")
	liveBase := strings.TrimSuffix(live, ".mkv")
	for _, name := range []string{base + ".chat.json", base + ".chat.vtt", liveBase + ".chat.json"} {
		path := filepath.Join(f.dir, name)
		if err := os.WriteFile(path, []byte("chat "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := f.now.Add(-time.Hour)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.Pass(context.Background())

	for _, name := range []string{base + ".chat.json", base + ".chat.vtt"} {
		it, ok := f.item(id, name)
		if !ok || it.State != db.OffloadVerified || it.Kind != db.OffloadKindRecording {
			t.Errorf("%s: %+v (found %v), want a verified recording-kind item", name, it, ok)
		}
	}
	// A segment the recorder may still hold is not offloaded, and neither is its chat.
	if _, ok := f.item(id, liveBase+".chat.json"); ok {
		t.Error("the live segment's chat was offloaded")
	}
	if !f.local(base + ".chat.json") {
		t.Error("mirror mode removed the local sidecar")
	}
}
