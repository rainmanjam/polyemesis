package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func mkTarget(t *testing.T, h http.Handler, sign func(*http.Request), name, mode, bucket string) int64 {
	t.Helper()
	rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets", map[string]any{
		"name": name, "provider": "s3", "endpoint": "https://s3.example", "bucket": bucket,
		"accessKeyId": "fake-access-key", "secretAccessKey": "fake-secret-value-not-real", "mode": mode})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body.String())
	}
	var v struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return v.ID
}

func verified(t *testing.T, store *db.DB, target int64, rel string) {
	t.Helper()
	if err := store.PutOffloadItem(db.OffloadItem{TargetID: target, RelPath: rel, Kind: db.OffloadKindRecording,
		Key: "k/" + strconv.FormatInt(target, 10) + "/" + rel, State: db.OffloadVerified, SHA256: "x", Bytes: 1,
		VerifiedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// Review #4: moving a target (endpoint, bucket, region, access key or
// addressing) is refused while it holds evicted recordings, and otherwise
// sends its files back to be uploaded at the new address -- so a stale
// "verified" row can never make another target look deletable.
func TestMovingATargetIsRefusedWhileItHoldsEvictedFilesAndResetsItsRowsOtherwise(t *testing.T) {
	h, store, sign := sourceServer(t)
	a := mkTarget(t, h, sign, "a", "move", "bucket-a")
	b := mkTarget(t, h, sign, "b", "mirror", "bucket-b")
	verified(t, store, a, "rec-1.mkv")
	verified(t, store, b, "rec-1.mkv")
	if err := store.BeginEvict("rec-1.mkv", nil); err != nil {
		t.Fatal(err)
	}
	for _, change := range []map[string]any{
		{"bucket": "bucket-c", "secretAccessKey": "another-fake-secret"},
		{"region": "eu-west-1"},
		{"pathStyle": true, "secretAccessKey": "another-fake-secret"},
		{"accessKeyId": "other-fake-key"},
	} {
		rec := doJSON(t, h, sign, http.MethodPut, "/api/v1/storage/targets/"+strconv.FormatInt(b, 10), change)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%v on a target holding an evicted file: %d %s", change, rec.Code, rec.Body.String())
		}
	}
	// A rename is not a move.
	if rec := doJSON(t, h, sign, http.MethodPut, "/api/v1/storage/targets/"+strconv.FormatInt(b, 10), map[string]any{"name": "b2"}); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}

	// Without evicted files a move is allowed and nothing stays "verified".
	_ = store.UndoEvict("rec-1.mkv")
	rec := doJSON(t, h, sign, http.MethodPut, "/api/v1/storage/targets/"+strconv.FormatInt(b, 10),
		map[string]any{"bucket": "bucket-c", "secretAccessKey": "another-fake-secret"})
	if rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	its, _ := store.OffloadItemsFor("rec-1.mkv")
	for _, it := range its {
		if it.TargetID == b && it.State != db.OffloadPending {
			t.Fatalf("moved target's row still %s", it.State)
		}
	}
	// So with a's copy evicted, b no longer counts as somewhere else.
	_ = store.BeginEvict("rec-1.mkv", nil) // refused: b is not verified
	verified(t, store, a, "rec-1.mkv")
	_, _ = store.SQL().Exec(`UPDATE offload_items SET evicted = 1 WHERE rel_path = 'rec-1.mkv'`)
	if rec := doJSON(t, h, sign, http.MethodDelete, "/api/v1/storage/targets/"+strconv.FormatInt(a, 10), nil); rec.Code != http.StatusConflict {
		t.Fatalf("deleted the target holding the only verified copy: %d", rec.Code)
	}
}

// Review #4: a target whose secret cannot be opened does not count as holding
// a usable copy when another target is deleted.
func TestAnUnreadableTargetDoesNotMakeAnotherDeletable(t *testing.T) {
	h, store, sign := sourceServer(t)
	a := mkTarget(t, h, sign, "a", "move", "bucket-a")
	b := mkTarget(t, h, sign, "b", "mirror", "bucket-b")
	verified(t, store, a, "rec-1.mkv")
	verified(t, store, b, "rec-1.mkv")
	_, _ = store.SQL().Exec(`UPDATE offload_items SET evicted = 1`)
	_, _ = store.SQL().Exec(`UPDATE storage_targets SET secret_enc = X'00' WHERE id = ?`, b)
	if rec := doJSON(t, h, sign, http.MethodDelete, "/api/v1/storage/targets/"+strconv.FormatInt(a, 10), nil); rec.Code != http.StatusConflict {
		t.Fatalf("deleted a's copy because b, whose secret will not open, also had one: %d", rec.Code)
	}
}

// Review #21: path style is part of what the saved secret is bound to.
func TestChangingPathStyleNeedsTheSecretAgain(t *testing.T) {
	saved := db.StorageTarget{Endpoint: "https://s3.example", Bucket: "b"}
	next := saved
	next.PathStyle = true
	if !secretBindingChanged(saved, next) {
		t.Fatal("path style flipped without needing the secret")
	}
}

// Review #10: deleting a recording deletes its offload rows.
func TestDeletingARecordingDropsItsOffloadRows(t *testing.T) {
	h, store, sign := sourceServer(t)
	a := mkTarget(t, h, sign, "a", "move", "bucket-a")
	r := &db.Recording{Filename: "rec-20261101-100000.mkv", StartedAt: time.Now().Add(-48 * time.Hour), DurationMS: 1000}
	if err := store.UpsertRecording(r); err != nil {
		t.Fatal(err)
	}
	verified(t, store, a, r.Filename)
	_, _ = store.SQL().Exec(`UPDATE offload_items SET evicted = 1`)
	recs, _ := store.ListRecordings()
	if rec := doJSON(t, h, sign, http.MethodDelete, "/api/v1/recordings/"+strconv.FormatInt(recs[0].ID, 10), nil); rec.Code != http.StatusOK {
		t.Fatalf("delete recording: %d %s", rec.Code, rec.Body.String())
	}
	if its, _ := store.OffloadItemsFor(r.Filename); len(its) != 0 {
		t.Fatalf("offload rows survived the recording: %+v", its)
	}
	if rec := doJSON(t, h, sign, http.MethodDelete, "/api/v1/storage/targets/"+strconv.FormatInt(a, 10), nil); rec.Code != http.StatusOK {
		t.Fatalf("target still undeletable after its recording went: %d", rec.Code)
	}
}

func TestOffloadInUseIsFalseWithoutAQueue(t *testing.T) {
	s := &Server{}
	if s.offloadInUse("x") {
		t.Fatal("no queue, nothing in use")
	}
	_ = strings.TrimSpace
}
