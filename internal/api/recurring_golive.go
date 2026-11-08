package api

// The go-live title and category for Twitch and Kick (#18).
//
// Neither platform has an event to create ahead of time. A Twitch or Kick channel has one live title
// and one category, and they are written when the show starts. So a start schedule's template reaches
// those two platforms here, at the moment the destination turns on, and not through the look-ahead.
//
// WHEN IT WRITES. A destination on a template-bearing start schedule is written once for each show, and
// only during the first goLiveWindow after the show's start instant. The start instant is the one the
// schedule fired at, which is the last occurrence at or before now. A destination the operator turns on
// by hand, long after the start, is not written: the operator is the one who chose its title then, and
// a template title written over it would be a surprise.
//
// THE CLAIM. A row in recurring_go_live is taken before the platform call, so the write happens once per
// show. It is kept whether the write succeeded or failed. A failed write is logged, and it is not retried
// for the same show: retrying a title write every 30 seconds through a live show is worse than a stale
// title the operator can fix in the composer, and the log names the refusal.
//
// WHAT IS SKIPPED, AND LOUDLY. Twitch has no stream description, and Kick's live broadcast has none
// either, so the template description is not sent to them. A category the platform cannot find is a
// warning from PushMetadata, and it is logged at warn level with the platform's own words.
//
// The write goes through the same PushMetadata the go-live composer uses, so it carries the same scope
// check and the same refusal advice as a manual push.

import (
	"context"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/redact"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

const (
	// goLiveTick is how often the start of a show is looked for. Short, because the title should be in
	// place within a minute of the show starting, and a read of three small tables costs nothing.
	goLiveTick = 30 * time.Second
	// goLiveWindow is how long after a show's start the go-live title may be written.
	goLiveWindow = 30 * time.Minute
	// goLiveLookback is how far back the last start instant is searched for. Eight days covers a weekly
	// schedule, the longest recurrence a schedule has.
	goLiveLookback = 8 * 24 * time.Hour
	// goLiveWalkLimit bounds the walk over the instants inside the lookback. A daily schedule has eight
	// in the window; the bound is generous and only stops a schedule that cannot be evaluated.
	goLiveWalkLimit = 64
)

// goLivePlatform reports whether a platform takes its title and category at go-live. YouTube and
// Facebook get theirs through the scheduled-event path, and every other platform has no template write.
func goLivePlatform(p db.Platform) bool {
	return p == db.PlatformTwitch || p == db.PlatformKick
}

// lastOccurrence is the most recent instant a schedule fired at, at or before now. It walks forward from
// the lookback, so it reads the same zone and daylight-saving rules Next does.
func lastOccurrence(sc scheduler.Schedule, now time.Time) (time.Time, bool) {
	sc = sc.Normalized()
	if _, err := sc.Location(); err != nil {
		return time.Time{}, false
	}
	var last time.Time
	found := false
	cursor := now.Add(-goLiveLookback)
	for i := 0; i < goLiveWalkLimit; i++ {
		at, ok := scheduler.Next(sc, cursor)
		if !ok || at.After(now) {
			break
		}
		last, found, cursor = at, true, at
	}
	return last, found
}

// goLiveTemplatesOnce writes the go-live title for every Twitch or Kick destination whose show started
// within the window. It is the body of the 30-second tick, split out so a test drives it with a clock.
func (s *Server) goLiveTemplatesOnce(ctx context.Context, now time.Time) {
	tpls, err := s.store.ListBroadcastTemplates()
	if err != nil {
		s.log.Warn("recurring: cannot read templates for go-live", "err", redactErr(err))
		return
	}
	if len(tpls) == 0 {
		return
	}
	scheds, err := s.store.ListSchedules()
	if err != nil {
		s.log.Warn("recurring: cannot read schedules for go-live", "err", redactErr(err))
		return
	}
	dests, err := s.store.ListDestinations()
	if err != nil {
		s.log.Warn("recurring: cannot read destinations for go-live", "err", redactErr(err))
		return
	}
	schedByID := make(map[int64]scheduler.Schedule, len(scheds))
	for _, sc := range scheds {
		schedByID[sc.ID] = sc
	}
	for _, tpl := range tpls {
		sc, ok := schedByID[tpl.ScheduleID]
		if !ok || !sc.Enabled || sc.Action != scheduler.ActionStart {
			continue
		}
		at, ok := lastOccurrence(sc, now)
		if !ok || now.Sub(at) >= goLiveWindow {
			continue
		}
		for _, d := range dests {
			if !goLivePlatform(d.Platform) || !d.Enabled || d.AccountID == nil || !scheduleTargets(sc, d.ID) {
				continue
			}
			s.goLiveFor(ctx, tpl, sc, d, at)
		}
	}
}

// goLiveFor writes one show's title and category to one destination, once.
func (s *Server) goLiveFor(ctx context.Context, tpl scheduler.BroadcastTemplate, sc scheduler.Schedule,
	d *db.Destination, at time.Time) {
	loc, err := sc.Location()
	if err != nil {
		return
	}
	title, err := scheduler.RenderTitle(tpl.TitlePattern, at, loc)
	if err != nil {
		s.log.Warn("recurring: the title pattern cannot be rendered for go-live", "schedule", sc.Name, "err", redactErr(err))
		return
	}
	mp, ok := oauth.As[oauth.MetadataPusher](s.providers, d.Platform)
	if !ok {
		return
	}
	acct, err := s.tokenFor(ctx, *d.AccountID)
	if err != nil {
		s.log.Warn("recurring: no usable token for the go-live title", "destination", d.Name, "err", redactErr(err))
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, acct.Platform)
	if err != nil {
		s.log.Warn("recurring: no platform credentials for the go-live title", "destination", d.Name)
		return
	}
	// The claim is taken here, immediately before the write, so a token or credentials failure above
	// does not use up the show's one attempt.
	claimed, err := s.store.ClaimGoLive(d.ID, sc.ID, at)
	if err != nil {
		s.log.Warn("recurring: cannot record the go-live title for a show", "destination", d.Name, "err", redactErr(err))
		return
	}
	if !claimed {
		return
	}
	// Each template field the platform cannot take is named once, so an operator can tell why the
	// channel does not show it. Only the title and the category are sent.
	if tpl.Description != "" {
		s.log.Warn("recurring: the template's description is not sent; the platform has no stream description",
			"platform", d.Platform, "destination", d.Name, "schedule", sc.Name)
	}
	if tpl.HasThumbnail {
		s.log.Warn("recurring: the template's thumbnail is not sent; the platform takes no thumbnail at go-live",
			"platform", d.Platform, "destination", d.Name, "schedule", sc.Name)
	}
	if tpl.Privacy != "" && tpl.Privacy != "public" {
		s.log.Warn("recurring: the template's privacy is not sent; the platform has no scheduled privacy at go-live",
			"platform", d.Platform, "destination", d.Name, "schedule", sc.Name)
	}
	cctx, cancel := context.WithTimeout(ctx, templateCallTimeout)
	defer cancel()
	var res *oauth.MetadataResult
	err = s.kickRetry(cctx, acct, func(token string) (e error) {
		res, e = mp.PushMetadata(cctx, creds.ClientID, token, acct.AccountRef, oauth.Metadata{
			Title:    title,
			Category: tpl.Category,
		})
		return e
	})
	if err != nil {
		s.log.Warn("recurring: the go-live title was refused; it is not retried for this show",
			"destination", d.Name, "schedule", sc.Name, "err", redactErr(err))
		return
	}
	if res == nil {
		return
	}
	for _, w := range res.Warnings {
		s.log.Warn("recurring: the platform did not take part of the go-live metadata",
			"destination", d.Name, "schedule", sc.Name, "detail", redact.Redact(w))
	}
	s.log.Info("recurring: wrote the show's title and category at go-live",
		"destination", d.Name, "schedule", sc.Name, "at", at, "applied", len(res.Applied))
}

// xGoLiveTitle is the title an X PUBLISH carries, as of now. It is the lifecycle
// coordinator's title source.
func (s *Server) xGoLiveTitle(d *db.Destination) string {
	return s.xGoLiveTitleAt(d, time.Now())
}

// xGoLiveTitleAt is the title an X PUBLISH at now carries. X takes a title only
// on PUBLISH (it has no write before or during a show), so this is where a
// recurring template reaches X: when a template-bearing start schedule that
// names d fired within goLiveWindow, its rendered title is sent. Otherwise the
// destination's stored go-live title is sent. A store that cannot be read is
// the stored title too: a missed template title is a plainer show, not a show
// that never goes public.
func (s *Server) xGoLiveTitleAt(d *db.Destination, now time.Time) string {
	if s.store == nil {
		return d.GoLiveTitle
	}
	tpls, err := s.store.ListBroadcastTemplates()
	if err != nil || len(tpls) == 0 {
		return d.GoLiveTitle
	}
	scheds, err := s.store.ListSchedules()
	if err != nil {
		return d.GoLiveTitle
	}
	schedByID := make(map[int64]scheduler.Schedule, len(scheds))
	for _, sc := range scheds {
		schedByID[sc.ID] = sc
	}
	// Every schedule that fired inside the window is a candidate, and the one that fired last wins: two shows on
	// the same destination half an hour apart must not publish the second under the first one's title. A tie goes
	// to the lower schedule id, so the answer does not depend on the order the store lists templates in.
	var (
		best   string
		bestAt time.Time
		bestID int64
	)
	for _, tpl := range tpls {
		sc, ok := schedByID[tpl.ScheduleID]
		if !ok || !sc.Enabled || sc.Action != scheduler.ActionStart || !scheduleTargets(sc, d.ID) {
			continue
		}
		at, ok := lastOccurrence(sc, now)
		if !ok || now.Sub(at) >= goLiveWindow {
			continue
		}
		loc, err := sc.Location()
		if err != nil {
			continue
		}
		title, err := scheduler.RenderTitle(tpl.TitlePattern, at, loc)
		if err != nil || strings.TrimSpace(title) == "" {
			continue
		}
		if best != "" && (at.Before(bestAt) || (at.Equal(bestAt) && sc.ID > bestID)) {
			continue
		}
		// No length check here: a template renders to at most 100 characters
		// (scheduler.BroadcastTemplate.Validate), well inside X's 280.
		best, bestAt, bestID = title, at, sc.ID
	}
	if best != "" {
		return best
	}
	return d.GoLiveTitle
}

// xTitleLiveRefusal is the answer to a go-live title change while the X
// broadcast runs. X documents no write that changes a running broadcast's title
// (the PUBLISH transition is the only one that takes one), so the copy says the
// change is not available, not that it is unverified.
const xControlsLiveRefusal = "X cannot change the chat access mode or language of a broadcast that is live: they are sent " +
	"only when the broadcast is published. Save them after this show ends, and the next show uses them"

const xTitleLiveRefusal = "X cannot change the title of a broadcast that is live: the title is sent " +
	"only when the broadcast is published. Save the new title after this show ends, and the next show uses it"
