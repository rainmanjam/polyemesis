package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// YouTube Studio's own upcoming events, for the schedule calendar: READ-ONLY, and one source for every connected
// YouTube account.
//
// QUOTA. Listing a channel's events is three units (two broadcast lists and a stream list). The calendar can be
// open all day, so each account is asked AT MOST once every studioEventsTTL (15 minutes) no matter how often the
// page polls; in between, the cached answer is served with its age. A failed refresh serves the last good answer
// and says it is stale, rather than retrying on every request. The cost is charged by the provider's request meter.
//
// NEVER A KEY OR AN INGEST ADDRESS: rows are built from oauth.EventSummary, which has neither.

const studioEventsTTL = 15 * time.Minute

type studioCacheEntry struct {
	at     time.Time
	events []oauth.EventSummary
	err    string
}

// studioEventRow is one Studio event in the calendar's answer.
type studioEventRow struct {
	AccountID       int64  `json:"accountId"`
	AccountName     string `json:"accountName"`
	ID              string `json:"id"`
	Title           string `json:"title"`
	ScheduledStart  string `json:"scheduledStart"`
	Privacy         string `json:"privacy,omitempty"`
	Status          string `json:"status"`
	EnableAutoStart bool   `json:"enableAutoStart"`
	// StudioURL opens the event in YouTube Studio.
	StudioURL string `json:"studioUrl"`
}

// handleYouTubeStudioEvents answers GET /youtube/studio-events.
func (s *Server) handleYouTubeStudioEvents(w http.ResponseWriter, r *http.Request) {
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	now := s.clock()
	rows := []studioEventRow{}
	stale := []string{}
	var fetched time.Time
	for _, a := range accts {
		if a.Platform != db.PlatformYouTube {
			continue
		}
		entry := s.studioEventsFor(r.Context(), a, now)
		if entry.err != "" {
			stale = append(stale, a.AccountName)
		}
		if fetched.IsZero() || entry.at.Before(fetched) {
			fetched = entry.at
		}
		for _, e := range entry.events {
			if e.ScheduledStart == "" {
				continue
			}
			rows = append(rows, studioEventRow{
				AccountID: a.ID, AccountName: a.AccountName, ID: e.ID, Title: e.Title, ScheduledStart: e.ScheduledStart,
				Privacy: e.Privacy, Status: e.Status, EnableAutoStart: e.EnableAutoStart,
				StudioURL: "https://studio.youtube.com/video/" + e.ID + "/livestreaming",
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ScheduledStart < rows[j].ScheduledStart })
	resp := map[string]any{"events": rows, "stale": stale}
	if !fetched.IsZero() {
		resp["refreshedAt"] = fetched
	}
	writeJSON(w, http.StatusOK, resp)
}

// studioEventsFor is the account's cached events, refreshed when older than studioEventsTTL. A refresh that fails
// keeps the last good events, records the error and the attempt time (so it is not retried for another TTL).
func (s *Server) studioEventsFor(ctx context.Context, a db.PlatformAccount, now time.Time) studioCacheEntry {
	// Single flight: requests that arrive while a refresh is running wait for it and then read what it stored,
	// rather than each making the three calls.
	s.ytStudioFlight.Lock()
	defer s.ytStudioFlight.Unlock()
	s.ytStudioMu.Lock()
	if cur, ok := s.ytStudio[a.ID]; ok && now.Sub(cur.at) < studioEventsTTL {
		s.ytStudioMu.Unlock()
		return cur
	}
	prev := s.ytStudio[a.ID]
	s.ytStudioMu.Unlock()

	next := studioCacheEntry{at: now}
	mgr, ok := oauth.As[oauth.EventManager](s.providers, a.Platform)
	if !ok {
		next.err = "polyemesis lists events only for YouTube"
	} else {
		cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		acct, err := s.tokenFor(cctx, a.ID)
		if err == nil {
			next.events, err = mgr.ListEvents(cctx, acct.AccessToken, nil)
		}
		if err != nil {
			next.err = err.Error()
			next.events = prev.events
		}
	}
	s.ytStudioMu.Lock()
	if s.ytStudio == nil {
		s.ytStudio = map[int64]studioCacheEntry{}
	}
	s.ytStudio[a.ID] = next
	s.ytStudioMu.Unlock()
	return next
}
