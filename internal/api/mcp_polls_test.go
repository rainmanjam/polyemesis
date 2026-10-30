package api

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// TestStartPollPreviewsTheAnnouncementBeforeItPostsAnything: start_poll posts
// into every chat as the streamer, which is exactly what an agent steered by
// injected chat text would want to do. Without confirm:true it posts nothing
// and answers with the exact message it would send -- cleaned the way the
// engine cleans it, so the preview cannot differ from what goes out.
func TestStartPollPreviewsTheAnnouncementBeforeItPostsAnything(t *testing.T) {
	rig := newMCPRig(t)
	admin := mcpClient(t, rig.h, rig.admin)
	args := map[string]any{"question": "Next\r\nmap?", "options": []string{"Dust", "Mirage"}, "durationSeconds": 60}

	text, isErr := callText(t, admin, "start_poll", args)
	if !isErr || !strings.Contains(text, "POLL: Next map? — 1) Dust 2) Mirage") {
		t.Fatalf("preview = %v %s", isErr, text)
	}
	if _, ok := rig.s.polls.Current(); ok {
		t.Fatal("a poll started without confirm")
	}
	if len(rig.mcpEvents()) != 0 {
		t.Error("the preview was audited as an action")
	}

	args["confirm"] = true
	if text, isErr := callText(t, admin, "start_poll", args); isErr || !strings.Contains(text, `"state":"open"`) {
		t.Fatalf("confirmed start = %v %s", isErr, text)
	}
	cur, ok := rig.s.polls.Current()
	if !ok || cur.Question != "Next map?" {
		t.Fatalf("running = %+v", cur)
	}
	if evs := rig.mcpEvents(); len(evs) != 1 {
		t.Errorf("%d audit events for the start, want 1", len(evs))
	}

	// An invalid spec is an argument error, not a refusal with a preview.
	bad := map[string]any{"question": "q", "options": []string{"a", "A"}, "durationSeconds": 60, "confirm": true}
	if text, isErr := callText(t, admin, "start_poll", bad); !isErr || !strings.Contains(text, "repeats") {
		t.Errorf("duplicate options = %v %s", isErr, text)
	}
}

// TestGetPollsGivesAReadTokenCountsAndNoVoter: the read tool reaches the same
// counts the REST route serves, and nothing that names who voted.
func TestGetPollsGivesAReadTokenCountsAndNoVoter(t *testing.T) {
	rig := newMCPRig(t)
	if _, err := rig.s.polls.Start(pollSpec()); err != nil {
		t.Fatal(err)
	}
	rig.s.polls.Offer(chat.Message{ID: "m-secret", Platform: db.PlatformKick, Account: "acct",
		Author: chat.Author{ID: "kick-user-8812", Name: "SecretVoterName"}, Text: "!vote 1"}, chat.DisplayClear)
	text, isErr := callText(t, mcpClient(t, rig.h, rig.read), "get_polls", map[string]any{"limit": 5})
	if isErr || !strings.Contains(text, `"kick":1`) {
		t.Fatalf("get_polls = %v %s", isErr, text)
	}
	for _, secret := range []string{"kick-user-8812", "SecretVoterName", "m-secret"} {
		if strings.Contains(text, secret) {
			t.Errorf("get_polls named %q", secret)
		}
	}
	if text, isErr := callText(t, mcpClient(t, rig.h, rig.read), "get_polls", map[string]any{"limit": 500}); !isErr {
		t.Errorf("an out-of-range limit was accepted: %s", text)
	}
}
