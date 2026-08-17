package api

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

/* "A PHASE RECORDED AGAINST ONE PLATFORM'S BROADCAST MUST NOT BE ATTRIBUTED TO
 * ANOTHER'S" -- forgetPlatformSwitch's own words, and nothing tested it.
 *
 * The rule fires when a destination is edited onto a platform whose broadcasts
 * cannot be commanded -- Twitch, Kick, a custom RTMP endpoint -- while still
 * holding a BroadcastControl block recorded against the platform it left.
 *
 * BOTH DIRECTIONS COST SOMETHING, WHICH IS WHY THE FUNCTION HAS A COMMENT AND
 * WHY IT NEEDS TESTS FROM BOTH SIDES:
 *
 *   Clearing costs a lingering broadcast on the platform that was left. It is
 *   nobody's job to end it any more, so the id goes to the LOG -- "the only
 *   place it can go that does not lie" -- and an operator can end it by hand.
 *
 *   NOT clearing costs a wrong id acted on with the wrong token. That is the
 *   worse one. A YouTube broadcast id sitting on a row that now points at
 *   Twitch is a live broadcast identifier the coordinator would carry forward,
 *   and the phase attached to it describes a broadcast on a channel this
 *   destination no longer publishes to.
 *
 * The tests below drive considerRow, not forgetPlatformSwitch directly: the
 * decision to call it lives in considerRow's `LifecycleFor` miss, and a test
 * that called the helper directly would pass even if nothing ever reached it.
 */

// capturingLog returns a logger and the buffer it writes to, so the WARN that
// carries the abandoned broadcast id can be asserted on. The id in that line is
// the only remaining way an operator can find the broadcast to end it.
func capturingLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// switchFixture builds a coordinator over one row, with no live platform
// provider registered: LifecycleFor misses for every platform, which is the
// state a switched destination is in.
func switchFixture(t *testing.T, row *db.Destination) (*lifecycleCoordinator, *fakeLifecycleStore, *bytes.Buffer) {
	t.Helper()
	log, buf := capturingLog()
	store := &fakeLifecycleStore{rows: map[int64]*db.Destination{}}
	cp := *row
	store.rows[row.ID] = &cp
	c := newLifecycleCoordinator(log, store, oauth.Set{},
		func(context.Context, int64) (*db.PlatformAccount, error) { return nil, nil },
		func(lifecycleFault) {},
	)
	return c, store, buf
}

// THE DIRECTION THAT COSTS MOST: a recorded broadcast must not survive onto a
// platform that did not create it.
func TestSwitchingAPlatformClearsTheBroadcastRecordedAgainstTheOldOne(t *testing.T) {
	row := &db.Destination{
		ID: 3, Name: "was youtube", Kind: db.DestRTMP,
		// Edited onto Twitch, still carrying YouTube's broadcast.
		Platform: db.PlatformTwitch,
		Enabled:  true,
		Lifecycle: db.BroadcastControl{
			BroadcastID: "yt-broadcast-77",
			Phase:       "live",
		},
	}
	c, store, _ := switchFixture(t, row)
	c.track(row.ID, lifecycleTarget{})

	c.considerRow(context.Background(), row, sweepEverything)

	stored := store.rows[3]
	if !stored.Lifecycle.Empty() {
		t.Fatalf("the destination is on %s and still carries broadcast %q at phase %q.\n"+
			"That id names a broadcast on a channel this destination no longer "+
			"publishes to, and the coordinator would carry it forward — acting on the "+
			"wrong id with the wrong token, which is the failure the function's own "+
			"comment is about.",
			stored.Platform, stored.Lifecycle.BroadcastID, stored.Lifecycle.Phase)
	}

	// Untracked too, or the sweep keeps visiting a row it has nothing to do for
	// and Wanted() keeps the engine's 2s snapshot alive for it.
	c.mu.Lock()
	_, tracked := c.tracked[3]
	c.mu.Unlock()
	if tracked {
		t.Error("the destination is still tracked after the platform switch, so the " +
			"sweep and the engine's edge delivery both keep paying for it")
	}
}

// THE COST OF CLEARING IS A LINGERING BROADCAST, AND THE LOG IS THE ONLY PLACE
// THE ID CAN GO.
//
// Once the block is cleared nothing in the database remembers the broadcast, so
// the WARN is the operator's only route to ending it. A clear that logged
// nothing would be silent data loss dressed as tidying up.
func TestClearingAnAbandonedBroadcastSaysWhichOneWasAbandoned(t *testing.T) {
	row := &db.Destination{
		ID: 3, Name: "was youtube", Kind: db.DestRTMP, Platform: db.PlatformKick, Enabled: true,
		Lifecycle: db.BroadcastControl{BroadcastID: "yt-broadcast-77", Phase: "live"},
	}
	c, _, buf := switchFixture(t, row)

	c.considerRow(context.Background(), row, sweepEverything)

	out := buf.String()
	if !strings.Contains(out, "yt-broadcast-77") {
		t.Errorf("the abandoned broadcast id is not in the log.\n"+
			"Nothing in the database remembers it once the block is cleared, so this "+
			"line is the only way an operator can find the broadcast to end it in the "+
			"platform's own console.\ngot: %s", out)
	}
	if !strings.Contains(strings.ToLower(out), "still live") &&
		!strings.Contains(strings.ToLower(out), "end it") {
		t.Errorf("the log names an id but not what to do about it: %s", out)
	}
}

// THE OTHER DIRECTION: the clear must not fire for a destination that is still
// on a platform whose broadcasts can be commanded.
//
// An over-eager clear is not a smaller bug than a missing one -- it wipes the
// phase of a broadcast that IS on air, so the coordinator forgets it is driving
// a live show and stops ending it.
func TestARowStillOnALifecyclePlatformKeepsItsBroadcast(t *testing.T) {
	yt := &ytFake{}
	row := lifecycleTestRow(true, "live")
	c, store, _ := lifecycleFixture(t, yt, row)

	// The premise: LifecycleFor resolves for YouTube here, so considerRow takes
	// the branch that does NOT abandon. Without this the test would pass by
	// reaching the same clear it is trying to prove does not happen.
	if _, ok := c.providers.LifecycleFor(db.PlatformYouTube); !ok {
		t.Fatal("fixture: YouTube has no lifecycle provider, so this would pass for " +
			"the wrong reason")
	}

	c.considerRow(context.Background(), store.rows[3], sweepEverything)

	if got := store.rows[3].Lifecycle.BroadcastID; got == "" {
		t.Fatal("a destination still on YouTube had its recorded broadcast wiped.\n" +
			"An over-eager clear is not the smaller bug: the coordinator forgets it is " +
			"driving a broadcast that IS on air, so nothing ever ends it, and the show " +
			"stays live on the channel after the operator stops the destination.")
	}
}

// An already-empty block must not produce a warning. The sweep visits every
// non-lifecycle destination on every pass -- every Twitch row, every custom
// RTMP endpoint -- and a line each time would bury the one that matters.
func TestAPlatformWithNoRecordedBroadcastIsClearedQuietly(t *testing.T) {
	row := &db.Destination{
		ID: 4, Name: "plain twitch", Kind: db.DestRTMP, Platform: db.PlatformTwitch, Enabled: true,
	}
	c, _, buf := switchFixture(t, row)

	for i := 0; i < 5; i++ {
		c.considerRow(context.Background(), row, sweepEverything)
	}

	if out := buf.String(); strings.Contains(out, "has been cleared") {
		t.Errorf("an ordinary Twitch destination logged an abandonment warning on every "+
			"sweep. There is nothing to abandon, and a line per pass per row buries the "+
			"one case where a real broadcast was dropped.\ngot: %s", out)
	}
}
