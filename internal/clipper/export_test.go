package clipper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// exportJob builds a finished clip.export whose Result names path.
func exportJob(t *testing.T, path string) jobs.Job {
	t.Helper()
	raw, err := json.Marshal(JobResult{Path: path, Mode: ModeFast})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return jobs.Job{ID: 7, Kind: JobKind, Result: raw}
}

// TestRemoveExportDeletesTheFileTheJobOwned is the positive half, and it runs
// first for the reason the download confinement test spells out: a test that
// only checks refusals passes with the whole function stubbed out.
func TestRemoveExportDeletesTheFileTheJobOwned(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ExportSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir exports: %v", err)
	}
	path := filepath.Join(dir, "highlight.mp4")
	if err := os.WriteFile(path, []byte("CLIPBYTES"), 0o644); err != nil {
		t.Fatalf("seed export: %v", err)
	}

	removed, err := RemoveExport(dir, exportJob(t, path))
	if err != nil {
		t.Fatalf("RemoveExport: %v", err)
	}
	if !removed {
		t.Error("RemoveExport reported nothing removed for a job that owned a file")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the exported file survived its job: %v", err)
	}
}

// TestRemoveExportRefusesAPathOutsideTheExportsDir is the same guard the
// download route carries, and it matters MORE here: serving the wrong file
// leaks it, removing the wrong file destroys it.
func TestRemoveExportRefusesAPathOutsideTheExportsDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ExportSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir exports: %v", err)
	}
	// A recording, which is precisely the thing a tampered row would aim at.
	canary := filepath.Join(root, "session.mkv")
	if err := os.WriteFile(canary, []byte("MASTER"), 0o644); err != nil {
		t.Fatalf("seed canary: %v", err)
	}

	for _, stored := range []string{
		canary,
		filepath.Join(dir, "..", "session.mkv"),
	} {
		removed, err := RemoveExport(dir, exportJob(t, stored))
		if err == nil {
			t.Errorf("RemoveExport(%q) was allowed outside the exports directory", stored)
		}
		if removed {
			t.Errorf("RemoveExport(%q) reported a removal", stored)
		}
		if _, err := os.Stat(canary); err != nil {
			t.Fatalf("a file outside the exports directory was deleted: %v", err)
		}
	}
}

// TestRemoveExportIgnoresJobsThatOwnNoExport keeps the delete and purge paths
// from failing over work that never wrote a file. Every other job kind goes
// through the same call.
func TestRemoveExportIgnoresJobsThatOwnNoExport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ExportSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir exports: %v", err)
	}
	gone := filepath.Join(dir, "already-deleted.mp4")

	cases := []struct {
		name string
		job  jobs.Job
	}{
		{"another kind entirely", jobs.Job{ID: 1, Kind: "media.proxy",
			Result: json.RawMessage(`{"path":"/etc/passwd"}`)}},
		{"an export with no result", jobs.Job{ID: 2, Kind: JobKind}},
		{"a result that does not parse", jobs.Job{ID: 3, Kind: JobKind,
			Result: json.RawMessage(`{oh no`)}},
		{"a result naming no path", jobs.Job{ID: 4, Kind: JobKind,
			Result: json.RawMessage(`{"bytes":12}`)}},
		{"a file already gone", exportJob(t, gone)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			removed, err := RemoveExport(dir, tc.job)
			if err != nil {
				t.Errorf("RemoveExport: %v", err)
			}
			if removed {
				t.Error("reported a removal where there was no file to remove")
			}
		})
	}
}
