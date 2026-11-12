package api

import (
	"context"
	"errors"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/activityhub"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/redact"
	"github.com/rainmanjam/polyemesis/internal/twitch/eventsub"
)

// WHERE A PLATFORM'S ACTIVITY IS WIRED. This file is the extension point the
// per-platform activity PRs share. Twitch EventSub is registered below.
//
// A platform's activity takes one of two shapes:
//
//  1. IT RIDES ITS CHAT CONNECTION (YouTube, Rumble, Kick, X, Facebook). The
//     adapter calls emitActivity on the chat.Receiver it was handed
//     (internal/chat/activity.go), so nothing in this file is involved in the
//     events. It reports its kinds by implementing
//     `ActivityKinds() []activity.KindStatus` on the adapter; chatActivityStatuses
//     below lists them in GET /api/v1/activity/sources with the chat connection's
//     own state as the transport state.
//
//  2. IT NEEDS A TRANSPORT OF ITS OWN (Twitch EventSub,
//     internal/activityhub/twitch.go). It implements
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

// activitySourceBuilders is keyed by platform. Twitch EventSub is the first
// own-transport source.
var activitySourceBuilders = map[db.Platform]activitySourceBuilder{
	db.PlatformTwitch: buildTwitchActivity,
}

// twitchEventSubSeam points the EventSub source at a stub in tests. Empty in
// production.
type twitchEventSubSeam struct {
	WSURL     string
	HelixBase string
	// AllowReconnectHost replaces the "reconnect_url must be under twitch.tv" rule.
	AllowReconnectHost func(rawurl string) error
}

// buildTwitchActivity builds the EventSub source for one connected Twitch
// account. An account with no developer credentials or no token has nothing to
// connect with: that is an error the caller logs, never a stopped chat.
func buildTwitchActivity(s *Server, a db.PlatformAccount) (activityhub.Source, error) {
	if s.store == nil {
		return nil, errors.New("no store")
	}
	creds, err := s.store.GetPlatformCreds(s.box, db.PlatformTwitch)
	if err != nil || creds.ClientID == "" {
		return nil, errors.New("Twitch developer credentials are missing")
	}
	if a.AccountRef == "" {
		return nil, errors.New("the Twitch account has no user id")
	}
	id := a.ID
	return activityhub.NewTwitch(activityhub.TwitchConfig{
		AccountRef: a.AccountRef,
		ClientID:   creds.ClientID,
		Token:      s.chatToken(id),
		Refresh:    s.chatTokenRefresh(id),
		// The scopes the account holds NOW, not the ones it held when the
		// source was built: a reconnect replaces the row.
		Scopes: func() []string {
			cur, err := s.store.GetPlatformAccount(s.box, id)
			if err != nil {
				return eventsub.ScopeList(a.Scopes)
			}
			return eventsub.ScopeList(cur.Scopes)
		},
		Mirrors:            &s.mirroredPolls,
		Log:                s.log,
		WSURL:              s.twitchEventSub.WSURL,
		HelixBase:          s.twitchEventSub.HelixBase,
		AllowReconnectHost: s.twitchEventSub.AllowReconnectHost,
	}), nil
}

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
