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
		case TypePollDisplay:
			env.Data = PollDisplay{ID: "p1", Question: "Next map?", State: "open",
				Options: []PollDisplayOption{{Label: "Dust", Votes: 3}, {Label: "Mirage", Votes: 1}}, Total: 4}
		case TypePollStarted, TypePollVote, TypePollEnded:
			// The admin and read-token payload, with its per-platform
			// breakdown: an overlay must never be sent it.
			env.Data = map[string]any{"id": 1, "question": "Next map?",
				"byPlatform": map[string]int{"twitch": 3, "youtube": 1}}
		}
		h.Publish(env)
	}
}

// TestAnOverlaySubscriberIsSentOnlyTheDisplayProjection: an overlay key is the
// narrowest principal there is. Of every type in the catalogue it gets the
// three overlay rows and nothing else -- not chat.message (which carries author
// ids and accounts), not chat.moderation, not the poll.* events with their
// per-platform breakdown, not destination status, not alerts -- whatever
// topics it asks for.
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
	want := []Type{TypeChatDisplay, TypeChatUndisplay, TypePollDisplay}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("overlay subscriber got %v, want exactly %v", types, want)
	}
}

// TestOnlyTheOverlayRowsAreOverlayVisible pins the closed set: adding a type to
// it is a decision about what a key in an OBS URL may see, and must be made
// here, in a reviewed diff, rather than by a catalogue row's topic.
//
// poll.display was added by #14, deliberately: the poll widget is drawn from
// the same key as the chat overlay, and what it is sent is the totals-only
// projection (PollDisplay), never the poll.* payload with its per-platform
// breakdown.
func TestOnlyTheOverlayRowsAreOverlayVisible(t *testing.T) {
	for _, s := range Catalogue() {
		visible := OverlayVisible(s.Type)
		should := s.Type == TypeChatDisplay || s.Type == TypeChatUndisplay || s.Type == TypePollDisplay
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

// TestARetractedLineIsNeverReplayedToAnOverlay: the replay ring holds every
// chat.display. An overlay that reconnects with an id from before a line that
// has since been retracted -- an OBS reload, a network blip -- must not be
// replayed that line, not even for the frame before its chat.undisplay. The
// retraction scrubs the line from overlay replay at the moment it is
// published, so it holds whether the resume point is before the line, between
// the line and its retraction, or unprovable (a gap replays everything held).
func TestARetractedLineIsNeverReplayedToAnOverlay(t *testing.T) {
	h := NewHub()
	start := h.boot + "-0"
	h.Publish(Envelope{Type: TypeChatDisplay, Data: DisplayLine{ID: "keep", Platform: "twitch", Text: "fine"}})
	h.Publish(Envelope{Type: TypeChatDisplay, Data: DisplayLine{ID: "gone", Platform: "twitch", Text: "removed later"}})
	between := h.boot + "-2"
	// Same id on another platform is a different line and must survive.
	h.Publish(Envelope{Type: TypeChatDisplay, Data: DisplayLine{ID: "gone", Platform: "youtube", Text: "other platform"}})
	h.Publish(Envelope{Type: TypeChatUndisplay, Data: DisplayRemoval{Platform: "twitch", IDs: []string{"gone"}}})

	for name, lastID := range map[string]string{"before the line": start, "after the line": between, "unprovable": "nope-1"} {
		sub, replay, _ := h.Subscribe(Filter{Overlay: &OverlayView{}}, lastID)
		sub.Close()
		var texts []string
		for _, env := range replay {
			if d, ok := env.Data.(DisplayLine); ok {
				texts = append(texts, d.Text)
				if d.Text == "removed later" {
					t.Errorf("resume %s: the retracted line was replayed to an overlay", name)
				}
			}
		}
		if name != "after the line" && !containsStr(texts, "fine") {
			t.Errorf("resume %s: replay %v lost a line that was never retracted", name, texts)
		}
		if !containsStr(texts, "other platform") {
			t.Errorf("resume %s: replay %v lost the other platform's line with the same id", name, texts)
		}
	}

	// An admin subscriber sees moderation anyway: it is replayed the line AND
	// the retraction after it, in order, which is the history as it happened.
	_, admin, _ := h.Subscribe(Filter{}, start)
	var seq []Type
	for _, env := range admin {
		seq = append(seq, env.Type)
	}
	if len(seq) != 4 || seq[3] != TypeChatUndisplay {
		t.Errorf("admin replay = %v, want all three lines then the retraction", seq)
	}
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// TestAPollIsShownOnEveryOverlayWhateverItsPlatformFilter: the poll widget is
// platform-neutral -- totals only, no platform named anywhere in it -- so the
// Twitch-only overlay that must not show a rival's logo can still show the
// poll. That is a typed exception to "a payload with no platform is
// withheld", and only for the PollDisplay type on the poll.display row: the
// same payload on another row, or another payload on this row, still fails
// closed.
func TestAPollIsShownOnEveryOverlayWhateverItsPlatformFilter(t *testing.T) {
	h := NewHub()
	tw, _, _ := h.Subscribe(Filter{Overlay: &OverlayView{Platforms: []string{"twitch"}}}, "")
	defer tw.Close()
	poll := PollDisplay{ID: "p1", Question: "q", State: "open", Options: []PollDisplayOption{{Label: "a"}, {Label: "b"}}}
	h.Publish(Envelope{Type: TypePollDisplay, Data: poll})
	// Wrong row for the payload, and wrong payload for the row.
	h.Publish(Envelope{Type: TypeChatDisplay, Data: poll})
	h.Publish(Envelope{Type: TypePollDisplay, Data: map[string]any{"question": "q"}})
	got := drain(tw)
	if len(got) != 1 || got[0].Type != TypePollDisplay {
		t.Fatalf("the Twitch overlay got %v, want exactly the one poll.display", got)
	}
}

// TestAPollDisplayCarriesNoPlatformOrVoterField: the projection's JSON field
// set is closed, like DisplayLine's. A per-platform breakdown would put a
// rival platform's name on a Twitch-only scene, and a voter's name or id on
// any scene; neither has a field to travel in.
func TestAPollDisplayCarriesNoPlatformOrVoterField(t *testing.T) {
	ended := time.Unix(2, 0)
	b, err := json.Marshal(PollDisplay{ID: "p1", Question: "q", State: "ended", Total: 2,
		Options: []PollDisplayOption{{Label: "a", Votes: 2}, {Label: "b"}},
		EndsAt:  time.Unix(1, 0), EndedAt: &ended})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"id": true, "question": true, "options": true, "total": true,
		"state": true, "endsAt": true, "endedAt": true}
	for k := range m {
		if !allowed[k] {
			t.Errorf("PollDisplay has field %q, which is not on the overlay allowlist", k)
		}
	}
	for _, o := range m["options"].([]any) {
		for k := range o.(map[string]any) {
			if k != "label" && k != "votes" {
				t.Errorf("a PollDisplay option has field %q", k)
			}
		}
	}
	for _, banned := range []string{"platform", "byPlatform", "voter", "author", "userId", "name", "account"} {
		if strings.Contains(string(b), `"`+banned+`"`) {
			t.Errorf("PollDisplay JSON carries %q", banned)
		}
	}
}

// TestAPollDisplaySurvivesRedactionTyped: the overlay policy recognises the
// poll by its TYPE, so redaction must keep it one while it scrubs the text.
func TestAPollDisplaySurvivesRedactionTyped(t *testing.T) {
	// Built at runtime: an obviously fake key in a stream-key URL, so no
	// literal in this file looks like a real credential to a scanner.
	keyA := strings.Repeat("a1b2", 5)
	keyB := strings.Repeat("c3d4", 5)
	env := Envelope{Type: TypePollDisplay, Data: PollDisplay{Question: "rtmp://x/live/" + keyA,
		Options: []PollDisplayOption{{Label: "rtmp://y/app/" + keyB}}}}.redacted()
	d, ok := env.Data.(PollDisplay)
	if !ok {
		t.Fatalf("redaction changed the payload to %T", env.Data)
	}
	if strings.Contains(d.Question, keyA) || strings.Contains(d.Options[0].Label, keyB) {
		t.Errorf("a stream key survived redaction: %+v", d)
	}
}
