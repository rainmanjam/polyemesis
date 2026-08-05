package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// YouTube broadcast settings: the metadata that is NOT title/description.
//
// This file exists separately from metadata.go because the YouTube live API has
// four traps in it, and every one of them produces a request that SUCCEEDS
// while doing the wrong thing. None is visible from a 200.
//
//  1. liveBroadcasts.update requires FOUR properties on every call -- id,
//     snippet.scheduledStartTime, contentDetails.monitorStream.enableMonitorStream
//     and .broadcastStreamDelayMs. A partial update is rejected.
//
//  2. It is destructive BY PART, not by field. Sending part=status without
//     privacyStatus does not leave the current value alone; YouTube documents
//     that it "will remove the existing privacy setting and revert to the
//     default". The same applies to every part. So a write is always
//     read-then-write, never a bare PATCH, and every field of a part being sent
//     is carried whether the operator touched it or not.
//
//  3. Most contentDetails toggles FREEZE once the broadcast leaves
//     created/ready, with documented 403s such as
//     enableDvrModificationNotAllowed. Metadata has a window.
//
//  4. Category and tags are not on the broadcast at all. They are
//     videos.update snippet.categoryId / snippet.tags[] against the broadcast
//     id, which is also a video id.

// BroadcastSettings is the per-broadcast metadata beyond title and description.
//
// Every field is a POINTER, and that is the whole design rather than a style
// choice. Trap 2 means this code must be able to tell "the operator cleared
// this" from "the operator did not mention it": the first has to be written,
// the second has to be carried through from the broadcast as it currently
// stands. A bool cannot express that and a zero value would silently disable
// somebody's DVR.
type BroadcastSettings struct {
	// Tags are the video's keywords. Replaced wholesale, because that is what
	// snippet.tags[] does -- there is no add-one operation.
	//
	// There is deliberately no category here. Metadata.Category already carries
	// it as a HUMAN NAME and setCategory resolves that against the assignable
	// list; a second numeric field would be two ways to set one thing, and the
	// two would disagree the first time somebody used both.
	Tags *[]string `json:"tags,omitempty"`
	// ScheduledStart is RFC 3339. Required on every liveBroadcasts.update, so
	// when the operator has not set one the current value is carried through.
	ScheduledStart *string `json:"scheduledStart,omitempty"`

	// The contentDetails toggles. These are the ones that freeze.
	EnableDvr       *bool `json:"enableDvr,omitempty"`
	EnableAutoStart *bool `json:"enableAutoStart,omitempty"`
	EnableAutoStop  *bool `json:"enableAutoStop,omitempty"`
	// MonitorStream is the review feed. Its two fields are among the four
	// liveBroadcasts.update demands on every call.
	EnableMonitorStream *bool `json:"enableMonitorStream,omitempty"`
	StreamDelayMs       *int  `json:"streamDelayMs,omitempty"`
}

// Empty reports whether there is nothing to write.
func (b BroadcastSettings) Empty() bool {
	return b.Tags == nil && b.ScheduledStart == nil &&
		b.EnableDvr == nil && b.EnableAutoStart == nil && b.EnableAutoStop == nil &&
		b.EnableMonitorStream == nil && b.StreamDelayMs == nil
}

// TouchesContentDetails reports whether this write needs the part that freezes.
func (b BroadcastSettings) TouchesContentDetails() bool {
	return b.EnableDvr != nil || b.EnableAutoStart != nil || b.EnableAutoStop != nil ||
		b.EnableMonitorStream != nil || b.StreamDelayMs != nil
}

// BroadcastWindow describes what can still be changed on the current
// broadcast, so the composer can disable a control BEFORE the operator edits it
// rather than reporting a 403 afterwards.
//
// The API remains the authority. A broadcast can go live between this read and
// the write, so a 403 is still handled; this exists to stop the operator
// discovering the limit mid-broadcast, which is the worst possible moment.
type BroadcastWindow struct {
	BroadcastID string `json:"broadcastId"`
	Title       string `json:"title"`
	// LifeCycleStatus is YouTube's own word: created, ready, testing, live,
	// complete. Passed through unchanged rather than mapped, because an
	// operator comparing this against YouTube Studio must see the same term.
	LifeCycleStatus string `json:"lifeCycleStatus"`
	// ContentDetailsLocked is true once the broadcast has left created/ready.
	ContentDetailsLocked bool `json:"contentDetailsLocked"`
	// LockedReason is shown next to the disabled controls. Empty when nothing
	// is locked.
	LockedReason string `json:"lockedReason,omitempty"`
}

// contentDetailsFrozen reports whether the toggles have locked.
//
// created and ready are the editable states. Everything else -- testing, live,
// complete -- is past the point where YouTube accepts changes. Written as an
// allowlist rather than a denylist on purpose: a lifecycle value this build has
// never heard of should read as LOCKED, because offering an edit that fails is
// worse than withholding one that might have worked.
func contentDetailsFrozen(lifeCycleStatus string) bool {
	switch strings.ToLower(strings.TrimSpace(lifeCycleStatus)) {
	case "created", "ready":
		return false
	default:
		return true
	}
}

// BroadcastWindow reads the current broadcast and reports what is still
// editable.
func (y *YouTube) BroadcastWindow(ctx context.Context, accessToken string) (*BroadcastWindow, error) {
	b, err := y.liveBroadcast(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	w := &BroadcastWindow{
		BroadcastID:     b.ID,
		Title:           b.Snippet.Title,
		LifeCycleStatus: b.Status.LifeCycleStatus,
	}
	if contentDetailsFrozen(b.Status.LifeCycleStatus) {
		w.ContentDetailsLocked = true
		w.LockedReason = fmt.Sprintf(
			"this broadcast is %q, and YouTube stops accepting DVR, auto-start, auto-stop "+
				"and monitor-stream changes once a broadcast leaves \"created\" or \"ready\"",
			b.Status.LifeCycleStatus)
	}
	return w, nil
}

// PushBroadcastSettings writes the settings above.
//
// Two calls to two resources, because YouTube puts them in two places:
// scheduling and the contentDetails toggles are on the BROADCAST, while
// category and tags are on the VIDEO of the same id.
func (y *YouTube) PushBroadcastSettings(ctx context.Context, clientID, accessToken string, s BroadcastSettings) (*MetadataResult, error) {
	if s.Empty() {
		return &MetadataResult{}, nil
	}
	b, err := y.liveBroadcast(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	res := &MetadataResult{Target: b.Snippet.Title}

	if s.ScheduledStart != nil || s.TouchesContentDetails() {
		if err := y.writeBroadcastParts(ctx, accessToken, b, s, res); err != nil {
			return nil, err
		}
	}
	if s.Tags != nil {
		// Reported rather than fatal: the broadcast write above may already
		// have landed, and failing the whole push would send the operator back
		// to redo work that took.
		if err := y.setTags(ctx, accessToken, b.ID, *s.Tags); err != nil {
			res.Skipped = append(res.Skipped, FieldTags)
			res.Warnings = append(res.Warnings, "tags: "+err.Error())
		} else {
			res.Applied = append(res.Applied, FieldTags)
		}
	}
	return res, nil
}

// writeBroadcastParts sends liveBroadcasts.update.
//
// This is where traps 1 and 2 live. The request ALWAYS carries the four
// required properties, and always carries every field of a part it sends --
// including ones the operator did not touch, read back from the broadcast --
// because omitting a field from a part reverts that field to its default.
func (y *YouTube) writeBroadcastParts(ctx context.Context, accessToken string, b *ytBroadcast, s BroadcastSettings, res *MetadataResult) error {
	// Start from what the broadcast currently is, then overlay the operator's
	// changes. Never the other way round.
	start := b.Snippet.ScheduledStartTime
	if s.ScheduledStart != nil {
		start = *s.ScheduledStart
	}
	if strings.TrimSpace(start) == "" {
		// liveBroadcasts.update rejects a missing scheduledStartTime, and a
		// broadcast that is already live legitimately has none stored. Saying
		// so beats YouTube's own message, which names the property and not the
		// reason.
		return fmt.Errorf("this broadcast has no scheduled start time, and YouTube requires one on " +
			"every broadcast update; set a start time in YouTube Studio, or push only tags, " +
			"which go to a different resource and do not need it")
	}

	monitor := b.ContentDetails.MonitorStream.EnableMonitorStream
	if s.EnableMonitorStream != nil {
		monitor = *s.EnableMonitorStream
	}
	delay := b.ContentDetails.MonitorStream.BroadcastStreamDelayMs
	if s.StreamDelayMs != nil {
		delay = *s.StreamDelayMs
	}

	// Note what this means when the broadcast is already LIVE and the operator
	// only changed the schedule: trap 1 forces contentDetails onto the request
	// anyway, and contentDetails is the part that freezes.
	//
	// It still works, and only because of the read-then-write above. YouTube's
	// ModificationNotAllowed refusals fire on a CHANGED value, not on the
	// presence of the property, so carrying the current values through sends a
	// contentDetails that asks for nothing new and is accepted. An
	// implementation that defaulted these to false instead would send a real
	// change, be refused, and look like YouTube rejecting a title edit.
	body := map[string]any{
		"id": b.ID,
		// part=snippet carries EVERY snippet field this resource has, not just
		// the changed one. Trap 2.
		"snippet": map[string]any{
			"title":              b.Snippet.Title,
			"description":        b.Snippet.Description,
			"scheduledStartTime": start,
		},
		"contentDetails": map[string]any{
			"monitorStream": map[string]any{
				"enableMonitorStream":    monitor,
				"broadcastStreamDelayMs": delay,
			},
		},
	}
	parts := "snippet,contentDetails"

	cd := body["contentDetails"].(map[string]any)
	// The freezing toggles, carried through from the broadcast when untouched
	// for the same reason as everything else in this function.
	cd["enableDvr"] = pickBool(s.EnableDvr, b.ContentDetails.EnableDvr)
	cd["enableAutoStart"] = pickBool(s.EnableAutoStart, b.ContentDetails.EnableAutoStart)
	cd["enableAutoStop"] = pickBool(s.EnableAutoStop, b.ContentDetails.EnableAutoStop)

	if err := requestJSON(ctx, http.MethodPut,
		y.apiEndpoint()+"/liveBroadcasts?part="+parts, accessToken, body, nil, nil); err != nil {
		return broadcastWriteAdvice(err, b.Status.LifeCycleStatus, s)
	}
	if s.ScheduledStart != nil {
		res.Applied = append(res.Applied, FieldScheduledStart)
	}
	if s.TouchesContentDetails() {
		res.Applied = append(res.Applied, FieldContentDetails)
	}
	return nil
}

// setTags replaces the video's keywords.
//
// Deliberately the same read-modify-write shape as setCategory next door, and
// reusing its ytVideoSnippet: videos.update replaces the WHOLE snippet part, so
// sending only tags would erase the title, the description and the category
// this build may have just written. That is trap 2 again, on a second
// resource.
//
// Tags replace rather than merge, because snippet.tags[] has no add operation.
// The composer therefore has to show the current tags before an edit, or an
// operator adding one keyword silently drops the rest.
func (y *YouTube) setTags(ctx context.Context, accessToken, videoID string, tags []string) error {
	var current struct {
		Items []struct {
			Snippet ytVideoSnippet `json:"snippet"`
		} `json:"items"`
	}
	err := getJSON(ctx, y.apiEndpoint()+"/videos?part=snippet&id="+url.QueryEscape(videoID), accessToken, nil, &current)
	if err != nil {
		return err
	}
	if len(current.Items) == 0 {
		return fmt.Errorf("YouTube did not return the broadcast's video, so its tags were left alone")
	}
	snip := current.Items[0].Snippet
	snip.Tags = tags

	// A snippet with no categoryId is rejected, and a video that has never had
	// one comes back empty. Said here rather than letting YouTube blame the
	// video for a field the operator never saw.
	if strings.TrimSpace(snip.CategoryID) == "" {
		return fmt.Errorf("YouTube requires a category on every video update and this " +
			"broadcast has none; set a category before pushing tags")
	}
	err = requestJSON(ctx, http.MethodPut, y.apiEndpoint()+"/videos?part=snippet", accessToken,
		map[string]any{"id": videoID, "snippet": snip}, nil, nil)
	if err != nil {
		return scopeAdvice(err, db.PlatformYouTube, y.MetadataCaps().Scope)
	}
	return nil
}

// broadcastWriteAdvice turns YouTube's 403 into something an operator can act
// on.
//
// The documented refusals name the property and not the reason -- a bare
// "enableDvrModificationNotAllowed" tells somebody nothing about WHEN they
// could have changed it. The lifecycle status is what makes it make sense.
func broadcastWriteAdvice(err error, lifeCycleStatus string, s BroadcastSettings) error {
	msg := err.Error()
	if !strings.Contains(msg, "ModificationNotAllowed") && !strings.Contains(msg, "403") {
		return err
	}
	if !s.TouchesContentDetails() {
		return err
	}
	return fmt.Errorf("%w — this broadcast is %q, and YouTube freezes DVR, auto-start, "+
		"auto-stop and monitor-stream once a broadcast leaves \"created\" or \"ready\". "+
		"Change these before going live, or start a new broadcast",
		err, lifeCycleStatus)
}

func pickBool(override *bool, current bool) bool {
	if override != nil {
		return *override
	}
	return current
}
