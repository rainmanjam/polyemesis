package chat

import (
	"context"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Activity: how a chat adapter reports that a viewer did something other than
// speak -- a Super Chat, a new member, a rant, a follow -- and how the overlay
// asks automod about the words and names in it.
//
// NOT THE CHAT PIPELINE. deliver() drops a message with no text, persists,
// keeps history, feeds analytics and runs automod on everything, and a runner
// is keyed by (platform, account). An activity is none of those things, so it
// leaves through its own door: ActivityReceiver, below, into internal/activityhub.

// ActivityReceiver is the optional capability a chat adapter reaches through
// emitActivity. Optional on the SINK rather than a method on Receiver, exactly as
// Retractor is, so SinkFunc and every test double stay valid.
type ActivityReceiver interface {
	// Activity reports one viewer or platform activity. The runner stamps
	// Platform and Account when the adapter left them empty.
	Activity(ev activity.Event)
}

// emitActivity reports an activity when the sink can take one. A sink that is
// not an ActivityReceiver is a legitimate answer -- a test double collecting chat
// does not model activity -- so this is a silent no-op rather than a failure.
//
// THE ONE FUNCTION an adapter calls to say "a viewer did something". A platform
// source never reaches into the hub, the catalogue or the overlay.
func emitActivity(sink Receiver, ev activity.Event) {
	if a, ok := sink.(ActivityReceiver); ok {
		a.Activity(ev)
	}
}

// ActivityIngester is what the Hub hands a runner's activities to. It is an
// interface declared here, satisfied by internal/activityhub.Hub, so chat never
// imports the package that imports it.
type ActivityIngester interface {
	Ingest(ev activity.Event)
}

// SetActivity attaches the activity hub. Nil detaches it, and an activity from
// an adapter is then dropped.
func (h *Hub) SetActivity(a ActivityIngester) {
	h.mu.Lock()
	h.activity = a
	h.mu.Unlock()
}

func (h *Hub) activityIngester() ActivityIngester {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.activity
}

// Activity implements ActivityReceiver for a runner: it stamps the platform and
// account exactly as deliver does, then hands the event on.
func (s runnerSink) Activity(ev activity.Event) {
	if ev.Platform == "" {
		ev.Platform = string(s.r.platform)
	}
	if ev.Account == "" {
		ev.Account = s.r.account
	}
	if a := s.r.hub.activityIngester(); a != nil {
		a.Ingest(ev)
	}
}

// TextChecker is the optional capability of a Moderator that can judge text
// which has no author. internal/automod.Engine has it. Without it ScreenText
// falls back to CheckFast with no author, which runs the rules but not the
// history detectors (they need an author to keep a window for).
type TextChecker interface {
	CheckText(p db.Platform, text string) automod.Verdict
}

func checkText(m Moderator, p db.Platform, text string) automod.Verdict {
	if tc, ok := m.(TextChecker); ok {
		return tc.CheckText(p, text)
	}
	return m.CheckFast(p, automod.Subject{Text: text})
}

// screenQueueDepth bounds the screening queue. SEPARATE from the moderation
// queue (automodQueueDepth) and from its worker: a bounded queue of cheap cheers
// must not be able to starve or drop a ban.
const screenQueueDepth = 64

// screenJob is one model question about alert text, with the platform it is
// asked as.
type screenJob struct {
	platform db.Platform
	req      *screenRequest
}

// runScreenWorker answers screening questions, one at a time, for one
// moderator generation. Its own goroutine, so it never delays a moderation
// action, and cancelled with the generation.
func (h *Hub) runScreenWorker(ctx context.Context, s *automodState) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-s.screenJobs:
			if ctx.Err() != nil {
				return
			}
			h.screenModel(ctx, s, job)
		}
	}
}

// screenRequest is a model question about text that is not chat.
type screenRequest struct {
	text string
	// done is called once with the verdict, from the automod worker. Never
	// called if the question is lost (queue full, moderator replaced), which
	// the asker bounds with its own deadline.
	done func(Display)
}

// ScreenText asks automod whether text a viewer wrote may go on air. It is the
// activity overlay's version of what the chat gate asks per message: the fast
// checkers decide at once on the caller's goroutine, and when a model is
// configured the answer is DisplayPending and done is called later.
//
// What it does NOT do is act. No history, no store, no flag, no hide, delete or
// ban: text that is not a chat message has nothing on the platform to remove,
// and a viewer must not be able to get a Super Chat's author banned by what the
// alert said. A removal verdict means only "not on air".
//
// With no moderator attached, everything is DisplayClear -- the same answer an
// install with automod off gets for chat.
func (h *Hub) ScreenText(p db.Platform, text string, done func(Display)) Display {
	h.mu.Lock()
	s := h.automod
	h.mu.Unlock()
	if s == nil || text == "" {
		return DisplayClear
	}
	// A held verdict keeps alert text off the overlay exactly as a removal
	// does: the alert shows without its words (see overlay.ActivityGate).
	if v := checkText(s.mod, p, text); removes(v) || v.Hold {
		return DisplayBlocked
	}
	if !s.mod.ModelEnabled() || done == nil {
		return DisplayClear
	}
	select {
	case s.screenJobs <- screenJob{platform: p, req: &screenRequest{text: text, done: done}}:
	default:
		// The screening queue is full. NOT the moderation queue: that one carries
		// bans and timeouts and is never touched here. The alert shows without
		// its words.
		h.log.Warn("alert text screening queue full; the alert is shown without its text", "platform", p)
		return DisplayBlocked
	}
	return DisplayPending
}

// ScreenName asks the fast checkers whether a name may go on air. STRICTER than
// ScreenText: any finding blanks the name, not only one the action matrix would
// remove, because a name is not conversation and hate raids use slur
// usernames. The model is never asked -- a name is short, a call per alert is
// a bill, and the blocked-terms list is the operator's own statement of what
// must not appear.
func (h *Hub) ScreenName(p db.Platform, name string) Display {
	h.mu.Lock()
	s := h.automod
	h.mu.Unlock()
	if s == nil || name == "" {
		return DisplayClear
	}
	if s.mod.CheckFast(p, automod.Subject{Text: name}).Flagged() {
		return DisplayBlocked
	}
	return DisplayClear
}

// screenModel answers a ScreenText question on the automod worker.
//
// FAILS CLOSED, unlike the chat gate: an error from the model means the text
// was not judged, and an alert loses nothing by appearing without a message.
// (A chat line that is already on the console fails open, because an overlay
// that went silent whenever the model did would be a second outage. An alert's
// name and amount still show.)
func (h *Hub) screenModel(ctx context.Context, s *automodState, job screenJob) {
	req := job.req
	v, err := s.mod.CheckModel(ctx, job.platform, req.text)
	switch {
	case err != nil:
		h.log.Warn("automod model check failed; the alert's text is not shown",
			"platform", job.platform, "err", err)
		req.done(DisplayBlocked)
	case removes(v) || v.Hold:
		req.done(DisplayBlocked)
	default:
		req.done(DisplayClear)
	}
}
