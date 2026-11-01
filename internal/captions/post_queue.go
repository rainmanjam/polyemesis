package captions

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// THE YOUTUBE CAPTION POST PATH, PER DESTINATION (#8).
//
// The cue router calls its sink from timers, so delivery must never block: a
// slow or unreachable ingest would otherwise hold up every other destination's
// captions. Each destination that posts captions gets one PostQueue: a bounded
// queue and one worker that owns the sequence number and sends one cue per POST
// with its own timeout. When the queue is full the OLDEST cue is dropped and
// counted. A caption shown late is worse than one not shown, so the newest text
// is the one kept.

// CaptionPoster is what a PostQueue sends through. YouTubePoster is the
// production one; a test passes its own.
type CaptionPoster interface {
	Post(ctx context.Context, seq int, body string) error
}

// YouTubeCaptionBody is the request body for one cue, and the ONE place its
// format is decided.
//
// PROVISIONAL (docs-youtube C8). No Google page documents the body, the seq
// parameter, the timestamp line or the region syntax. This follows the
// third-party description (the lcyt client, support.streamtext.net): a UTC
// timestamp line "YYYY-MM-DDTHH:MM:SS.mmm", the caption text, and a trailing
// newline, with the timestamp within about a minute of YouTube's clock. A live
// check on a test channel confirms or replaces it, and only this function changes.
// Line breaks inside the text are folded to spaces so a cue cannot add a line the
// ingest would read as a second timestamp.
func YouTubeCaptionBody(at time.Time, text string) string {
	text = strings.Join(strings.Fields(text), " ")
	return at.UTC().Format("2006-01-02T15:04:05.000") + "\n" + text + "\n"
}

type queuedCue struct {
	at   time.Time
	text string
}

// PostQueue is one destination's bounded caption queue and its worker.
type PostQueue struct {
	poster  CaptionPoster
	timeout time.Duration
	onErr   func(error)
	ch      chan queuedCue
	// enqueueMu makes drop-oldest-then-send one step, so two timers cannot both
	// drop a cue to make room for one.
	enqueueMu sync.Mutex
	dropped   atomic.Int64
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

// NewPostQueue starts a queue of at most capacity cues. Each POST gets timeout.
// onErr, when set, is told about a failed POST; the errors YouTubePoster returns
// never carry the URL.
func NewPostQueue(p CaptionPoster, capacity int, timeout time.Duration, onErr func(error)) *PostQueue {
	if capacity < 1 {
		capacity = 1
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &PostQueue{
		poster: p, timeout: timeout, onErr: onErr,
		ch: make(chan queuedCue, capacity), cancel: cancel, done: make(chan struct{}),
	}
	go q.run(ctx)
	return q
}

// Enqueue queues one cue stamped at, and never blocks. A full queue drops its
// oldest cue first. After Close it does nothing.
func (q *PostQueue) Enqueue(at time.Time, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	select {
	case <-q.done:
		return
	default:
	}
	q.enqueueMu.Lock()
	defer q.enqueueMu.Unlock()
	c := queuedCue{at: at, text: text}
	for {
		select {
		case q.ch <- c:
			return
		default:
		}
		select {
		case <-q.ch:
			q.dropped.Add(1)
		default:
		}
	}
}

// Dropped is how many cues were dropped because the queue was full.
func (q *PostQueue) Dropped() int64 { return q.dropped.Load() }

// Close stops the worker and waits for it. A POST in flight is cancelled, and
// queued cues are discarded. Safe to call more than once.
func (q *PostQueue) Close() {
	q.closeOnce.Do(q.cancel)
	<-q.done
}

func (q *PostQueue) run(ctx context.Context) {
	defer close(q.done)
	seq := 0
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-q.ch:
			// The sequence rises once per POST sent, whatever the answer, so the
			// ingest never sees a number twice.
			seq++
			pctx, cancel := context.WithTimeout(ctx, q.timeout)
			err := q.poster.Post(pctx, seq, YouTubeCaptionBody(c.at, c.text))
			cancel()
			if err != nil && q.onErr != nil && ctx.Err() == nil {
				q.onErr(err)
			}
		}
	}
}
