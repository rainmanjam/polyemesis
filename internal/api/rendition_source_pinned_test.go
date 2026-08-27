package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A RENDITION CANNOT BE MOVED BETWEEN PROGRAMMES BY AN UPDATE.
//
// handleCreateRendition has had requireNamedSource since #543; handleUpdate-
// Rendition decodes the body over the row it just read and had nothing, so
// `{"sourceId": 2}` was a complete, successful relocation. The rendition left
// programme 1's reconciler, which tore its encode down, while every destination
// in programme 1 still selected it by id -- no process, and the card's
// explanation was "rendition N is no longer available" for a rendition that had
// only been moved. A one-field PUT nobody thinks of as destructive, and nothing
// in the 200 said a programme had changed.
//
// `sourceId: 999` came back as the raw "FOREIGN KEY constraint failed (787)",
// which names neither the field nor the fix. Every other source refusal on this
// server ends with the list of programmes to choose from.
//
// Mutation: delete the renditionKeepsItsSource call from handleUpdateRendition.
// Observed to fail with "PUT with another programme's sourceId returned 200,
// want 400" (net/http status assertion inside send).
func TestARenditionCannotBeMovedBetweenProgrammesByAnUpdate(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	created := createRendition(t, h, sign, map[string]any{
		"name": "1080p60", "height": 1080, "videoBitrate": 6000,
	})
	if created.SourceID == nil {
		t.Fatal("the fixture created a rendition belonging to no programme, so there " +
			"is no owner for the assertions below to protect")
	}
	owner := *created.SourceID
	path := "/api/v1/renditions/" + strconv.FormatInt(created.ID, 10)

	second := &db.Source{Name: "Studio B", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(second); err != nil {
		t.Fatalf("create the second source: %v", err)
	}
	if second.ID == owner {
		t.Fatal("both programmes report the same id, so a move cannot be distinguished " +
			"from staying put")
	}

	// THE CONTROLS, first, because a guard that refuses every update is worse
	// than the bug: it makes renditions uneditable on every install.
	t.Run("a body that names no source still saves", func(t *testing.T) {
		send(t, h, sign, http.MethodPut, path,
			map[string]any{"videoBitrate": 4500}, http.StatusOK)
	})
	t.Run("a body that names its own source still saves", func(t *testing.T) {
		send(t, h, sign, http.MethodPut, path,
			map[string]any{"videoBitrate": 4600, "sourceId": owner}, http.StatusOK)
	})

	for _, tt := range []struct {
		name string
		want int64
	}{
		{"another programme", second.ID},
		{"a programme that does not exist", 999},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := mustJSONError(t, h, sign, http.MethodPut, path,
				map[string]any{"sourceId": tt.want}, http.StatusBadRequest)
			if !strings.Contains(msg, "cannot be moved between programmes") {
				t.Errorf("refusal = %q, want it to say a rendition cannot change programme", msg)
			}
			if !strings.Contains(msg, "Available:") {
				t.Errorf("refusal = %q, want the list of programmes to choose from; a 400 "+
					"that names no option is a validation message, not a usable one", msg)
			}
			if strings.Contains(msg, "FOREIGN KEY") {
				t.Errorf("refusal = %q -- that is SQLite's constraint text reaching the "+
					"operator, which names neither the field nor the fix", msg)
			}
		})
	}

	// THE ROW ITSELF. A 400 that had already written would be the worst of both.
	after, err := store.GetRendition(created.ID)
	if err != nil {
		t.Fatalf("GetRendition: %v", err)
	}
	if after.SourceID == nil || *after.SourceID != owner {
		t.Errorf("rendition %d now belongs to %s, want source %d: the refusal did not "+
			"stop the write", created.ID, sourceRef(after.SourceID), owner)
	}
}
