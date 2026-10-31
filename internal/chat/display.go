package chat

import (
	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Display is what automod has decided about a message's place on an ON-SCREEN
// overlay, which is a stricter question than the console's.
//
// The console shows a message first and retracts it if automod objects a
// moment later (see deliver). An overlay is composited into the broadcast, so
// "a moment later" is too late: a slur that was on air for 300 ms was on air.
// The hub therefore tells a DisplaySink, as each message arrives, whether
// automod has already ruled it out, has passed it, or is still asking the
// model -- and the sink holds it until it knows.
type Display int

const (
	// DisplayClear: nothing automod knows of will remove it.
	DisplayClear Display = iota
	// DisplayPending: the model checker has been asked and has not answered.
	// A Judge call follows unless the question is lost (queue full, a
	// moderator generation replaced, the message aged out of history), which
	// is why the sink bounds how long it waits.
	DisplayPending
	// DisplayBlocked: automod is going to remove it. It must never be shown,
	// whether or not the removal then succeeds on the platform.
	DisplayBlocked
)

// DisplaySink receives every message with automod's verdict on it, later
// verdicts for messages that were pending, and every retraction.
//
// Called synchronously from the adapter goroutine, the automod worker and the
// moderation methods, in program order for any one message -- which is why
// this is a direct call and not a bus subscription: the bus drops for a slow
// subscriber, and a dropped retraction is a removed message left on screen.
// Implementations must not block.
type DisplaySink interface {
	Offer(m Message, d Display)
	Judge(p db.Platform, account, messageID string, d Display)
	Retract(p db.Platform, account string, messageIDs []string)
}

// Tap is a second observer of the same verdicts, for a consumer that must know
// WHY a message went and not only which ids: the chat poll's vote counter
// (internal/polls).
//
// The DisplaySink's Retract is one list of ids for three different events,
// which is all an overlay needs and is wrong for a vote. A cleared room is not
// a judgement on anyone, so it must not void a vote. A ban is a judgement on
// the PERSON, and it has to reach the counter even when none of their messages
// is still in the history ring -- retractUser can only list ids it still holds,
// and a vote cast early in a busy chat has long scrolled out by the time a
// moderator acts. One deleted or hidden message voids that message.
//
// Same calling rules as DisplaySink: synchronous, on the adapter goroutine,
// the automod worker or a moderation call, in program order for any one
// message. Implementations must not block and must not call back into the hub.
type Tap interface {
	Offer(m Message, d Display)
	Judge(p db.Platform, account, messageID string, d Display)
	// RetractMessage: one message was deleted or hidden, here or on the
	// platform.
	RetractMessage(p db.Platform, account, messageID string)
	// RetractAuthor: a person was banned or timed out, by an operator, by
	// automod or on the platform's own dashboard.
	RetractAuthor(p db.Platform, account, authorID string)
	// ClearRoom: the platform cleared the whole chat. A housekeeping action,
	// not a verdict on any message in it.
	ClearRoom(p db.Platform, account string)
}

// SetTap attaches the tap. Nil detaches it.
func (h *Hub) SetTap(t Tap) {
	h.mu.Lock()
	h.tap = t
	h.mu.Unlock()
}

func (h *Hub) currentTap() Tap {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tap
}

// SetDisplay attaches the overlay's sink. Nil detaches it.
func (h *Hub) SetDisplay(d DisplaySink) {
	h.mu.Lock()
	h.display = d
	h.mu.Unlock()
}

func (h *Hub) displaySink() DisplaySink {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.display
}

// removes reports whether a permitted automod action takes the message off the
// screen. Flag does not: it is a note for the review queue, and the console
// keeps showing a flagged message too.
func removes(v automod.Verdict) bool {
	for _, f := range v.Act {
		switch f.Action {
		case automod.ActionHideLocal, automod.ActionHide, automod.ActionDelete,
			automod.ActionTimeout, automod.ActionBan:
			return true
		}
	}
	return false
}

func (h *Hub) offerDisplay(m Message, d Display) {
	if s := h.displaySink(); s != nil {
		s.Offer(m, d)
	}
	if t := h.currentTap(); t != nil {
		t.Offer(m, d)
	}
}

func (h *Hub) judgeDisplay(p db.Platform, account, id string, d Display) {
	if s := h.displaySink(); s != nil {
		s.Judge(p, account, id, d)
	}
	if t := h.currentTap(); t != nil {
		t.Judge(p, account, id, d)
	}
}

// tapMessage, tapAuthor and tapRoom tell the tap why something went. Called
// from retract and retractUser beside retractDisplay, whatever the history
// ring still held.
func (h *Hub) tapMessage(p db.Platform, account, id string) {
	if t := h.currentTap(); t != nil {
		t.RetractMessage(p, account, id)
	}
}

func (h *Hub) tapAuthor(p db.Platform, account, authorID string) {
	t := h.currentTap()
	if t == nil {
		return
	}
	if authorID == "" {
		t.ClearRoom(p, account)
		return
	}
	t.RetractAuthor(p, account, authorID)
}

func (h *Hub) retractDisplay(p db.Platform, account string, ids []string) {
	if len(ids) == 0 {
		return
	}
	if s := h.displaySink(); s != nil {
		s.Retract(p, account, ids)
	}
}
