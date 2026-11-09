package api

import (
	"net/http"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Which chats are "live", for chat send and chat polls.
//
// A connected chat is not a live one. Twitch IRC and Kick's webhook are up
// whether or not the channel is streaming, which is exactly why a chat's own
// State ("live" meaning connected) cannot answer this. The signals used instead:
//
//  1. an enabled destination on that platform, linked to that account, whose
//     process is on air (running, or reconnecting, as the upgrade gate reads it).
//     This is the signal for every platform, and the only one for Twitch and Kick;
//  2. the chat adapter's own answer where it has one (chat.LiveChatter): YouTube
//     holds a live chat id only while a broadcast is active, and X is told of a
//     running broadcast by the lifecycle coordinator. This covers a broadcast
//     that is live on the platform without a destination of ours carrying it;
//  3. a destination with no linked account (a pasted stream key) counts for its
//     platform only when exactly one chat account is connected there, since
//     with two there is no telling whose channel it feeds.

// onAirDest is one destination carrying media right now, reduced to what the
// chat side needs.
type onAirDest struct {
	Platform  db.Platform
	AccountID *int64
}

// onAirDestinations lists the enabled destinations whose process is carrying.
//
// A var for the reason surveyOnAir is one: s.mgr is a concrete *engine.Manager
// whose destinations are supervised FFmpeg children, and a test cannot stand
// one up without starting processes.
var onAirDestinations = func(s *Server) []onAirDest {
	if s.mgr == nil || s.store == nil {
		return nil
	}
	rows, err := s.store.ListDestinations()
	if err != nil {
		s.log.Debug("chat live: destinations unreadable", "err", err)
		return nil
	}
	accountOf := make(map[int64]*int64, len(rows))
	for _, r := range rows {
		accountOf[r.ID] = r.AccountID
	}
	var out []onAirDest
	for _, e := range s.mgr.Engines() {
		for _, d := range e.Status().Destinations {
			if !d.OnAir() {
				continue
			}
			out = append(out, onAirDest{Platform: d.Platform, AccountID: accountOf[d.ID]})
		}
	}
	return out
}

// chatLiveSet answers "is this connected chat live" for one moment.
type chatLiveSet struct {
	s        *Server
	accounts map[string]bool // platform\x00ref: linked destination on air
	unlinked map[db.Platform]bool
	perPlat  map[db.Platform]int // connected chats per platform
}

func chatKey(p db.Platform, account string) string { return string(p) + "\x00" + account }

// liveChats takes one reading of what is on air.
func (s *Server) liveChats() chatLiveSet {
	set := chatLiveSet{
		s:        s,
		accounts: map[string]bool{},
		unlinked: map[db.Platform]bool{},
		perPlat:  map[db.Platform]int{},
	}
	if s.chat == nil {
		return set
	}
	for _, st := range s.chat.Statuses() {
		set.perPlat[st.Platform]++
	}
	dests := onAirDestinations(s)
	if len(dests) == 0 {
		return set
	}
	refs := map[int64]string{}
	if s.store != nil {
		if accts, err := s.store.ListPlatformAccounts(); err == nil {
			for _, a := range accts {
				refs[a.ID] = chatKey(a.Platform, a.AccountRef)
			}
		}
	}
	for _, d := range dests {
		if d.AccountID == nil {
			set.unlinked[d.Platform] = true
			continue
		}
		if k, ok := refs[*d.AccountID]; ok {
			set.accounts[k] = true
		}
	}
	return set
}

// Live reports whether one connected chat is live.
func (l chatLiveSet) Live(p db.Platform, account string) bool {
	if l.accounts[chatKey(p, account)] {
		return true
	}
	if l.unlinked[p] && l.perPlat[p] == 1 {
		return true
	}
	return l.s.chat != nil && l.s.chat.LiveChat(p, account)
}

// anyLive reports whether some connected chat that can take a message is live.
// A live Facebook, which cannot be posted to, does not make "nothing is live"
// untrue for a send.
func (l chatLiveSet) anyLive(statuses []chat.Status) bool {
	for _, st := range statuses {
		if st.CanSend && l.Live(st.Platform, st.Account) {
			return true
		}
	}
	return false
}

// chatTarget is one connected chat as the composer's chips need it.
type chatTarget struct {
	Platform db.Platform `json:"platform"`
	Account  string      `json:"account,omitempty"`
	// CanSend is false for a chat polyemesis only reads (Facebook).
	CanSend bool `json:"canSend"`
	// Live is the same answer a default send uses, so the chips and the server
	// cannot disagree: a chip is on by default exactly when the send would go there.
	Live bool `json:"live"`
}

// chatLiveView is GET /chat/live.
type chatLiveView struct {
	Targets []chatTarget `json:"targets"`
	// AnyLive is whether a default send has anywhere to go. When false it is
	// refused with 409 and the composer asks the operator to choose.
	AnyLive bool `json:"anyLive"`
}

// handleChatLive says which connected chats are live right now. It is a read of
// the same function POST /chat/send uses, not a second opinion.
func (s *Server) handleChatLive(w http.ResponseWriter, _ *http.Request) {
	out := chatLiveView{Targets: []chatTarget{}}
	if s.chat != nil {
		statuses := s.chat.Statuses()
		live := s.liveChats()
		for _, st := range statuses {
			out.Targets = append(out.Targets, chatTarget{
				Platform: st.Platform, Account: st.Account,
				CanSend: st.CanSend, Live: live.Live(st.Platform, st.Account),
			})
		}
		out.AnyLive = live.anyLive(statuses)
	}
	writeJSON(w, http.StatusOK, out)
}
