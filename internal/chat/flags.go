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
	// Acted says the automod matrix permitted at least one action on this
	// message, so it was not only logged.
	Acted bool `json:"acted"`
	// At is when the latest finding on this message was recorded.
	At time.Time `json:"at"`
}

// noteFlag records findings against m. A second verdict on the same message --
// the model answering after the fast checkers did -- joins the existing entry
// rather than counting the message twice.
func (h *Hub) noteFlag(m Message, findings []automod.Finding, acted bool) {
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
		f.Acted = f.Acted || acted
		f.At = now
		// Moved to the end: the list is ordered by when automod last spoke.
		h.flags = append(append(h.flags[:i:i], h.flags[i+1:]...), f)
		return
	}
	h.flags = append(h.flags, FlaggedMessage{
		Message:  m,
		Findings: append([]automod.Finding(nil), findings...),
		Acted:    acted,
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
