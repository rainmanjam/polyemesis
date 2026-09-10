package db

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
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
// each leaf's json name is *nameable* within the UI's Settings type. Looser than
// the rendition guard still: it does not check WHICH nested block of that type
// the name appears in, and deliberately so, because the settings types are one
// interface with inline nested objects and pinning the exact shape would fail on
// every harmless refactor while catching nothing extra. Absence is the failure
// mode worth catching, and absence is what this sees.
//
// Two limits were stated here rather than discovered later. Both have since
// been paid for, and what is left of each is written out below.
//
//  1. NAMEABLE IS NOT REACHABLE, and this test still cannot see the difference:
//     it reads declarations, and a declaration is not a control. That sentence
//     cost eight findings in the #788 reachability sweep -- whole blocks
//     validated, stored and acted on, with no box anywhere in the web UI, while
//     every guard in the repository stayed green.
//
//     ui/src/lib/settings-reachability.test.tsx is the guard that measures what
//     this one only implies. It renders the settings screens into jsdom and
//     asks, of every leaf, whether any control's displayed value moves with it.
//     The two are a chain and neither half is sufficient: this asserts every Go
//     field is NAMED in types.ts, that one asserts every leaf named in types.ts
//     is REACHABLE. A field that fails here is invisible to that one, because
//     it never reaches the type it walks.
//
//     Which is why the search below is now scoped. See the next paragraph.
//
//  2. It used to match anywhere in the file, and that was not a small looseness.
//     types.ts is three thousand lines and a hundred interfaces, so a common
//     field name matched SOMETHING almost always: three of the sweep's findings
//     were fields that passed this guard on an interface they have no relation
//     to -- `sourceId` on Destination, `encoder` and `preset` on Rendition. A
//     guard that answers yes to a name it found in the wrong place is worse than
//     no guard, because it is read as an answer.
//
//     uiTypeTree confines the search to the interface tree the UI actually
//     reaches this struct through: `export interface Settings`, plus every
//     interface named transitively inside it. Still a text scan, and still
//     loose WITHIN that tree -- it does not check which nested block the name
//     sits in, and the reasoning for that has not changed: the settings types
//     nest inline object literals, and pinning the exact shape would fail on
//     every harmless refactor while catching nothing extra. What it no longer
//     does is find the name in a type nothing in this tree points at.
func TestUITypesCanNameEverySettingsField(t *testing.T) {
	src := uiTypeTree(t, "Settings")

	// Fields the UI deliberately does not carry through THIS type, each with the
	// route it is carried through instead. A name added here is a decision; a
	// name absent from both lists and from the tree is a feature no operator can
	// reach.
	//
	// A path here covers everything under it. A block is edited through another
	// endpoint as a block, not leaf by leaf, and listing its leaves one at a time
	// would mean a field added to that block silently inheriting the excuse --
	// which is the same trade the walk itself makes about nested structs.
	elsewhere := map[string]string{
		// The engine overlays the source's ingest onto settings on the way out,
		// and the UI edits annotations through /source/annotations rather than
		// through the settings blob.
		"ingest.annotations": "edited through the annotations editor, not the settings form",
		// GET/PUT /jobs/policy carries this block on its own, typed
		// PostProdSettings -- api.jobPolicy and api.putJobPolicy -- and
		// AutomationPage edits it there. types.ts's `Settings` does not name it
		// at all, which is correct: the settings blob is not how it travels.
		//
		// It passed this guard for as long as the search was the whole file:
		// PostProdSettings is declared in types.ts for the other endpoint, so
		// every one of its twenty-odd field names matched something. Scoped to
		// the Settings tree the match is gone, and the honest answer is not
		// "missing" but "carried somewhere else".
		"postProd": "carried by GET/PUT /jobs/policy as PostProdSettings, which the Settings tree does not reference",
	}

	// KNOWN GAPS -- fields with no name in this tree and no other route either.
	// Not a decision; a finding, parked where the next person to touch the block
	// will read it. The same shape as NO_CONTROL_YET in
	// ui/src/lib/settings-reachability.test.tsx, and for the same reason: the
	// suite has to be green to be worth reading, and a bug that is green and
	// unrecorded is how these accumulate in the first place.
	notYet := map[string]string{
		// #788 sweep. WHICH PROGRAMME the public page serves, on an install with
		// more than one. types.ts's PlayoutSettings has no such field and
		// PlayoutPage has no control for it, so a two-programme install cannot
		// choose what its audience sees -- the exact defect SourceID was added
		// to fix, undone at the last layer.
		//
		// This is one of the three fields the whole-file search hid: `sourceId`
		// matched on Destination, which has nothing to do with playout, and the
		// guard reported it carried.
		"playout.sourceId": "#788 sweep: no field in the UI's PlayoutSettings and no control on PlayoutPage",
	}

	var missing []string
	walk(t, reflect.TypeOf(Settings{}), "", func(path, name string) {
		if excused(path, elsewhere, notYet) {
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
		t.Errorf("Settings.%s has no name in the UI's Settings type. "+
			"A field the UI cannot name is a feature no operator can reach -- either "+
			"add it to types.ts and give it a control (which "+
			"ui/src/lib/settings-reachability.test.tsx will then require), or add it "+
			"to one of the lists above with the route it travels by.", p)
	}
}

// excused reports whether a dotted path, or any block containing it, is named in
// one of the lists above.
//
// PREFIX, not equality, and it has to be: the walk visits LEAVES, so a list
// naming a block ("postProd") would never match "postProd.niceLevel" and the
// block would be reported field by field. Bounded to whole segments -- "playout"
// does not excuse "playoutExtra" -- because a prefix that stops mid-name would
// hand out excuses to fields nobody wrote one for.
func excused(path string, lists ...map[string]string) bool {
	for _, list := range lists {
		for prefix := range list {
			if path == prefix || strings.HasPrefix(path, prefix+".") {
				return true
			}
		}
	}
	return false
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
//
// Scoped to the Destination tree for the reason given above, and this is where
// the looseness cost the most: `sourceId` and `position` were BOTH matching
// elsewhere in types.ts before the skip list below was written, and `encoder`
// and `preset` still match Rendition, which a Destination does not carry.
func TestUITypesCanNameEveryDestinationField(t *testing.T) {
	src := uiTypeTree(t, "Destination")

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

// uiTypeTree is the text of one interface in ui/src/lib/types.ts together with
// every interface reachable from it, and it is what turns the two guards above
// from "this name appears somewhere in the file" into "this name appears in the
// type the UI reads this struct through".
//
// WHY THIS EXISTS. types.ts is three thousand lines and a hundred interfaces, so
// a bare substring search over it answers yes to almost any plausible field
// name. Three findings in the #788 reachability sweep were exactly that:
// `sourceId` matched on an unrelated interface, `encoder` and `preset` matched
// on Rendition, and the destination guard reported them carried when nothing
// about a destination mentions either. The guard was not wrong about what it
// measured. It was measuring the wrong thing, and reading as though it had
// measured the right one -- which is the more expensive kind of wrong, because
// it is quoted as evidence.
//
// HOW IT IS BOUNDED. Start at `export interface <root>`, take its body, and
// follow every capitalised identifier in that body that is itself an exported
// interface, transitively. That OVER-collects: an identifier in a type position
// is indistinguishable here from one anywhere else in the body. Over-collecting
// is the safe direction -- the worst case is the old behaviour confined to a
// subtree, and the wrong answer it can still produce is a name found in a type
// this tree really does reference, never in one nothing points at.
//
// COMMENTS ARE BLANKED FIRST, and that is not tidiness. types.ts is heavily
// commented and the comments name types in prose -- "Mirrors
// db.FacebookSettings, which this mirrors field for field". Without stripping,
// a type merely DISCUSSED next to the root would be walked into the tree, which
// puts the whole-file looseness straight back by another route.
//
// A TEXT SCAN, not a TypeScript parse, for the reason testenv.StripJSComments
// gives at length: a Go test that shells out to a TypeScript toolchain skips
// itself on every machine without one, and a guard that skips silently is worse
// than no guard because the next person reads green and believes it.
func uiTypeTree(t *testing.T, root string) string {
	t.Helper()
	src := testenv.StripJSComments(testenv.ReadUI(t, "lib", "types.ts"))

	// Every exported interface in the file, by name, with its body text.
	bodies := map[string]string{}
	decl := regexp.MustCompile(`export interface (\w+)[^{]*\{`)
	for _, m := range decl.FindAllStringSubmatchIndex(src, -1) {
		name := src[m[2]:m[3]]
		if body, ok := braceBody(src, m[1]-1); ok {
			bodies[name] = body
		}
	}
	// FATAL, not an empty tree. An empty tree makes every field "missing" and
	// reports a hundred failures whose real cause is one renamed interface --
	// or, if a future caller inverts the assertion, passes over nothing at all.
	if _, ok := bodies[root]; !ok {
		t.Fatalf("ui/src/lib/types.ts declares no `export interface %s`. Either it "+
			"was renamed -- in which case this guard has been searching a tree that "+
			"does not exist -- or the UI no longer carries this type at all.", root)
	}

	ident := regexp.MustCompile(`\b[A-Z]\w*\b`)
	seen := map[string]bool{root: true}
	queue := []string{root}
	var tree strings.Builder
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		body := bodies[name]
		tree.WriteString(body)
		tree.WriteString("\n")
		for _, ref := range ident.FindAllString(body, -1) {
			if _, isInterface := bodies[ref]; isInterface && !seen[ref] {
				seen[ref] = true
				queue = append(queue, ref)
			}
		}
	}
	return tree.String()
}

// braceBody returns the text between the brace at `open` and its match.
//
// The second result is false for an unbalanced file. Returning the remainder of
// the file instead would hand the caller exactly the whole-file search
// uiTypeTree exists to end, and it would do it silently, on the one input --
// a types.ts somebody has half-edited -- where nobody is looking for it.
func braceBody(src string, open int) (string, bool) {
	if open < 0 || open >= len(src) || src[open] != '{' {
		return "", false
	}
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open+1 : i], true
			}
		}
	}
	return "", false
}
