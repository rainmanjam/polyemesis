package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/offload"
)

// refusingBucket aborts nothing, as a bucket whose policy denies it does.
type refusingBucket struct{ *apiMemBucket }

func (refusingBucket) AbortMultipart(context.Context, string, string) error {
	return errors.New("AccessDenied")
}

// #14 of the 2026-11-04 lingering-state audit. Deleting a storage target always
// succeeds, and used to say {"status":"deleted"} even when its unfinished
// uploads could not be aborted -- uploads that keep billing, whose ids the
// delete had just thrown away. The response now names them.
func TestDeletingAStorageTargetNamesTheUploadsItCouldNotAbort(t *testing.T) {
	h, store, sign := sourceServer(t)
	s := serverUnderTest(t, h)
	bucket := refusingBucket{&apiMemBucket{objs: map[string][]byte{}, meta: map[string]map[string]string{}}}
	s.offload = offload.New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, s.box, s.recordings(), nil,
		offload.WithClientFactory(func(db.StorageTarget) (offload.Client, error) { return bucket, nil }))
	t.Cleanup(func() { s.offload = nil })

	rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets", map[string]any{
		"name": "archive", "provider": "s3", "endpoint": "https://s3.example", "bucket": "media-archive",
		"accessKeyId": "fake-access-key", "secretAccessKey": "fake-secret-value-not-real"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create target: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, rec.Body.Bytes(), &created)
	if err := store.PutOffloadMultipart(db.OffloadMultipart{TargetID: created.ID, RelPath: "rec.mkv",
		Key: "media/rec.mkv", UploadID: "upload-one", PartSize: 40, Bytes: 100, SHA256: "x"}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Status           string              `json:"status"`
		Warning          string              `json:"warning"`
		UnabortedUploads []map[string]string `json:"unabortedUploads"`
	}
	decodeInto(t, send(t, h, sign, http.MethodDelete, "/api/v1/storage/targets/"+itoa(created.ID), nil,
		http.StatusOK), &got)
	if got.Status != "deleted" {
		t.Fatalf("status %q: the delete must still succeed", got.Status)
	}
	if len(got.UnabortedUploads) != 1 || got.UnabortedUploads[0]["uploadId"] != "upload-one" ||
		got.UnabortedUploads[0]["key"] != "media/rec.mkv" {
		t.Fatalf("unabortedUploads %+v, want the one refused upload", got.UnabortedUploads)
	}
	if !strings.Contains(got.Warning, "keep billing") {
		t.Errorf("warning %q does not say the upload keeps billing", got.Warning)
	}
}
