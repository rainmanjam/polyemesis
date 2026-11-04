package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// YouTube publishes a clip as a YouTube Short with the Data API's resumable
// upload (https://developers.google.com/youtube/v3/guides/using_resumable_upload_protocol).
//
// There is no "Shorts" endpoint: a Short is an ordinary upload that is vertical
// and under three minutes, which the export already is. Nothing is added to the
// words the person confirmed; the form is prefilled with a Shorts tag they can
// see and remove.
//
// WHY THIS CANNOT DOUBLE-POST. The session URI YouTube returns is stored (sealed)
// BEFORE the first byte is sent. A retry never opens a second session while that
// one is alive: it asks the session what it holds with an empty PUT and
// "Content-Range: bytes */N". 200/201 means the upload finished and the body is
// the video, which is recorded without sending anything; 308 reports how far it
// got and the upload continues from there; 404/410 means the session is gone and
// only then is a new one opened.
type YouTube struct {
	// Base is the API origin; tests aim it at a fake server.
	Base   string
	Client *http.Client
}

// AcceptedYouTubeScopes are the scopes videos.insert documents as sufficient:
// https://developers.google.com/youtube/v3/docs/videos/insert. The existing
// "youtube" scope is one of them, which is why an account connected before this
// feature does not need reconnecting.
var AcceptedYouTubeScopes = []string{
	"https://www.googleapis.com/auth/youtube.upload",
	"https://www.googleapis.com/auth/youtube",
	"https://www.googleapis.com/auth/youtube.force-ssl",
	"https://www.googleapis.com/auth/youtubepartner",
}

const (
	youTubeBase = "https://www.googleapis.com"
	// youTubeUploadCost is videos.insert in quota units, from the same daily
	// allowance live chat polling spends.
	youTubeUploadCost = 1600
)

func (y *YouTube) base() string {
	if y.Base != "" {
		return strings.TrimRight(y.Base, "/")
	}
	return youTubeBase
}

func (y *YouTube) client() *http.Client {
	if y.Client != nil {
		return y.Client
	}
	return defaultClient()
}

// HasAnyScope reports whether a stored grant includes one of want. An empty
// grant means the platform did not report one, which is no verdict.
func HasAnyScope(granted string, want []string) bool {
	fields := strings.FieldsFunc(granted, func(r rune) bool { return r == ' ' || r == ',' || r == '+' })
	if len(fields) == 0 {
		return true
	}
	for _, f := range fields {
		for _, w := range want {
			if f == w {
				return true
			}
		}
	}
	return false
}

// Ready implements Adapter.
func (y *YouTube) Ready(c Credential) *Failure {
	if !HasAnyScope(c.Scopes, AcceptedYouTubeScopes) {
		return fail(CauseNeedsReconnect, false, "this YouTube connection was not granted permission to upload; reconnect it to allow uploads")
	}
	return nil
}

type ytVideo struct {
	ID     string `json:"id"`
	Status struct {
		PrivacyStatus string `json:"privacyStatus"`
		UploadStatus  string `json:"uploadStatus"`
	} `json:"status"`
}

type ytError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	} `json:"error"`
}

func ytFailure(code int, body []byte) *Failure {
	var e ytError
	_ = json.Unmarshal(body, &e)
	reason := ""
	if len(e.Error.Errors) > 0 {
		reason = e.Error.Errors[0].Reason
	}
	msg := oneLine(e.Error.Message)
	switch reason {
	// THREE DIFFERENT LIMITS, which used to share the daily-quota answer
	// (poka-yoke audit 2026-11-04). A short-window rateLimitExceeded was
	// reported as "used up until midnight Pacific" and never retried, so a
	// clip that a minute's backoff would have published was dropped.
	case "quotaExceeded", "dailyLimitExceeded":
		return fail(CauseQuota, false, "YouTube's daily allowance is used up (%s). It resets at midnight Pacific; publish again then", reason)
	case "rateLimitExceeded", "userRateLimitExceeded":
		return fail(CauseRateLimited, true, "YouTube asked to slow down (%s); this will be retried", reason)
	case "uploadLimitExceeded":
		return fail(CauseUploadLimited, false, "this channel has reached YouTube's limit on how many videos it may upload for now (%s). "+
			"It is a per-channel limit, not the API allowance; publish again later", reason)
	case "insufficientPermissions", "forbidden":
		if code == http.StatusForbidden {
			return fail(CauseNeedsReconnect, false, "YouTube says this connection may not upload; reconnect it to allow uploads")
		}
	case "youtubeSignupRequired":
		return fail(CauseRefused, false, "this Google account has no YouTube channel to upload to")
	}
	if code == http.StatusBadRequest && strings.Contains(strings.ToLower(msg), "video") {
		return fail(CauseClipRejected, false, "YouTube rejected the video: %s", msg)
	}
	return statusFailure(code, msg)
}

// metaHash binds an upload session to the words AND the file it was opened for.
func metaHash(m Meta, f File) string {
	b, _ := json.Marshal([]any{m, f.Size, f.ModTime})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Publish implements Adapter.
func (y *YouTube) Publish(ctx context.Context, r Run) (Done, error) {
	total := r.File.Size
	if total <= 0 {
		return Done{}, fail(CauseClipMissing, false, "the exported clip is empty")
	}
	f, err := os.Open(r.File.Path)
	if err != nil {
		return Done{}, fail(CauseClipMissing, false, "the exported clip could not be opened")
	}
	defer f.Close()

	want := metaHash(r.Meta, r.File)
	session, have, gerr := r.Sess.Get()
	if gerr != nil {
		// A failed read is not "no session": opening a second upload next to a
		// live one is the double post this ledger exists to prevent.
		return Done{}, fail(CauseUnknown, true, "could not read the stored upload session, so nothing was sent")
	}
	offset := int64(0)
	if session != "" {
		st, perr := y.probe(ctx, r.Cred.AccessToken, session, total)
		switch {
		case perr != nil:
			return Done{}, perr
		case st.done != nil:
			// Finished. Even if it was opened for earlier words, it is THE
			// upload for this clip: adopting it is what stops a duplicate.
			if have != want {
				r.Rep.Logf("the upload that finished was opened with the earlier title and description")
			}
			return y.done(*st.done, r), nil
		case st.gone:
			if !r.Pub.FinalizingAt.IsZero() {
				// Bytes may already have reached YouTube and the answer was lost; a
				// gone session cannot say. Opening a new one could be a second video.
				return Done{}, fail(CauseOutcomeUnknown, false,
					"the upload may or may not have reached YouTube and its session is gone. Nothing was sent again; check YouTube Studio before publishing again")
			}
			if err := r.Sess.Clear(); err != nil {
				return Done{}, err
			}
			session = ""
		case st.offset >= total:
			// Every byte is in and YouTube has not answered yet: wait for it,
			// whatever the words say now.
			return Done{}, fail(CauseUnavailable, true, "YouTube has every byte and is still finishing the upload")
		case have != want:
			// Opened for different words or a different file, with bytes still
			// to send: it holds nothing final, so a fresh one is allowed.
			if err := r.Sess.Clear(); err != nil {
				return Done{}, err
			}
			session = ""
		default:
			offset = st.offset
			r.Rep.Logf("resuming the upload at byte %d of %d", offset, total)
		}
	}
	if session == "" {
		session, err = y.open(ctx, r, total)
		if err != nil {
			return Done{}, err
		}
		if err := r.Sess.Save(session, want); err != nil {
			// Without a stored session a retry could open a second one after
			// this one finishes. Refuse to send bytes at all.
			return Done{}, fail(CauseUnknown, true, "could not record the upload session, so nothing was sent")
		}
	}
	// Intent first: from here bytes may reach YouTube, and a session that has
	// vanished by the next attempt can no longer be trusted to say whether they did.
	if _, err := r.Sess.StampFinalizing(); err != nil {
		return Done{}, fail(CauseUnknown, true, "could not record the upload attempt, so nothing was sent")
	}
	return y.send(ctx, r, f, session, offset, total)
}

type ytProbe struct {
	done   *ytVideo
	offset int64
	gone   bool
}

func (y *YouTube) probe(ctx context.Context, token, session string, total int64) (ytProbe, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, session, nil)
	if err != nil {
		return ytProbe{}, fail(CauseUnknown, true, "the stored upload session is unusable")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Length", "0")
	req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", total))
	resp, err := y.client().Do(req)
	if err != nil {
		return ytProbe{}, netFailure(err)
	}
	defer resp.Body.Close()
	body := readBody(resp)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var v ytVideo
		if json.Unmarshal(body, &v) != nil || v.ID == "" {
			return ytProbe{}, fail(CauseUnknown, true, "YouTube reported the upload finished but returned no video id")
		}
		return ytProbe{done: &v}, nil
	case http.StatusPermanentRedirect: // 308: resume incomplete
		return ytProbe{offset: rangeEnd(resp.Header.Get("Range"))}, nil
	case http.StatusNotFound, http.StatusGone:
		return ytProbe{gone: true}, nil
	}
	return ytProbe{}, ytFailure(resp.StatusCode, body)
}

// rangeEnd reads "bytes=0-999999" as the next byte to send, 1000000. No Range
// header means nothing has been received.
func rangeEnd(h string) int64 {
	h = strings.TrimSpace(h)
	i := strings.LastIndex(h, "-")
	if h == "" || i < 0 {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(h[i+1:]), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n + 1
}

func (y *YouTube) open(ctx context.Context, r Run, total int64) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"snippet": map[string]any{
			"title": r.Meta.Title, "description": r.Meta.Description, "tags": r.Meta.Tags,
			// 22 is "People & Blogs", the category the API's own samples use.
			"categoryId": "22",
		},
		"status": map[string]any{"privacyStatus": r.Meta.Privacy},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		y.base()+"/upload/youtube/v3/videos?uploadType=resumable&part=snippet,status", strings.NewReader(string(body)))
	if err != nil {
		return "", fail(CauseUnknown, false, "could not build the upload request")
	}
	req.Header.Set("Authorization", "Bearer "+r.Cred.AccessToken)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(total, 10))
	req.Header.Set("X-Upload-Content-Type", "video/mp4")
	// Charged here, because videos.insert is billed when the session opens.
	oauth.ChargeYouTubeQuota(youTubeUploadCost)
	resp, err := y.client().Do(req)
	if err != nil {
		return "", netFailure(err)
	}
	defer resp.Body.Close()
	b := readBody(resp)
	if resp.StatusCode != http.StatusOK {
		return "", ytFailure(resp.StatusCode, b)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fail(CauseUnavailable, true, "YouTube did not return an upload session")
	}
	return loc, nil
}

func (y *YouTube) send(ctx context.Context, r Run, f *os.File, session string, offset, total int64) (Done, error) {
	if offset >= total {
		// Every byte is already there; ask the session for the verdict.
		st, err := y.probe(ctx, r.Cred.AccessToken, session, total)
		if err != nil {
			return Done{}, err
		}
		if st.done != nil {
			return y.done(*st.done, r), nil
		}
		return Done{}, fail(CauseUnavailable, true, "YouTube has every byte but has not finished the upload yet")
	}
	remaining := total - offset
	body := &progressReader{r: io.NewSectionReader(f, offset, remaining), of: total, done: offset, fn: r.Rep.Progress}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, session, body)
	if err != nil {
		return Done{}, fail(CauseUnknown, true, "the stored upload session is unusable")
	}
	req.ContentLength = remaining
	req.Header.Set("Authorization", "Bearer "+r.Cred.AccessToken)
	req.Header.Set("Content-Type", "video/mp4")
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, total-1, total))
	resp, err := y.client().Do(req)
	if err != nil {
		return Done{}, netFailure(err)
	}
	defer resp.Body.Close()
	b := readBody(resp)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var v ytVideo
		if json.Unmarshal(b, &v) != nil || v.ID == "" {
			// The bytes are in. Do not guess: the session knows.
			return Done{}, fail(CauseUnknown, true, "YouTube accepted the upload but the response was unreadable")
		}
		return y.done(v, r), nil
	case http.StatusPermanentRedirect:
		return Done{}, fail(CauseUnavailable, true, "the upload stopped part way; it will resume")
	}
	return Done{}, ytFailure(resp.StatusCode, b)
}

func (y *YouTube) done(v ytVideo, r Run) Done {
	url := "https://www.youtube.com/watch?v=" + v.ID
	if r.File.Vertical {
		url = "https://www.youtube.com/shorts/" + v.ID
	}
	return Done{PostID: v.ID, URL: url, Privacy: v.Status.PrivacyStatus}
}
