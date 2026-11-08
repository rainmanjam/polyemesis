package oauth

import (
	"context"
	"strings"
)

// StreamSummary is one reusable ingest stream of a YouTube channel as the stream
// picker shows it.
//
// IT CARRIES NO KEY AND NO INGEST ADDRESS, and that is the point of having a type
// of its own rather than reusing ytLiveStream: the picker's response is built from
// this struct, so a credential cannot reach the wire by a field being added to a
// shared type. MatchedKeys is the only trace of the keys, as indexes into the slice
// the caller supplied, and it is not serialised.
type StreamSummary struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Resolution    string `json:"resolution,omitempty"`
	FrameRate     string `json:"frameRate,omitempty"`
	IngestionType string `json:"ingestionType,omitempty"`
	// StreamStatus is YouTube's streamStatus: created, ready, active, inactive or error.
	StreamStatus string `json:"streamStatus,omitempty"`
	// Health is status.healthStatus.status: good, ok, bad or noData.
	Health string `json:"health,omitempty"`
	// MatchedKeys are the indexes of the supplied held keys that belong to this stream.
	MatchedKeys []int `json:"-"`
}

// StreamLister is the optional capability of listing a channel's reusable streams.
type StreamLister interface {
	Provider
	// ListStreams lists the channel's reusable RTMP streams. heldKeys are stream keys the caller holds;
	// each stream reports which of them are its own (StreamSummary.MatchedKeys) so the caller can say which
	// stream a destination publishes to without the key ever leaving this package.
	ListStreams(ctx context.Context, accessToken string, heldKeys []string) ([]StreamSummary, error)
}

var _ StreamLister = (*YouTube)(nil)

// ListStreams implements StreamLister: one liveStreams.list (1 unit a page).
func (y *YouTube) ListStreams(ctx context.Context, accessToken string, heldKeys []string) ([]StreamSummary, error) {
	streams, err := y.listStreamsParts(ctx, accessToken, "id,snippet,cdn,status,contentDetails")
	if err != nil {
		return nil, err
	}
	out := make([]StreamSummary, 0, len(streams))
	for _, s := range streams {
		if !strings.EqualFold(s.CDN.IngestionType, "rtmp") {
			continue
		}
		if s.ContentDetails.IsReusable != nil && !*s.ContentDetails.IsReusable {
			continue
		}
		sum := StreamSummary{
			ID: s.ID, Title: s.Snippet.Title, Resolution: s.CDN.Resolution, FrameRate: s.CDN.FrameRate,
			IngestionType: s.CDN.IngestionType, StreamStatus: s.Status.StreamStatus, Health: s.Status.HealthStatus.Status,
		}
		if name := s.CDN.IngestionInfo.StreamName; name != "" {
			for i, k := range heldKeys {
				if k == name {
					sum.MatchedKeys = append(sum.MatchedKeys, i)
				}
			}
		}
		out = append(out, sum)
	}
	return out, nil
}
