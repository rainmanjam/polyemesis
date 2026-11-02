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

// TestAnOffloadedRecordingIsFetchedBackBeforeTheClipperOpensIt is the
// end-to-end answer to "how does the clipper wait": 409 "offloaded" naming
// the file, POST .../fetch answers 202, the recordings event fires when the
// file lands, and the same clipper route then opens it.
func TestAnOffloadedRecordingIsFetchedBackBeforeTheClipperOpensIt(t *testing.T) {
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
	recs, _ := store.ListRecordings()
	var id int64
	for _, r := range recs {
		if r.Filename == old.Filename {
			id = r.ID
		}
	}

	bucket := &apiMemBucket{objs: map[string][]byte{}, meta: map[string]map[string]string{}}
	changed := make(chan struct{}, 8)
	s.offload = offload.New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, s.box, s.recordings(),
		func() { changed <- struct{}{} },
		offload.WithClientFactory(func(db.StorageTarget) (offload.Client, error) { return bucket, nil }))
	t.Cleanup(func() { s.offload = nil })

	rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets", map[string]any{
		"name": "archive", "provider": "s3", "endpoint": "https://s3.example", "bucket": "media-archive",
		"accessKeyId": "fake-access-key", "secretAccessKey": "fake-secret-value-not-real",
		"mode": "move", "keepLocalHours": 0})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create target: %d %s", rec.Code, rec.Body.String())
	}
	// Saving the target kicks a pass of its own; Pass is single-flight, so
	// drive it until the eviction is observed rather than once.
	for start := time.Now(); ; {
		s.offload.Pass(context.Background())
		if _, err := os.Stat(filepath.Join(dir, old.Filename)); os.IsNotExist(err) {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatal("never evicted")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The Library shows where it went.
	lib := storageBody(t, h, sign, "/api/v1/library")
	var view struct {
		Ungrouped []struct {
			Filename string            `json:"filename"`
			Offload  *recordingOffload `json:"offload"`
		} `json:"ungrouped"`
	}
	if err := json.Unmarshal([]byte(lib), &view); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range view.Ungrouped {
		if r.Filename == old.Filename {
			found = true
			if r.Offload == nil || !r.Offload.Evicted || len(r.Offload.Targets) != 1 ||
				r.Offload.Targets[0].State != db.OffloadVerified || r.Offload.Targets[0].TargetName != "archive" {
				t.Fatalf("library offload view %+v", r.Offload)
			}
		}
	}
	if !found {
		t.Fatalf("evicted recording missing from the library: %s", lib)
	}

	// The route the clip editor polls while it waits says the same.
	var one struct {
		Recording struct {
			Offload *recordingOffload `json:"offload"`
		} `json:"recording"`
	}
	if err := json.Unmarshal([]byte(storageBody(t, h, sign, fmt.Sprintf("/api/v1/library/recordings/%d", id))), &one); err != nil ||
		one.Recording.Offload == nil || !one.Recording.Offload.Evicted {
		t.Fatalf("GET /library/recordings/{id} does not report the eviction: %+v %v", one.Recording.Offload, err)
	}

	clip := fmt.Sprintf("/api/v1/clipper/recordings/%d", id)
	code, body := rawBodyOf(t, h, sign, clip)
	if code != http.StatusConflict {
		t.Fatalf("clipper on an offloaded recording: %d %s", code, body)
	}
	var refusal struct {
		Code      string   `json:"code"`
		Offloaded []string `json:"offloaded"`
	}
	_ = json.Unmarshal([]byte(body), &refusal)
	if refusal.Code != "offloaded" || len(refusal.Offloaded) != 1 || refusal.Offloaded[0] != old.Filename {
		t.Fatalf("refusal %s", body)
	}
	// The download route refuses the same way (the job route does too, but
	// this fixture runs no job queue, so it answers 503 before reaching it).
	if code, body := rawBodyOf(t, h, sign, fmt.Sprintf("/api/v1/recordings/%d/download", id)); code != http.StatusConflict {
		t.Fatalf("download of an offloaded recording: %d %s", code, body)
	}

	if rec := doJSON(t, h, sign, http.MethodPost, fmt.Sprintf("/api/v1/library/recordings/%d/fetch", id), map[string]any{}); rec.Code != http.StatusAccepted {
		t.Fatalf("fetch: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, ok := s.offload.FetchStatus(old.Filename); ok && st.State != offload.FetchFetching {
			if st.State != offload.FetchDone {
				t.Fatalf("fetch %+v", st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fetch never reported back")
		}
		select {
		case <-changed:
		case <-time.After(50 * time.Millisecond):
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, old.Filename))
	if err != nil || string(b) != "an hour of footage" {
		t.Fatalf("fetched %q %v", b, err)
	}
	if code, body := rawBodyOf(t, h, sign, clip); code == http.StatusConflict {
		t.Fatalf("clipper still refuses after the fetch: %s", body)
	}
	// Already local: a second fetch is a conflict, not a re-download.
	if rec := doJSON(t, h, sign, http.MethodPost, fmt.Sprintf("/api/v1/library/recordings/%d/fetch", id), map[string]any{}); rec.Code != http.StatusConflict {
		t.Fatalf("second fetch: %d", rec.Code)
	}
}
