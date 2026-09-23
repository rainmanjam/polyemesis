package api

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// An unknown field under settings.automod is refused, the way it is in every
// other section of the document.
//
// THE BUG: PUT /settings decodes with DisallowUnknownFields, but automod has
// its own UnmarshalJSON (so a sent matrix replaces rather than merges), and a
// custom unmarshaler is handed raw bytes -- the outer decoder's strictness does
// not reach it. {recording:{bogus:1}} was a 400 and {automod:{bogus:1}} was a
// 200 that stored nothing. Worse, AUTOMOD.md documented the history bounds as
// window / retain / idleEviction -- the engine's internal names, not the wire
// ones -- so an operator following the docs got a success and no change.
func TestAnUnknownAutomodFieldIsRefusedLikeEveryOtherSection(t *testing.T) {
	h, store, auth := renditionServer(t, defaultTools())

	for _, body := range []map[string]any{
		{"automod": map[string]any{"bogus": 1}},
		{"automod": map[string]any{"history": map[string]any{"window": "45s"}}},
		{"automod": map[string]any{"history": map[string]any{"retain": 50}}},
		{"automod": map[string]any{"history": map[string]any{"idleEviction": "5m"}}},
		{"automod": map[string]any{"history": map[string]any{"maxAuthors": 5000}}},
		{"automod": map[string]any{"model": map[string]any{"apiKey": "sk-x"}}},
		{"automod": map[string]any{"rules": []any{map[string]any{"regex": "x"}}}},
	} {
		r := jsonRequest(t, http.MethodPut, "/api/v1/settings", body)
		auth(r)
		if w := do(t, h, r); w.Code != http.StatusBadRequest {
			t.Errorf("PUT /settings %v: got %d, want 400 -- an unknown automod field "+
				"was accepted and silently dropped", body, w.Code)
		}
	}

	// The wire names still work, and land.
	r := jsonRequest(t, http.MethodPut, "/api/v1/settings", map[string]any{
		"automod": map[string]any{"history": map[string]any{
			"windowSeconds": 45, "retainPerAuthor": 50, "idleEvictionSeconds": 300,
		}},
	})
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("PUT /settings with the documented names: %d %s", w.Code, w.Body.String())
	}
	st, err := store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if hs := st.Automod.History; hs.WindowSeconds != 45 || hs.RetainPerAuthor != 50 || hs.IdleEvictionSeconds != 300 {
		t.Errorf("the documented history fields did not land: %+v", hs)
	}
}

// The history table in AUTOMOD.md names exactly the wire fields of
// db.AutomodHistory -- no more, no fewer.
//
// It named window / retain / idleEviction / maxAuthors, which are the engine's
// internal HistoryLimits names. With the server now refusing them, a doc that
// drifts again sends an operator straight into a 400, so the table is checked
// against the struct rather than trusted.
func TestAutomodDocsNameTheHistoryFieldsTheServerAccepts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "AUTOMOD.md"))
	if err != nil {
		t.Fatalf("read AUTOMOD.md: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "## The history checker's settings" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal(`docs/AUTOMOD.md has no "## The history checker's settings" section; ` +
			"if it was renamed, rename it here too")
	}
	row := regexp.MustCompile("^\\| `([A-Za-z]+)` \\|")
	var documented []string
	inTable := false
	for _, l := range lines[start+1:] {
		if strings.HasPrefix(l, "|") {
			inTable = true
			if m := row.FindStringSubmatch(l); m != nil {
				documented = append(documented, m[1])
			}
			continue
		}
		if inTable {
			break
		}
	}

	var wire []string
	ty := reflect.TypeOf(db.AutomodHistory{})
	for i := 0; i < ty.NumField(); i++ {
		wire = append(wire, strings.Split(ty.Field(i).Tag.Get("json"), ",")[0])
	}
	sort.Strings(documented)
	sort.Strings(wire)
	if !reflect.DeepEqual(documented, wire) {
		t.Errorf("AUTOMOD.md's history table names %v;\nthe server accepts %v.\n"+
			"A documented name the server does not have is a 400 for anyone who follows the doc.",
			documented, wire)
	}
}
