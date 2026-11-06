package db

import "testing"

// The one rule every reader of "which broadcast is this destination on?" shares.
// Callers that used to differ: the Facebook actions read only the announcement
// mirror (so a rewritten mirror could name a broadcast that is not on air), and
// the account listing and rollover read the mirror for X too (X never announces
// ahead, so its mirror is stale by construction).
func TestActiveBroadcastID(t *testing.T) {
	mk := func(p Platform, life, mirror string) *Destination {
		d := &Destination{Platform: p}
		d.Lifecycle.BroadcastID = life
		d.Facebook.BroadcastID = mirror
		return d
	}
	cases := []struct {
		name string
		d    *Destination
		want string
	}{
		{"nil", nil, ""},
		{"recorded wins over mirror", mk(PlatformYouTube, " live1 ", "mirror"), "live1"},
		{"facebook: recorded wins over rewritten mirror", mk(PlatformFacebook, "onair", "scheduled"), "onair"},
		{"falls back to mirror", mk(PlatformFacebook, "", " m1 "), "m1"},
		{"x never falls back to its stale mirror", mk(PlatformX, "", "ended"), ""},
		{"x with a recorded id keeps it", mk(PlatformX, "x1", "ended"), "x1"},
		{"neither", mk(PlatformTwitch, "", ""), ""},
	}
	for _, c := range cases {
		if got := c.d.ActiveBroadcastID(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
