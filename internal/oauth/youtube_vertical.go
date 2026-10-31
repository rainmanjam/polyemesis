package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// YouTube paired vertical (#7): the 9:16 companion of a horizontal broadcast.
//
// PROVISIONAL, AND THE SPIKE SAYS WHY. The Live Streaming API has no property that
// attaches a vertical companion to a broadcast, and a broadcast binds to exactly one
// stream. So this file does three things the API can do and leaves the pairing to a
// human:
//
//   1. Creates the vertical liveStream on its own (CreateVerticalStream), outside
//      streamFor. It never reuses the channel's shared stream.
//   2. Reads that stream's status and health by id (VerticalStreamHealth), so the
//      vertical half can show its own sub-row.
//   3. Never binds that stream to the horizontal broadcast. The horizontal side
//      keeps using streamFor, and streamFor skips vertical streams (youtube.go).
//
// The one-time Live Control Room step, where the operator picks this stream's key
// as the vertical "second stream key", is guided in the UI and has no API equivalent.

// ytVerticalMarker is appended to every vertical stream's title. It is what lets the
// first-RTMP fallback recognise a vertical stream when no exclusion list is passed.
const ytVerticalMarker = " (vertical)"

// ytVerticalStreamTitle names a vertical stream for the operator in YouTube Studio.
// The label is cut to make room for the marker, never the marker itself. A title
// that lost its marker would look like a horizontal stream to the fallback.
func ytVerticalStreamTitle(label string) string {
	body := ytStreamTitleBase
	if l := strings.TrimSpace(label); l != "" {
		body = ytStreamTitleBase + " - " + l
	}
	room := ytStreamTitleMax - len([]rune(ytVerticalMarker))
	if r := []rune(body); len(r) > room {
		body = string(r[:room])
	}
	return body + ytVerticalMarker
}

// isVerticalStreamTitle reports whether a stream title carries the vertical marker.
func isVerticalStreamTitle(title string) bool {
	return strings.HasSuffix(title, ytVerticalMarker)
}

// containsString reports whether want is in list. It is exact, byte for byte, the same
// as the held-key comparison: a stream id is an opaque token, not text to normalise.
func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// VerticalStream is a vertical liveStream polyemesis created. Ingest carries the key
// the vertical destination publishes with. It is stored through the encrypted stream
// key path and must never be logged or placed in the pair row.
type VerticalStream struct {
	ID     string
	Ingest Ingest
}

// CreateVerticalStream provisions a new vertical RTMP liveStream and returns its id and
// ingest. It does not list the channel's streams first, and it does not reuse the
// shared one, so the vertical key is always its own.
func (y *YouTube) CreateVerticalStream(ctx context.Context, accessToken, label string) (*VerticalStream, error) {
	created := ytLiveStream{}
	payload := map[string]any{
		"snippet": map[string]any{"title": ytVerticalStreamTitle(label)},
		"cdn": map[string]any{
			"ingestionType": "rtmp",
			"resolution":    "variable",
			"frameRate":     "variable",
		},
	}
	err := postJSON(ctx,
		y.apiEndpoint()+ytStreamsPath+"?part=id,snippet,cdn",
		accessToken, payload, nil, &created)
	if err != nil {
		return nil, fmt.Errorf("could not create the YouTube vertical ingest stream: %w", err)
	}
	if created.CDN.IngestionInfo.StreamName == "" {
		return nil, fmt.Errorf("YouTube created the vertical stream but returned no stream key")
	}
	return &VerticalStream{
		ID: created.ID,
		Ingest: Ingest{
			URL: created.CDN.IngestionInfo.IngestionAddress,
			Key: created.CDN.IngestionInfo.StreamName,
		},
	}, nil
}

// VerticalStreamHealth is the vertical half's status as YouTube reports it. It holds
// no key, and it is the only shape the sub-row reads.
type VerticalStreamHealth struct {
	StreamStatus string `json:"streamStatus"`
	HealthStatus string `json:"healthStatus"`
}

// VerticalStreamHealth reads liveStreams status and health for one vertical stream id.
// Its failure is the sub-row's failure, which is why it returns an error rather than an
// empty answer: "unknown" and "healthy" must not look the same.
func (y *YouTube) VerticalStreamHealth(ctx context.Context, accessToken, streamID string) (*VerticalStreamHealth, error) {
	id := strings.TrimSpace(streamID)
	if id == "" {
		return nil, fmt.Errorf("a vertical stream id is required to read its health")
	}
	var list struct {
		Items []struct {
			ID     string `json:"id"`
			Status struct {
				StreamStatus string `json:"streamStatus"`
				HealthStatus struct {
					Status string `json:"status"`
				} `json:"healthStatus"`
			} `json:"status"`
		} `json:"items"`
	}
	err := getJSON(ctx, y.apiEndpoint()+ytStreamsPath+"?part=id,status&id="+url.QueryEscape(id),
		accessToken, nil, &list)
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("YouTube returned no stream with id %q; the vertical stream may have been deleted", id)
	}
	it := list.Items[0]
	return &VerticalStreamHealth{
		StreamStatus: it.Status.StreamStatus,
		HealthStatus: it.Status.HealthStatus.Status,
	}, nil
}
