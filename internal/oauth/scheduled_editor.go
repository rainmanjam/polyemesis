package oauth

import (
	"context"
)

// ScheduledEditor is the optional capability a platform needs before a recurring broadcast template
// (#18) may create events on it.
//
// ScheduledBroadcaster can create a scheduled event and move it. A template needs three things more:
// to write the title, description, privacy and category of ONE named event, to attach its thumbnail,
// and to cancel it by id. Without those, a look-ahead would put an event with a placeholder title on
// the platform, which is the one outcome the feature exists to prevent. So a platform that is a
// ScheduledBroadcaster but not a ScheduledEditor is left to the legacy pre-announce path, and the
// template sweep never creates on it.
//
// Today only YouTube implements it. Facebook is PROVISIONAL: its Page API may or may not take a title
// on a scheduled live video, and nothing here has checked. X is the clean extension point the plan
// names. It implements this interface once the X lane has a scheduled-broadcast create.
type ScheduledEditor interface {
	ScheduledBroadcaster
	// EditScheduledBroadcast writes the metadata of one scheduled broadcast, addressed by id. The
	// returned warnings are the parts the platform refused that do not invalidate the rest, such as a
	// category. An error means the title write itself failed and nothing useful landed.
	EditScheduledBroadcast(ctx context.Context, accessToken, broadcastID string, e ScheduledEdit) (warnings []string, err error)
	// SetScheduledThumbnail uploads the picture for one scheduled broadcast.
	SetScheduledThumbnail(ctx context.Context, accessToken, broadcastID string, data []byte, mime string) error
	// CancelScheduledBroadcast removes one scheduled broadcast by id. A broadcast that is already
	// gone is not an error: the goal is that no event page remains.
	CancelScheduledBroadcast(ctx context.Context, accessToken, broadcastID string) error
}

// ScheduledEdit is the metadata a template writes to one occurrence's event. Title and Description are
// always written, so an empty Description clears one. Privacy and Category are sent only when set.
type ScheduledEdit struct {
	Title       string
	Description string
	Privacy     string
	Category    string
}
