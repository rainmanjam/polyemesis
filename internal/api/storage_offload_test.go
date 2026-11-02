package api

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/objstore"
	"github.com/rainmanjam/polyemesis/internal/offload"
)

// apiMemBucket is a minimal in-memory bucket for driving offload through the
// HTTP surface without a network.
type apiMemBucket struct {
	mu   sync.Mutex
	objs map[string][]byte
	meta map[string]map[string]string
}

func (m *apiMemBucket) Put(_ context.Context, key string, body io.ReadSeeker, _ int64, _, _ string, meta map[string]string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := io.ReadAll(body)
	m.objs[key], m.meta[key] = b, meta
	s := md5.Sum(b)
	return hex.EncodeToString(s[:]), nil
}

func (m *apiMemBucket) Head(_ context.Context, key string) (objstore.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return objstore.ObjectInfo{}, &objstore.Error{StatusCode: 404}
	}
	s := md5.Sum(b)
	return objstore.ObjectInfo{Size: int64(len(b)), ETag: hex.EncodeToString(s[:]), Meta: m.meta[key]}, nil
}

func (m *apiMemBucket) Get(_ context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, objstore.ObjectInfo{}, &objstore.Error{StatusCode: 404}
	}
	return io.NopCloser(bytes.NewReader(b)), objstore.ObjectInfo{Size: int64(len(b))}, nil
}

func (m *apiMemBucket) GetFrom(ctx context.Context, key string, _ int64) (io.ReadCloser, objstore.ObjectInfo, bool, error) {
	b, info, err := m.Get(ctx, key)
	return b, info, false, err
}

// Multipart is not reached: the files here are far below the threshold.
func (m *apiMemBucket) CreateMultipart(context.Context, string, map[string]string) (string, error) {
	return "", errors.New("not used")
}
func (m *apiMemBucket) UploadPart(context.Context, string, string, int, io.ReadSeeker, int64, string, string) (string, error) {
	return "", errors.New("not used")
}
func (m *apiMemBucket) CompleteMultipart(context.Context, string, string, []objstore.Part) (string, error) {
	return "", errors.New("not used")
}
func (m *apiMemBucket) AbortMultipart(context.Context, string, string) error { return nil }

func (m *apiMemBucket) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}

// TestTheLibraryShowsEachRecordingsUploadState drives a mirror upload through
// the real service and reads it back from the Library: per target, with its
// state and the target's name. The local file is untouched.
func TestTheLibraryShowsEachRecordingsUploadState(t *testing.T) {
	h, store, sign := sourceServer(t)
	s := serverUnderTest(t, h)
	dir := s.recordings().Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mk := func(age time.Duration, body string) *db.Recording {
		at := now.Add(-age).Truncate(time.Second)
		name := "rec-" + at.Format("20060102-150405") + ".mkv"
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-age + time.Minute)
		_ = os.Chtimes(path, mt, mt)
		r := &db.Recording{Filename: name, StartedAt: at, Bytes: int64(len(body)), DurationMS: 60000}
		if err := store.UpsertRecording(r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	old := mk(5*time.Hour, "an hour of footage")
	mk(time.Minute, "the segment being written")

	bucket := &apiMemBucket{objs: map[string][]byte{}, meta: map[string]map[string]string{}}
	s.offload = offload.New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, s.box, s.recordings(), nil,
		offload.WithClientFactory(func(db.StorageTarget) (offload.Client, error) { return bucket, nil }))
	t.Cleanup(func() { s.offload = nil })

	rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets", map[string]any{
		"name": "archive", "provider": "s3", "endpoint": "https://s3.example", "bucket": "media-archive",
		"accessKeyId": "fake-access-key", "secretAccessKey": "fake-secret-value-not-real"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create target: %d %s", rec.Code, rec.Body.String())
	}
	for start := time.Now(); ; {
		s.offload.Pass(context.Background())
		its, _ := store.OffloadItemsFor(old.Filename)
		if len(its) == 1 && its[0].State == db.OffloadVerified {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("never verified: %+v", its)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, old.Filename)); err != nil {
		t.Fatalf("mirror mode touched the local file: %v", err)
	}
	// WHEREVER THE LIBRARY PUTS IT. sourceServer's engine is running its
	// recording manager underneath this test, and that manager's sweep -- once
	// the moment the engine starts, then every 30s -- ends by grouping every
	// ungrouped recording into a session, which is what it is for. When the
	// startup sweep lands after the rows above, this recording is a session
	// segment by now, not an ungrouped one; on a loaded macOS runner it did,
	// and "recording missing from the library" was the product working. Both
	// places carry the upload state, so the test asks the one that holds it.
	// Grouping only ever goes one way, so a recording absent from Ungrouped
	// is in a session by the time it is looked up.
	type listed struct {
		Filename string            `json:"filename"`
		Offload  *recordingOffload `json:"offload"`
	}
	find := func(rs []listed) *listed {
		for i := range rs {
			if rs[i].Filename == old.Filename {
				return &rs[i]
			}
		}
		return nil
	}
	var page struct {
		Ungrouped []listed `json:"ungrouped"`
	}
	if err := json.Unmarshal([]byte(storageBody(t, h, sign, "/api/v1/library")), &page); err != nil {
		t.Fatal(err)
	}
	got, where := find(page.Ungrouped), "the library's ungrouped list"
	if got == nil {
		recs, err := store.ListRecordings()
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		for _, r := range recs {
			if r.Filename == old.Filename {
				id = r.ID
			}
		}
		sess, err := store.SessionForRecording(id)
		if err != nil {
			t.Fatalf("recording missing from the library: not ungrouped, and in no session (id %d): %v", id, err)
		}
		var segs struct {
			Recordings []listed `json:"recordings"`
		}
		path := fmt.Sprintf("/api/v1/library/sessions/%d", sess.ID)
		if err := json.Unmarshal([]byte(storageBody(t, h, sign, path)), &segs); err != nil {
			t.Fatal(err)
		}
		got, where = find(segs.Recordings), "its session's segment list"
		if got == nil {
			t.Fatalf("recording missing from the library: its session %d does not list it", sess.ID)
		}
	}
	if o := got.Offload; o == nil || len(o.Targets) != 1 || o.Targets[0].State != db.OffloadVerified ||
		o.Targets[0].TargetName != "archive" {
		t.Fatalf("library offload view in %s: %+v", where, got.Offload)
	}
}
