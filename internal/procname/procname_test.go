package procname

import "testing"

func TestDestProcessNameKeepsThePrimaryNameAndSuffixesTheRoles(t *testing.T) {
	for _, c := range []struct {
		id   int64
		role string
		want string
	}{
		{7, RolePrimary, "dest:7"},
		{7, RoleBackup, "dest:7:backup"},
		{12, RoleDelay, "dest:12:delay"},
		{3, RoleCaption, "dest:3:caption"},
	} {
		if got := DestProcessName(c.id, c.role); got != c.want {
			t.Errorf("DestProcessName(%d, %q) = %q, want %q", c.id, c.role, got, c.want)
		}
	}
}

// The kinds are wire values: the process table, the census and the teardown
// tallies print them, and the UI filters on them.
func TestTheKindsAreTheWireValues(t *testing.T) {
	want := map[Kind]string{
		KindIngest: "ingest", KindSource: "source", KindDestination: "destination",
		KindRecorder: "recorder", KindPreview: "preview", KindMeters: "meters",
		KindLoudness: "loudness", KindRendition: "rendition", KindSilence: "silence",
		KindPlayout: "playout",
	}
	for k, s := range want {
		if string(k) != s {
			t.Errorf("kind %q changed its wire value from %q", string(k), s)
		}
	}
}
