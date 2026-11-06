package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/platform"
)

// YouTube's scheduled-broadcast editor (#18): the writes a recurring template makes to ONE upcoming
// broadcast, addressed by its id.
//
// Every write here reads the broadcast it targets first and then sends what it changed, because
// liveBroadcasts.update is destructive by part (see the traps at the top of youtube_broadcast.go).
// The title and description go through writeBroadcastParts, which already carries the start time and
// the freezing toggles through. Privacy is on the status part, sent exactly as compliance.go sends it.
// Category lives on the video, and setCategory already does its read-modify-write.
//
// The thumbnail call is PROVISIONAL. thumbnails.set is documented for a video id, and a live broadcast's
// id is its video id, so the call should work before go-live. That is the one claim here that has not
// been checked against a real channel, and the owner decided to verify it live later.

var _ ScheduledEditor = (*YouTube)(nil)

// ytUploadBase is the upload host for thumbnails.set. Production uses it; a WithBaseURL stub replaces
// it, the same as every other call site.
const ytUploadBase = platform.YouTubeAPIHost + "/upload/youtube/v3"

// ytThumbnailsPath is thumbnails.set, relative to the upload base.
const ytThumbnailsPath = "/thumbnails/set"

// EditScheduledBroadcast writes the title and description of one scheduled broadcast, then its
// privacy, then its category. The order is deliberate: the title is the thing a viewer sees first and
// the one whose failure matters most, so it lands first. A category that the platform refuses is a
// warning, not a failure, because the title has already been written.
func (y *YouTube) EditScheduledBroadcast(ctx context.Context, accessToken, broadcastID string, e ScheduledEdit) ([]string, error) {
	if strings.TrimSpace(broadcastID) == "" {
		return nil, fmt.Errorf("no broadcast id to edit")
	}
	b, err := y.broadcastByID(ctx, accessToken, broadcastID)
	if err != nil {
		return nil, err
	}
	b.Snippet.Title = e.Title
	b.Snippet.Description = e.Description
	res := &MetadataResult{}
	if err := y.writeBroadcastParts(ctx, accessToken, b, BroadcastSettings{}, res); err != nil {
		return nil, err
	}

	if strings.TrimSpace(e.Privacy) != "" {
		body := map[string]any{
			"id":     broadcastID,
			"status": map[string]any{"privacyStatus": strings.ToLower(strings.TrimSpace(e.Privacy))},
		}
		if err := requestJSON(ctx, http.MethodPut, y.apiEndpoint()+ytBroadcastsPath+"?part=status",
			accessToken, body, nil, nil); err != nil {
			return nil, broadcastWriteAdvice(err, b.Status.LifeCycleStatus, BroadcastSettings{})
		}
	}

	var warnings []string
	if strings.TrimSpace(e.Category) != "" {
		if _, err := y.setCategory(ctx, accessToken, broadcastID, e.Category); err != nil {
			warnings = append(warnings, "category: "+err.Error())
		}
	}
	return warnings, nil
}

// SetScheduledThumbnail uploads a thumbnail for one scheduled broadcast. The bytes are sent raw, with
// the image's own content type, which is what thumbnails.set takes; it is not a JSON request.
func (y *YouTube) SetScheduledThumbnail(ctx context.Context, accessToken, broadcastID string, data []byte, mime string) error {
	if strings.TrimSpace(broadcastID) == "" {
		return fmt.Errorf("no broadcast id to attach a thumbnail to")
	}
	if len(data) == 0 {
		return fmt.Errorf("the thumbnail is empty")
	}
	if mime != "image/png" && mime != "image/jpeg" {
		return fmt.Errorf("a YouTube thumbnail must be a PNG or a JPEG, not %q", mime)
	}
	q := url.Values{}
	q.Set("videoId", broadcastID)
	return requestRaw(ctx, http.MethodPost,
		y.apiBase(ytUploadBase)+ytThumbnailsPath+"?"+q.Encode(), accessToken, mime, data)
}

// ErrScheduledNotCancellable is returned by CancelScheduledBroadcast when YouTube reports the broadcast
// as no longer upcoming. A broadcast that is testing, live or complete may hold a VOD or a live audience,
// so the cancel refuses it without sending a DELETE, and the caller stops retrying.
var ErrScheduledNotCancellable = errors.New("this broadcast is no longer upcoming, so it cannot be removed from here")

// upcomingLifecycles are the lifecycle states in which a broadcast has not yet gone on air. Only these
// may be deleted by a cancel.
var upcomingLifecycles = map[string]bool{"created": true, "ready": true}

// CancelScheduledBroadcast deletes one scheduled broadcast by id, and only while YouTube still reports
// it as upcoming. A broadcast that is no longer listed, or that a 404 answers for, is already gone, and
// that reads as cancelled. The DELETE itself is deleteBroadcast, which also treats a 404 as deleted.
func (y *YouTube) CancelScheduledBroadcast(ctx context.Context, accessToken, broadcastID string) error {
	if strings.TrimSpace(broadcastID) == "" {
		return fmt.Errorf("no broadcast id to cancel")
	}
	b, err := y.broadcastByID(ctx, accessToken, broadcastID)
	if errors.Is(err, errBroadcastNotListed) {
		return nil
	}
	var se *statusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if !upcomingLifecycles[b.Status.LifeCycleStatus] {
		return fmt.Errorf("%w: YouTube reports it as %q", ErrScheduledNotCancellable, b.Status.LifeCycleStatus)
	}
	return y.deleteBroadcast(ctx, accessToken, broadcastID)
}

// requestRaw sends a non-JSON body. It mirrors requestJSON for auth, status handling and the error
// shape, so a refusal from thumbnails.set reads the same as every other refusal in this package.
func requestRaw(ctx context.Context, method, endpoint, accessToken, contentType string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")

	resp, err := doRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &statusError{Status: resp.StatusCode, URL: endpoint, Body: snippet(raw), full: string(raw)}
	}
	return nil
}
