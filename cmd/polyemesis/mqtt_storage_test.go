package main

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
)

// The telemetry snapshot carries each storage target's upload counts, so
// Home Assistant can alert on stuck uploads.
func TestTheSnapshotCountsEachStorageTargetsStuckUploads(t *testing.T) {
	store := dbtest.Open(t)
	id, err := store.CreateStorageTarget(nil, db.StorageTarget{Name: "Archive", Provider: "s3", Endpoint: "http://h", Bucket: "b", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, st := range []string{db.OffloadStuck, db.OffloadStuck, db.OffloadFailed, db.OffloadVerified, db.OffloadPending} {
		if err := store.PutOffloadItem(db.OffloadItem{TargetID: id, RelPath: "f" + string(rune('a'+i)), Kind: db.OffloadKindRecording,
			Key: "k" + string(rune('a'+i)), State: st}); err != nil {
			t.Fatal(err)
		}
	}
	r := &mqttRunner{store: store, log: slog.New(slog.NewTextHandler(io.Discard, nil)), version: "test",
		engines: func() []mqttEngine { return nil }, started: time.Now()}
	got := r.snapshot().Storage
	if len(got) != 1 || got[0].Stuck != 2 || got[0].Failed != 1 || got[0].Verified != 1 || got[0].Pending != 1 || got[0].Name != "Archive" {
		t.Fatalf("%+v", got)
	}
}

// Codex #4: two targets with the same name get distinct topics and entities.
func TestStorageTargetsWithTheSameNameDoNotShareATopic(t *testing.T) {
	store := dbtest.Open(t)
	for i := 0; i < 2; i++ {
		if _, err := store.CreateStorageTarget(nil, db.StorageTarget{Name: "Archive", Provider: "s3", Endpoint: "http://h", Bucket: "b", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	r := &mqttRunner{store: store, log: slog.New(slog.NewTextHandler(io.Discard, nil)), version: "test",
		engines: func() []mqttEngine { return nil }, started: time.Now()}
	got := r.snapshot().Storage
	if len(got) != 2 || got[0].Slug == got[1].Slug || got[0].Name != "Archive" || got[1].Name != "Archive" {
		t.Fatalf("%+v", got)
	}
}
