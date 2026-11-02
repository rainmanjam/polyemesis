package api

import (
	"bytes"
	"context"
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

// youTubeStatsQuotaCost is what one oauth.YouTube.Stats call spends:
// channels.list (Account), liveBroadcasts.list, then videos.list. Each is a
// list operation, one unit per the truth sheet's correction 6
// (docs-youtube.md); liveBroadcasts.list carries chat's own, unverified,
// figure so the two cannot drift apart.
const youTubeStatsQuotaCost = 1 + chat.QuotaCostListBroadcasts + 1

// analyticsTopChatters is how many authors a report names.
const analyticsTopChatters = 10

// analyticsStreamsLimit bounds the stream list.
const analyticsStreamsLimit = 100

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
func (a analyticsSource) Targets(context.Context) ([]analytics.Target, error) {
	s := a.s
	running := s.runningDestinationIDs()
	dests, err := s.store.ListDestinations()
	if err != nil {
		return nil, err
	}
	linked := map[int64]bool{}
	for _, d := range dests {
		if d.AccountID == nil || !running[d.ID] {
			continue
		}
		linked[*d.AccountID] = true
	}
	if len(linked) == 0 {
		return []analytics.Target{}, nil
	}
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		return nil, err
	}
	out := []analytics.Target{}
	for _, acct := range accts {
		if !linked[acct.ID] {
			continue
		}
		if _, ok := s.providers.StatsFor(acct.Platform); !ok {
			continue
		}
		out = append(out, analytics.Target{
			Platform: string(acct.Platform), AccountID: acct.ID, Account: acct.AccountRef, Name: acct.AccountName,
		})
	}
	return out, nil
}

// Allow charges a YouTube read to the chat poll's quota budget for that
// account, through the same seam the vertical status read uses. Every other
// platform has no daily allowance to protect.
func (a analyticsSource) Allow(t analytics.Target) bool {
	s := a.s
	if t.Platform != string(db.PlatformYouTube) {
		return true
	}
	if s.analyticsChargeQuota != nil {
		return s.analyticsChargeQuota(t.Account, youTubeStatsQuotaCost)
	}
	if s.chat == nil {
		return true
	}
	ad, ok := s.chat.Adapter(db.PlatformYouTube, t.Account)
	if !ok {
		return true
	}
	c, ok := ad.(quotaChargeable)
	if !ok {
		return true
	}
	return c.ChargeQuota(youTubeStatsQuotaCost)
}

// Read is handleAccountStats without the HTTP: refresh the token if it has
// expired, then ask through the provider Set's Stats twin.
func (a analyticsSource) Read(ctx context.Context, t analytics.Target) (analytics.Stats, error) {
	s := a.s
	st, ok := s.providers.StatsFor(db.Platform(t.Platform))
	if !ok {
		return analytics.Stats{}, fmt.Errorf("polyemesis does not read a viewer count from %s", t.Platform)
	}
	acct, err := s.tokenFor(ctx, t.AccountID)
	if err != nil {
		return analytics.Stats{}, err
	}
	creds, err := s.store.GetPlatformCreds(s.box, acct.Platform)
	if err != nil {
		return analytics.Stats{}, err
	}
	ls, err := st.Stats(ctx, creds.ClientID, acct.AccessToken)
	if err != nil {
		return analytics.Stats{}, err
	}
	return statsOf(ls), nil
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
		if samples, err := s.store.AnalyticsSamples(st.ID); err == nil {
			sum := analytics.Summarise(samples)
			st.PeakTotal, st.AverageTotal = sum.PeakTotal, sum.AverageTotal
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
	a.Total = l.Total
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
	out.Total, out.Reported = l.Total, l.Reported
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
