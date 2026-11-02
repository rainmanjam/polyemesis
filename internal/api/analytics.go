package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/rainmanjam/polyemesis/internal/analytics"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/clips"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/metrics"
	"github.com/rainmanjam/polyemesis/internal/mqtt"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// Cross-platform analytics (Wave 4 #4), the API half: the Source the sampler
// reads the world through, the loop main starts, the three admin-only reads,
// and the scrape's audience block. The math is internal/analytics; the
// storage is internal/db/analytics.go.
//
// EVERY ROUTE HERE IS ON readScopeDeniedPatterns. The live picture is cheap
// and makes no outbound call -- it is the sampler's last tick -- but the
// feature is admin-only by decision: the report names the stream's top
// chatters, which is who said things in chat, and the per-account rows name
// the operator's connected channels. A read token is for monitoring the
// pipeline, and the scrape (polyemesis_platform_viewers) already gives a
// dashboard the numbers without the names.

// analyticsQuotaShare is the fraction of the Google client's daily YouTube
// allowance the sampler may spend, and analyticsQuotaTag the name it spends
// it under. Chat, title push and the lifecycle need the rest: a viewer count
// is the thing to give up first.
//
// AT ONE UNIT A TICK (videos.list against the cached id; the lookup is one
// more, once per broadcast), one YouTube account at the default 60 s costs
// 1,440 units a day of continuous streaming -- 14.4% of a default 10,000, just
// under the 1,500-unit share. A second account, or a shorter interval, would
// pass it, and then the interval stretches: see analyticsSource.Interval.
const (
	analyticsQuotaShare = 0.15
	analyticsQuotaTag   = "analytics"
)

// analyticsTopChatters is how many authors a report names.
const analyticsTopChatters = 10

// analyticsStreamsLimit bounds the stream list.
const analyticsStreamsLimit = 100

// ytQuota is the install's YouTube allowance: ONE value, bound to the
// configured Google client, held by every chat adapter, the vertical read,
// the sampler and the request meter (PR #950 round 2, R3 and R12). It is
// never looked up by key per call, so a transient credential-read error
// cannot hand anyone a fresh, unspent budget. Until the credentials have been
// read it is bound lazily here.
func (s *Server) ytQuota() *chat.YouTubeQuota {
	if s.ytQuotaShared.ClientID() == "" {
		s.bindYouTubeClient()
	}
	return s.ytQuotaShared
}

// bindYouTubeClient binds the allowance to the stored YouTube client. Called
// at startup and whenever the YouTube credentials are saved or deleted. A
// read error leaves the binding as it was.
func (s *Server) bindYouTubeClient() {
	if s.store == nil {
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, db.PlatformYouTube)
	if err != nil {
		return
	}
	s.ytQuotaShared.Bind(creds.ClientID)
}

// YouTubeMeter charges the shared allowance for each YouTube Data API call
// internal/oauth makes on a path that did not charge it already.
func (s *Server) YouTubeMeter() oauth.YouTubeMeter {
	return func(cost int) { s.ytQuota().Spend(cost) }
}

// InstallYouTubeMeter is the wiring main uses: every YouTube Data API call
// internal/oauth makes -- title push, compliance, the go-live lifecycle,
// rollover -- is charged to this server's allowance. hosts are extra API
// hosts to meter besides Google's, for a test that aims the providers at a
// stub; production passes none.
func InstallYouTubeMeter(s *Server, hosts ...string) {
	oauth.SetYouTubeMeter(s.YouTubeMeter(), hosts...)
}

// ytLiveVideo is an account's live video, remembered so each tick is one
// videos.list instead of a lookup and a read.
type ytLiveVideo struct {
	id, title string
}

// youTubeViewerReader is the two cheap reads oauth.YouTube offers the
// sampler. Asserted on the provider the Set resolves, so a stubbed Set never
// falls through to production.
type youTubeViewerReader interface {
	ActiveBroadcast(ctx context.Context, accessToken, channelRef string) (string, string, error)
	LiveViewers(ctx context.Context, accessToken, videoID string) (oauth.LiveVideo, error)
}

// analyticsSource is the sampler's view of this server.
type analyticsSource struct{ s *Server }

// OnAir is any destination running, the same predicate the vertical health
// read uses. A listener waiting for a publisher is not a running destination.
func (a analyticsSource) OnAir() bool {
	return len(a.s.runningDestinationIDs()) > 0
}

// runningDestinationIDs is every destination whose process is running.
// analyticsRunning is the test seam.
func (s *Server) runningDestinationIDs() map[int64]bool {
	if s.analyticsRunning != nil {
		return s.analyticsRunning()
	}
	out := map[int64]bool{}
	if s.mgr == nil {
		return out
	}
	for _, ds := range s.mgr.DestinationStatuses() {
		if ds.Process != nil && ds.Process.State == supervisor.StateRunning {
			out[ds.ID] = true
		}
	}
	return out
}

// Targets is the connected accounts that a RUNNING destination streams to,
// and that can be asked for a count. An account nothing is streaming to is not
// this broadcast's audience, and asking it would spend quota on nobody.
//
// A running VERTICAL half has no account of its own: it is the paired
// horizontal's broadcast, so it counts as that account, once (#9). A running
// destination with a pasted key and no account at all is a row that says the
// viewers are unavailable, rather than nothing (owner question 2).
func (a analyticsSource) Targets(context.Context) ([]analytics.Target, error) {
	s := a.s
	running := s.runningDestinationIDs()
	if len(running) == 0 {
		return []analytics.Target{}, nil
	}
	dests, err := s.store.ListDestinations()
	if err != nil {
		return nil, err
	}
	byID := map[int64]*db.Destination{}
	for _, d := range dests {
		byID[d.ID] = d
	}
	horizontalOf := map[int64]int64{}
	if pairs, err := s.store.ListYouTubeVerticalPairs(); err == nil {
		for _, p := range pairs {
			horizontalOf[p.VerticalID] = p.HorizontalID
		}
	}
	linked := map[int64]bool{}
	out := []analytics.Target{}
	// Platforms with a running destination that has no account row: a pasted
	// key, or Rumble, whose chat hangs off no account. Their chat is whatever
	// adapter is attached for that platform (final pass, F1).
	pasted := map[db.Platform]bool{}
	for _, d := range dests {
		if !running[d.ID] {
			continue
		}
		acct := d.AccountID
		if acct == nil {
			if h, ok := byID[horizontalOf[d.ID]]; ok && h.AccountID != nil {
				acct = h.AccountID
			}
		}
		if acct != nil {
			linked[*acct] = true
			continue
		}
		if _, paired := horizontalOf[d.ID]; paired {
			// A vertical half whose horizontal has no account: there is no
			// broadcast to read, and the horizontal's own row says so.
			continue
		}
		pasted[d.Platform] = true
		if _, ok := s.providers.StatsFor(d.Platform); ok {
			out = append(out, analytics.Target{Platform: string(d.Platform), Name: d.Name, Unlinked: true})
		}
	}
	out = append(out, s.attachedChatTargets(pasted)...)
	if len(linked) == 0 {
		return out, nil
	}
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		return nil, err
	}
	for _, acct := range accts {
		if !linked[acct.ID] {
			continue
		}
		t := analytics.Target{
			Platform: string(acct.Platform), AccountID: acct.ID, Account: acct.AccountRef, Name: acct.AccountName,
		}
		// A platform with no viewer count still has chat, and the stream's
		// chat is every running destination's account's (round 2, R2).
		if _, ok := s.providers.StatsFor(acct.Platform); !ok {
			t.ChatOnly = true
		}
		out = append(out, t)
	}
	return out, nil
}

// attachedChatTargets is a chat-only target for every attached chat adapter
// of a platform a pasted-key destination is running on, by the adapter's own
// account ref -- independent of an account row and of a viewer count.
func (s *Server) attachedChatTargets(platforms map[db.Platform]bool) []analytics.Target {
	if s.chat == nil || len(platforms) == 0 {
		return nil
	}
	var out []analytics.Target
	for _, st := range s.chat.Statuses() {
		if platforms[st.Platform] && st.Account != "" {
			out = append(out, analytics.Target{Platform: string(st.Platform), Account: st.Account, ChatOnly: true})
		}
	}
	return out
}

// Allow reports whether a YouTube read fits the sampler's share of the shared
// allowance, spending nothing: Read charges each call it actually makes.
// Every other platform has no daily allowance; it is paced by its own
// Retry-After instead (#4).
//
// Until the YouTube credentials have been read there is no client to charge,
// so a YouTube read is refused rather than charged to nobody (R3).
func (a analyticsSource) Allow(t analytics.Target) bool {
	if t.Platform != string(db.PlatformYouTube) {
		return true
	}
	q := a.s.ytQuota()
	return q.ClientID() != "" && q.AffordShare(analyticsQuotaTag, analyticsQuotaShare, 1)
}

// Interval stretches the configured interval when the sampler's share of the
// YouTube allowance cannot afford it until the reset: with r reads left in
// the share and c YouTube accounts read each tick, the wait is at least
// reset-in / (r / c). Platforms without an allowance never stretch it.
func (a analyticsSource) Interval(configured time.Duration, targets []analytics.Target) time.Duration {
	perTick := 0
	for _, t := range targets {
		if t.Platform == string(db.PlatformYouTube) && !t.Unlinked {
			perTick++
		}
	}
	if perTick == 0 {
		return configured
	}
	q := a.s.ytQuota()
	// Reads left above the send reserve (R9): the share is inside it.
	left := min(q.ShareLeft(analyticsQuotaTag, analyticsQuotaShare), q.ReadsLeft())
	ticks := left / perTick
	resetIn := q.ResetIn()
	if ticks <= 0 {
		// Nothing left until the reset. Waiting for it costs nothing on the
		// other platforms only if there are none; keep sampling them at the
		// configured interval and let YouTube read "quota".
		return configured
	}
	return max(configured, resetIn/time.Duration(ticks))
}

// Read is handleAccountStats without the HTTP: refresh the token if it has
// expired, then ask the platform. A platform refusal is mapped to the
// sampler's kinds: a spent quota is ErrQuota (and pauses the shared YouTube
// allowance), a rate limit is a RetryAfterError (#4, #5).
func (a analyticsSource) Read(ctx context.Context, t analytics.Target) (analytics.Stats, error) {
	s := a.s
	acct, err := s.tokenFor(ctx, t.AccountID)
	if err != nil {
		return analytics.Stats{}, err
	}
	var st analytics.Stats
	if t.Platform == string(db.PlatformYouTube) {
		st, err = a.readYouTube(ctx, t, acct.AccessToken)
	} else {
		st, err = a.readStats(ctx, acct)
	}
	return st, a.platformError(t, err)
}

func (a analyticsSource) readStats(ctx context.Context, acct *db.PlatformAccount) (analytics.Stats, error) {
	s := a.s
	sf, ok := s.providers.StatsFor(acct.Platform)
	if !ok {
		return analytics.Stats{}, fmt.Errorf("polyemesis does not read a viewer count from %s", acct.Platform)
	}
	creds, err := s.store.GetPlatformCreds(s.box, acct.Platform)
	if err != nil {
		return analytics.Stats{}, err
	}
	ls, err := sf.Stats(ctx, creds.ClientID, acct.AccessToken)
	if err != nil {
		return analytics.Stats{}, err
	}
	return statsOf(ls), nil
}

// readYouTube is the cheap YouTube read (#3): one videos.list against the
// account's remembered live video, and a liveBroadcasts.list only when there
// is none, or it has gone or ended. Each call is charged to the sampler's
// share before it is made; the context tells the request meter not to count
// it a second time.
func (a analyticsSource) readYouTube(ctx context.Context, t analytics.Target, token string) (analytics.Stats, error) {
	s := a.s
	p, err := s.providers.Get(db.PlatformYouTube)
	if err != nil {
		return analytics.Stats{}, err
	}
	yr, ok := p.(youTubeViewerReader)
	if !ok {
		return analytics.Stats{}, fmt.Errorf("polyemesis does not read a viewer count from %s", t.Platform)
	}
	q := s.ytQuota()
	charge := func() bool { return q.ChargeShare(analyticsQuotaTag, analyticsQuotaShare, 1) }
	ctx = oauth.WithQuotaCharged(ctx)

	s.ytVideosMu.Lock()
	cached, have := s.ytVideos[t.AccountID]
	s.ytVideosMu.Unlock()
	if have {
		if !charge() {
			return analytics.Stats{}, analytics.ErrQuota
		}
		v, err := yr.LiveViewers(ctx, token, cached.id)
		if err != nil {
			return analytics.Stats{}, err
		}
		if v.Found && !v.Ended {
			return analytics.Stats{Live: true, Viewers: v.Viewers, Title: cached.title}, nil
		}
		s.forgetYouTubeVideo(t.AccountID)
	}
	if !charge() {
		return analytics.Stats{}, analytics.ErrQuota
	}
	id, title, err := yr.ActiveBroadcast(ctx, token, t.Account)
	if err != nil {
		return analytics.Stats{}, err
	}
	if id == "" {
		return analytics.Stats{}, nil
	}
	s.ytVideosMu.Lock()
	s.ytVideos[t.AccountID] = ytLiveVideo{id: id, title: title}
	s.ytVideosMu.Unlock()
	if !charge() {
		return analytics.Stats{}, analytics.ErrQuota
	}
	v, err := yr.LiveViewers(ctx, token, id)
	if err != nil {
		return analytics.Stats{}, err
	}
	if !v.Found || v.Ended {
		s.forgetYouTubeVideo(t.AccountID)
		return analytics.Stats{}, nil
	}
	return analytics.Stats{Live: true, Viewers: v.Viewers, Title: title}, nil
}

func (s *Server) forgetYouTubeVideo(accountID int64) {
	s.ytVideosMu.Lock()
	delete(s.ytVideos, accountID)
	s.ytVideosMu.Unlock()
}

// platformError maps a platform's typed refusal onto the sampler's kinds.
func (a analyticsSource) platformError(t analytics.Target, err error) error {
	var se *oauth.StatusError
	if err == nil || !errors.As(err, &se) {
		return err
	}
	switch {
	case se.QuotaExceeded:
		if t.Platform == string(db.PlatformYouTube) {
			// YouTube is the authority on its own counter: every spender of
			// this client stops until the reset.
			a.s.ytQuota().Pause()
		}
		return fmt.Errorf("%w: %v", analytics.ErrQuota, err)
	case se.Status == http.StatusTooManyRequests:
		return &analytics.RetryAfterError{After: max(se.RetryAfter, time.Minute)}
	}
	return err
}

func statsOf(ls *oauth.LiveStats) analytics.Stats {
	if ls == nil {
		return analytics.Stats{}
	}
	return analytics.Stats{Live: ls.Live, Viewers: ls.ViewerCount, Title: ls.Title}
}

// Player is the public player's request-side viewer count and its QoE
// counters, when the player is enabled and running. Viewers is
// playout.Analytics.Viewers only -- not QoE's active sessions as well, which
// count many of the same people a second time.
func (a analyticsSource) Player() (int, analytics.PlayerQoE, bool) {
	m := a.s.playoutManager()
	if m == nil || !m.Settings().Enabled {
		return 0, analytics.PlayerQoE{}, false
	}
	an := m.Analytics()
	q := an.QoE
	return an.Viewers, analytics.PlayerQoE{
		Sessions:           q.Sessions,
		Startups:           q.Startups,
		StartupMs:          q.StartupMs,
		Rebuffers:          q.Rebuffers,
		RebufferMs:         q.RebufferMs,
		SessionsWithErrors: q.SessionsWithErrors,
		StartupHistogram:   append([]int64(nil), q.StartupHistogram...),
	}, true
}

// analyticsConfig reads the sampler's settings. A store that cannot be read
// leaves the sampler off for the tick rather than sampling on a guess.
func (s *Server) analyticsConfig() analytics.Config {
	set, err := s.store.GetSettings()
	if err != nil {
		return analytics.Config{}
	}
	a := set.Analytics
	return analytics.Config{
		Enabled:   a.Enabled,
		Interval:  time.Duration(a.IntervalSeconds) * time.Second,
		Retention: time.Duration(a.RetentionDays) * 24 * time.Hour,
	}
}

// AnalyticsLoop runs the audience sampler until ctx ends. main starts it
// beside the rollover sweep.
func (s *Server) AnalyticsLoop(ctx context.Context) {
	if s.audience == nil {
		return
	}
	s.audience.Run(ctx)
}

func (s *Server) newAudienceSampler() *analytics.Sampler {
	// Late-bound, so a test can set the clock seam after New.
	now := func() time.Time {
		if s.analyticsNow != nil {
			return s.analyticsNow()
		}
		return time.Now()
	}
	var log *slog.Logger
	if s.log != nil {
		log = s.log.With("component", "analytics")
	}
	return analytics.New(analyticsSource{s}, s.store, s.analyticsConfig, now, log)
}

// liveAnalytics is the sampler's picture, or a not-sampling one when the
// server has no sampler.
func (s *Server) liveAnalytics() analytics.Live {
	if s.audience == nil {
		return analytics.Live{Readings: []analytics.Reading{}, Points: []analytics.Point{}}
	}
	return s.audience.Live()
}

// GET /analytics/live
func (s *Server) handleAnalyticsLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.liveAnalytics())
}

// GET /analytics/streams
func (s *Server) handleAnalyticsStreams(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListAnalyticsStreams(analyticsStreamsLimit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]analytics.Stream, 0, len(rows))
	for _, row := range rows {
		st := row.Stream
		// A closed stream carries its peak and average, stored at close (#12).
		// Only the one open stream -- at most a day of samples, by the
		// rollover -- is summed here.
		if st.EndedAt == nil {
			if samples, err := s.store.AnalyticsSamples(st.ID); err == nil {
				sum := analytics.Summarise(samples)
				st.PeakTotal, st.AverageTotal = sum.PeakTotal, sum.AverageTotal
			}
		}
		out = append(out, st)
	}
	writeJSON(w, http.StatusOK, map[string]any{"streams": out})
}

// GET /analytics/streams/{id}, and ?format=csv for the same report as CSV.
func (s *Server) handleAnalyticsStream(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "json" && format != "csv" {
		writeError(w, http.StatusBadRequest, "format must be json or csv")
		return
	}
	rep, err := s.analyticsReport(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if format != "csv" {
		writeJSON(w, http.StatusOK, rep)
		return
	}
	var b bytes.Buffer
	if err := analytics.WriteCSV(&b, rep); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The filename is built from the id alone, never the title: a title is
	// whatever the platform reported, and it does not belong in a header.
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="polyemesis-stream-`+strconv.FormatInt(id, 10)+`.csv"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The report names chatters: kept out of every cache, as the caption
	// export is.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.Bytes())
}

// analyticsReport assembles one stream's report.
func (s *Server) analyticsReport(id int64) (analytics.Report, error) {
	row, err := s.store.GetAnalyticsStream(id)
	if err != nil {
		return analytics.Report{}, err
	}
	samples, err := s.store.AnalyticsSamples(id)
	if err != nil {
		return analytics.Report{}, err
	}
	counts, top, err := s.store.AnalyticsChat(id, analyticsTopChatters)
	if err != nil {
		return analytics.Report{}, err
	}
	rep := analytics.Report{
		Stream:      row.Stream,
		Summary:     analytics.Summarise(samples),
		Chat:        counts,
		TopChatters: top,
		Recordings:  []analytics.RecordingLink{},
		Clips:       []analytics.ClipLink{},
	}
	if row.Stream.EndedAt != nil {
		// An ended stream's totals are the ones stored at its close: retention
		// thins its samples, and the list reads the stored pair (R10).
		rep.Summary.PeakTotal, rep.Summary.AverageTotal = row.Stream.PeakTotal, row.Stream.AverageTotal
	}
	rep.Stream.PeakTotal, rep.Stream.AverageTotal = rep.Summary.PeakTotal, rep.Summary.AverageTotal
	if row.QoEStart != nil && row.QoELast != nil {
		if d, ok := row.QoELast.Delta(*row.QoEStart); ok {
			rep.Player = &d
		}
	}

	start := row.Stream.StartedAt
	end := start.Add(time.Duration(row.Stream.DurationMs) * time.Millisecond)
	if recs, err := s.store.ListRecordings(); err == nil {
		for _, rec := range recs {
			recEnd := rec.StartedAt.Add(time.Duration(rec.DurationMS) * time.Millisecond)
			// Overlap, with a minute's slack either side: a segment's start is
			// stamped to the second and the first sample lands up to a tick
			// after go-live.
			if rec.StartedAt.After(end.Add(time.Minute)) || recEnd.Before(start.Add(-time.Minute)) {
				continue
			}
			has, _ := s.store.HasTranscript(rec.ID)
			rep.Recordings = append(rep.Recordings, analytics.RecordingLink{
				ID: rec.ID, Filename: rec.Filename, StartedAt: rec.StartedAt,
				DurationMs: rec.DurationMS, Transcript: has,
			})
		}
		sort.Slice(rep.Recordings, func(i, j int) bool {
			return rep.Recordings[i].StartedAt.Before(rep.Recordings[j].StartedAt)
		})
	}
	if list, err := clips.List(s.clipDir()); err == nil {
		for _, c := range list {
			// Captured while the stream was on air, with the same slack after
			// the end: a clip of the last minute is asked for after it.
			if c.CreatedAt.Before(start) || c.CreatedAt.After(end.Add(analytics.CloseGrace)) {
				continue
			}
			rep.Clips = append(rep.Clips, analytics.ClipLink{Name: c.Name, CreatedAt: c.CreatedAt, Seconds: c.Seconds})
		}
		sort.Slice(rep.Clips, func(i, j int) bool { return rep.Clips[i].CreatedAt.Before(rep.Clips[j].CreatedAt) })
	}
	return rep, nil
}

// audienceMetrics is the scrape's audience block: the last tick's count per
// platform and the cross-platform total. Nil when nothing is sampling.
func (s *Server) audienceMetrics() *metrics.Audience {
	if s.audience == nil {
		return nil
	}
	l := s.audience.Live()
	a := &metrics.Audience{Platforms: map[string]*int{}}
	if !l.OnAir {
		return a
	}
	a.Platforms = l.ByPlatform()
	// A partial tick's total is the audience minus whoever failed; graphing
	// it would draw a dip that never happened (#6).
	if !l.Partial {
		a.Total = l.Total
	}
	return a
}

// AudienceState is the viewer picture for the MQTT topic: counts only, with
// an unreported platform left nil. Nil when no sampler runs.
func (s *Server) AudienceState() *mqtt.AudienceState {
	if s.audience == nil {
		return nil
	}
	l := s.audience.Live()
	out := &mqtt.AudienceState{OnAir: l.OnAir, At: time.Now()}
	if l.At != nil {
		out.At = *l.At
	}
	if !l.OnAir {
		return out
	}
	out.Reported = l.Reported
	if !l.Partial {
		out.Total = l.Total
	}
	by := l.ByPlatform()
	out.YouTube, out.Twitch, out.Kick = by[string(db.PlatformYouTube)], by[string(db.PlatformTwitch)], by[string(db.PlatformKick)]
	for _, r := range l.Readings {
		if r.Platform == analytics.PlatformPlayer && r.Viewers != nil {
			v := *r.Viewers
			out.Player = &v
		}
	}
	return out
}
