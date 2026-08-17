package api

// WHAT DELETING A DESTINATION DOES ABOUT ITS YouTube liveStream: it NAMES it and
// LEAVES it.
//
// The leak is real and these tests do not pretend otherwise -- every destination
// after the first on an account has a liveStream of its own, and deleting the
// row strands that stream on the channel, unused, one per deletion, for ever.
//
// The cleanup is deliberately NOT BUILT. The argument is written out in full
// above noteOrphanedIngestStream in oauth_handlers.go and is three facts:
// polyemesis records neither the stream's id nor any provenance, so it cannot
// prove a given stream is one it created rather than one of the operator's; the
// channel's shared stream is chosen positionally and so is not identifiable
// after the fact either; and while YouTube documents that it refuses to delete a
// stream "bound to a broadcast that has still not completed", it does not
// document whether a broadcast that is LIVE RIGHT NOW is inside that condition
// -- that equation between prose and the lifeCycleStatus enum is an inference,
// and the cost of it being wrong is a show going dark.
//
// An unused stream is clutter. A wrongly deleted one is a broadcast off air.
// Every ambiguity resolves the same way.
//
// So what is asserted here is the narrow claim that IS supportable -- which
// destination is worth telling the operator about -- plus the two cases where
// saying anything at all would be dangerous, plus a guard that nothing in this
// process learns to send the delete.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The account's shared stream is NEVER nominated as removable. An operator's
// Studio-scheduled events are bound to it, so pointing anybody at it points them
// at something polyemesis did not create and must not break.
//
// MUTATION, internal/api/oauth_handlers.go, in leavesAnOrphanedIngestStream:
// drop the `s.needsOwnIngestStream(dest)` term and return true instead.
// Observed: FAIL -- "the shared-stream holder was named as removable".
func TestTheSharedIngestStreamIsNeverNamedAsSomethingToDelete(t *testing.T) {
	s, _, store := testServerWith(t, Options{})
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	first := ytDest(t, store, acct, "First show", "key-abc")

	if s.leavesAnOrphanedIngestStream(first) {
		t.Error("the shared-stream holder was named as removable; the channel's reusable " +
			"stream is what the operator's own scheduled events are bound to")
	}
}

// An upgraded install, where every YouTube destination was handed the SAME key,
// says nothing either: a stream a sibling is still publishing to is not orphaned
// by anybody's deletion.
//
// MUTATION, internal/api/oauth_handlers.go, in leavesAnOrphanedIngestStream:
// drop the keyIsSharedWithASibling check. Observed: FAIL -- a destination whose
// key another row is still using was named as removable.
func TestAKeyASiblingIsStillUsingIsNotNamedAsSomethingToDelete(t *testing.T) {
	s, _, store := testServerWith(t, Options{})
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	ytDest(t, store, acct, "First show", "key-shared")
	second := ytDest(t, store, acct, "Second show", "key-shared")

	if s.leavesAnOrphanedIngestStream(second) {
		t.Error("a destination whose key another destination is still publishing with was " +
			"named as removable; that stream is in use")
	}
}

// A destination that WAS given a stream of its own is the one case worth
// mentioning -- and the title mentioned has to be the title that was actually
// sent, or the operator is told to look in Studio for something that is not
// there.
func TestADestinationWithItsOwnStreamIsNamedAlongWithTheTitleToLookFor(t *testing.T) {
	s, _, store := testServerWith(t, Options{})
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	ytDest(t, store, acct, "First show", "key-abc")
	second := ytDest(t, store, acct, "Second show", "key-own")

	if !s.leavesAnOrphanedIngestStream(second) {
		t.Fatal("a destination with an ingest stream of its own left one behind unmentioned")
	}
	// The same function the provider titles a created stream with, so the two
	// spellings cannot drift apart.
	if got := oauth.YouTubeStreamTitle(second.Name); got != "polyemesis - Second show" {
		t.Errorf("stream title = %q, want the title createStream actually sends", got)
	}
}

// A destination with no connected account had its key typed in by hand.
// polyemesis provisioned nothing, so it has nothing to say.
func TestAManuallyKeyedDestinationLeavesNothingBehindToMention(t *testing.T) {
	s, _, store := testServerWith(t, Options{})
	d, err := store.CreateDestination(&db.Destination{
		Name: "Pasted key", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.example/live2", StreamKey: "typed-by-hand",
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	if s.leavesAnOrphanedIngestStream(d) {
		t.Error("a hand-keyed destination was said to leave a polyemesis stream behind")
	}
}

// NOTHING IN THIS PROCESS SENDS A DELETE TO liveStreams, and this is the guard
// that keeps it that way.
//
// Source-level rather than behavioural, because the hazard is a future edit and
// not a present bug: liveStreams.delete is a real documented endpoint, "just
// clean up the orphans" is an obvious thing to reach for, and the two ways it
// goes wrong -- a stream removed out from under a broadcast that is still live,
// or an operator's own stream removed because its title happened to match -- are
// both silent and both unrecoverable.
//
// Whoever settles the open questions has to delete this test to ship the
// cleanup. Having to delete a test named this is the point.
func TestNothingSendsADeleteToTheLiveStreamsEndpoint(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, "../oauth", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse internal/oauth: %v", err)
	}
	// The guard is worth nothing if the parse found nothing, which is exactly
	// what a moved package or a wrong relative path looks like.
	seen := 0
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				seen++
				var deletes, streams bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch v := n.(type) {
					case *ast.SelectorExpr:
						if v.Sel != nil && v.Sel.Name == "MethodDelete" {
							deletes = true
						}
					case *ast.Ident:
						if v.Name == "ytStreamsPath" {
							streams = true
						}
					}
					return true
				})
				if deletes && streams {
					t.Errorf("%s: %s issues an HTTP DELETE against the liveStreams endpoint. "+
						"polyemesis cannot prove a stream is one it created rather than one of "+
						"the operator's, and YouTube does not document whether a stream bound to "+
						"a broadcast that is LIVE is protected -- see noteOrphanedIngestStream "+
						"in internal/api/oauth_handlers.go", name, fn.Name.Name)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("parsed no functions out of internal/oauth, so this guard proved nothing")
	}
}
