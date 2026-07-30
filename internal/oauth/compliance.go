package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The compliance TYPES live in internal/db, not here.
//
// internal/oauth already imports internal/db, so putting them here and having
// the destination row reference them would be a cycle. The data lives with the
// row that stores it; the API calls that write it live here, with the platform
// they belong to.

// twitchLabelPayload renders the WRITE shape.
//
// Sorted so the request body is deterministic, which is what makes it
// assertable in a test and diffable in a log.
func twitchLabelPayload(labels map[string]bool) []map[string]any {
	ids := make([]string, 0, len(labels))
	for id := range labels {
		if db.ValidTwitchLabel(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"id": id, "is_enabled": labels[id]})
	}
	return out
}

// PushCompliance writes YouTube's privacy status and COPPA declaration.
//
// Two calls, to two different endpoints, because YouTube puts them in two
// different places:
//
//   - privacyStatus is on the BROADCAST, liveBroadcasts.update part=status.
//   - selfDeclaredMadeForKids is settable on liveBroadcasts.insert and is
//     absent from update's settable list, so for a broadcast that already
//     exists it has to go through videos.update against the broadcast id.
//     Anyone who assumes symmetry here writes a call that returns 200 and
//     changes nothing.
func (y *YouTube) PushCompliance(ctx context.Context, clientID, accessToken string, c db.Compliance) (*MetadataResult, error) {
	if c.Empty() {
		return &MetadataResult{}, nil
	}
	b, err := y.liveBroadcast(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	res := &MetadataResult{Target: b.Snippet.Title}

	if c.Privacy != db.PrivacyUnchanged {
		// part=status ALWAYS carries privacyStatus. Sending the part without
		// it does not leave the current value alone -- YouTube documents that
		// it "will remove the existing privacy setting and revert to the
		// default", which is how a private broadcast becomes public.
		body := map[string]any{
			"id":     b.ID,
			"status": map[string]any{"privacyStatus": string(c.Privacy)},
		}
		if err := requestJSON(ctx, http.MethodPut, ytAPIBase+"/liveBroadcasts?part=status",
			accessToken, body, nil, nil); err != nil {
			return nil, scopeAdvice(err, db.PlatformYouTube, y.MetadataCaps().Scope)
		}
		res.Applied = append(res.Applied, FieldPrivacy)
	}

	if c.MadeForKids != nil {
		// videos.update, not liveBroadcasts.update. Same id, different
		// resource, and the only place this field is writable after insert.
		body := map[string]any{
			"id":     b.ID,
			"status": map[string]any{"selfDeclaredMadeForKids": *c.MadeForKids},
		}
		if err := requestJSON(ctx, http.MethodPut, ytAPIBase+"/videos?part=status",
			accessToken, body, nil, nil); err != nil {
			// Reported rather than fatal: the privacy change above may already
			// have landed, and failing the whole push would send the operator
			// back to redo work that took.
			res.Skipped = append(res.Skipped, FieldMadeForKids)
			res.Warnings = append(res.Warnings, "made-for-kids: "+err.Error())
		} else {
			res.Applied = append(res.Applied, FieldMadeForKids)
		}
	}
	return res, nil
}

// PushCompliance writes Twitch's content classification labels.
func (t *Twitch) PushCompliance(ctx context.Context, clientID, accessToken, accountRef string, c db.Compliance) (*MetadataResult, error) {
	if len(c.Labels) == 0 {
		return &MetadataResult{}, nil
	}
	if accountRef == "" {
		return nil, fmt.Errorf("this Twitch account has no broadcaster id recorded; reconnect it in Settings → Platforms")
	}
	payload := twitchLabelPayload(c.Labels)
	if len(payload) == 0 {
		// Every label was one Twitch will not write. Validation refuses this at
		// save time, so reaching here means a stored row predates the rules.
		return &MetadataResult{Skipped: []MetadataField{FieldLabels}}, nil
	}

	body := map[string]any{"content_classification_labels": payload}
	endpoint := twitchHelixBase + "/channels?broadcaster_id=" + url.QueryEscape(accountRef)
	if err := requestJSON(ctx, http.MethodPatch, endpoint, accessToken, body,
		helixHeaders(clientID), nil); err != nil {
		return nil, scopeAdvice(err, db.PlatformTwitch, t.MetadataCaps().Scope)
	}
	return &MetadataResult{Applied: []MetadataField{FieldLabels}}, nil
}

// firstNonEmptyLabel is a small helper for rendering, kept here so the UI and
// the log describe a label set the same way.
func LabelSummary(labels map[string]bool) string {
	on := make([]string, 0, len(labels))
	for id, enabled := range labels {
		if enabled && db.ValidTwitchLabel(id) {
			on = append(on, id)
		}
	}
	sort.Strings(on)
	if len(on) == 0 {
		return "none"
	}
	return strings.Join(on, ", ")
}
