package engine

import (
	"crypto/sha256"
	"net/http"
	"sort"
	"time"

	"github.com/rainmanjam/polyemesis/internal/captions"
)

// YOUTUBE HTTP POST CAPTIONS IN A DESTINATION'S PATH (#8).
//
// A YouTube destination whose caption mode is youtube_post gets one
// captions.PostQueue, keyed by destination id beside the SEI stages. The cue
// router releases each cue at the destination's delay, as it does for SEI, and
// the sink stamps it with the release instant and queues it: by then the video
// the cue belongs to is reaching YouTube, so the timestamp lines the two up.
//
// The queue needs the caption ingestion URL, which is a credential. It is read
// from the store (sealed, see internal/db/caption_ingest.go) on the reconcile,
// held only inside the poster, and never placed in intentSig, a destination
// spec, a log line, an event or an error. A changed URL replaces the queue; the
// URL's identity is compared by hash so even the comparison keeps no copy.
// Unlike an SEI stage, nothing here touches the destination's FFmpeg: the video
// path is unchanged, so turning POST captions on or off never restarts it.

const (
	// captionPostQueueSize bounds what one destination holds while its ingest is
	// slow. Cues arrive every few seconds, so this is about a minute of text.
	captionPostQueueSize = 16
	// captionPostTimeout bounds one POST. A cue posted later than this is stale.
	captionPostTimeout = 10 * time.Second
)

// destPoster is one destination's running caption queue.
type destPoster struct {
	queue   *captions.PostQueue
	urlHash [32]byte
}

// defaultCaptionPoster is the production poster: the real YouTube poster, with
// its https and ingest-host checks, on a client with its own timeout.
func defaultCaptionPoster(ingestURL string) captions.CaptionPoster {
	return captions.NewYouTubePoster(&http.Client{Timeout: captionPostTimeout}, ingestURL)
}

// syncCaptionPosters makes the running queues match want (destination id to
// ingestion URL). It starts what is missing, replaces a queue whose URL changed,
// and closes what is no longer wanted. Nothing is started once the engine has
// stopped.
func (e *Engine) syncCaptionPosters(want map[int64]string) {
	e.mu.RLock()
	stopped := e.stopped
	newPoster := e.captionPosterFor
	e.mu.RUnlock()
	if newPoster == nil {
		newPoster = defaultCaptionPoster
	}
	if stopped {
		want = nil
	}
	e.captPostMu.Lock()
	defer e.captPostMu.Unlock()
	if e.captPosts == nil {
		e.captPosts = map[int64]*destPoster{}
	}
	for id, cur := range e.captPosts {
		u, ok := want[id]
		if ok && sha256.Sum256([]byte(u)) == cur.urlHash {
			continue
		}
		e.postQueues.CompareAndDelete(id, cur.queue)
		cur.queue.Close()
		delete(e.captPosts, id)
	}
	ids := make([]int64, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if _, ok := e.captPosts[id]; ok {
			continue
		}
		destID := id
		q := captions.NewPostQueue(newPoster(want[id]), captionPostQueueSize, captionPostTimeout,
			func(err error) { e.noteCaptionPostFailure(destID, err) })
		e.captPosts[id] = &destPoster{queue: q, urlHash: sha256.Sum256([]byte(want[id]))}
		e.postQueues.Store(id, q)
	}
}

// noteCaptionPostFailure logs a refused POST, at most once a minute per
// destination. The error is the poster's, which never carries the URL.
func (e *Engine) noteCaptionPostFailure(id int64, err error) {
	now := time.Now()
	if v, ok := e.captPostWarned.Load(id); ok && now.Sub(v.(time.Time)) < time.Minute {
		return
	}
	e.captPostWarned.Store(id, now)
	e.log.Warn("live captions: YouTube did not accept a caption POST", "destination", id, "err", err)
}

// closeCaptionPosters stops every queue. It is the engine's Stop path.
func (e *Engine) closeCaptionPosters() {
	e.captPostMu.Lock()
	defer e.captPostMu.Unlock()
	for id, cur := range e.captPosts {
		e.postQueues.CompareAndDelete(id, cur.queue)
		cur.queue.Close()
		delete(e.captPosts, id)
	}
}
