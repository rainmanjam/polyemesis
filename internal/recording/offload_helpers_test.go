package recording

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestLiveSetNamesTheNewestAndRecentSegments(t *testing.T) {
	now := time.Now()
	recs := []db.Recording{
		{Filename: "old", StartedAt: now.Add(-3 * time.Hour)},
		{Filename: "recent", StartedAt: now.Add(-30 * time.Second)},
	}
	live := LiveSet(recs, 60, now)
	if !live["recent"] || live["old"] {
		t.Fatalf("live %v", live)
	}
	if only := LiveSet(recs[:1], 60, now); !only["old"] {
		t.Fatal("the newest segment is always live")
	}
}

func TestStemMasterBase(t *testing.T) {
	if b, ok := StemMasterBase("rec-s3-20240115-143000-mic.flac"); !ok || b != "rec-s3-20240115-143000" {
		t.Fatalf("%q %v", b, ok)
	}
	if _, ok := StemMasterBase("rec-20240115-143000.mkv"); ok {
		t.Fatal("a master is not a stem")
	}
}
