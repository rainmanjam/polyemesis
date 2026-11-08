package eventstream

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

func tipEvent(msg string) activity.Event {
	return activity.Event{
		ID: "act_1", Kind: activity.KindTip, Platform: "twitch", Account: "acct", At: time.Unix(1, 0),
		Actor:   &activity.Actor{ID: "u1", Name: "Mara", Login: "mara"},
		Amount:  &activity.Amount{Currency: "USD", Minor: 500, Exponent: 2, Display: "$5.00"},
		Message: msg, Source: activity.Source{Type: "channel.cheer", ID: "x"},
	}
}

// TestAnActivityDisplayCarriesNoIdentityField pins the projection's JSON field
// set: a field added to ActivityDisplay is a field on every OBS scene, so it
// has to be added here on purpose.
func TestAnActivityDisplayCarriesNoIdentityField(t *testing.T) {
	b, err := json.Marshal(ActivityDisplay{
		ID: "x", Platform: "twitch", Kind: "gift", Name: "n",
		Amount: &activity.Amount{Currency: "USD", Minor: 1, Exponent: 2, Display: "0.01 USD"},
		Tier:   "1000", Months: 1, Count: 1, Recipient: "r", Title: "t", Viewers: 1, Level: 1,
		Message: "m", Test: true, At: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"id": true, "platform": true, "kind": true, "name": true, "amount": true, "tier": true,
		"months": true, "count": true, "recipient": true, "title": true, "viewers": true,
		"level": true, "message": true, "test": true, "at": true,
	}
	for k := range m {
		if !allowed[k] {
			t.Errorf("ActivityDisplay has field %q, which is not on the overlay allowlist", k)
		}
	}
	for _, banned := range []string{"actor", "account", "source", "recipients", "other", "login", "breakdown", "action", "progress"} {
		if strings.Contains(string(b), `"`+banned+`"`) {
			t.Errorf("ActivityDisplay JSON carries %q", banned)
		}
	}
}

// TestActivityRowsAreClassifiedAsDocumented: the activity row names viewers and
// is partly readable; its display twin is overlay-only and withheld.
func TestActivityRowsAreClassifiedAsDocumented(t *testing.T) {
	a, ok := Lookup(TypeActivity)
	if !ok || a.Topic != TopicActivity || a.ReadScope != Partly || a.ReadIf == nil {
		t.Fatalf("activity row: %+v", a)
	}
	d, ok := Lookup(TypeActivityDisplay)
	if !ok || d.Topic != TopicOverlay || d.ReadScope != Withheld {
		t.Fatalf("activity.display row: %+v", d)
	}
	if string(TypeActivity) != "activity" || string(TypeActivityDisplay) != "activity.display" || string(TopicActivity) != "activity" {
		t.Error("the wire names are frozen")
	}
}

func TestOnlyTheStreamKindIsReadableByAReadToken(t *testing.T) {
	for _, k := range activity.AllKinds() {
		e := activity.Sample(k, "twitch", activity.Overrides{}, time.Now())
		got := activityReadable(Envelope{Type: TypeActivity, Data: e})
		if want := k == activity.KindStream; got != want {
			t.Errorf("%s readable = %v, want %v", k, got, want)
		}
	}
	// Fail closed on a payload that is not an activity.
	if activityReadable(Envelope{Type: TypeActivity, Data: map[string]any{"kind": "stream"}}) {
		t.Error("a map payload was readable")
	}
	if activityReadable(Envelope{Type: TypeActivity}) {
		t.Error("a nil payload was readable")
	}
}

func TestActivitySurvivesRedactionTypedAndRedacted(t *testing.T) {
	key := "rtmp://x/live/streamkey_abcdefghijklmnop"
	e := tipEvent(key)
	e.Title = key
	e.Status = key
	e.Other = &activity.Actor{Name: key, Login: key}
	e.Recipients = []activity.Recipient{{ID: "r1", Name: key}}
	e.Progress = &activity.Progress{Options: []activity.ProgressOption{{Label: key, Votes: 3}}}
	env := Envelope{Type: TypeActivity, Data: e}.redacted()
	got, ok := env.Data.(activity.Event)
	if !ok {
		t.Fatalf("redaction changed the payload to %T", env.Data)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "streamkey_abcdefghijklmnop") {
		t.Errorf("a credential-shaped string survived: %s", b)
	}
	if got.Actor.ID != "u1" || got.Progress.Options[0].Votes != 3 || got.Recipients[0].ID != "r1" {
		t.Errorf("redaction damaged the structure: %+v", got)
	}
	// The original is untouched: the activity hub's ring shares it.
	if e.Message != key || e.Other.Name != key {
		t.Error("redaction wrote through to the caller's event")
	}

	d := Envelope{Type: TypeActivityDisplay, Data: ActivityDisplay{Platform: "twitch", Name: key, Recipient: key, Title: key, Message: key}}.redacted()
	dd, ok := d.Data.(ActivityDisplay)
	if !ok {
		t.Fatalf("redaction changed the display payload to %T", d.Data)
	}
	if strings.Contains(dd.Name+dd.Recipient+dd.Title+dd.Message, "streamkey_abcdefghijklmnop") {
		t.Errorf("display survived: %+v", dd)
	}
	if redactActor(nil) != nil {
		t.Error("redactActor(nil)")
	}
}

func TestAnActivityDisplayObeysThePlatformFilterAndNothingElseReachesAnOverlay(t *testing.T) {
	h := NewHub()
	tw, _, _ := h.Subscribe(Filter{Overlay: &OverlayView{Platforms: []string{"twitch"}}}, "")
	defer tw.Close()
	all, _, _ := h.Subscribe(Filter{Overlay: &OverlayView{}}, "")
	defer all.Close()

	h.Publish(Envelope{Type: TypeActivityDisplay, Data: ActivityDisplay{ID: "a", Platform: "twitch", Kind: "follow"}})
	h.Publish(Envelope{Type: TypeActivityDisplay, Data: ActivityDisplay{ID: "b", Platform: "kick", Kind: "follow"}})
	// The full activity (names an id, carries text) is never an overlay's.
	h.Publish(Envelope{Type: TypeActivity, Data: tipEvent("hi")})
	// Not a display payload: withheld from every overlay.
	h.Publish(Envelope{Type: TypeActivityDisplay, Data: map[string]string{"platform": "twitch"}})

	twGot := drain(tw)
	if len(twGot) != 1 || displayPlatform(twGot[0]) != "twitch" {
		t.Errorf("the Twitch overlay got %d events: %+v", len(twGot), twGot)
	}
	allGot := drain(all)
	if len(allGot) != 2 {
		t.Errorf("the all-platforms overlay got %d events, want 2", len(allGot))
	}
	for _, e := range allGot {
		if e.Type != TypeActivityDisplay {
			t.Errorf("an overlay was sent %s", e.Type)
		}
	}
}

func TestAnOverlayIsNotReplayedAnOldActivityDisplay(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := NewHub(WithClock(func() time.Time { return now }))
	h.Publish(Envelope{Type: TypeActivityDisplay, Data: ActivityDisplay{ID: "old", Platform: "twitch", Kind: "follow"}})
	h.Publish(Envelope{Type: TypeChatDisplay, Data: DisplayLine{ID: "line", Platform: "twitch", Text: "t"}})
	first := lastID(t, h)
	now = now.Add(OverlayActivityReplayMax + time.Second)
	h.Publish(Envelope{Type: TypeActivityDisplay, Data: ActivityDisplay{ID: "fresh", Platform: "twitch", Kind: "follow"}})
	h.Publish(Envelope{Type: TypeActivity, Data: tipEvent("hi")})

	// Resuming from before everything: an overlay is replayed the fresh alert and
	// the (non-activity) chat line, not the stale alert.
	sub, replay, _ := h.Subscribe(Filter{Overlay: &OverlayView{}}, h.boot+"-0")
	defer sub.Close()
	var ids []string
	for _, e := range replay {
		switch d := e.Data.(type) {
		case ActivityDisplay:
			ids = append(ids, d.ID)
		case DisplayLine:
			ids = append(ids, d.ID)
		}
	}
	if strings.Join(ids, ",") != "line,fresh" {
		t.Errorf("overlay replay = %v, want [line fresh]", ids)
	}

	// An admin subscriber is replayed everything still held.
	asub, areplay, _ := h.Subscribe(Filter{}, h.boot+"-0")
	defer asub.Close()
	if len(areplay) != 4 {
		t.Errorf("admin replay = %d events, want 4", len(areplay))
	}
	_ = first
}

func lastID(t *testing.T, h *Hub) string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ring[len(h.ring)-1].ID
}

func TestAReadScopedSubscriberGetsOnlyTheStreamKind(t *testing.T) {
	h := NewHub()
	sub, _, _ := h.Subscribe(Filter{ReadScoped: true}, "")
	defer sub.Close()
	h.Publish(Envelope{Type: TypeActivity, Data: tipEvent("hi")})
	h.Publish(Envelope{Type: TypeActivityDisplay, Data: ActivityDisplay{Platform: "twitch"}})
	h.Publish(Envelope{Type: TypeActivity, Data: activity.Sample(activity.KindStream, "twitch", activity.Overrides{}, time.Now())})
	got := drain(sub)
	if len(got) != 1 || got[0].Data.(activity.Event).Kind != activity.KindStream {
		t.Errorf("read token received %+v", got)
	}
}

func TestTheBusBridgeCarriesActivity(t *testing.T) {
	if st, ok := busTypes["activity"]; !ok || st != TypeActivity {
		t.Fatalf("busTypes: %v", busTypes)
	}
}

// A read token is sent the platform's stream report and NOTHING that identifies
// the account, the source or a person: the projected shape is pinned, and the
// test fails if the full event leaks, live or on replay.
func TestAReadTokenIsSentOnlyAProjectionOfTheStreamReport(t *testing.T) {
	h := NewHub()
	full := activity.Event{
		ID: "act_secret", Kind: activity.KindStream, Platform: "twitch", Account: "acct-ref-secret",
		At: time.Unix(5, 0), Phase: "online", Title: "Private title",
		Actor: &activity.Actor{ID: "actor-secret", Name: "Someone"}, Other: &activity.Actor{ID: "other-secret", Name: "Other"},
		Message: "words", Source: activity.Source{Type: "stream.online", ID: "source-secret"},
	}
	live, _, _ := h.Subscribe(Filter{ReadScoped: true}, "")
	defer live.Close()
	h.Publish(Envelope{Type: TypeActivity, Data: full})

	check := func(label string, env Envelope) {
		t.Helper()
		got, ok := env.Data.(activity.Event)
		if !ok {
			t.Fatalf("%s: payload %T", label, env.Data)
		}
		want := activity.Event{Kind: activity.KindStream, Platform: "twitch", At: time.Unix(5, 0), Phase: "online"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: read token was sent %+v, want only %+v", label, got, want)
		}
		b, _ := json.Marshal(env)
		for _, secret := range []string{"acct-ref-secret", "actor-secret", "other-secret", "source-secret", "Private title", "words", "act_secret"} {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s: the read stream leaks %q: %s", label, secret, b)
			}
		}
	}
	got := drain(live)
	if len(got) != 1 {
		t.Fatalf("live: %d events", len(got))
	}
	check("live", got[0])

	_, replay, _ := h.Subscribe(Filter{ReadScoped: true}, h.boot+"-0")
	if len(replay) != 1 {
		t.Fatalf("replay: %d events", len(replay))
	}
	check("replay", replay[0])

	// An admin subscriber still gets the whole event.
	adm, _, _ := h.Subscribe(Filter{}, "")
	defer adm.Close()
	h.Publish(Envelope{Type: TypeActivity, Data: full})
	if a := drain(adm)[0].Data.(activity.Event); a.Account != "acct-ref-secret" || a.Source.ID != "source-secret" {
		t.Errorf("the projection was applied to an admin: %+v", a)
	}
	// And the ring keeps the full copy, unprojected.
	h.mu.Lock()
	ringed := h.ring[0].Data.(activity.Event)
	h.mu.Unlock()
	if ringed.Account == "" {
		t.Error("the projection wrote through to the ring")
	}
}
