package chat

import (
	"context"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Automod wiring: where a decision becomes an action.
//
// The split is deliberate and load-bearing. internal/automod DECIDES and never
// acts; the Hub ACTS and never decides. Every action therefore leaves through
// the same Delete/Hide/Ban path a human moderator uses, which is what keeps the
// audit trail in one place -- and, incidentally, what makes Kick's
// minutes-versus-seconds conversion apply to automod for free, because it lives
// in the adapters rather than in the caller.

// Moderator is the deciding half. internal/automod.Engine implements it.
//
// Both halves are here on purpose. An earlier version declared only CheckFast,
// with the result that the model checker had no production caller at all: an
// operator could enable it, paste an API key, see "configured", and never get a
// single call. The interface now names everything the Hub must drive, so a
// checker that is not wired fails to compile rather than failing silently.
type Moderator interface {
	CheckFast(p db.Platform, s automod.Subject) automod.Verdict
	CheckModel(ctx context.Context, p db.Platform, text string) (automod.Verdict, error)
	ModelEnabled() bool
	ModelStats() automod.ModelStats
}

// nameLearner is the optional half of a Moderator that wants to hear about the
// broadcaster's and moderators' names. A separate interface, found by type
// assertion, so adding it changes no fake of Moderator.
type nameLearner interface {
	LearnProtectedName(name string)
}

// modelGater is the optional half of a Moderator that decides WHICH messages are
// worth a paid model call (the hard-case scope). A separate interface, found by
// type assertion, so a Moderator without it asks about every message as it
// always did and no fake of Moderator changes.
type modelGater interface {
	WantsModel(p db.Platform, s automod.Subject, v automod.Verdict) bool
}

// modelOutcomer is the optional half of the gate that also says WHY it declined:
// whether the message showed a hard-case signal. A declined message with a
// signal was not cleared by anything and is handled as the model being
// unavailable (see checkAutomod). A gate without it is read through WantsModel.
type modelOutcomer interface {
	ModelOutcome(p db.Platform, s automod.Subject, v automod.Verdict) automod.GateOutcome
}

// modelFailureHolder is the optional half that says whether a message the model
// could not answer should wait off the overlay instead of passing.
type modelFailureHolder interface {
	HoldOnModelError() bool
}

// automodQueueDepth bounds the pending action queue.
//
// Bounded, and it DROPS when full rather than blocking. Blocking would put a
// network call on the adapter's read loop, and under a raid -- exactly when
// this matters -- that stalls the whole chat feed for every viewer. Dropping
// degrades moderation; blocking degrades the product. Same fail-open reasoning
// the model connector uses.
const automodQueueDepth = 256

// automodJob is one action the matrix permitted.
type automodJob struct {
	// gen is the moderator generation this job was created under. A job from a
	// superseded generation is discarded rather than performed: the kill switch
	// is what an operator reaches for mid-incident, and one that stops future
	// decisions while a queue of bans keeps draining is not a kill switch.
	gen      uint64
	platform db.Platform
	account  string
	msgID    string
	authorID string
	finding  automod.Finding
	// signal marks a model question about a line that showed a hard-case
	// signal, so a spent hourly ceiling can list it for review.
	signal bool
}

// automodState is everything the worker needs, replaced atomically on each
// SetModerator so a reconfiguration cannot half-apply.
type automodState struct {
	mod  Moderator
	gen  uint64
	jobs chan automodJob
	// screenJobs is the alert-text screening queue (activity.go), with its own
	// worker, apart from jobs.
	screenJobs chan screenJob
	stop       context.CancelFunc
	done       chan struct{}
	wg         sync.WaitGroup
}

// SetModerator attaches the deciding half, replacing any previous one.
//
// Safe with nil, which is the default and also what the global kill switch
// produces. Calling it repeatedly is expected -- settings are re-applied on
// every save -- so it must not leak a worker per call, and it must not let the
// previous generation's queued actions escape.
func (h *Hub) SetModerator(m Moderator) {
	h.mu.Lock()
	prev := h.automod
	h.automodGen++
	gen := h.automodGen
	var next *automodState
	if m != nil {
		ctx, cancel := context.WithCancel(context.Background())
		next = &automodState{
			mod:        m,
			gen:        gen,
			jobs:       make(chan automodJob, automodQueueDepth),
			screenJobs: make(chan screenJob, screenQueueDepth),
			stop:       cancel,
			done:       make(chan struct{}),
		}
		next.wg.Add(1)
		next.wg.Add(1)
		go h.runAutomodWorker(ctx, next)
		go h.runScreenWorker(ctx, next)
	}
	h.automod = next
	h.mu.Unlock()

	// Outside the lock: stopping the old worker waits for an in-flight platform
	// call, and holding the Hub mutex through a network round trip would stall
	// every adapter delivering a message.
	stopAutomod(prev)
}

// stopAutomod cancels a generation and waits for its worker to leave.
func stopAutomod(s *automodState) {
	if s == nil {
		return
	}
	s.stop()
	s.wg.Wait()
}

// closeAutomod is called from Hub.Close so the worker does not outlive the Hub.
func (h *Hub) closeAutomod() {
	h.mu.Lock()
	s := h.automod
	h.automod = nil
	h.mu.Unlock()
	stopAutomod(s)
}

// checkAutomod runs the cheap checkers, queues whatever the matrix allows, and
// hands the message to the model when one is configured.
//
// Called from deliver() AFTER the message has been stored and published, so the
// message is on screen before any of this happens. A verdict that arrives a
// moment later retracts it; a verdict that blocked its display would make chat
// feel broken, and the retraction path already exists for exactly this.
//
// The fast checkers are in-memory and microseconds, so they run on the
// adapter's goroutine. The model is a network call and does not.
func (h *Hub) checkAutomod(m Message) {
	h.mu.Lock()
	s := h.automod
	h.mu.Unlock()
	if s == nil || m.Trusted() {
		// No automod, or a message automod does not judge (the operator's own,
		// the broadcaster's, a moderator's): nothing will act on it, so
		// nothing need be waited for.
		//
		// But this is where the broadcaster and moderators are SEEN: an
		// account named like one of them is an impostor, and these are the only
		// messages that say who they are.
		if s != nil && (m.Author.Broadcaster || m.Author.Moderator) && m.Author.Name != "" {
			if l, ok := s.mod.(nameLearner); ok {
				l.LearnProtectedName(m.Author.Name)
			}
		}
		h.offerDisplay(m, DisplayClear)
		return
	}

	subj := automod.Subject{
		AuthorID:   m.Author.ID,
		AuthorName: m.Author.Name,
		Text:       m.Text,
		EmoteCount: len(m.Emotes),
		ReplyToID:  m.ReplyToID,
	}
	v := s.mod.CheckFast(m.Platform, subj)
	// NOT for a message the fast verdict removes. It used to be every
	// non-empty message while the model was on, including one a rule had just
	// deleted: a paid call -- against the hourly ceiling every other message
	// shares -- whose answer could change nothing. A fast verdict that only
	// FLAGS removes nothing, so that message is still asked about.
	askModel := s.mod.ModelEnabled() && m.Text != "" && !removes(v)
	// Under the hard-case scope only a message the free checkers could not
	// settle, with a sign of a hard case, is worth the call. Asked LAST, after
	// the cheap tests: the gate spends a per-author allowance when it says yes.
	// A message it declines is not Pending, so it reaches the overlay at once.
	var signal, declined bool
	if askModel {
		if o, ok := s.mod.(modelOutcomer); ok {
			out := o.ModelOutcome(m.Platform, subj, v)
			askModel, signal, declined = out.Ask, out.Signal, out.Declined()
		} else if g, ok := s.mod.(modelGater); ok {
			askModel = g.WantsModel(m.Platform, subj, v)
		}
	}
	// A line with a hard-case signal that the gate's limits kept from the
	// model (the per-author cap, a full table, the raid brake) was judged by
	// nothing. Treating it as clear is what a decoy-then-attack or a flood past
	// the brake would engineer, so it follows the model-outage policy exactly:
	// held off the overlay when holdWhenUnavailable is on, as before when off,
	// and review-listed either way.
	holdDeclined := false
	if declined && !removes(v) && !v.Hold {
		if fh, ok := s.mod.(modelFailureHolder); ok && fh.HoldOnModelError() {
			holdDeclined = true
		}
	}

	// The overlay is told BEFORE anything is queued, so neither an action the
	// fast verdict permits nor the model's answer can reach the sink ahead of
	// the message it is about.
	switch {
	case removes(v):
		h.offerDisplay(m, DisplayBlocked)
	case v.Hold:
		// Flagged in the console, off every overlay, and releasable from the
		// review list (overlay.Gate.Release). Nothing goes to a platform.
		h.offerDisplay(m, DisplayHeld)
	case holdDeclined:
		h.offerDisplay(m, DisplayHeld)
	case askModel:
		h.offerDisplay(m, DisplayPending)
	default:
		h.offerDisplay(m, DisplayClear)
	}
	h.recordAutomod(s, m, v)
	if !removes(v) && v.Hold {
		h.markFlagHeld(m.Platform, m.Account, m.ID)
	}
	if declined && !removes(v) && !v.Hold {
		h.listUnchecked(m.Platform, m.Account, m.ID, m.Author.ID, uncheckedGateReason, holdDeclined)
	}

	// The model runs off the read loop, on the worker, so a slow endpoint
	// cannot stall chat. Queued through the same bounded channel as an action,
	// so a raid cannot spawn a goroutine per message either.
	if askModel {
		h.setModelAsked(m.Key(), true)
		if !h.enqueueAutomod(s, automodJob{
			gen: s.gen, platform: m.Platform, account: m.Account,
			msgID: m.ID, authorID: m.Author.ID, signal: signal,
			// A zero Action marks this as "ask the model", which the worker
			// tells apart from an action to perform.
			finding: automod.Finding{Checker: automod.CheckerModel},
		}) {
			h.setModelAsked(m.Key(), false)
		}
	}
}

// recordAutomod logs findings and queues permitted actions.
func (h *Hub) recordAutomod(s *automodState, m Message, v automod.Verdict) {
	if !v.Flagged() {
		return
	}
	// Everything found is logged, whether or not anything was permitted. That
	// record IS the review queue, and it is the reason automod with every cell
	// off is still worth running.
	for _, f := range v.Findings {
		h.log.Info("automod finding",
			"platform", m.Platform, "author", m.Author.Name,
			"checker", f.Checker, "action", f.Action, "reason", f.Reason,
			"acting", len(v.Act) > 0)
	}
	// And held, so the Chat page can show it (UX Council A48): the log alone
	// was a review queue nobody on the console could read.
	h.noteFlag(m, v.Findings)
	for _, f := range v.Act {
		h.enqueueAutomod(s, automodJob{
			gen: s.gen, platform: m.Platform, account: m.Account,
			msgID: m.ID, authorID: m.Author.ID, finding: f,
		})
	}
}

func (h *Hub) enqueueAutomod(s *automodState, job automodJob) bool {
	select {
	case s.jobs <- job:
		return true
	default:
		// Named, not silent. An operator whose automod quietly stopped keeping
		// up would otherwise believe it was keeping up.
		h.log.Warn("automod queue full, dropping work",
			"platform", job.platform, "action", job.finding.Action)
		return false
	}
}

// runAutomodWorker performs queued work, one item at a time.
//
// One worker rather than a pool: these are moderation actions against rate-
// limited platform APIs, and firing them concurrently is how an account gets
// throttled at the moment it most needs to work.
func (h *Hub) runAutomodWorker(ctx context.Context, s *automodState) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			// Everything still queued belongs to a superseded generation and is
			// abandoned deliberately. See automodJob.gen.
			return
		case job := <-s.jobs:
			// Checked again after the receive: select chooses at random between
			// two ready cases, so a cancelled generation with a full queue
			// would otherwise perform an arbitrary number of further actions.
			if ctx.Err() != nil {
				return
			}
			if job.finding.Action == "" {
				h.askModel(ctx, s, job)
				continue
			}
			h.performAutomod(ctx, s, job)
		}
	}
}

// askModel runs the paid checker and queues whatever it permits.
func (h *Hub) askModel(ctx context.Context, s *automodState, job automodJob) {
	// Answered or abandoned, the question is no longer outstanding.
	defer h.setModelAsked(Message{Platform: job.platform, Account: job.account, ID: job.msgID}.Key(), false)
	// The Hub does not keep message text on the job -- it would double the
	// queue's memory for a raid. Re-reading it from history is cheap and, if
	// the message has already aged out, the moment has passed anyway.
	text := h.textFor(job.platform, job.account, job.msgID)
	if text == "" {
		return
	}
	v, err := s.mod.CheckModel(ctx, job.platform, text)
	if err != nil {
		// Fail open, loudly. Silence would be indistinguishable from "the model
		// looked and was fine with it", and the operator needs to know their
		// moderation is degraded.
		h.log.Warn("automod model check failed; the message passes",
			"platform", job.platform, "err", err)
		// err names each checker that failed. The endpoint inside it has
		// been through redactEndpoint, and no response body is ever in it.
		// It passes on the overlay too: the console is showing it, and an
		// overlay that went silent whenever the model endpoint did would be a
		// second, invisible outage.
		//
		// Not HELD for a spent hourly ceiling, though: the model was not down,
		// the spend limit did its job, and every line after call N holding off
		// the overlay would turn a cost control into an outage. It passes like
		// any decline. But a line that showed a hard-case signal was not
		// checked, and that is not left silent: it is listed for review
		// (never held), and the engine's stats say moderation is degraded.
		if automod.IsBudgetSpent(err) {
			if job.signal {
				h.listUnchecked(job.platform, job.account, job.msgID, job.authorID, uncheckedBudgetReason, false)
			}
			h.judgeDisplay(job.platform, job.account, job.msgID, DisplayClear)
			return
		}
		if fh, ok := s.mod.(modelFailureHolder); ok && fh.HoldOnModelError() {
			// The operator asked for it: a message nobody could judge waits off
			// the overlay, and is listed for review so it can be released.
			h.holdUnjudged(job)
			return
		}
		h.judgeDisplay(job.platform, job.account, job.msgID, DisplayClear)
		return
	}
	if v.DecidedBy == automod.SlotSecondary {
		// The primary could not answer and the secondary did. Info, not Warn:
		// moderation is working, which is the point of having a secondary --
		// but an operator reading the log should be able to see it happening.
		h.log.Info("automod primary checker could not answer; the secondary decided",
			"platform", job.platform)
	}
	// Judged before the actions are queued, for the ordering reason in
	// checkAutomod.
	if removes(v) {
		h.judgeDisplay(job.platform, job.account, job.msgID, DisplayBlocked)
	} else if v.Hold {
		h.judgeDisplay(job.platform, job.account, job.msgID, DisplayHeld)
	} else {
		h.judgeDisplay(job.platform, job.account, job.msgID, DisplayClear)
	}
	if v.Flagged() {
		h.noteFlag(h.messageFor(job.platform, job.account, job.msgID, job.authorID), v.Findings)
		if !removes(v) && v.Hold {
			h.markFlagHeld(job.platform, job.account, job.msgID)
		}
	}
	for _, f := range v.Act {
		h.enqueueAutomod(s, automodJob{
			gen: job.gen, platform: job.platform, account: job.account,
			msgID: job.msgID, authorID: job.authorID, finding: f,
		})
	}
	for _, f := range v.Findings {
		// modelNote is the model's own prose and is LABELLED as such, because
		// the model was handed a viewer's message and anything here may be that
		// viewer's words. It is logged and goes no further: it is `json:"-"` on
		// the Finding and PlatformReason cannot reach it. #495.
		h.log.Info("automod model finding",
			"platform", job.platform, "action", f.Action,
			"confidence", f.Confidence, "category", f.Category,
			"decidedBy", f.DecidedBy, "provider", f.Provider,
			"reason", f.Reason, "modelNote", f.Note)
	}
}

// The review-list entries for a line nothing checked. All three count against
// one cap (unjudgedHoldCap), so no mix of outage, gate decline and spent
// ceiling can push real findings off the 500-entry list.
const (
	// unjudgedReason: the model was asked and could not answer.
	unjudgedReason = "the model could not answer, so the line is held"
	// uncheckedGateReason: the gate's per-author cap or raid brake kept a
	// hard-case line from the model.
	uncheckedGateReason = "the model was not asked (author limit or raid brake), so the line was not checked"
	// uncheckedBudgetReason: the hourly ceiling was spent.
	uncheckedBudgetReason = "the hourly ceiling was spent, so the model was not asked and the line was not checked"
)

// unjudgedHoldCap is how many such entries may sit on the review list at once.
// It bounds the LIST, not the hold: past it a held line stays held off the
// overlay and simply has no entry, and the overlay gate's hold TTL drops it.
const unjudgedHoldCap = 50

func isUncheckedReason(r string) bool {
	return r == unjudgedReason || r == uncheckedGateReason || r == uncheckedBudgetReason
}

// holdUnjudged keeps a message the model could not judge off every overlay and
// puts it on the review list (while there is room for the entry). The caller has already
// judged the overlay.
func (h *Hub) holdUnjudged(job automodJob) {
	// The line is Pending on the overlay; hold it, entry or no entry.
	h.judgeDisplay(job.platform, job.account, job.msgID, DisplayHeld)
	h.listUnchecked(job.platform, job.account, job.msgID, job.authorID, unjudgedReason, true)
}

// listUnchecked puts a line nothing checked on the review list, and keeps it
// off the overlay when hold is set. The finding is a flag, with no category
// beyond "other" and no platform action: nothing was found, only not looked at.
//
// An outage can last as long as it likes and every line asked in it lands
// here, and the review list is bounded and shared with real findings. So the
// cap is on ENTRIES: past unjudgedHoldCap the line gets none. A held line with
// no entry stays held and is dropped by the overlay's hold TTL, which is
// bounded memory; releasing it as clear instead would air exactly what an
// operator who turned the hold on asked not to air.
func (h *Hub) listUnchecked(p db.Platform, account, msgID, authorID, reason string, hold bool) {
	// The cap is checked BEFORE anything is looked up, and not rescanned for
	// every line once it is hit: this runs on the adapter's read loop for each
	// declined line, and under a raid brake that is every line.
	if h.uncheckedListFull() {
		h.countUnlisted(p, hold)
		return
	}
	h.noteFlag(h.messageFor(p, account, msgID, authorID), []automod.Finding{{
		Checker:  automod.CheckerModel,
		Action:   automod.ActionFlag,
		Category: automod.CategoryOther,
		Reason:   reason,
		Hold:     hold,
	}})
	if hold {
		h.markFlagHeld(p, account, msgID)
	}
}

// uncheckedFullRecheck is how long a full list is believed to still be full.
const uncheckedFullRecheck = time.Second

// uncheckedListFull reports whether the shared cap on unchecked entries is
// reached. A positive answer is remembered for a second, so a flood does not
// scan the review list per line.
func (h *Hub) uncheckedListFull() bool {
	h.mu.Lock()
	if h.now().Before(h.uncheckedFullUntil) {
		h.mu.Unlock()
		return true
	}
	h.mu.Unlock()
	if h.unjudgedHeld() < unjudgedHoldCap {
		return false
	}
	h.mu.Lock()
	h.uncheckedFullUntil = h.now().Add(uncheckedFullRecheck)
	h.mu.Unlock()
	return true
}

// countUnlisted counts a line that got no review entry, and warns once per
// window (the time an entry can still hold a line) rather than per line.
func (h *Hub) countUnlisted(p db.Platform, held bool) {
	h.mu.Lock()
	h.uncheckedUnlisted++
	now := h.now()
	warn := now.Sub(h.uncheckedWarnedAt) >= unjudgedHeldFor
	if warn {
		h.uncheckedWarnedAt = now
	}
	h.mu.Unlock()
	if warn {
		h.log.Warn("automod: too many unchecked lines on the review list; further ones are not listed (counted in the model stats)",
			"platform", p, "cap", unjudgedHoldCap, "held", held)
	}
}

// textFor returns a stored message's text, or "" if it is no longer held.
func (h *Hub) textFor(p db.Platform, account, msgID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.history {
		if m.ID == msgID && m.Platform == p && m.Account == account {
			return m.Text
		}
	}
	return ""
}

func (h *Hub) performAutomod(ctx context.Context, s *automodState, job automodJob) {
	// A timeout per action, so one hung platform call cannot stall the queue
	// behind it. Derived from the generation's context, so cancelling the
	// generation also abandons an action already in flight.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Every action below is announced as automod's, not an operator's: the
	// same Delete/Hide/Ban path a human takes, told who is taking it.
	ctx = WithActor(ctx, ActorAutomod)
	// And which model checker decided, so the moderation event -- the audit
	// record -- can say. Empty for the rules and history checkers.
	ctx = withDecidedBy(ctx, string(job.finding.DecidedBy))

	// PlatformReason, NEVER finding.Reason.
	//
	// Twitch and Kick both POST the ban reason under the broadcaster's
	// credential onto a moderation record that is PERMANENT and attributed to
	// them. This used to be the model checker's own prose, which meant a viewer
	// whose message steered the model wrote text of their choosing onto it, and
	// the operator's system-prompt instruction could come back out the same
	// field. PlatformReason is a pure function of the closed Category set, so
	// there is no free-text field left for anything to travel in. #495.
	reason := job.finding.PlatformReason()

	var err error
	switch job.finding.Action {
	case automod.ActionFlag:
		// Already recorded by the caller. Nothing to send.
		return
	case automod.ActionHideLocal:
		err = h.hideLocally(ctx, job.platform, job.account, job.msgID)
	case automod.ActionHide:
		err = h.Hide(ctx, job.platform, job.account, job.msgID, true)
	case automod.ActionDelete:
		err = h.Delete(ctx, job.platform, job.account, job.msgID)
	case automod.ActionTimeout:
		// Seconds throughout, converted to the platform's unit inside the
		// adapter. Kick counts minutes; doing that arithmetic here would put a
		// second copy of it outside the one place that is tested.
		//
		// THROUGH TimeoutDuration, NOT A RAW MULTIPLY, and the case below says
		// why: zero is a PERMANENT ban. This used to pass TimeoutSeconds
		// straight through, so a rule that asked for a timeout and carried no
		// duration banned a viewer for ever and logged it as a success.
		// Compile now refuses to create such a rule; this is what protects the
		// ones already stored, which no validation change can reach.
		err = h.Ban(ctx, job.platform, job.account, job.authorID,
			automod.TimeoutDuration(job.finding.TimeoutSeconds), reason)
	case automod.ActionBan:
		// Zero duration is a permanent ban, same as the moderator UI sends.
		err = h.Ban(ctx, job.platform, job.account, job.authorID, 0, reason)
	default:
		h.log.Warn("automod asked for an action this build does not know",
			"action", job.finding.Action)
		return
	}

	if err != nil {
		// An action that failed is worth more noise than one that worked: the
		// operator believes the channel is being moderated.
		h.log.Error("automod action failed",
			"platform", job.platform, "action", job.finding.Action, "err", err)
		return
	}
	// The review list says it was acted on only now, when it was (#945 review).
	h.markFlagActed(job.platform, job.account, job.msgID)
	// Both: what the operator's own record says, and what the platform was
	// actually told. They differ by design now, and an audit that showed only
	// one of them could not answer either question.
	h.log.Info("automod acted",
		"platform", job.platform, "action", job.finding.Action,
		"checker", job.finding.Checker, "category", job.finding.Category,
		"decidedBy", job.finding.DecidedBy, "provider", job.finding.Provider,
		"reason", job.finding.Reason, "sentReason", reason)
}

// ModelStats reports the live engine's spend, or a zero value with none wired.
func (h *Hub) ModelStats() automod.ModelStats {
	h.mu.Lock()
	s := h.automod
	h.mu.Unlock()
	if s == nil {
		return automod.ModelStats{}
	}
	st := s.mod.ModelStats()
	h.mu.Lock()
	st.UncheckedUnlisted = h.uncheckedUnlisted
	h.mu.Unlock()
	return st
}

// Moderator returns the moderator currently attached, or nil.
//
// Exposed so the API can read the live engine's counters. Returning the
// interface rather than the concrete engine keeps the Hub unaware of what a
// moderator is made of, which is the property SetModerator's signature already
// chose.
func (h *Hub) Moderator() Moderator {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.automod == nil {
		return nil
	}
	return h.automod.mod
}
