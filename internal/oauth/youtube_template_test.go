package oauth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The scheduled-broadcast editor (#18). Every call here targets ONE broadcast by id, because a
// template look-ahead holds several upcoming broadcasts on the same channel at once. The
// liveBroadcast() heuristic that the go-live path uses would edit the wrong one of them.
//
// As in youtube_schedule_test.go, the stub answers with documented shapes and the assertions are on
// the request: the query string, the part names, and the body. A fake that agrees with the code
// proves only that the code agrees with itself.

const ytEditorBroadcast = `{"items":[{"id":"bcast-9",
	"snippet":{"title":"Scheduled stream","description":"","scheduledStartTime":"2030-11-03T23:00:00Z"},
	"status":{"lifeCycleStatus":"created","privacyStatus":"public"},
	"contentDetails":{"enableDvr":true,"enableAutoStart":false,"enableAutoStop":false,
		"monitorStream":{"enableMonitorStream":false,"broadcastStreamDelayMs":0}}}]}`

// ytEditorStub serves the broadcast read and the write endpoints, and records every request.
func ytEditorStub(t *testing.T, log *[]capture, categoryStatus int) *YouTube {
	t.Helper()
	srv := httptest.NewServer(recordAll(t, log, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET " + ytBroadcastsPath:
			io.WriteString(w, ytEditorBroadcast)
		case "PUT " + ytBroadcastsPath:
			io.WriteString(w, `{"id":"bcast-9"}`)
		case "POST /thumbnails/set":
			io.WriteString(w, `{}`)
		case "DELETE " + ytBroadcastsPath:
			w.WriteHeader(http.StatusNoContent)
		case "GET /videoCategories":
			w.WriteHeader(categoryStatus)
			io.WriteString(w, `{"items":[{"id":"20","snippet":{"title":"Gaming","assignable":true}}]}`)
		case "GET /videos":
			io.WriteString(w, `{"items":[{"id":"bcast-9","snippet":{"title":"Scheduled stream","categoryId":"22"}}]}`)
		case "PUT /videos":
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return NewYouTube(WithBaseURL(srv.URL))
}

// findCapture returns the first recorded request matching method and path.
func findCapture(log []capture, method, path string) *capture {
	for i := range log {
		if log[i].Method == method && log[i].Path == path {
			return &log[i]
		}
	}
	return nil
}

// The title and description are written to the broadcast the caller named, and the scheduled start
// is carried through from the broadcast as it stands. A write that dropped the start would be
// refused by YouTube, and one that defaulted it would move the show.
func TestYouTubeEditsAScheduledBroadcastsTitleAndDescriptionAndKeepsItsStart(t *testing.T) {
	var log []capture
	y := ytEditorStub(t, &log, http.StatusOK)

	warnings, err := y.EditScheduledBroadcast(context.Background(), "tok", "bcast-9", ScheduledEdit{
		Title:       "Friday live 2030-11-03",
		Description: "Every week at seven.",
	})
	if err != nil {
		t.Fatalf("EditScheduledBroadcast: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("a title-only edit reported warnings: %v", warnings)
	}
	put := findCapture(log, "PUT", ytBroadcastsPath)
	if put == nil {
		t.Fatalf("no liveBroadcasts.update reached the stub")
	}
	if got := put.Query; !strings.Contains(got, "part=snippet%2CcontentDetails") && !strings.Contains(got, "part=snippet,contentDetails") {
		t.Fatalf("update part = %q, want snippet and contentDetails", got)
	}
	snip, _ := put.Body["snippet"].(map[string]any)
	if snip["title"] != "Friday live 2030-11-03" || snip["description"] != "Every week at seven." {
		t.Fatalf("update snippet = %v, want the new title and description", snip)
	}
	if snip["scheduledStartTime"] != "2030-11-03T23:00:00Z" {
		t.Fatalf("update dropped or moved the scheduled start: %v", snip["scheduledStartTime"])
	}
	if put.Body["id"] != "bcast-9" {
		t.Fatalf("update named broadcast %v, want bcast-9", put.Body["id"])
	}
}

// Privacy is on the broadcast's status part. A privacy write is sent only when the template names one,
// because a bare part=status with no privacyStatus is the request YouTube documents as reverting the
// setting to its default.
func TestYouTubeSendsPrivacyOnItsOwnPartAndSendsNothingWhenTheTemplateHasNone(t *testing.T) {
	var log []capture
	y := ytEditorStub(t, &log, http.StatusOK)
	if _, err := y.EditScheduledBroadcast(context.Background(), "tok", "bcast-9",
		ScheduledEdit{Title: "t", Privacy: "unlisted"}); err != nil {
		t.Fatalf("edit with privacy: %v", err)
	}
	var statusWrite *capture
	for i := range log {
		if log[i].Method == "PUT" && strings.Contains(log[i].Query, "part=status") {
			statusWrite = &log[i]
		}
	}
	if statusWrite == nil {
		t.Fatalf("a privacy edit sent no part=status write")
	}
	st, _ := statusWrite.Body["status"].(map[string]any)
	if st["privacyStatus"] != "unlisted" {
		t.Fatalf("status write privacy = %v, want unlisted", st["privacyStatus"])
	}

	var quiet []capture
	y2 := ytEditorStub(t, &quiet, http.StatusOK)
	if _, err := y2.EditScheduledBroadcast(context.Background(), "tok", "bcast-9",
		ScheduledEdit{Title: "t"}); err != nil {
		t.Fatalf("edit without privacy: %v", err)
	}
	for _, c := range quiet {
		if strings.Contains(c.Query, "part=status") {
			t.Fatalf("an edit with no privacy still sent part=status: %+v", c)
		}
	}
}

// A category that the platform cannot resolve must not fail the whole edit: the title was written,
// and failing here would send the operator to redo work that landed. It is reported as a warning.
func TestYouTubeACategoryTheWriteCannotSetIsAWarningNotAFailure(t *testing.T) {
	var log []capture
	y := ytEditorStub(t, &log, http.StatusInternalServerError)
	warnings, err := y.EditScheduledBroadcast(context.Background(), "tok", "bcast-9",
		ScheduledEdit{Title: "t", Category: "Gaming"})
	if err != nil {
		t.Fatalf("a category failure failed the whole edit: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatalf("a category the platform refused was not reported at all")
	}
	if findCapture(log, "PUT", ytBroadcastsPath) == nil {
		t.Fatalf("the title write did not happen before the category was tried")
	}
}

// The thumbnail goes up as raw image bytes, to thumbnails.set, against the broadcast's id. The
// broadcast id is the video id, so no lookup is needed.
func TestYouTubeUploadsAThumbnailAsRawImageBytesAgainstTheBroadcastId(t *testing.T) {
	var (
		gotQuery, gotType string
		gotBody           []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/thumbnails/set" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.RawQuery
		gotType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	y := NewYouTube(WithBaseURL(srv.URL))

	picture := []byte("\x89PNG\r\n-fake-thumbnail-bytes")
	if err := y.SetScheduledThumbnail(context.Background(), "tok", "bcast-9", picture, "image/png"); err != nil {
		t.Fatalf("SetScheduledThumbnail: %v", err)
	}
	if !strings.Contains(gotQuery, "videoId=bcast-9") {
		t.Fatalf("thumbnail query = %q, want videoId=bcast-9", gotQuery)
	}
	if gotType != "image/png" {
		t.Fatalf("thumbnail Content-Type = %q, want image/png", gotType)
	}
	if !bytes.Equal(gotBody, picture) {
		t.Fatalf("thumbnail body was not the raw image bytes")
	}
}

// Cancelling deletes the broadcast by id and nothing else. The already-deleted case is pinned by
// youtube_schedule_test.go, because it is the same delete.
func TestYouTubeCancelsAScheduledBroadcastByIdOnly(t *testing.T) {
	var log []capture
	y := ytEditorStub(t, &log, http.StatusOK)
	if err := y.CancelScheduledBroadcast(context.Background(), "tok", "bcast-9"); err != nil {
		t.Fatalf("CancelScheduledBroadcast: %v", err)
	}
	del := findCapture(log, "DELETE", ytBroadcastsPath)
	if del == nil || !strings.Contains(del.Query, "id=bcast-9") {
		t.Fatalf("cancel did not delete bcast-9: %+v", del)
	}
}
