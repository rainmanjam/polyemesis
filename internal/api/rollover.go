package api

// Planned rollover (#19): replace a live broadcast before its platform's cap lands on it.
//
// THE SHAPE, AND WHAT IT IS ALLOWED TO TOUCH. A 30-second sweep reads each destination that has rollover switched
// on. For a destination that is delivering, it works out the session (the platform's own live start where the
// platform gives one), asks oauth.PlanRollover whether a trigger is due, and, if one is, acts. The planner decides;
// this file runs the decision.
//
// Two platforms act, and they do it differently:
//
//   - YouTube replaces the broadcast. oauth.YouTube.RollOverBroadcast creates, binds and takes live a new
//     broadcast on the same ingest, then ends the old one. Only the destination's broadcast record changes; the
//     ingest is never touched.
//   - Twitch reconnects. The destination is restarted through the engine, which is the one call here that
//     reaches a process. It is the same restart the operator's own button sends, and nothing else.
//
// Every other platform is COUNTDOWN ONLY. Its cap is shown, but no action is taken, because nothing here knows how
// to replace its broadcast safely.
//
// NOTHING IN THIS FILE STOPS A STREAM. A failed rollover leaves the old broadcast running, records the failure and
// emits rollover.failed. The same rule the lifecycle coordinator states, for the same reason: the one condition under
// which a retry can succeed is an ingest that is still delivering.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/redact"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// rolloverSweepInterval is how often the sweep looks at every enabled destination. A margin measured in minutes does
// not need more, and the platform calls it makes are the cost that matters.
const rolloverSweepInterval = 30 * time.Second

// errRolloverFailed answers a manual run the platform refused. The timeline has the same reason.
var errRolloverFailed = errors.New("the platform refused the rollover")

// errRolloverTooSoon refuses a manual run inside the debounce after a rollover. The sweep already waits that long, so a
// manual run is held to the same rule: the broadcast a rollover just created is not rolled over again at once.
var errRolloverTooSoon = errors.New("a rollover ran less than ten minutes ago; wait before rolling over again")

// rolloverMaxDetail bounds what a failure puts into the timeline and the webhook. An error from a platform can carry
// a response body, so it is cut short here and not trusted to be short.
const rolloverMaxDetail = 240

// rolloverStore is the slice of the database the coordinator uses. It is an interface so the tests can drive the
// coordinator against a real store without the rest of the server.
type rolloverStore interface {
	ListEnabledRollovers() ([]db.RolloverSettings, error)
	GetRollover(destinationID int64) (db.RolloverSettings, error)
	SetRolloverSession(destinationID int64, broadcastID string, startedAt, now time.Time) error
	RecordRolloverResult(destinationID int64, trigger, result string, at time.Time) error
	RecordTimelineEvent(ev db.TimelineEvent) error
	GetDestination(id int64) (*db.Destination, error)
	UpdateLifecycle(id int64, apply func(*db.Destination) bool) (*db.Destination, error)
	RolloverTemplateFor(destinationID int64, broadcastID string) (*scheduler.BroadcastTemplate, error)
	RebindOccurrenceEvent(destinationID int64, from, to string) error
	GetSchedule(id int64) (*scheduler.Schedule, error)
	BroadcastThumbnail(scheduleID int64) ([]byte, string, error)
}

// rolloverTemplateEditor is the part of a scheduled editor a rollover uses to give the replacement the template's
// category and thumbnail. It is asserted on the rollover capability rather than required by it, so a fake without it
// still rolls over and the real YouTube provider, which is a scheduled editor, gets them.
type rolloverTemplateEditor interface {
	EditScheduledBroadcast(ctx context.Context, accessToken, broadcastID string, e oauth.ScheduledEdit) ([]string, error)
	SetScheduledThumbnail(ctx context.Context, accessToken, broadcastID string, data []byte, mime string) error
}

// youtubeRollover is the part of the YouTube provider the coordinator uses. It is declared here, rather than taken as
// oauth.BroadcastRollover, so a test can stand in a fake without implementing the whole provider.
type youtubeRollover interface {
	RollOverBroadcast(ctx context.Context, accessToken string, req oauth.RolloverRequest) (*oauth.RolloverOutcome, error)
	RolloverSessionStart(ctx context.Context, accessToken, broadcastID string) (time.Time, error)
}

// rolloverHost is everything the coordinator may reach outside the database. Each field is a narrow function, so
// the coordinator holds no engine, no supervisor and no reconcile: restart is the only one that can touch a process,
// and it is the operator's own restart.
type rolloverHost struct {
	tokenFor func(ctx context.Context, accountID int64) (*db.PlatformAccount, error)
	// youtube returns the YouTube rollover capability; the bool is the answer callers branch on.
	youtube func() (youtubeRollover, bool)
	// restart cycles one destination's process. It is the operator's restart, used for Twitch.
	restart func(destinationID int64) error
	// delivering is a read-only predicate: whether the destination's output is moving right now.
	delivering func(destinationID int64) bool
	// announce carries a rollover event to the webhook and the events stream.
	announce func(ev hooks.Event)
	// chatNotice sends the optional line to the chat of one platform account: the destination's own, named by its
	// AccountRef, and no other chat on the same platform. A nil value means no chat is wired, and the notice is
	// skipped. An error never stops the rollover; it is logged.
	chatNotice func(ctx context.Context, p db.Platform, account, text string) error
	// twitchStart reads a Twitch stream's own start (Helix started_at) for the countdown. A zero time or an
	// error means the start is unknown, and the first sighting is used instead.
	twitchStart func(ctx context.Context, accessToken string) (time.Time, error)
}

// rolloverChatNoticeText is the one line a viewer sees before a rollover. It is fixed on purpose: it names no
// broadcast, account or credential, so there is nothing in it to leak.
const rolloverChatNoticeText = "We are switching to a fresh broadcast for a moment. Stay with us."

// rolloverChatNoticeTimeout bounds the notice, so a slow chat cannot hold the rollover back.
const rolloverChatNoticeTimeout = 5 * time.Second

// rolloverCoordinator runs the planned-rollover sweep.
type rolloverCoordinator struct {
	log   *slog.Logger
	store rolloverStore
	host  rolloverHost
	now   func() time.Time

	// scenes is the last live switch to each OBS scene name, fed by the OBS event stream. A scene switch that OBS
	// reported as a reconnect snapshot never reaches it, so a reconnect is not a cue. The OBS bridge writes it and
	// the sweep and the card read it from other goroutines, so sceneMu guards it.
	sceneMu sync.Mutex
	scenes  map[string]time.Time

	// rollSem serialises every pass over a destination: the sweep and a manual run both go through considerOne, so
	// the two can never roll the same destination over at once. Each pass re-reads the settings under it, so a pass
	// that waited sees the rollover the other one just recorded and does not repeat it.
	//
	// A one-slot channel rather than a mutex, so a pass waits only as long as its own context allows: a manual run
	// that queues behind a slow platform call gives up at its timeout instead of blocking past it.
	rollSem chan struct{}
}

func newRolloverCoordinator(log *slog.Logger, store rolloverStore, host rolloverHost) *rolloverCoordinator {
	if log == nil {
		log = slog.Default()
	}
	if host.youtube == nil {
		// No provider set was handed in: there is no rollover to run, rather
		// than one built from the package-level providers, which carry no quota
		// meter.
		host.youtube = func() (youtubeRollover, bool) { return nil, false }
	}
	return &rolloverCoordinator{log: log, store: store, host: host, now: time.Now, scenes: map[string]time.Time{},
		rollSem: make(chan struct{}, 1)}
}

// noteScene records a live OBS scene switch. Sync switches are a state read after (re)connecting, not a change OBS
// made, and they are dropped here as well as in the planner, so the rule holds even if the planner changes.
func (c *rolloverCoordinator) noteScene(scene string, syncRead bool, at time.Time) {
	if syncRead || scene == "" {
		return
	}
	c.sceneMu.Lock()
	defer c.sceneMu.Unlock()
	c.scenes[scene] = at
}

// sceneSwitchedAt is the last live switch to a scene, or zero when it has not been seen.
func (c *rolloverCoordinator) sceneSwitchedAt(scene string) time.Time {
	c.sceneMu.Lock()
	defer c.sceneMu.Unlock()
	return c.scenes[scene]
}

// rolloverAction is the action behind a rollover on a platform: replace the
// broadcast on YouTube, reconnect the output on Twitch. ok is false for every
// other platform, which is countdown only, and for YouTube in a build without
// the broadcast client. actsOn and the dispatch below both read it, so the
// platforms that may be gated and the platforms that act are one list.
func (c *rolloverCoordinator) rolloverAction(p db.Platform) (act func(ctx context.Context, d *db.Destination, bid string, now time.Time) (result, reason string, err error), ok bool) {
	switch p {
	case db.PlatformTwitch:
		return func(_ context.Context, d *db.Destination, _ string, now time.Time) (string, string, error) {
			return c.reconnectTwitch(d, now)
		}, true
	case db.PlatformYouTube:
		if _, ok := c.host.youtube(); ok {
			return c.rollYouTube, true
		}
	}
	return nil, false
}

// actsOn reports whether a platform has an action behind a rollover. Every other platform is countdown only.
func (c *rolloverCoordinator) actsOn(p db.Platform) bool {
	_, ok := c.rolloverAction(p)
	return ok
}

// sweepOnce looks at every enabled rollover once. A failure on one destination is logged and does not stop the rest.
func (c *rolloverCoordinator) sweepOnce(ctx context.Context) {
	list, err := c.store.ListEnabledRollovers()
	if err != nil {
		c.log.Warn("planned rollover: could not list the enabled destinations", "err", err)
		return
	}
	for _, cfg := range list {
		if ctx.Err() != nil {
			return
		}
		c.considerOne(ctx, cfg, false)
	}
}

// considerOne is one destination's pass. force is set by a manual run, which skips the planner and its debounce but
// nothing else: the destination must still be delivering and must have an action.
func (c *rolloverCoordinator) considerOne(ctx context.Context, cfg db.RolloverSettings, force bool) (acted bool, err error) {
	select {
	case c.rollSem <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-c.rollSem }()
	// The settings the caller read may be older than a rollover that finished while this pass waited for the lock.
	// The planner's debounce and a manual run both read LastRolloverAt, so the fresh row is the only one used.
	fresh, err := c.store.GetRollover(cfg.DestinationID)
	if err != nil {
		return false, err
	}
	cfg = fresh
	now := c.now()
	d, err := c.store.GetDestination(cfg.DestinationID)
	if err != nil || d == nil {
		// A destination that was deleted is not this sweep's business. Its settings row stays, and the planner never
		// sees it, because there is no destination to plan for.
		return false, err
	}
	if !d.Enabled || c.host.delivering == nil || !c.host.delivering(d.ID) {
		return c.endOffAirSession(d, cfg, now)
	}
	bid := d.ActiveBroadcastID()
	if !c.actsOn(d.Platform) {
		return c.recordCountdownSession(d, cfg, bid, now)
	}

	if d.Platform == db.PlatformYouTube && bid == "" {
		// Not announced, so there is no broadcast to replace and nothing to count from.
		return false, nil
	}
	if force && !cfg.LastRolloverAt.IsZero() && now.Before(oauth.RolloverDebounceEnds(cfg.LastRolloverAt)) &&
		!retriesAFailedRollover(cfg, bid) {
		return false, errRolloverTooSoon
	}
	if cfg.SessionBroadcastID != bid || cfg.SessionStartedAt.IsZero() {
		start, err := c.sessionStart(ctx, d, bid, cfg.LastRolloverAt, now)
		if err != nil {
			c.log.Warn("planned rollover: could not read the live start; trying again next sweep", "destination", d.Name, "err", redact.Redact(err.Error()))
			return false, err
		}
		if start.IsZero() && !force {
			// The platform has not recorded the live start yet. Nothing is counted from a guess: the session is not
			// stored, and the next sweep reads the start again. A manual run needs no countdown, so it goes ahead.
			return false, nil
		}
		if err := c.store.SetRolloverSession(d.ID, bid, start, now); err != nil {
			return false, err
		}
		cfg.SessionBroadcastID, cfg.SessionStartedAt = bid, start
	}

	trigger, due := c.rolloverTrigger(cfg, d, now, force)
	if !due {
		return false, nil
	}
	err = c.act(ctx, d, cfg, bid, trigger, now)
	return true, err
}

// endOffAirSession ends the recorded session of a destination that is off air. The
// session ends with the broadcast, so the next time it goes live the countdown
// starts again from the platform's own start, not from the old session.
func (c *rolloverCoordinator) endOffAirSession(d *db.Destination, cfg db.RolloverSettings, now time.Time) (bool, error) {
	// Off air. The session ends with the broadcast, so the next time it goes live the countdown starts again from
	// the platform's own start, not from the old session.
	if !cfg.SessionStartedAt.IsZero() {
		if err := c.store.SetRolloverSession(d.ID, "", time.Time{}, now); err != nil {
			c.log.Warn("planned rollover: could not end the session", "destination", d.Name, "err", err)
		}
	}
	return false, nil
}

// recordCountdownSession records the session for a platform the planner does not
// act on, once per broadcast, so its countdown card has a start to count from.
func (c *rolloverCoordinator) recordCountdownSession(d *db.Destination, cfg db.RolloverSettings, bid string, now time.Time) (bool, error) {
	// Countdown only. The planner is not consulted, because a due trigger would have nothing to do. The session
	// is still recorded, once per broadcast, because the countdown card counts from it: without it a Facebook
	// destination, whose cap is documented, never showed one. Nothing here can read such a platform's own start,
	// so the first sighting on air is used.
	if cfg.SessionBroadcastID != bid || cfg.SessionStartedAt.IsZero() {
		if err := c.store.SetRolloverSession(d.ID, bid, now, now); err != nil {
			c.log.Warn("planned rollover: could not record the session", "destination", d.Name, "err", err)
		}
	}
	return false, nil
}

// rolloverTrigger decides whether a rollover is due and, if so, what triggered it.
// A manual run is always due, and is labelled as one.
func (c *rolloverCoordinator) rolloverTrigger(cfg db.RolloverSettings, d *db.Destination, now time.Time, force bool) (oauth.RolloverTrigger, bool) {
	if force {
		return "manual", true
	}
	plan := oauth.PlanRollover(c.planInput(cfg, d.Platform, now))
	if !plan.Due {
		return "", false
	}
	return plan.Trigger, true
}

// rolloverFailedPrefix starts the result act records for a rollover that failed.
const rolloverFailedPrefix = "failed: "

// retriesAFailedRollover reports whether a manual run inside the debounce is the operator retrying a rollover that
// failed, which the debounce does not hold back. The debounce exists so a click does not roll over the broadcast a
// rollover just created; a failed one created nothing the destination is on. So the last attempt must have failed,
// and the destination must still be on the broadcast that attempt was replacing. A failure that moved the destination
// to a replacement that would not start (adoptStrandedReplacement) changes the broadcast, so that one stays debounced,
// and the lifecycle coordinator takes the replacement live. The sweep keeps the debounce either way.
func retriesAFailedRollover(cfg db.RolloverSettings, bid string) bool {
	return strings.HasPrefix(cfg.LastResult, rolloverFailedPrefix) && cfg.SessionBroadcastID == bid
}

// sessionStart is the moment the countdown counts from. YouTube reports its own, and a zero answer means it has not
// recorded one yet: the caller waits. Twitch has no such read, so the first moment polyemesis sees the destination on
// air is used, and that moment is kept in the database.
//
// A Twitch start older than the last rollover means Twitch kept the session across the reconnect. The reconnect moment
// is used then: counting from the unchanged start would make the margin due again the moment the debounce ends, and
// restart the destination every ten minutes to no effect.
func (c *rolloverCoordinator) sessionStart(ctx context.Context, d *db.Destination, bid string, lastRollover, now time.Time) (time.Time, error) {
	if d.Platform == db.PlatformTwitch && c.host.twitchStart != nil {
		acct, err := c.account(ctx, d)
		if err != nil {
			return now, nil
		}
		start, err := c.host.twitchStart(ctx, acct.AccessToken)
		if err != nil || start.IsZero() {
			return now, nil
		}
		if !lastRollover.IsZero() && start.Before(lastRollover) {
			c.log.Warn("planned rollover: Twitch reports the same stream start after the reconnect, so the reconnect "+
				"did not reset its count; counting from the reconnect instead", "destination", d.Name)
			return lastRollover, nil
		}
		return start, nil
	}
	if d.Platform != db.PlatformYouTube {
		return now, nil
	}
	br, ok := c.host.youtube()
	if !ok {
		return now, nil
	}
	acct, err := c.account(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return br.RolloverSessionStart(ctx, acct.AccessToken, bid)
}

// planInput turns a stored settings row and the live facts into the planner's input.
func (c *rolloverCoordinator) planInput(cfg db.RolloverSettings, p db.Platform, now time.Time) oauth.RolloverInput {
	limit, _ := oauth.RolloverLimitFor(p)
	return oauth.RolloverInput{
		Now:              now,
		Live:             true,
		Limit:            limit,
		SessionStartedAt: cfg.SessionStartedAt,
		MarginMinutes:    cfg.MarginMinutes,
		QuietStart:       cfg.QuietStart,
		QuietEnd:         cfg.QuietEnd,
		QuietZone:        cfg.QuietZone,
		OBSScene:         cfg.OBSScene,
		SceneSwitchedAt:  c.sceneSwitchedAt(cfg.OBSScene),
		LastRolloverAt:   cfg.LastRolloverAt,
	}
}

// act carries out one rollover and records and announces the outcome. A failure is written to the timeline, the
// webhook and the result row, and it is also returned, wrapped in errRolloverFailed, so a manual run can tell the
// operator. The sweep ignores the return and goes on to the next destination.
func (c *rolloverCoordinator) act(ctx context.Context, d *db.Destination, cfg db.RolloverSettings, bid string, trigger oauth.RolloverTrigger, now time.Time) error {
	c.announceEvent(d, d.Platform, hooks.TriggerRolloverStarted, string(trigger))

	var (
		result, reason string
		err            error
	)
	if cfg.ChatNotice && c.host.chatNotice != nil {
		c.sendChatNotice(ctx, d)
	}
	if act, ok := c.rolloverAction(d.Platform); ok {
		result, reason, err = act(ctx, d, bid, now)
	} else {
		err = fmt.Errorf("%s has no rollover action", d.Platform)
	}

	if err != nil {
		// Redacted before it is clipped: a clip can cut a credential in half, and the pattern then misses it.
		msg := clipDetail(redact.Redact(err.Error()))
		if rerr := c.store.RecordRolloverResult(d.ID, string(trigger), rolloverFailedPrefix+msg, now); rerr != nil {
			c.log.Warn("planned rollover: could not record the failure", "destination", d.Name, "err", rerr)
		}
		c.announceEvent(d, d.Platform, hooks.TriggerRolloverFailed, string(trigger)+": "+msg)
		c.log.Warn("planned rollover failed", "destination", d.Name, "platform", d.Platform, "trigger", trigger, "err", msg)
		return fmt.Errorf("%w: %s", errRolloverFailed, msg)
	}
	if rerr := c.store.RecordRolloverResult(d.ID, string(trigger), result, now); rerr != nil {
		c.log.Warn("planned rollover: could not record the result", "destination", d.Name, "err", rerr)
	}
	c.announceEvent(d, d.Platform, hooks.TriggerRolloverCompleted, string(trigger)+": "+reason)
	c.log.Info("planned rollover done", "destination", d.Name, "platform", d.Platform, "trigger", trigger, "result", result)
	return nil
}

// sendChatNotice posts the notice to the destination's own account's chat. The account is resolved first, because a
// platform can have several chats connected and only this destination's viewers are about to see the switch.
func (c *rolloverCoordinator) sendChatNotice(ctx context.Context, d *db.Destination) {
	nctx, cancel := context.WithTimeout(ctx, rolloverChatNoticeTimeout)
	defer cancel()
	acct, err := c.account(nctx, d)
	if err == nil && acct.AccountRef == "" {
		err = errors.New("the account has no reference to address its chat by")
	}
	if err == nil {
		err = c.host.chatNotice(nctx, d.Platform, acct.AccountRef, rolloverChatNoticeText)
	}
	if err != nil {
		c.log.Warn("planned rollover: the chat notice was not sent; the rollover goes ahead",
			"destination", d.Name, "platform", d.Platform, "err", redact.Redact(err.Error()))
	}
}

// rollYouTube replaces the broadcast. The template, when there is one, decides the title, description and privacy;
// without it the live broadcast is copied, which is recorded as the result so the operator can see it.
func (c *rolloverCoordinator) rollYouTube(ctx context.Context, d *db.Destination, bid string, now time.Time) (result, reason string, err error) {
	if bid == "" {
		return "", "", errors.New("no live broadcast id to replace")
	}
	br, ok := c.host.youtube()
	if !ok {
		return "", "", errors.New("YouTube rollover is not available in this build")
	}
	acct, err := c.account(ctx, d)
	if err != nil {
		return "", "", err
	}
	req := oauth.RolloverRequest{OldBroadcastID: bid, Now: now}
	source := "the live broadcast"
	tpl, terr := c.store.RolloverTemplateFor(d.ID, bid)
	switch {
	case terr == nil:
		loc := time.UTC
		if sch, serr := c.store.GetSchedule(tpl.ScheduleID); serr == nil && sch != nil {
			if l, lerr := sch.Location(); lerr == nil {
				loc = l
			}
		}
		title, rerr := scheduler.RenderTitle(tpl.TitlePattern, now, loc)
		if rerr != nil {
			return "", "", fmt.Errorf("the recurring template's title does not render: %w", rerr)
		}
		req.Title, req.Description, req.Privacy = title, tpl.Description, templatePrivacy(*tpl, d)
		source = "the recurring template"
	case !errors.Is(terr, db.ErrNotFound):
		return "", "", terr
	}

	out, err := br.RollOverBroadcast(ctx, acct.AccessToken, req)
	if err != nil {
		return "", "", c.adoptStrandedReplacement(d, err)
	}
	if terr == nil {
		out.Warnings = append(out.Warnings, c.applyTemplateExtras(ctx, br, acct.AccessToken, out.NewBroadcastID, *tpl, req)...)
		// The occurrence follows the show onto its replacement. The template is found by the live broadcast's id,
		// so without this a show longer than two caps lost its template at the second rollover: the second
		// replacement copied the live broadcast and dropped the template's category and thumbnail.
		if err := c.store.RebindOccurrenceEvent(d.ID, bid, out.NewBroadcastID); err != nil {
			c.log.Warn("planned rollover: the recurring occurrence could not follow the replacement; a later "+
				"rollover will copy the live broadcast instead of the template", "destination", d.Name, "err", err)
		}
	}
	// Platform text, so it is redacted before it reaches the fault, the result, the webhook or the timeline, the same
	// as a failure's reason is.
	warnings := redact.Redact(strings.Join(out.Warnings, "; "))
	if _, err := c.store.UpdateLifecycle(d.ID, func(cur *db.Destination) bool {
		cur.Lifecycle.BroadcastID = out.NewBroadcastID
		cur.Lifecycle.Generation++ // a lifecycle sweep holding the row from before must not write the old id back
		cur.Lifecycle.Phase = phaseLive
		cur.Lifecycle.Attempts = 0
		cur.Lifecycle.Fault = ""
		if len(out.Warnings) > 0 {
			cur.Lifecycle.Fault = clipDetail(warnings)
		}
		return true
	}); err != nil {
		// The replacement is live and the destination was not pointed at it. The next sweep reads the old id, finds
		// the old broadcast not live, and the lifecycle coordinator will report it. Said loudly, because it is the
		// one place a rollover can leave the record behind the platform.
		return "", "", fmt.Errorf("the replacement %s is live, but the destination could not be pointed at it: %w", out.NewBroadcastID, err)
	}
	// The session restarts from the platform's own start for the new broadcast, read on the next sweep.
	if err := c.store.SetRolloverSession(d.ID, "", now, now); err != nil {
		c.log.Warn("planned rollover: could not reset the session", "destination", d.Name, "err", err)
	}
	result = "rolled over to " + out.NewBroadcastID + " from " + source
	reason = "replacement " + out.NewBroadcastID + " is live"
	if out.Gap {
		reason += "; the old broadcast ended before the replacement went live, so viewers saw a gap (provisional)"
	}
	if len(out.Warnings) > 0 {
		reason += "; " + warnings
	}
	return result, reason, nil
}

// adoptStrandedReplacement handles the one failure that leaves the record pointing at the wrong broadcast: the old
// one was ended and the replacement exists but would not start. Left alone, every later sweep reads an ended
// broadcast and the show stays dark. The record moves to the replacement in a non-live phase with a fresh budget, so
// the lifecycle coordinator takes it live once the ingest is seen there. The error is still returned: the rollover
// failed, and the operator is told so.
func (c *rolloverCoordinator) adoptStrandedReplacement(d *db.Destination, err error) error {
	var rerr *oauth.RolloverError
	if !errors.As(err, &rerr) || rerr.OldStillLive || strings.TrimSpace(rerr.NewBroadcastID) == "" {
		return err
	}
	newID := strings.TrimSpace(rerr.NewBroadcastID)
	if _, uerr := c.store.UpdateLifecycle(d.ID, func(cur *db.Destination) bool {
		cur.Lifecycle.BroadcastID = newID
		cur.Lifecycle.Generation++
		cur.Lifecycle.Phase = phaseCreated
		cur.Lifecycle.Attempts = 0
		cur.Lifecycle.Fault = clipDetail("the old broadcast ended and the replacement " + newID +
			" did not start; it is retried as this destination's broadcast")
		return true
	}); uerr != nil {
		return fmt.Errorf("%w; the destination could not be pointed at the replacement %s either: %v", err, newID, uerr)
	}
	return fmt.Errorf("%w; the destination now points at the replacement %s, which is retried until it goes live", err, newID)
}

// applyTemplateExtras gives the replacement the template's category and thumbnail, best effort: the replacement is
// already live, so a refusal of either is a warning and never a failure.
func (c *rolloverCoordinator) applyTemplateExtras(ctx context.Context, br youtubeRollover, token, id string,
	tpl scheduler.BroadcastTemplate, req oauth.RolloverRequest) []string {
	ed, ok := br.(rolloverTemplateEditor)
	if !ok || id == "" {
		return nil
	}
	var warnings []string
	if strings.TrimSpace(tpl.Category) != "" {
		w, err := ed.EditScheduledBroadcast(ctx, token, id, oauth.ScheduledEdit{
			Title: req.Title, Description: req.Description, Privacy: req.Privacy, Category: tpl.Category,
		})
		warnings = append(warnings, w...)
		if err != nil {
			warnings = append(warnings, "the template's category was not applied: "+err.Error())
		}
	}
	if data, mime, err := c.store.BroadcastThumbnail(tpl.ScheduleID); err == nil && len(data) > 0 {
		if err := ed.SetScheduledThumbnail(ctx, token, id, data, mime); err != nil {
			warnings = append(warnings, "the template's thumbnail was not applied: "+err.Error())
		}
	}
	return warnings
}

// reconnectTwitch restarts the destination. The countdown restarts with it, which is the unverified part: Twitch
// may continue the same session across a reconnect, in which case the countdown runs ahead of the real cap. The
// completed event says so, so an operator does not read a reset as a fact.
func (c *rolloverCoordinator) reconnectTwitch(d *db.Destination, now time.Time) (result, reason string, err error) {
	if c.host.restart == nil {
		return "", "", errors.New("the destination cannot be restarted from here")
	}
	if err := c.host.restart(d.ID); err != nil {
		return "", "", err
	}
	// Cleared rather than set to now, so the next sweep re-reads Helix's started_at, which is what says whether Twitch
	// reset its count. See sessionStart for an unchanged start.
	if err := c.store.SetRolloverSession(d.ID, "", time.Time{}, now); err != nil {
		c.log.Warn("planned rollover: could not reset the session", "destination", d.Name, "err", err)
	}
	return "reconnected", "reconnected; whether Twitch resets its 48-hour count on a reconnect is unverified (provisional)", nil
}

// account resolves the destination's platform account and its refreshed token.
func (c *rolloverCoordinator) account(ctx context.Context, d *db.Destination) (*db.PlatformAccount, error) {
	if d.AccountID == nil {
		return nil, errors.New("the destination has no connected account")
	}
	if c.host.tokenFor == nil {
		return nil, errors.New("no token source is wired")
	}
	return c.host.tokenFor(ctx, *d.AccountID)
}

// announceEvent sends one rollover event to the webhook and the events stream, through the host, and writes the same
// event to the operator timeline. The timeline row is what the Monitoring page lists; a failure to write it is logged
// and does not stop the rollover.
func (c *rolloverCoordinator) announceEvent(d *db.Destination, p db.Platform, trigger hooks.Trigger, reason string) {
	c.recordTimeline(d, trigger, reason)
	if c.host.announce == nil {
		return
	}
	var sourceID int64
	if d.SourceID != nil {
		sourceID = *d.SourceID
	}
	c.host.announce(hooks.Event{
		Trigger:     trigger,
		Source:      hooks.SourceRef{ID: sourceID},
		Destination: &hooks.DestinationRef{ID: d.ID, Name: d.Name, Platform: string(p)},
		Reason:      clipDetail(reason),
		At:          c.now(),
	})
}

// recordTimeline writes one rollover event to the operator timeline. A failure is a warning in the log and nothing more.
func (c *rolloverCoordinator) recordTimeline(d *db.Destination, trigger hooks.Trigger, reason string) {
	if c.store == nil {
		return
	}
	severity := "info"
	title := "Planned rollover started on " + d.Name
	switch trigger {
	case hooks.TriggerRolloverCompleted:
		title = "Planned rollover completed on " + d.Name
	case hooks.TriggerRolloverFailed:
		severity = "warning"
		title = "Planned rollover failed on " + d.Name
	}
	if err := c.store.RecordTimelineEvent(db.TimelineEvent{
		At: c.now(), Type: string(trigger), Severity: severity, Title: title,
		Detail: clipDetail(reason), Subject: fmt.Sprintf("destination:%d", d.ID),
	}); err != nil {
		c.log.Warn("planned rollover: could not write the timeline row", "destination", d.Name, "err", err)
	}
}

// RunNow carries out a rollover on one destination when an operator asks for it. It is the same act the sweep runs,
// without the planner's timing: a manual rollover is wanted now, but it still needs a live destination and an action.
func (c *rolloverCoordinator) RunNow(ctx context.Context, destinationID int64) error {
	// The platform calls run on a context the browser cannot cancel. A disconnect between creating the new broadcast
	// and binding it would otherwise leave a public broadcast with nothing bound to it. The timeout stops a stuck call
	// from holding rollSem for ever.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), manualRolloverTimeout)
	defer cancel()
	cfg, err := c.store.GetRollover(destinationID)
	if err != nil {
		return err
	}
	d, err := c.store.GetDestination(destinationID)
	if err != nil {
		return err
	}
	if !d.Enabled || c.host.delivering == nil || !c.host.delivering(d.ID) {
		return errRolloverNotLive
	}
	if !c.actsOn(d.Platform) {
		return errRolloverNoAction
	}
	acted, err := c.considerOne(ctx, cfg, true)
	if err != nil {
		return err
	}
	if !acted {
		return errRolloverNotLive
	}
	return nil
}

// manualRolloverTimeout bounds one operator-triggered rollover, end to end, however long the browser stays connected.
const manualRolloverTimeout = 5 * time.Minute

var (
	errRolloverNotLive  = errors.New("the destination is not delivering, so there is no live broadcast to roll over")
	errRolloverNoAction = errors.New("this platform has no rollover action; it is countdown only")
)

// clipDetail bounds a free-text detail, cutting on a rune boundary so the cut cannot split a character.
func clipDetail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= rolloverMaxDetail {
		return s
	}
	cut := rolloverMaxDetail
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// youtubeRolloverFrom resolves the YouTube rollover capability from the
// server's own provider set, the metered one.
func youtubeRolloverFrom(set oauth.Set) func() (youtubeRollover, bool) {
	return func() (youtubeRollover, bool) {
		br, ok := oauth.As[oauth.BroadcastRollover](set, db.PlatformYouTube)
		if !ok {
			return nil, false
		}
		return br, true
	}
}
