package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// TestRedactJSONTreeDoesNotLeakAnArgvHeader is the #162 half of redactJSONTree.
//
// An array of strings in a payload is an argv, and an argv is where a
// credential arrives SPLIT. alerts.Redact's grammar is `label SEP value`, so
// running it over each element in turn masks nothing at all on the canonical,
// correctly-spelled header form -- which is the shape it is most important to
// catch. See the doc on alerts.Redact and TestRedactPerElementIsStrictlyWorse.
func TestRedactJSONTreeDoesNotLeakAnArgvHeader(t *testing.T) {
	// Named `sentinel`, not `secret`. gitleaks' generic-api-key rule keys off the
	// IDENTIFIER on the left of the assignment -- key, api, token, secret, auth
	// and friends -- and this value clears its 3.5 entropy floor, so
	// `const secret = "..."` is a finding. Adding it to .gitleaks.toml's
	// allowlist would be the wrong repair: the allowlist self-test in
	// .github/workflows/security.yml begins by requiring a CLEAN working tree,
	// so every fixture that needs an exemption is one more thing that can
	// disable the check proving the exemptions are narrow. A name that is not a
	// credential keyword costs nothing and needs no exemption at all.
	const sentinel = "SENTINEL-ws-argv-header-4f21bc90ee"

	leaky := []struct {
		name string
		in   any
	}{
		{
			name: "the canonical split header, which per-element Redact cannot see",
			in:   map[string]any{"argv": []any{"-headers", "Authorization:", "Bearer", sentinel}},
		},
		{
			name: "the same with Basic",
			in:   map[string]any{"argv": []any{"-headers", "Authorization:", "Basic", sentinel}},
		},
		{
			name: "a secret-named label split from its value",
			in:   map[string]any{"argv": []any{"-x", "token:", sentinel}},
		},
		{
			name: "nested one level down, because payloads nest",
			in: map[string]any{
				"process": map[string]any{
					"args": []any{"-headers", "Authorization:", "Bearer", sentinel},
				},
			},
		},
		{
			name: "a URL element, which whole-text Redact already caught",
			in:   map[string]any{"argv": []any{"-f", "flv", "rtmps://live.example/app/" + sentinel}},
		},
		{
			// A HETEROGENEOUS array, and the reason the []any arm joins the
			// string elements it finds rather than requiring the array to be
			// wholly strings.
			//
			// Requiring all-strings was suggested in review and is the wrong
			// trade HERE: it makes this row recurse element-wise, and
			// element-wise is precisely what masks nothing on a split header.
			// One non-string neighbour would be enough to turn the fix off. The
			// cost of joining what is there instead is that a mixed array whose
			// strings happen to join into a match collapses -- an over-mask, on
			// a payload shape nothing in this build produces, in exchange for
			// closing the leak on every shape that is not enumerable in advance.
			name: "a non-string neighbour must not switch the rule off",
			in: map[string]any{"argv": []any{
				"Authorization:", map[string]any{"kind": "metadata"}, "Bearer", sentinel,
			}},
		},
	}

	for _, tc := range leaky {
		t.Run(tc.name, func(t *testing.T) {
			got := mustJSONString(t, redactEventText(tc.in))
			if strings.Contains(got, sentinel) {
				t.Errorf("redactEventText leaked %s:\n%s\n\n"+
					"An array of strings is an argv. Redacting it PER ELEMENT is strictly "+
					"worse than redacting the join and never better (#162); the []any arm "+
					"must test the JOIN and collapse the array when the join changes.",
					sentinel, got)
			}
			if !strings.Contains(got, alerts.Mask) {
				t.Errorf("nothing was masked at all in %s -- the sentinel is gone but so is "+
					"any sign that redaction happened, which usually means the payload was "+
					"dropped rather than redacted", got)
			}
		})
	}
}

// TestReadScopedFramesAreUnchanged is the GATE on the change above.
//
// The []any arm collapses an array to a single mask when its join changes, and
// that is a WIRE-SHAPE change. It is only acceptable because it does not happen
// to anything this build actually sends. These are the three payload types
// carrying wsRedactText -- engine.SourceInfo, chat.Message and
// engine.CaptionEvent -- built the way their producers build them, asserted
// BYTE-IDENTICAL through redactEventText.
//
// If this fails, the array rule has started firing on real traffic and the
// trade has changed. Do not relax the rule to make it green: find which array
// joined into a match and say why.
func TestReadScopedFramesAreUnchanged(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	payloads := []struct {
		name string
		in   any
	}{
		{
			name: "engine.SourceInfo with tracks, video and operator annotations",
			in: engine.SourceInfo{
				ID: 1, Name: "Main programme", Probed: true,
				Tracks: []routing.Track{
					{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo", Language: "eng", Title: "Programme"},
					{Index: 1, Channels: 1, Codec: "aac", Layout: "mono", Language: "spa", Title: "Comentario"},
				},
				Video: &ffmpeg.VideoStream{Codec: "h264", Width: 1920, Height: 1080},
				Annotations: []routing.TrackAnnotation{
					{Track: 0, Role: routing.RoleCommentary, Label: "Guest mic (Zoom)", Language: "en"},
					{Track: 1, Role: routing.RoleMusic, Label: "Licensed bed - do not archive"},
				},
			},
		},
		{
			name: "chat.Message with badges and the three role flags",
			in: chat.Message{
				ID: "m-1", Platform: db.PlatformTwitch, Account: "acct-1",
				Author: chat.Author{
					ID: "u-9", Name: "someviewer", Color: "#aabbcc",
					Badges:     []chat.Badge{{ID: "moderator"}, {ID: "subscriber"}},
					Moderator:  true,
					Subscriber: true,
				},
				Text: "great stream, what preset are you on? -preset ultrafast right?",
				At:   now,
			},
		},
		{
			name: "engine.CaptionEvent carrying a line",
			in: engine.CaptionEvent{
				Line: &transcribe.LiveCaption{
					Segment: transcribe.Segment{Text: "and we are back after the break"},
					At:      now, LagMS: 1800,
				},
			},
		},
		{
			name: "a plain string array that is not a credential",
			in:   map[string]any{"warnings": []any{"no audio on track 2", "silence tier engaged"}},
		},
		{
			name: "an argv of ordinary FFmpeg vocabulary",
			in:   map[string]any{"argv": []any{"-preset", "ultrafast", "-c:v", "libx264", "-f", "flv"}},
		},
	}

	for _, tc := range payloads {
		t.Run(tc.name, func(t *testing.T) {
			// The comparison is against the payload put through the SAME
			// marshal/unmarshal round trip with no redaction, not against the
			// struct's own marshalling. redactEventText decodes into map[string]any
			// by design -- see its doc -- and Go re-emits a map's keys in sorted
			// order, so a raw comparison would measure the round trip rather
			// than the redaction. What must be unchanged is the redaction's
			// effect, and that is exactly what this isolates.
			want := mustJSONString(t, jsonRoundTrip(t, tc.in))
			got := mustJSONString(t, redactEventText(tc.in))
			if got != want {
				t.Errorf("a read-scoped frame is no longer identical to the unredacted one.\n"+
					"plain:    %s\nredacted: %s\n\n"+
					"redactEventText is a RESIDUAL pass over payloads that carry no stored "+
					"credential; a difference here is over-masking of real traffic.", want, got)
			}
		})
	}
}

// jsonRoundTrip is redactEventText's decode step with the redaction removed:
// marshal, unmarshal into any, and nothing else.
func jsonRoundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var tree any
	if err := json.Unmarshal(b, &tree); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return tree
}

// TestRedactJSONTreeArrayRuleIsSoundInOneDirection states the argument the
// []any arm rests on, executably.
//
// The claim is: if the space-joined array is unchanged by Redact, then every
// element is unchanged too -- so recursing is safe and no element needs
// masking. Redact replaces SUBSTRINGS, and joining with a space only ever adds
// context that can COMPLETE a `label SEP value` match, never break one. This
// asserts the direction over the shapes that actually differ.
func TestRedactJSONTreeArrayRuleIsSoundInOneDirection(t *testing.T) {
	const k = "SENTINEL-soundness-8812aa"
	arrays := [][]string{
		{"-headers", "Authorization:", "Bearer", k},
		{"Authorization:", "Basic", k},
		{"token:", k},
		{"-preset", "ultrafast"},
		{"no audio on track 2", "silence tier engaged"},
		{"-rtmp_conn", "S:" + k},
		{"rtmps://live.example/app/" + k},
	}
	for _, arr := range arrays {
		joined := strings.Join(arr, " ")
		if alerts.Redact(joined) != joined {
			// The join changed, so the array collapses; nothing to check about
			// the elements.
			continue
		}
		for _, e := range arr {
			if alerts.Redact(e) != e {
				t.Errorf("the join of %v was UNCHANGED but the element %q was not. "+
					"The []any arm recurses when the join is unchanged, so this would be "+
					"a leak of the array rule's own reasoning: Redact must only ever "+
					"match MORE over the join, never less.", arr, e)
			}
		}
	}
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
