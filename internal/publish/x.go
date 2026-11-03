package publish

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// X publishes a clip as a post with a video, through X's v2 chunked media upload
// (initialize, append, finalize, status) and POST /2/tweets.
//
// IT IS BUILT AND OFF. Worker and API both refuse it unless the operator has set
// POLYEMESIS_X_PUBLISH (Gates.XPublish); with the gate closed this adapter is
// never constructed with a token and sends no request at all. It has not been
// run against the live API: the endpoint shapes are from docs.x.com's media
// upload reference and the first live run is a manual step (see the PR).
//
// THE ONE STEP THAT CANNOT BE ASKED ABOUT is creating the post: if the response
// is lost, X offers no way to ask whether it landed. So intent is stamped in the
// ledger before the call, and a retry that finds the stamp does NOT call again;
// it fails with CauseOutcomeUnknown and tells the human to check the account.
type X struct {
	// Enabled is the operator's switch (POLYEMESIS_X_PUBLISH), checked here as
	// well as by the API and the worker: an adapter that is switched off sends
	// nothing even if something reaches it.
	Enabled   bool
	Base      string
	Client    *http.Client
	PollEvery time.Duration
}

const xBase = "https://api.x.com"

// XScopes are what posting a video needs beyond sign-in. The existing grant
// asks for neither, so enabling this later needs an X scope bump and a
// reconnect; Ready reports that rather than letting X's 403 say it.
var XScopes = []string{"tweet.write", "media.write"}

func (x *X) base() string {
	if x.Base != "" {
		return strings.TrimRight(x.Base, "/")
	}
	return xBase
}

func (x *X) client() *http.Client {
	if x.Client != nil {
		return x.Client
	}
	return defaultClient()
}

// Ready implements Adapter. Unlike the others an empty grant is not accepted:
// X reports the scopes it granted, and these two are never in the sign-in set.
func (x *X) Ready(c Credential) *Failure {
	have := map[string]bool{}
	for _, f := range strings.FieldsFunc(c.Scopes, func(r rune) bool { return r == ' ' || r == ',' }) {
		have[f] = true
	}
	for _, s := range XScopes {
		if !have[s] {
			return fail(CauseNeedsReconnect, false, "this X connection was not granted permission to post; reconnect it to allow uploads")
		}
	}
	return nil
}

func (x *X) do(ctx context.Context, method, path string, token string, body any, out any) *Failure {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, x.base()+path, rdr)
	if err != nil {
		return fail(CauseUnknown, false, "could not build the request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := x.client().Do(req)
	if err != nil {
		return netFailure(err)
	}
	defer resp.Body.Close()
	raw := readBody(resp)
	if resp.StatusCode/100 != 2 {
		var e struct {
			Detail string `json:"detail"`
			Title  string `json:"title"`
		}
		_ = json.Unmarshal(raw, &e)
		if resp.StatusCode == http.StatusForbidden {
			return fail(CauseRefused, false, "X refused: %s", firstNonEmpty(oneLine(e.Detail), oneLine(e.Title)))
		}
		return statusFailure(resp.StatusCode, firstNonEmpty(oneLine(e.Detail), oneLine(e.Title)))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			// A 2xx whose body does not parse is not an answer: it must not read as
			// an empty one (which could mean "ready").
			return fail(CauseUnavailable, true, "X answered with something unreadable")
		}
	}
	return nil
}

// Publish implements Adapter.
func (x *X) Publish(ctx context.Context, r Run) (Done, error) {
	if !x.Enabled {
		return Done{}, fail(CauseGated, false, "%s", XGateCopy)
	}
	total := r.File.Size
	if total <= 0 {
		return Done{}, fail(CauseClipMissing, false, "the exported clip is empty")
	}
	// The remote id records how far the media got: "<id>" is initialised, and
	// "<id>:f" has had every segment appended and been finalized. A retry after
	// an upload error continues from the phase it reached; it never skips a
	// phase it did not finish.
	mediaID, finalized := strings.CutSuffix(r.Sess.Remote, ":f")
	if mediaID == "" {
		var ini struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if f := x.do(ctx, http.MethodPost, "/2/media/upload/initialize", r.Cred.AccessToken, map[string]any{
			"media_type": "video/mp4", "total_bytes": total, "media_category": "tweet_video",
		}, &ini); f != nil {
			return Done{}, f
		}
		if ini.Data.ID == "" {
			return Done{}, fail(CauseUnavailable, true, "X did not return a media id")
		}
		mediaID = ini.Data.ID
		if err := r.Sess.SaveRemote(mediaID); err != nil {
			return Done{}, fail(CauseUnknown, true, "could not record the X media id, so nothing was sent")
		}
	}
	processing := ""
	if !finalized {
		if f := x.append(ctx, r, mediaID, total); f != nil {
			return Done{}, x.forgetIfDead(r, f)
		}
		var fin struct {
			Data struct {
				Processing *struct {
					State string `json:"state"`
				} `json:"processing_info"`
			} `json:"data"`
		}
		if f := x.do(ctx, http.MethodPost, "/2/media/upload/"+mediaID+"/finalize", r.Cred.AccessToken, nil, &fin); f != nil {
			return Done{}, x.forgetIfDead(r, f)
		}
		if err := r.Sess.SaveRemote(mediaID + ":f"); err != nil {
			return Done{}, fail(CauseUnknown, true, "could not record the finalized X media, so nothing more was sent")
		}
		if fin.Data.Processing != nil {
			processing = fin.Data.Processing.State
		}
	}
	// A finalize that reports no processing_info means the media is ready; a
	// STATUS read that reports none does not, and is waited on.
	if finalized || processing != "" {
		if f := x.waitProcessed(ctx, r, mediaID); f != nil {
			return Done{}, f
		}
	}

	first, err := r.Sess.StampFinalizing()
	if err != nil {
		return Done{}, fail(CauseUnknown, true, "could not record the intent to post, so nothing was sent")
	}
	if !first {
		return Done{}, fail(CauseOutcomeUnknown, false,
			"an earlier attempt may already have created the X post and no answer came back. Nothing was sent again; check the X account before publishing again")
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if f := x.do(ctx, http.MethodPost, "/2/tweets", r.Cred.AccessToken, map[string]any{
		"text": xTextFor(r.Meta), "media": map[string]any{"media_ids": []string{mediaID}},
	}, &created); f != nil {
		// The call may have landed even though the answer was an error or never
		// arrived; it is not retried.
		if f.Retry {
			f = fail(CauseOutcomeUnknown, false, "X did not confirm the post (%s). Nothing was sent again; check the X account before publishing again", f.Detail)
		}
		return Done{}, f
	}
	if created.Data.ID == "" {
		return Done{}, fail(CauseOutcomeUnknown, false, "X accepted the post but returned no id; check the X account before publishing again")
	}
	return Done{PostID: created.Data.ID, URL: "https://x.com/i/status/" + created.Data.ID, Privacy: PrivacyPublic}, nil
}

func (x *X) append(ctx context.Context, r Run, mediaID string, total int64) *Failure {
	f, err := os.Open(r.File.Path)
	if err != nil {
		return fail(CauseClipMissing, false, "the exported clip could not be opened")
	}
	defer f.Close()
	const seg = 4 << 20
	buf := make([]byte, seg)
	for idx, off := 0, int64(0); off < total; idx++ {
		n, err := io.ReadFull(io.NewSectionReader(f, off, seg), buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return fail(CauseClipMissing, false, "the exported clip could not be read")
		}
		if n == 0 {
			break
		}
		if fl := x.do(ctx, http.MethodPost, fmt.Sprintf("/2/media/upload/%s/append", mediaID), r.Cred.AccessToken,
			map[string]any{"media": base64.StdEncoding.EncodeToString(buf[:n]), "segment_index": idx}, nil); fl != nil {
			return fl
		}
		off += int64(n)
		r.Rep.Progress(float64(off) / float64(total))
	}
	return nil
}

func (x *X) waitProcessed(ctx context.Context, r Run, mediaID string) *Failure {
	every := x.PollEvery
	if every <= 0 {
		every = 2 * time.Second
	}
	for i := 0; i < 90; i++ {
		var st struct {
			Data struct {
				Processing *struct {
					State string `json:"state"`
				} `json:"processing_info"`
			} `json:"data"`
		}
		if f := x.do(ctx, http.MethodGet, "/2/media/upload?command=STATUS&media_id="+mediaID, r.Cred.AccessToken, nil, &st); f != nil {
			return f
		}
		if st.Data.Processing == nil {
			return fail(CauseUnavailable, true, "X has not reported the video's processing state yet")
		}
		switch st.Data.Processing.State {
		case "succeeded":
			return nil
		case "failed":
			if err := r.Sess.SaveRemote(""); err != nil {
				return fail(CauseUnknown, true, "could not forget the failed X media")
			}
			return fail(CauseClipRejected, false, "X could not process the video")
		}
		select {
		case <-ctx.Done():
			return fail(CauseUnavailable, true, "interrupted while X processed the video")
		case <-time.After(every):
		}
	}
	return fail(CauseUnavailable, true, "X is still processing the video; checking again shortly")
}

// forgetIfDead drops a media id that X has refused for good (an expired or
// rejected upload), so the next attempt starts a fresh one instead of wedging on
// it. A transient failure keeps the id and the phase it reached.
func (x *X) forgetIfDead(r Run, f *Failure) error {
	if !f.Retry {
		if err := r.Sess.SaveRemote(""); err != nil {
			return err
		}
	}
	return f
}
