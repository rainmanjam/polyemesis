package api

// A YouTube destination's broadcast is opened (inserted and bound) BEFORE its FFmpeg is allowed to publish.
//
// WHY. YouTube's auto-start fires on the bound stream's inactive-to-active edge. A broadcast inserted and bound AFTER
// video is already flowing never sees that edge, so it sits "ready" with an active stream and never goes live (found
// live on staging: the broadcast was bound 5 seconds after video arrived). OBS creates and binds first, then sends.
//
// HOW. It rides the start path #1041 built for Facebook (renewFacebookKeyLocked, called by Start, Start all, Restart,
// a save that enables the destination and, through prepareDestinationStarts, scheduled starts), under the same
// per-destination lock, so a double start cannot open twice and the open finishes before the reconcile spawns FFmpeg.
//
// WHAT IT NEVER DOES. A supervisor respawn does not pass through here (the process is in use, and a respawn is not a
// new start), a destination that already names a broadcast is left alone, and a failed or slow open never blocks the
// start: it is bounded by youtubeOpenBeforeStartTimeout, the destination starts anyway, and the coordinator opens the
// broadcast on its next sweep as it always did (then the fallback to live, not testing, moves it).

import (
	"context"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// youtubeOpenBeforeStartTimeout bounds the open a clicked start waits for. A var so a test can shorten it.
var youtubeOpenBeforeStartTimeout = 10 * time.Second

// openYouTubeBeforeStart opens row's broadcast now when a new start of it needs one, and returns a warning for the
// response (always empty: a failed open is logged and left to the coordinator). THE CALLER HOLDS freshKey.lock(id).
func (s *Server) openYouTubeBeforeStart(ctx context.Context, row *db.Destination) string {
	if s.lifecycle == nil || row == nil || !row.YouTubeLiveNow() || row.AccountID == nil {
		return ""
	}
	if s.destinationInUse(row.ID) {
		return "" // publishing, respawning or draining: not a new start
	}
	if lifecycleBroadcastID(row) != "" {
		return "" // already has a broadcast (announced, or opened and not yet aired)
	}
	now := time.Now()
	if s.freshKey.renewedWithin(row.ID, now, pendingStartWindow) {
		return "" // a start already in flight opened it moments ago
	}
	creator, ok := oauth.As[oauth.BroadcastCreator](s.providers, db.PlatformYouTube)
	if !ok {
		return ""
	}
	s.freshKey.note(row.ID, now)
	octx, cancel := context.WithTimeout(ctx, youtubeOpenBeforeStartTimeout)
	defer cancel()
	s.lifecycle.open(octx, row, creator)
	if cur, err := s.store.GetDestination(row.ID); err == nil && cur != nil && lifecycleBroadcastID(cur) == "" {
		s.log.Warn("start: could not open the YouTube broadcast before video; starting anyway, and the coordinator "+
			"will open it", "destination", row.ID)
	}
	return ""
}
