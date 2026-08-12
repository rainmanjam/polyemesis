package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ISSUE #168, HALF TWO: THE WHOLE-PAYLOAD SHAPES, AND THE RULE THAT SAYS WHICH
// OF THEM A DERIVATION CAN REACH.
//
// #287 derived the response-header family and stopped, with a reason written
// into deferredWithReasons: "a header names its own shape at the call site; a
// whole-payload shape has no such literal, and inventing one is how #176 found
// streaming-media discharged by a proof reading an error page." That reason was
// right about `Header().Set("Vary", ...)` and WRONG about the eleven rows it
// was used to defer, because it looked for the wrong literal. A payload shape
// does not name itself in a header-setting call -- but most of the ones in this
// registry name themselves in a MEDIA TYPE, and a media type is a string
// literal in this package's source exactly as a header name is.
//
// THE MEASUREMENT. This package's non-test source contains NINE media-type
// literals at nine sites. Eight of them are a response this API sends; one is an
// Accept header on an outbound request to GitHub. Of the eight, one --
// image/jpeg, the playout poster -- corresponded to NO ROW IN THE REGISTRY. It
// is a still frame of a stream that authorizePlayout gates, served
// `public, max-age=10` with `Access-Control-Allow-Origin: *` when embedding is
// on, and the shape list had never mentioned it. That is the same class of find
// as the twelve unwritten headers, produced by the same move, and it is why
// this file exists rather than the deferral being restated.
//
// WHAT IS DERIVED HERE, AND THE TEST FOR "IS THIS A DERIVATION OR A DECORATION".
// A signature earns a place below only if BREAKING THE EMISSION MAKES A NAMED
// TEST FAIL. That is not a style rule; it is the only thing separating this
// file from the `By` string #176 deleted, which resolved beautifully and proved
// nothing. Two candidates were REJECTED by that test and are recorded rather
// than shipped:
//
//   - slog-output. Its emission is `s.log.Log/Info/Warn/Error/Debug(...)` at 81
//     sites in 18 files. A scan for it can only ever return "yes", and no edit
//     short of deleting every logging call in the package moves it. A check
//     whose answer is fixed is not a join. Left hand-written, with that count as
//     its reason.
//   - the five out-of-package rows. Their bytes are written in internal/hooks,
//     internal/alerts, internal/supervisor and cmd/polyemesis (twice).
//     Deriving them means an AST walk over another package's source, which is
//     the mechanism #245 DELETED when it removed the symbol index that resolved
//     `By` strings. Re-adding it one abstraction up to raise a count here is
//     precisely the trade that round rejected.
//
// So the residual is no longer "eleven rows somebody wrote". It is a RULE, and
// assertEveryShapeRowIsAccountedFor below makes the rule checkable: every row in
// shapeRegistry() is either derived from a header name, derived from a media
// type or a call signature here, emitted by a package that is not this one, or
// emitted here with no anchor narrow enough to mutate. A row that is none of
// those four fails, so the next hand-written shape cannot be added silently.

// sortedMapKeys is the deterministic iteration order every failure below is
// reported in. The package already has a sortedKeys, and it is
// map[string]string only; these maps carry shapeRows and site slices.
func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mediaTypeLiteral is one media type this package's source spells out, with
// every site that spells it. Sites are the evidence, as in the header
// derivation: a failure names lines, not just a string.
type mediaTypeLiteral struct {
	Value string
	Sites []string
}

// mediaTypeLiteralForm is what counts as a media type for the census.
//
// Deliberately narrow -- a registered top-level type, then a subtype in the
// character set RFC 6838 allows -- because the census is TOTAL over string
// literals and a loose pattern would drag half the package in. Parameters are
// stripped before matching, so "application/json; charset=utf-8" is censused as
// application/json and the two spellings in writeJSON and writeNoSuchEndpoint
// do not become two shapes.
var mediaTypeLiteralForm = regexp.MustCompile(
	`^(application|audio|font|image|message|model|multipart|text|video)/[a-z0-9][a-z0-9!#$&^_.+-]*$`)

// derivedMediaTypeLiterals is the population, and it is a census over EVERY
// string literal in this package's non-test source rather than over the
// arguments of a particular call.
//
// That width is the whole point, and it is the second draft of a narrower idea.
// The first version read the value at `Header().Set("Content-Type", <lit>)`
// sites, mirroring the header scan -- and it was blind to four of this
// package's eight emitted media types, because serveFileDownload takes the
// content type as a PARAMETER: the stem download's application/octet-stream,
// the clip's video/mp2t and the recording's video/x-matroska are literals at a
// CALL to a helper, not at a Set. A scan keyed to one syntactic form is blind
// to a spelling, which is the same lesson the header scan learned when it
// missed the five security headers written through an `h := w.Header()` alias.
// A census over literals cannot be defeated by a helper, an alias, or a
// constant, because the bytes have to be written down somewhere to be sent.
//
// WHAT IT DELIBERATELY DOES NOT CLAIM:
//
//   - IMPORT PATHS ARE SKIPPED, and this is not a detail. A Go import path is
//     syntactically indistinguishable from a media type: `image/jpeg`,
//     `image/png`, `text/template` and `text/scanner` all match the pattern
//     above exactly. Without the ImportSpec skip the census would report
//     whichever of those the package imports as an emitted shape, and the
//     failure would send a reader looking for a response that does not exist.
//   - a media type COMPOSED at runtime is invisible. `mime.TypeByExtension`,
//     a fmt.Sprintf, a value read from settings -- none of them put the string
//     in the source. There are none in this package today; if one appears, this
//     census silently narrows, and that is stated here rather than hidden
//     because "derived, therefore total" is the belief this ledger exists to
//     disbelieve.
//   - a literal is not proof the bytes are SENT. Its presence is why a shape
//     row must exist; whether the emitted bytes really are that shape is the
//     inspector's job, and the two are kept apart on purpose. #176's failure
//     was an inspector reading an error page, not a population that was too
//     wide.
func derivedMediaTypeLiterals(t *testing.T) []mediaTypeLiteral {
	t.Helper()

	sites := map[string][]string{}
	for _, file := range parsePackageSource(t) {
		ast.Inspect(file.f, func(n ast.Node) bool {
			// See above: an import path IS a media type, syntactically.
			if _, ok := n.(*ast.ImportSpec); ok {
				return false
			}
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			raw, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			value := strings.ToLower(strings.TrimSpace(raw))
			if i := strings.IndexByte(value, ';'); i >= 0 {
				value = strings.TrimSpace(value[:i])
			}
			if !mediaTypeLiteralForm.MatchString(value) {
				return true
			}
			sites[value] = append(sites[value], file.fset.Position(lit.Pos()).String())
			return true
		})
	}

	out := make([]mediaTypeLiteral, 0, len(sites))
	for value, where := range sites {
		sort.Strings(where)
		out = append(out, mediaTypeLiteral{Value: value, Sites: where})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

// websocketPackage is the import that makes a file capable of emitting the
// websocket-frame shape.
const websocketPackage = "github.com/gorilla/websocket"

// derivedWebsocketSites is the second signature, and it is here because
// websocket-frame is the one payload shape in this registry whose emission
// names itself in a CALL rather than in a media type: a frame carries no
// Content-Type, and the shape's identity is "the bytes went out over an
// upgraded connection".
//
// Keyed on the IMPORT PATH rather than the package name, so aliasing the import
// does not hide the emission. Both halves of the mechanism are collected --
// the upgrade that turns a request into a connection and the write that puts a
// frame on it -- because either one disappearing is the shape disappearing.
func derivedWebsocketSites(t *testing.T) []string {
	t.Helper()

	var sites []string
	for _, file := range parsePackageSource(t) {
		imported := false
		for _, spec := range file.f.Imports {
			if path, err := strconv.Unquote(spec.Path.Value); err == nil &&
				path == websocketPackage {
				imported = true
			}
		}
		if !imported {
			continue
		}
		ast.Inspect(file.f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Upgrade", "WriteMessage", "WriteJSON", "NextWriter",
				"WritePreparedMessage":
				sites = append(sites,
					file.fset.Position(call.Pos()).String()+": ."+sel.Sel.Name)
			}
			return true
		})
	}
	sort.Strings(sites)
	return sites
}

// parsedFile is one non-test .go file of this package with the fileset that
// positions it.
type parsedFile struct {
	name string
	f    *ast.File
	fset *token.FileSet
}

// parsePackageSource is the shared read, and it carries the positive control
// the header derivation carries for the same reason: a scan that parsed nothing
// derives the empty set, and the empty set satisfies every "each derived X has
// a row" assertion perfectly.
func parsePackageSource(t *testing.T) []parsedFile {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var out []parsedFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		out = append(out, parsedFile{name: name, f: f, fset: fset})
	}
	if len(out) == 0 {
		t.Fatal("the payload-shape derivation parsed no production file. It reads the " +
			"package directory at test time; with nothing parsed every census below is " +
			"empty, and an empty census agrees with any registry.")
	}
	return out
}

// mediaTypeClaim is what a media type literal MEANS. Shape names the registry
// row it belongs to; an empty Shape is the explicit "this is not a response
// this API emits", which needs a reason precisely because it is the escape
// hatch.
type mediaTypeClaim struct {
	Shape string
	Why   string
}

// mediaTypeShapeClaims is the join table, and its TOTALITY is what makes the
// census a population check rather than a lookup: a media type in this
// package's source with no entry here FAILS. That is the direction #168 is
// about -- "nothing fails when a new response shape appears" -- and it does not
// depend on anybody having anticipated the new shape's name.
//
// The empty-Shape escape hatch is the part to be suspicious of, so it is used
// once, for a string that is not a response at all, and the reason says which
// direction the bytes travel.
func mediaTypeShapeClaims() map[string]mediaTypeClaim {
	return map[string]mediaTypeClaim{
		"application/json": {Shape: "json-body",
			Why: "writeJSON and writeNoSuchEndpoint, the two sites that label this API's " +
				"ordinary response. Both spell it `; charset=utf-8`; the census strips " +
				"parameters so the two spellings are one shape."},
		"application/vnd.apple.mpegurl": {Shape: "streaming-media",
			Why: "the HLS manifest's type, at the one site in handlers.go that answers a " +
				"path ending .m3u8."},
		"application/x-mpegurl": {Shape: "streaming-media",
			Why: "the other registered spelling of the same manifest type. No site writes " +
				"it today; it is claimed so that switching spellings is not a new shape."},
		"text/event-stream": {Shape: "sse",
			Why: "THE ROW THIS ENTRY EXISTS FOR. `sse` is the only Emitted:false row in the " +
				"registry, and until this census nothing checked that claim: adding an " +
				"event-stream handler would have left every test in the repository green. " +
				"No site writes it, which is what the row says."},
		"video/x-matroska": {Shape: "file-download",
			Why: "the recording download's type, set beside a Content-Disposition and " +
				"served by http.ServeContent."},
		"video/mp2t": {Shape: "file-download",
			Why: "the clip download, through serveFileDownload -- an attachment, not a " +
				"segment of the live stream, which is why this is not streaming-media."},
		"application/octet-stream": {Shape: "file-download",
			Why: "the stem download's fallback type, also through serveFileDownload."},
		"application/x-pem-file": {Shape: "file-download",
			Why: "the CA certificate download; the fourth of the four Content-Disposition " +
				"sites the header family already names."},
		"image/jpeg": {Shape: "playout-poster",
			Why: "THE SHAPE THIS CENSUS FOUND. A still frame rendered off the live stream " +
				"by FFmpeg and served from the poster cache. It is not json-body, not a " +
				"download (no Content-Disposition, and it is served inline to a player), " +
				"and not the manifest-and-segments shape streaming-media names -- so the " +
				"honest classification was a row that did not exist."},
		"application/vnd.github+json": {Shape: "",
			Why: "NOT AN EMISSION. This is an Accept header on an OUTBOUND request from " +
				"this process to the GitHub release API in handlers.go, so the bytes " +
				"travel the other way and no shape row could describe them. The census " +
				"reads literals rather than call positions and therefore cannot tell " +
				"direction; this entry is where a human says which way it goes."},
	}
}

// payloadShapeSignatures is the set of registry rows this file derives, mapped
// to the thing it looks for. Every one of them was mutation-tested; the
// mutations and what they printed are on TestEveryEmittedPayloadShapeHasAShapeRow.
func payloadShapeSignatures() map[string]string {
	return map[string]string{
		"json-body":       "a media-type literal claimed for it (application/json)",
		"streaming-media": "a media-type literal claimed for it (application/vnd.apple.mpegurl)",
		"file-download":   "a media-type literal claimed for it (four of them)",
		"playout-poster":  "a media-type literal claimed for it (image/jpeg)",
		"sse":             "a media-type literal claimed for it (text/event-stream); ABSENT is the claim",
		"websocket-frame": "an upgrade or a frame write in a file importing " + websocketPackage,
	}
}

// shapesEmittedOutsideThisPackage is the first residual bucket, and it is a
// FACT ABOUT WHERE CODE LIVES rather than an excuse. Each value is the package
// directory whose source writes those bytes, relative to the repository root,
// and the guard checks the directory exists -- so a package that moves or is
// renamed breaks this claim instead of aging into fiction.
//
// These are the rows a derivation rooted at this package's directory cannot
// reach, and the honest options for them are (a) leave them to the Jurisdiction
// and Inspector machinery that already covers them, or (b) walk another
// package's AST from a test in this one. (b) is what #245 deleted. This bucket
// is the written-down form of choosing (a).
func shapesEmittedOutsideThisPackage() map[string]string {
	return map[string]string{
		"outbound-hook-body":  "internal/hooks",
		"outbound-alert-body": "internal/alerts",
		"on-disk-process-log": "internal/supervisor",
		"mqtt-retained-topic": "cmd/polyemesis",
		"plain-http-listener": "cmd/polyemesis",
	}
}

// shapesWithNoSyntacticAnchor is the second residual bucket: emitted by THIS
// package, and still hand-written, because the emission has no site set small
// enough for breaking it to break a test.
//
// One row, and the reason is a number rather than a judgement.
func shapesWithNoSyntacticAnchor() map[string]string {
	return map[string]string{
		"slog-output": "the emission is a call on the Server's *slog.Logger field -- " +
			"s.log.Log/Info/Warn/Error/Debug -- at 81 sites in 18 non-test files. A scan " +
			"for that returns \"emitted\" for every build this package will ever have, so " +
			"the join could not fail and the reverse direction could not be mutated. " +
			"Inspected by inspectSlogOutput, which reads the real bytes; what is missing " +
			"is a derivation of the POPULATION, and a check that cannot fail is not one.",
	}
}

// TestEveryEmittedPayloadShapeHasAShapeRow is the join, in the same three
// directions the header derivation is joined in, plus the fourth this file adds.
//
// Called from TestLedgerPreflight as well as declared here, for the two reasons
// every guard in this ledger is: `rm` on a file nothing references leaves the
// suite green, and the TestMain preflight forces only ^TestLedgerPreflight$, so
// a guard outside it does not run in the filtered invocation the preflight
// exists to survive. The call IS the compile-time reference. It parses this
// package's source and drives nothing; measured at 0.03s.
//
// MUTATION TESTED, SIX WAYS. Each mutation was applied, this test was run with
// `-run TestEveryEmittedPayloadShapeHasAShapeRow`, the failure was observed to
// name THIS test rather than the preflight around it, and the file was then
// restored from a copy taken before the edit:
//
//   - A NEW SHAPE APPEARS. `w.Header().Set("Content-Type", "text/event-stream")`
//     added to writeJSON, in front of the Set that overwrites it -- deliberately
//     inert on the wire, so the only thing that moves is the census, and the
//     failure can be read without forty JSON assertions changing at the same
//     time. That inertness is also the census's documented limit: it reads
//     literals, not sends. Observed FAIL: `the shape row "sse" records this API
//     as NOT emitting that shape and this package emits it at 1 site(s):
//     api.go:1321:33`. This is #168's sentence aimed at the one row in the
//     registry that makes a NEGATIVE claim, and before this census that edit was
//     invisible to every test in the repository.
//   - A MEDIA TYPE NOBODY CLASSIFIED. playout.go's "image/jpeg" changed to
//     "image/webp". Observed FAIL, TWICE and in both directions at once: `this
//     package spells the media type "image/webp" at 1 site(s) and
//     mediaTypeShapeClaims accounts for no such type`, and `the shape registry
//     records "playout-poster" as emitted and no site in this package matches
//     its signature`. This is the general form of the first mutation -- it fires
//     for a media type nobody anticipated -- and it is what makes the census a
//     population rather than a lookup.
//   - A ROW GOES STALE. handlers.go's "application/vnd.apple.mpegurl" replaced
//     by a call returning strings.Join([]string{"application",
//     "vnd.apple.mpegurl"}, "/"). Observed FAIL: `the shape registry records
//     "streaming-media" as emitted and no site in this package matches its
//     signature`. A first attempt used `"application/vnd.apple." + "mpegurl"`
//     and was RE-ROLLED: the census flagged the fragment "application/vnd.apple."
//     as an unclassified media type, so it exercised the forward direction as
//     well and did not isolate the reverse one. The mutation that stands leaves
//     no media-type-shaped literal behind, which is also the honest
//     demonstration of the limit stated on derivedMediaTypeLiterals: a type this
//     package composes rather than spells is a type this census cannot see.
//   - THE WEBSOCKET SIGNATURE. ws.go reduced to a handleWS answering 501, with
//     the gorilla/websocket import, the Upgrader and writeEvent deleted.
//     Observed FAIL: `the shape registry records "websocket-frame" as emitted
//     and no site in this package matches its signature (an upgrade or a frame
//     write in a file importing github.com/gorilla/websocket)`.
//   - THE TOTALITY GUARD. A `{Shape: "ledger-mutation", Emitted: true}` row
//     added to shapeRegistry(). Observed FAIL: `the shape row "ledger-mutation"
//     is in none of the four buckets`. That is what stops the next hand-written
//     row being added in silence, which is what the eleven-name list this
//     replaces could not do.
//   - AN OUT-OF-PACKAGE CLAIM AGES INTO FICTION. outbound-hook-body's bucket
//     entry changed from "internal/hooks" to "internal/hooks-moved-away".
//     Observed FAIL: `... and that directory does not exist from here (stat
//     ../../internal/hooks-moved-away: no such file or directory)`. Worth
//     spending a mutation on because that branch is the whole difference between
//     a checked claim and the `By` strings #178 measured, three of seven of
//     which named something that could not run.
func TestEveryEmittedPayloadShapeHasAShapeRow(t *testing.T) {
	assertDerivedPayloadShapesAreRegistered(t)
}

func assertDerivedPayloadShapesAreRegistered(t *testing.T) {
	t.Helper()

	rows := map[string]shapeRow{}
	for _, r := range shapeRegistry() {
		rows[r.Shape] = r
	}

	literals := derivedMediaTypeLiterals(t)
	claims := mediaTypeShapeClaims()

	// derivedSites is what the census and the signatures together say this
	// package emits, per shape.
	derivedSites := map[string][]string{}

	for _, lit := range literals {
		claim, ok := claims[lit.Value]
		if !ok {
			t.Errorf("this package spells the media type %q at %d site(s) and "+
				"mediaTypeShapeClaims accounts for no such type:\n  %s\n"+
				"THIS IS THE FAILURE #168 IS ABOUT, in the half that is not headers. A "+
				"media type written into this package's source is a shape this API sends "+
				"(or, once, an Accept header on a request it makes) and the shape list was "+
				"joined to nothing that emits. Say which row it belongs to -- adding one if "+
				"none fits, which is how playout-poster got here -- or claim it with an "+
				"empty Shape and a reason saying which way the bytes travel.",
				lit.Value, len(lit.Sites), strings.Join(lit.Sites, "\n  "))
			continue
		}
		if strings.TrimSpace(claim.Why) == "" {
			t.Errorf("the media type %q is claimed with no reason. The empty reason is how "+
				"the empty-Shape escape hatch stops being an argument and becomes a "+
				"habit.", lit.Value)
		}
		if claim.Shape == "" {
			continue
		}
		derivedSites[claim.Shape] = append(derivedSites[claim.Shape], lit.Sites...)
	}

	if ws := derivedWebsocketSites(t); len(ws) > 0 {
		derivedSites["websocket-frame"] = append(derivedSites["websocket-frame"], ws...)
	}

	// FORWARD: something this package emits with no row, or with a row that
	// denies emitting it.
	for _, shape := range sortedMapKeys(derivedSites) {
		where := derivedSites[shape]
		sort.Strings(where)
		row, ok := rows[shape]
		if !ok {
			t.Errorf("this package emits the shape %q at %d site(s) and the shape registry "+
				"has no row for it:\n  %s\n"+
				"Add one with an Inspector -- a func this preflight CALLS, which is the "+
				"strong discharge -- or a Jurisdiction naming the package and test that "+
				"assert it.", shape, len(where), strings.Join(where, "\n  "))
			continue
		}
		if !row.Emitted {
			t.Errorf("the shape row %q records this API as NOT emitting that shape and this "+
				"package emits it at %d site(s):\n  %s\n"+
				"`Emitted: false` gives step 7 the verdict \"absent\", so the row needs "+
				"neither an inspector nor a jurisdiction record -- a blind spot with its "+
				"discharge already stamped on. `sse` is the only row in this registry that "+
				"makes this claim, and this is the check that makes it a claim rather than "+
				"a sentence.", shape, len(where), strings.Join(where, "\n  "))
		}
	}

	// REVERSE: a row this file claims to derive, marked emitted, that no site
	// produces. The staleness the shapeFloor ratchet cannot see, because the
	// count does not move when a row stops describing this build.
	signatures := payloadShapeSignatures()
	for _, shape := range sortedMapKeys(signatures) {
		row, ok := rows[shape]
		if !ok {
			t.Errorf("payloadShapeSignatures derives the shape %q and shapeRegistry has no "+
				"such row. The signature table and the registry have drifted apart, which "+
				"means this file is deriving something nothing records.", shape)
			continue
		}
		if !row.Emitted || len(derivedSites[shape]) > 0 {
			continue
		}
		t.Errorf("the shape registry records %q as emitted and no site in this package "+
			"matches its signature (%s).\n"+
			"Either the emission moved to another package -- in which case the row wants a "+
			"Jurisdiction and an entry in shapesEmittedOutsideThisPackage rather than a "+
			"signature -- or it is gone and so is the row.", shape, signatures[shape])
	}

	assertEveryShapeRowIsAccountedFor(t, rows)

	// THE POSITIVE CONTROLS, and there are two because the census makes one
	// NEGATIVE claim and a negative claim from a broken scanner is free.
	//
	// `sse` passes by finding no text/event-stream literal. That is worth
	// something only if the same scanner, on the same files, in the same run,
	// demonstrably finds media types that ARE there -- so the census must
	// return a non-empty set, and at least one signature-table shape must have
	// been derived from it. Without this, deleting the census body and
	// returning nil would leave the sse row discharged by a scan that looked at
	// nothing, which is the exact shape of every defect this ledger has caught.
	if len(literals) == 0 {
		t.Fatal("the media-type census found no literal at all. `sse` is discharged by this " +
			"census finding no text/event-stream, and a census that finds nothing " +
			"discharges it having examined nothing.")
	}
	if len(derivedSites) == 0 {
		t.Fatal("no payload shape was derived from the census. Every forward check above " +
			"iterates that set, so all of them passed having examined nothing.")
	}
}

// assertEveryShapeRowIsAccountedFor is the fourth direction, and the one that
// replaces the old deferral's list of eleven names with a rule.
//
// The old row in deferredWithReasons named the rows that were hand-written. A
// list of names in a deferral goes stale exactly as a list of shapes does, and
// nothing joined it to the registry either: adding a twelfth hand-written row
// would not have changed a character of it. This makes the same statement
// checkable -- every row is derived from a header, derived here, emitted
// elsewhere, or anchorless-and-measured -- so the residual cannot grow in
// silence.
func assertEveryShapeRowIsAccountedFor(t *testing.T, rows map[string]shapeRow) {
	t.Helper()

	elsewhere := shapesEmittedOutsideThisPackage()
	anchorless := shapesWithNoSyntacticAnchor()
	signatures := payloadShapeSignatures()

	for _, shape := range sortedMapKeys(rows) {
		switch {
		case strings.HasPrefix(shape, responseHeaderShapePrefix):
		case signatures[shape] != "":
		case elsewhere[shape] != "":
		case anchorless[shape] != "":
		default:
			t.Errorf("the shape row %q is in none of the four buckets: it is not a "+
				"response header (derived by assertDerivedHeaderShapesAreRegistered), it "+
				"is not in payloadShapeSignatures (derived here), it is not in "+
				"shapesEmittedOutsideThisPackage, and it is not in "+
				"shapesWithNoSyntacticAnchor.\n"+
				"#168 is \"the shape list is maintained by hand and joined to nothing that "+
				"emits\". A row that is in no bucket is a row back in that state. Give it a "+
				"signature if this package's source names it, or say where it is emitted "+
				"and why no scan here can see it.", shape)
		}
	}

	// The out-of-package claims are checked rather than believed. A directory
	// that does not exist is a jurisdiction record that has aged into fiction,
	// which is the failure mode #178 measured on the `By` strings: three of
	// seven named something that could not run.
	for _, shape := range sortedMapKeys(elsewhere) {
		dir := elsewhere[shape]
		if dir == "internal/api" {
			t.Errorf("the shape %q is recorded as emitted outside this package and names "+
				"internal/api. That bucket is for bytes this package's source does not "+
				"write; a row pointing back here belongs in payloadShapeSignatures or in "+
				"shapesWithNoSyntacticAnchor with a measured reason.", shape)
			continue
		}
		if _, err := os.Stat(filepath.Join("..", "..", dir)); err != nil {
			t.Errorf("the shape %q is recorded as emitted by the package %q and that "+
				"directory does not exist from here (%v). The bucket is a claim about "+
				"where code lives, and an unchecked claim about another package is what "+
				"the deleted symbol index was built to catch.", shape, dir, err)
		}
		if _, ok := rows[shape]; !ok {
			t.Errorf("shapesEmittedOutsideThisPackage names %q and shapeRegistry has no "+
				"such row.", shape)
		}
	}
	for _, shape := range sortedMapKeys(anchorless) {
		if _, ok := rows[shape]; !ok {
			t.Errorf("shapesWithNoSyntacticAnchor names %q and shapeRegistry has no such "+
				"row.", shape)
		}
	}

	// The positive control for the bucket rule: if every bucket emptied, the
	// loop above would pass having classified nothing.
	if len(elsewhere)+len(anchorless)+len(signatures) == 0 {
		t.Fatal("every bucket is empty, so the accounting rule classified nothing and " +
			"passed.")
	}
}

// inspectPlayoutPoster is the inspector for the row the census produced.
//
// WHAT IT WITNESSES AND WHAT IT CANNOT, said first because this is the one
// inspector in the registry whose source bytes are planted, and #176 is the
// case of a proof that read the wrong bytes and nobody noticed.
//
// The RENDER is FFmpeg's: posterJPEG decodes a frame out of a .ts segment on
// disk, which no fixture in this package has, which is why the poster route
// 404s for every test here and why response-header/Content-Length had never
// been observed until the header derivation found it. Priming the cache does
// not make FFmpeg run and this inspector does not claim it does.
//
// What it does witness is the SERVING, which is the whole of what leaves this
// API, and it is asserted on three properties the planting cannot fake:
//
//   - the response is the 200 branch, not the 404. That is the distinction
//     #176 turned on: a green proof reading fifty bytes of {"error":...} while
//     a row called the media shape covered.
//   - the bytes on the wire are byte-identical to the cache entry. A handler
//     that re-encoded, truncated, or wrapped the image would fail here, and
//     that is a property of the handler regardless of which bytes went in.
//   - the label matches the body: Content-Type is image/jpeg and the body does
//     not begin with a JSON delimiter. A poster route that had fallen back to
//     writeJSON would still be 200 and still be non-empty.
//
// The JPEG SOI marker in plantedPosterBytes is deliberately NOT asserted. It is
// this test's own byte, and asserting it would be the inspector reading its own
// fixture back -- the tautology TestBlankingEveryShapeNoteChangesNoVerdict was
// caught being.
func inspectPlayoutPoster(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	w := rigPosterRecorder(t, rig)
	body := w.Body.String()

	if got := w.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("the playout-poster inspector read Content-Type %q from the poster "+
			"route's 200 branch, want image/jpeg. A media shape whose label is not its "+
			"media type is the label being wrong or the branch being another handler's.",
			got)
	}
	if body != plantedPosterBytes {
		t.Errorf("the poster route served %d bytes and the cache it served them from "+
			"holds %d. The poster is written to the wire verbatim; a difference is the "+
			"handler transforming an image it is supposed to pass through.",
			len(body), len(plantedPosterBytes))
	}
	if strings.HasPrefix(strings.TrimSpace(body), "{") ||
		strings.HasPrefix(strings.TrimSpace(body), "[") {
		t.Errorf("the playout-poster inspector read a body that starts as JSON: %s\n"+
			"That is the substitution this registry exists to catch -- an error object "+
			"standing in for the media shape, with a 200 and a length in front of it.",
			truncateForFailure(body))
	}
	return shapeObservation{Shape: "playout-poster", Sample: body}
}
