package api

import (
	"context"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/startupenv"
)

// Rumble chat follows the startup environment handed to the server, not the
// process environment: no key means silence, a key attaches the adapter.
func TestRumbleChatFollowsTheStartupEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  startupenv.Env
		want bool
	}{
		{"no key", startupenv.Env{}, false},
		{"a key", startupenv.Env{RumbleChatKey: "k", RumbleChatChannel: "chan"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := testServerWith(t, Options{Env: tc.env})
			s.chat = newTestHub(t)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if got := s.attachRumble(ctx); got != tc.want {
				t.Errorf("attachRumble = %v, want %v", got, tc.want)
			}
			if got := len(chatsOn(s.chat, db.PlatformRumble)) == 1; got != tc.want {
				t.Errorf("a Rumble adapter attached = %v, want %v", got, tc.want)
			}
		})
	}
}
