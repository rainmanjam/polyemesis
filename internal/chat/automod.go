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
	CheckFast(p db.Platform, authorID, text string) automod.Verdict
	CheckModel(ctx context.Context, p db.Platform, text string) (automod.Verdict, error)
	ModelEnabled() bool
	ModelStats() automod.ModelStats
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
}

// automodState is everything the worker needs, replaced atomically on each
// SetModerator so a reconfiguration cannot half-apply.
type automodState struct {
	mod  Moderator
	gen  uint64
	jobs chan automodJob
	stop context.CancelFunc
	done chan struct{}
	wg   sync.WaitGroup
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
			mod:  m,
			gen:  gen,
			jobs: make(chan automodJob, automodQueueDepth),
			stop: cancel,
			done: make(chan struct{}),
		}
		next.wg.Add(1)
		go h.runAutomodWorker(ctx, next)
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
	if s == nil {
		return
	}

	v := s.mod.CheckFast(m.Platform, m.Author.ID, m.Text)
	h.recordAutomod(s, m, v)

	// The model runs off the read loop, on the worker, so a slow endpoint
	// cannot stall chat. Queued through the same bounded channel as an action,
	// so a raid cannot spawn a goroutine per message either.
	if s.mod.ModelEnabled() && m.Text != "" {
		h.enqueueAutomod(s, automodJob{
			gen: s.gen, platform: m.Platform, account: m.Account,
			msgID: m.ID, authorID: m.Author.ID,
			// A zero Action marks this as "ask the model", which the worker
			// tells apart from an action to perform.
			finding: automod.Finding{Checker: automod.CheckerModel},
		})
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
	for _, f := range v.Act {
		h.enqueueAutomod(s, automodJob{
			gen: s.gen, platform: m.Platform, account: m.Account,
			msgID: m.ID, authorID: m.Author.ID, finding: f,
		})
	}
}

func (h *Hub) enqueueAutomod(s *automodState, job automodJob) {
	select {
	case s.jobs <- job:
	default:
		// Named, not silent. An operator whose automod quietly stopped keeping
		// up would otherwise believe it was keeping up.
		h.log.Warn("automod queue full, dropping work",
			"platform", job.platform, "action", job.finding.Action)
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
		return
	}
	for _, f := range v.Act {
		h.enqueueAutomod(s, automodJob{
			gen: job.gen, platform: job.platform, account: job.account,
			msgID: job.msgID, authorID: job.authorID, finding: f,
		})
	}
	for _, f := range v.Findings {
		h.log.Info("automod model finding",
			"platform", job.platform, "action", f.Action,
			"confidence", f.Confidence, "reason", f.Reason)
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

	var err error
	switch job.finding.Action {
	case automod.ActionFlag:
		// Already recorded by the caller. Nothing to send.
		return
	case automod.ActionHideLocal:
		err = h.HideLocally(job.platform, job.account, job.msgID)
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
			automod.TimeoutDuration(job.finding.TimeoutSeconds), job.finding.Reason)
	case automod.ActionBan:
		// Zero duration is a permanent ban, same as the moderator UI sends.
		err = h.Ban(ctx, job.platform, job.account, job.authorID, 0, job.finding.Reason)
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
	h.log.Info("automod acted",
		"platform", job.platform, "action", job.finding.Action,
		"checker", job.finding.Checker, "reason", job.finding.Reason)
}

// ModelStats reports the live engine's spend, or a zero value with none wired.
func (h *Hub) ModelStats() automod.ModelStats {
	h.mu.Lock()
	s := h.automod
	h.mu.Unlock()
	if s == nil {
		return automod.ModelStats{}
	}
	return s.mod.ModelStats()
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
