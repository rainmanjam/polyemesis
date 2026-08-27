package oauth

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

/* THE MATRIX SAYS WHAT POLYEMESIS CAN DO; THIS ASKS THE CODE WHETHER IT AGREES.
 *
 * capabilities.go states the house rule -- "a capability nothing implements is
 * not a capability" -- and until now exactly ONE of the eight columns was
 * actually joined to the implementation. stats_capability_drift_test.go names
 * the blind spot in its own header: four surfaces describe what a platform can
 * do, "every one of them compares a DOCUMENT to another DOCUMENT. None compares
 * any of them to the provider code." That file closed it for viewerStats. This
 * one closes it for the columns whose implementation lives in this package.
 *
 * THE DIRECTION THAT MATTERS MOST IS claimed-but-not-implemented. The matrix is
 * rendered in the UI as a table an operator picks a platform from. A cell
 * reading "Works" for something no code does is a promise made at the moment
 * somebody is choosing what to rely on, and they find out it was false while
 * live. The reverse -- implemented but not claimed -- is a smaller failure: a
 * feature nobody is told about, wasted rather than harmful.
 *
 * WHY THIS IS A TABLE OF RESOLVERS AND NOT ONE BICONDITIONAL. Writing this
 * revealed that the obvious resolver is wrong twice, and both corrections are
 * the interesting part:
 *
 *   STREAM KEY does not resolve through TargetsFor. Ingest is on the BASE
 *   Provider interface (oauth.go:105), so every registered provider fetches a
 *   key; TargetedProvider is about which PAGE or channel to publish to, not
 *   whether a key can be had at all. A first draft of this test joined
 *   streamKey to TargetsFor and reported Twitch as a violation. Twitch is fine;
 *   the test was wrong.
 *
 *   BROADCAST LIFECYCLE has two mechanisms and only one resolver. See the
 *   Facebook exception below.
 *
 * So each column names how the code actually provides it, and a disagreement
 * that is genuine gets a written exception rather than a widened rule.
 */

// capabilityJoin ties one column to the code that implements it.
type capabilityJoin struct {
	cap Capability
	// what the column is called where an operator reads it, for messages.
	label string
	// implemented answers "does this package provide this for this platform".
	implemented func(db.Platform) bool
	// exceptions record platforms where the cell legitimately disagrees with
	// implemented, and WHY. An exception is a claim about a second mechanism,
	// so it is checked in both directions: an exception that has stopped being
	// necessary fails just as loudly as a missing one, because a stale excuse is
	// how a real violation eventually hides behind a sentence nobody re-read.
	exceptions map[db.Platform]string
}

func capabilityJoins() []capabilityJoin {
	registered := func(p db.Platform) bool {
		_, ok := Providers()[p]
		return ok
	}
	return []capabilityJoin{
		{
			cap: CapSSO, label: "Sign in",
			implemented: registered,
		},
		{
			cap: CapStreamKey, label: "Stream key",
			// Registration, not TargetsFor: Ingest is on the base Provider
			// interface, so signing in is exactly what makes a key fetchable.
			implemented: registered,
			exceptions: map[db.Platform]string{
				// SIGNING IN IS NOT WHAT MAKES A KEY FETCHABLE HERE, which is
				// the assumption the resolver above encodes and the first
				// platform to break it.
				//
				// Vimeo has no permanent stream key at all: the ingest URL and
				// key belong to a live event, so obtaining one means reading or
				// creating an event -- and every live method is Enterprise-only
				// ("our live API is available only to Vimeo Enterprise
				// customers"). Vimeo.Ingest returns ErrNoStreamKeyAPI on every
				// path, saying WHICH of the two reasons applies to the account
				// in front of it; the cell is By hand because the operator
				// pastes the pair from the event's setup panel.
				//
				// THE RESOLVER WAS NOT WIDENED, and the obvious widening is
				// wrong: `registered && !ManualKeyFor(p)` looks like the right
				// discriminator and would fail Kick, which implements ManualKey
				// for the legacy-token case and fetches its key perfectly well.
				// The test's own instruction is to check the mechanism before
				// touching the resolver -- checked, and it is an exception.
				//
				// DELETE THIS the day polyemesis creates Vimeo events and the
				// cell becomes SupportYes. The test will tell you to, because a
				// stale exception fails as loudly as a missing one.
				db.PlatformVimeo: "no permanent key exists; the ingest belongs to a live " +
					"event and every live method is behind Vimeo's Enterprise gate",
			},
		},
		{
			cap: CapMetadata, label: "Title / category",
			implemented: func(p db.Platform) bool { _, ok := MetadataFor(p); return ok },
		},
		{
			cap: CapBroadcastLifecycle, label: "Start / end",
			implemented: func(p db.Platform) bool { _, ok := LifecycleFor(p); return ok },
			exceptions: map[db.Platform]string{
				// COMMANDED BY HAND RATHER THAN DRIVEN, and both are real, which
				// is why the cell is Yes and LifecycleFor is false.
				//
				// BroadcastLifecycler is the interface the lifecycle COORDINATOR
				// drives (internal/api/lifecycle.go: "Only platforms that implement
				// oauth.BroadcastLifecycler are driven, which today means
				// YouTube"). Facebook reaches the same two outcomes by other
				// routes: connecting the account CREATES the live video through
				// IngestFor, and ending it is POST
				// /destinations/{id}/facebook/end-broadcast, which the destination
				// card offers as a menu item.
				//
				// So an operator on Facebook genuinely can start and end a
				// broadcast from polyemesis. What they cannot do is have it happen
				// for them, and capabilities.go's Reasons entry for this cell says
				// exactly that in the UI. If Facebook ever implements
				// BroadcastLifecycler, DELETE THIS ENTRY -- the test will tell you
				// to, because a stale exception fails.
				db.PlatformFacebook: "created by IngestFor and ended by the " +
					"/facebook/end-broadcast route; not driven by the coordinator",
			},
		},
	}
}

func TestEveryCapabilityCellAgreesWithTheCodeThatImplementsIt(t *testing.T) {
	for _, join := range capabilityJoins() {
		join := join
		t.Run(string(join.cap), func(t *testing.T) {
			for _, row := range PlatformCapabilities() {
				row := row
				t.Run(row.PresetID, func(t *testing.T) {
					claimed := row.Caps[join.cap] == SupportYes
					impl := join.implemented(row.Platform)
					why, excused := join.exceptions[row.Platform]

					// A stale excuse is checked first, because once the code and
					// the cell agree the sentence is no longer describing anything
					// and is free to rot into cover for a later violation.
					if excused && claimed == impl {
						t.Fatalf("%s/%s carries an exception (%q) but the cell and the "+
							"code now AGREE. Delete the exception: an excuse nobody has "+
							"to re-read is where the next real violation hides.",
							row.PresetID, join.cap, why)
					}
					if excused {
						t.Logf("excused: %s", why)
						return
					}

					switch {
					case claimed && !impl:
						t.Errorf("%s claims %q (%s) works, and nothing in internal/oauth "+
							"implements it.\n\n"+
							"This is the expensive direction: the matrix is rendered as the "+
							"table an operator PICKS A PLATFORM FROM, in the UI and in "+
							"docs/PLATFORMS.md. A cell reading Works for something no code "+
							"does is a promise made at the moment somebody is deciding what "+
							"to rely on.\n\n"+
							"BEFORE MAKING THIS PASS, CHECK WHICH MECHANISM THE PLATFORM "+
							"ACTUALLY USES -- this test knows about one per column and has "+
							"already been wrong twice about that. If the capability reaches "+
							"the operator another way, add an exception here SAYING WHICH "+
							"WAY. Do not widen the resolver until you have checked, and do "+
							"not just take the claim back if it is true.",
							row.PresetID, join.cap, join.label)
					case impl && !claimed:
						t.Errorf("%s implements %q (%s) but its cell reads %q.\n"+
							"The capability works and every surface an operator reads tells "+
							"them it does not, so the feature is built and unused. Set the "+
							"cell to SupportYes and mirror it into ui/src/lib/capabilities.ts "+
							"and docs/PLATFORMS.md.",
							row.PresetID, join.cap, join.label, row.Caps[join.cap])
					}
				})
			}
		})
	}
}

// TestEveryJoinedCapabilityIsAColumnAndEveryExceptionNamesARealPlatform keeps
// the table above from describing things that are not there -- a capability
// that was renamed, or an exception for a platform that has been removed. Both
// would silently stop asserting anything.
func TestEveryJoinedCapabilityIsAColumnAndEveryExceptionNamesARealPlatform(t *testing.T) {
	cols := map[Capability]bool{}
	for _, c := range CapabilityColumns() {
		cols[c.Key] = true
	}
	rows := map[db.Platform]bool{}
	for _, r := range PlatformCapabilities() {
		rows[r.Platform] = true
	}

	for _, join := range capabilityJoins() {
		if !cols[join.cap] {
			t.Errorf("the join table names %q, which is not a capability column — it "+
				"asserts about a cell that renders nowhere", join.cap)
		}
		for p, why := range join.exceptions {
			if !rows[p] {
				t.Errorf("%q carries an exception for %q, which is not a platform in the "+
					"matrix: %s", join.cap, p, why)
			}
		}
	}
}

// TestTheChatColumnsAreKnowinglyUnjoined records what this file does NOT cover,
// so the gap is a decision rather than an oversight.
//
// chatRead, chatSend and moderation are implemented in internal/chat, by
// adapters this package cannot see -- and deliberately: Rumble has chat with no
// OAuth provider at all, so the resolver for those three is a registry in
// another package rather than anything in here. Joining them means either
// importing internal/chat from an internal/oauth test, or moving the join into
// internal/chat where the adapters live. The second is the right shape.
//
// Written as a test rather than a comment so that adding a chat resolver to
// this package makes somebody delete it on purpose.
func TestTheChatColumnsAreKnowinglyUnjoined(t *testing.T) {
	joined := map[Capability]bool{CapViewerStats: true} // the stats drift test
	for _, j := range capabilityJoins() {
		joined[j.cap] = true
	}
	unjoined := []Capability{}
	for _, c := range CapabilityColumns() {
		if !joined[c.Key] {
			unjoined = append(unjoined, c.Key)
		}
	}

	want := map[Capability]bool{CapChatRead: true, CapChatSend: true, CapModeration: true}
	for _, c := range unjoined {
		if !want[c] {
			t.Errorf("%q has no capability<->code join and is not one of the three chat "+
				"columns this file knowingly excludes. A new column without a join is "+
				"how the matrix drifts from the code, which is the whole reason this "+
				"file exists — add it to capabilityJoins(), or say here why it cannot "+
				"be joined.", c)
		}
		delete(want, c)
	}
	for c := range want {
		t.Errorf("%q is recorded here as unjoinable, but it now has a join. Delete it "+
			"from this test's exclusion list.", c)
	}
}
