package chat

import (
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// flagCap bounds the review list. A raid of flagged spam is exactly when this
// fills, and the newest flags are the ones a moderator is looking for, so the
// oldest are shed.
const flagCap = 500

// FlaggedMessage is one message automod found something in, held for the
// operator's review (UX Council A48).
//
// Findings are automod.Finding as-is, which is what keeps Note -- the model's
// own prose, `json:"-"` -- off this surface (#495). Reason is the operator's
// own words or a closed-set category reason, never the model's.
//
// Held in memory, for this process's lifetime, and kept through a retraction:
// a flagged message a moderator then deleted is still a decision somebody may
// want to review. The list is a review queue, not the scrollback.
type FlaggedMessage struct {
	Message  Message           `json:"message"`
	Findings []automod.Finding `json:"findings"`
	// Acted says automod PERFORMED an action on this message and the platform
	// accepted it. Not "an action was permitted": a Flag finding is permitted
	// by the default matrix and sends nothing, and a permitted action can be
	// dropped by a full queue or refused by the platform. Set by
	// markFlagActed once performAutomod succeeds.
	Acted bool `json:"acted"`
	// Held says the message was actually kept off the overlays by a hold, which
	// is what makes "Show on overlay" real. NOT the same as a finding's Hold:
	// a removal outranks a hold (the line is Blocked, and is not offered back),
	// and a switched-off automod or platform sets no hold at all, so the
	// finding can say hold while the line went out. Set only where the hub
	// offers or judges DisplayHeld.
	Held bool `json:"held,omitempty"`
	// At is when the latest finding on this message was recorded.
	At time.Time `json:"at"`
}

// noteFlag records findings against m. A second verdict on the same message --
// the model answering after the fast checkers did -- joins the existing entry
// rather than counting the message twice.
func (h *Hub) noteFlag(m Message, findings []automod.Finding) {
	if len(findings) == 0 {
		return
	}
	key := m.Key()
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	for i := len(h.flags) - 1; i >= 0; i-- {
		if h.flags[i].Message.Key() != key {
			continue
		}
		f := h.flags[i]
		f.Findings = append(append([]automod.Finding(nil), f.Findings...), findings...)
		f.At = now
		// Moved to the end: the list is ordered by when automod last spoke.
		h.flags = append(append(h.flags[:i:i], h.flags[i+1:]...), f)
		return
	}
	h.flags = append(h.flags, FlaggedMessage{
		Message:  m,
		Findings: append([]automod.Finding(nil), findings...),
		At:       now,
	})
	if over := len(h.flags) - flagCap; over > 0 {
		h.flags = append(h.flags[:0:0], h.flags[over:]...)
	}
}

// messageFor returns a held message, or a bare one naming it when it has
// already left the history ring.
func (h *Hub) messageFor(p db.Platform, account, msgID, authorID string) Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.history {
		if m.ID == msgID && m.Platform == p && m.Account == account {
			return m
		}
	}
	return Message{Platform: p, Account: account, ID: msgID, Author: Author{ID: authorID}}
}

// Flagged returns the held flags, newest first, optionally narrowed to one
// platform. A limit of zero or less returns everything held.
func (h *Hub) Flagged(p db.Platform, limit int) []FlaggedMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []FlaggedMessage{}
	for i := len(h.flags) - 1; i >= 0; i-- {
		f := h.flags[i]
		if p != "" && f.Message.Platform != p {
			continue
		}
		f.Findings = append([]automod.Finding(nil), f.Findings...)
		out = append(out, f)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// markFlagActed records that an action automod performed on a message
// succeeded. A message no longer held -- shed by the cap -- is simply missed.
func (h *Hub) markFlagActed(p db.Platform, account, msgID string) {
	key := Message{Platform: p, Account: account, ID: msgID}.Key()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.flags) - 1; i >= 0; i-- {
		if h.flags[i].Message.Key() == key {
			h.flags[i].Acted = true
			return
		}
	}
}

// markFlagHeld records that the message was withheld from the overlays by a
// hold. A message no longer held in the list is simply missed.
func (h *Hub) markFlagHeld(p db.Platform, account, msgID string) {
	key := Message{Platform: p, Account: account, ID: msgID}.Key()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.flags) - 1; i >= 0; i-- {
		if h.flags[i].Message.Key() == key {
			h.flags[i].Held = true
			return
		}
	}
}

// FlagCounts reports how many messages are held per platform, so the Chat
// page's chip can show an exact, filterable count without fetching the list.
func (h *Hub) FlagCounts() map[db.Platform]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[db.Platform]int{}
	for _, f := range h.flags {
		out[f.Message.Platform]++
	}
	return out
}
