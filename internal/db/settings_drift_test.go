package db

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The same guard TestUITypesCarryEveryRenditionField applies to renditions,
// widened to the whole settings tree.
//
// That guard exists because this repo has repeatedly shipped features that were
// complete in every layer except the reachable one: validated, compiled into
// FFmpeg arguments, tested, and absent from ui/src. It caught the overlay
// column the day it landed. But it only ever walked db.Rendition -- so
// SlateSettings.ImagePath, a field with a validator, a path-confinement rule
// and a filter builder behind it, sat with no UI control at all and nothing
// watching.
//
// Settings is a tree of nested structs, so this walks it recursively and checks
// each leaf's json name is *nameable* somewhere in the UI's types. That is a
// looser test than the rendition one -- it does not check WHICH interface the
// name appears in -- and deliberately so: the settings types are a single
// interface with inline nested objects, and pinning the shape would fail on
// every harmless refactor while catching nothing extra. Absence is the failure
// mode worth catching, and absence is what this sees.
//
// Two limits, stated rather than discovered later:
//
//  1. NAMEABLE IS NOT REACHABLE. A field present in types.ts still needs a
//     control somewhere. recording.stems was flagged by an early draft of this
//     test and turned out to be fully reachable -- RecordingsPage read it
//     through a local `as {...}` cast instead of the shared type. Real gap,
//     lesser one.
//  2. It matches anywhere in the file. Deleting the reference to an interface
//     while leaving the interface itself still passes. What it does catch is a
//     field with NO type at all, which is exactly how the whole MQTT block
//     shipped.
func TestUITypesCanNameEverySettingsField(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "lib", "types.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)

	// Fields the UI deliberately does not carry, each with the reason. A name
	// added here is a decision; a name absent from both this list and the UI is
	// a feature no operator can reach.
	skip := map[string]string{
		// The engine overlays the source's ingest onto settings on the way out,
		// and the UI edits annotations through /source/annotations rather than
		// through the settings blob.
		"ingest.annotations": "edited through the annotations editor, not the settings form",
		// The failover playlist (DESIGN 2026-08-01-playlist-own-hub) has its
		// settings, validation and reload rules landing now, but no operator
		// control. Sub-project A -- this one -- covers settings, the tier,
		// the hub, sampling, an acceptance case and verification, and none of
		// its six tasks add a UI. The control belongs with sub-project B
		// (sequencing), where there is a playlist worth configuring rather
		// than a single file. Until B lands, both are knobs no operator can
		// reach.
		//
		// enabled is listed here too even though the guard currently passes
		// it: the guard matches by LEAF NAME, not dotted path, and "enabled:"
		// already appears throughout types.ts for dozens of unrelated
		// fields, so the pass is an accident of a common name colliding, not
		// evidence the UI can reach this field. Recording it turns that
		// accident into a decision, which is what this list is for.
		"failover.playlist.enabled": "UI control belongs with sub-project B2 (sequencing); passes today only because the leaf name \"enabled\" collides with unrelated fields, not because it is reachable",
		// filePath became items (DESIGN 2026-08-01-playlist-items): still no
		// operator control, and the reason has to name sub-project B2
		// specifically. "a later task of this sub-project" was written here
		// once before and had to be corrected, because none of this
		// sub-project's own tasks add a UI -- the control lands with B2.
		// Items is a slice of a struct, so walk descends into it and the leaf
		// this guard actually finds is the nested "upload" field, not "items"
		// itself -- a container name is never a control, only its leaves are.
		"failover.playlist.items.upload": "UI control belongs with sub-project B2 (sequencing), where there is a playlist worth configuring rather than a single file",
	}

	var missing []string
	walk(t, reflect.TypeOf(Settings{}), "", func(path, name string) {
		if _, ok := skip[path]; ok {
			return
		}
		// A field declaration in TypeScript: `name:` or `name?:`.
		//
		// NOT anchored to the start of a line: the settings types nest inline
		// object literals on one line (`srt: { passphrase: string; latencyMs:
		// number }`), and a line-anchored match reported every one of those as
		// missing. Requiring a brace, semicolon, comma or whitespace in front
		// keeps it from matching a substring of a longer identifier.
		if regexp.MustCompile(`(^|[{;,\s])` + regexp.QuoteMeta(name) + `\??\s*:`).MatchString(src) {
			return
		}
		missing = append(missing, path)
	})

	for _, p := range missing {
		t.Errorf("Settings.%s has no name anywhere in the UI's types. "+
			"A field the UI cannot name is a feature no operator can reach -- either "+
			"add it to types.ts and give it a control, or add it to the skip list "+
			"above with a reason.", p)
	}
}

// walk visits every leaf json field in a struct tree, reporting a dotted path
// and the leaf name.
//
// Slices and maps are followed into their element type: PlayoutSettings.Variants
// is where the variant fields live, and skipping them would leave the largest
// block of settings unguarded.
func walk(t *testing.T, rt reflect.Type, prefix string, visit func(path, name string)) {
	t.Helper()
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return
	}
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			// An untagged exported field still serialises, under its Go name.
			name = f.Name
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		ft := f.Type
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Map {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" && ft.Name() != "Time" {
			// A nested block: the container name itself is not a control, so
			// only its leaves are checked.
			walk(t, ft, path, visit)
			continue
		}
		visit(path, name)
	}
}

// The rendition guard's twin, for destinations.
//
// Destination gained a whole transport block, and nothing was watching that
// side of the tree either. Same reflection-driven shape as
// TestUITypesCarryEveryRenditionField so a field added to the Go struct is
// covered the day it lands.
func TestUITypesCanNameEveryDestinationField(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "lib", "types.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)

	skip := map[string]string{
		// The UI scopes destinations by the source it is already viewing and
		// never round-trips the id, exactly as it does for renditions.
		"sourceId": "the UI scopes by the source it is already viewing",
		// Server-assigned ordering, moved by drag rather than typed.
		"position": "reordered by drag, never entered as a number",
		// Expert mode is fully reachable, through its own endpoint and its own
		// payload shape: MonitoringPage edits ExpertArgs{inputArgs, outputArgs,
		// ackReencode}. The names differ from these json tags because the
		// endpoint returns the resolved command alongside them, so a
		// name-matching guard cannot see it. Reachable, differently named.
		"extraInputArgs":    "edited through the expert-mode endpoint as ExpertArgs.inputArgs",
		"extraOutputArgs":   "edited through the expert-mode endpoint as ExpertArgs.outputArgs",
		"expertAckReencode": "edited through the expert-mode endpoint as ExpertArgs.ackReencode",
	}

	var missing []string
	walk(t, reflect.TypeOf(Destination{}), "", func(path, name string) {
		if _, ok := skip[path]; ok {
			return
		}
		if regexp.MustCompile(`(^|[{;,\s])` + regexp.QuoteMeta(name) + `\??\s*:`).MatchString(src) {
			return
		}
		missing = append(missing, path)
	})

	for _, p := range missing {
		t.Errorf("Destination.%s has no name anywhere in the UI's types. "+
			"A field the UI cannot name is a feature no operator can reach.", p)
	}
}
