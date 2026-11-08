package api

import (
	"context"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/activityhub"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/redact"
)

// WHERE A PLATFORM'S ACTIVITY IS WIRED. This file is the extension point the
// per-platform activity PRs share; the core PR leaves both tables empty.
//
// A platform's activity takes one of two shapes:
//
//  1. IT RIDES ITS CHAT CONNECTION (YouTube, Rumble, Kick, X, Facebook). The
//     adapter calls emitActivity on the chat.Sink it was handed
//     (internal/chat/activity.go), so nothing in this file is involved in the
//     events. It reports its kinds by implementing
//     `ActivityKinds() []activity.KindStatus` on the adapter; chatActivityStatuses
//     below lists them in GET /api/v1/activity/sources with the chat connection's
//     own state as the transport state.
//
//  2. IT NEEDS A TRANSPORT OF ITS OWN (Twitch EventSub). It implements
//     activityhub.Source and registers a builder in activitySourceBuilders. It
//     is started when the account's chat is connected and stopped when it is
//     detached (connectChat, detachChat), so the two always agree about which
//     accounts are live.

// activityKindReporter is the optional capability a chat adapter implements to
// say which of its activity kinds are on.
type activityKindReporter interface {
	ActivityKinds() []activity.KindStatus
}

// activitySourceBuilder builds the own-transport source for one connected
// account, or reports that this account has none to run. An error is logged
// and the account simply has no activity source: it never stops chat.
type activitySourceBuilder func(s *Server, a db.PlatformAccount) (activityhub.Source, error)

// activitySourceBuilders is keyed by platform. EMPTY IN THE CORE PR: the Twitch
// EventSub PR adds `db.PlatformTwitch` here, in this file, and nowhere else.
var activitySourceBuilders = map[db.Platform]activitySourceBuilder{}

// attachActivitySource starts the account's own-transport activity source, when
// its platform has one. Caller holds chatWire. A failure is logged, not
// returned: activity is additive and must never stop an account's chat.
func (s *Server) attachActivitySource(ctx context.Context, a db.PlatformAccount) {
	build, ok := activitySourceBuilders[a.Platform]
	if !ok || s.activity == nil {
		return
	}
	src, err := build(s, a)
	if err != nil {
		s.log.Info("activity: no source for this account", "platform", a.Platform, "account", a.AccountName, "reason", err)
		return
	}
	if src == nil {
		return
	}
	// Replaced rather than refused: attachAccountChat detaches before it
	// connects, but a builder that reuses a key must not wedge the account.
	s.activity.Detach(src.Platform(), src.Account())
	if err := s.activity.Attach(ctx, src); err != nil {
		s.log.Warn("activity: could not start the source", "platform", a.Platform, "err", err)
	}
}

// detachActivitySource stops the account's own-transport source. Nothing is an
// error: most accounts have none.
func (s *Server) detachActivitySource(p db.Platform, account string) {
	if s.activity == nil {
		return
	}
	s.activity.Detach(string(p), account)
}

// chatActivityStatuses lists, as sources, every chat connection whose adapter
// reports activity kinds. Registered once with Hub.AddStatusProvider.
func chatActivityStatuses(h *chat.Hub) func() []activityhub.SourceStatus {
	return func() []activityhub.SourceStatus {
		if h == nil {
			return nil
		}
		var out []activityhub.SourceStatus
		for _, st := range h.Statuses() {
			ad, ok := h.Adapter(st.Platform, st.Account)
			if !ok {
				continue
			}
			kr, ok := ad.(activityKindReporter)
			if !ok {
				continue
			}
			out = append(out, activityhub.SourceStatus{
				Platform: string(st.Platform), Account: st.Account,
				State: st.State, Detail: redact.Redact(st.Detail),
				Transport: activityhub.TransportChat, Kinds: kr.ActivityKinds(),
			})
		}
		return out
	}
}
