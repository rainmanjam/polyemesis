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
	"strings"
)

// YouTubePoster sends caption text to a YouTube caption ingestion URL, the HTTP
// POST path the #8 spike found documented.
//
// PROVISIONAL: the body is whatever the caller passes, which in production is
// YouTubeCaptionBody (the one function that decides the format), and the
// sequence number goes in the query. Neither is documented by Google (docs-youtube
// C8), so a live ingest may reject them. The embedded CEA-608 path is the working
// fallback until a live check passes.
type YouTubePoster struct {
	client *http.Client
	// allowHost decides whether the ingest host may receive the signed request.
	// Production leaves it at ingestHostAllowed. Tests replace it to reach a
	// loopback server.
	allowHost func(host string) bool
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
	// The shallow copy keeps the caller's transport and timeouts. Only the
	// redirect policy changes, so a 3xx never carries the signed query onward.
	copied := *client
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client = &copied
	return &YouTubePoster{client: client, ingest: ingestionURL, allowHost: ingestHostAllowed}
}

// Post sends one request body with its sequence number. The sequence
// must increase by one per batch, which the ingest uses to order them.
func (p *YouTubePoster) Post(ctx context.Context, seq int, text string) error {
	u, err := url.Parse(p.ingest)
	if err != nil || u.Scheme != "https" {
		// Deliberately no detail: the only useful fact is the scheme, and the
		// URL itself stays out of the message.
		return fmt.Errorf("caption ingestion URL must be https")
	}
	if p.allowHost == nil || !p.allowHost(u.Hostname()) {
		return fmt.Errorf("caption ingestion host is not a YouTube ingest host")
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

// ingestHostAllowed reports whether a host may receive a signed caption request.
// The allowlist is the YouTube ingest hosts: upload.youtube.com and its
// subdomains. Any other host, including one that merely ends in youtube.com
// inside a longer name, is refused. PROVISIONAL: the documented host is taken
// from the YouTube caption guide, and the live check on a test channel confirms
// the ingestion URL's host before relying on it.
func ingestHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "upload.youtube.com" || strings.HasSuffix(host, ".upload.youtube.com")
}
