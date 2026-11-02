package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// validateStorageTarget refuses each way a target cannot work, and names it.
func TestValidateStorageTargetRefusesEachUnusableField(t *testing.T) {
	good := db.StorageTarget{Name: "a", Provider: "s3", Endpoint: "https://s3.example", Bucket: "media-archive",
		Region: "us-east-1", AccessKeyID: "fake-access-key", MaxAttempts: 8}
	if err := validateStorageTarget(good); err != nil {
		t.Fatalf("good target refused: %v", err)
	}
	cases := map[string]func(*db.StorageTarget){
		"name":        func(x *db.StorageTarget) { x.Name = strings.Repeat("n", 81) },
		"provider":    func(x *db.StorageTarget) { x.Provider = "ftp" },
		"scheme":      func(x *db.StorageTarget) { x.Endpoint = "ftp://s3.example" },
		"no host":     func(x *db.StorageTarget) { x.Endpoint = "https://" },
		"query":       func(x *db.StorageTarget) { x.Endpoint = "https://s3.example?x=1" },
		"fragment":    func(x *db.StorageTarget) { x.Endpoint = "https://s3.example#f" },
		"loopback":    func(x *db.StorageTarget) { x.Endpoint = "http://127.0.0.1:9000" },
		"bucket":      func(x *db.StorageTarget) { x.Bucket = "ab" },
		"region":      func(x *db.StorageTarget) { x.Region = "US EAST" },
		"template":    func(x *db.StorageTarget) { x.PrefixTemplate = "{nope}" },
		"attempts":    func(x *db.StorageTarget) { x.MaxAttempts = 101 },
		"no key id":   func(x *db.StorageTarget) { x.AccessKeyID = "" },
		"long key":    func(x *db.StorageTarget) { x.AccessKeyID = strings.Repeat("k", 129) },
		"long secret": func(x *db.StorageTarget) { x.SecretAccessKey = strings.Repeat("s", 257) },
	}
	for name, mut := range cases {
		x := good
		mut(&x)
		if err := validateStorageTarget(x); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Every storage route answers a malformed or unknown id the same way the rest
// of the API does: 400 for something that is not an id, 404 for a target
// that does not exist.
func TestStorageRoutesAnswerBadAndUnknownIDs(t *testing.T) {
	h, _, sign := sourceServer(t)
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/api/v1/storage/targets/abc", nil, http.StatusBadRequest},
		{http.MethodPut, "/api/v1/storage/targets/abc", map[string]any{}, http.StatusBadRequest},
		{http.MethodDelete, "/api/v1/storage/targets/abc", nil, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/storage/targets/abc/retry", map[string]any{}, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/library/recordings/abc/offload/retry", map[string]any{}, http.StatusBadRequest},
		{http.MethodGet, "/api/v1/storage/targets/999", nil, http.StatusNotFound},
		{http.MethodPut, "/api/v1/storage/targets/999", map[string]any{"name": "x"}, http.StatusNotFound},
		{http.MethodDelete, "/api/v1/storage/targets/999", nil, http.StatusNotFound},
		{http.MethodPost, "/api/v1/library/recordings/999/offload/retry", map[string]any{}, http.StatusNotFound},
		{http.MethodPost, "/api/v1/storage/targets/test", map[string]any{"id": 999}, http.StatusNotFound},
	} {
		rec := doJSON(t, h, sign, tc.method, tc.path, tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s %s: %d %s, want %d", tc.method, tc.path, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

// A save that would make a working target unusable is refused and changes
// nothing; a Test with no secret anywhere asks for one.
func TestAnInvalidUpdateIsRefusedAndATestWithoutASecretAsksForOne(t *testing.T) {
	h, store, sign := sourceServer(t)
	id := mkTarget(t, h, sign, "a", "bucket-a")
	rec := doJSON(t, h, sign, http.MethodPut, "/api/v1/storage/targets/"+strconv.FormatInt(id, 10), map[string]any{"maxAttempts": 0})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid update: %d %s", rec.Code, rec.Body.String())
	}
	tg, _ := store.GetStorageTarget(storageFixtureBox(t), id)
	if tg.MaxAttempts != db.DefaultOffloadMaxAttempts {
		t.Fatalf("refused update changed the target: %+v", tg)
	}
	rec = doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets/test", map[string]any{
		"name": "t", "provider": "s3", "endpoint": "https://s3.example", "bucket": "media-archive", "accessKeyId": "fake-access-key"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("test without a secret: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets/test", map[string]any{
		"provider": "s3", "endpoint": "ftp://bad", "bucket": "media-archive", "accessKeyId": "k", "secretAccessKey": "fake-secret-value-not-real"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("test of an invalid target: %d", rec.Code)
	}
	// A saved secret this machine cannot open is reported as such.
	_, _ = store.SQL().Exec(`UPDATE storage_targets SET secret_enc = X'00' WHERE id = ?`, id)
	rec = doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets/test", map[string]any{"id": id})
	if rec.Code != http.StatusConflict {
		t.Fatalf("test with an unreadable secret: %d %s", rec.Code, rec.Body.String())
	}
}
