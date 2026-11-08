package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

const ypeStreams = `{"items":[
 {"id":"stream-A","snippet":{"title":"Main"},"status":{"streamStatus":"inactive"},
  "cdn":{"ingestionType":"rtmp","ingestionInfo":{"streamName":"` + ytpKeyA + `","ingestionAddress":"rtmp://a.rtmp.youtube.com/live2","backupIngestionAddress":"rtmp://b.rtmp.youtube.com/live2"}}},
 {"id":"stream-B","snippet":{"title":"Other"},"status":{"streamStatus":"active"},
  "cdn":{"ingestionType":"rtmp","ingestionInfo":{"streamName":"` + ytpKeyB + `","ingestionAddress":"rtmp://a.rtmp.youtube.com/live2"}}}]}`

func ypeEvent(id, status, bound string, autoStart bool) string {
	return `{"id":"` + id + `","snippet":{"title":"Event ` + id + `","scheduledStartTime":"2030-01-01T18:00:00Z","channelId":"UCchan"},` +
		`"status":{"lifeCycleStatus":"` + status + `","privacyStatus":"private"},` +
		`"contentDetails":{"boundStreamId":"` + bound + `","enableAutoStart":` + strconv.FormatBool(autoStart) + `,"enableAutoStop":false,"enableDvr":true,"monitorStream":{"enableMonitorStream":false}}}`
}

func ypeFixture(t *testing.T, puts *[]string) (*Server, http.Handler, *db.DB, func(*http.Request), int64) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/liveStreams":
			_, _ = io.WriteString(w, ypeStreams)
		case r.URL.Path == "/liveBroadcasts" && r.Method == http.MethodGet && r.URL.Query().Get("id") != "":
			_, _ = io.WriteString(w, `{"items":[`+ypeEvent(r.URL.Query().Get("id"), "ready", "stream-A", false)+`]}`)
		case r.URL.Path == "/liveBroadcasts" && r.Method == http.MethodGet && r.URL.Query().Get("broadcastStatus") == "upcoming":
			_, _ = io.WriteString(w, `{"items":[`+ypeEvent("ev-up", "ready", "stream-A", false)+`]}`)
		case r.URL.Path == "/liveBroadcasts" && r.Method == http.MethodGet && r.URL.Query().Get("broadcastStatus") == "active":
			_, _ = io.WriteString(w, `{"items":[`+ypeEvent("ev-live", "live", "stream-B", true)+`]}`)
		case r.URL.Path == "/channels":
			_, _ = io.WriteString(w, `{"items":[{"id":"UCchan","snippet":{"title":"Chan"}}]}`)
		case r.URL.Path == "/liveBroadcasts" && r.Method == http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			*puts = append(*puts, string(b))
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	s, h, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	sign := login(t, h)
	return s, h, store, sign, connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
}

// The event list carries the flags the dialog warns from and never a key or an ingest address.
func TestTheYouTubeEventPickerListsFlagsAndNeverReturnsAKeyOrAnIngestAddress(t *testing.T) {
	var puts []string
	_, h, store, sign, acct := ypeFixture(t, &puts)
	d := ytpDest(t, store, acct, "main", ytpKeyA, "")
	ytpDest(t, store, acct, "second", ytpKeyB, "")
	r := jsonRequest(t, http.MethodGet, "/api/v1/platforms/accounts/"+itoa(acct)+"/youtube/events?destinationId="+itoa(d.ID), nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, leak := range []string{ytpKeyA, ytpKeyB, "streamName", "ingestionAddress", "backupIngestionAddress", "rtmp://"} {
		if strings.Contains(body, leak) {
			t.Errorf("the event list carries %q: %s", leak, body)
		}
	}
	var got struct {
		Events []struct {
			ID              string `json:"id"`
			EnableAutoStart bool   `json:"enableAutoStart"`
			EnableAutoStop  bool   `json:"enableAutoStop"`
			BoundStreamID   string `json:"boundStreamId"`
			StreamActive    *bool  `json:"streamActive"`
			OnThisStream    bool   `json:"onThisStream"`
			Privacy         string `json:"privacy"`
		} `json:"events"`
	}
	mustDecode(t, w.Body.Bytes(), &got)
	if len(got.Events) != 2 {
		t.Fatalf("events = %+v", got.Events)
	}
	up, live := got.Events[0], got.Events[1]
	if up.ID != "ev-up" || up.EnableAutoStart || up.Privacy != "private" || !up.OnThisStream || up.StreamActive == nil || *up.StreamActive {
		t.Errorf("upcoming event = %+v, want auto-start off, private, on this destination's idle stream", up)
	}
	if live.ID != "ev-live" || live.OnThisStream || live.StreamActive == nil || !*live.StreamActive || live.BoundStreamID != "stream-B" {
		t.Errorf("live event = %+v, want it on the other (active) stream", live)
	}
}

// One click, one write: auto-start on, every other field carried through, and only for the event named.
func TestTurningAutoStartOnIsOneWriteForTheNamedEvent(t *testing.T) {
	var puts []string
	_, h, _, sign, acct := ypeFixture(t, &puts)
	r := jsonRequest(t, http.MethodPost, "/api/v1/platforms/accounts/"+itoa(acct)+"/youtube/events/ev-up/auto-start", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(puts) != 1 || !strings.Contains(puts[0], `"enableAutoStart":true`) || !strings.Contains(puts[0], `"id":"ev-up"`) ||
		!strings.Contains(puts[0], `"enableAutoStop":false`) {
		t.Errorf("writes = %v, want one update turning auto-start on and carrying the rest", puts)
	}
}

// proveYouTubeEvents discharges GET .../youtube/events in the route ledger, as proveYouTubeStreams does.
func proveYouTubeEvents(t *testing.T) proofResult {
	var puts []string
	_, h, store, sign, acct := ypeFixture(t, &puts)
	ytpDest(t, store, acct, "main", ytpKeyA, "")
	read := createScopedToken(t, h, sign, "ledger-youtube-events", db.ScopeRead)
	path := "/api/v1/platforms/accounts/" + itoa(acct) + "/youtube/events"
	code, admin := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK {
		t.Fatalf("the admin's event list answered %d: %s", code, truncateForFailure(admin))
	}
	if strings.Contains(admin, ytpKeyA) {
		t.Errorf("the event list carries a stream key even to the admin: %s", truncateForFailure(admin))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's event list answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, path),
		High:      bodyOf(t, h, sign, "/api/v1/destinations"),
		Read:      low,
		Sentinels: []string{ytpKeyA},
	}
}

// Refreshing a destination's key never creates a broadcast. A refresh on a YouTube destination that holds a listed
// key re-reads the stream and answers; a POST to liveBroadcasts or bind from it would put a second public broadcast
// on the channel every time somebody pressed the button.
func TestRefreshingAYouTubeKeyNeverCreatesOrBindsABroadcast(t *testing.T) {
	var posts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			posts = append(posts, r.Method+" "+r.URL.Path)
		}
		switch r.URL.Path {
		case "/channels":
			_, _ = io.WriteString(w, `{"items":[{"id":"chan-ref","snippet":{"title":"Chan"}}]}`)
		case "/liveStreams":
			_, _ = io.WriteString(w, ypeStreams)
		default:
			_, _ = io.WriteString(w, `{"items":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	addSource(t, h, sign, "main")
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "secret"); err != nil {
		t.Fatal(err)
	}
	d := ytpDest(t, store, acct, "main", ytpKeyA, "")
	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/"+itoa(d.ID)+"/refresh-key", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("refresh = %d: %s", w.Code, w.Body.String())
	}
	if len(posts) != 0 {
		t.Errorf("refresh sent %v; it must not create or bind a broadcast", posts)
	}
}

// DELETE /destinations/{id} removes a never-aired broadcast polyemesis created (so a delete made while the daemon was
// down is covered), and never an adopted event.
func TestDeletingADestinationOverHTTPDeletesItsNeverAiredCreatedBroadcastOnly(t *testing.T) {
	var deletes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/liveBroadcasts":
			deletes = append(deletes, r.URL.Query().Get("id"))
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/liveBroadcasts":
			_, _ = io.WriteString(w, `{"items":[`+ypeEvent(r.URL.Query().Get("id"), "created", "stream-A", true)+`]}`)
		case r.URL.Path == "/liveStreams":
			_, _ = io.WriteString(w, ypeStreams)
		default:
			_, _ = io.WriteString(w, `{"items":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	addSource(t, h, sign, "main")
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	mk := func(name, id string, created bool) int64 {
		d := ytpDest(t, store, acct, name, ytpKeyA, "")
		if _, err := store.UpdateLifecycle(d.ID, func(x *db.Destination) bool {
			x.Lifecycle = db.BroadcastControl{BroadcastID: id, Phase: "created", Opened: true, Created: created}
			return true
		}); err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	own := mk("own", "bcast-own", true)
	picked := mk("picked", "ev-picked", false)
	// The coordinator is tracking the created broadcast, as it does once it has opened one.
	ownRow, _ := store.GetDestination(own)
	s.lifecycle.trackAs(ownRow, "bcast-own", "created")
	for _, id := range []int64{own, picked} {
		r := jsonRequest(t, http.MethodDelete, "/api/v1/destinations/"+itoa(id), nil)
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusOK {
			t.Fatalf("delete %d = %d: %s", id, w.Code, w.Body.String())
		}
	}
	if len(deletes) != 1 || deletes[0] != "bcast-own" {
		t.Errorf("YouTube deletes = %v, want only the broadcast polyemesis created", deletes)
	}
	s.lifecycle.mu.Lock()
	_, stillTracked := s.lifecycle.tracked[own]
	s.lifecycle.mu.Unlock()
	if stillTracked {
		t.Error("the coordinator still tracks the broadcast the DELETE removed, and would read it for ever")
	}
}

// A delete YouTube refuses is reported to the operator in the response, with the broadcast id, and the destination is
// still deleted.
func TestDeletingADestinationSaysWhenItsNeverAiredBroadcastCouldNotBeDeleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/liveBroadcasts":
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/liveBroadcasts":
			_, _ = io.WriteString(w, `{"items":[`+ypeEvent(r.URL.Query().Get("id"), "created", "stream-A", true)+`]}`)
		default:
			_, _ = io.WriteString(w, `{"items":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	addSource(t, h, sign, "main")
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	d := ytpDest(t, store, acct, "own", ytpKeyA, "")
	if _, err := store.UpdateLifecycle(d.ID, func(x *db.Destination) bool {
		x.Lifecycle = db.BroadcastControl{BroadcastID: "bcast-own", Phase: "created", Opened: true, Created: true}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	r := jsonRequest(t, http.MethodDelete, "/api/v1/destinations/"+itoa(d.ID), nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "could not be deleted") ||
		!strings.Contains(w.Body.String(), "bcast-own") || strings.Contains(w.Body.String(), ytpKeyA) {
		t.Errorf("delete = %d %s, want 200 naming the broadcast that is left, and no key", w.Code, w.Body.String())
	}
	if _, err := store.GetDestination(d.ID); err == nil {
		t.Error("the destination was not deleted")
	}
}
