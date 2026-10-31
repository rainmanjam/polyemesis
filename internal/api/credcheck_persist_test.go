package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE LAST VERDICT SURVIVES A RELOAD (settings-council R10).
//
// The settings page draws its platform rows from GET /platforms/credentials.
// That list used to carry no verdict at all, so the check that ran on Save
// was visible only until the page was reloaded, and a returning operator saw
// "configured" over a pair the platform had refused. Both writers of a verdict
// -- the Save and the Re-check -- now record it, and the list returns it.

type listedCreds struct {
	Platform  string `json:"platform"`
	LastCheck *struct {
		State     string `json:"state"`
		Method    string `json:"method"`
		Detail    string `json:"detail"`
		CheckedAt string `json:"checkedAt"`
	} `json:"lastCheck"`
}

func listCreds(t *testing.T, h http.Handler, sign func(*http.Request)) ([]listedCreds, string) {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/platforms/credentials", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", w.Code, w.Body.String())
	}
	var got []listedCreds
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return got, w.Body.String()
}

// Saving a pair records the verdict the save reached, and the next read of the
// list -- the one a reloaded page makes -- returns it.
func TestTheVerdictASaveReachedIsStillListedOnTheNextRead(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	if code, got := putCreds(t, h, sign, "youtube", "not-a-google-id", "some-secret"); code != http.StatusOK ||
		got.Check.State != "rejected" {
		t.Fatalf("seed save: status %d, state %q; want 200 and rejected", code, got.Check.State)
	}

	list, _ := listCreds(t, h, sign)
	if len(list) != 1 || list[0].LastCheck == nil {
		t.Fatalf("list = %+v, want the youtube row carrying the verdict the save reached", list)
	}
	if list[0].LastCheck.State != "rejected" || list[0].LastCheck.Detail == "" ||
		list[0].LastCheck.CheckedAt == "" {
		t.Fatalf("last check = %+v, want rejected with its detail and time", list[0].LastCheck)
	}
}

// A Re-check replaces the stored verdict with its own, so the list follows the
// most recent answer rather than the first one.
func TestARecheckReplacesTheStoredVerdict(t *testing.T) {
	srv, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	if err := store.PutPlatformCreds(srv.box, db.PlatformYouTube, "my-streaming-project", "s"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.RecordPlatformCredsCheck(db.PlatformYouTube, "verified", "format", "stale", time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatalf("seed verdict: %v", err)
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/platforms/credentials/youtube/check", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("re-check status = %d: %s", w.Code, w.Body.String())
	}

	list, _ := listCreds(t, h, sign)
	if len(list) != 1 || list[0].LastCheck == nil || list[0].LastCheck.State != "rejected" {
		t.Fatalf("list = %+v, want the re-check's rejected verdict, not the seeded verified one", list)
	}
}

// The stored verdict is the checker's sentence and nothing else: the secret
// that was checked never appears in the list.
func TestTheListedVerdictCarriesNoSecret(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	// Built at run time so the fixture cannot look like a real credential.
	secret := strings.Repeat("q", 6) + "-fixture-" + strings.Repeat("7", 6)
	putCreds(t, h, sign, "youtube", "1234.apps.googleusercontent.com", secret)

	_, body := listCreds(t, h, sign)
	if strings.Contains(body, secret) {
		t.Fatalf("the credentials list carries the client secret: %s", body)
	}
}
