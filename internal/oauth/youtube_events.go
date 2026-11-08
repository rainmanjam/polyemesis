package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// EventSummary is one upcoming or active YouTube broadcast ("event") of the channel, as the event picker shows it.
//
// Like StreamSummary it carries no key and no ingest address: BoundStreamID is the stream's id, and whether that
// stream is one of the caller's held keys travels as BoundToHeld, an index into the slice the caller supplied.
type EventSummary struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	ScheduledStart string `json:"scheduledStart,omitempty"`
	Privacy        string `json:"privacy,omitempty"`
	// Status is YouTube's lifeCycleStatus: created, ready, testing or live (a finished event is not listed).
	Status string `json:"status"`
	// EnableAutoStart and EnableAutoStop are the event's own flags, reported as they are and never changed here.
	EnableAutoStart bool   `json:"enableAutoStart"`
	EnableAutoStop  bool   `json:"enableAutoStop"`
	BoundStreamID   string `json:"boundStreamId,omitempty"`
	// StreamActive is whether the bound stream is receiving video; nil when the event is bound to no stream.
	StreamActive *bool `json:"streamActive,omitempty"`
	// BoundToHeld are the indexes of the supplied held keys whose stream this event is bound to.
	BoundToHeld []int `json:"-"`
}

// EventManager is the optional capability of reading a channel's events and turning auto-start on for one.
type EventManager interface {
	Provider
	// ListEvents lists the channel's upcoming and active broadcasts: two liveBroadcasts.list calls (broadcastStatus
	// is a filter that excludes mine, so one call each) and one liveStreams.list for the streams' state.
	ListEvents(ctx context.Context, accessToken string, heldKeys []string) ([]EventSummary, error)
	// SetEventAutoStart turns enableAutoStart on for one event. It is a write the operator asked for by clicking.
	SetEventAutoStart(ctx context.Context, accessToken, eventID string) error
}

var _ EventManager = (*YouTube)(nil)

// ListEvents implements EventManager.
func (y *YouTube) ListEvents(ctx context.Context, accessToken string, heldKeys []string) ([]EventSummary, error) {
	streams, err := y.listStreamsParts(ctx, accessToken, "id,snippet,cdn,status")
	if err != nil {
		return nil, err
	}
	var out []EventSummary
	seen := map[string]bool{}
	for _, status := range []string{"upcoming", "active"} {
		q := url.Values{}
		q.Set("part", "id,snippet,status,contentDetails")
		q.Set("broadcastStatus", status)
		q.Set("broadcastType", "all")
		q.Set("maxResults", "50")
		var list struct {
			Items []ytBroadcast `json:"items"`
		}
		if err := y.getJSON(ctx, y.apiEndpoint()+ytBroadcastsPath+"?"+q.Encode(), accessToken, nil, &list); err != nil {
			return nil, err
		}
		for _, b := range list.Items {
			if seen[b.ID] {
				continue
			}
			seen[b.ID] = true
			switch strings.ToLower(b.Status.LifeCycleStatus) {
			case "complete", "revoked":
				continue
			}
			ev := EventSummary{
				ID: b.ID, Title: b.Snippet.Title, ScheduledStart: b.Snippet.ScheduledStartTime,
				Privacy: b.Status.PrivacyStatus, Status: b.Status.LifeCycleStatus,
				EnableAutoStart: b.ContentDetails.EnableAutoStart, EnableAutoStop: b.ContentDetails.EnableAutoStop,
				BoundStreamID: b.ContentDetails.BoundStreamID,
			}
			for i := range streams {
				if streams[i].ID != ev.BoundStreamID || ev.BoundStreamID == "" {
					continue
				}
				active := strings.EqualFold(streams[i].Status.StreamStatus, "active")
				ev.StreamActive = &active
				for k, key := range heldKeys {
					if key != "" && key == streams[i].CDN.IngestionInfo.StreamName {
						ev.BoundToHeld = append(ev.BoundToHeld, k)
					}
				}
			}
			out = append(out, ev)
		}
	}
	return out, nil
}

// SetEventAutoStart implements EventManager. It reads the event, refuses one on another channel or past
// created/ready (YouTube freezes the toggle after that), and writes through writeBroadcastParts, which carries
// every other field of the part unchanged so the update does not reset them.
func (y *YouTube) SetEventAutoStart(ctx context.Context, accessToken, eventID string) error {
	if strings.TrimSpace(eventID) == "" {
		return errors.New("no YouTube event id")
	}
	b, err := y.broadcastByID(ctx, accessToken, eventID)
	if err != nil {
		return err
	}
	switch strings.ToLower(b.Status.LifeCycleStatus) {
	case "created", "ready":
	default:
		return fmt.Errorf("this event is %s, and YouTube accepts no change to auto-start once an event is testing, "+
			"live or finished", b.Status.LifeCycleStatus)
	}
	if cid := strings.TrimSpace(b.Snippet.ChannelID); cid != "" {
		acct, aerr := y.Account(ctx, "", accessToken)
		if aerr != nil {
			return aerr
		}
		if cid != acct.Ref {
			return fmt.Errorf("event %s belongs to YouTube channel %s, but this connection is to channel %s; "+
				"polyemesis will not change another channel's event", eventID, cid, acct.Ref)
		}
	}
	on := true
	return y.writeBroadcastParts(ctx, accessToken, b, BroadcastSettings{EnableAutoStart: &on}, &MetadataResult{})
}
