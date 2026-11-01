package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The planned-rollover HTTP surface (#19), driven through the real router with a real session. The coordinator
// tests in rollover_test.go prove what a sweep does; these prove what the countdown card is told, and that a save
// the planner would reject never reaches the store. A card that shows a cutoff for a platform with no honest one,
// or a save that stores a quiet window with no zone, is the failure these exist to catch.

// rolloverHTTP is a logged-in server with one YouTube and one Kick destination.
type rolloverHTTP struct {
	s       *Server
	h       http.Handler
	sign    func(*http.Request)
	store   *db.DB
	youtube *db.Destination
	kick    *db.Destination
}

func newRolloverHTTP(t *testing.T) *rolloverHTTP {
	t.Helper()
	s, h, store, sign := engineServer(t, defaultTools(), Options{Config: config.Config{}})
	yt := seedDestination(t, s, store, db.PlatformYouTube, "yt")
	kick := seedDestination(t, s, store, db.PlatformKick, "kick")
	return &rolloverHTTP{s: s, h: h, sign: sign, store: store, youtube: yt, kick: kick}
}

func rolloverPath(id int64) string {
	return "/api/v1/destinations/" + strconv.FormatInt(id, 10) + "/rollover"
}

// The limits table is the one the planner counts to, in its display order, each with how well it is established.
// Kick's is unverified with no number, and is served as such rather than dropped, so the card can say why it has no
// countdown.
func TestTheRolloverLimitsRouteServesTheCapsWithHowWellEachIsEstablished(t *testing.T) {
	f := newRolloverHTTP(t)
	var got []rolloverLimitView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, "/api/v1/rollover/limits", nil, http.StatusOK), &got)
	byPreset := map[string]rolloverLimitView{}
	for _, l := range got {
		byPreset[l.PresetID] = l
	}
	if yt := byPreset["youtube"]; yt.Hours != 12 || yt.Kind != string(oauth.LimitKindArchiveDuration) || yt.Status != string(oauth.LimitDocumented) {
		t.Fatalf("youtube limit = %+v, want the 12-hour archive cap", yt)
	}
	if kick, ok := byPreset["kick"]; !ok || kick.Hours != 0 || kick.Status != string(oauth.LimitUnverified) {
		t.Fatalf("kick limit = %+v (present %v), want it served as unverified with no number", kick, ok)
	}
	if len(got) != 4 {
		t.Fatalf("served %d limits, want the table's 4", len(got))
	}
}

// A YouTube destination with a recorded session start gets its cutoff twelve hours on; one that is not delivering
// says so in a sentence instead of a countdown, and no due flag is set on it. The action is "automatic" because the
// server's coordinator acts on YouTube.
func TestTheRolloverCardCountsAYouTubeSessionToItsArchiveCapAndSaysWhyItIsNotRunning(t *testing.T) {
	f := newRolloverHTTP(t)
	started := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	if err := f.store.SetRolloverSession(f.youtube.ID, "bc-1", started, time.Now()); err != nil {
		t.Fatalf("SetRolloverSession: %v", err)
	}

	var v rolloverView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, rolloverPath(f.youtube.ID), nil, http.StatusOK), &v)
	if v.Platform != "youtube" || v.Action != "automatic" {
		t.Fatalf("platform/action = %q/%q, want youtube/automatic", v.Platform, v.Action)
	}
	if v.Limit == nil || v.Limit.Hours != 12 {
		t.Fatalf("limit = %+v, want YouTube's 12-hour cap", v.Limit)
	}
	if v.CutoffAt == nil || !v.CutoffAt.Equal(started.Add(12*time.Hour)) {
		t.Fatalf("cutoff = %v, want the session start plus 12 hours (%v)", v.CutoffAt, started.Add(12*time.Hour))
	}
	if v.Delivering || v.Due || v.NextAt != nil {
		t.Fatalf("an idle destination was given a running countdown: %+v", v)
	}
	if v.ReasonIfNone == "" {
		t.Fatal("an idle destination's card carried no reason for having no countdown")
	}
}

// Kick's cap is unverified, so its card names the cap but counts to nothing, and its action is a countdown only:
// the card must not imply an action that will not happen.
func TestAKickRolloverCardShowsItsUnverifiedCapWithNoCutoffAndNoAction(t *testing.T) {
	f := newRolloverHTTP(t)
	if err := f.store.SetRolloverSession(f.kick.ID, "", time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatalf("SetRolloverSession: %v", err)
	}
	var v rolloverView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, rolloverPath(f.kick.ID), nil, http.StatusOK), &v)
	if v.Action != "countdown" {
		t.Fatalf("kick action = %q, want countdown", v.Action)
	}
	if v.Limit == nil || v.Limit.Status != string(oauth.LimitUnverified) {
		t.Fatalf("kick limit = %+v, want the unverified entry", v.Limit)
	}
	if v.CutoffAt != nil {
		t.Fatalf("kick card counted to %v from an unverified cap", v.CutoffAt)
	}
}

// A save is validated by the planner's own rules before it is stored, and a refused save leaves the stored settings
// as they were. A good save is answered with the view as it now stands.
func TestARolloverSaveIsValidatedBeforeItIsStoredAndAnsweredWithTheNewView(t *testing.T) {
	f := newRolloverHTTP(t)
	path := rolloverPath(f.youtube.ID)

	refused := []struct {
		name string
		body map[string]any
	}{
		{"margin under the floor", map[string]any{"enabled": true, "marginMinutes": 1}},
		{"quiet window with no zone", map[string]any{"enabled": true, "marginMinutes": 30, "quietStart": "01:00", "quietEnd": "03:00"}},
		{"quiet window in an unknown zone", map[string]any{"enabled": true, "marginMinutes": 30, "quietStart": "01:00", "quietEnd": "03:00", "quietZone": "Mars/Olympus"}},
		{"scene with a control character", map[string]any{"enabled": true, "marginMinutes": 30, "obsScene": "a\nb"}},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			raw := send(t, f.h, f.sign, http.MethodPut, path, tc.body, http.StatusBadRequest)
			if !strings.Contains(string(raw), "error") {
				t.Fatalf("refusal carried no sentence: %s", raw)
			}
			got, err := f.store.GetRollover(f.youtube.ID)
			if err != nil {
				t.Fatalf("GetRollover: %v", err)
			}
			if got.Enabled {
				t.Fatalf("a refused save was stored: %+v", got)
			}
		})
	}

	var v rolloverView
	decodeInto(t, send(t, f.h, f.sign, http.MethodPut, path, map[string]any{
		"enabled": true, "marginMinutes": 45, "quietStart": "01:00", "quietEnd": "03:00",
		"quietZone": "Europe/London", "obsScene": "Be right back", "chatNotice": true,
	}, http.StatusOK), &v)
	if !v.Settings.Enabled || v.Settings.MarginMinutes != 45 || v.Settings.QuietZone != "Europe/London" ||
		v.Settings.OBSScene != "Be right back" || !v.Settings.ChatNotice {
		t.Fatalf("the save's answer did not carry the stored settings: %+v", v.Settings)
	}
	stored, err := f.store.GetRollover(f.youtube.ID)
	if err != nil || !stored.Enabled || stored.MarginMinutes != 45 {
		t.Fatalf("stored = %+v (%v), want the saved settings", stored, err)
	}
}

// A destination that does not exist is not found on every rollover route, and a malformed id is a bad request.
func TestTheRolloverRoutesRefuseAnUnknownOrMalformedDestination(t *testing.T) {
	f := newRolloverHTTP(t)
	send(t, f.h, f.sign, http.MethodGet, rolloverPath(99999), nil, http.StatusNotFound)
	send(t, f.h, f.sign, http.MethodPut, rolloverPath(99999), map[string]any{"marginMinutes": 30}, http.StatusNotFound)
	send(t, f.h, f.sign, http.MethodPost, rolloverPath(99999)+"/run", nil, http.StatusNotFound)
	send(t, f.h, f.sign, http.MethodGet, "/api/v1/destinations/x/rollover", nil, http.StatusBadRequest)
}

// A manual run on a destination that is not delivering is refused as a conflict with the coordinator's reason.
func TestAManualRolloverRunOverHTTPOnAnIdleDestinationIsAConflict(t *testing.T) {
	f := newRolloverHTTP(t)
	raw := send(t, f.h, f.sign, http.MethodPost, rolloverPath(f.youtube.ID)+"/run", nil, http.StatusConflict)
	if !strings.Contains(string(raw), "error") {
		t.Fatalf("refusal carried no sentence: %s", raw)
	}
}

// A server without a coordinator says so rather than pretending to roll anything over.
func TestAManualRolloverRunOnAServerWithNoCoordinatorSaysItIsNotRunning(t *testing.T) {
	f := newRolloverHTTP(t)
	f.s.rollover = nil
	raw := send(t, f.h, f.sign, http.MethodPost, rolloverPath(f.youtube.ID)+"/run", nil, http.StatusConflict)
	if !strings.Contains(string(raw), "not running") {
		t.Fatalf("refusal = %s, want it to say the rollover is not running here", raw)
	}
	// The card still reads: the planner is built over the stored settings, and YouTube is then a countdown only,
	// because nothing on this server would act on it.
	var v rolloverView
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, rolloverPath(f.youtube.ID), nil, http.StatusOK), &v)
	if v.Action != "countdown" {
		t.Fatalf("action with no coordinator = %q, want countdown", v.Action)
	}
}

// noticeAdapter is a chat adapter that records every line it is asked to post, and fails when told to.
type noticeAdapter struct {
	platform db.Platform
	account  string
	fail     error

	mu   sync.Mutex
	sent []string
}

func (a *noticeAdapter) Platform() db.Platform { return a.platform }
func (a *noticeAdapter) Account() string       { return a.account }
func (a *noticeAdapter) Run(ctx context.Context, _ chat.Sink) error {
	<-ctx.Done()
	return nil
}

func (a *noticeAdapter) Send(_ context.Context, text string) (chat.Message, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail != nil {
		return chat.Message{}, a.fail
	}
	a.sent = append(a.sent, text)
	return chat.Message{}, nil
}

func (a *noticeAdapter) lines() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.sent...)
}

// The rollover notice goes to the one chat the destination's own account runs, and to no other chat: not the
// same platform's other account, and not another platform.
func TestTheRolloverNoticeIsPostedOnlyToTheDestinationsOwnChat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := chat.New()
	t.Cleanup(hub.Close)
	mine := &noticeAdapter{platform: db.PlatformYouTube, account: "mine"}
	other := &noticeAdapter{platform: db.PlatformYouTube, account: "other"}
	twitch := &noticeAdapter{platform: db.PlatformTwitch, account: "mine"}
	for _, a := range []*noticeAdapter{mine, other, twitch} {
		if err := hub.Attach(ctx, a); err != nil {
			t.Fatalf("Attach: %v", err)
		}
	}
	s := &Server{chat: hub}

	if err := s.sendRolloverNotice(ctx, db.PlatformYouTube, "mine", "We are switching streams"); err != nil {
		t.Fatalf("sendRolloverNotice: %v", err)
	}
	if got := mine.lines(); len(got) != 1 || got[0] != "We are switching streams" {
		t.Fatalf("own chat got %v, want the one notice", got)
	}
	if len(other.lines()) != 0 || len(twitch.lines()) != 0 {
		t.Fatalf("the notice reached another chat: other=%v twitch=%v", other.lines(), twitch.lines())
	}

	// No chat on the destination's account is an error that names the platform, so the log says why no line came.
	err := s.sendRolloverNotice(ctx, db.PlatformKick, "mine", "x")
	if err == nil || !strings.Contains(err.Error(), "kick") {
		t.Fatalf("no matching chat: err = %v, want one naming kick", err)
	}
}

// A platform's refusal is reported redacted: the detail can carry the access token the adapter sent.
func TestARefusedRolloverNoticeIsReportedWithItsCredentialRedacted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := chat.New()
	t.Cleanup(hub.Close)
	token := "ya29." + strings.Repeat("Q", 40)
	a := &noticeAdapter{platform: db.PlatformYouTube, account: "mine", fail: errors.New("401 for access_token=" + token)}
	if err := hub.Attach(ctx, a); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	s := &Server{chat: hub}
	err := s.sendRolloverNotice(ctx, db.PlatformYouTube, "mine", "switching")
	if err == nil {
		t.Fatal("a refused notice was reported as posted")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("the refusal carried the token: %v", err)
	}

	if err := (&Server{}).sendRolloverNotice(ctx, db.PlatformYouTube, "mine", "x"); err == nil {
		t.Fatal("a server with no chat reported the notice as posted")
	}
}
