package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestViewerNotesRoundTripAndList(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	const path = "/api/v1/chat/users/notes?platform=twitch&account=acct&authorId=u1"

	var empty viewerNoteBody
	decodeInto(t, send(t, h, sign, http.MethodGet, path, nil, http.StatusOK), &empty)
	if empty.Note != "" || empty.Tags == nil || len(empty.Tags) != 0 {
		t.Fatalf("a viewer with no note must answer an empty, editable one: %+v", empty)
	}

	var saved viewerNoteBody
	decodeInto(t, send(t, h, sign, http.MethodPut, path,
		map[string]any{"note": " mean in chat ", "tags": []string{"watch", "Watch", "regular"}}, http.StatusOK), &saved)
	if saved.Note != "mean in chat" || len(saved.Tags) != 2 || saved.UpdatedBy == "" || saved.UpdatedAt == nil {
		t.Fatalf("PUT = %+v", saved)
	}
	var got viewerNoteBody
	decodeInto(t, send(t, h, sign, http.MethodGet, path, nil, http.StatusOK), &got)
	if got.Note != "mean in chat" {
		t.Fatalf("GET = %+v", got)
	}

	// The list carries tags and never the note.
	raw := send(t, h, sign, http.MethodGet, "/api/v1/chat/users/notes", nil, http.StatusOK)
	if !strings.Contains(string(raw), `"authorId":"u1"`) || strings.Contains(string(raw), "mean in chat") {
		t.Fatalf("the tagged list must name the viewer and omit the note text: %s", raw)
	}

	send(t, h, sign, http.MethodDelete, path, nil, http.StatusOK)
	send(t, h, sign, http.MethodDelete, path, nil, http.StatusOK)
	decodeInto(t, send(t, h, sign, http.MethodGet, path, nil, http.StatusOK), &got)
	if got.Note != "" {
		t.Fatalf("note survived DELETE: %+v", got)
	}
}

func TestViewerNotesValidation(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	const path = "/api/v1/chat/users/notes?platform=twitch&authorId=u1"
	send(t, h, sign, http.MethodPut, path, map[string]any{"note": strings.Repeat("x", db.MaxViewerNoteLen+1)}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPut, path, map[string]any{"tags": []string{strings.Repeat("x", db.MaxViewerTagLen+1)}}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPut, path, map[string]any{"tags": []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPut, path, map[string]any{"bogus": 1}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPut, "/api/v1/chat/users/notes?platform=twitch", map[string]any{"note": "x"}, http.StatusBadRequest)
	send(t, h, sign, http.MethodGet, "/api/v1/chat/users/notes?authorId=u1", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodDelete, "/api/v1/chat/users/notes?platform=twitch", nil, http.StatusBadRequest)
	// A save with neither text nor tags stores nothing.
	send(t, h, sign, http.MethodPut, path, map[string]any{"note": "  ", "tags": []string{}}, http.StatusOK)
	var list struct {
		Viewers []viewerTagsBody `json:"viewers"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/chat/users/notes", nil, http.StatusOK), &list)
	if len(list.Viewers) != 0 {
		t.Fatalf("an empty save left a row: %+v", list)
	}
}

func TestViewerNotesAreRefusedToAReadToken(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	read := createScopedToken(t, h, sign, "viewer-notes-read", db.ScopeRead)
	const path = "/api/v1/chat/users/notes?platform=twitch&authorId=u1"
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		var body any
		if m == http.MethodPut {
			body = map[string]any{"note": "x"}
		}
		send(t, h, bearer(read), m, path, body, http.StatusForbidden)
	}
}

func TestViewerNotesRefuseAnUnknownPlatformAndNameTheLimitsFromTheConstants(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	const bad = "/api/v1/chat/users/notes?platform=myspace&authorId=u1"
	send(t, h, sign, http.MethodGet, bad, nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodPut, bad, map[string]any{"note": "x"}, http.StatusBadRequest)
	send(t, h, sign, http.MethodDelete, bad, nil, http.StatusBadRequest)
	// Nothing was stored under the made-up platform.
	var list struct {
		Viewers []viewerTagsBody `json:"viewers"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/chat/users/notes", nil, http.StatusOK), &list)
	if len(list.Viewers) != 0 {
		t.Fatalf("a row was stored for an unknown platform: %+v", list)
	}

	raw := send(t, h, sign, http.MethodPut, "/api/v1/chat/users/notes?platform=twitch&authorId=u1",
		map[string]any{"note": strings.Repeat("x", db.MaxViewerNoteLen+1)}, http.StatusBadRequest)
	want := fmt.Sprintf("at most %d characters, %d tags of up to %d characters", db.MaxViewerNoteLen, db.MaxViewerTags, db.MaxViewerTagLen)
	if !strings.Contains(string(raw), want) {
		t.Fatalf("the 400 must be built from the db limits (%q): %s", want, raw)
	}
}
