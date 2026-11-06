package oauth

import (
	"context"
	"net/url"
)

// The audience sampler's two YouTube reads (PR #950 review, item 3).
//
// Stats is three list calls -- channels.list for the channel id,
// liveBroadcasts.list for the live video, videos.list for the count -- and a
// sampler that asks every minute does not need two of them on most ticks:
// the channel id is the account ref it already holds, and the live video id
// does not change while the broadcast runs. These split the read so the
// caller can find the video once and then spend ONE unit a tick on
// videos.list against the id it remembered.

// ActiveBroadcast is the channel's live broadcast id and title, or "" when it
// has none. One liveBroadcasts.list (1 unit). channelRef is the channel id the
// account was connected as; a broadcast reporting another channel is skipped,
// for the reason Stats gives.
func (y *YouTube) ActiveBroadcast(ctx context.Context, accessToken, channelRef string) (string, string, error) {
	var out struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				ChannelID string `json:"channelId"`
				Title     string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	err := y.getJSON(ctx,
		y.apiEndpoint()+ytBroadcastsPath+"?part=id,snippet&broadcastStatus=active&broadcastType=all",
		accessToken, nil, &out)
	if err != nil {
		return "", "", err
	}
	for _, b := range out.Items {
		if b.Snippet.ChannelID != "" && channelRef != "" && b.Snippet.ChannelID != channelRef {
			continue
		}
		return b.ID, b.Snippet.Title, nil
	}
	return "", "", nil
}

// LiveVideo is one videos.list answer about a live video.
type LiveVideo struct {
	// Found is false when the video is gone: deleted, or no longer visible.
	Found bool
	// Ended is set once the broadcast has an actualEndTime.
	Ended bool
	// Viewers is nil when YouTube sent no concurrentViewers: hidden by the
	// owner, nobody watching, or the broadcast over. Never 0 for "not told".
	Viewers *int
}

// LiveViewers reads one video's live details. One videos.list (1 unit).
func (y *YouTube) LiveViewers(ctx context.Context, accessToken, videoID string) (LiveVideo, error) {
	var out struct {
		Items []struct {
			LiveStreamingDetails struct {
				ConcurrentViewers ytConcurrentViewers `json:"concurrentViewers"`
				ActualEndTime     string              `json:"actualEndTime"`
			} `json:"liveStreamingDetails"`
		} `json:"items"`
	}
	err := y.getJSON(ctx,
		y.apiEndpoint()+ytVideosPath+"?part=liveStreamingDetails&id="+url.QueryEscape(videoID),
		accessToken, nil, &out)
	if err != nil {
		return LiveVideo{}, err
	}
	if len(out.Items) == 0 {
		return LiveVideo{}, nil
	}
	d := out.Items[0].LiveStreamingDetails
	v := LiveVideo{Found: true, Ended: d.ActualEndTime != ""}
	if d.ConcurrentViewers.present && !v.Ended {
		n := d.ConcurrentViewers.value
		v.Viewers = &n
	}
	return v, nil
}
