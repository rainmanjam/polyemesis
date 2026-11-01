package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/secrets"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// THE ENGINE'S YOUTUBE CAPTION POST PATH (#8). A youtube_post destination with
// a stored caption URL gets its own queue, the sink posts each released cue to
// it, and the URL -- a signed credential -- is in no signature, log line or
// event.

type fakeCaptionPoster struct {
	mu     sync.Mutex
	url    string
	bodies []string
	err    error
}

func (f *fakeCaptionPoster) Post(_ context.Context, _ int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, body)
	return f.err
}

func (f *fakeCaptionPoster) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

// postLog is a log sink the queue's worker writes to while the test reads it.
type postLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *postLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *postLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func plantedCaptionURL(tag string) string {
	return "https://upload.youtube.com/closedcaption?cid=" + "planted-" + tag
}

// postEngine is failoverEngine on a store with a secret box (the caption URL is
// stored sealed or not at all), a captured log, and a fake poster factory.
func postEngine(t *testing.T) (*Engine, *postLog, map[string]*fakeCaptionPoster, *sync.Mutex) {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := dbtest.OpenAt(t, filepath.Join(t.TempDir(), "test.db"), db.WithSecretBox(box))
	src, err := store.DefaultSourceID()
	if err != nil {
		t.Fatal(err)
	}
	buf := &postLog{}
	var mu sync.Mutex
	posters := map[string]*fakeCaptionPoster{}
	e := &Engine{log: slog.New(slog.NewTextHandler(buf, nil)), store: store, sourceID: src, bus: events.NewBroker()}
	e.captionPosterFor = func(u string) captions.CaptionPoster {
		mu.Lock()
		defer mu.Unlock()
		p := &fakeCaptionPoster{url: u}
		posters[u] = p
		return p
	}
	t.Cleanup(e.closeCaptionPosters)
	return e, buf, posters, &mu
}

func postDest(t *testing.T, e *Engine, name, key string) *db.Destination {
	t.Helper()
	d, err := e.store.CreateDestination(&db.Destination{
		Name: name, Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://127.0.0.1:1/live2", StreamKey: key, AudioBitrate: 128,
		Profile: routing.DefaultProfile(), CaptionMode: "youtube_post", DelaySeconds: 30,
		SourceID: &e.sourceID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return d
}

// TestAPOSTCaptionDestinationIsATargetOnlyWithItsURLAndTheURLIsNotInTheSignature.
//
// MUTATION: the URL check in captionIntentNow removed (every youtube_post row a
// target). Observed: FAIL, the destination without a URL was a target.
func TestAPOSTCaptionDestinationIsATargetOnlyWithItsURLAndTheURLIsNotInTheSignature(t *testing.T) {
	e, _, _, _ := postEngine(t)
	with := postDest(t, e, "with-url", "key-a")
	if err := e.store.PutCaptionIngestURL(with.ID, "key-a", plantedCaptionURL("a")); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetDestinationEnabled(with.ID, true); err != nil {
		t.Fatal(err)
	}
	without := postDest(t, e, "no-url", "key-b")
	// Enabled through a URL that is then lost: the mid-show case.
	if err := e.store.PutCaptionIngestURL(without.ID, "key-b", plantedCaptionURL("b")); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetDestinationEnabled(without.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.store.DeleteCaptionIngestURL(without.ID); err != nil {
		t.Fatal(err)
	}

	in, err := e.captionIntentNow()
	if err != nil {
		t.Fatal(err)
	}
	if len(in.targets) != 1 || in.targets[0].ID != with.ID || in.targets[0].Delay != 30*time.Second {
		t.Fatalf("targets = %+v, want only the destination with a URL, at its 30s delay", in.targets)
	}
	if in.posts[with.ID] != plantedCaptionURL("a") || len(in.posts) != 1 {
		t.Fatalf("posts carry %d URLs, want exactly the stored one", len(in.posts))
	}
	if strings.Contains(in.sig, "planted") || strings.Contains(in.sig, "upload.youtube.com") {
		t.Fatalf("the caption URL is in the intent signature: %q", in.sig)
	}
}

// TestACueReleasedToAPOSTDestinationIsPostedToItsOwnURL.
//
// MUTATION: the postQueues branch in captionSink.Deliver removed. Observed:
// FAIL, nothing was posted.
func TestACueReleasedToAPOSTDestinationIsPostedToItsOwnURL(t *testing.T) {
	e, _, posters, mu := postEngine(t)
	e.syncCaptionPosters(map[int64]string{4: plantedCaptionURL("four"), 5: plantedCaptionURL("five")})

	cue := transcribe.LiveCaption{}
	cue.Text, cue.StartMS, cue.EndMS = "HELLO WORLD", 0, 2000
	captionSink{e: e}.Deliver(4, cue)

	mu.Lock()
	four, five := posters[plantedCaptionURL("four")], posters[plantedCaptionURL("five")]
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for len(four.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := four.got()
	if len(got) != 1 || !strings.HasSuffix(got[0], "\nHELLO WORLD\n") {
		t.Fatalf("destination 4 posted %q, want the cue once", got)
	}
	if len(five.got()) != 0 {
		t.Fatalf("a cue for destination 4 was posted to destination 5")
	}
}

// TestPOSTQueuesFollowTheURLsAndCloseWhenNoLongerWanted.
func TestPOSTQueuesFollowTheURLsAndCloseWhenNoLongerWanted(t *testing.T) {
	e, _, posters, mu := postEngine(t)
	e.syncCaptionPosters(map[int64]string{4: plantedCaptionURL("old")})
	first, _ := e.postQueues.Load(int64(4))
	e.syncCaptionPosters(map[int64]string{4: plantedCaptionURL("old")})
	same, _ := e.postQueues.Load(int64(4))
	if first != same {
		t.Fatalf("an unchanged URL replaced the queue")
	}
	e.syncCaptionPosters(map[int64]string{4: plantedCaptionURL("new")})
	replaced, _ := e.postQueues.Load(int64(4))
	if replaced == first {
		t.Fatalf("a changed URL kept the old queue")
	}
	mu.Lock()
	n := len(posters)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("posters built = %d, want 2", n)
	}
	e.syncCaptionPosters(nil)
	if _, ok := e.postQueues.Load(int64(4)); ok {
		t.Fatalf("a queue survived its destination leaving the intent")
	}
	// Stopped: nothing is started.
	e.mu.Lock()
	e.stopped = true
	e.mu.Unlock()
	e.syncCaptionPosters(map[int64]string{9: plantedCaptionURL("late")})
	if _, ok := e.postQueues.Load(int64(9)); ok {
		t.Fatalf("a queue was started on a stopped engine")
	}
}

// TestAFailedCaptionPOSTIsLoggedOnceAMinuteWithoutTheURL is the leak test: the
// planted URL appears in no log line and no event.
func TestAFailedCaptionPOSTIsLoggedOnceAMinuteWithoutTheURL(t *testing.T) {
	e, buf, _, _ := postEngine(t)
	sub := e.bus.Subscribe()
	defer sub.Close()
	e.captionPosterFor = func(u string) captions.CaptionPoster {
		return &fakeCaptionPoster{url: u, err: errors.New("caption POST: ingest answered 403")}
	}
	e.syncCaptionPosters(map[int64]string{4: plantedCaptionURL("leak")})
	cue := transcribe.LiveCaption{}
	cue.Text = "one"
	captionSink{e: e}.Deliver(4, cue)
	cue.Text = "two"
	captionSink{e: e}.Deliver(4, cue)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "did not accept") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	e.closeCaptionPosters()
	out := buf.String()
	if strings.Count(out, "did not accept a caption POST") != 1 {
		t.Fatalf("want exactly one warning, got log:\n%s", out)
	}
	if strings.Contains(out, "planted") || strings.Contains(out, "closedcaption") {
		t.Fatalf("the caption URL reached the log:\n%s", out)
	}
	for {
		select {
		case ev := <-sub.C:
			raw, _ := json.Marshal(ev)
			if strings.Contains(string(raw), "planted") {
				t.Fatalf("the caption URL reached an event: %s", raw)
			}
		default:
			return
		}
	}
}
