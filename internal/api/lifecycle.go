package api

// The broadcast lifecycle coordinator: the thing that decides WHEN a platform's
// broadcast goes live, and when it ends.
//
// IF YOU ARE HERE BECAUSE A BROADCAST DID NOT START, read this paragraph and
// then go and look at the destination's `lifecycle` block in the API (phase,
// attempts, fault). Nothing below guesses. Every decision is made from what the
// platform said about the broadcast in the last fifteen seconds, plus one fact
// out of the destination row: whether the operator wants it running. If the
// phase says "testing" and nothing is moving, the fault field says why; if the
// fault field is empty and attempts is zero, the platform is being asked and is
// answering "not yet" -- which is what errorStreamInactive means and is normal
// for the first seconds of a show.
//
// ------------------------------------------------------------------ THE RULE
//
// A TRANSITION FAILURE NEVER STOPS THE STREAM. Not once, not as a cleanup, not
// as a "safe" fallback. It is the one rule in this file with a test named after
// it, because it is the only rule here whose violation could take a show off
// the air.
//
// Why it is not merely a preference: YouTube REQUIRES AN ACTIVE INGEST TO
// TRANSITION. Stopping the stream because a transition failed destroys the only
// condition under which a retry could ever succeed -- so the "safe" cleanup is
// not conservative, it is the one action that makes the failure permanent. A
// failure therefore ESCALATES: it raises a fault on the row, an alert and a
// webhook, and the stream carries on being delivered to a platform that has not
// yet put it in front of an audience.
//
// The rule is enforced structurally rather than by care:
//
//  1. NO PROCESS HANDLE. This file holds a store, a provider set, a token
//     helper, an escalation callback and a channel. There is no engine, no
//     manager, no supervisor and no reconcile in reach of any expression here.
//     TestTheLifecycleCoordinatorCannotReachAnythingThatRunsAProcess parses
//     this file and fails if one appears.
//  2. THE ONLY WRITER CANNOT WRITE INTENT. db.UpdateLifecycle writes one
//     column and discards everything else -- set Enabled inside its callback
//     and it is thrown away. And that column reaches no FFmpeg argument, so no
//     write made from here can cycle a process either (pinned by
//     internal/engine/lifecycle_spec_test.go).
//  3. THE END CALL IS GATED ON OPERATOR INTENT, RE-CHECKED LAST. The transition
//     to `complete` has exactly one call site, and reaching it requires a fresh
//     read INSIDE a transaction that still says !Enabled. A destination that is
//     delivering is by definition Enabled -- a disabled row is torn down by the
//     reconcile -- so the end call is unreachable for anything on air.
//
// --------------------------------------------------------------- THE SHAPE
//
// A SWEEP, WOKEN BY EDGES. Durable state lives in the destination row and a 15s
// sweep re-drives it; the edges the engine derives are latency only. That single
// choice makes a dropped edge, a partial failure, a token expiry and a daemon
// restart the same code path -- the next sweep -- rather than four.
//
// The sweep is the proven shape from internal/api/preannounce.go, and the
// promise at the top of that file applies here in a stronger form: nothing in
// this file may fail a go-live, because nothing in this file can reach the thing
// that performs one.
//
// IDEMPOTENCY COMES FROM ASKING, NOT REMEMBERING. Every transition is preceded
// by reading the platform's current state, so "the request succeeded and the
// response was lost" costs one read rather than a wrong action -- and a
// broadcast already in the phase we want needs no call at all. See
// oauth.BroadcastLifecycleState's header, which makes the same argument from
// the other side.
//
// ------------------------------------------------- WHAT IT DELIBERATELY IS NOT
//
// It does not create broadcasts. A destination with no recorded broadcast id is
// simply out of scope -- creating one is the announce path's job (preannounce.go
// and the ingest path), and a coordinator that could create would be a
// coordinator that could put an unannounced public event page on somebody's
// channel because a sweep read a row wrong.
//
// It does not drive Facebook. oauth.Facebook.EndBroadcast exists and is already
// wired to a route and a button, but Facebook ends a live video when the BYTES
// STOP -- its own header says so, quoting Meta -- so a coordinator END there
// would be a second writer duplicating a platform behaviour. Only platforms that
// implement oauth.BroadcastLifecycler are driven, which today means YouTube.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/redact"
)

const (
	// lifecycleTick is how often every tracked broadcast is re-derived.
	//
	// Seconds rather than preannounce's minutes, because the two watch
	// different clocks: that sweep is waiting for a show that is days away, and
	// this one is the delay between an encoder connecting and an audience being
	// able to see it. Fifteen seconds is the worst case an operator waits on a
	// watch page that says "starting soon", and it is also the retry interval --
	// there is no second backoff anywhere in this file.
	lifecycleTick = 15 * time.Second
	// lifecycleGiveUpAfter is how many CONSECUTIVE faulted sweeps mean asking
	// again is not going to help. Twenty against a fifteen-second tick is five
	// minutes of unbroken refusal.
	//
	// COUNTED, NOT TIMED, and the reasoning is announceOne's verbatim: a wall
	// clock would have to be persisted, and a timestamp absent on every row an
	// upgrade finds reads as infinitely old, which would age every record at
	// once on the first sweep after an upgrade.
	//
	// Reaching it HOLDS the fault rather than resolving it. There is no cleanup
	// at the end of the budget -- see THE RULE above -- so the destination goes
	// on delivering and the operator is looking at a sentence that says what to
	// do. The count clears on the next success or on a fresh UP edge.
	lifecycleGiveUpAfter = 20
	// lifecycleLiveRecheckEvery is how many sweeps a CONFIRMED-LIVE broadcast is
	// allowed to pass unasked-about before the platform is asked again. Forty
	// sweeps against a fifteen-second tick is ten minutes.
	//
	// COUNTED, NOT TIMED, for lifecycleGiveUpAfter's reason exactly: a wall clock
	// would have to be persisted, and a timestamp absent on every row an upgrade
	// finds reads as infinitely old, so the first sweep after an upgrade would
	// re-read every live broadcast in the install at once.
	//
	// THE COUNT IS IN MEMORY AND ABSENT MEANS DUE. That makes a daemon restart
	// re-confirm every live broadcast once, which is what lifecycleLoop's boot
	// pass already claims to do and could not: the settled branch returned before
	// it ever asked. One read per live destination per daemon start is the whole
	// price of that promise being true.
	//
	// WHY FORTY AND NOT ONE, in calls per destination per day, against Google's
	// published default project allocation of 10,000 units a day -- which is
	// SHARED with metadata pushes, chat, stats and every other call polyemesis
	// makes on that account:
	//
	//	every sweep   5,760 reads x 2 calls = 11,520/day. One destination
	//	              exhausts the whole allocation before mid-afternoon, and an
	//	              install out of quota cannot END anything -- which is
	//	              strictly worse than a stale label.
	//	every 40th      144 reads x 2 calls =    288/day. Under 3% of the
	//	              allocation per destination, and a platform-side end is
	//	              noticed within ten minutes.
	//
	// Ten minutes is the CEILING on how long a wrong label can stand, not the
	// usual case: an UP edge spends the budget immediately (see
	// forgetLiveRecheck), and that is the edge the common cause of a
	// platform-side end -- enableAutoStop firing when the ingest stopped --
	// produces for free on its way back.
	lifecycleLiveRecheckEvery = 40
	// lifecycleLiveRecheckAfterEdge is the FLOOR an edge may pull the budget
	// down to, and it exists because an edge is not rare.
	//
	// Four sweeps is about a minute at the ordinary tick -- soon enough that a
	// broadcast ended in Studio is noticed while the operator is still looking
	// at the card, and far enough out that a destination flapping every twelve
	// seconds cannot spend more than one re-read per minute. Without a floor
	// that same destination forced a re-read on every edge: roughly 7,200 a
	// day, 14,400 API units, against an allocation of 10,000.
	lifecycleLiveRecheckAfterEdge = 4
	// lifecycleCallTimeout bounds one platform round trip. Matched to
	// preannounce's, and for the same reason: it has to be shorter than the
	// sweep interval times the give-up budget, or "consecutive sweeps" would
	// stop meaning consecutive attempts.
	lifecycleCallTimeout = 20 * time.Second
	// lifecycleWakeQueue is the depth of the edge queue.
	//
	// A FULL QUEUE DROPS, and that is safe by construction rather than by luck:
	// an edge is only a wakeup, and every fact it could have carried is
	// re-derived from the row by the next tick. The alternative -- blocking --
	// would put a database write and an HTTP call on the engine's observe
	// goroutine, which is the one thing LifecycleObserver forbids.
	lifecycleWakeQueue = 64
)

// The DOWN reasons, copied verbatim from internal/hooks/watch.go, which is the
// only place they are produced.
//
// COPIED RATHER THAN IMPORTED because they are not exported there -- they are
// free text on an Event, redacted on the way to a webhook, and promoting them to
// package constants would make a webhook payload's prose into an API. So they
// are pinned instead: TestTheDownReasonsAreTheOnesTheWatcherActuallyEmits drives
// a real hooks.Watcher through the three transitions and fails if a spelling
// here stops matching what it emits.
//
// THE WHOLE END POLICY IS THIS SWITCH, so it is worth saying what each costs:
//
//	"disabled" -> END. The operator turned it off. That is explicit intent and
//	              the show is over.
//	"removed"  -> END. The row was deleted. Same intent, and the row is gone, so
//	              the tuple has to come from the in-memory mirror.
//	"stopped"  -> NOTHING, AND THIS IS THE IMPORTANT ONE. The process died.
//	              A completed YouTube broadcast CANNOT RETURN TO LIVE, so ending
//	              on a fault means an FFmpeg crash permanently destroys a show
//	              the operator could otherwise have recovered by doing nothing
//	              at all -- the supervisor reconnects to the same key and the
//	              same bound stream, and the broadcast is still live. Leave it
//	              live, let it recover, let the alerts fire. If nothing ever
//	              reconnects, the platform's own answer is enableAutoStop
//	              (oauth.BroadcastSettings.EnableAutoStop), which knows more
//	              about the ingest than this process does.
const (
	lifecycleReasonDisabled = "disabled"
	lifecycleReasonRemoved  = "removed"
	lifecycleReasonStopped  = "stopped"
)

// The platform's own words for a broadcast's phase, lowercased for comparison.
//
// This is YouTube's vocabulary, because YouTube is the only platform that
// implements oauth.BroadcastLifecycler today. A SECOND PLATFORM WITH DIFFERENT
// WORDS DOES NOT MISBEHAVE HERE, it stalls visibly: an unrecognised phase sends
// nothing and counts an attempt, so it surfaces as a fault naming the word
// rather than as a broadcast that sits in silence for ever.
const (
	phaseCreated      = "created"
	phaseReady        = "ready"
	phaseTesting      = "testing"
	phaseTestStarting = "teststarting"
	phaseLive         = "live"
	phaseLiveStarting = "livestarting"
	phaseComplete     = "complete"
	phaseRevoked      = "revoked"
	// phasePrePublished is X's PRE_PUBLISHED as recorded here: this process sent the PUBLISH and X is still
	// moving the broadcast to RUNNING. It is on its way to air, so it counts as confirmed for ending.
	phasePrePublished = "prepublished"
)

// isTerminalPhase reports whether the platform has said this broadcast is over
// in a way that cannot be walked back. YouTube documents no transition out of
// either, and a completed broadcast cannot return to live -- which is the whole
// reason the coordinator refuses to send `complete` on a crash.
// endableFromPhase reports whether a broadcast in this phase is one this
// process confirmed on air, and therefore one it may end.
//
// The empty phase is the case that matters: it means no transition was ever
// recorded for this broadcast, which covers a row that predates the coordinator
// (an upgrade) and a broadcast created in somebody else's console. `created`
// and `ready` are excluded for the reason lifecycleTarget.Phase documents --
// YouTube has no documented path from either to `complete`. X's `prepublished`
// is included: it is recorded only after this process sent the PUBLISH.
func endableFromPhase(phase string) bool {
	switch {
	case strings.EqualFold(phase, phaseTesting), strings.EqualFold(phase, phaseLive),
		strings.EqualFold(phase, phasePrePublished):
		return true
	default:
		return false
	}
}

func isTerminalPhase(phase string) bool {
	return strings.EqualFold(phase, phaseComplete) || strings.EqualFold(phase, phaseRevoked)
}

const ()

// lifecycleStore is the coordinator's entire reach into the database.
//
// TWO METHODS, AND NEITHER CAN START OR STOP ANYTHING. An interface rather than
// *db.DB so a test can drive the whole state machine without a file on disk --
// and so that a reader can see the blast radius by reading four lines instead of
// auditing a package.
type lifecycleStore interface {
	ListDestinations() ([]*db.Destination, error)
	UpdateLifecycle(id int64, apply func(*db.Destination) bool) (*db.Destination, error)
	// GetDestination CONFIRMS A DELETION RATHER THAN INFERRING ONE, and that
	// distinction is the difference between ending a broadcast because a row is
	// gone and ending it because a query did not mention it.
	//
	// endOrphan used to fire on absence from ListDestinations alone. That is
	// correct only while the listing is unfiltered and whole-table, which it is
	// -- and which nothing enforced. Scope that query later, to one source or
	// to enabled rows or to a page, and every live broadcast outside the scope
	// looks deleted and gets completed. The failure would be silent, permanent
	// on YouTube, and would arrive as "why did half my broadcasts end".
	//
	// A second look costs one indexed read on a path that already spends two
	// platform calls, and it turns a whole class of future refactor from
	// dangerous into merely wrong.
	GetDestination(id int64) (*db.Destination, error)
}

// lifecycleFault is one thing an operator has to be told about. It carries no
// token and no stream key; the broadcast id is deliberately included, because it
// is the one string that lets somebody find the broadcast in the platform's own
// console and fix it by hand.
type lifecycleFault struct {
	SourceID      int64
	DestinationID int64
	Destination   string
	Platform      string
	BroadcastID   string
	Detail        string
}

// lifecycleTarget is everything needed to act on one broadcast WITHOUT the row.
//
// It exists for exactly one case: a destination that has been DELETED. The
// "removed" edge carries only {ID, Name, Platform} and the row -- account id,
// broadcast id -- is already gone, so a mirror refreshed on every sweep is the
// only way the broadcast can still be named. If the daemon dies before that end
// lands the tuple is lost and enableAutoStop is the backstop; the cost is a
// lingering watch page, never a show.
type lifecycleTarget struct {
	// destinationID is unexported and set by track, so a caller cannot build a
	// target that names one destination and is filed under another.
	destinationID int64
	SourceID      int64
	Name          string
	Platform      db.Platform
	AccountID     int64
	BroadcastID   string
	// Phase is the last phase confirmed for this broadcast. A target whose
	// broadcast never aired must not be ended when its row disappears -- there
	// is nothing to end, and sending `complete` to a `created` broadcast is a
	// transition YouTube documents no path for.
	Phase string
	// maybeLive marks an X destination whose go-live was sent and not resolved: a broadcast may be on air behind its
	// pending handle. It is kept (and read again) after the row is deleted, until an in-window broadcast is found and
	// ended or sentMS is old enough that none can appear. sentMS is the local time the go-live was sent.
	maybeLive bool
	sentMS    int64
}

// lifecycleCoordinator drives every tracked broadcast towards the state the
// operator's stored intent implies.
type lifecycleCoordinator struct {
	log       *slog.Logger
	store     lifecycleStore
	providers oauth.Set
	// tokenFor returns the ACCOUNT with a refreshed token on it, not a token
	// string, and it performs the refresh. A function rather than a *Server so
	// this type holds nothing that could reach a process; see the header.
	tokenFor func(context.Context, int64) (*db.PlatformAccount, error)
	// retokenFor is tokenFor for a token the platform refused before its stored expiry: it refreshes that token and
	// returns the account. Nil means a refused token is not retried here.
	retokenFor func(ctx context.Context, accountID int64, rejected string) (*db.PlatformAccount, error)
	// escalate is the ONLY way a fault leaves this file. Everything it can do is
	// tell somebody: a row field, an alert, a webhook. It cannot stop a stream,
	// because the thing that could stop a stream is not in scope here.
	escalate func(lifecycleFault)

	// airing reports whether a destination is still on the air with its
	// broadcast delay's buffered tail (#11). Its row is already disabled -- the
	// operator pressed Stop -- but viewers are still watching, so ending the
	// platform broadcast now would cut them off up to fifteen minutes early.
	// Nil means no delay is possible here (tests, an engine-less server).
	airing func(id int64) bool

	// wake carries destination ids whose state may have changed. Buffered and
	// lossy on purpose -- see lifecycleWakeQueue.
	wake chan int64
	// wanted is the engine's observe gate, cached so it can be read on a 2s
	// tick. See engine.LifecycleObserver.Wanted: an install with no lifecycle
	// destination must not start paying for a status snapshot it has no use for.
	wanted atomic.Bool

	// now is the clock; nil is time.Now. A test sets it.
	now func() time.Time
	// rearm is when each held, non-terminal destination was last tried (see lifecycleRearmEvery). In memory: after a
	// restart a held destination is due at once, which is the patient direction.
	rearm map[int64]time.Time

	mu sync.Mutex
	// tracked mirrors the acting tuple for every destination the coordinator is
	// driving, so a row that is deleted can still be ended. Rebuilt every sweep.
	tracked map[int64]lifecycleTarget
	// orphanAttempts counts consecutive failures to end a DELETED destination's
	// broadcast. In memory because the row it belonged to no longer exists;
	// losing the count on restart only makes the coordinator more patient, which
	// is the safe direction.
	orphanAttempts map[int64]int
	// clearFaults holds destinations whose fault counter an UP edge has reset.
	// A destination that has just started delivering is one whose previous
	// refusals -- almost always "no bytes are arriving" -- have stopped being
	// true, and holding a stale give-up against it would keep a working show off
	// the air out of bookkeeping.
	clearFaults map[int64]bool
	// liveSkips is how many more sweeps each confirmed-live destination may be
	// skipped before its state is read again. See lifecycleLiveRecheckEvery;
	// absent means due now, which is the safe direction and the one a restart
	// lands in.
	liveSkips map[int64]int
	// quietX is the broadcast each switched-off X destination was last read as
	// never aired on. One read after the switch-off is what proves a recorded
	// "created" broadcast was not in fact published (a publish whose record was
	// lost); after it, the row is not read again until something changes.
	quietX map[int64]string

	// announce is told every transition the platform CONFIRMED -- a broadcast
	// moved on air or into testing, or ended -- after it is recorded. Nil means
	// nobody is listening. It is the real-time events API's go-live and stop
	// (broadcast.live, broadcast.complete); set once in New, before any loop
	// runs, and never changed. Must not block: it runs on the sweep.
	announce func(broadcastPhase)

	// goLiveTitle is the title an X PUBLISH carries. X takes a title only at that
	// step, so this is the one chance to send it. Nil means the destination's own
	// stored GoLiveTitle; the server wires one that prefers a recurring show's
	// rendered title. Set once in New, before any loop runs.
	goLiveTitle func(d *db.Destination) string

	// pointChat tells the X chat adapter of one account which broadcast is live,
	// or "" when it is over, so the adapter does not wait for its own poll to
	// find out. Nil means nobody is listening. Must not block: it runs on the
	// sweep.
	pointChat func(accountID int64, broadcastID string)
}

// xPublisher is the X provider's PUBLISH with its title and chat option. The
// generic TransitionBroadcast sends an empty publish, so the coordinator asks
// for this one by name when it takes an X broadcast live.
type xPublisher interface {
	PublishBroadcast(ctx context.Context, accessToken, broadcastID string, p oauth.XPublish) (*oauth.XPublishResult, error)
}

// titleFor is the title the PUBLISH for d carries.
func (c *lifecycleCoordinator) titleFor(d *db.Destination) string {
	if c.goLiveTitle != nil {
		return c.goLiveTitle(d)
	}
	return d.GoLiveTitle
}

// tellChat points d's X chat adapter at broadcastID ("" for none). Only X has a
// chat adapter that needs it.
func (c *lifecycleCoordinator) tellChat(platform db.Platform, accountID *int64, broadcastID string) {
	if c.pointChat == nil || platform != db.PlatformX || accountID == nil {
		return
	}
	c.pointChat(*accountID, broadcastID)
}

// transition sends one start transition. An X go-live goes through
// PublishBroadcast so it carries the title; everything else is the generic
// transition, unchanged.
func (c *lifecycleCoordinator) transition(ctx context.Context, prov oauth.BroadcastLifecycler,
	acct *db.PlatformAccount, d *db.Destination, broadcastID string, to oauth.BroadcastPhase) (*oauth.TransitionResult, error) {
	if pub, ok := prov.(xPublisher); ok && d.Platform == db.PlatformX && to == oauth.PhaseLive {
		r, err := pub.PublishBroadcast(ctx, acct.AccessToken, broadcastID,
			oauth.XPublish{Title: c.titleFor(d), ShouldNotTweet: xShouldNotTweet(d)})
		if err != nil {
			return nil, err
		}
		if r.Existing {
			c.log.Info("broadcast lifecycle: X already had a live broadcast on this source, so that one was adopted "+
				"and the request's title and options were ignored",
				"destination", d.Name, bcAttr(r.ID))
		}
		for _, w := range r.Warnings {
			c.log.Warn("broadcast lifecycle: X published the broadcast with a warning",
				"destination", d.Name, bcAttr(broadcastID), "detail", redact.Redact(w))
		}
		// r.ID is the REAL broadcast id X made on go-live. broadcastID was the pending handle (or a broadcast an
		// earlier version recorded), and the caller must record and point chat at the one that exists on X.
		return &oauth.TransitionResult{BroadcastID: r.ID, Requested: to, Status: r.State, Warnings: r.Warnings}, nil
	}
	return prov.TransitionBroadcast(ctx, acct.AccessToken, broadcastID, to)
}

// xShouldNotTweet is whether d's X PUBLISH suppresses X's announcement post, from
// the destination's XGoLiveTweet setting: never posts, every always does, and the
// default (first) posts on a show's first go-live only, so a broadcast opened by
// renew within the same show (BroadcastControl.Renewed) goes live quietly.
// PROVISIONAL with should_not_tweet itself: no live go-live has confirmed it.
func xShouldNotTweet(d *db.Destination) bool {
	switch d.XGoLiveTweetMode() {
	case db.XTweetNever:
		return true
	case db.XTweetEveryGoLive:
		return false
	default:
		return d.Lifecycle.Renewed
	}
}

// broadcastPhase is one confirmed transition, as the events stream reports it.
type broadcastPhase struct {
	DestinationID int64
	SourceID      int64
	Destination   string
	Platform      string
	BroadcastID   string
	// Phase is the platform's own word for where the broadcast now is
	// (testing, live, complete), never the one that was requested.
	Phase string
	// Ended is true for an end, false for a start.
	Ended bool
	// Redundant is true when the platform reported it was already there.
	Redundant bool
}

func (c *lifecycleCoordinator) tell(p broadcastPhase) {
	if c.announce != nil {
		c.announce(p)
	}
}

func sourceIDOf(d *db.Destination) int64 {
	if d == nil || d.SourceID == nil {
		return 0
	}
	return *d.SourceID
}

// newLifecycleCoordinator builds one. Every dependency is explicit so that what
// it CANNOT do is visible at the construction site.
func newLifecycleCoordinator(log *slog.Logger, store lifecycleStore, providers oauth.Set,
	tokenFor func(context.Context, int64) (*db.PlatformAccount, error),
	escalate func(lifecycleFault)) *lifecycleCoordinator {
	return &lifecycleCoordinator{
		log:            log,
		store:          store,
		providers:      providers,
		tokenFor:       tokenFor,
		escalate:       escalate,
		wake:           make(chan int64, lifecycleWakeQueue),
		tracked:        map[int64]lifecycleTarget{},
		orphanAttempts: map[int64]int{},
		clearFaults:    map[int64]bool{},
		liveSkips:      map[int64]int{},
		quietX:         map[int64]string{},
		rearm:          map[int64]time.Time{},
	}
}

// clock is the coordinator's time: time.Now unless a test set now.
func (c *lifecycleCoordinator) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// lifecycleRearmEvery is how often a destination that has used its budget on a counted (non-terminal) fault is tried
// again. Level-triggered, not edge-triggered: while the source reports bytes, one try per interval, until one works.
const lifecycleRearmEvery = 2 * time.Minute

// rearmDue reports whether a held, non-terminal destination may be tried again now, and if so notes the try. A
// destination with no entry (after a restart) is due at once.
func (c *lifecycleCoordinator) rearmDue(id int64) bool {
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, ok := c.rearm[id]; ok && now.Sub(last) < lifecycleRearmEvery {
		return false
	}
	c.rearm[id] = now
	return true
}

// noteRearm starts the interval at the moment a destination is held.
func (c *lifecycleCoordinator) noteRearm(id int64) {
	c.mu.Lock()
	c.rearm[id] = c.clock()
	c.mu.Unlock()
}

// ---------------------------------------------------------------- the observer

// Observe receives one edge from the engine. It enqueues and returns.
//
// NO HTTP, NO DATABASE, NO SLEEP, NO ENGINE LOCK. This runs on the goroutine
// that raises every alert and publishes every webhook for a programme, on a
// two-second tick; anything slow here delays all three.
//
// AND IT IS WHERE "stopped" DIES. A process fault is dropped on the floor at the
// earliest possible point rather than being passed on and filtered later,
// because the filter that has to be right is easier to keep right when it is
// three lines long and next to the reason it exists.
func (c *lifecycleCoordinator) Observe(ev hooks.Event) {
	if ev.Destination == nil {
		return
	}
	id := ev.Destination.ID
	switch ev.Trigger {
	case hooks.TriggerDestinationUp:
		// Delivering again. Whatever it failed at before, it is worth asking
		// once more -- see clearFaults.
		c.mu.Lock()
		c.clearFaults[id] = true
		c.mu.Unlock()
	case hooks.TriggerDestinationDown:
		switch ev.Reason {
		case lifecycleReasonDisabled, lifecycleReasonRemoved:
			// Explicit intent, in both cases. Wake the sweep so the end does
			// not wait out a whole tick.
		default:
			// "stopped" -- a crash -- and anything a future release adds. THE
			// DEFAULT IS SILENCE ON PURPOSE: an unrecognised reason costs at
			// most fifteen seconds of latency, because the sweep re-derives
			// everything from the row anyway, whereas guessing that an unknown
			// reason means "the operator meant to end this" costs a show.
			return
		}
	default:
		return
	}
	select {
	case c.wake <- id:
	default:
		// Dropped. The sweep re-derives; see lifecycleWakeQueue.
	}
}

// Wanted reports whether any destination currently needs the engine's edges.
func (c *lifecycleCoordinator) Wanted() bool { return c.wanted.Load() }

// ---------------------------------------------------------------------- the loop

// lifecycleLoop runs the sweep until ctx ends.
func (c *lifecycleCoordinator) lifecycleLoop(ctx context.Context) {
	tick := time.NewTicker(lifecycleTick)
	defer tick.Stop()
	// ONE SWEEP BEFORE THE FIRST TICK, AND THAT SWEEP IS BOOT RECONCILIATION.
	// A daemon that restarts mid-show comes back to rows saying a broadcast is
	// live; this pass asks the platform about each one and either confirms it
	// (enabled, still live -- nothing to do) or ends it (disabled or deleted
	// while the daemon was down, and the platform agrees it is still on air).
	// Waiting a full tick would also make the loop's behaviour after a restart
	// differ from its behaviour in steady state, which is the sort of difference
	// nothing tests and everyone assumes away.
	c.sweepOnce(ctx, sweepEverything)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.sweepOnce(ctx, sweepEverything)
		case <-c.wake:
			// Coalesce whatever else is already queued: several destinations
			// going down together is one sweep, not one sweep each.
			for drained := true; drained; {
				select {
				case <-c.wake:
				default:
					drained = false
				}
			}
			c.sweepOnce(ctx, sweepEverything)
		}
	}
}

// drain is the shutdown pass: ends what the operator has already asked to end,
// and starts nothing.
//
// A CLEAN STOP PRODUCES NO SWEEP, which is the gap this closes: an operator who
// disables a destination and immediately stops the daemon would otherwise leave
// a broadcast on air with nothing left running to end it. The budget belongs to
// the WHOLE drain and is set by the caller -- never per destination, or twelve
// destinations turn a ten-second shutdown into two minutes and systemd kills the
// process in the middle of finalising a recording.
//
// It deliberately does NOT go live. A box that is shutting down must not put a
// broadcast in front of an audience it is about to stop feeding.
func (c *lifecycleCoordinator) drain(ctx context.Context) {
	c.sweepOnce(ctx, sweepEndsOnly)
}

// sweepMode says how much of the state machine this pass is allowed to drive.
type sweepMode int

const (
	sweepEverything sweepMode = iota
	// sweepEndsOnly runs the disabled/removed branches and skips every enabled
	// destination. Used by drain.
	sweepEndsOnly
)

// sweepOnce re-derives every tracked broadcast from durable facts.
//
// THERE IS NO STORED "WANT". Desire is computed here every time, from the row's
// Enabled flag, whether the row exists at all, the recorded phase and -- above
// all -- the platform's own answer. That is what collapses a dropped edge, a
// partial failure, an expired token and a daemon restart into one code path.
func (c *lifecycleCoordinator) sweepOnce(ctx context.Context, mode sweepMode) {
	rows, err := c.store.ListDestinations()
	if err != nil {
		c.log.Warn("broadcast lifecycle: cannot read destinations", "err", err)
		return
	}

	present := make(map[int64]bool, len(rows))
	for _, d := range rows {
		present[d.ID] = true
		if ctx.Err() != nil {
			return
		}
		c.considerRow(ctx, d, mode)
	}

	// Rows that have disappeared since the last sweep. This is the "removed"
	// case, and it is derived here rather than driven off the edge so that a
	// deletion that happened while the daemon was down is handled by the same
	// code as one that happened a second ago.
	for _, t := range c.absentTargets(present) {
		if ctx.Err() != nil {
			return
		}
		c.endOrphan(ctx, t)
	}

	// The engine's observe gate. False on an install with no lifecycle
	// destination, which is most of them, and that is what keeps the promise
	// observeLoop makes about paying for nothing.
	c.mu.Lock()
	c.wanted.Store(len(c.tracked) > 0)
	c.mu.Unlock()
}

// considerRow decides and acts for one destination.
func (c *lifecycleCoordinator) considerRow(ctx context.Context, d *db.Destination, mode sweepMode) {
	prov, ok := oauth.As[oauth.BroadcastLifecycler](c.providers, d.Platform)
	if !ok {
		// Either an ordinary platform -- Twitch, Kick, a custom RTMP endpoint --
		// or one this destination was switched TO while holding a phase recorded
		// against the platform it left.
		c.forgetPlatformSwitch(d)
		return
	}
	// The recorded id wins over the announcement mirror, and is seeded from it
	// exactly once. The mirror (db.AnnouncementSet, embedded in the `facebook`
	// column for every pre-announcing platform including YouTube) is REWRITTEN
	// whenever a schedule moves, so a phase recorded against the id it used to
	// carry would be attributed to a broadcast that is not the one on air.
	if d = c.clearRenewalOnSwitchOff(d); d == nil {
		return
	}
	broadcastID := lifecycleBroadcastID(d)
	if broadcastID == "" {
		c.considerUnopened(ctx, d, prov, mode)
		return
	}
	if d.AccountID == nil {
		// No connected account means no token, and a broadcast id nothing here
		// can act on. Not a fault: it is what a destination looks like after its
		// account was disconnected, and the remedy is the operator's.
		c.untrack(d.ID)
		return
	}

	c.trackRow(d, broadcastID)

	// STOPPED BUT STILL AIRING ITS DELAYED TAIL: not yet. The row says
	// disabled, which on its own would complete the broadcast this sweep, with
	// the tail still playing to viewers. Tracked above so the gate stays on; the
	// drain's end is a down edge, which wakes the sweep that ends it.
	if !d.Enabled && c.airing != nil && c.airing(d.ID) {
		return
	}

	cleared := c.takeClearFault(d.ID)
	if d = c.resetOnUpEdge(d, broadcastID, cleared); d == nil {
		return
	}

	if d.Enabled && d.Platform == db.PlatformX {
		// Switched on again: the next switch-off earns its own read.
		c.noteQuietX(d.ID, "")
	}
	if c.skipStateRead(d, broadcastID, mode) {
		return
	}
	c.readAndAct(ctx, prov, d, broadcastID, cleared)
}

// clearRenewalOnSwitchOff drops an X destination's renewal marker when it is
// switched off, returning the re-read row (nil if the write lost the row).
func (c *lifecycleCoordinator) clearRenewalOnSwitchOff(d *db.Destination) *db.Destination {
	if d.Enabled || d.Platform != db.PlatformX || !d.Lifecycle.Renewed {
		return d
	}
	// SWITCHING THE DESTINATION OFF ENDS THE SHOW, so the renewal marker goes
	// with it and the next show's first go-live posts its announcement.
	// Cleared here, before any branch below can return: a renewal whose open
	// failed has no broadcast and is only held, and one that never aired goes
	// quiet after one read. Both keep the rest of their block across the
	// switch-off. One write, only when the marker is set.
	return c.recordControl(d, lifecycleBroadcastID(d), func(b *db.BroadcastControl) { b.Renewed = false })
}

// considerUnopened handles a row with no recorded broadcast: it is untracked,
// and a platform that can create broadcasts opens (or holds) one.
func (c *lifecycleCoordinator) considerUnopened(ctx context.Context, d *db.Destination,
	prov oauth.BroadcastLifecycler, mode sweepMode) {
	// Taken BEFORE untrack, which clears it: an UP edge is the release of the
	// open() hold below, and untrack would otherwise throw it away unread.
	cleared := c.takeClearFault(d.ID)
	c.untrack(d.ID)
	if creator, ok := prov.(oauth.BroadcastCreator); ok && mode != sweepEndsOnly {
		c.openOrHold(ctx, d, creator, cleared)
	}
	// Every other platform: no broadcast object, so nothing to command.
	// Creating one is the announce path's job; see the header.
}

// trackRow records d as a destination this coordinator is acting on.
func (c *lifecycleCoordinator) trackRow(d *db.Destination, broadcastID string) {
	c.trackAs(d, broadcastID, d.Lifecycle.Phase)
}

// trackAs is trackRow with the phase given, for a broadcast that has just moved: the row still holds the phase
// and id it was read with, and a target filed under those would not be ended if the row vanished now.
func (c *lifecycleCoordinator) trackAs(d *db.Destination, broadcastID, phase string) {
	var sourceID int64
	if d.SourceID != nil {
		sourceID = *d.SourceID
	}
	var accountID int64
	if d.AccountID != nil {
		accountID = *d.AccountID
	}
	next := lifecycleTarget{
		SourceID:    sourceID,
		Name:        d.Name,
		Platform:    d.Platform,
		AccountID:   accountID,
		BroadcastID: broadcastID,
		Phase:       phase,
		maybeLive:   d.Lifecycle.GoLiveSentMS > 0 && publicBroadcastID(broadcastID) == "",
		sentMS:      d.Lifecycle.GoLiveSentMS,
	}
	// MERGE, DO NOT OVERWRITE. The durable row can lag a broadcast this process has verified on air: the go-live
	// succeeded and the write that records it failed. Re-tracking from that row every sweep would move the target
	// backwards to a pending handle and phase created, and a deleted row would then be let go of with the show still
	// live. So a tracked target that is a real, endable broadcast is kept over a pending or never-aired one for the
	// same row. (A renewal forgets its target explicitly, so this never holds a finished broadcast.)
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.tracked[d.ID]; ok && cur.Platform == next.Platform && publicBroadcastID(cur.BroadcastID) != "" &&
		endableFromPhase(cur.Phase) {
		_, nextPending := oauth.XPendingSource(next.BroadcastID)
		if nextPending || (next.BroadcastID == cur.BroadcastID && !endableFromPhase(next.Phase) &&
			(next.Phase == "" || strings.EqualFold(next.Phase, phaseCreated))) {
			return
		}
	}
	next.destinationID = d.ID
	c.tracked[d.ID] = next
}

// goLiveUnresolvedFor is how long after a go-live was sent a read keeps looking for the broadcast it may have made:
// the request timeout, the window X's created_at_ms is allowed to fall in, and generous slack for a list that lags.
const goLiveUnresolvedFor = 2 * time.Minute

// goLiveUnresolved reports whether a go-live sent at sentMS (local ms, 0 for none) may still have a broadcast to find.
func (c *lifecycleCoordinator) goLiveUnresolved(sentMS int64) bool {
	return sentMS > 0 && c.clock().UnixMilli()-sentMS < goLiveUnresolvedFor.Milliseconds()
}

// leftLiveFault is what the operator reads wherever tracking gives up on a broadcast a go-live may have made.
const leftLiveFault = "polyemesis may have left a broadcast live on X — check X's producer tooling and end it there."

// raiseLeftLive is the durable, visible alternative to giving up silently on a maybe-live broadcast whose row still
// exists: the fault is written to the row (so it survives a restart and shows on the destination) and raised once. The
// go-live markers stay, so a reconnect does not wipe it. It is held like every other terminal fault: Attempts is set
// to the give-up count, so a re-enabled row does not send another go-live until the fault is cleared.
//
// It reports whether the fault is durably on the row. When the write fails nothing is alerted and the caller must keep
// the target tracked, so the next sweep tries again; the alert follows the write, never the other way round.
func (c *lifecycleCoordinator) raiseLeftLive(d *db.Destination, broadcastID string) bool {
	if d.Lifecycle.Fault == leftLiveFault {
		return true
	}
	if c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
		b.Fault, b.Terminal = leftLiveFault, true
		if b.Attempts < lifecycleGiveUpAfter {
			b.Attempts = lifecycleGiveUpAfter
		}
	}) == nil {
		return false
	}
	c.raise(d, "", leftLiveFault)
	return true
}

// raiseLeftLiveTarget is raiseLeftLive for a deleted row, which has nowhere to keep a fault: it is raised.
func (c *lifecycleCoordinator) raiseLeftLiveTarget(t lifecycleTarget) {
	if c.escalate == nil {
		return
	}
	c.escalate(lifecycleFault{
		SourceID: t.SourceID, DestinationID: t.destinationID, Destination: t.Name,
		Platform: string(t.Platform), Detail: leftLiveFault,
	})
}

// forgetTarget drops the tracked target of one destination and nothing else, for a broadcast that is over.
func (c *lifecycleCoordinator) forgetTarget(id int64) {
	c.mu.Lock()
	delete(c.tracked, id)
	c.mu.Unlock()
}

// resetXPendingLifecycle forgets the pending state (a source that may no longer exist, a stale fault) of an account's X
// destinations, so a reconnect or a stream-key refresh starts them again, and bumps each row's Generation so a late
// write from a sweep that read the old row is refused. A destination with a real broadcast is left alone, and so is one
// whose go-live is in flight or unresolved (GoLiveSentMS): a broadcast may exist behind it, and the sweep resolves it.
// It returns how many were reset.
func resetXPendingLifecycle(store interface {
	ListDestinations() ([]*db.Destination, error)
	UpdateLifecycle(id int64, apply func(*db.Destination) bool) (*db.Destination, error)
}, accountID int64) int {
	rows, err := store.ListDestinations()
	if err != nil {
		return 0
	}
	n := 0
	for _, d := range rows {
		if d == nil || d.Platform != db.PlatformX || d.AccountID == nil || *d.AccountID != accountID ||
			d.Lifecycle.BroadcastID != "" || d.Lifecycle.GoLiveSentMS != 0 ||
			(d.Lifecycle.PendingSource == "" && d.Lifecycle.Fault == "" && d.Lifecycle.Attempts == 0) {
			continue
		}
		_, err := store.UpdateLifecycle(d.ID, func(cur *db.Destination) bool {
			// Decided again on the row as it stands inside the write.
			if cur.Lifecycle.BroadcastID != "" || cur.Lifecycle.GoLiveSentMS != 0 {
				return false
			}
			renewed, gen := cur.Lifecycle.Renewed, cur.Lifecycle.Generation
			cur.Lifecycle = db.BroadcastControl{Renewed: renewed, Generation: gen + 1}
			return true
		})
		if err == nil {
			n++
		}
	}
	return n
}

// lifecycleBroadcastID is the id the coordinator works with for d: the recorded broadcast, or, for an X destination
// that is opened but not yet live, the pending handle rebuilt from its recorded source. Nothing outside this file
// ever sees the handle.
func lifecycleBroadcastID(d *db.Destination) string {
	if id := d.ActiveBroadcastID(); id != "" {
		return id
	}
	if src := strings.TrimSpace(d.Lifecycle.PendingSource); src != "" && d.Platform == db.PlatformX {
		return oauth.XPendingHandle(src, d.Lifecycle.GoLiveAttemptMS)
	}
	return ""
}

// setLifecycleBroadcastID records id in b. A pending handle is stored as its source (PendingSource) with
// BroadcastID empty, so the API, the console and the disconnect check see no broadcast until one exists.
func setLifecycleBroadcastID(b *db.BroadcastControl, id string) {
	if src, _, ok := oauth.XPendingParts(id); ok {
		// The attempt fields are NOT read back from the handle: a write carrying a stamp-less handle must not clear
		// them. They are set by the stamp write, cleared by a definite refusal, and cleared here only when the pending
		// state ends (a real id or none).
		b.BroadcastID, b.PendingSource = "", src
		return
	}
	b.BroadcastID, b.PendingSource, b.GoLiveAttemptMS, b.GoLiveSentMS = id, "", 0, 0
}

// bcAttr is the log attribute for a broadcast id. A pending handle is logged as pending_source=<id> with no broadcast
// key at all, so a log line never shows an id that does not exist on X.
func bcAttr(id string) slog.Attr {
	if src, ok := oauth.XPendingSource(id); ok {
		return slog.String("pending_source", src)
	}
	return slog.String("broadcast", id)
}

// publicBroadcastID is id for anything an operator or another system sees: empty for a pending handle.
func publicBroadcastID(id string) string {
	if _, ok := oauth.XPendingSource(id); ok {
		return ""
	}
	return id
}

// describeBroadcast names a broadcast for operator-facing text. A pending handle is not a broadcast on X yet.
func describeBroadcast(id string) string {
	if _, ok := oauth.XPendingSource(id); ok {
		return "the X broadcast (waiting for video)"
	}
	return "broadcast " + id
}

// resetOnUpEdge handles an UP edge (cleared): the live re-check budget is
// spent and a recorded fault or attempt count is cleared. It returns the
// row to carry on with, nil if the write lost it.
func (c *lifecycleCoordinator) resetOnUpEdge(d *db.Destination, broadcastID string, cleared bool) *db.Destination {
	if !cleared {
		return d
	}
	// AN UP EDGE IS A FREE RE-READ, AND IT IS THE ONE THAT MATTERS MOST.
	//
	// The edge itself costs nothing -- the engine derives it either way and
	// Observe has already woken the sweep -- so spending the skip budget here
	// buys a state read at the exact moment it is most likely to have changed,
	// without adding a single call to the steady state.
	//
	// Why THIS moment: an UP edge means bytes stopped and started again, and a
	// stopped ingest is precisely what fires YouTube's enableAutoStop. The
	// broadcast this destination is publishing to may well have been completed
	// while the encoder was away, so the row's `live` is exactly the label the
	// ten-minute cadence exists to catch -- caught here in one sweep instead.
	c.forgetLiveRecheck(d.ID)
	if d.Lifecycle.Fault == "" && d.Lifecycle.Attempts <= 0 {
		return d
	}
	// It is delivering again, so the previous refusals -- overwhelmingly
	// "no bytes are arriving yet" -- have stopped describing the world.
	return c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
		b.Attempts, b.Fault = 0, ""
	})
}

// skipStateRead reports whether this sweep leaves the row alone without
// reading its broadcast's state, applying the untracking or reset each
// skipping case calls for.
func (c *lifecycleCoordinator) skipStateRead(d *db.Destination, broadcastID string, mode sweepMode) bool {
	switch {
	case !d.Enabled && d.Lifecycle.Phase == "":
		// A DISABLED DESTINATION THAT NEVER WENT THROUGH THE COORDINATOR IS NOT
		// ITS BUSINESS. This is the entire upgrade story and the entire
		// "somebody else's broadcast" story at once: a row that has no recorded
		// phase has never been confirmed on air by this process, so there is
		// nothing here that could justify sending `complete` to it.
		return true
	case !d.Enabled && d.Platform == db.PlatformX && strings.EqualFold(d.Lifecycle.Phase, phaseCreated) &&
		c.quietXAt(d.ID) == broadcastID && !c.goLiveUnresolved(d.Lifecycle.GoLiveSentMS):
		// AN X BROADCAST THAT NEVER AIRED, ON A DESTINATION SWITCHED OFF, AND
		// ALREADY READ ONCE SINCE. open() records one for every enabled X
		// destination before any bytes arrive, and only this coordinator's
		// PUBLISH can take it to RUNNING, so with the destination off there is
		// nothing it can become that needs an answer. The one read after the
		// switch-off is kept: a publish whose record was lost leaves "created"
		// on a RUNNING broadcast, and that read is what ends it.
		// Reading it anyway cost a broadcast read, a source list and the user
		// lookup against X, which publishes no rate limits, every fifteen
		// seconds for the life of the daemon. Re-enabling reads it again.
		if d.Lifecycle.GoLiveSentMS != 0 && !c.raiseLeftLive(d, broadcastID) {
			// The window has passed and nothing was found, and the fault could not be written: stay tracked and
			// try again next sweep rather than going quiet.
			return true
		}
		c.untrack(d.ID)
		return true
	case !d.Enabled && d.Platform == db.PlatformX && isTerminalPhase(d.Lifecycle.Phase):
		// Finished, as below, and the finished broadcast is forgotten as well, so
		// switching the destination on again opens a new one. This is the release
		// the CANCELED fault names.
		if lifecycleBroadcastID(d) != "" || d.Lifecycle.Fault != "" || d.Lifecycle.Attempts > 0 {
			c.recordControl(d, "", func(b *db.BroadcastControl) { *b = db.BroadcastControl{} })
		}
		c.untrack(d.ID)
		return true
	case !d.Enabled && isTerminalPhase(d.Lifecycle.Phase):
		// FINISHED BUSINESS, AND WITHOUT THIS IT COSTS QUOTA FOREVER. A
		// disabled row whose broadcast has already completed has nothing left
		// that can be done to it -- complete and revoked are both terminal, and
		// a completed YouTube broadcast cannot return to live.
		//
		// Falling through instead meant tokenFor plus BroadcastState -- two API
		// calls -- every fifteen seconds for the rest of the daemon's life. That
		// is the same arithmetic the settled-live case below spells out, over
		// eleven thousand units a day against a default of ten thousand, except
		// that this one never stops on its own and never had a reason to run at
		// all. One dead row is enough to exhaust the whole install's quota.
		//
		// Untracking also releases the Wanted() gate: c.tracked being non-empty
		// is what keeps the engine building snapshots, so a row nobody will ever
		// act on would otherwise pin the observe loop on forever too.
		c.untrack(d.ID)
		return true
	case d.Enabled && mode == sweepEndsOnly:
		// Shutting down. Never go live on the way out.
		return true
	case d.Enabled && strings.EqualFold(d.Lifecycle.Phase, phaseLive) && d.Lifecycle.Fault == "" &&
		!c.dueForLiveRecheck(d.ID):
		// SETTLED, SO ASKED RARELY -- NOT NEVER. A confirmed-live broadcast on an
		// enabled destination usually has nothing left to be done to it, and
		// BroadcastState is TWO API calls: the broadcast, then its bound stream.
		//
		// THIS BRANCH USED TO RETURN FOR EVER, on the claim that "nothing is lost
		// by not asking: the next thing that can matter is the operator disabling
		// or deleting the row, and both change the row". THAT SENTENCE WAS FALSE.
		// A broadcast can END WITH THE ROW UNTOUCHED, and at least three routine
		// things do it:
		//
		//   - YouTube's own enableAutoStop (oauth.BroadcastSettings.EnableAutoStop)
		//     completes the broadcast when the ingest stops. THE END POLICY ABOVE
		//     LEANS ON IT BY NAME as the backstop for a crash -- so the coordinator
		//     was depending on a mechanism whose result it then refused to look at.
		//   - The operator ends the broadcast in YouTube Studio, or an
		//     administrator removes it (phase `revoked`).
		//   - The token is revoked, so nothing here can confirm anything again.
		//
		// A permanent skip makes all three invisible and the skip is permanent:
		// the row says `live` for the rest of the daemon's life, on the card the
		// design tells operators to read and in the phase the END policy consults.
		//
		// RE-READING EVERY SWEEP IS NOT THE FIX EITHER. 5,760 sweeps a day times
		// two calls is 11,520 per destination per day against Google's published
		// default project allocation of 10,000 units a day, SHARED with metadata
		// pushes, chat and stats -- one destination would exhaust the install by
		// mid-afternoon, and an install out of quota cannot END anything, which is
		// strictly worse than a stale label.
		//
		// So it is a cadence: one read every fortieth sweep, 144 reads x 2 calls =
		// 288 a day per destination, under 3% of the allocation, with a wrong
		// label standing for at most ten minutes. See lifecycleLiveRecheckEvery
		// for the full arithmetic, and forgetLiveRecheck for why the case that
		// actually happens is noticed in one sweep rather than forty.
		return true
	case d.Enabled && d.Lifecycle.Attempts >= lifecycleGiveUpAfter &&
		!(d.Platform == db.PlatformX && !d.Lifecycle.Terminal && c.rearmDue(d.ID)):
		// Held. (An X destination whose fault is not terminal is let through once every lifecycleRearmEvery: the
		// read says whether the source has bytes, and if it does one go-live is tried. The first success clears it.) See lifecycleGiveUpAfter -- the fault stands, the stream keeps
		// going out, and nothing here calls the platform again until an UP edge
		// says the world changed.
		//
		// The hold is deliberately NOT applied to the disabled branch below: an
		// operator who gives up on going live and switches the destination off
		// must still have their broadcast ended.
		return true
	case !d.Enabled && d.Lifecycle.Attempts >= lifecycleGiveUpAfter*2:
		// THE DISABLED PATH NEEDS A BOUND TOO, JUST A LATER ONE. Exempting it
		// from the hold above is right -- a pending end must still land -- but
		// "not held" was implemented as "retried at fifteen seconds for ever",
		// and a state read that fails permanently (a revoked token, a deleted
		// broadcast) then retries until the process dies while noteFailure logs
		// "giving up for now", which was false.
		//
		// Twice the enabled budget, because ending matters more than starting:
		// a broadcast left live is worse than one left un-started, so this tries
		// considerably harder before it stops. endOrphan already works this way;
		// this is the same rule for the row that still exists.
		if d.Lifecycle.GoLiveSentMS != 0 && !c.raiseLeftLive(d, broadcastID) {
			return true
		}
		c.untrack(d.ID)
		return true
	}
	return false
}

// readAndAct reads the broadcast's state from the platform and acts on the
// plan derived from it.
func (c *lifecycleCoordinator) readAndAct(ctx context.Context, prov oauth.BroadcastLifecycler,
	d *db.Destination, broadcastID string, cleared bool) {
	acct, err := c.tokenFor(ctx, *d.AccountID)
	if err != nil {
		c.noteFailureUnlessPaused(d, broadcastID, fmt.Sprintf(
			"the connected %s account could not be used to read this broadcast's state: %v",
			d.Platform, err), err)
		return
	}

	cctx, cancel := context.WithTimeout(ctx, lifecycleCallTimeout)
	defer cancel()

	st, err := prov.BroadcastState(cctx, acct.AccessToken, broadcastID)
	if err != nil && oauth.XCredentialRejected(err) && c.retokenFor != nil {
		// X refused a token the clock says is good (revoked, or rotated by another install). One forced refresh and
		// one more read; a refresh that fails replaces the error, so a gone grant reads as "reconnect".
		if fresh, rerr := c.retokenFor(ctx, *d.AccountID, acct.AccessToken); rerr != nil {
			err = rerr
		} else {
			acct = fresh
			st, err = prov.BroadcastState(cctx, acct.AccessToken, broadcastID)
		}
	}
	if err != nil && errors.Is(err, oauth.ErrXSourceGone) {
		// THE SOURCE IS GONE FROM X, SO NOTHING CAN EVER GO LIVE ON IT. Forget it, and its go-live markers, so the next
		// sweep opens the destination again instead of asking about a dead id for ever; keep the fault visible and
		// raised until an open succeeds (open() clears it). Opening needs a source that holds this destination's
		// stream key, which the account reconnect and the stream-key refresh provide. The reset bumps the row's
		// Generation, so a late write from this sweep cannot put the dead source back. A tracked target that may have
		// a live broadcast behind it is KEPT: forgetting it would leave that broadcast with nothing to end it.
		why := err.Error()
		maybeLive := d.Lifecycle.GoLiveSentMS > 0
		_, uerr := c.store.UpdateLifecycle(d.ID, func(cur *db.Destination) bool {
			if cur.Lifecycle.Generation != d.Lifecycle.Generation {
				return false
			}
			renewed, gen := cur.Lifecycle.Renewed, cur.Lifecycle.Generation
			cur.Lifecycle = db.BroadcastControl{Renewed: renewed, Fault: why, Generation: gen + 1}
			return true
		})
		if uerr != nil {
			return
		}
		if !maybeLive {
			c.forgetTarget(d.ID)
		}
		c.log.Warn("broadcast lifecycle: "+why, "destination", d.Name, "platform", d.Platform, bcAttr(broadcastID))
		c.raise(d, "", why)
		return
	}
	if err != nil {
		c.noteFailureUnlessPaused(d, broadcastID, fmt.Sprintf(
			"%s would not say what state %s is in: %v", d.Platform, describeBroadcast(broadcastID), err), err)
		return
	}
	if d.Platform == db.PlatformX && st.BroadcastID != "" && st.BroadcastID != broadcastID {
		// The handle stood for a broadcast that is already on air: a go-live whose answer was lost, or whose id
		// could not be recorded. Adopt the real one, and track it as what it is so a deleted row still ends it.
		c.log.Info("broadcast lifecycle: adopted the live X broadcast behind a pending destination",
			"destination", d.Name, bcAttr(st.BroadcastID), "state", st.Status)
		broadcastID = st.BroadcastID
		c.trackAs(d, broadcastID, xLifecyclePhase(st.Status))
	}

	plan := planLifecycle(d.Enabled, st)
	if !d.Enabled && d.Platform == db.PlatformX && plan.Send == "" {
		c.noteQuietX(d.ID, broadcastID)
	}
	if cleared && plan.Wait == xEndedWhileSending {
		plan.Wait, plan.Renew = "", true
	}
	// The platform has just answered, so the skip budget restarts from what IT
	// said rather than from what the row says. Armed only on `live`, and cleared
	// on everything else, so the map can never hold a budget that some earlier
	// read put there for a broadcast that has since moved.
	c.noteLiveRead(d.ID, strings.EqualFold(strings.TrimSpace(plan.Phase), phaseLive))
	switch {
	case plan.Renew:
		// Second show: the recorded broadcast is finished and the destination is
		// still on air. Forget it and open the next one.
		if creator, ok := prov.(oauth.BroadcastCreator); ok {
			c.renew(ctx, creator, d)
		}
		return
	case plan.Fault != "":
		// Terminal: no number of retries changes it. Recorded at the give-up
		// count so nothing calls again, and escalated so somebody is told.
		c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
			b.Phase, b.Attempts, b.Fault, b.Terminal = plan.Phase, lifecycleGiveUpAfter, plan.Fault, true
		})
		c.raise(d, broadcastID, plan.Fault)
		return
	case plan.Send == "":
		c.settleUnsent(d, broadcastID, plan)
		return
	}
	c.sendPlan(cctx, prov, acct, d, broadcastID, st, plan)
}

// settleUnsent records a plan with nothing to send: the phase the platform
// reported, or (for an unrecognised word) a counted failure.
func (c *lifecycleCoordinator) settleUnsent(d *db.Destination, broadcastID string, plan lifecyclePlan) {
	// Nothing to send: either it is where it should be, or the platform is
	// mid-transition, or no bytes have arrived yet. All three are ordinary.
	if plan.Unknown {
		// Except this one, which is "polyemesis does not recognise the word
		// the platform used". Counted so that it escalates instead of
		// stalling in silence, which is the failure mode this whole file is
		// built to avoid.
		c.noteFailure(d, broadcastID, plan.Wait)
		return
	}
	c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
		b.Phase, b.Attempts, b.Fault = plan.Phase, 0, ""
	})
	if plan.Wait != "" {
		c.log.Debug("broadcast lifecycle: waiting", "destination", d.Name,
			bcAttr(broadcastID), "phase", plan.Phase, "why", plan.Wait)
	}
}

// sendPlan sends the transition the plan calls for: complete, or a step
// towards live unless captions would be frozen out.
func (c *lifecycleCoordinator) sendPlan(cctx context.Context, prov oauth.BroadcastLifecycler,
	acct *db.PlatformAccount, d *db.Destination, broadcastID string,
	st *oauth.BroadcastLifecycleState, plan lifecyclePlan) {
	if plan.Send == oauth.PhaseComplete {
		c.end(cctx, prov, acct, d, broadcastID, plan)
		return
	}
	if why := captionGoLiveProblem(d, st); why != "" {
		// Not taken towards live: the caption settings freeze once the broadcast
		// leaves created/ready, so a show started now could never carry captions.
		// Held at the give-up count like any terminal fault, so it costs no more
		// calls; the stream itself keeps going out. An UP edge re-reads it.
		c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
			b.Phase, b.Attempts, b.Fault, b.Terminal = plan.Phase, lifecycleGiveUpAfter, why, true
		})
		c.raise(d, broadcastID, why)
		return
	}
	c.advance(cctx, prov, acct, d, broadcastID, plan)
}

// captionGoLiveProblem is why a destination that posts captions to YouTube by
// HTTP POST may not be taken to testing or live (#8), or "" when it may. Two
// things are checked: a caption ingestion URL is stored for its current stream
// key, and the broadcast is set for POST captions with the monitor stream and a
// 30 s or 60 s delay (oauth.BroadcastLifecycleState.CaptionPOSTProblem). Every
// other destination answers "".
//
// A live-now show with no broadcast recorded never reaches this check: there is
// no broadcast for polyemesis to read, and docs/CAPTIONS.md says so.
func captionGoLiveProblem(d *db.Destination, st *oauth.BroadcastLifecycleState) string {
	if !sendsCaptionPOST(d) {
		return ""
	}
	// The remedies named are the ones that work on THIS broadcast: its id is
	// recorded, so a later announce does not replace it, and a reschedule carries
	// its caption settings through unchanged.
	const fix = " Set captions to off or embedded to go live without them."
	if !d.CaptionIngestReady {
		return "captions are set to YouTube HTTP POST, and no caption ingestion URL is stored for this " +
			"destination's stream key; press Refresh stream key to create its own YouTube stream (the key " +
			"changes once). A broadcast announced before that is bound to the old stream." + fix
	}
	if why := st.CaptionPOSTProblem(); why != "" {
		return why + ". While the broadcast is still upcoming, set its captions to HTTP POST, with the monitor " +
			"stream on and a 30 or 60 second delay, in YouTube Studio; YouTube does not allow the change once it " +
			"is testing or live." + fix
	}
	return ""
}

// openOrHold is the no-broadcast branch for a platform that opens its own (X). It keeps
// the same budget every other fault here keeps: after lifecycleGiveUpAfter consecutive
// refusals the fault stands and nothing calls the platform again, which before this
// retried a refused create every fifteen seconds for ever.
//
// TWO RELEASES. An UP edge (cleared), as everywhere else in this file. And the operator
// switching the destination off: a row with no broadcast is untracked, so it holds the
// observe gate open for nobody and may never see an UP edge, and a hold with no release
// the operator can reach would be permanent. Disabling is that release, and it writes
// once, only when there is something to clear.
func (c *lifecycleCoordinator) openOrHold(ctx context.Context, d *db.Destination,
	creator oauth.BroadcastCreator, cleared bool) {
	if (cleared || !d.Enabled) && (d.Lifecycle.Attempts > 0 || d.Lifecycle.Fault != "") {
		d = c.recordControl(d, "", func(b *db.BroadcastControl) { b.Attempts, b.Fault = 0, "" })
		if d == nil {
			return
		}
	}
	if !d.Enabled || (d.Lifecycle.Attempts >= lifecycleGiveUpAfter &&
		!(d.Platform == db.PlatformX && !d.Lifecycle.Terminal && c.rearmDue(d.ID))) {
		return
	}
	// An enabled destination with no broadcast is opened on its stream source. That records a
	// pending handle and creates nothing on X, so nothing is public until go-live.
	c.open(ctx, d, creator)
}

// renew forgets a finished broadcast and opens the next one. It clears the whole
// lifecycle block, not only the id: the fault, the attempts and the recorded
// phase all describe the broadcast that just ended, and must not be carried
// into the new show.
func (c *lifecycleCoordinator) renew(ctx context.Context, creator oauth.BroadcastCreator, d *db.Destination) {
	// The next broadcast is the same show going on, and its PUBLISH reads Renewed
	// (xShouldNotTweet), so it survives the reset -- but only once the show has
	// posted: the finished broadcast went past created (it was published), or it
	// was already a renewal. A broadcast opened when the destination was switched
	// on and ended by X before the encoder ever sent is renewed WITHOUT the
	// marker, so the show's first real go-live still posts. Read before the reset.
	renewed := d.Lifecycle.Renewed || (d.Lifecycle.Phase != "" && d.Lifecycle.Phase != phaseCreated)
	cur := c.recordControl(d, "", func(b *db.BroadcastControl) { *b = db.BroadcastControl{Renewed: renewed} })
	if cur == nil {
		return
	}
	// The finished broadcast is no longer where chat goes, and no longer a target to end if the row is deleted.
	c.tellChat(d.Platform, d.AccountID, "")
	c.forgetTarget(d.ID)
	c.log.Info("broadcast lifecycle: the recorded broadcast has finished and the destination is still on air; opening the next one",
		"destination", d.Name, "platform", d.Platform)
	c.open(ctx, cur, creator)
}

// open opens a new broadcast for an enabled destination on its stream source and
// records its id. It is gated on the platform's Livestream access before any
// network call, so a closed gate is quiet and is not a fault. Any other failure
// goes through noteFailure, which escalates after the usual number of attempts.
func (c *lifecycleCoordinator) open(ctx context.Context, d *db.Destination, creator oauth.BroadcastCreator) {
	if !creator.LivestreamAccess() {
		return
	}
	if d.AccountID == nil {
		// No account means no token. Not a fault: the remedy is the operator's.
		return
	}
	if strings.TrimSpace(d.StreamKey) == "" {
		c.noteFailure(d, "", "this destination has no stream source key to open a broadcast on; "+
			"reconnect the account so polyemesis can mint one")
		return
	}
	acct, err := c.tokenFor(ctx, *d.AccountID)
	if err != nil {
		c.noteFailureUnlessPaused(d, "", scrubSourceKey(fmt.Sprintf(
			"the connected %s account could not be used to open a broadcast: %v", d.Platform, err), d.StreamKey), err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, lifecycleCallTimeout)
	defer cancel()
	id, err := creator.OpenBroadcast(cctx, acct.AccessToken, d.StreamKey)
	if err != nil && oauth.XCredentialRejected(err) && c.retokenFor != nil {
		if fresh, rerr := c.retokenFor(ctx, *d.AccountID, acct.AccessToken); rerr != nil {
			err = rerr
		} else {
			id, err = creator.OpenBroadcast(cctx, fresh.AccessToken, d.StreamKey)
		}
	}
	if err != nil {
		c.noteFailureUnlessPaused(d, "", scrubSourceKey(fmt.Sprintf(
			"%s would not open a broadcast for this destination: %v", d.Platform, err), d.StreamKey), err)
		return
	}
	c.recordControl(d, id, func(b *db.BroadcastControl) {
		b.Phase, b.Attempts, b.Fault = phaseCreated, 0, ""
	})
	c.log.Info("broadcast lifecycle: opened this destination, nothing is public until it goes live",
		"destination", d.Name, "platform", d.Platform, bcAttr(id))
}

// scrubSourceKey makes a platform error safe for the fault, the log, the escalation and
// the timeline. A platform's problem detail can echo what it was sent, and a stream key
// has no credential shape redact.Redact could recognise. (X's /2/sources routes no longer
// take the key in a request at all, but the scrub stays: it is the backstop.) So the
// key is removed as a literal first, and the pattern pass runs over what is left.
func scrubSourceKey(detail, key string) string {
	if k := strings.TrimSpace(key); k != "" {
		detail = strings.ReplaceAll(detail, k, "[redacted]")
	}
	return redact.Redact(detail)
}

// advance sends a transition that can only ever START something: testing or
// live. It is the branch every ENABLED destination takes, and it has no path to
// oauth.PhaseComplete at all.
func (c *lifecycleCoordinator) advance(ctx context.Context, prov oauth.BroadcastLifecycler,
	acct *db.PlatformAccount, d *db.Destination, broadcastID string, plan lifecyclePlan) {
	// MARK THE GO-LIVE BEFORE IT IS SENT. Two things are written first. GoLiveSentMS (local time) says a go-live is in
	// flight or unresolved, so a reset leaves the row alone and a delete keeps looking for what it made.
	// GoLiveAttemptMS is the same moment in X's own time, from the offset of X's last answer (the source read earlier
	// in this sweep): if the answer is lost, a later read adopts a live broadcast only if X created it inside a window
	// around that time. With no reading of X's clock it stays zero and nothing is ever adopted, which fails safe.
	sendID := broadcastID
	if plan.Send == oauth.PhaseLive && d.Platform == db.PlatformX {
		if src, _, ok := oauth.XPendingParts(broadcastID); ok && d.Lifecycle.GoLiveSentMS == 0 {
			now := c.clock()
			var attemptX int64
			if cr, ok := prov.(interface {
				XClockOffsetMS(accessToken string) (int64, bool)
			}); ok {
				if off, ok := cr.XClockOffsetMS(acct.AccessToken); ok {
					attemptX = now.UnixMilli() + off
				}
			}
			sendID = oauth.XPendingHandle(src, attemptX)
			if _, err := c.recordControlErr(d, sendID, func(b *db.BroadcastControl) {
				b.GoLiveAttemptMS, b.GoLiveSentMS = attemptX, now.UnixMilli()
			}); err != nil {
				// NOTHING IS SENT THAT COULD NOT BE RECORDED. A reset under this sweep (the copy of the row is out of
				// date), a database error and a deleted row all end here: a broadcast with no durable marker is one a
				// later read could not find and a switch-off could not end. The next sweep starts again from the row.
				return
			}
			// From here a broadcast may exist behind the handle, so the tracked target says so and carries the
			// stamped handle: a deleted row is then still read, and ended if X made one.
			c.mu.Lock()
			if t, ok := c.tracked[d.ID]; ok {
				t.maybeLive, t.sentMS, t.BroadcastID = true, now.UnixMilli(), sendID
				c.tracked[d.ID] = t
			}
			c.mu.Unlock()
		}
	}
	res, err := c.transition(ctx, prov, acct, d, sendID, plan.Send)
	if err != nil {
		// A DEFINITE REFUSAL created nothing, so the markers are cleared with it. Any other failure (a timeout, a 5xx)
		// might have created a broadcast, so they are kept.
		var refused *oauth.TransitionRefused
		if errors.As(err, &refused) {
			c.recordControl(d, broadcastID, func(b *db.BroadcastControl) { b.GoLiveAttemptMS, b.GoLiveSentMS = 0, 0 })
		}
		c.handleRefusal(d, sendID, plan.Send, err)
		return
	}
	// A go-live can answer with a different id than the one it was asked about: X creates the broadcast at go-live,
	// so what the row held until now was a pending handle. Everything from here names the broadcast that exists.
	if id := strings.TrimSpace(res.BroadcastID); id != "" {
		broadcastID = id
	}
	if plan.Send == oauth.PhaseLive {
		c.tellChat(d.Platform, d.AccountID, broadcastID)
	}
	// TRACKED BEFORE THE RECORD IS WRITTEN. The broadcast is on air from here, and the tracked target is what
	// endOrphan ends if the row is deleted before the next sweep. Tracking it only after a successful write
	// would leave a live broadcast nobody can end when that write fails.
	c.trackAs(d, broadcastID, recordedPhase(res.Status, plan.Phase))
	if c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
		// The response's own lifeCycleStatus when it carried one; otherwise the
		// phase we read a moment ago. NEVER the phase that was requested -- see
		// db.BroadcastControl.Phase, and oauth.TransitionResult, which leaves
		// Status empty on the redundant path precisely so a caller cannot
		// mistake "already there" for "reported as there".
		b.Phase = recordedPhase(res.Status, plan.Phase)
		b.Attempts, b.Fault = 0, ""
	}) == nil {
		c.log.Warn("broadcast lifecycle: the broadcast moved but its state could not be recorded; it stays tracked "+
			"so it is still ended if the destination is switched off or deleted",
			"destination", d.Name, "platform", d.Platform, bcAttr(broadcastID), "to", plan.Send)
	}
	c.log.Info("moved a broadcast on", "destination", d.Name, "platform", d.Platform,
		bcAttr(broadcastID), "to", plan.Send, "redundant", res.Redundant)
	c.tell(broadcastPhase{
		DestinationID: d.ID, SourceID: sourceIDOf(d), Destination: d.Name, Platform: string(d.Platform),
		BroadcastID: publicBroadcastID(broadcastID), Phase: recordedPhase(res.Status, plan.Phase), Redundant: res.Redundant,
	})
}

// end is one of TWO places in this process that send oauth.PhaseComplete, and
// the other one is endOrphan below.
//
// THIS COMMENT SAID "THE ONLY PLACE" AND IT WAS FALSE WHEN IT WAS WRITTEN. The
// distinction that matters is not how many call sites there are, it is what
// gates each one, so here is both:
//
//	end        gated by a COMPARE-AND-SET inside the writing transaction. The
//	           row is re-read at the instant of the write, so an operator who
//	           re-enables a destination while the state read is in flight gets
//	           nothing sent.
//	endOrphan  gated by the row being ABSENT from a successful, unfiltered,
//	           whole-table ListDestinations. There is no row left to
//	           compare-and-set against, which is exactly why it is a separate
//	           function rather than a branch of this one.
//
// endOrphan's gate has a fragility this one does not: it depends on that
// listing being unfiltered. Scope the query later -- to one source, to enabled
// rows, to a page -- and every live broadcast outside the scope looks deleted
// and gets completed. There is a test pinning it; see the subset case beside
// endOrphan.
//
// THE GATE IS THE FIRST STATEMENT AND IT IS A COMPARE-AND-SET. db.UpdateLifecycle
// re-reads the row inside the transaction that writes it, so the callback below
// is looking at the destination as it stands at this instant -- not at the
// snapshot the sweep read before it went and asked the platform two questions.
// Returning false rolls back and sends nothing.
//
// What that buys, precisely: an operator who disables a destination and changes
// their mind while the state read is in flight gets nothing sent. Two daemons on
// one database both find !Enabled -- which is one operator intent, expressed
// once -- and the second one's transition comes back redundantTransition, which
// oauth reports as success.
//
// The residual window is the operator re-enabling in the seconds between this
// commit and the HTTP call landing. That is somebody racing their own explicit
// end, and it is identical to pressing "End broadcast" in Studio and then
// reconnecting an encoder. Documented, not defended against.
func (c *lifecycleCoordinator) end(ctx context.Context, prov oauth.BroadcastLifecycler,
	acct *db.PlatformAccount, d *db.Destination, broadcastID string, plan lifecyclePlan) {
	_, err := c.store.UpdateLifecycle(d.ID, func(cur *db.Destination) bool {
		if cur.Enabled {
			return false
		}
		// Record the phase the platform just confirmed, which is what makes a
		// daemon that dies on the next line recoverable: the next boot finds a
		// disabled row holding a live phase and ends it.
		setLifecycleBroadcastID(&cur.Lifecycle, broadcastID)
		cur.Lifecycle.Phase = plan.Phase
		return true
	})
	switch {
	case errors.Is(err, db.ErrLifecycleSkipped):
		c.log.Info("broadcast lifecycle: the destination was re-enabled while its broadcast's "+
			"state was being read, so nothing was ended",
			"destination", d.Name, bcAttr(broadcastID))
		return
	case errors.Is(err, db.ErrNotFound):
		// Deleted underneath us. The absent-row pass picks it up next sweep with
		// the mirrored tuple.
		return
	case err != nil:
		c.log.Warn("broadcast lifecycle: could not record the end claim; nothing was sent",
			"destination", d.Name, "err", err)
		return
	}

	res, err := prov.TransitionBroadcast(ctx, acct.AccessToken, broadcastID, oauth.PhaseComplete)
	if err != nil {
		c.handleRefusal(d, broadcastID, oauth.PhaseComplete, err)
		return
	}
	c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
		b.Phase = recordedPhase(res.Status, phaseComplete)
		b.Attempts, b.Fault = 0, ""
	})
	c.tellChat(d.Platform, d.AccountID, "")
	c.log.Info("ended a broadcast", "destination", d.Name, "platform", d.Platform,
		bcAttr(broadcastID), "redundant", res.Redundant)
	c.tell(broadcastPhase{
		DestinationID: d.ID, SourceID: sourceIDOf(d), Destination: d.Name, Platform: string(d.Platform),
		BroadcastID: publicBroadcastID(broadcastID), Phase: recordedPhase(res.Status, phaseComplete), Ended: true,
		Redundant: res.Redundant,
	})
}

// endOrphan ends the broadcast of a destination whose ROW HAS BEEN DELETED.
//
// No compare-and-set is possible or needed: the row is gone, which is the
// strongest statement of intent there is, and there is nothing left to race
// with. The state is still read first, so a broadcast that never aired -- or one
// somebody already ended in the platform's console -- costs nothing.
func (c *lifecycleCoordinator) endOrphan(ctx context.Context, t lifecycleTarget) {
	prov, ok := oauth.As[oauth.BroadcastLifecycler](c.providers, t.Platform)
	if !ok {
		c.untrack(t.destinationID)
		return
	}

	// A BROADCAST THIS PROCESS NEVER PUT ON AIR IS NOT ITS BUSINESS TO END.
	//
	// lifecycleTarget.Phase was added for exactly this and nothing ever read it.
	// Its own doc says so: "A target whose broadcast never aired must not be
	// ended when its row disappears -- there is nothing to end, and sending
	// `complete` to a `created` broadcast is a transition YouTube documents no
	// path for."
	//
	// considerRow already refuses on the same ground for a disabled row with no
	// recorded phase, and calls that "the entire upgrade story and the entire
	// somebody-else's-broadcast story at once". endOrphan reaches the same
	// broadcasts by a different route -- the row vanished rather than being
	// disabled -- and applied no such rule, so deleting a destination that had
	// been created but never went live sent `complete` to it anyway.
	//
	// The asymmetry this file states everywhere decides the direction: declining
	// to end a broadcast that should have ended costs a watch page that stays
	// open; ending one that should not costs the show, because on YouTube
	// complete is terminal.
	if !endableFromPhase(t.Phase) && !(t.maybeLive && c.goLiveUnresolved(t.sentMS)) {
		if t.maybeLive {
			c.raiseLeftLiveTarget(t)
		}
		c.untrack(t.destinationID)
		c.log.Info("broadcast lifecycle: a deleted destination's broadcast was never "+
			"confirmed on air by this process, so it was left alone rather than ended",
			"destination", t.Name, "platform", t.Platform,
			bcAttr(t.BroadcastID), "phase", firstNonBlank(t.Phase, "(none)"))
		return
	}

	// CONFIRM THE DELETION BEFORE ACTING ON IT. The caller reached here because
	// the row was absent from ListDestinations, which is an inference about a
	// query rather than a fact about a row. Ask directly.
	//
	// Anything other than a clean "not found" means DO NOTHING: a store error
	// is not evidence of a deletion, and the next sweep will ask again. The
	// asymmetry is deliberate and matches every other decision in this file --
	// declining to end a broadcast that should have ended costs a watch page
	// that stays open, and ending one that should not costs the show, because
	// on YouTube complete is terminal.
	if _, err := c.store.GetDestination(t.destinationID); !errors.Is(err, db.ErrNotFound) {
		if err != nil {
			c.log.Warn("broadcast lifecycle: could not confirm a destination was deleted, "+
				"so its broadcast is left alone",
				"destination", t.Name, bcAttr(t.BroadcastID), "err", err)
			return
		}
		// The row is there after all. The listing was incomplete, not the
		// world -- which is exactly the case this check exists for.
		c.log.Warn("broadcast lifecycle: a destination was missing from the listing but "+
			"still exists, so its broadcast was NOT ended",
			"destination", t.Name, bcAttr(t.BroadcastID))
		return
	}
	if c.orphanAttemptCount(t.destinationID) >= lifecycleGiveUpAfter {
		if t.maybeLive {
			c.raiseLeftLiveTarget(t)
		}
		c.untrack(t.destinationID)
		c.log.Warn("broadcast lifecycle: giving up on ending a deleted destination's broadcast; "+
			"end it in the platform's own console, or let the platform's automatic stop close it "+
			"when the ingest goes quiet",
			"destination", t.Name, "platform", t.Platform, bcAttr(t.BroadcastID))
		return
	}

	acct, err := c.tokenFor(ctx, t.AccountID)
	if err != nil {
		c.noteOrphanFailure(t, err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, lifecycleCallTimeout)
	defer cancel()

	st, err := prov.BroadcastState(cctx, acct.AccessToken, t.BroadcastID)
	if err != nil {
		c.noteOrphanFailure(t, err)
		return
	}
	// X IS STILL PUBLISHING IT. A PRE_PUBLISHED broadcast cannot be ended yet and goes RUNNING by itself, so
	// letting go of it now would leave a show on air with nothing to end it until X times it out. It stays tracked
	// and is read again next sweep, against the same give-up budget as a failed end.
	if st.Status == oauth.XStatePrePublished {
		c.mu.Lock()
		c.orphanAttempts[t.destinationID]++
		c.mu.Unlock()
		return
	}
	// enabled=false: a deleted row cannot want anything to be running.
	plan := planLifecycle(false, st)
	if plan.Send != oauth.PhaseComplete {
		if t.maybeLive && c.goLiveUnresolved(t.sentMS) && st.Status == oauth.XStateNotStarted {
			// A go-live was sent and nothing in its window is on the list YET: the list may lag. Keep reading, against
			// the same give-up budget, until it shows up or the window has clearly passed.
			c.mu.Lock()
			c.orphanAttempts[t.destinationID]++
			c.mu.Unlock()
			return
		}
		// Never aired, already complete, or a phase with no end to send. A maybe-live target that reaches here has used
		// its window with nothing found: say so rather than letting go in silence.
		if t.maybeLive && st.Status == oauth.XStateNotStarted {
			c.raiseLeftLiveTarget(t)
		}
		c.untrack(t.destinationID)
		return
	}
	// A pending handle names no broadcast: what is ended is the real one the read adopted from it.
	if st.BroadcastID != "" && st.BroadcastID != t.BroadcastID {
		t.BroadcastID = st.BroadcastID
	}
	if _, err := prov.TransitionBroadcast(cctx, acct.AccessToken, t.BroadcastID, oauth.PhaseComplete); err != nil {
		c.noteOrphanFailure(t, err)
		return
	}
	c.tellChat(t.Platform, &t.AccountID, "")
	c.log.Info("ended the broadcast of a deleted destination",
		"destination", t.Name, "platform", t.Platform, bcAttr(t.BroadcastID))
	c.untrack(t.destinationID)
	c.tell(broadcastPhase{
		DestinationID: t.destinationID, SourceID: t.SourceID, Destination: t.Name,
		Platform: string(t.Platform), BroadcastID: publicBroadcastID(t.BroadcastID), Phase: phaseComplete, Ended: true,
	})
}

// ------------------------------------------------------------------- the plan

// lifecyclePlan is what one sweep decided about one broadcast. It is produced by
// a pure function so the whole policy is a table test rather than a fixture with
// an HTTP server in it.
type lifecyclePlan struct {
	// Send is the transition to send, or empty for "nothing to do".
	Send oauth.BroadcastPhase
	// Phase is the platform's own word, to be recorded verbatim.
	Phase string
	// Fault is a TERMINAL problem: retrying cannot fix it and a human must act.
	Fault string
	// Wait says why nothing is being sent, when that is worth saying.
	Wait string
	// Unknown means the platform used a phase this build does not recognise.
	// Counted rather than ignored, so it escalates instead of stalling.
	Unknown bool
	// Renew means the recorded broadcast is finished while the destination is
	// still wanted on air, so the coordinator opens a new one for the next show.
	// Only X's planner sets it; a finished YouTube broadcast still faults.
	Renew bool
}

// xLifecyclePhase maps X's six documented states onto the phase words the rest
// of the coordinator already understands, so the recorded phase, the live-recheck
// budget and isTerminalPhase all work unchanged for X. The raw X word is what
// planXLifecycle decides on; this is only what gets recorded.
func xLifecyclePhase(status string) string {
	switch status {
	case oauth.XStateNotStarted:
		return phaseCreated
	case oauth.XStatePrePublished:
		return phasePrePublished
	case oauth.XStateRunning:
		return phaseLive
	case oauth.XStateEnded:
		return phaseComplete
	case oauth.XStateTimedOut, oauth.XStateCanceled:
		return phaseRevoked
	}
	return ""
}

// recordedPhase is the phase a transition's answer is recorded under. X answers
// in its own words, which are mapped here, so the stored phase is always the
// coordinator's and isTerminalPhase sees an ended X broadcast as complete. Every
// other platform's answer is recorded verbatim, as before.
func recordedPhase(status, fallback string) string {
	if oauth.XStateKnown(status) {
		return xLifecyclePhase(status)
	}
	return firstNonBlank(status, fallback)
}

// planXLifecycle decides for an X broadcast. X has no testing phase: a
// NOT_STARTED broadcast goes straight to RUNNING, and publishing it while no
// bytes are arriving would make an empty show public, so it waits for ingest.
// PRE_PUBLISHED is X's own transition and is left alone. Each of the six states
// is handled explicitly; the caller only reaches here for a state
// oauth.XStateKnown accepts, so an unrecognised word never gets this far.
func planXLifecycle(enabled bool, st *oauth.BroadcastLifecycleState) lifecyclePlan {
	plan := lifecyclePlan{Phase: xLifecyclePhase(st.Status)}
	active := st.StreamActive != nil && *st.StreamActive
	if !enabled {
		switch st.Status {
		case oauth.XStateRunning:
			// The operator switched the destination off while it was on air, so
			// the broadcast is ended here, as YouTube's is.
			plan.Send = oauth.PhaseComplete
		case oauth.XStateNotStarted, oauth.XStatePrePublished:
			plan.Wait = "never aired"
		default:
			plan.Wait = "already finished"
		}
		return plan
	}
	switch st.Status {
	case oauth.XStateNotStarted:
		if active {
			plan.Send = oauth.PhaseLive
		} else {
			plan.Wait = "no bytes are arriving at the X stream source yet"
		}
	case oauth.XStatePrePublished:
		plan.Wait = "X is still moving the broadcast to published"
	case oauth.XStateRunning:
		plan.Wait = "already live"
	case oauth.XStateCanceled:
		// X, or somebody in X's tooling, canceled it. Opening another one by
		// itself would let a cancel-and-reopen cycle post a fresh public
		// announcement on every turn, so it is a fault the operator releases.
		plan.Fault = "X canceled this broadcast. polyemesis does not open another one by itself; " +
			"switch the destination off and on again to start a new broadcast"
	default:
		// ENDED and TIMED_OUT. A finished X broadcast cannot return to live, so
		// the next show gets a new broadcast instead of a fault -- but only once
		// the encoder has been seen to stop. An END with bytes still arriving is
		// the operator's emergency stop in the X app, and renewing then would
		// publish a new public broadcast, with a new announcement, within a
		// sweep. Unknown counts as still arriving.
		if st.StreamActive == nil || *st.StreamActive {
			plan.Wait = xEndedWhileSending
			return plan
		}
		plan.Renew = true
	}
	return plan
}

// xEndedWhileSending is the wait an ended X broadcast holds while bytes may still
// be arriving. considerRow releases it on an UP edge: the ingest stopped and came
// back, which renews the broadcast within the same show, and is the only release
// when X's answer cannot say whether the encoder is sending (no source, or one
// that no longer matches).
const xEndedWhileSending = "the broadcast ended while the encoder is still sending; a new one opens " +
	"once the stream stops and starts again"

// planLifecycle decides what to do about one broadcast, given the operator's
// stored intent and the platform's answer.
//
// enabled IS THE OPERATOR'S INTENT AND NOTHING ELSE. It is not "is FFmpeg
// running": a crashed destination is still enabled, which is exactly why a crash
// does not end a broadcast. And "are bytes arriving" is not read from this
// process at all -- it comes from the platform's own StreamActive, which is the
// authoritative version of the fact and avoids the two-sampler problem
// internal/engine's observeLoop spells out. There is no third input.
func planLifecycle(enabled bool, st *oauth.BroadcastLifecycleState) lifecyclePlan {
	if st == nil {
		return lifecyclePlan{Wait: "no state was read", Unknown: true}
	}
	// X's words are upper case and exact-matched, so they can never be mistaken
	// for YouTube's lower-case phases below.
	if oauth.XStateKnown(st.Status) {
		return planXLifecycle(enabled, st)
	}
	phase := strings.ToLower(strings.TrimSpace(st.Status))
	plan := lifecyclePlan{Phase: st.Status}

	if !enabled {
		// THE END BRANCH. Reached only when the operator has switched the
		// destination off or deleted it -- never for anything on air, because a
		// destination that is delivering is by definition enabled.
		switch phase {
		case phaseTesting, phaseLive, phaseTestStarting, phaseLiveStarting:
			plan.Send = oauth.PhaseComplete
		case phaseComplete, phaseRevoked:
			plan.Wait = "already finished"
		case phaseCreated, phaseReady:
			// It never went in front of anybody. There is nothing to end, and
			// YouTube documents no transition from created to complete.
			plan.Wait = "never aired"
		default:
			plan.Wait = unknownPhaseMessage(st.Status)
			plan.Unknown = true
		}
		return plan
	}

	switch phase {
	case phaseLive:
		// Idempotency by asking: it is already where it should be, so no
		// transition is spent finding that out.
		plan.Wait = "already live"
	case phaseTesting:
		plan.Send = oauth.PhaseLive
	case phaseTestStarting, phaseLiveStarting:
		// The platform is mid-transition. Not a fault, and sending anything now
		// would earn an invalidTransition for asking twice.
		plan.Wait = "the platform is still moving it"
	case phaseComplete, phaseRevoked:
		// TERMINAL, AND THE MESSAGE HAS TO SAY WHY, because the obvious remedy
		// -- "try again" -- is the one thing that cannot work.
		plan.Fault = "this broadcast has already been completed, and a completed broadcast " +
			"cannot return to live. The stream is still being delivered; to put it in front " +
			"of an audience, create or announce a new broadcast for this destination."
	case phaseCreated, phaseReady:
		ready, why := st.ReadyForTesting()
		switch {
		case ready:
			// Both documented preconditions hold: the monitor stream is on and
			// the bound stream is active. Testing is the only legal next step
			// when a monitor stream exists.
			plan.Send = oauth.PhaseTesting
		case st.StreamActive == nil || !*st.StreamActive:
			// NO BYTES YET. Do not spend a transition to be told
			// errorStreamInactive: ReadyForTesting is the advisory pre-check
			// that exists for exactly this, and this is the ordinary state of
			// every broadcast in the seconds before an encoder connects.
			plan.Wait = why
		default:
			// Bytes ARE arriving, so the refusal above was about the monitor
			// stream. With it disabled there is no testing phase at all and
			// live is the correct next step.
			//
			// AN UNKNOWN monitorStream TAKES THIS BRANCH TOO, and that is a
			// deliberate choice between two bad ones. Waiting on an unknown
			// precondition risks a broadcast that never goes live with no error
			// anywhere -- the exact silent failure this file exists to prevent.
			// Sending `live` when the monitor stream is in fact enabled earns
			// invalidTransition, which is classified, counted, and surfaces as
			// a fault naming the broadcast. A loud wrong guess beats a silent
			// right one.
			plan.Send = oauth.PhaseLive
		}
	default:
		plan.Wait = unknownPhaseMessage(st.Status)
		plan.Unknown = true
	}
	return plan
}

func unknownPhaseMessage(status string) string {
	return fmt.Sprintf("this build does not recognise the broadcast phase %q, so it is not "+
		"sending any transition; the stream is unaffected", status)
}

// ------------------------------------------------------------------- refusals

// handleRefusal turns a platform refusal into a row update and, when somebody
// has to act, an escalation.
//
// IT SWITCHES ON THE CLASSIFICATION, NEVER ON A WIRE STRING.
// internal/oauth/youtube_lifecycle.go warns at length that its labels and
// YouTube's reasons deliberately differ -- RefusalStreamInactive spells
// "streamInactive" where the wire says "errorStreamInactive" -- so a comparison
// against a raw reason compiles, runs, and silently never matches.
//
// NOTHING IN HERE STOPS ANYTHING. There is no branch below that could: see THE
// RULE at the top of this file for why stopping on a failed transition is
// self-defeating rather than merely wrong.
func (c *lifecycleCoordinator) handleRefusal(d *db.Destination, broadcastID string,
	to oauth.BroadcastPhase, err error) {
	var ref *oauth.TransitionRefused
	if !errors.As(err, &ref) {
		// Unclassified: an expired token, a transport failure, a body that
		// would not decode. Counted and retried; oauth deliberately does not
		// invent a classification for these, and neither does this.
		c.noteFailureUnlessPaused(d, broadcastID, fmt.Sprintf(
			"could not move %s to %q: %v", describeBroadcast(broadcastID), to, err), err)
		return
	}
	if ref.Refusal == oauth.RefusalMissingScopes {
		why := fmt.Sprintf("X refused to take the stream source live because the connected account's token lacks "+
			"a permission (%v). Reconnect the X account in polyemesis so it is granted broadcast.read and "+
			"broadcast.write. polyemesis will not try again until the destination is switched off and on.", ref.Unwrap())
		c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
			b.Attempts, b.Fault, b.Terminal = lifecycleGiveUpAfter, why, true
		})
		c.raise(d, broadcastID, why)
		return
	}
	if ref.Refusal == oauth.RefusalNotEligible {
		// TERMINAL. X will answer the same 403 on every try, so it is recorded at the give-up count, which stops
		// the calls, and raised once. An UP edge, or switching the destination off and on, releases it.
		why := fmt.Sprintf("X will not let this account go live (%v). Check that the account is not protected "+
			"and is allowed to broadcast on X. polyemesis will not try again until the destination is "+
			"switched off and on.", ref.Unwrap())
		c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
			b.Attempts, b.Fault, b.Terminal = lifecycleGiveUpAfter, why, true
		})
		c.raise(d, broadcastID, why)
		return
	}
	if !ref.Fault() {
		// errorStreamInactive. EXPECTED, NOT AN INCIDENT, AND NOT COUNTED: it
		// is the state every broadcast is in before its encoder connects, and
		// counting it would mark a destination unhealthy for the crime of
		// asking a second before the bytes turned up.
		c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
			b.Attempts, b.Fault = 0, ""
		})
		c.log.Debug("broadcast lifecycle: no data at the platform's ingest yet",
			"destination", d.Name, bcAttr(broadcastID))
		return
	}
	c.noteFailure(d, broadcastID, refusalMessage(ref, broadcastID))
}

// refusalMessage is the sentence an operator reads. THE TWO CEILINGS GET
// DIFFERENT WORDS and that is the whole reason this function exists rather than
// a %v: both arrive as HTTP 403, and telling somebody to stop another broadcast
// when they have hit polyemesis's own shared-stream ceiling sends them to fix
// something that is not their fault and will not help.
func refusalMessage(ref *oauth.TransitionRefused, broadcastID string) string {
	switch ref.Refusal {
	case oauth.RefusalChannelFull:
		return "the channel already has the maximum number of concurrent live broadcasts, so " +
			"this one could not start. Stop a broadcast that is already live on that channel " +
			"and it will be retried."
	case oauth.RefusalSharedIngestionFull:
		return "too many of this channel's broadcasts are sharing one ingest. This is a limit " +
			"polyemesis causes for itself -- every destination on this account publishes to the " +
			"same reusable stream -- so stopping one of your own broadcasts elsewhere will not " +
			"help. Send fewer destinations to this account for now."
	case oauth.RefusalRateLimited:
		return "the platform is asking us to slow down; this will be retried."
	case oauth.RefusalTransient:
		return "the platform hit an error changing this broadcast's status; this will be retried."
	case oauth.RefusalInvalidTransition:
		// NEVER RESENT AS-IS: the next sweep re-reads the state and re-plans
		// from whatever it finds, so a broadcast that moved underneath us gets
		// the transition that suits where it actually is.
		return fmt.Sprintf("the platform would not move %s from where it is to %q; "+
			"its state will be re-read and re-planned", describeBroadcast(broadcastID), ref.Requested)
	case oauth.RefusalSourceBusy:
		return fmt.Sprintf("another broadcast is already live on this destination's X stream source (%v). "+
			"End it in X's own tools; going live is retried.", ref.Unwrap())
	default:
		return ref.Error()
	}
}

// ----------------------------------------------------------------- bookkeeping

// noteFailure counts one consecutive failure and, at the give-up threshold,
// escalates once.
//
// LOGGED AND ESCALATED ONCE PER RUN OF FAILURES, not once per sweep, which is
// the noteAnnounceFailure pattern: a fault an operator cannot resolve without
// acting -- an account at its broadcast ceiling, a token that will not refresh
// -- would otherwise be 5,760 identical warnings a day.
func (c *lifecycleCoordinator) noteFailure(d *db.Destination, broadcastID, detail string) {
	updated := c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
		b.Attempts++
		b.Fault = detail
	})
	n := 1
	if updated != nil {
		n = updated.Lifecycle.Attempts
	}
	if n == 1 {
		c.log.Warn("broadcast lifecycle: "+detail, "destination", d.Name,
			"platform", d.Platform, bcAttr(broadcastID))
		c.raise(d, broadcastID, detail)
		return
	}
	if n >= lifecycleGiveUpAfter {
		c.noteRearm(d.ID)
	}
	if n == lifecycleGiveUpAfter {
		c.log.Warn("broadcast lifecycle: giving up for now; the stream is unaffected and is "+
			"still being delivered",
			"destination", d.Name, bcAttr(broadcastID),
			"consecutiveSweeps", n, "fault", detail)
	}
}

// noteReconnectNeeded records a failure only the operator can fix: the platform refused the account's grant or token
// and a refresh cannot help. Twenty sweeps of asking again could not change the answer, so the fault is written once
// with the give-up count already reached, raised once, and left to the periodic re-arm, which notices a reconnect.
func (c *lifecycleCoordinator) noteReconnectNeeded(d *db.Destination, broadcastID, detail string) {
	first := false
	updated := c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
		first = b.Fault != detail
		if b.Attempts < lifecycleGiveUpAfter {
			b.Attempts = lifecycleGiveUpAfter
		}
		b.Fault = detail
	})
	c.noteRearm(d.ID)
	if updated == nil || !first {
		return
	}
	c.log.Warn("broadcast lifecycle: "+detail, "destination", d.Name, "platform", d.Platform, bcAttr(broadcastID))
	c.raise(d, broadcastID, detail)
}

// quietXAt and noteQuietX keep quietX; see its comment.
func (c *lifecycleCoordinator) quietXAt(id int64) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.quietX[id]
}

func (c *lifecycleCoordinator) noteQuietX(id int64, broadcastID string) {
	c.mu.Lock()
	c.quietX[id] = broadcastID
	c.mu.Unlock()
}

// noteFailureUnlessPaused is noteFailure, except for a platform asking for a
// pause. X publishes no Livestream rate limits, so its 429 is the only word on
// them, and the provider already holds every call until X's reset time. Counting
// those answers would spend the give-up budget on X saying "later", and leave the
// destination held long after the limit lifted. The fault is still recorded, so
// the operator sees why nothing is moving; the count is left alone.
func (c *lifecycleCoordinator) noteFailureUnlessPaused(d *db.Destination, broadcastID, detail string, err error) {
	if errors.Is(err, oauth.ErrReconnectRequired) {
		c.noteReconnectNeeded(d, broadcastID, detail)
		return
	}
	if until, ok := oauth.XRateLimited(err); ok {
		c.recordControl(d, broadcastID, func(b *db.BroadcastControl) { b.Fault = detail })
		c.log.Debug("broadcast lifecycle: the platform asked for a pause", "destination", d.Name,
			bcAttr(broadcastID), "until", until)
		return
	}
	c.noteFailure(d, broadcastID, detail)
}

func (c *lifecycleCoordinator) noteOrphanFailure(t lifecycleTarget, err error) {
	c.mu.Lock()
	c.orphanAttempts[t.destinationID]++
	n := c.orphanAttempts[t.destinationID]
	c.mu.Unlock()
	if n == 1 {
		c.log.Warn("broadcast lifecycle: could not end a deleted destination's broadcast; "+
			"retrying",
			"destination", t.Name, bcAttr(t.BroadcastID), "err", err)
	}
}

// raise hands one fault to whatever tells people. It is the only outbound path
// from this file that is not a platform call.
func (c *lifecycleCoordinator) raise(d *db.Destination, broadcastID, detail string) {
	if c.escalate == nil {
		return
	}
	var sourceID int64
	if d.SourceID != nil {
		sourceID = *d.SourceID
	}
	c.escalate(lifecycleFault{
		SourceID:      sourceID,
		DestinationID: d.ID,
		Destination:   d.Name,
		Platform:      string(d.Platform),
		BroadcastID:   publicBroadcastID(broadcastID),
		Detail:        detail,
	})
}

// recordControl persists a change to the lifecycle block and returns the row as
// it now stands, or nil if the write did not happen.
//
// The returned row is what the caller must go on using: the sweep read its copy
// before an HTTP call that took up to twenty seconds, and deciding the next step
// against that stale snapshot is how a destination gets acted on twice.
func (c *lifecycleCoordinator) recordControl(d *db.Destination, broadcastID string,
	mutate func(*db.BroadcastControl)) *db.Destination {
	updated, _ := c.recordControlErr(d, broadcastID, mutate)
	return updated
}

// recordControlErr is recordControl returning why nothing was written. db.ErrLifecycleSkipped means the row was reset
// since this sweep read it (its Generation moved), so the caller's copy is out of date and what it was about to do
// should not be done.
func (c *lifecycleCoordinator) recordControlErr(d *db.Destination, broadcastID string,
	mutate func(*db.BroadcastControl)) (*db.Destination, error) {
	updated, err := c.store.UpdateLifecycle(d.ID, func(cur *db.Destination) bool {
		gen := cur.Lifecycle.Generation
		if gen != d.Lifecycle.Generation {
			return false // reset from outside since this copy was read: a late result must not undo it
		}
		setLifecycleBroadcastID(&cur.Lifecycle, broadcastID)
		mutate(&cur.Lifecycle)
		cur.Lifecycle.Generation = gen // a mutate that rebuilds the block must not lose the counter
		if cur.Lifecycle.Fault == "" {
			cur.Lifecycle.Terminal = false
		}
		return true
	})
	switch {
	case errors.Is(err, db.ErrNotFound):
		// Deleted while this sweep was talking to the platform. The absent-row
		// pass has the tuple.
		return nil, err
	case errors.Is(err, db.ErrLifecycleSkipped):
		c.log.Debug("broadcast lifecycle: not recorded, the destination was reset since this sweep read it",
			"destination", d.Name)
		return nil, err
	case err != nil:
		c.log.Warn("broadcast lifecycle: could not record broadcast state",
			"destination", d.Name, "err", err)
		return nil, err
	}
	return updated, nil
}

// forgetPlatformSwitch clears a lifecycle block left behind on a destination
// that is no longer on a platform whose broadcasts can be commanded.
//
// A PHASE RECORDED AGAINST ONE PLATFORM'S BROADCAST MUST NOT BE ATTRIBUTED TO
// ANOTHER'S. Clearing costs a lingering broadcast on the platform that was left
// -- so the id goes in the log, which is the only place it can go that does not
// lie -- and the alternative costs a wrong id acted on with the wrong token.
func (c *lifecycleCoordinator) forgetPlatformSwitch(d *db.Destination) {
	c.untrack(d.ID)
	if d.Lifecycle.Empty() {
		return
	}
	c.log.Warn("broadcast lifecycle: this destination is no longer on a platform whose "+
		"broadcasts can be commanded, so its recorded broadcast state has been cleared. If the "+
		"broadcast named here is still live, end it in that platform's own console",
		"destination", d.Name, "platform", d.Platform,
		bcAttr(d.Lifecycle.BroadcastID), "phase", d.Lifecycle.Phase)
	c.recordControl(d, "", func(b *db.BroadcastControl) { *b = db.BroadcastControl{} })
}

func (c *lifecycleCoordinator) track(id int64, t lifecycleTarget) {
	t.destinationID = id
	c.mu.Lock()
	c.tracked[id] = t
	c.mu.Unlock()
}

func (c *lifecycleCoordinator) untrack(id int64) {
	c.mu.Lock()
	delete(c.tracked, id)
	delete(c.orphanAttempts, id)
	delete(c.clearFaults, id)
	delete(c.liveSkips, id)
	c.mu.Unlock()
}

// dueForLiveRecheck reports whether a settled-live destination's skip budget has
// run out, and SPENDS ONE SWEEP OF IT when it has not.
//
// It is called from a switch condition, which is only sound because of where
// that condition sits: every branch above it either returns for a disabled row
// or returns for drain, so the budget is spent exactly once per sweep per
// destination that is actually being skipped -- never on a pass that was going
// to make the call anyway, and never during a shutdown.
//
// ABSENT MEANS DUE. A destination this process has not yet confirmed live with
// its own eyes -- a fresh boot, a row promoted into this branch by a transition
// -- is asked immediately rather than trusted for ten minutes on the strength of
// a column written before the daemon restarted.
func (c *lifecycleCoordinator) dueForLiveRecheck(id int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.liveSkips[id] <= 0 {
		delete(c.liveSkips, id)
		return true
	}
	c.liveSkips[id]--
	return false
}

// noteLiveRead records what the platform just said, arming the skip budget only
// when it said `live`.
//
// MINUS ONE, because the read that arms the budget is itself one of the forty.
// Arming with the whole number would make the real period forty-one sweeps and
// the arithmetic in lifecycleLiveRecheckEvery -- which a reader will check
// against a quota dashboard -- quietly wrong.
func (c *lifecycleCoordinator) noteLiveRead(id int64, live bool) {
	c.mu.Lock()
	if live {
		c.liveSkips[id] = lifecycleLiveRecheckEvery - 1
	} else {
		delete(c.liveSkips, id)
	}
	c.mu.Unlock()
}

// forgetLiveRecheck makes the next sweep ask, whatever the budget said.
//
// Called on an UP edge, and that is a re-read this file gets for nothing: the
// engine derives the edge whether or not anybody listens, Observe has already
// queued the wakeup, and the sweep it wakes was going to walk this row anyway.
// The only cost is the two calls, and they are spent at the moment the answer is
// most likely to have changed -- an UP edge means the ingest stopped and came
// back, and a stopped ingest is exactly what fires enableAutoStop.
// forgetLiveRecheck brings a settled-live row's next re-read FORWARD after an
// edge says the world may have moved. It CLAMPS the budget; it does not delete
// it, and the difference is the whole install's quota.
//
// Deleting made the next sweep re-read unconditionally, which is right once and
// ruinous repeatedly, because an UP edge is not a rare event. The engine makes
// UP immediate while DOWN dwells ten seconds, on a two-second observe tick, so
// a destination that flaps has a floor of roughly twelve seconds between edges
// -- about 7,200 forced re-reads a day. At two API calls each that is ~14,400
// units against a default allocation of 10,000 SHARED with metadata push and
// chat: one flapping destination exhausts the install and the coordinator can
// then no longer END anything, which is precisely the failure the recheck
// cadence was added to avoid. Measured at 33x the advertised rate before this.
//
// Clamping keeps the useful half. An edge still means "look sooner" -- within a
// minute rather than up to ten -- and a destination flapping every twelve
// seconds costs the same as one flapping once, because the budget cannot go
// below the floor no matter how often it is nudged.
func (c *lifecycleCoordinator) forgetLiveRecheck(id int64) {
	c.mu.Lock()
	if cur, ok := c.liveSkips[id]; !ok || cur > lifecycleLiveRecheckAfterEdge {
		c.liveSkips[id] = lifecycleLiveRecheckAfterEdge
	}
	c.mu.Unlock()
}

// absentTargets is every tracked destination whose row is no longer there.
func (c *lifecycleCoordinator) absentTargets(present map[int64]bool) []lifecycleTarget {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []lifecycleTarget
	for id, t := range c.tracked {
		if !present[id] {
			out = append(out, t)
		}
	}
	return out
}

func (c *lifecycleCoordinator) orphanAttemptCount(id int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.orphanAttempts[id]
}

func (c *lifecycleCoordinator) takeClearFault(id int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.clearFaults[id] {
		return false
	}
	delete(c.clearFaults, id)
	return true
}

// firstNonBlank is the "the platform said so, otherwise what we read" pick. A
// tiny helper with a name, because the alternative at three call sites is three
// chances to write the arguments the wrong way round and record a phase nobody
// reported.
func firstNonBlank(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
