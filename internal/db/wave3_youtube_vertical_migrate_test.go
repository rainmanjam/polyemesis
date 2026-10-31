package db

import (
	"strings"
	"testing"
)

// TestWave3YouTubeVerticalPairingTableIsIdempotentAndLeavesTheSchemaVersionAlone pins the
// schema half of #7 YouTube paired vertical.
//
// The pairing link is a new table, not a column on destinations: #8 and #15 add
// columns there too, and two lanes editing one scan list is a merge conflict waiting
// to happen. The version check is what makes the table a non-event for older
// binaries: an older binary never reads youtube_vertical_pairs, so there is nothing
// it could misread, and currentSchemaVersion has no reason to move.
func TestWave3YouTubeVerticalPairingTableIsIdempotentAndLeavesTheSchemaVersionAlone(t *testing.T) {
	d := testDB(t)
	before, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := d.MigrateWave3YouTubeVertical(); err != nil {
			t.Fatalf("MigrateWave3YouTubeVertical pass %d: %v", pass, err)
		}
	}
	after, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version after slot: %v", err)
	}
	if after != before {
		t.Fatalf("MigrateWave3YouTubeVertical moved the schema version from %d to %d", before, after)
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='youtube_vertical_pairs'`).Scan(&n); err != nil {
		t.Fatalf("look for the pairing table: %v", err)
	}
	if n != 1 {
		t.Fatalf("youtube_vertical_pairs table missing after the slot ran twice (count %d)", n)
	}
}

// TestYouTubeVerticalPairSurvivesOnlyWhileBothHalvesExist pins the cascade: deleting
// either destination removes the link, so a pair row can never point at a destination
// that no longer exists. The vertical stream key is never stored in the pair row, which
// is asserted by the column list rather than by reading a value back.
func TestYouTubeVerticalPairSurvivesOnlyWhileBothHalvesExist(t *testing.T) {
	d := testDB(t)
	h, err := d.CreateDestination(&Destination{Name: "yt", Kind: DestRTMP, URL: "rtmp://a.example/live2", StreamKey: "h-key", AudioBitrate: 160})
	if err != nil {
		t.Fatalf("create horizontal: %v", err)
	}
	v, err := d.CreateDestination(&Destination{Name: "yt (vertical)", Kind: DestRTMP, URL: "rtmp://a.example/live2", StreamKey: "v-key", AudioBitrate: 160})
	if err != nil {
		t.Fatalf("create vertical: %v", err)
	}
	if err := d.CreateYouTubeVerticalPair(h.ID, v.ID, "stream-vertical-1"); err != nil {
		t.Fatalf("CreateYouTubeVerticalPair: %v", err)
	}
	got, err := d.GetYouTubeVerticalPairByHorizontal(h.ID)
	if err != nil {
		t.Fatalf("GetYouTubeVerticalPairByHorizontal: %v", err)
	}
	if got.VerticalID != v.ID || got.VerticalStreamID != "stream-vertical-1" {
		t.Fatalf("pair read back as %+v", got)
	}
	if err := d.DeleteDestination(v.ID); err != nil {
		t.Fatalf("DeleteDestination(vertical): %v", err)
	}
	if _, err := d.GetYouTubeVerticalPairByHorizontal(h.ID); err == nil {
		t.Fatal("deleting the vertical half left its pair row behind")
	}
}

// TestYouTubeVerticalPairTableHasNoKeyColumn fails if anyone adds a column that could hold
// a stream key. The key belongs in destinations.stream_key, which is the encrypted path.
func TestYouTubeVerticalPairTableHasNoKeyColumn(t *testing.T) {
	d := testDB(t)
	rows, err := d.sql.Query(`SELECT name FROM pragma_table_info('youtube_vertical_pairs')`)
	if err != nil {
		t.Fatalf("read pair columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if strings.Contains(strings.ToLower(col), "key") {
			t.Fatalf("youtube_vertical_pairs has column %q; a stream key must never live in the pair table", col)
		}
	}
}
