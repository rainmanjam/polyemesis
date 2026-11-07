package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The two chat-export downloads, each given a counterpart proof rather than a bare
// Want, so neither adds to counterpartlessExcusedCeiling. Shaped like proveChatFlags:
// plant what the route exists to hand out, show the admin receives it, and hand the
// read token's answer to the runner to scan.

// chatExportSpoken is the planted viewer line. Ordinary English, for the reason
// chatFlagsSpoken gives.
const chatExportSpoken = "the ferry captain waved at the lighthouse twice"

// proveChatExport discharges GET /chat/export.
func proveChatExport(t *testing.T) proofResult {
	h, sign, srv, rec := highlightFixture(t)
	start := plantChatExportLine(t, srv, rec)
	read := createScopedToken(t, h, sign, "ledger-chat-export", db.ScopeRead)
	path := "/api/v1/chat/export?from=" + strconv.FormatInt(start.Add(-time.Minute).UnixMilli(), 10) +
		"&to=" + strconv.FormatInt(start.Add(time.Hour).UnixMilli(), 10)

	code, high := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK || !strings.Contains(high, chatExportSpoken) {
		t.Fatalf("the admin's export answered %d without the planted message, so the read "+
			"principal would be denied nothing: %s", code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's export answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, "/api/v1/chat/export"),
		High:      high,
		Read:      low,
		Sentinels: []string{chatExportSpoken},
	}
}

// proveRecordingChat discharges GET /recordings/{id}/chat.
func proveRecordingChat(t *testing.T) proofResult {
	h, sign, srv, rec := highlightFixture(t)
	plantChatExportLine(t, srv, rec)
	read := createScopedToken(t, h, sign, "ledger-recording-chat", db.ScopeRead)
	path := "/api/v1/recordings/" + strconv.FormatInt(rec, 10) + "/chat"

	code, high := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK || !strings.Contains(high, chatExportSpoken) {
		t.Fatalf("the admin's recording chat answered %d without the planted message: %s",
			code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's recording chat answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, path),
		High:      high,
		Read:      low,
		Sentinels: []string{chatExportSpoken},
	}
}

func plantChatExportLine(t *testing.T, srv *Server, rec int64) time.Time {
	t.Helper()
	r, err := srv.store.GetRecording(rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.AppendChatMessages([]db.ChatMessage{{Platform: db.PlatformTwitch, Account: "acct",
		MessageID: "ledger-export", AuthorID: "u-ledger", AuthorName: "viewer", Text: chatExportSpoken,
		At: r.StartedAt.Add(30 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	return r.StartedAt
}
