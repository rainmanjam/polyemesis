package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// sourceServer is renditionServer under a name that says what these tests are
// about. Same fixture: a real store, a real manager, and an FFmpeg path that
// cannot exec, so a reconcile logs a failed spawn instead of binding a real
// port from a unit test.
func sourceServer(t *testing.T) (http.Handler, *db.DB, func(*http.Request)) {
	t.Helper()
	return renditionServer(t, defaultTools())
}

type sourceRow struct {
	ID            int64             `json:"id"`
	Name          string            `json:"name"`
	Token         string            `json:"token"`
	IsDefault     bool              `json:"isDefault"`
	TokenEnforced bool              `json:"tokenEnforced"`
	PublishURLs   map[string]string `json:"publishUrls"`
}

func listSources(t *testing.T, h http.Handler, sign func(*http.Request)) []sourceRow {
	t.Helper()
	var rows []sourceRow
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/sources", nil, http.StatusOK), &rows)
	return rows
}

func TestSourcesListStartsWithTheMigratedDefault(t *testing.T) {
	h, _, sign := sourceServer(t)

	rows := listSources(t, h, sign)
	if len(rows) != 1 {
		t.Fatalf("got %d sources on a fresh install, want 1", len(rows))
	}
	if !rows[0].IsDefault {
		t.Error("the only source is not marked default; unscoped requests would have nowhere to go")
	}
	if rows[0].Token == "" {
		t.Error("source has no publish token")
	}
}

func TestCreatingASourceNeedsOnlyAName(t *testing.T) {
	h, _, sign := sourceServer(t)

	// The point of defaulting the ingest block: an operator adds a programme
	// and then edits its ports, rather than having to supply a complete valid
	// ingest before the row can exist at all.
	var created sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": "Vertical"}, http.StatusCreated), &created)

	if created.Name != "Vertical" {
		t.Errorf("name = %q, want Vertical", created.Name)
	}
	if created.IsDefault {
		t.Error("a second source became the default; unscoped requests would change programme")
	}
	if created.Token == "" {
		t.Error("created source has no publish token")
	}
}

// The token is minted and rotatable, but no listener checks it yet, and the API
// has to say so. A UI presenting "rotate token" as a security control while the
// ingest ignored it would be worse than not having the field at all.
func TestSourceReportsThatItsTokenIsNotEnforced(t *testing.T) {
	h, _, sign := sourceServer(t)

	rows := listSources(t, h, sign)
	if rows[0].TokenEnforced {
		t.Error("tokenEnforced is true, but nothing checks the token")
	}
	// And it must not appear in a publish URL, which would imply it does.
	for proto, u := range rows[0].PublishURLs {
		if rows[0].Token != "" && strings.Contains(u, rows[0].Token) {
			t.Errorf("%s publish URL embeds the token, implying it authenticates: %s", proto, u)
		}
	}
}

func TestRotatingASourceTokenChangesItAndReturnsTheNewOne(t *testing.T) {
	h, _, sign := sourceServer(t)

	before := listSources(t, h, sign)[0]
	var got sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPost,
		"/api/v1/sources/"+strconv.FormatInt(before.ID, 10)+"/token", nil, http.StatusOK), &got)

	if got.Token == "" {
		t.Fatal("rotate returned no token")
	}
	if got.Token == before.Token {
		t.Error("rotate returned the same token")
	}
	// The response carries the URLs too: an operator who rotates and then
	// cannot see what to paste has taken their own ingest down.
	if len(got.PublishURLs) == 0 {
		t.Error("rotate response has no publishUrls")
	}
}

func TestUpdatingASourceWithoutATokenKeepsTheStoredOne(t *testing.T) {
	h, _, sign := sourceServer(t)

	before := listSources(t, h, sign)[0]
	// A rename, which is what the UI sends. It carries no token, and blanking
	// a secret an encoder is already using would take the ingest down.
	var got sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPut,
		"/api/v1/sources/"+strconv.FormatInt(before.ID, 10),
		map[string]any{"name": "Horizontal"}, http.StatusOK), &got)

	if got.Name != "Horizontal" {
		t.Errorf("name = %q, want Horizontal", got.Name)
	}
	if got.Token != before.Token {
		t.Errorf("token changed on a rename: %q, want %q", got.Token, before.Token)
	}
}

func TestTheLastSourceCannotBeDeletedThroughTheAPI(t *testing.T) {
	h, _, sign := sourceServer(t)

	only := listSources(t, h, sign)[0]
	// 400 rather than 204: an install with no sources has no ingest at all and
	// no way back through the UI.
	send(t, h, sign, http.MethodDelete,
		"/api/v1/sources/"+strconv.FormatInt(only.ID, 10), nil, http.StatusBadRequest)
}

func TestDeletingASourceIsAllowedOnceASecondExists(t *testing.T) {
	h, _, sign := sourceServer(t)

	var extra sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": "Vertical"}, http.StatusCreated), &extra)

	send(t, h, sign, http.MethodDelete,
		"/api/v1/sources/"+strconv.FormatInt(extra.ID, 10), nil, http.StatusNoContent)

	if rows := listSources(t, h, sign); len(rows) != 1 {
		t.Fatalf("got %d sources after deleting one, want 1", len(rows))
	}
}

func TestADestinationCreatedWithoutASourceLandsOnTheDefault(t *testing.T) {
	h, store, sign := sourceServer(t)

	// This is every API client written before sources existed.
	createDestination(t, h, sign, map[string]any{
		"name": "legacy", "kind": "rtmp", "url": "rtmp://example.test/live",
		"audioBitrate": 160,
	})

	want, err := store.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	rows, err := store.ListDestinationsBySource(want)
	if err != nil {
		t.Fatalf("ListDestinationsBySource: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("default source has %d destinations, want 1 -- a NULL source_id "+
			"leaves a destination created but never started", len(rows))
	}
}

func TestSourceValidationSurfacesThroughTheAPI(t *testing.T) {
	h, _, sign := sourceServer(t)

	body := send(t, h, sign, http.MethodPost, "/api/v1/sources", map[string]any{
		"name":   "Broken",
		"ingest": map[string]any{"mode": "srt", "srt": map[string]any{"port": 70000, "latencyMs": 200}},
	}, http.StatusBadRequest)

	if !strings.Contains(string(body), "srt port") {
		t.Errorf("error body %s, want it to name the srt port", body)
	}
}
