package uploads

import (
	"os"
	"path/filepath"
	"testing"
)

// #186. PutMedia/PutVerdict validated only that the name had no separator and
// was non-empty, so any other string wrote a ".probe-<name>.json" that nothing
// in the product ever reads or removes: readMedia is only consulted for names
// List returned, and removeVerdict is only called from Delete on a real upload.
//
// THE CONTROL IS FIRST AND IT IS NOT DECORATION. Every assertion below is that
// a write was REFUSED, and a PutVerdict that refused everything would satisfy
// all of them while breaking the one caller that matters.
func TestAVerdictCannotBeRecordedForANameNoUploadHas(t *testing.T) {
	s := mediaStore(t)
	writeUpload(t, s, "real-abcd1234.ts", "bytes")
	if err := s.PutMedia("real-abcd1234.ts", MediaInfo{AudioTracks: 2}); err != nil {
		t.Fatalf("the control failed, so every refusal below proves nothing: %v", err)
	}
	if v, recorded := s.Verdict("real-abcd1234.ts"); !recorded || !v.Verified {
		t.Fatalf("the control wrote no usable record: %+v recorded=%v", v, recorded)
	}

	for _, tc := range []struct{ name, why string }{
		{"absent-abcd1234.ts", "no file of that name is in the directory"},
		{"", "the empty name"},
		{".", "the directory itself"},
		{"..", "the parent directory"},
		{".probe-real-abcd1234.ts.json", "a verdict sidecar, which is not media"},
		{".partial-1234567.ts", "staged bytes, which are not published"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := dirNames(t, s)
			if err := s.PutVerdict(tc.name, VerifiedVerdict(MediaInfo{AudioTracks: 9})); err == nil {
				t.Errorf("PutVerdict recorded a PASS for %q (%s); nothing will ever "+
					"read or remove that sidecar", tc.name, tc.why)
			}
			if after := dirNames(t, s); len(after) != len(before) {
				t.Errorf("PutVerdict(%q) left %v behind, where the directory held %v",
					tc.name, after, before)
			}
		})
	}
}

// The staged and sidecar names above are the two that a Stat-based check would
// have accepted, because both have bytes at their path. Seed them so the
// refusal is about the NAME rather than about the file being absent -- without
// this the two cases prove only what "absent" already proves.
func TestAVerdictCannotBeRecordedForAStagedFileOrASidecarThatExists(t *testing.T) {
	s := mediaStore(t)
	writeUpload(t, s, "real-abcd1234.ts", "bytes")
	writeUpload(t, s, ".partial-1234567.ts", "staged bytes")
	writeUpload(t, s, ".probe-real-abcd1234.ts.json", `{"verified":true}`)

	for _, name := range []string{".partial-1234567.ts", ".probe-real-abcd1234.ts.json"} {
		if err := s.PutVerdict(name, VerifiedVerdict(MediaInfo{AudioTracks: 9})); err == nil {
			t.Errorf("PutVerdict wrote a record ABOUT %q, which has bytes at its "+
				"path and is still not an upload", name)
		}
		if _, err := os.Stat(filepath.Join(s.dir, sidecarName(name))); err == nil {
			t.Errorf("a sidecar for %q is on disk; nothing reads or removes it", name)
		}
	}
}

func dirNames(t *testing.T, s *Store) []string {
	t.Helper()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("read the uploads directory: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
