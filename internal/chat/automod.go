package chat

import (
	"context"
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
// An interface rather than the concrete type so the Hub can be tested with a
// stub, and so a build that wires no automod at all is a nil field rather than
// a special case threaded through deliver().
type Moderator interface {
	CheckFast(p db.Platform, authorID, text string) automod.Verdict
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
	platform db.Platform
	account  string
	msgID    string
	authorID string
	finding  automod.Finding
}

// SetModerator attaches the deciding half and starts the action worker.
//
// Safe to call with nil, which is the default: no moderator means deliver()
// does nothing extra and costs nothing.
func (h *Hub) SetModerator(m Moderator) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.moderator = m
	if m != nil && h.automodJobs == nil {
		h.automodJobs = make(chan automodJob, automodQueueDepth)
		go h.runAutomodWorker()
	}
}

// checkAutomod runs the cheap checkers and queues whatever the matrix allows.
//
// Called from deliver() AFTER the message has been stored and published, so the
// message is on screen before any of this happens. A verdict that arrives a
// moment later retracts it; a verdict that blocked its display would make chat
// feel broken, and the retraction path already exists for exactly this.
//
// The checkers themselves are in-memory and microseconds, so running them on
// the adapter's goroutine is fine. The ACTIONS are not -- those are network
// calls, and they go to the worker below.
func (h *Hub) checkAutomod(m Message) {
	h.mu.Lock()
	mod := h.moderator
	jobs := h.automodJobs
	h.mu.Unlock()
	if mod == nil {
		return
	}

	v := mod.CheckFast(m.Platform, m.Author.ID, m.Text)
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
		job := automodJob{
			platform: m.Platform, account: m.Account,
			msgID: m.ID, authorID: m.Author.ID, finding: f,
		}
		select {
		case jobs <- job:
		default:
			// Named, not silent. An operator whose automod quietly stopped
			// keeping up would otherwise believe it was keeping up.
			h.log.Warn("automod queue full, dropping an action",
				"platform", m.Platform, "action", f.Action)
		}
	}
}

// runAutomodWorker performs queued actions, one at a time.
//
// One worker rather than a pool: these are moderation actions against rate-
// limited platform APIs, and firing them concurrently is how an account gets
// throttled at the moment it most needs to work.
func (h *Hub) runAutomodWorker() {
	for job := range h.automodJobs {
		h.performAutomod(job)
	}
}

func (h *Hub) performAutomod(job automodJob) {
	// A timeout per action, so one hung platform call cannot stall the queue
	// behind it for everything else.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
		err = h.Ban(ctx, job.platform, job.account, job.authorID,
			time.Duration(job.finding.TimeoutSeconds)*time.Second, job.finding.Reason)
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
