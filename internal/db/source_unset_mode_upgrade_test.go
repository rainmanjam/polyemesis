package db

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// v0100ConsoleSourceIngest is the ingest blob v0.10.0 stored for a source
// created from the Sources page, byte for byte. Captured, not written by hand:
// the v0.10.0 image (ghcr.io/rainmanjam/polyemesis:0.10.0) was set up, sent
// POST /api/v1/sources {"name":"console-made"} -- the whole of the console's
// create body -- stopped, and the row read out of /data/polyemesis.db with
// sqlite3. That same source accepted an SRT publish on the shared port before
// it was stopped ("srt publisher connected ... source=console-made"), which is
// the behaviour an upgrade must not take away.
const v0100ConsoleSourceIngest = `{"mode":"","srt":{"passphrase":"","latencyMs":200},` +
	`"rtmp":{"app":"live","streamKey":""},` +
	`"pull":{"url":"","reconnectDelayMaxSeconds":30,"rtspTransport":"tcp"}}`

// A source whose ingest mode was never chosen is upgraded to SRT, the only
// ingest it ever had.
//
// Until the shared SRT port learned to admit only SRT-mode sources, it admitted
// any source with a running engine -- and every source the console created was
// mode "", because the create form sends {name} and the server filled in
// DefaultSettings().Ingest, whose mode is deliberately unset. So on every
// multi-source install those sources WERE SRT sources in all but the stored
// word, with encoders pointed at them. The listener fix is right to refuse
// rtmp and pull; refusing these silently disconnected their encoders on the
// first boot after an upgrade, with nothing in the UI to say why.
//
// Built from what v0.10.0 actually wrote (v0100ConsoleSourceIngest), next to an
// rtmp and a pull source that must be left alone, a pre-mode blob with no
// "mode" key at all, and a blob that does not parse -- which scanSource
// tolerates, so the migration must not be what stops the server booting.
//
// Mutation: remove MigrateUnsetSourceIngestMode from Open. Observed to fail
// with `"console-made" came through the upgrade with ingest mode ""`.
func TestAnUpgradeGivesSourcesWithNoIngestModeSRT(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	old, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	insert := func(name, ingest, token string) int64 {
		t.Helper()
		res, err := old.SQL().Exec(
			`INSERT INTO sources (name, enabled, ingest, token, position, created_at, updated_at)
			 VALUES (?, 1, ?, ?, 1, 1700000000, 1700000000)`, name, ingest, token)
		if err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	unset := insert("console-made", v0100ConsoleSourceIngest, "tok-unset-000000000000000000000001")
	noKey := insert("pre-mode", `{"srt":{"latencyMs":350}}`, "tok-nokey-000000000000000000000002")
	rtmp := insert("rtmp", `{"mode":"rtmp","srt":{"latencyMs":200},"rtmp":{"app":"live"}}`,
		"tok-rtmp-0000000000000000000000003")
	pull := insert("pull", `{"mode":"pull","srt":{"latencyMs":200},"pull":{"url":"rtmp://cam.invalid/live/x"}}`,
		"tok-pull-0000000000000000000000004")
	broken := insert("broken", `{not json`, "tok-broken-00000000000000000000005")
	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The upgrade boot.
	d, err := Open(path)
	if err != nil {
		t.Fatalf("the upgrade boot refused to open the database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	mode := func(id int64) (IngestMode, map[string]any) {
		t.Helper()
		s, err := d.GetSource(id)
		if err != nil {
			t.Fatalf("GetSource(%d): %v", id, err)
		}
		var raw string
		if err := d.SQL().QueryRow(`SELECT ingest FROM sources WHERE id = ?`, id).Scan(&raw); err != nil {
			t.Fatalf("read ingest %d: %v", id, err)
		}
		var blob map[string]any
		_ = json.Unmarshal([]byte(raw), &blob)
		return s.Ingest.Mode, blob
	}

	for _, c := range []struct {
		id      int64
		name    string
		latency float64
	}{{unset, "console-made", 200}, {noKey, "pre-mode", 350}} {
		got, blob := mode(c.id)
		if got != IngestSRT {
			t.Errorf("%q came through the upgrade with ingest mode %q; it accepted SRT "+
				"on the shared port before the upgrade and must still", c.name, got)
		}
		// Only the mode is written. Rewriting the blob through IngestSettings
		// would drop any key this build does not model.
		if lat, _ := blob["srt"].(map[string]any)["latencyMs"].(float64); lat != c.latency {
			t.Errorf("%q: srt.latencyMs = %v after the upgrade, want %v kept", c.name, lat, c.latency)
		}
	}
	if got, _ := mode(rtmp); got != IngestRTMP {
		t.Errorf("an rtmp source came through the upgrade as %q", got)
	}
	if got, _ := mode(pull); got != IngestPull {
		t.Errorf("a pull source came through the upgrade as %q", got)
	}
	var brokenRaw string
	if err := d.SQL().QueryRow(`SELECT ingest FROM sources WHERE id = ?`, broken).Scan(&brokenRaw); err != nil {
		t.Fatalf("read broken: %v", err)
	}
	if brokenRaw != `{not json` {
		t.Errorf("an unparseable ingest blob was rewritten to %q; it should be left for the "+
			"operator to see and fix", brokenRaw)
	}

	// The boot can say what it did, and says it once.
	if got := d.SourcesGivenSRTOnOpen(); len(got) != 2 {
		t.Errorf("SourcesGivenSRTOnOpen = %v on the upgrade boot, want the two unset sources", got)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	t.Cleanup(func() { again.Close() })
	if got := again.SourcesGivenSRTOnOpen(); len(got) != 0 {
		t.Errorf("the boot after the upgrade reported %v again; the migration is not idempotent", got)
	}
}

// A migration that cannot run must say so, not report an empty list: Open turns
// the error into a refused boot, and an empty list would read as "nothing to
// change" while every console-made source stayed unreachable over SRT.
func TestTheUnsetModeMigrationReportsAFailureItCannotRun(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "polyemesis.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := d.MigrateUnsetSourceIngestMode(); err == nil {
		t.Fatal("MigrateUnsetSourceIngestMode on a closed database returned nil; " +
			"a migration that did not run must not look like one that found nothing")
	}
	if got := d.SourcesGivenSRTOnOpen(); len(got) != 0 {
		t.Errorf("a failed migration recorded sources as changed: %v", got)
	}
}
