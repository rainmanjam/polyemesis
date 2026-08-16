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
		// UNEXPORTED USUALLY MEANS "NEVER ON THE WIRE", AND EMBEDDING IS THE
		// EXCEPTION. encoding/json promotes the EXPORTED fields of an anonymous
		// embedded struct even when the embedded type itself is unexported, so
		// `struct{ announcementSet }` puts announcementSet's exported leaves in
		// the enclosing object under their own names. Skipping the field would
		// have hidden every one of them from this guard -- leaves that are
		// stored, readable, and unchecked, which is the precise blind spot this
		// walk exists to close.
		//
		// Only structs. An unexported embedded field of any other type really
		// is dropped by encoding/json, and so is any ordinary unexported field.
		if !f.IsExported() {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if !f.Anonymous || et.Kind() != reflect.Struct {
				continue
			}
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		// An ANONYMOUS embedded struct whose json tag names nothing is INLINED
		// by encoding/json: its leaves are keys of the enclosing object, and the
		// type's own name appears in no JSON anywhere. Recursing under the
		// PARENT's prefix is what keeps this walk agreeing with the bytes --
		// FacebookSettings embeds db.AnnouncementSet, and without this the guard
		// demands the UI name "facebook.AnnouncementSet.announcements", which
		// nothing has ever sent or stored.
		//
		// Only when the tag names nothing: `json:"x"` on an embedded field DOES
		// nest it, and then the block name is real.
		// INLINED MEANS EMBEDDED STRUCT, AND encoding/json IS STRICTER THAN
		// "anonymous with no tag". An embedded *struct is inlined too, but an
		// embedded NAMED SLICE or MAP is not -- `type Tags []string` embedded
		// anonymously nests under "Tags" and its elements are not keys of the
		// enclosing object. The test below was computed BEFORE the deref loop,
		// which strips slices and maps as well as pointers, so such a field
		// would have been walked as inlined and every path under it would have
		// been off by one segment.
		//
		// Nothing in the tree embeds a named slice today. This matters because
		// THIS diff is what makes anonymous embedding the sharing pattern here:
		// the next person to reach for it gets a walker that agrees with the
		// bytes, or a guard that quietly checks the wrong paths.
		//
		// So the inline test sees through POINTERS ONLY and stops there.
		embedded := f.Type
		for embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}
		inlined := f.Anonymous && name == "" && embedded.Kind() == reflect.Struct
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
			if inlined {
				walk(t, ft, prefix, visit)
			} else {
				walk(t, ft, path, visit)
			}
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
		// The announcement markers are bookkeeping the pre-announce sweep writes
		// and nobody types. What the operator sees of them IS reachable and is
		// checked: facebook.broadcastId and facebook.scheduledFor mirror the
		// soonest announced show and are what the card links to. The per-show
		// entries behind that mirror have no control and never will -- an
		// operator cannot choose which schedule owns which live_video.
		"facebook.announcements.scheduleId":  "written by the pre-announce sweep, never entered",
		"facebook.announcements.occurrence":  "written by the pre-announce sweep, never entered",
		"facebook.announcements.broadcastId": "written by the pre-announce sweep; the mirror at facebook.broadcastId is what the card shows",
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
