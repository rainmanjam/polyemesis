package oauth

import (
	"context"
	"fmt"
	"net/url"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// StreamKeyReader is the optional capability to READ the stream keys a
// connected account currently has, changing nothing.
//
// It exists for one question, asked when a destination's publish was refused:
// "is the key we are sending still one of this account's keys?" If it is not,
// the platform rotated it (a reset in Studio, a reconnected channel) and the
// operator's fix is one click on Refresh stream key, not a hunt through a
// creator dashboard.
//
// READ-ONLY IS THE WHOLE CONTRACT. Provider.Ingest is not usable here: on
// YouTube it falls through to creating a liveStream when the held key is gone,
// and on Facebook the key IS a live video, so "reading" one creates a public
// broadcast. A cross-check that runs because something failed must not be the
// thing that rotates a key under a working encoder. Only platforms with a
// genuinely read-only endpoint implement this; the rest are skipped and the
// check says nothing about them.
type StreamKeyReader interface {
	Provider
	// StreamKeys lists the account's current stream keys. Compared byte for
	// byte with the stored key by the caller -- no trimming, no case folding
	// (see the note on streamFor about #306).
	StreamKeys(ctx context.Context, clientID, accessToken string) ([]string, error)
}

// StreamKeysFor reports whether a platform's keys can be read without side
// effects.
func StreamKeysFor(p db.Platform) (StreamKeyReader, bool) {
	prov, err := Get(p)
	if err != nil {
		return nil, false
	}
	r, ok := prov.(StreamKeyReader)
	return r, ok
}

// StreamKeysFor is the Set twin; see the note above MetadataFor for why every
// capability needs one.
func (s Set) StreamKeysFor(p db.Platform) (StreamKeyReader, bool) {
	pr, ok := s.All()[p]
	if !ok {
		return nil, false
	}
	r, ok := pr.(StreamKeyReader)
	return r, ok
}

// StreamKeys lists every liveStream key on the channel. liveStreams.list and
// nothing else: a channel holds one stream per destination that asked for its
// own, so "the" current key is any of them, and a held key absent from all of
// them is the rotation the caller is looking for.
//
// EVERY PAGE, unlike listStreams, which reads the first 50 and is enough for
// choosing a stream. Here a key on page two read as absent would put "key
// changed" on a destination whose key is fine and send the operator to rotate
// it. Bounded at ytKeyPagesMax pages: a channel past that is answered as "cannot
// tell" (an error), never as "changed".
func (y *YouTube) StreamKeys(ctx context.Context, _, accessToken string) ([]string, error) {
	var keys []string
	token := ""
	for page := 0; page < ytKeyPagesMax; page++ {
		var list struct {
			NextPageToken string         `json:"nextPageToken"`
			Items         []ytLiveStream `json:"items"`
		}
		u := y.apiEndpoint() + ytStreamsPath + "?part=cdn&mine=true&maxResults=50"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		if err := getJSON(ctx, u, accessToken, nil, &list); err != nil {
			return nil, err
		}
		for _, s := range list.Items {
			if k := s.CDN.IngestionInfo.StreamName; k != "" {
				keys = append(keys, k)
			}
		}
		if list.NextPageToken == "" {
			return keys, nil
		}
		token = list.NextPageToken
	}
	return nil, fmt.Errorf("the channel lists more than %d pages of live streams; not checking its keys", ytKeyPagesMax)
}

// ytKeyPagesMax bounds the key read at 500 streams. liveStreams.list costs one
// quota unit per page, and the check runs at most once per destination every
// few minutes, so the bound is about a runaway loop, not about cost.
const ytKeyPagesMax = 10

// StreamKeys is the channel's one key. Get Stream Key is a read; Twitch's reset
// is a separate action in the dashboard that this never performs.
func (t *Twitch) StreamKeys(ctx context.Context, clientID, accessToken string) ([]string, error) {
	ing, err := t.Ingest(ctx, clientID, accessToken)
	if err != nil {
		return nil, err
	}
	return []string{ing.Key}, nil
}
