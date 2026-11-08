package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The literal keys planted in the fake liveStreams list. The test fails if either reaches the response.
const (
	ytpKeyA = "aaaa-bbbb-cccc-dddd-0001"
	ytpKeyB = "eeee-ffff-gggg-hhhh-0002"
)

const ytpStreams = `{"items":[
 {"id":"stream-A","snippet":{"title":"Main"},"cdn":{"ingestionType":"rtmp","resolution":"1080p","frameRate":"30fps",
  "ingestionInfo":{"streamName":"` + ytpKeyA + `","ingestionAddress":"rtmp://a.rtmp.youtube.com/live2","backupIngestionAddress":"rtmp://b.rtmp.youtube.com/live2"}},
  "status":{"streamStatus":"active","healthStatus":{"status":"good"}},"contentDetails":{"isReusable":true}},
 {"id":"stream-B","snippet":{"title":"Second"},"cdn":{"ingestionType":"rtmp","resolution":"variable",
  "ingestionInfo":{"streamName":"` + ytpKeyB + `","ingestionAddress":"rtmp://a.rtmp.youtube.com/live2"}},
  "status":{"streamStatus":"inactive","healthStatus":{"status":"noData"}}},
 {"id":"stream-once","snippet":{"title":"One-off"},"cdn":{"ingestionType":"rtmp","ingestionInfo":{"streamName":"once-key"}},
  "contentDetails":{"isReusable":false}}]}`

func ytpFixture(t *testing.T) (*Server, http.Handler, *db.DB, func(*http.Request), int64) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/liveStreams" {
			if !strings.Contains(r.URL.Query().Get("part"), "status") {
				t.Errorf("liveStreams.list part = %q, want it to name status (the picker shows health)", r.URL.Query().Get("part"))
			}
			_, _ = io.WriteString(w, ytpStreams)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	s, h, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	sign := login(t, h)
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	return s, h, store, sign, acct
}

func ytpDest(t *testing.T, store *db.DB, acct int64, name, key, pin string) *db.Destination {
	t.Helper()
	row := &db.Destination{Name: name, Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.rtmp.youtube.com/live2", StreamKey: key, AccountID: &acct}
	if pin != "" {
		row.Compliance.YouTube = &db.YouTubeBroadcastControls{StreamID: pin}
	}
	d, err := store.CreateDestination(row)
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	return d
}

// THE PICKER NEVER RETURNS A STREAM KEY OR AN INGEST ADDRESS. The keys are planted verbatim in the fake list, and
// the response must carry neither them nor the field names that hold them.
func TestTheYouTubeStreamPickerNeverReturnsAKeyOrAnIngestAddress(t *testing.T) {
	_, h, store, sign, acct := ytpFixture(t)
	d := ytpDest(t, store, acct, "main", ytpKeyA, "")
	ytpDest(t, store, acct, "second", ytpKeyB, "")

	r := jsonRequest(t, http.MethodGet, "/api/v1/platforms/accounts/"+strconv.FormatInt(acct, 10)+
		"/youtube/streams?destinationId="+strconv.FormatInt(d.ID, 10), nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, leak := range []string{ytpKeyA, ytpKeyB, "once-key", "streamName", "ingestionAddress",
		"backupIngestionAddress", "rtmp://", "ingestionInfo"} {
		if strings.Contains(body, leak) {
			t.Errorf("the picker response carries %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, `"id":"stream-A"`) || !strings.Contains(body, `"health":"good"`) ||
		!strings.Contains(body, `"resolution":"1080p"`) {
		t.Errorf("the response lost the fields the picker shows: %s", body)
	}
	if strings.Contains(body, "stream-once") {
		t.Errorf("a stream YouTube marks not reusable was listed: %s", body)
	}
}

// The destination being edited sees its own stream marked current, and another destination on the same stream is
// reported by name so the dialog can warn.
func TestTheYouTubeStreamPickerMarksTheCurrentStreamAndWhoElseUsesOne(t *testing.T) {
	_, h, store, sign, acct := ytpFixture(t)
	d := ytpDest(t, store, acct, "main", ytpKeyA, "")
	other := ytpDest(t, store, acct, "backup show", "unlisted-key", "stream-A") // pinned to A

	r := jsonRequest(t, http.MethodGet, "/api/v1/platforms/accounts/"+strconv.FormatInt(acct, 10)+
		"/youtube/streams?destinationId="+strconv.FormatInt(d.ID, 10), nil)
	sign(r)
	w := do(t, h, r)
	var got struct {
		Streams []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
			UsedBy  []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"usedBy"`
		} `json:"streams"`
	}
	mustDecode(t, w.Body.Bytes(), &got)
	if len(got.Streams) != 2 {
		t.Fatalf("streams = %+v", got.Streams)
	}
	a := got.Streams[0]
	if a.ID != "stream-A" || !a.Current {
		t.Errorf("stream A should be current for the destination holding its key: %+v", a)
	}
	if len(a.UsedBy) != 1 || a.UsedBy[0].ID != other.ID || a.UsedBy[0].Name != "backup show" {
		t.Errorf("stream A should name the destination pinned to it: %+v", a.UsedBy)
	}
	if got.Streams[1].Current || len(got.Streams[1].UsedBy) != 0 {
		t.Errorf("stream B is nobody's: %+v", got.Streams[1])
	}
}

// A read-scope token is refused: the route calls YouTube.
func TestTheYouTubeStreamPickerIsDeniedToAReadToken(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	_ = s
	r := jsonRequest(t, http.MethodGet, "/api/v1/platforms/accounts/1/youtube/streams", nil)
	if w := do(t, h, r); w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Errorf("unauthenticated status = %d, want a refusal", w.Code)
	}
}

// proveYouTubeStreams discharges GET /platforms/accounts/{id}/youtube/streams in the route ledger. The admin's
// destination list shows the stored key, which is the positive control; the picker must not carry it to anyone.
func proveYouTubeStreams(t *testing.T) proofResult {
	_, h, store, sign, acct := ytpFixture(t)
	ytpDest(t, store, acct, "main", ytpKeyA, "")
	read := createScopedToken(t, h, sign, "ledger-youtube-streams", db.ScopeRead)
	path := "/api/v1/platforms/accounts/" + itoa(acct) + "/youtube/streams"

	code, admin := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK {
		t.Fatalf("the admin's stream list answered %d: %s", code, truncateForFailure(admin))
	}
	if strings.Contains(admin, ytpKeyA) {
		t.Errorf("the stream list carries a stream key even to the admin: %s", truncateForFailure(admin))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's stream list answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, path),
		High:      bodyOf(t, h, sign, "/api/v1/destinations"),
		Read:      low,
		Sentinels: []string{ytpKeyA},
	}
}
