package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func mkTarget(t *testing.T, h http.Handler, sign func(*http.Request), name, bucket string) int64 {
	t.Helper()
	rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets", map[string]any{
		"name": name, "provider": "s3", "endpoint": "https://s3.example", "bucket": bucket,
		"accessKeyId": "fake-access-key", "secretAccessKey": "fake-secret-value-not-real"})
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

// Moving a target (endpoint, bucket, region, access key or addressing) sends
// every one of its files back to be uploaded at the new address: nothing
// stays "verified" for a bucket the target no longer points at. A rename is
// not a move.
func TestMovingATargetResetsItsRowsAndARenameDoesNot(t *testing.T) {
	h, store, sign := sourceServer(t)
	b := mkTarget(t, h, sign, "b", "bucket-b")
	verified(t, store, b, "rec-1.mkv")
	if rec := doJSON(t, h, sign, http.MethodPut, "/api/v1/storage/targets/"+strconv.FormatInt(b, 10), map[string]any{"name": "b2"}); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if its, _ := store.OffloadItemsFor("rec-1.mkv"); its[0].State != db.OffloadVerified {
		t.Fatal("a rename reset the rows")
	}
	for _, change := range []map[string]any{
		{"bucket": "bucket-c", "secretAccessKey": "another-fake-secret"},
		{"region": "eu-west-1"},
		{"pathStyle": true, "secretAccessKey": "another-fake-secret"},
		{"accessKeyId": "other-fake-key"},
	} {
		verified(t, store, b, "rec-1.mkv")
		rec := doJSON(t, h, sign, http.MethodPut, "/api/v1/storage/targets/"+strconv.FormatInt(b, 10), change)
		if rec.Code != http.StatusOK {
			t.Fatalf("%v: %d %s", change, rec.Code, rec.Body.String())
		}
		if its, _ := store.OffloadItemsFor("rec-1.mkv"); its[0].State != db.OffloadPending {
			t.Fatalf("%v left the row %s", change, its[0].State)
		}
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
