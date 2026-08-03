package scheduler

import (
	"strings"
	"testing"
)

// A playlist schedule and a destination schedule are different shapes, and a
// row that is both is a row whose author expected something the product will
// not do. Refusing it beats half-honouring it.
//
// The mutation: delete the DestinationIDs clause in Validate and this passes.
func TestAPlaylistScheduleMayNotAlsoNameDestinations(t *testing.T) {
	s := Schedule{
		Name:           "evening filler",
		Action:         ActionPlaylistStart,
		Kind:           KindDaily,
		DestinationIDs: []int64{7},
	}.Normalized()
	err := s.Validate()
	if err == nil {
		t.Fatal("a playlist schedule naming destinations was accepted")
	}
	if !strings.Contains(err.Error(), "destination") {
		t.Errorf("error %q does not tell the operator which half is the problem", err)
	}
}

// Both new actions must pass validation, or the feature is unreachable in a way
// no other test would notice: Validate's default branch rejects anything it does
// not know, so forgetting to list them here fails every save.
//
// The mutation: remove ActionPlaylistStart from the switch and this fails.
func TestBothPlaylistActionsValidate(t *testing.T) {
	for _, a := range []Action{ActionPlaylistStart, ActionPlaylistStop} {
		s := Schedule{Name: "filler", Action: a, Kind: KindDaily}.Normalized()
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", a, err)
		}
	}
}

// TargetsPlaylist is what the runner branches on. It exists so no caller
// compares against a string literal: a fourth action added later would then
// silently take the destination path.
//
// The mutation: make TargetsPlaylist return false for ActionPlaylistStop and
// this fails.
func TestTargetsPlaylistIsTrueForBothPlaylistActionsAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		action Action
		want   bool
	}{
		{ActionPlaylistStart, true},
		{ActionPlaylistStop, true},
		{ActionStart, false},
		{ActionStop, false},
	} {
		if got := (Schedule{Action: tc.action}).TargetsPlaylist(); got != tc.want {
			t.Errorf("TargetsPlaylist(%q) = %v, want %v", tc.action, got, tc.want)
		}
	}
}

// Enables() answers "the enabled value this schedule writes" and is read by the
// DESTINATION path. playlist.stop answers false there, which is correct for the
// playlist and catastrophic if it ever reaches a destination: it would disable
// every one of them.
//
// This test pins the pairing rather than the boolean: whatever Enables says, a
// playlist action must not be routed by it.
func TestPlaylistStopDoesNotLookLikeADestinationDisable(t *testing.T) {
	s := Schedule{Action: ActionPlaylistStop}
	if !s.TargetsPlaylist() {
		t.Fatal("playlist.stop is not recognised as a playlist action, so the runner " +
			"would route it by Enables() and disable every destination")
	}
}
