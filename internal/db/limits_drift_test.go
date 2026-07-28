package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The UI mirrors these bounds in ui/src/lib/limits.ts so a numeric input can
// refuse an out-of-range value at the keystroke instead of accepting it and
// reporting the problem a round trip later.
//
// Mirrored constants drift, and this pair drifts silently in the worst
// direction: if the UI permits more than the server accepts, the operator gets
// a save button that does nothing and an error message they cannot connect to
// the field they typed in. If it permits less, a legitimate value becomes
// untypeable with no explanation at all.
//
// So the mirror is checked rather than trusted. This test reads the TypeScript
// and fails when a number stops matching its Go source.
func TestUIBoundsMatchTheGoValidators(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "lib", "limits.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		// Not skipped: this file existing is the point. A UI build that dropped
		// it would leave every input unbounded again, silently.
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)

	want := map[string][2]int{
		"port":                    {1, 65535},
		"srtLatencyMs":            {20, 8000},
		"srtPassphrase":           {10, 79},
		"renditionDimension":      {MinRenditionDimension, MaxRenditionDimension},
		"renditionFPS":            {1, MaxRenditionFPS},
		"renditionBitrateKbps":    {MinRenditionBitrate, MaxRenditionBitrate},
		"renditionGOPSeconds":     {int(MinRenditionGOP), int(MaxRenditionGOP)},
		"audioBitrateKbps":        {32, 512},
		"recordingSegmentSeconds": {10, 86400},
	}

	for name, bounds := range want {
		got, ok := parseBound(src, name)
		if !ok {
			t.Errorf("%s is missing from limits.ts; its input is unbounded in the UI", name)
			continue
		}
		if got != bounds {
			t.Errorf("%s: limits.ts says {min:%d,max:%d}, Go says {min:%d,max:%d} -- "+
				"the form and the validator disagree, so one of them is lying to the operator",
				name, got[0], got[1], bounds[0], bounds[1])
		}
	}
}

// parseBound pulls `name: { min: N, max: M }` out of the TypeScript, tolerating
// numeric separators (100_000) and whitespace.
func parseBound(src, name string) ([2]int, bool) {
	re := regexp.MustCompile(name + `\s*:\s*\{\s*min:\s*([0-9_]+)\s*,\s*max:\s*([0-9_]+)`)
	m := re.FindStringSubmatch(src)
	if len(m) != 3 {
		return [2]int{}, false
	}
	lo, err1 := strconv.Atoi(strings.ReplaceAll(m[1], "_", ""))
	hi, err2 := strconv.Atoi(strings.ReplaceAll(m[2], "_", ""))
	if err1 != nil || err2 != nil {
		return [2]int{}, false
	}
	return [2]int{lo, hi}, true
}
