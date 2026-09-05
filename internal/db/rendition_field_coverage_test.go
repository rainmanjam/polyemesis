package db

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// EVERY rendition field survives Create, survives Update, and comes back from
// Get -- and "every" is counted by reflection rather than by whoever last
// touched this file.
//
// The old round-trip test compared nine fields by hand against a struct that
// has thirty-one. Deleting `pad_color=?` and `r.PadColor` from UpdateRendition
// left the whole package green, because pad_color was not one of the nine: the
// column still existed, Create still wrote it, Get still read it, and the only
// evidence was that editing an existing rendition's letterbox colour did
// nothing -- while the API handler returned the re-read row, so the form even
// showed the value that had just failed to save.
//
// A hand-written comparison is rung zero wearing a test's clothes. It covers
// exactly the fields somebody remembered on the day, it goes stale the moment
// a field is added, and its staleness is invisible: nothing fails, the number
// of assertions simply stops keeping up with the number of fields. So this one
// does not name the fields at all. It walks the struct, and three reflective
// checks make an incomplete fixture a failing test:
//
//   - every field of the create fixture is non-zero, so a column dropped from
//     the INSERT shows up as the column default coming back instead;
//   - every field of the update fixture DIFFERS from the create fixture's, so
//     a column dropped from the UPDATE shows up as the old value surviving;
//   - the round trip is compared field by field over the same walk, so a
//     column dropped from the SELECT, or a value bound out of order in
//     renditionValues, shows up as a mismatch that names the field.
//
// A fourth check covers the one mistake the three above cannot see. Swapping
// two entries in renditionValueColumns is symmetric -- the INSERT writes
// PadColor into the deinterlace column and the SELECT reads the deinterlace
// column back into PadColor -- so it round-trips perfectly on a fresh database
// while scrambling every row that was already stored. That was measured, not
// assumed: the swap was applied and the three checks above passed. So
// assertRenditionColumnsBindTheirOwnFields pins each column to the field whose
// name it spells, which is the association a symmetric swap breaks.
//
// The consequence worth stating: ADD A FIELD TO Rendition AND THIS TEST FAILS
// UNTIL THE FIXTURES BELOW CARRY IT. That is the whole device. Nobody has to
// remember to extend the coverage, because the coverage is derived from the
// type and the type is what changed.
func TestARenditionKeepsEveryFieldThroughCreateUpdateAndGet(t *testing.T) {
	d := testDB(t)
	other := secondProgramme(t, d)

	first, err := d.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}

	created := fullyPopulatedRendition(&first)
	updated := differentlyPopulatedRendition(&other.ID)

	// The fixtures are the test. Check them before touching the database, so a
	// fixture that has fallen behind the struct reports itself as a fixture
	// problem rather than as a storage bug.
	assertEveryRenditionFieldSet(t, "create fixture", created)
	assertRenditionFieldsAllDiffer(t, created, updated)
	assertRenditionFixtureValuesAreDistinct(t, created)
	assertRenditionColumnsBindTheirOwnFields(t, created)

	got, err := d.CreateRendition(created)
	if err != nil {
		t.Fatalf("CreateRendition: %v", err)
	}
	assertRenditionFieldsMatch(t, "after CreateRendition", created, got)

	reread, err := d.GetRendition(got.ID)
	if err != nil {
		t.Fatalf("GetRendition: %v", err)
	}
	assertRenditionFieldsMatch(t, "after GetRendition", created, reread)
	if reread.CreatedAt.IsZero() || reread.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: created=%v updated=%v", reread.CreatedAt, reread.UpdatedAt)
	}

	updated.ID = got.ID
	if _, err := d.UpdateRendition(updated); err != nil {
		t.Fatalf("UpdateRendition: %v", err)
	}
	afterUpdate, err := d.GetRendition(got.ID)
	if err != nil {
		t.Fatalf("GetRendition after update: %v", err)
	}
	assertRenditionFieldsMatch(t, "after UpdateRendition", updated, afterUpdate)

	// ListRenditions and ListRenditionsBySource share scanRendition with Get,
	// but they do not share the query, and a column dropped from one of those
	// two SELECT lists would be invisible above.
	list, err := d.ListRenditionsBySource(other.ID)
	if err != nil {
		t.Fatalf("ListRenditionsBySource: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListRenditionsBySource(%d) = %d renditions, want the one just moved there",
			other.ID, len(list))
	}
	assertRenditionFieldsMatch(t, "via ListRenditionsBySource", updated, list[0])
}

// fullyPopulatedRendition is a valid rendition with NOTHING left at its zero
// value. Every value is deliberately distinguishable from both the column
// default and from its opposite number in differentlyPopulatedRendition.
//
// It has to stay valid, so the values are drawn from what Validate accepts:
// even dimensions in range, a known encoder, a preset that is a bare token, an
// aspect mode that needs both axes set (they are), an overlay and a caption
// that are both active, and percentages inside their documented bands.
func fullyPopulatedRendition(source *int64) *Rendition {
	return &Rendition{
		Name:         "1080p60 everything",
		Width:        1920,
		Height:       1080,
		FPS:          60,
		VideoBitrate: 6000,
		MaxrateKbps:  8000,
		BufsizeKbps:  16000,
		Encoder:      EncoderX264,
		Preset:       "veryfast",
		GOPSeconds:   2,
		AspectMode:   "pad",
		Deinterlace:  "auto",
		PadColor:     "black",
		Overlay: RenditionOverlay{
			Image:      "overlays/station-logo.png",
			Anchor:     "top-left",
			WidthPct:   0.12,
			MarginXPct: 0.03,
			MarginYPct: 0.04,
			Opacity:    0.8,
		},
		Text: RenditionText{
			Content:    "STUDIO A",
			Font:       "DejaVuSans.ttf",
			Anchor:     "bottom-left",
			SizePct:    0.05,
			Color:      "white",
			MarginXPct: 0.02,
			MarginYPct: 0.06,
			Box:        true,
			BoxColor:   "0x112233",
			BoxOpacity: 0.5,
		},
		Note:     "the tier every field of this test is measured on",
		SourceID: source,
	}
}

// differentlyPopulatedRendition is the same shape with a DIFFERENT value in
// every single field, which is what turns "the update wrote it" into an
// observation rather than a hope: a set-clause that never mentions a column
// leaves the create fixture's value in place, and only a differing pair can
// see that.
//
// Text.Box is the one field that cannot be non-zero in both fixtures, being a
// bool. It is true in the create fixture and false here, which is the right
// way round: the create fixture is the one that has to differ from the column
// default for a dropped INSERT column to show.
func differentlyPopulatedRendition(source *int64) *Rendition {
	return &Rendition{
		Name:         "720p30 everything else",
		Width:        1280,
		Height:       720,
		FPS:          30,
		VideoBitrate: 2500,
		MaxrateKbps:  3000,
		BufsizeKbps:  6000,
		Encoder:      EncoderNVENCH264,
		Preset:       "p6",
		GOPSeconds:   1.5,
		AspectMode:   "crop",
		Deinterlace:  "all",
		PadColor:     "0x101010",
		Overlay: RenditionOverlay{
			Image:      "overlays/other-logo.png",
			Anchor:     "bottom-right",
			WidthPct:   0.2,
			MarginXPct: 0.05,
			MarginYPct: 0.07,
			Opacity:    0.6,
		},
		Text: RenditionText{
			Content:    "STUDIO B",
			Font:       "DejaVuSerif.ttf",
			Anchor:     "top-right",
			SizePct:    0.09,
			Color:      "0xffcc00",
			MarginXPct: 0.08,
			MarginYPct: 0.09,
			Box:        false,
			BoxColor:   "0x223344",
			BoxOpacity: 0.25,
		},
		Note:     "the tier the update has to actually reach",
		SourceID: source,
	}
}

// renditionOwnedFields are the fields the STORE fills in, not the caller: the
// table assigns the id and CreateRendition/UpdateRendition stamp the times. A
// fixture cannot be expected to set them and the two fixtures cannot be
// expected to differ in them, so they sit outside every check below. They are
// asserted directly in the test instead.
var renditionOwnedFields = map[string]bool{
	"ID": true, "CreatedAt": true, "UpdatedAt": true,
}

// renditionFieldValues flattens a rendition into "field path" -> value,
// recursing into the nested Overlay and Text structs and dereferencing
// pointers so a *int64 compares by what it points at rather than by where it
// points.
//
// Derived from the type by reflection, which is the point: a field added to
// Rendition, RenditionOverlay or RenditionText appears here on the next
// compile, with nobody having been asked to remember it.
func renditionFieldValues(r *Rendition) map[string]any {
	out := map[string]any{}
	var walk func(prefix string, v reflect.Value)
	walk = func(prefix string, v reflect.Value) {
		typ := v.Type()
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.PkgPath != "" {
				continue // unexported; no column can carry it
			}
			path := prefix + f.Name
			if prefix == "" && renditionOwnedFields[f.Name] {
				continue
			}
			fv := v.Field(i)
			switch {
			case fv.Kind() == reflect.Pointer:
				if fv.IsNil() {
					out[path] = nil
					continue
				}
				out[path] = fv.Elem().Interface()
			case fv.Kind() == reflect.Struct && fv.Type() != reflect.TypeOf(time.Time{}):
				walk(path+".", fv)
			default:
				out[path] = fv.Interface()
			}
		}
	}
	walk("", reflect.ValueOf(*r))
	return out
}

// assertEveryRenditionFieldSet fails naming any field left at its zero value.
//
// This is the check that makes a NEW field self-reporting: a field nobody
// added to the fixture is zero, a zero field is indistinguishable from the
// column default, and a field indistinguishable from its column default proves
// nothing about whether the INSERT wrote it.
func assertEveryRenditionFieldSet(t *testing.T, what string, r *Rendition) {
	t.Helper()
	var unset []string
	for path, v := range renditionFieldValues(r) {
		if v == nil || reflect.ValueOf(v).IsZero() {
			unset = append(unset, path)
		}
	}
	sort.Strings(unset)
	if len(unset) > 0 {
		t.Fatalf("%s leaves these fields at their zero value:\n  %s\n\n"+
			"A zero field is the same value the column defaults to, so storing it "+
			"proves nothing -- a Create that never wrote the column would pass. "+
			"Give each of them a distinguishable value in fullyPopulatedRendition "+
			"(and a different one in differentlyPopulatedRendition). If you have "+
			"just added a field to Rendition, this is that field asking to be "+
			"covered.", what, strings.Join(unset, "\n  "))
	}
}

// assertRenditionFieldsAllDiffer fails naming any field the two fixtures agree
// on. An UPDATE that never mentions a column leaves what Create wrote, so a
// field with the same value in both fixtures can never show that.
func assertRenditionFieldsAllDiffer(t *testing.T, a, b *Rendition) {
	t.Helper()
	av, bv := renditionFieldValues(a), renditionFieldValues(b)
	var same []string
	for path, want := range av {
		if reflect.DeepEqual(want, bv[path]) {
			same = append(same, fmt.Sprintf("%s (both %v)", path, want))
		}
	}
	sort.Strings(same)
	if len(same) > 0 {
		t.Fatalf("the create and update fixtures carry the same value for:\n  %s\n\n"+
			"An UPDATE that never mentions a column leaves the value Create wrote, "+
			"so these fields cannot tell a working set-clause from a missing one. "+
			"Give differentlyPopulatedRendition a different value for each.",
			strings.Join(same, "\n  "))
	}
}

// assertRenditionFixtureValuesAreDistinct fails if two fields of the create
// fixture carry the same value.
//
// It is what gives assertRenditionColumnsBindTheirOwnFields its teeth. That
// check recognises a field by its value, so two fields sharing one value are
// two fields it cannot tell apart -- and a swap between exactly those two
// would slip past. Distinctness is free to arrange in a fixture and turns "a
// swap is usually caught" into "a swap is always caught".
func assertRenditionFixtureValuesAreDistinct(t *testing.T, r *Rendition) {
	t.Helper()
	seen := map[string]string{}
	var clashes []string
	for path, v := range renditionFieldValues(r) {
		k := fmt.Sprintf("%T:%v", v, v)
		if other, dup := seen[k]; dup {
			lo, hi := path, other
			if lo > hi {
				lo, hi = hi, lo
			}
			clashes = append(clashes, fmt.Sprintf("%s and %s are both %v", lo, hi, v))
			continue
		}
		seen[k] = path
	}
	sort.Strings(clashes)
	if len(clashes) > 0 {
		t.Fatalf("fullyPopulatedRendition uses one value for two fields:\n  %s\n\n"+
			"The column-binding check below identifies a field by its value, so a "+
			"shared value is a pair of columns it cannot tell apart -- and a swap "+
			"between exactly those two would go unnoticed. Give each field its own "+
			"value.", strings.Join(clashes, "\n  "))
	}
}

// assertRenditionColumnsBindTheirOwnFields checks that the Nth name in
// renditionValueColumns and the Nth value out of renditionValues belong to the
// SAME field.
//
// The mistake it exists for is a symmetric one, and symmetric mistakes are the
// ones that survive round-trip tests. Swap pad_color and deinterlace in
// renditionValueColumns and nothing observable changes on a fresh install: the
// INSERT puts the pad colour in the deinterlace column, the SELECT -- built
// from the same swapped list -- puts the deinterlace column back into PadColor,
// and every assertion about what went in coming out again passes. What breaks
// is every rendition ALREADY IN the table, whose pad_color really does hold a
// pad colour: after the swap those rows read back as a rendition whose
// deinterlace mode is "black", which Validate refuses and the encoder cannot
// run. The damage is entirely to the installed base, which is exactly the
// population a fresh-database test never has.
//
// The pin is the column's own name. Every rendition column is its field's name
// in snake case -- video_bitrate/VideoBitrate, overlay_width_pct/
// Overlay.WidthPct, source_id/SourceID -- so lowercasing and dropping the
// punctuation makes the two directly comparable, with no table of mappings to
// fall out of date. A future column that does not follow the convention fails
// here and says so; renaming it is the fix.
func assertRenditionColumnsBindTheirOwnFields(t *testing.T, r *Rendition) {
	t.Helper()

	squash := func(s string) string {
		return strings.NewReplacer("_", "", ".", "").Replace(strings.ToLower(s))
	}
	byName := map[string]any{}
	for path, v := range renditionFieldValues(r) {
		byName[squash(path)] = v
	}

	cols := strings.Split(renditionValueColumns, ",")
	vals := renditionValues(r)
	if len(cols) != len(vals) {
		// init() already refuses to let the package load in this state; the
		// check is here so a reader is not left wondering what happens next.
		t.Fatalf("renditionValueColumns names %d columns and renditionValues "+
			"returns %d values", len(cols), len(vals))
	}

	var wrong []string
	for i, c := range cols {
		name := squash(strings.TrimSpace(c))
		want, known := byName[name]
		if !known {
			wrong = append(wrong, fmt.Sprintf(
				"column %q names no field of Rendition", strings.TrimSpace(c)))
			continue
		}
		got := vals[i]
		// renditionValues hands SourceID over as the pointer the column takes;
		// the field walk dereferences it. Compare what they point at.
		if rv := reflect.ValueOf(got); rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				got = nil
			} else {
				got = rv.Elem().Interface()
			}
		}
		if !reflect.DeepEqual(got, want) {
			wrong = append(wrong, fmt.Sprintf(
				"column %q is bound to %v, but that is not %s's value (%v)",
				strings.TrimSpace(c), got, strings.TrimSpace(c), want))
		}
	}
	if len(wrong) > 0 {
		t.Errorf("renditionValueColumns and renditionValues disagree about which "+
			"column carries which field:\n  %s\n\n"+
			"Position N of the column list and position N of the value slice have "+
			"to be the same field. Swapping two entries in the column list alone "+
			"is invisible to every other test in this file -- it round-trips "+
			"perfectly on a fresh database and silently reinterprets every row "+
			"already stored.", strings.Join(wrong, "\n  "))
	}
}

// assertRenditionFieldsMatch compares every field of a rendition that came out
// of the store against the one that went in, and names the ones that differ.
func assertRenditionFieldsMatch(t *testing.T, when string, want, got *Rendition) {
	t.Helper()
	wv, gv := renditionFieldValues(want), renditionFieldValues(got)
	var wrong []string
	for path, w := range wv {
		if g := gv[path]; !reflect.DeepEqual(w, g) {
			wrong = append(wrong, fmt.Sprintf("%s = %v, want %v", path, g, w))
		}
	}
	sort.Strings(wrong)
	if len(wrong) > 0 {
		t.Errorf("%s, these fields did not survive:\n  %s\n\n"+
			"Every rendition column is named once, in renditionValueColumns, and "+
			"bound once, in renditionValues. A field that comes back as the column "+
			"default was never written; a field that comes back as ANOTHER field's "+
			"value means those two are out of order between that list and that "+
			"function, or between either of them and scanRendition.",
			when, strings.Join(wrong, "\n  "))
	}
}
