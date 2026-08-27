package db

import (
	"encoding/json"
	"testing"
)

// UNTICKING A MATRIX CELL HAS TO SURVIVE THE SAVE.
//
// The matrix is sparse and absence is its whole meaning: an absent cell is off.
// json.Unmarshal decoding an object into an existing map MERGES, so absence
// meant "unchanged" instead -- and the settings save is a decode over the
// stored document by design, so the two meanings of absent collided on every
// save. Untick "auto-ban on Twitch", save, get a 200, and the ban kept firing
// until somebody restarted the server while every screen said it was off.
//
// Mutation: delete the `if len(sent.On) > 0 { a.On = nil }` branch from
// AutomodSettings.UnmarshalJSON. Observed to fail with
//
//	unticking one cell: cell twitch/ban/links is still on after a save that
//	left it out -- the untick did nothing
func TestDecodingAnAutomodMatrixReplacesTheStoredCells(t *testing.T) {
	stored := func() AutomodSettings {
		return AutomodSettings{On: map[string]bool{
			"twitch/ban/links":     true,
			"twitch/timeout/caps":  true,
			"youtube/delete/links": true,
		}}
	}

	cases := []struct {
		name string
		body string
		want map[string]bool
	}{{
		name: "unticking one cell",
		body: `{"on":{"twitch/timeout/caps":true,"youtube/delete/links":true}}`,
		want: map[string]bool{"twitch/timeout/caps": true, "youtube/delete/links": true},
	}, {
		name: "clearing every cell",
		body: `{"on":{}}`,
		want: map[string]bool{},
	}, {
		// A client that wrote null meant empty.
		name: "an explicit null clears too",
		body: `{"on":null}`,
		want: map[string]bool{},
	}, {
		// THE CONTROL. A decoder that cleared the matrix on every automod
		// document would satisfy every case above and quietly disarm
		// moderation on any save that did not mention the matrix -- which is
		// most of them, because a client that has never heard of the matrix
		// must still be able to save the rest of the document.
		name: "a document that does not mention the matrix changes nothing",
		body: `{"enabled":true}`,
		want: map[string]bool{
			"twitch/ban/links":     true,
			"twitch/timeout/caps":  true,
			"youtube/delete/links": true,
		},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := stored()
			if err := json.Unmarshal([]byte(tc.body), &a); err != nil {
				t.Fatalf("decode %s: %v", tc.body, err)
			}
			for k, want := range tc.want {
				if a.On[k] != want {
					t.Fatalf("%s: cell %s is %v after the save, want %v",
						tc.name, k, a.On[k], want)
				}
			}
			for k := range a.On {
				if !tc.want[k] {
					t.Fatalf("%s: cell %s is still on after a save that left "+
						"it out -- the untick did nothing", tc.name, k)
				}
			}
		})
	}
}

// The per-platform kill switch is the same shape and the same trap: absence
// means enabled, so a merge cannot express removing a platform from the map
// either. It works today only because the UI happens to send an explicit false
// for every platform, which is a habit rather than a guarantee.
//
// Mutation: delete the `if len(sent.PlatformEnabled) > 0` branch from
// AutomodSettings.UnmarshalJSON. Observed to fail with "kick is still in the
// per-platform switches after a save that dropped it".
func TestDecodingAutomodPlatformSwitchesReplacesTheStoredOnes(t *testing.T) {
	a := AutomodSettings{PlatformEnabled: map[Platform]bool{
		"twitch": false,
		"kick":   false,
	}}
	if err := json.Unmarshal([]byte(`{"platformEnabled":{"twitch":false}}`), &a); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := a.PlatformEnabled["kick"]; ok {
		t.Fatal("kick is still in the per-platform switches after a save that " +
			"dropped it, so moderation stays off there with nothing saying so")
	}
	if on, ok := a.PlatformEnabled["twitch"]; !ok || on {
		t.Fatalf("twitch = (%v, present %v) after a save that sent it as "+
			"false; the sent value was lost", on, ok)
	}

	// THE CONTROL. A decoder that dropped the switches on every automod
	// document would satisfy the assertions above and silently re-enable a
	// platform an operator had turned off, on any save that did not mention
	// them.
	b := AutomodSettings{PlatformEnabled: map[Platform]bool{"kick": false}}
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if on, ok := b.PlatformEnabled["kick"]; !ok || on {
		t.Fatalf("kick = (%v, present %v) after a save that did not mention "+
			"the platform switches; moderation was silently switched back on", on, ok)
	}
}

// Everything else in the block still decodes OVER what is stored, which is what
// makes a partial payload safe. A custom UnmarshalJSON is the easiest place to
// lose that by accident -- decode into a fresh value and every field the client
// did not send is blanked.
//
// Mutation: change the final decode in AutomodSettings.UnmarshalJSON to build a
// zero value instead of decoding into the existing one. Observed to fail with
// "the model instruction was blanked by a save that only changed enabled".
func TestDecodingAnAutomodBlockStillMergesEverythingElse(t *testing.T) {
	a := DefaultSettings().Automod
	want := a.Model.Instruction
	a.History.MaxLinks = 7

	if err := json.Unmarshal([]byte(`{"enabled":false}`), &a); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a.Enabled {
		t.Fatal("the sent field did not take")
	}
	if a.Model.Instruction != want {
		t.Fatalf("the model instruction was blanked by a save that only "+
			"changed enabled: %q", a.Model.Instruction)
	}
	if a.History.MaxLinks != 7 {
		t.Fatalf("the stored history bound was reset to %d by a save that "+
			"only changed enabled", a.History.MaxLinks)
	}
}
