package eventstream

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// everyCataloguedType publishes one envelope of every catalogued type, each
// with the payload a real producer would carry, so an overlay filter is tested
// against the whole catalogue rather than the two rows it is meant to admit.
func everyCataloguedType(h *Hub) {
	for _, s := range Catalogue() {
		env := Envelope{Type: s.Type}
		switch s.Type {
		case TypeChatDisplay:
			env.Data = DisplayLine{ID: "d1", Platform: "twitch", Name: "viewer", Text: "hello"}
		case TypeChatUndisplay:
			env.Data = DisplayRemoval{Platform: "twitch", IDs: []string{"d1"}}
		case TypeChatMessage:
			env.Data = map[string]any{"platform": "twitch", "text": "raw", "author": map[string]any{"id": "42"}}
		}
		h.Publish(env)
	}
}

// TestAnOverlaySubscriberIsSentOnlyTheDisplayProjection: an overlay key is the
// narrowest principal there is. Of every type in the catalogue it gets the two
// overlay rows and nothing else -- not chat.message (which carries author ids
// and accounts), not chat.moderation, not destination status, not alerts --
// whatever topics it asks for.
func TestAnOverlaySubscriberIsSentOnlyTheDisplayProjection(t *testing.T) {
	h := NewHub()
	ov, _, _ := h.Subscribe(Filter{
		// Asking for everything changes nothing: the overlay view wins.
		Topics:  Topics(),
		Overlay: &OverlayView{},
	}, "")
	defer ov.Close()

	everyCataloguedType(h)
	// A producer that put a display payload on the WRONG type: the platform is
	// readable, so only the closed type set stands between it and the overlay.
	h.Publish(Envelope{Type: TypeChatMessage, Data: DisplayLine{ID: "x", Platform: "twitch", Text: "raw"}})
	h.Publish(Envelope{Type: TypeChatModeration, Data: DisplayRemoval{Platform: "twitch", IDs: []string{"x"}}})
	got := drain(ov)
	var types []Type
	for _, env := range got {
		types = append(types, env.Type)
	}
	want := []Type{TypeChatDisplay, TypeChatUndisplay}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("overlay subscriber got %v, want exactly %v", types, want)
	}
}

// TestOnlyTheOverlayRowsAreOverlayVisible pins the closed set: adding a type to
// it is a decision about what a key in an OBS URL may see, and must be made
// here, in a reviewed diff, rather than by a catalogue row's topic.
func TestOnlyTheOverlayRowsAreOverlayVisible(t *testing.T) {
	for _, s := range Catalogue() {
		visible := OverlayVisible(s.Type)
		should := s.Type == TypeChatDisplay || s.Type == TypeChatUndisplay
		if visible != should {
			t.Errorf("%s: overlay-visible = %v, want %v", s.Type, visible, should)
		}
		if visible && s.ReadScope != Withheld {
			t.Errorf("%s is overlay-visible and also sent to read tokens; chat is withheld from them", s.Type)
		}
		if visible && s.Topic != TopicOverlay {
			t.Errorf("%s is overlay-visible but in topic %s", s.Type, s.Topic)
		}
	}
}

// TestAPlatformFilteredOverlayIsSentOnlyItsPlatforms: the Twitch-safe overlay.
// A line or a removal from any other platform never reaches the wire, and a
// payload whose platform cannot be read is withheld -- fail closed.
func TestAPlatformFilteredOverlayIsSentOnlyItsPlatforms(t *testing.T) {
	h := NewHub()
	tw, _, _ := h.Subscribe(Filter{Overlay: &OverlayView{Platforms: []string{"twitch"}}}, "")
	defer tw.Close()
	all, _, _ := h.Subscribe(Filter{Overlay: &OverlayView{}}, "")
	defer all.Close()

	h.Publish(Envelope{Type: TypeChatDisplay, Data: DisplayLine{ID: "a", Platform: "twitch", Text: "t"}})
	h.Publish(Envelope{Type: TypeChatDisplay, Data: DisplayLine{ID: "b", Platform: "youtube", Text: "y"}})
	h.Publish(Envelope{Type: TypeChatUndisplay, Data: DisplayRemoval{Platform: "youtube", IDs: []string{"b"}}})
	h.Publish(Envelope{Type: TypeChatUndisplay, Data: DisplayRemoval{Platform: "twitch", IDs: []string{"a"}}})
	// Not a display payload at all: nobody on an overlay gets it.
	h.Publish(Envelope{Type: TypeChatDisplay, Data: map[string]string{"platform": "twitch"}})

	for _, env := range drain(tw) {
		if p := displayPlatform(env); p != "twitch" {
			t.Errorf("the Twitch overlay was sent a %s event for %q", env.Type, p)
		}
	}
	if n := len(drain(all)); n != 4 {
		t.Errorf("the all-platforms overlay got %d events, want 4 (the untyped payload withheld)", n)
	}
}

// TestADisplayLineCarriesNoIdentityOrModerationField: the projection's JSON
// field set is closed. A field added to DisplayLine is a field on every OBS
// scene, so it has to be added here on purpose.
func TestADisplayLineCarriesNoIdentityOrModerationField(t *testing.T) {
	b, err := json.Marshal(DisplayLine{
		ID: "x", Platform: "twitch", Name: "n", Color: "#ffffff", Text: "t", Action: true,
		Badges:    []DisplayBadge{{ID: "subscriber", Version: "1", Label: "Sub"}},
		Moderator: true, Subscriber: true, Broadcaster: true, At: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"id": true, "platform": true, "name": true, "color": true, "badges": true,
		"text": true, "action": true, "at": true,
		"moderator": true, "subscriber": true, "broadcaster": true,
	}
	for k := range m {
		if !allowed[k] {
			t.Errorf("DisplayLine has field %q, which is not on the overlay allowlist", k)
		}
	}
	for _, banned := range []string{"author", "authorId", "userId", "account", "channel", "ip", "replyToId", "by", "reason"} {
		if strings.Contains(string(b), `"`+banned+`"`) {
			t.Errorf("DisplayLine JSON carries %q", banned)
		}
	}
}

// TestADisplayLineSurvivesRedactionTyped: the overlay filter reads the platform
// off the TYPED payload, so the redaction pass must not turn it into a map --
// which RedactTree would, and every line would then be withheld.
func TestADisplayLineSurvivesRedactionTyped(t *testing.T) {
	env := Envelope{Type: TypeChatDisplay, Data: DisplayLine{Platform: "twitch", Name: "a", Text: "rtmp://x/live/sk_live_abcdefghijklmnop"}}.redacted()
	d, ok := env.Data.(DisplayLine)
	if !ok {
		t.Fatalf("redaction changed the payload to %T", env.Data)
	}
	if strings.Contains(d.Text, "sk_live_abcdefghijklmnop") {
		t.Errorf("a credential-shaped string survived redaction: %q", d.Text)
	}
}
