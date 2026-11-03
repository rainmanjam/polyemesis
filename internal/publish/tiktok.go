package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// TikTokPrivacy IS THE ONLY PRIVACY LEVEL THIS PACKAGE EVER SENDS TO TIKTOK.
//
// TikTok's Content Posting API holds every post from an app that has not passed
// TikTok's audit to private viewing, and a request for any other level from an
// unaudited app is refused. So rather than offer a choice that cannot be
// honoured, the adapter has no parameter for it: Normalize overwrites whatever
// the request carried, the init body below writes this constant, and creator
// info must list it as an option or the post fails permanently instead of
// falling back to another level. Lifting this is a code change made after the
// app is audited, on purpose.
const TikTokPrivacy = "SELF_ONLY"

const tikTokBase = "https://open.tiktokapis.com"

// TikTokScope is the permission Direct Post needs.
const TikTokScope = "video.publish"

const (
	tikTokChunk     = 10 << 20
	tikTokPollEvery = 3 * time.Second
	tikTokPollFor   = 4 * time.Minute
)

// TikTok publishes with the Content Posting API's Direct Post, FILE_UPLOAD
// source: creator info, init, sequential chunk PUTs, then status polling.
//
// RESUMING WITHOUT A SECOND POST: init returns a publish_id and an upload URL.
// The publish_id is stored as the row's remote id and the URL (a capability)
// sealed as its session, both before the first chunk. A retry polls the
// publish_id; it only initialises again when TikTok says the earlier attempt
// failed or never received its bytes.
type TikTok struct {
	Base   string
	Client *http.Client
	// PollEvery and PollFor tune the status loop; tests shorten them.
	PollEvery time.Duration
	PollFor   time.Duration
}

func (t *TikTok) base() string {
	if t.Base != "" {
		return strings.TrimRight(t.Base, "/")
	}
	return tikTokBase
}

func (t *TikTok) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return defaultClient()
}

// Ready implements Adapter.
func (t *TikTok) Ready(c Credential) *Failure {
	if !HasAnyScope(c.Scopes, []string{TikTokScope}) {
		return fail(CauseNeedsReconnect, false, "this TikTok connection was not granted permission to post; reconnect it to allow uploads")
	}
	return nil
}

type tkEnvelope struct {
	Data  json.RawMessage `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func tkFailure(code int, env tkEnvelope) *Failure {
	msg := oneLine(env.Error.Message)
	switch env.Error.Code {
	case "access_token_invalid", "access_token_expired", "scope_not_authorized", "token_not_authorized_for_specified_user":
		return fail(CauseNeedsReconnect, false, "TikTok says this connection may not post; reconnect it to allow uploads")
	case "rate_limit_exceeded":
		return fail(CauseRateLimited, true, "TikTok asked to slow down (%s)", msg)
	case "spam_risk_too_many_posts", "spam_risk_user_banned_from_posting":
		return fail(CauseQuota, false, "TikTok will not take more posts from this account right now (%s)", env.Error.Code)
	case "unaudited_client_can_only_post_to_private_accounts":
		return fail(CauseRefused, false, "TikTok only accepts posts from an unaudited app on an account set to private; set the TikTok account to private and try again")
	case "privacy_level_option_mismatch":
		return fail(CausePrivacyUnavailable, false, "TikTok does not offer the private option for this account")
	}
	if code >= 400 || env.Error.Code != "" {
		f := statusFailure(max(code, 400), firstNonEmpty(msg, env.Error.Code))
		return f
	}
	return fail(CauseUnknown, false, "%s", firstNonEmpty(msg, "TikTok refused the request"))
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (t *TikTok) call(ctx context.Context, token, path string, body any) (tkEnvelope, *Failure) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base()+path, rdr)
	if err != nil {
		return tkEnvelope{}, fail(CauseUnknown, false, "could not build the request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp, err := t.client().Do(req)
	if err != nil {
		return tkEnvelope{}, netFailure(err)
	}
	defer resp.Body.Close()
	raw := readBody(resp)
	var env tkEnvelope
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode/100 != 2 || (env.Error.Code != "" && env.Error.Code != "ok") {
		return env, tkFailure(resp.StatusCode, env)
	}
	return env, nil
}

type tkStatus struct {
	Status        string  `json:"status"`
	FailReason    string  `json:"fail_reason"`
	PostIDs       []int64 `json:"publicaly_available_post_id"`
	UploadedBytes int64   `json:"uploaded_bytes"`
}

// Publish implements Adapter.
func (t *TikTok) Publish(ctx context.Context, r Run) (Done, error) {
	total := r.File.Size
	if total <= 0 {
		return Done{}, fail(CauseClipMissing, false, "the exported clip is empty")
	}
	upload, _, gerr := r.Sess.Get()
	if gerr != nil {
		return Done{}, fail(CauseUnknown, true, "could not read the stored upload address, so nothing was sent")
	}

	if r.Sess.Remote != "" {
		st, f := t.status(ctx, r.Cred.AccessToken, r.Sess.Remote)
		if f != nil {
			return Done{}, f
		}
		switch st.Status {
		case "PUBLISH_COMPLETE":
			return t.done(r.Sess.Remote, st), nil
		case "FAILED":
			// Terminal on TikTok's side: nothing was posted, so a fresh init
			// cannot double-post. Forget the dead one first.
			_ = r.Sess.SaveRemote("")
			_ = r.Sess.Clear()
			upload = ""
			r.Sess.Remote = ""
		default:
			switch {
			case st.UploadedBytes >= total:
				// Every byte is in: poll, do not re-send.
				return t.poll(ctx, r, r.Sess.Remote)
			case upload == "" && !r.Pub.FinalizingAt.IsZero():
				// The final chunk may have gone out and its address has since
				// expired. TikTok is still processing this publish id; starting
				// another would be a second post. Wait for its verdict.
				return t.poll(ctx, r, r.Sess.Remote)
			case upload == "":
				// No bytes went out and there is no address to send them to: the
				// publish id can never complete, so a fresh init is safe.
				if err := r.Sess.SaveRemote(""); err != nil {
					return Done{}, fail(CauseUnknown, true, "could not forget the unusable publish id")
				}
			default:
				return t.sendAndPoll(ctx, r, upload, r.Sess.Remote, st.UploadedBytes)
			}
		}
	}

	// Creator info first: it says whether the private option exists and how long
	// a video this account may post.
	env, f := t.call(ctx, r.Cred.AccessToken, "/v2/post/publish/creator_info/query/", nil)
	if f != nil {
		return Done{}, f
	}
	var ci struct {
		Options []string `json:"privacy_level_options"`
		MaxSec  int      `json:"max_video_post_duration_sec"`
	}
	_ = json.Unmarshal(env.Data, &ci)
	ok := false
	for _, o := range ci.Options {
		if o == TikTokPrivacy {
			ok = true
		}
	}
	if !ok {
		return Done{}, fail(CausePrivacyUnavailable, false,
			"TikTok did not offer a private posting option for this account, and polyemesis posts only privately until the app is audited")
	}
	if ci.MaxSec > 0 && r.File.Seconds > float64(ci.MaxSec) {
		return Done{}, fail(CauseClipRejected, false, "this clip is %.0f seconds and this TikTok account may post up to %d", r.File.Seconds, ci.MaxSec)
	}

	chunk, count := tikTokChunks(total)
	env, f = t.call(ctx, r.Cred.AccessToken, "/v2/post/publish/video/init/", map[string]any{
		"post_info": map[string]any{
			"title":                captionFor(r.Meta),
			"privacy_level":        TikTokPrivacy,
			"brand_content_toggle": false,
			"brand_organic_toggle": false,
		},
		"source_info": map[string]any{
			"source": "FILE_UPLOAD", "video_size": total, "chunk_size": chunk, "total_chunk_count": count,
		},
	})
	if f != nil {
		return Done{}, f
	}
	var ini struct {
		PublishID string `json:"publish_id"`
		UploadURL string `json:"upload_url"`
	}
	if json.Unmarshal(env.Data, &ini) != nil || ini.PublishID == "" || ini.UploadURL == "" {
		return Done{}, fail(CauseUnavailable, true, "TikTok did not return an upload address")
	}
	// Both are stored BEFORE a byte is sent.
	if err := r.Sess.SaveRemote(ini.PublishID); err != nil {
		return Done{}, fail(CauseUnknown, true, "could not record the TikTok publish id, so nothing was sent")
	}
	if err := r.Sess.Save(ini.UploadURL, "tiktok"); err != nil {
		return Done{}, fail(CauseUnknown, true, "could not record the TikTok upload address, so nothing was sent")
	}
	return t.sendAndPoll(ctx, r, ini.UploadURL, ini.PublishID, 0)
}

// tikTokChunks follows TikTok's chunk rules: 5-64 MB chunks, the last may be
// larger, under 5 MB goes whole, and the count is the floor of size/chunk.
func tikTokChunks(total int64) (chunk int64, count int64) {
	// Under two chunks' worth, send it whole with chunk_size equal to
	// video_size, so a single chunk can never disagree with what init declared.
	if total < 2*tikTokChunk {
		return total, 1
	}
	return tikTokChunk, total / tikTokChunk
}

func (t *TikTok) sendAndPoll(ctx context.Context, r Run, uploadURL, publishID string, uploaded int64) (Done, error) {
	f, err := os.Open(r.File.Path)
	if err != nil {
		return Done{}, fail(CauseClipMissing, false, "the exported clip could not be opened")
	}
	defer f.Close()
	total := r.File.Size
	chunk, count := tikTokChunks(total)
	// Resume on a chunk boundary: chunks are sequential, and a half-received
	// chunk is re-sent whole.
	idx := uploaded / chunk
	if idx >= count {
		idx = count - 1
	}
	for ; idx < count; idx++ {
		start := idx * chunk
		end := start + chunk - 1
		if idx == count-1 {
			end = total - 1
		}
		n := end - start + 1
		if idx == count-1 {
			// From the final chunk on, TikTok may complete the post without our
			// ever hearing; a later attempt must poll rather than start over.
			if _, err := r.Sess.StampFinalizing(); err != nil {
				return Done{}, fail(CauseUnknown, true, "could not record the final upload, so nothing was sent")
			}
		}
		body := &progressReader{r: io.NewSectionReader(f, start, n), of: total, done: start, fn: r.Rep.Progress}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, body)
		if err != nil {
			return Done{}, fail(CauseUnknown, true, "the stored TikTok upload address is unusable")
		}
		req.ContentLength = n
		req.Header.Set("Content-Type", "video/mp4")
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		resp, err := t.client().Do(req)
		if err != nil {
			return Done{}, netFailure(err)
		}
		raw := readBody(resp)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
				// The upload address expired. Forget it; the next attempt asks
				// TikTok whether the publish id is alive before starting over.
				_ = r.Sess.Clear()
			}
			return Done{}, statusFailure(resp.StatusCode, oneLine(string(raw)))
		}
	}
	return t.poll(ctx, r, publishID)
}

func (t *TikTok) status(ctx context.Context, token, publishID string) (tkStatus, *Failure) {
	env, f := t.call(ctx, token, "/v2/post/publish/status/fetch/", map[string]string{"publish_id": publishID})
	if f != nil {
		return tkStatus{}, f
	}
	var st tkStatus
	if json.Unmarshal(env.Data, &st) != nil {
		return tkStatus{}, fail(CauseUnavailable, true, "TikTok's status answer was unreadable")
	}
	return st, nil
}

func (t *TikTok) poll(ctx context.Context, r Run, publishID string) (Done, error) {
	every, forDur := t.PollEvery, t.PollFor
	if every <= 0 {
		every = tikTokPollEvery
	}
	if forDur <= 0 {
		forDur = tikTokPollFor
	}
	deadline := time.Now().Add(forDur)
	for {
		st, f := t.status(ctx, r.Cred.AccessToken, publishID)
		if f != nil {
			return Done{}, f
		}
		switch st.Status {
		case "PUBLISH_COMPLETE":
			return t.done(publishID, st), nil
		case "FAILED":
			_ = r.Sess.SaveRemote("")
			_ = r.Sess.Clear()
			return Done{}, fail(CauseRefused, false, "TikTok could not publish the video: %s", firstNonEmpty(oneLine(st.FailReason), "no reason given"))
		}
		if time.Now().After(deadline) {
			return Done{}, fail(CauseUnavailable, true, "TikTok is still processing the video; checking again shortly")
		}
		select {
		case <-ctx.Done():
			return Done{}, ctx.Err()
		case <-time.After(every):
		}
	}
}

func (t *TikTok) done(publishID string, st tkStatus) Done {
	id := publishID
	if len(st.PostIDs) > 0 {
		id = fmt.Sprintf("%d", st.PostIDs[0])
	}
	return Done{PostID: id, Privacy: TikTokPrivacy}
}
