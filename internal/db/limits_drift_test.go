package db

import (
	"os"
	"path/filepath"
	"reflect"
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
		"chatHistoryMessages":     {MinChatHistoryMessages, MaxChatHistoryMessages},
		"alertRetryAttempts":      {MinAlertRetryAttempts, MaxAlertRetryAttempts},
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

// The bounds check above guards one axis of the mirroring hazard: a number that
// stops matching. It does not guard the other, which is a field the UI never
// grows at all -- and that is the axis that actually drifted.
//
// aspectMode, padColor and deinterlace were added to Rendition, persisted,
// validated, compiled into FFmpeg arguments and covered by tests, and never
// reached ui/src/lib/types.ts. Dual-format vertical output and deinterlacing
// were complete in every layer except the one a person touches, and nothing
// failed. It was the third time this project shipped a feature nobody could
// reach; DESIGN-ONE-PORT-ONLY.md records the first.
//
// A naive set difference over every mirrored type is NOT the fix. Measured on
// the tree the day this was written, that approach reported 19 missing fields
// across 5 types, and 16 of them were legitimate structural difference:
// ChatMessage nests its author fields in a ChatAuthor object, Destination
// models expert mode as its own ExpertArgs/ExpertGuard shape, Settings.failover
// is fetched separately. A check that cries wolf gets switched off within a
// month, so this one names the types it covers and requires a deliberate line
// to add another.
func TestUITypesCarryEveryRenditionField(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "lib", "types.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	iface, ok := tsInterface(string(raw), "Rendition")
	if !ok {
		t.Fatalf("no `export interface Rendition` in %s", path)
	}

	// Every json tag on db.Rendition, derived by reflection rather than typed
	// out here, so a field added to the Go struct is covered the day it lands.
	rt := reflect.TypeOf(Rendition{})
	for i := range rt.NumField() {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			continue
		}
		// sourceId is deliberately absent: the UI scopes renditions by the
		// source it is already viewing and never round-trips the id.
		if name == "sourceId" {
			continue
		}
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\??\s*:`).MatchString(iface) {
			t.Errorf("Rendition.%s is absent from the UI's Rendition interface. "+
				"A field the UI cannot name is a feature no operator can reach -- "+
				"either add it to types.ts and give it a control, or add it to the "+
				"skip list above with a reason", name)
		}
	}
}

// tsInterface returns the body of `export interface <name> { ... }`.
func tsInterface(src, name string) (string, bool) {
	start := strings.Index(src, "export interface "+name+" {")
	if start < 0 {
		return "", false
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		return "", false
	}
	return src[start : start+end], true
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
