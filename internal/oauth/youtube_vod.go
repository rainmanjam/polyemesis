package oauth

// The two things polyemesis does to a YouTube broadcast AFTER it has ended:
// change the archive's privacy, and file the archive in a playlist.
//
// A file of their own rather than more of youtube_broadcast.go, because those
// calls edit something that is still a BROADCAST, while both of these address
// the VIDEO the broadcast left behind. That difference is not cosmetic: one of
// them can delete metadata off a finished stream, and the other is not
// idempotent and has no documented moment at which it starts working.
//
// Every endpoint, scope, refusal and quota figure below is from
// docs/evidence/platform-lifecycle-apis-2026-08-16.md (pages read 2026-08-16).
// Nothing here was inferred from a neighbouring endpoint.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

const ytPlaylistItemsPath = "/playlistItems"

// SetVODPrivacy changes the privacy of the video a finished broadcast left
// behind.
//
// THE REQUEST BODY IS THE WHOLE OF THIS METHOD, AND IT IS DELIBERATELY TINY.
// videos.update is destructive, and the reference says so twice, in two
// independent places. On the resource: "If your request does not specify a
// value for a property that already has a value, the property's existing value
// will be deleted." On the part parameter: "If the request body does not
// specify a value, the existing privacy setting will be removed and the video
// will revert to the default privacy setting."
//
// So this sends part=status ALONE, and never snippet. Adding snippet -- out of
// symmetry with setTags and setCategory next door, which do send it -- makes
// snippet.title and snippet.categoryId REQUIRED, and every snippet field left
// out of the body is then deleted from a broadcast somebody has already
// finished: the title, the description, the tags. The test that pins this
// reads the request body rather than trusting this comment, because the body is
// the only thing YouTube sees.
//
// This is the same call compliance.go already makes for
// status.selfDeclaredMadeForKids, in the same shape, against the same resource.
// What was missing was never the request -- it was a video id to point it at,
// which is why that one resolves the current broadcast and this one takes an
// id.
//
// What this deliberately does NOT do is read the status part back and carry the
// rest of it through, the way setTags carries the snippet through. The
// by-part destructiveness argues for exactly that, but WHICH status properties
// are writable is not established: madeForKids is read-only where
// selfDeclaredMadeForKids is not, and status.publishAt is documented to require
// privacyStatus=private and a video that "has never been published", so echoing
// one back on a public archive is a refusal waiting to happen. Writing back a
// set of fields nobody verified would be a guess in the one method where a
// guess costs an operator their video. This writes the single property the
// evidence documents; grounding a full read-modify-write needs its own evidence
// pass first.
func (y *YouTube) SetVODPrivacy(ctx context.Context, accessToken, videoID string, privacy db.PrivacyStatus) error {
	if strings.TrimSpace(videoID) == "" {
		// Refused before the call, because videos.update with no id is a request
		// whose failure mode reads as "YouTube said no" rather than "polyemesis
		// never recorded which video this was".
		return fmt.Errorf("no YouTube video id was recorded for this broadcast, so its privacy was left alone")
	}
	if privacy == db.PrivacyUnchanged {
		// "Unchanged" is expressible only as SENDING NOTHING. It cannot be sent
		// as an empty privacyStatus and it cannot be sent as a bare part=status:
		// that is the exact request the reference says removes the existing
		// setting and reverts the video to the default. So the no-op is a no
		// request at all.
		return nil
	}
	if !db.ValidPrivacy(privacy) {
		// invalidPrivacyStatus is a documented refusal, and one polyemesis can
		// see coming without spending a request on it.
		return fmt.Errorf("%q is not a YouTube privacy status; it accepts public, unlisted or private", privacy)
	}

	body := map[string]any{
		"id":     videoID,
		"status": map[string]any{"privacyStatus": string(privacy)},
	}
	if err := requestJSON(ctx, http.MethodPut, y.apiEndpoint()+ytVideosPath+"?part=status",
		accessToken, body, nil, nil); err != nil {
		// youtube.upload is NOT accepted by videos.update, unlike thumbnails.set
		// -- so an account whose grant is narrower than the youtube scope this
		// provider asks for fails here and nowhere else. scopeAdvice names the
		// scope and the button that fixes it.
		return scopeAdvice(err, db.PlatformYouTube, y.MetadataCaps().Scope)
	}
	return nil
}

// ytArchiveRetryDelays is POLYEMESIS'S OWN retry cadence, not a documented one.
//
// See AddVODToPlaylist for why a retry exists at all. The schedule is bounded
// rather than open-ended because every attempt spends quota: playlistItems.insert
// costs a documented 50 units against a 10,000-unit daily allocation shared with
// every other YouTube feature here, and "all API requests, including invalid
// requests, incur a quota cost of at least one point". A caller that waits
// forever on a video that will never appear takes title push down with it.
var ytArchiveRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}

// PlaylistAdd is what AddVODToPlaylist observed.
type PlaylistAdd struct {
	// Added is true when THIS call created the playlist item.
	Added bool
	// AlreadyPresent is true when YouTube refused the insert because the video
	// is already in the playlist. That is a success for the caller's purpose --
	// the playlist contains the video -- and it is reported separately rather
	// than folded into Added because "we put it there" and "it was already
	// there" are different things to log, and only one of them spent 50 units
	// on a write.
	AlreadyPresent bool
	// ItemID is the playlistItem id YouTube returned, empty when the insert was
	// refused as a duplicate. Absent is not an error here; there was no new item.
	ItemID string
	// Attempts is how many inserts were sent, so a caller can log that the
	// archive took three tries to become addressable. 1 is the ordinary case.
	Attempts int
}

// AddVODToPlaylist files a finished broadcast's archive in a playlist.
//
// NOT IDEMPOTENT, and that is the fact the method is built around. A second
// insert of a video the playlist already holds is refused with
// duplicate/videoAlreadyInPlaylist -- "The video that you are trying to add to
// the playlist is already in the playlist." An operator pressing the button
// twice, or any caller retrying a timed-out request, would otherwise be shown a
// failure for a playlist that contains exactly what was asked for. So that one
// refusal is reported as AlreadyPresent with no error: the desired state holds,
// and pretending otherwise would send somebody to fix something that is fine.
//
// THE RETRY IS OURS, NOT YOUTUBE'S. Nothing documents how soon after a
// broadcast completes its archive video id becomes a valid playlistItems
// target, and an eager call is expected to be refused with videoNotFound. This
// waits and tries again because the alternative is silently losing the
// file-it-away step for every caller that acts promptly -- an engineering
// necessity, not a guarantee anybody published. If YouTube ever documents a
// settling time, this schedule should be replaced by it rather than tuned
// against it.
//
// snippet.position is never sent. It is optional, and manualSortRequired is
// documented to be fixed "by removing the snippet.position element" -- an
// element that was never added cannot cause it.
//
// requestJSON rather than postJSON, and not for the method: postJSON flattens a
// refusal into a message TRUNCATED at 300 characters, and the reason string is
// what decides whether this returns success, waits, or gives up. metadata.go's
// statusError.full documents the live bug that truncation already caused once.
func (y *YouTube) AddVODToPlaylist(ctx context.Context, accessToken, playlistID, videoID string) (*PlaylistAdd, error) {
	if strings.TrimSpace(playlistID) == "" {
		return nil, fmt.Errorf("no YouTube playlist was chosen, so the broadcast was not added to one")
	}
	if strings.TrimSpace(videoID) == "" {
		return nil, fmt.Errorf("no YouTube video id was recorded for this broadcast, so it was not added to a playlist")
	}

	body := map[string]any{
		"snippet": map[string]any{
			"playlistId": playlistID,
			// kind is REQUIRED inside resourceId and identifies which sort of
			// thing the id names; a playlist can hold more than videos.
			"resourceId": map[string]any{"kind": "youtube#video", "videoId": videoID},
		},
	}

	delays := y.vodRetry
	if delays == nil {
		delays = ytArchiveRetryDelays
	}

	res := &PlaylistAdd{}
	var lastErr error
	for attempt := 0; ; attempt++ {
		var created struct {
			ID string `json:"id"`
		}
		res.Attempts = attempt + 1
		err := requestJSON(ctx, http.MethodPost,
			y.apiEndpoint()+ytPlaylistItemsPath+"?part=snippet", accessToken, body, nil, &created)
		if err == nil {
			res.Added, res.ItemID = true, created.ID
			return res, nil
		}
		if ytHasReason(err, "videoAlreadyInPlaylist") {
			res.AlreadyPresent = true
			return res, nil
		}
		lastErr = err
		// Only videoNotFound is waited on. Every other refusal is answered the
		// same way by a second attempt as by the first, and retrying it would
		// spend quota to reproduce it.
		if !ytHasReason(err, "videoNotFound") || attempt >= len(delays) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delays[attempt]):
		}
	}
	return nil, ytPlaylistAdvice(lastErr, y.MetadataCaps().Scope, playlistID, videoID, res.Attempts)
}

// ytHasReason reports whether a refusal carries a named reason.
//
// STRUCTURED ONLY, never a substring search of the body. A substring match for
// "videoAlreadyInPlaylist" would also fire on an error whose prose merely
// mentions it, and this function's answer decides whether a failed write is
// reported to the operator as a success. A body that will not decode yields no
// reason, which surfaces the error rather than guessing at it.
func ytHasReason(err error, reason string) bool {
	se, ok := err.(*statusError)
	if !ok {
		return false
	}
	var body struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(se.payload()), &body) != nil {
		return false
	}
	for _, e := range body.Error.Errors {
		if strings.EqualFold(strings.TrimSpace(e.Reason), reason) {
			return true
		}
	}
	return false
}

// ytPlaylistAdvice turns the documented playlist refusals into something an
// operator can act on. YouTube's own wording names the condition and not the
// remedy, and two of these three are not the operator's mistake at all.
//
// Only the refusals the evidence file could actually surface are handled.
// playlistIdRequired, channelIdRequired and resourceIdRequired were WITHDRAWN --
// they could not be found on the page -- so nothing here pretends to recognise
// them.
func ytPlaylistAdvice(err error, scope, playlistID, videoID string, attempts int) error {
	switch {
	case ytHasReason(err, "playlistOperationUnsupported"):
		return fmt.Errorf("YouTube does not allow adding videos to that playlist (%s) — a channel's "+
			"Uploads playlist is maintained by YouTube and is not a valid target. Choose a playlist "+
			"you created: %w", playlistID, err)
	case ytHasReason(err, "playlistContainsMaximumNumberOfVideos"):
		// NO NUMBER APPEARS HERE ON PURPOSE. YouTube documents THAT a playlist
		// fills up and never says at what size, so quoting a figure would put an
		// invented limit in front of the operator -- the most-repeated mistake in
		// this repository's history. The remedy is the same whatever the number is.
		return fmt.Errorf("that YouTube playlist is full — YouTube refuses further additions to it "+
			"and publishes no figure for the limit, so there is none to show here. Add the "+
			"broadcast to another playlist: %w", err)
	case ytHasReason(err, "videoNotFound"):
		return fmt.Errorf("YouTube still does not recognise video %s after %d attempts, so it was not "+
			"added to playlist %s. A just-finished broadcast's archive is not immediately addressable "+
			"and nothing documents how long it takes, so polyemesis waits and retries rather than "+
			"claiming to know; this one outlasted that wait. Add it by hand, or try again later: %w",
			videoID, attempts, playlistID, err)
	}
	return scopeAdvice(err, db.PlatformYouTube, scope)
}
