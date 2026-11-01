package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// YouTube paired vertical (#7), HTTP layer.
//
// PROVISIONAL. The API cannot pair the two halves, so this layer creates the vertical
// stream through the API, records the pair in polyemesis, and reads the vertical half's
// health back by stream id. The one-time Live Control Room step, where the operator
// selects this stream's key as the vertical "second stream key", stays a human action.
// The confirmation endpoint records that the step was done. It does not verify it.
//
// THE PAIR IS ONE-TO-ONE AND THE TWO HALVES ARE SEPARATE ROWS. Each runs in its own
// FFmpeg process, so a failure in one does not stop the other. The vertical row has no
// account link, so the horizontal-only paths (refresh, lifecycle, schedule) skip it, and
// the horizontal broadcast never binds to the vertical stream.

// youtubeVerticalProvider is what the vertical path needs from the YouTube provider. It is
// an interface rather than a concrete type so a provider without it refuses cleanly.
type youtubeVerticalProvider interface {
	CreateVerticalStream(ctx context.Context, accessToken, label string) (*oauth.VerticalStream, error)
	VerticalStreamHealth(ctx context.Context, accessToken, streamID string) (*oauth.VerticalStreamHealth, error)
}

// verticalPairView is the vertical half as the API serves it. It carries no stream key.
type verticalPairView struct {
	HorizontalID     int64  `json:"horizontalId"`
	VerticalID       int64  `json:"verticalId"`
	VerticalStreamID string `json:"verticalStreamId"`
	PairingConfirmed bool   `json:"pairingConfirmed"`
	StreamStatus     string `json:"streamStatus,omitempty"`
	HealthStatus     string `json:"healthStatus,omitempty"`
	HealthError      string `json:"healthError,omitempty"`
}

// verticalPairsInFlight holds the horizontal destinations whose pairing is under way. The zero value is ready. The
// claim is taken before the existing-pair check, so the check and the create are one step: a second request for the
// same destination is refused before it can reach YouTube, and a request that starts after the first has finished
// sees the pair the first one stored.
type verticalPairsInFlight struct {
	mu  sync.Mutex
	ids map[int64]struct{}
}

// begin claims a destination for one pairing. It reports false when a pairing for it is already under way.
func (f *verticalPairsInFlight) begin(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, busy := f.ids[id]; busy {
		return false
	}
	if f.ids == nil {
		f.ids = map[int64]struct{}{}
	}
	f.ids[id] = struct{}{}
	return true
}

// end releases a claim taken by begin.
func (f *verticalPairsInFlight) end(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.ids, id)
}

// handleCreateYouTubeVertical pairs a horizontal YouTube destination with a new vertical
// one. The body names a tier from db.YouTubeVerticalTiers.
func (s *Server) handleCreateYouTubeVertical(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	dest, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if dest.Platform != db.PlatformYouTube {
		writeError(w, http.StatusBadRequest, "only a YouTube destination has a vertical half")
		return
	}
	if dest.AccountID == nil {
		writeError(w, http.StatusBadRequest,
			"connect the YouTube account first; the vertical stream is created through it")
		return
	}
	var req struct {
		Tier string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	tier, ok := findYouTubeVerticalTier(req.Tier)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown vertical tier; choose one of the portrait tiers")
		return
	}
	if !s.verticalPairs.begin(id) {
		writeError(w, http.StatusConflict, "a vertical half is already being created for this destination")
		return
	}
	defer s.verticalPairs.end(id)
	if _, err := s.store.GetYouTubeVerticalPairByHorizontal(id); err == nil {
		writeError(w, http.StatusConflict, "this destination already has a vertical half")
		return
	} else if !errors.Is(err, db.ErrNoYouTubeVerticalPair) {
		writeError(w, http.StatusInternalServerError, "could not read the vertical pair")
		return
	}
	if s.isVerticalHalf(id) {
		writeError(w, http.StatusBadRequest, "this destination is already the vertical half of a pair")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	acct, vp, err := s.verticalProviderFor(ctx, *dest.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, alerts.Redact(err.Error()))
		return
	}
	rendID, err := s.verticalRenditionFor(dest, tier)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not prepare the vertical rendition")
		return
	}
	vs, err := vp.CreateVerticalStream(ctx, acct.AccessToken, dest.Name)
	if err != nil {
		writeError(w, http.StatusBadGateway, alerts.Redact(err.Error()))
		return
	}
	row := &db.Destination{
		Name:         dest.Name + " (vertical)",
		Kind:         db.DestRTMP,
		Platform:     db.PlatformYouTube,
		URL:          vs.Ingest.URL,
		StreamKey:    vs.Ingest.Key,
		RenditionID:  &rendID,
		SourceID:     dest.SourceID,
		Enabled:      dest.Enabled,
		AudioBitrate: dest.AudioBitrate,
		Profile:      dest.Profile,
	}
	// Every route that stores a destination repairs the row first, so a setting the platform cannot send is dropped
	// before the write and not sent later. The vertical row copies settings from the horizontal one, so it gets the same repair.
	if _, err := s.dropUnsendableSettings(row); err != nil {
		writeError(w, http.StatusInternalServerError, "could not prepare the vertical destination")
		return
	}
	vdest, err := s.store.CreateDestination(row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save the vertical destination")
		return
	}
	if err := s.store.CreateYouTubeVerticalPair(id, vdest.ID, vs.ID); err != nil {
		// Roll back the row so a failed pair does not leave an orphan destination. The
		// YouTube stream stays on the channel: polyemesis does not delete streams it cannot
		// prove are its own, and the operator can remove it in Studio.
		_ = s.store.DeleteDestination(vdest.ID)
		writeError(w, http.StatusInternalServerError, "could not record the vertical pair")
		return
	}
	writeJSON(w, http.StatusOK, verticalPairView{
		HorizontalID: id, VerticalID: vdest.ID, VerticalStreamID: vs.ID,
	})
}

// handleYouTubeVerticalStatus reads the vertical half's health by stream id. A failure to
// read it is reported on the sub-row and does not change the horizontal half.
func (s *Server) handleYouTubeVerticalStatus(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	pair, err := s.store.GetYouTubeVerticalPairByHorizontal(id)
	if errors.Is(err, db.ErrNoYouTubeVerticalPair) {
		writeError(w, http.StatusNotFound, "this destination has no vertical half")
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	view := verticalPairView{
		HorizontalID: pair.HorizontalID, VerticalID: pair.VerticalID,
		VerticalStreamID: pair.VerticalStreamID, PairingConfirmed: pair.PairingConfirmedAt != 0,
	}
	dest, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if dest.AccountID == nil {
		view.HealthError = "connect the YouTube account to read the vertical half's status"
		writeJSON(w, http.StatusOK, view)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	acct, vp, err := s.verticalProviderFor(ctx, *dest.AccountID)
	if err != nil {
		view.HealthError = alerts.Redact(err.Error())
		writeJSON(w, http.StatusOK, view)
		return
	}
	health, err := vp.VerticalStreamHealth(ctx, acct.AccessToken, pair.VerticalStreamID)
	if err != nil {
		view.HealthError = alerts.Redact(err.Error())
		writeJSON(w, http.StatusOK, view)
		return
	}
	view.StreamStatus = health.StreamStatus
	view.HealthStatus = health.HealthStatus
	writeJSON(w, http.StatusOK, view)
}

// handleConfirmYouTubeVertical records that the operator completed the one-time Live
// Control Room pairing. It stores the fact and does not verify it.
func (s *Server) handleConfirmYouTubeVertical(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.ConfirmYouTubeVerticalPairing(id, time.Now()); err != nil {
		if errors.Is(err, db.ErrNoYouTubeVerticalPair) {
			writeError(w, http.StatusNotFound, "this destination has no vertical half")
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairingConfirmed": true})
}

// verticalProviderFor resolves the account's token and the YouTube provider as the vertical
// path needs them, and refuses a provider that cannot create a vertical stream.
func (s *Server) verticalProviderFor(ctx context.Context, accountID int64) (*db.PlatformAccount, youtubeVerticalProvider, error) {
	acct, err := s.tokenFor(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	provider, err := s.providers.Get(acct.Platform)
	if err != nil {
		return nil, nil, err
	}
	vp, ok := provider.(youtubeVerticalProvider)
	if !ok {
		return nil, nil, errors.New("this build cannot create a YouTube vertical stream")
	}
	return acct, vp, nil
}

// verticalRenditionFor finds or creates the portrait rendition a tier names, under the
// destination's programme. A rendition is shared by every destination that picks the same
// shape, so the function reuses a match rather than creating a copy each time.
func (s *Server) verticalRenditionFor(dest *db.Destination, tier db.RenditionPreset) (int64, error) {
	if dest.SourceID == nil {
		return 0, errors.New("destination has no programme")
	}
	existing, err := s.store.ListRenditionsBySource(*dest.SourceID)
	if err != nil {
		return 0, err
	}
	want := tier.Rendition
	for _, r := range existing {
		if r.Width == want.Width && r.Height == want.Height && r.FPS == want.FPS &&
			r.AspectMode == want.AspectMode && r.Encoder == want.Encoder {
			return r.ID, nil
		}
	}
	dup := *want
	dup.SourceID = dest.SourceID
	created, err := s.store.CreateRendition(&dup)
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

// isVerticalHalf reports whether a destination is already the vertical half of some pair.
func (s *Server) isVerticalHalf(destID int64) bool {
	pairs, err := s.store.ListYouTubeVerticalPairs()
	if err != nil {
		return false
	}
	for _, p := range pairs {
		if p.VerticalID == destID {
			return true
		}
	}
	return false
}

// verticalStreamIDs lists every vertical stream id the fallback must not adopt. A store
// failure returns nil. The title marker still keeps those streams out of the fallback.
func (s *Server) verticalStreamIDs() []string {
	pairs, err := s.store.ListYouTubeVerticalPairs()
	if err != nil {
		return nil
	}
	var ids []string
	for _, p := range pairs {
		if strings.TrimSpace(p.VerticalStreamID) != "" {
			ids = append(ids, p.VerticalStreamID)
		}
	}
	return ids
}

// findYouTubeVerticalTier looks a tier key up in the portrait list.
func findYouTubeVerticalTier(key string) (db.RenditionPreset, bool) {
	for _, t := range db.YouTubeVerticalTiers() {
		if t.Key == key {
			return t, true
		}
	}
	return db.RenditionPreset{}, false
}
