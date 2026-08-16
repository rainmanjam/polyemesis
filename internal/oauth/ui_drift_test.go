package oauth

import (
	"regexp"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// The same guard internal/db keeps over Rendition, one layer over.
//
// db's version catches a stored field the UI cannot name, and it has caught
// five unreachable settings in this repository. MetadataField has the identical
// shape -- a Go enumeration mirrored by a hand-written TypeScript union -- and
// had no guard at all. This exists because I widened that union by hand while
// adding three fields, noticed nothing would have caught me forgetting, and the
// evidence says somebody eventually does forget.
//
// It matters for a specific reason rather than for tidiness. A push RESULT names
// these fields in `applied` and `skipped`, and the composer renders them
// straight from the response. A field the UI cannot name is not a compile error
// or a blank -- it is a row that reports nothing, on the screen an operator is
// looking at seconds before going live.
func TestUITypesCanNameEveryMetadataField(t *testing.T) {
	union, ok := tsUnion(testenv.StripJSComments(testenv.ReadUI(t, "lib", "types.ts")), "MetaField")
	if !ok {
		t.Fatal("no `export type MetaField = ...` in ui/src/lib/types.ts. It was moved " +
			"there from Dashboard.tsx precisely so this guard has one canonical place to read")
	}

	for _, f := range AllMetadataFields {
		// Quoted, so a field named "tag" cannot be satisfied by "tags".
		if !strings.Contains(union, `"`+string(f)+`"`) {
			t.Errorf("MetadataField %q is absent from the UI's MetaField union in types.ts. "+
				"A push result can name this field, and a field the UI cannot name renders as "+
				"nothing at all -- add it to the union, or remove the constant if nothing "+
				"reports it", f)
		}
	}

	// And the other direction, which the db guard does not do and should:
	// a union member with no Go constant is dead code the compiler cannot see,
	// and it misleads the next person into thinking the backend sends it.
	for _, m := range regexp.MustCompile(`"([a-zA-Z]+)"`).FindAllStringSubmatch(union, -1) {
		var found bool
		for _, f := range AllMetadataFields {
			if string(f) == m[1] {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the UI's MetaField union has %q, which no MetadataField constant "+
				"produces. Nothing will ever send it", m[1])
		}
	}
}

// Every field must be reachable through some platform's caps, or it is a field
// nothing can ever apply.
//
// Not every field belongs to every platform -- that is the whole point of
// MetadataCaps -- but a field NO platform advertises is either unimplemented or
// abandoned, and both are worth failing over rather than leaving to be
// discovered by an operator whose push reported nothing.
func TestEveryMetadataFieldIsAdvertisedBySomePlatform(t *testing.T) {
	advertised := map[MetadataField]bool{}
	for _, p := range Providers() {
		mp, ok := p.(MetadataPusher)
		if !ok {
			continue
		}
		for _, f := range mp.MetadataCaps().Fields {
			advertised[f] = true
		}
	}
	// The compliance fields are pushed through PushCompliance rather than
	// through MetadataCaps, so they are legitimately absent from every caps
	// list while still being real. PushCompliance is called from
	// internal/api/metadata.go's pushOne, alongside the ordinary metadata
	// push, for every account whose resolved destination has compliance
	// stored -- not from anywhere in this package. Named individually rather
	// than skipped as a group, so adding a fourth compliance field has to be
	// a decision.
	viaCompliance := map[MetadataField]bool{
		FieldPrivacy: true, FieldMadeForKids: true, FieldLabels: true,
	}
	for _, f := range AllMetadataFields {
		if advertised[f] || viaCompliance[f] {
			continue
		}
		t.Errorf("no platform advertises %q in its MetadataCaps and it is not a compliance "+
			"field, so nothing can ever apply it. Either wire it to a platform or delete it", f)
	}
}

// tsUnion returns the body of `export type <name> = ...;`.
//
// IT USED TO SAY comments were kept deliberately, on the grounds that the union
// carries a note per group about which push path produces those fields and that
// stripping them "would make the file harder to read in exchange for nothing".
// That reasoning was wrong twice over and #379 is the correction.
//
// It was not in exchange for nothing. The forward check below is
// `strings.Contains(union, "\"tags\"")`, so deleting a member and leaving
// `// "tags" -- removed, see ...` behind kept this guard green over a union that
// could no longer name the field. That is the whole failure mode this family of
// guards exists to catch, and this one was open to it.
//
// It also was not free in the other direction: the body is bounded by the first
// `;` after the type name, and a semicolon inside one of those explanatory
// comments truncates the union early, hiding every member after it from a check
// that would then fail while naming the wrong cause.
//
// Callers pass source that has already been through testenv.StripJSComments, so
// what is returned is the union as the compiler sees it. Nobody's reading
// experience changes -- the notes are still in types.ts, this just stops them
// counting as declarations.
func tsUnion(src, name string) (string, bool) {
	start := strings.Index(src, "export type "+name+" =")
	if start < 0 {
		return "", false
	}
	end := strings.Index(src[start:], ";")
	if end < 0 {
		return "", false
	}
	return src[start : start+end], true
}
