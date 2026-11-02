package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// UX Council A48's read-token-refused route, given a counterpart proof rather than a
// bare Want, so it adds nothing to counterpartlessExcusedCeiling.
//
// A Want that observes a 403 says what the read token got today. It says nothing about
// what the route carries, which is the part that would leak if that 403 ever became a
// 200. The proof below plants the thing the route exists to hand out -- a viewer's
// message automod flagged -- shows that the admin receives it, and scans what the read
// token received for it.

// chatFlagsSpoken is the planted viewer message. Ordinary English for the reason
// transcriptExportSpoken gives: chat is human speech, and a high-entropy literal would
// be flagged by gitleaks for no benefit. A proof-local sentinel, so the runner scans
// the read principal's bytes for it as well as for the standing list.
const chatFlagsSpoken = "the harbour pilot counted seven gulls on the breakwater"

// proveChatFlags discharges GET /chat/flags.
//
// The route lists the messages automod flagged, text and author included. The proof
// attaches a Hub whose moderator flags exactly the planted line, waits until the
// admin's answer carries it -- the positive control -- and returns the read token's
// answer for the runner to scan.
func proveChatFlags(t *testing.T) proofResult {
	s, h, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	hub.SetModerator(flagText{text: chatFlagsSpoken})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Attach(ctx, &speakingAdapter{
		moderatableAdapter: moderatableAdapter{platform: db.PlatformTwitch, account: "acct"},
		say: []chat.Message{{
			ID: "ledger-flag", Author: chat.Author{ID: "u-ledger", Name: "viewer"}, Text: chatFlagsSpoken,
		}},
	}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	sign := login(t, h)
	read := createScopedToken(t, h, sign, "ledger-chat-flags", db.ScopeRead)

	const path = "/api/v1/chat/flags"
	var high string
	deadline := time.Now().Add(2 * time.Second)
	for {
		code, body := rawBodyOf(t, h, sign, path)
		if code == http.StatusOK && strings.Contains(body, chatFlagsSpoken) {
			high = body
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the admin's flag list answered %d without the planted message, so the "+
				"read principal would be denied nothing: %s", code, truncateForFailure(body))
		}
		time.Sleep(5 * time.Millisecond)
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's flag list answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, path),
		High:      high,
		Read:      low,
		Sentinels: []string{chatFlagsSpoken},
	}
}
