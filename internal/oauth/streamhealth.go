package oauth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
)

// StreamHealthVerdict is the platform's verdict on one of a channel's streams.
type StreamHealthVerdict struct {
	// Status is the platform's word, verbatim. For YouTube,
	// status.healthStatus.status: good, ok, bad or noData, where "good" is
	// BETTER than "ok" (docs/evidence/platform-lifecycle-apis-2026-08-16.md).
	Status string
	// Issues are the codes (configurationIssues[].type) of the issues the
	// platform rates as errors. Warnings and info are left out: an ok stream
	// with a warning is, by YouTube's own definition, ok.
	Issues []string
}

// StreamHealthReader is the optional capability to READ the platform's health
// verdict on every stream of a connected account, changing nothing.
//
// YouTube only. Twitch has no public stream-health API (Inspector is a web
// tool), and Facebook and Kick publish no verdict (docs/DESIGN-DESTINATION-
// HEALTH.md). READ-ONLY for StreamKeyReader's reason.
type StreamHealthReader interface {
	Provider
	// StreamHealths reads every stream on the channel, keyed by the SHA-256
	// of its stream key, so a caller can match its destination's key without
	// holding a second plaintext copy of anyone's.
	StreamHealths(ctx context.Context, clientID, accessToken string) (map[[32]byte]StreamHealthVerdict, error)
}

// ytHealthStream is the part of a liveStream resource the health read uses.
type ytHealthStream struct {
	CDN struct {
		IngestionInfo ytHealthIngestionInfo `json:"ingestionInfo"`
	} `json:"cdn"`
	Status struct {
		HealthStatus ytHealthStatus `json:"healthStatus"`
	} `json:"status"`
}

// ytHealthIngestionInfo is the ingestionInfo part of a liveStream's cdn block.
type ytHealthIngestionInfo struct {
	StreamName string `json:"streamName"`
}

// ytHealthStatus is the healthStatus part of a liveStream's status block.
type ytHealthStatus struct {
	Status              string `json:"status"`
	ConfigurationIssues []struct {
		Type     string `json:"type"`
		Severity string `json:"severity"`
	} `json:"configurationIssues"`
}

// healthVerdict turns a liveStream's health block into a verdict, keeping only
// the configuration issues YouTube reports as errors.
func healthVerdict(hs ytHealthStatus) StreamHealthVerdict {
	h := StreamHealthVerdict{Status: hs.Status}
	for _, is := range hs.ConfigurationIssues {
		if is.Severity == "error" && is.Type != "" {
			h.Issues = append(h.Issues, is.Type)
		}
	}
	return h
}

// StreamHealths lists the channel's liveStreams with part=cdn,status: every
// page, bounded by ytKeyPagesMax, for StreamKeys' reason -- a destination whose
// stream is on page two must not read as unjudged. Each page is one list call,
// whose quota cost Google does not publish (see internal/chat/quota.go).
// part=status is on the liveStreams.list part table, and the poller can hold the
// narrower youtube.readonly grant (the evidence file above).
func (y *YouTube) StreamHealths(ctx context.Context, _, accessToken string) (map[[32]byte]StreamHealthVerdict, error) {
	out := map[[32]byte]StreamHealthVerdict{}
	token := ""
	for page := 0; page < ytKeyPagesMax; page++ {
		var list struct {
			NextPageToken string           `json:"nextPageToken"`
			Items         []ytHealthStream `json:"items"`
		}
		u := y.apiEndpoint() + ytStreamsPath + "?part=cdn,status&mine=true&maxResults=50"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		if err := y.getJSON(ctx, u, accessToken, nil, &list); err != nil {
			return nil, err
		}
		for _, s := range list.Items {
			k := s.CDN.IngestionInfo.StreamName
			if k == "" {
				continue
			}
			out[sha256.Sum256([]byte(k))] = healthVerdict(s.Status.HealthStatus)
		}
		if list.NextPageToken == "" {
			return out, nil
		}
		token = list.NextPageToken
	}
	return nil, fmt.Errorf("the channel lists more than %d pages of live streams; not reading their health", ytKeyPagesMax)
}
