package publish

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultClient is used when an adapter is given none. No overall Timeout: an
// upload of a long short on a slow link legitimately takes minutes, and the
// job's context is what bounds it.
func defaultClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       30 * time.Second,
	}}
}

// netFailure turns a transport error into a retryable Failure WITHOUT the URL.
// net/http's *url.Error quotes the full request URL, and for an upload that URL
// is a capability (an upload id or token in its query) that must never reach a
// ledger row, a job log or the UI.
func netFailure(err error) *Failure {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := "could not reach the platform"
	var to interface{ Timeout() bool }
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &to) && to.Timeout()):
		return fail(CauseUnavailable, true, "%s: the request timed out", msg)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return fail(CauseNetwork, true, "%s: the connection closed early", msg)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return fail(CauseNetwork, true, "%s: the host name did not resolve", msg)
	}
	return fail(CauseNetwork, true, "%s: %s", msg, strings.TrimSpace(err.Error()))
}

// readBody reads at most 64 KiB of a response.
func readBody(resp *http.Response) []byte {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return b
}

// statusFailure maps the statuses every platform shares.
func statusFailure(code int, detail string) *Failure {
	switch {
	case code == http.StatusUnauthorized:
		return fail(CauseTokenRejected, false, "the platform rejected the access token; reconnect the account (%s)", detail)
	case code == http.StatusTooManyRequests:
		return fail(CauseRateLimited, true, "the platform asked to slow down (%s)", detail)
	case code == http.StatusRequestTimeout || code >= 500:
		return fail(CauseUnavailable, true, "the platform was unavailable, HTTP %d (%s)", code, detail)
	}
	return fail(CauseRefused, false, "the platform refused the request, HTTP %d: %s", code, detail)
}

// oneLine trims a platform message to something safe to store and show.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

type progressReader struct {
	r    io.Reader
	done int64
	of   int64
	fn   func(float64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.fn != nil && p.of > 0 {
		p.fn(float64(p.done) / float64(p.of))
	}
	return n, err
}
