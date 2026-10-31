package captions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// YouTubePoster sends caption text to a YouTube caption ingestion URL, the HTTP
// POST path the #8 spike found documented.
//
// PROVISIONAL: the request body is plain caption text with the sequence number
// in the query. The URL and the sequence parameter follow the documented shape,
// but the body encoding has not been checked against a live ingest, so a
// platform may reject it. The embedded CEA-608 path is the working fallback
// until a live check passes.
type YouTubePoster struct {
	client *http.Client
	// ingest is the signed URL. It is a credential: it is never placed in an
	// error, a log line or an event, only used to make the request.
	ingest string
}

// NewYouTubePoster returns a poster for one ingestion URL. A nil client uses
// the default.
func NewYouTubePoster(client *http.Client, ingestionURL string) *YouTubePoster {
	if client == nil {
		client = http.DefaultClient
	}
	return &YouTubePoster{client: client, ingest: ingestionURL}
}

// Post sends one batch of caption text with its sequence number. The sequence
// must increase by one per batch, which the ingest uses to order them.
func (p *YouTubePoster) Post(ctx context.Context, seq int, text string) error {
	u, err := url.Parse(p.ingest)
	if err != nil || u.Scheme != "https" {
		// Deliberately no detail: the only useful fact is the scheme, and the
		// URL itself stays out of the message.
		return fmt.Errorf("caption ingestion URL must be https")
	}
	q := u.Query()
	q.Set("seq", strconv.Itoa(seq))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewBufferString(text))
	if err != nil {
		return fmt.Errorf("caption POST: %s", transportMessage(err))
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("caption POST: %s", transportMessage(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("caption POST: ingest answered %d", resp.StatusCode)
	}
	return nil
}

// transportMessage is the cause of a failed request without the request. The
// HTTP client wraps its error in a url.Error that quotes the full URL, query and
// signature included, so the wrapper is unwrapped and only the cause is kept.
func transportMessage(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return "request failed"
}
