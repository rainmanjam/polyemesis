package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/redact"
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
	// Live says whether either half is running. The health fields are only read while it is true.
	Live         bool   `json:"live"`
	StreamStatus string `json:"streamStatus,omitempty"`
	HealthStatus string `json:"healthStatus,omitempty"`
	HealthError  string `json:"healthError,omitempty"`
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
		writeError(w, http.StatusBadRequest, errInvalidID)
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
	// decodeJSON, like every other body here: a 1 MiB cap, and an unknown field is refused rather than ignored.
	if !decodeJSON(w, r, &req) {
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
	// A failed read is not "no": the guard fails closed, or a transient error would let a vertical half get a
	// vertical half of its own.
	if half, err := s.isVerticalHalf(id); err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the vertical pairs")
		return
	} else if half {
		writeError(w, http.StatusBadRequest, "this destination is already the vertical half of a pair")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	acct, vp, err := s.verticalProviderFor(ctx, *dest.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, redact.Redact(err.Error()))
		return
	}
	rendID, rendCreated, err := s.verticalRenditionFor(dest, tier)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not prepare the vertical rendition")
		return
	}
	// A rendition this request made is removed again if the pair is not made, so a failed attempt does not leave an
	// unused portrait rendition on the programme. A rendition that was reused belongs to whoever made it and stays.
	paired := false
	defer func() {
		if !paired && rendCreated {
			s.dropUnusedVerticalRendition(rendID)
		}
	}()
	vs, err := vp.CreateVerticalStream(ctx, acct.AccessToken, dest.Name)
	if err != nil {
		writeError(w, http.StatusBadGateway, redact.Redact(err.Error()))
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
	paired = true
	// A new destination and, often, a new rendition: the pipeline is reconciled as every other destination create
	// does, so an enabled pair starts now rather than at whatever save comes next. The vertical row is removed through
	// DELETE /destinations/{id}, which reconciles already.
	rw := s.reconcileNow("the vertical half")
	writeMutation(w, http.StatusOK, rw, verticalPairView{
		HorizontalID: id, VerticalID: vdest.ID, VerticalStreamID: vs.ID,
	})
}

// verticalHealthMinInterval is the least time between two YouTube reads of one pair's vertical
// half (#7, correction 6 of the YouTube truth sheet). It is a floor on the reads that reach
// Google, not on the responses: a request inside the window is answered from the last read.
const verticalHealthMinInterval = 60 * time.Second

// verticalHealthRead is one YouTube read of a vertical half, kept so a request inside the window
// can be answered without a call. The error is kept with the health: a failed read is paced the
// same as a good one, otherwise a dead stream would be retried on every page load.
type verticalHealthRead struct {
	at     time.Time
	health *oauth.VerticalStreamHealth
	err    string
}

// verticalHealthKey names one vertical stream of one pair. The stream id is part of it, so a pair re-made on a new
// stream is never answered with the old stream's health.
type verticalHealthKey struct {
	horizontalID int64
	streamID     string
}

// verticalHealthPacer holds the last read per vertical stream, and one lock per stream so concurrent requests make one
// call between them. The zero value is ready.
type verticalHealthPacer struct {
	mu    sync.Mutex
	last  map[verticalHealthKey]verticalHealthRead
	locks map[verticalHealthKey]*sync.Mutex
}

// recent returns the last read for a stream when it is still inside the window.
func (p *verticalHealthPacer) recent(k verticalHealthKey, now time.Time) (verticalHealthRead, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.last[k]
	if !ok || now.Sub(r.at) >= verticalHealthMinInterval {
		return verticalHealthRead{}, false
	}
	return r, true
}

// remember stores a read so the next request inside the window can reuse it.
func (p *verticalHealthPacer) remember(k verticalHealthKey, r verticalHealthRead) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last = map[verticalHealthKey]verticalHealthRead{}
	}
	p.last[k] = r
}

// flight returns the lock one stream's read is made under. The check against recent and the call that follows are
// one step under it, so five requests arriving together make one YouTube call and four cached answers. The map grows
// by one entry per vertical stream ever read, which is bounded by the pairs an operator makes.
func (p *verticalHealthPacer) flight(k verticalHealthKey) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.locks == nil {
		p.locks = map[verticalHealthKey]*sync.Mutex{}
	}
	l, ok := p.locks[k]
	if !ok {
		l = &sync.Mutex{}
		p.locks[k] = l
	}
	return l
}

// verticalHalfIsLive reports whether any of the given destinations is running. The status read
// is gated on this: a vertical stream is only worth watching while one of its halves is on air.
// The seam is for tests; production asks the engine.
func (s *Server) verticalHalfIsLive(ids ...int64) bool {
	if s.verticalHalfLive != nil {
		return s.verticalHalfLive(ids...)
	}
	if s.mgr == nil {
		return false
	}
	for _, ds := range s.mgr.DestinationStatuses() {
		if !ds.IsUp() {
			continue
		}
		for _, id := range ids {
			if ds.ID == id {
				return true
			}
		}
	}
	return false
}

// verticalChargeOK spends one unit of the install's YouTube allowance for a read made on the
// operator's behalf -- the same allowance the chat poll paces against -- and reports whether it
// fitted above the send reserve. Always charged, chat or no chat (PR #950 review, #1).
func (s *Server) verticalChargeOK() bool {
	if s.verticalChargeQuota != nil {
		return s.verticalChargeQuota(chat.QuotaCostListLiveStreams)
	}
	return s.ytQuota().Charge(chat.QuotaCostListLiveStreams)
}

// handleYouTubeVerticalStatus reads the vertical half's health by stream id. It is paced and
// gated (#7, correction 6): no YouTube call unless one of the two halves is running, and at most
// one call per pair per verticalHealthMinInterval. A failure to read is reported on the sub-row
// and does not change the horizontal half.
func (s *Server) handleYouTubeVerticalStatus(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidID)
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
	// EITHER HALF COUNTS. A vertical half that is up while its horizontal is down still shows its
	// health, which is the case the sub-row exists for.
	view.Live = s.verticalHalfIsLive(pair.HorizontalID, pair.VerticalID)
	if !view.Live {
		writeJSON(w, http.StatusOK, view)
		return
	}
	key := verticalHealthKey{horizontalID: id, streamID: pair.VerticalStreamID}
	flight := s.verticalHealth.flight(key)
	flight.Lock()
	defer flight.Unlock()
	now := s.clock()
	if prev, ok := s.verticalHealth.recent(key, now); ok {
		applyVerticalRead(&view, prev)
		writeJSON(w, http.StatusOK, view)
		return
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
	// Not tied to the browser. The read is cached for every caller for a minute, so a request abandoned mid-read
	// must not cancel it: a cancelled read would be remembered as a failure and shown to everyone as an error.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Second)
	defer cancel()
	acct, vp, err := s.verticalProviderFor(ctx, *dest.AccountID)
	if err != nil {
		view.HealthError = redact.Redact(err.Error())
		writeJSON(w, http.StatusOK, view)
		return
	}
	if !s.verticalChargeOK() {
		// Refused before the call, so nothing reached YouTube. Not remembered: the next request
		// after the budget frees up should read. The sub-row says why in words, not a bare error.
		view.HealthError = "the YouTube quota is nearly spent; the vertical status is paused until it resets"
		writeJSON(w, http.StatusOK, view)
		return
	}
	// Charged above; the request meter must not count it again.
	health, err := vp.VerticalStreamHealth(oauth.WithQuotaCharged(ctx), acct.AccessToken, pair.VerticalStreamID)
	read := verticalHealthRead{at: now, health: health}
	if err != nil {
		read.err = redact.Redact(err.Error())
		read.health = nil
	}
	s.verticalHealth.remember(key, read)
	applyVerticalRead(&view, read)
	writeJSON(w, http.StatusOK, view)
}

// applyVerticalRead copies a stored read onto the view. Kept in one place so a cached answer and a
// fresh one cannot disagree about which fields they fill.
func applyVerticalRead(view *verticalPairView, r verticalHealthRead) {
	if r.err != "" {
		view.HealthError = r.err
		return
	}
	if r.health != nil {
		view.StreamStatus = r.health.StreamStatus
		view.HealthStatus = r.health.HealthStatus
	}
}

// handleConfirmYouTubeVertical records that the operator completed the one-time Live
// Control Room pairing. It stores the fact and does not verify it.
func (s *Server) handleConfirmYouTubeVertical(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidID)
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
// shape, so the function reuses a match rather than creating a copy each time. created says
// whether this call made the rendition, which is the only case a failed pairing may remove it.
func (s *Server) verticalRenditionFor(dest *db.Destination, tier db.RenditionPreset) (id int64, created bool, err error) {
	if dest.SourceID == nil {
		return 0, false, errors.New("destination has no programme")
	}
	existing, err := s.store.ListRenditionsBySource(*dest.SourceID)
	if err != nil {
		return 0, false, err
	}
	want := tier.Rendition
	for _, r := range existing {
		if sameVerticalEncode(*r, *want) {
			return r.ID, false, nil
		}
	}
	dup := *want
	dup.SourceID = dest.SourceID
	made, err := s.store.CreateRendition(&dup)
	if err != nil {
		return 0, false, err
	}
	return made.ID, true, nil
}

// dropUnusedVerticalRendition removes a portrait rendition a failed pairing made. It is removed only while no
// destination selects it: a pairing on another destination of the same programme can pick the same shape in the
// meantime, and its rendition must not be taken from under it.
func (s *Server) dropUnusedVerticalRendition(id int64) {
	dests, err := s.store.ListDestinations()
	if err != nil {
		return
	}
	for _, d := range dests {
		if d.RenditionID != nil && *d.RenditionID == id {
			return
		}
	}
	if err := s.store.DeleteRendition(id); err != nil && !errors.Is(err, db.ErrNotFound) {
		s.log.Warn("youtube vertical: could not remove the rendition a failed pairing made", "rendition", id, "err", err)
	}
}

// sameVerticalEncode reports whether a stored rendition encodes exactly as the tier would, so reusing it changes
// nothing for the destinations already on it. Shape alone is not enough: a portrait rendition the operator made at
// another bitrate, preset or keyframe interval would otherwise be shared, and the vertical half would send what that
// operator chose and not what the tier says. Overlay and text are compared too, so a branded tier is not borrowed.
func sameVerticalEncode(r, want db.Rendition) bool {
	return r.Width == want.Width && r.Height == want.Height && r.FPS == want.FPS &&
		r.AspectMode == want.AspectMode && r.Encoder == want.Encoder &&
		r.VideoBitrate == want.VideoBitrate && r.MaxrateKbps == want.MaxrateKbps &&
		r.BufsizeKbps == want.BufsizeKbps && r.Preset == want.Preset && r.GOPSeconds == want.GOPSeconds &&
		r.Deinterlace == want.Deinterlace && r.PadColor == want.PadColor &&
		r.Overlay == want.Overlay && r.Text == want.Text
}

// isVerticalHalf reports whether a destination is already the vertical half of some pair. A
// store error is returned, not read as "no", so the caller's guard fails closed.
func (s *Server) isVerticalHalf(destID int64) (bool, error) {
	pairs, err := s.store.ListYouTubeVerticalPairs()
	if err != nil {
		return false, err
	}
	for _, p := range pairs {
		if p.VerticalID == destID {
			return true, nil
		}
	}
	return false, nil
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

// linkVerticalPairs fills the pair fields on the status rows (#7). A horizontal row gets the
// vertical half's id and the confirmation; the vertical row gets the horizontal it belongs to.
// A store failure leaves the rows unlinked, which the UI reads as "no pair shown", never as a
// wrong pair. Costs no YouTube call: the pair table is local.
func (s *Server) linkVerticalPairs(dests []engine.DestStatus) []engine.DestStatus {
	pairs, err := s.store.ListYouTubeVerticalPairs()
	if err != nil || len(pairs) == 0 {
		return dests
	}
	byHorizontal := make(map[int64]db.YouTubeVerticalPair, len(pairs))
	for _, p := range pairs {
		byHorizontal[p.HorizontalID] = p
	}
	for i := range dests {
		if p, ok := byHorizontal[dests[i].ID]; ok {
			dests[i].YouTubeVertical = &engine.YouTubeVerticalLink{
				VerticalID: p.VerticalID, PairingConfirmed: p.PairingConfirmedAt != 0,
			}
		}
	}
	for i := range dests {
		for _, p := range pairs {
			if p.VerticalID == dests[i].ID {
				h := p.HorizontalID
				dests[i].VerticalHalfOf = &h
				break
			}
		}
	}
	return dests
}
